package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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
}

func newDeployCommand() *cobra.Command {
	var (
		dir      string
		repo     string
		branch   string
		name     string
		detach   bool
		forceNew bool
	)

	command := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy the current project",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeploy(deployOptions{
				dir:      dir,
				repo:     repo,
				branch:   branch,
				name:     name,
				detach:   detach,
				forceNew: forceNew,
			})
		},
	}

	command.Flags().StringVar(&dir, "dir", ".", "Directory to deploy")
	command.Flags().StringVar(&repo, "repo", "", "GitHub repo (owner/name or URL); defaults to git remote origin")
	command.Flags().StringVar(&branch, "branch", "", "Branch name metadata; defaults to current git branch")
	command.Flags().StringVar(&name, "name", "", "App name; defaults to repo name")
	command.Flags().BoolVar(&detach, "detach", false, "Exit after upload without waiting for build")
	command.Flags().BoolVar(&forceNew, "new", false, "Create a new deployment instead of updating trapiche.json")
	return command
}

type deployOptions struct {
	dir      string
	repo     string
	branch   string
	name     string
	detach   bool
	forceNew bool
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
		repoURL, err = gitOriginRemote()
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
		branch, err = gitCurrentBranch()
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
		created, err = queueAuthenticatedDeployUpdate(creds, archivePath, cfg.DeploymentID, repoURL, branch)
		if err != nil {
			sp2.Fail("Upload failed")
			if isHTTPStatus(err, http.StatusNotFound) {
				return fmt.Errorf("%s\n\n%s", err.Error(), deploymentNotFoundHelp())
			}
			return err
		}
		sp2.Stop(fmt.Sprintf("Uploaded  redeploying %s", created.Name))
	} else {
		created, err = queueAuthenticatedDeploy(creds, archivePath, repoURL, appName, branch)
		if err != nil {
			sp2.Fail("Upload failed")
			return err
		}
		sp2.Stop(fmt.Sprintf("Uploaded  queued as %s", created.Name))

		if err := refreshProjectConfig(absDir, created, repoURL, branch); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to write %s: %v\n", projectConfigFile, err)
		} else if cfg == nil {
			fmt.Fprintf(os.Stderr, "\nTip: add %s to .gitignore to avoid committing deployment metadata.\n\n", projectConfigFile)
		}
	}

	if isUpdate {
		if err := refreshProjectConfig(absDir, created, repoURL, branch); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to update %s: %v\n", projectConfigFile, err)
		}
	}

	if opts.detach {
		fmt.Printf("\n✓ Queued %s\n\n  %s\n\n  Run: trapiche logs %s\n", created.ID, created.URL, created.ID)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	return pollDeploymentUntilDone(ctx, creds, created.ID, created.URL)
}

func queueAuthenticatedDeploy(creds *credentials, archivePath, repoURL, appName, branch string) (*deploymentResponse, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open archive: %w", err)
	}
	defer file.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	if err := writer.WriteField("repoURL", repoURL); err != nil {
		return nil, err
	}
	if err := writer.WriteField("appName", appName); err != nil {
		return nil, err
	}
	if err := writer.WriteField("branch", branch); err != nil {
		return nil, err
	}

	part, err := writer.CreateFormFile("archive", filepath.Base(archivePath))
	if err != nil {
		return nil, fmt.Errorf("failed to create multipart field: %w", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		return nil, fmt.Errorf("failed to attach archive: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("failed to finalize multipart body: %w", err)
	}

	req, err := authRequest(creds, http.MethodPost, apiPath("/api/deployments/upload"), bytes.NewReader(body.Bytes()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to upload archive: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		return nil, apiError(resp.StatusCode, respBody)
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

func queueAuthenticatedDeployUpdate(creds *credentials, archivePath, deploymentID, repoURL, branch string) (*deploymentResponse, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open archive: %w", err)
	}
	defer file.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	if err := writer.WriteField("repoURL", repoURL); err != nil {
		return nil, err
	}
	if err := writer.WriteField("branch", branch); err != nil {
		return nil, err
	}

	part, err := writer.CreateFormFile("archive", filepath.Base(archivePath))
	if err != nil {
		return nil, fmt.Errorf("failed to create multipart field: %w", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		return nil, fmt.Errorf("failed to attach archive: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("failed to finalize multipart body: %w", err)
	}

	path := fmt.Sprintf("/api/deployments/%s/upload", deploymentID)
	req, err := authRequest(creds, http.MethodPost, apiPath(path), bytes.NewReader(body.Bytes()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to upload archive: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		return nil, apiError(resp.StatusCode, respBody)
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

func pollDeploymentUntilDone(ctx context.Context, creds *credentials, deploymentID, defaultURL string) error {
	fmt.Println("\nBuilding...")
	fmt.Println(strings.Repeat("─", 40))

	if err := streamDeploymentLogs(ctx, creds, deploymentID, false); err != nil {
		return err
	}

	dep, err := getDeployment(creds, deploymentID)
	if err != nil {
		return err
	}

	switch dep.Status {
	case "deployed":
		finalURL := dep.URL
		if finalURL == "" {
			finalURL = defaultURL
		}
		fmt.Printf("\n✓ Deployed!\n\n  %s\n", finalURL)
		return nil
	case "failed":
		return fmt.Errorf("deployment failed")
	default:
		return fmt.Errorf("deployment ended with status: %s", dep.Status)
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

func streamDeploymentLogs(ctx context.Context, creds *credentials, deploymentID string, noFollow bool) error {
	url := apiPath("/api/deployments/" + deploymentID + "/logs/stream")
	req, err := authRequest(creds, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to stream logs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return apiError(resp.StatusCode, body)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out")
		default:
		}

		line := scanner.Text()
		if strings.HasPrefix(line, "event: close") {
			return nil
		}
		if strings.HasPrefix(line, "data: ") {
			fmt.Println(strings.TrimPrefix(line, "data: "))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if noFollow {
		return nil
	}
	return nil
}
