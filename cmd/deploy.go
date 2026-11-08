package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type deploymentResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	RepoURL   string `json:"repoURL"`
	Branch    string `json:"branch"`
	Status    string `json:"status"`
	URL       string `json:"url"`
	Logs      string `json:"logs"`
	CreatedAt string `json:"createdAt"`
	// DeploymentType is "static" or "ssr" (container).
	DeploymentType string `json:"deploymentType"`
}

func newDeployCommand() *cobra.Command {
	var (
		dir        string
		repo       string
		branch     string
		name       string
		detach     bool
		forceNew   bool
		deployType string
	)

	command := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy the current project",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeploy(deployOptions{
				dir:        dir,
				repo:       repo,
				branch:     branch,
				name:       name,
				detach:     detach,
				forceNew:   forceNew,
				deployType: deployType,
			})
		},
	}

	command.Flags().StringVar(&dir, "dir", ".", "Directory to deploy")
	command.Flags().StringVar(&repo, "repo", "", "GitHub repo (owner/name or URL); defaults to git remote origin")
	command.Flags().StringVar(&branch, "branch", "", "Branch name metadata; defaults to current git branch")
	command.Flags().StringVar(&name, "name", "", "App name; defaults to repo name")
	command.Flags().BoolVar(&detach, "detach", false, "Exit after upload without waiting for build")
	command.Flags().BoolVar(&forceNew, "new", false, "Create a new deployment instead of updating trapiche.json")
	command.Flags().StringVar(&deployType, "type", deployTypeAuto, "Deployment type: auto, static, or ssr (container). Container apps must listen on port 3000")
	return command
}

type deployOptions struct {
	dir        string
	repo       string
	branch     string
	name       string
	detach     bool
	forceNew   bool
	deployType string
}

func runDeploy(opts deployOptions) error {
	fmt.Print(trapicheTitle)

	creds, err := loadCredentials()
	if err != nil {
		return err
	}

	absDir, err := filepath.Abs(opts.dir)
	if err != nil {
		return fmt.Errorf("failed to resolve directory: %w", err)
	}
	info, err := os.Stat(absDir)
	if err != nil {
		return fmt.Errorf("failed to read directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("path is not a directory: %s", absDir)
	}

	repoURL := strings.TrimSpace(opts.repo)
	if repoURL == "" {
		repoURL, err = gitOriginRemote(absDir)
		if err != nil {
			return err
		}
	} else {
		repoURL, err = normalizeRepoArg(repoURL)
		if err != nil {
			return err
		}
	}

	branch := strings.TrimSpace(opts.branch)
	if branch == "" {
		branch, err = gitCurrentBranch(absDir)
		if err != nil {
			return err
		}
	}

	appName := strings.TrimSpace(opts.name)
	if appName == "" {
		appName = repoNameFromURL(repoURL)
	}

	cfg, err := loadProjectConfig(absDir)
	if err != nil {
		return err
	}
	isUpdate := cfg != nil && !opts.forceNew

	requestedType, err := normalizeDeployType(opts.deployType)
	if err != nil {
		return err
	}
	detectedType, detectReason := detectDeployType(absDir)
	savedType := ""
	if isUpdate {
		savedType = cfg.DeploymentType
	}
	deployType, err := resolveDeployType(requestedType, detectedType, savedType, isUpdate)
	if err != nil {
		return err
	}
	fmt.Printf("Type: %s\n", describeDeployType(deployType, requestedType, detectedType, detectReason, isUpdate && savedType != ""))
	if isUpdate && savedType != "" && detectedType != "" && detectedType != savedType {
		fmt.Fprintf(os.Stderr, "warning: this folder looks like a %s (%s), but the linked deployment is a %s. Use --new to create a new deployment.\n",
			deployTypeLabel(detectedType), detectReason, deployTypeLabel(savedType))
	}
	if deployType != deployTypeStatic {
		if warning := dockerfilePortWarning(absDir); warning != "" {
			fmt.Fprintf(os.Stderr, "warning: %s\n", warning)
		}
	}
	if deployType == deployTypeSSR {
		if hint := containerPortHint(absDir); hint != "" {
			fmt.Fprintf(os.Stderr, "note: %s\n", hint)
		}
	}

	sp := newSpinner("Compressing...")
	sp.Start()
	archivePath, fileCount, err := createTarGz(absDir)
	if err != nil {
		sp.Fail("Compression failed")
		return err
	}
	defer os.Remove(archivePath)

	archiveInfo, _ := os.Stat(archivePath)
	sizeMB := float64(archiveInfo.Size()) / (1024 * 1024)
	sp.Stop(fmt.Sprintf("Compressed  %.1f MB · %d files", sizeMB, fileCount))

	sp2 := newSpinner("Uploading...")
	sp2.Start()

	var created *deploymentResponse
	if isUpdate {
		created, err = queueAuthenticatedDeployUpdate(creds, archivePath, cfg.DeploymentID, repoURL, branch, deployType)
		if err != nil {
			sp2.Fail("Upload failed")
			if isHTTPStatus(err, http.StatusNotFound) {
				return fmt.Errorf("%s\n\n%s", err.Error(), deploymentNotFoundHelp())
			}
			return err
		}
		sp2.Stop(fmt.Sprintf("Uploaded  redeploying %s", created.Name))
		fillDeploymentType(creds, created, deployType)
	} else {
		created, err = queueAuthenticatedDeploy(creds, archivePath, repoURL, appName, branch, deployType)
		if err != nil {
			sp2.Fail("Upload failed")
			return err
		}
		sp2.Stop(fmt.Sprintf("Uploaded  queued as %s", created.Name))
		fillDeploymentType(creds, created, deployType)

		if err := refreshProjectConfig(absDir, created, repoURL, branch); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to write %s: %v\n", projectConfigFile, err)
		} else if cfg == nil {
			fmt.Fprintf(os.Stderr, "\nTip: add %s to .gitignore to avoid committing deployment metadata.\n\n", projectConfigFile)
		}
	}

	if isUpdate {
		// The reply of an update can have no URL (a failed build clears it).
		// Saving it as it is would erase the URL in trapiche.json.
		keepSavedDeployment(created, cfg)
		if err := refreshProjectConfig(absDir, created, repoURL, branch); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to update %s: %v\n", projectConfigFile, err)
		}
	}

	if opts.detach {
		fmt.Printf("\n✓ Queued %s\n\n  %s\n\n  Run: trapiche logs %s\n", created.ID, created.URL, created.ID)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), deployWaitTimeout)
	defer cancel()

	final, waitErr := pollDeploymentUntilDone(ctx, creds, created.ID, created.URL, created.DeploymentType)

	// trapiche.json was written from the upload reply. That reply can have no URL
	// (a container has none yet) and no type (auto). Save the URL and type that
	// the server reports. A finished build that ends as "suspended" also has them.
	if final != nil && mergeFinalDeployment(created, final) {
		if err := refreshProjectConfig(absDir, created, repoURL, branch); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to update %s: %v\n", projectConfigFile, err)
		}
	}
	return waitErr
}

// deployWaitTimeout is how long the CLI waits for a build. The API stops a build
// after 15 minutes, so the CLI waits a little longer to see the result.
const deployWaitTimeout = 16 * time.Minute

// keepSavedDeployment fills the empty fields of an upload reply from trapiche.json.
func keepSavedDeployment(created *deploymentResponse, saved *projectConfig) {
	if created == nil || saved == nil {
		return
	}
	if created.Name == "" {
		created.Name = saved.Name
	}
	if created.URL == "" {
		created.URL = saved.URL
	}
	if created.DeploymentType == "" {
		created.DeploymentType = saved.DeploymentType
	}
}

// mergeFinalDeployment copies the URL and type of a finished deployment into
// created. It returns true if created changed. Empty values never replace a known one.
func mergeFinalDeployment(created, final *deploymentResponse) bool {
	if created == nil || final == nil {
		return false
	}
	changed := false
	if final.URL != "" && final.URL != created.URL {
		created.URL = final.URL
		changed = true
	}
	if final.DeploymentType != "" && final.DeploymentType != created.DeploymentType {
		created.DeploymentType = final.DeploymentType
		changed = true
	}
	return changed
}

const (
	uploadTimeout         = 15 * time.Minute
	uploadResponseTimeout = 2 * time.Minute
)

// uploadArchive sends the archive as multipart form data without loading
// the file into memory. Only the small multipart header is buffered.
// It returns the response body of a 202 reply.
func uploadArchive(creds *credentials, route string, fields [][2]string, archivePath string) ([]byte, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open archive: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to read archive: %w", err)
	}

	var head bytes.Buffer
	writer := multipart.NewWriter(&head)
	for _, f := range fields {
		if err := writer.WriteField(f[0], f[1]); err != nil {
			return nil, err
		}
	}
	if _, err := writer.CreateFormFile("archive", filepath.Base(archivePath)); err != nil {
		return nil, fmt.Errorf("failed to create multipart field: %w", err)
	}
	tail := fmt.Sprintf("\r\n--%s--\r\n", writer.Boundary())

	body := io.MultiReader(&head, file, strings.NewReader(tail))
	contentLength := int64(head.Len()) + info.Size() + int64(len(tail))

	ctx, cancel := context.WithTimeout(context.Background(), uploadTimeout)
	defer cancel()

	req, err := authRequest(creds, http.MethodPost, apiPath(route), body)
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)
	req.ContentLength = contentLength
	req.Header.Set("Content-Type", writer.FormDataContentType())

	var transport *http.Transport
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = base.Clone()
	} else {
		transport = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}
	transport.ResponseHeaderTimeout = uploadResponseTimeout
	client := &http.Client{Transport: transport}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to upload archive: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		return nil, apiError(resp.StatusCode, respBody)
	}
	return respBody, nil
}

// fillDeploymentType makes sure dep.DeploymentType is set after an upload.
// The server reply may leave it out. Then the type that the CLI sent is used.
// If the CLI sent no clear type (auto or none), the CLI asks the server.
// An empty type must not reach trapiche.json, because it would remove the type lock.
func fillDeploymentType(creds *credentials, dep *deploymentResponse, sent string) {
	if dep.DeploymentType != "" {
		return
	}
	if sent == deployTypeStatic || sent == deployTypeSSR {
		dep.DeploymentType = sent
		return
	}
	if got, err := getDeployment(creds, dep.ID); err == nil {
		dep.DeploymentType = got.DeploymentType
	}
}

// typeField returns the form field for the deployment type, or none when empty.
func typeField(deployType string) [][2]string {
	if deployType == "" {
		return nil
	}
	return [][2]string{{"deploymentType", deployType}}
}

func queueAuthenticatedDeploy(creds *credentials, archivePath, repoURL, appName, branch, deployType string) (*deploymentResponse, error) {
	fields := [][2]string{
		{"repoURL", repoURL},
		{"appName", appName},
		{"branch", branch},
	}
	respBody, err := uploadArchive(creds, "/api/deployments/upload", append(fields, typeField(deployType)...), archivePath)
	if err != nil {
		return nil, err
	}

	var created deploymentResponse
	if err := json.Unmarshal(respBody, &created); err != nil {
		return nil, fmt.Errorf("failed to parse deploy response: %w", err)
	}
	if created.ID == "" {
		return nil, fmt.Errorf("deploy response missing deployment id")
	}
	return &created, nil
}

func queueAuthenticatedDeployUpdate(creds *credentials, archivePath, deploymentID, repoURL, branch, deployType string) (*deploymentResponse, error) {
	path := fmt.Sprintf("/api/deployments/%s/upload", deploymentID)
	fields := [][2]string{
		{"repoURL", repoURL},
		{"branch", branch},
	}
	respBody, err := uploadArchive(creds, path, append(fields, typeField(deployType)...), archivePath)
	if err != nil {
		return nil, err
	}

	var updated deploymentResponse
	if err := json.Unmarshal(respBody, &updated); err != nil {
		return nil, fmt.Errorf("failed to parse deploy response: %w", err)
	}
	if updated.ID == "" {
		updated.ID = deploymentID
	}
	return &updated, nil
}

// followBuildLogs prints the build log of a container deployment until the build ends.
// The log stream endpoint shows runtime logs for containers, and it has no
// running container during the build. So this polls the deployment record.
func followBuildLogs(ctx context.Context, creds *credentials, deploymentID string) error {
	printed := 0
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		dep, err := getDeployment(creds, deploymentID)
		if err != nil {
			return err
		}
		printNewLogs(dep.Logs, &printed)
		if dep.Status != "queued" && dep.Status != "building" {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out while waiting for the build — it may still be running. Check with: trapiche logs %s", deploymentID)
		case <-ticker.C:
		}
	}
}

// pollDeploymentUntilDone waits for the build and prints the log. On success it
// returns the finished deployment. Its URL is never empty (defaultURL is the fallback).
func pollDeploymentUntilDone(ctx context.Context, creds *credentials, deploymentID, defaultURL, deployType string) (*deploymentResponse, error) {
	fmt.Println("\nBuilding...")
	fmt.Println(strings.Repeat("─", 40))

	// The log stream is only safe for a known static deployment. For a container
	// it shows runtime logs, and for an unknown type the CLI cannot tell. The
	// deployment record holds the build log for both types, so poll it instead.
	var followErr error
	if deployType == deployTypeStatic {
		followErr = streamDeploymentLogs(ctx, creds, deploymentID, false)
	} else {
		followErr = followBuildLogs(ctx, creds, deploymentID)
	}
	if followErr != nil {
		return nil, followErr
	}

	dep, err := getDeployment(creds, deploymentID)
	if err != nil {
		return nil, err
	}

	switch dep.Status {
	case "deployed":
		if dep.URL == "" {
			dep.URL = defaultURL
		}
		fmt.Printf("\n✓ Deployed!\n\n  %s\n", dep.URL)
		return dep, nil
	case "suspended":
		// The build finished and has a URL, but the account is suspended (billing).
		// Return the deployment so the caller keeps the URL.
		if dep.URL == "" {
			dep.URL = defaultURL
		}
		return dep, fmt.Errorf("the build finished, but the deployment is suspended (account billing) — check your plan in the dashboard")
	case "failed":
		return nil, fmt.Errorf("deployment failed")
	default:
		return nil, fmt.Errorf("deployment ended with status: %s", dep.Status)
	}
}

func getDeployment(creds *credentials, id string) (*deploymentResponse, error) {
	body, status, err := apiGet(creds, "/api/deployments/"+id)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, apiError(status, body)
	}
	var dep deploymentResponse
	if err := json.Unmarshal(body, &dep); err != nil {
		return nil, err
	}
	return &dep, nil
}

// Limits of `logs --no-follow`. The log stream of a running container never
// ends, so "print the current logs and exit" needs two limits:
//   - noFollowIdle: stop when no log line arrives for this long. The timer starts
//     when the server answers, not before, so a slow first byte is still printed.
//   - noFollowMax: stop after this long, even if lines keep arriving.
//
// They are variables so that tests can use short values.
var (
	noFollowIdle = 2 * time.Second
	noFollowMax  = 10 * time.Second
)

func streamDeploymentLogs(ctx context.Context, creds *credentials, deploymentID string, noFollow bool) error {
	url := apiPath("/api/deployments/" + deploymentID + "/logs/stream")
	req, err := authRequest(creds, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	var cancel context.CancelFunc
	if noFollow {
		ctx, cancel = context.WithTimeout(ctx, noFollowMax)
		defer cancel()
	}
	// stoppedByLimit is true when --no-follow ended the stream on its own limits.
	stoppedByLimit := func() bool {
		return noFollow && (errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded))
	}

	// Stop the stream when the caller's time limit ends, even if no line arrives.
	req = req.WithContext(ctx)

	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		if stoppedByLimit() {
			return nil
		}
		return fmt.Errorf("failed to stream logs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return apiError(resp.StatusCode, body)
	}

	// The idle timer starts now: the server has answered.
	var idle *time.Timer
	if noFollow {
		idle = time.AfterFunc(noFollowIdle, cancel)
		defer idle.Stop()
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			if stoppedByLimit() {
				return nil
			}
			return fmt.Errorf("timed out")
		default:
		}

		line := scanner.Text()
		if strings.HasPrefix(line, "event: close") {
			return nil
		}
		if strings.HasPrefix(line, "data: ") {
			fmt.Println(strings.TrimPrefix(line, "data: "))
			if idle != nil {
				idle.Reset(noFollowIdle)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if stoppedByLimit() {
			return nil
		}
		return err
	}
	return nil
}
