package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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

var excludedDirs = map[string]struct{}{
	"node_modules": {},
	".git":         {},
	".next":        {},
	"dist":         {},
	"out":          {},
	"build":        {},
	"coverage":     {},
}

type deployCreateResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

type deployStatusResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	URL       string `json:"url"`
	ExpiresAt string `json:"expiresAt"`
	Logs      string `json:"logs"`
}

func newDeployCommand() *cobra.Command {
	var dir string

	command := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy a local static project anonymously",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeploy(dir)
		},
	}

	command.Flags().StringVar(&dir, "dir", ".", "Directory to deploy")
	return command
}

func runDeploy(dir string) error {
	fmt.Print(trapicheTitle)

	absDir, err := filepath.Abs(dir)
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
	created, err := queueAnonymousDeploy(archivePath)
	if err != nil {
		sp2.Fail("Upload failed")
		return err
	}
	sp2.Stop(fmt.Sprintf("Uploaded  queued as %s", created.Name))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	return pollUntilDone(ctx, created.ID, created.URL)
}

func shouldExclude(relPath string, d os.DirEntry) bool {
	name := d.Name()

	if d.IsDir() {
		_, excluded := excludedDirs[name]
		return excluded
	}

	if name == ".DS_Store" {
		return true
	}
	if strings.HasSuffix(name, ".log") {
		return true
	}
	if name == ".env" || strings.HasPrefix(name, ".env.") {
		return true
	}

	_ = relPath
	return false
}

func createTarGz(root string) (string, int, error) {
	tempFile, err := os.CreateTemp("", "trapiche-*.tar.gz")
	if err != nil {
		return "", 0, fmt.Errorf("failed to create temp archive: %w", err)
	}

	gzipWriter := gzip.NewWriter(tempFile)
	tarWriter := tar.NewWriter(gzipWriter)
	fileCount := 0

	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}

		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		if shouldExclude(relPath, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			_ = file.Close()
			return err
		}
		header.Name = filepath.ToSlash(relPath)

		if err := tarWriter.WriteHeader(header); err != nil {
			_ = file.Close()
			return err
		}
		if _, err := io.Copy(tarWriter, file); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}

		fileCount++
		return nil
	})

	closeErr := tarWriter.Close()
	if walkErr == nil && closeErr != nil {
		walkErr = closeErr
	}
	closeErr = gzipWriter.Close()
	if walkErr == nil && closeErr != nil {
		walkErr = closeErr
	}
	closeErr = tempFile.Close()
	if walkErr == nil && closeErr != nil {
		walkErr = closeErr
	}

	if walkErr != nil {
		_ = os.Remove(tempFile.Name())
		return "", 0, fmt.Errorf("failed to create archive: %w", walkErr)
	}

	return tempFile.Name(), fileCount, nil
}

func queueAnonymousDeploy(archivePath string) (*deployCreateResponse, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open archive: %w", err)
	}
	defer file.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

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

	req, err := http.NewRequest(http.MethodPost, apiPath("/api/anonymous/deploy"), &body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
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
		return nil, fmt.Errorf("deploy request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var created deployCreateResponse
	if err := json.Unmarshal(respBody, &created); err != nil {
		return nil, fmt.Errorf("failed to parse deploy response: %w", err)
	}
	if created.ID == "" {
		return nil, fmt.Errorf("deploy response missing deployment id")
	}

	return &created, nil
}

func pollUntilDone(ctx context.Context, deploymentID, defaultURL string) error {
	fmt.Println("\nBuilding...")
	fmt.Println(strings.Repeat("─", 40))

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	printedLogLen := 0
	tty := isTerminal()
	buildFrames := []string{"⠋", "⠙", "⠸", "⠴", "⠇"}
	frameIdx := 0

	for {
		status, err := getAnonymousDeployStatus(deploymentID)
		if err != nil {
			return err
		}

		hadNewLogs := len(status.Logs) > printedLogLen
		if hadNewLogs && tty {
			fmt.Printf("\r%s\r", strings.Repeat(" ", 30))
		}
		printNewLogs(status.Logs, &printedLogLen)

		switch status.Status {
		case "deployed":
			finalURL := status.URL
			if finalURL == "" {
				finalURL = defaultURL
			}
			if tty {
				fmt.Printf("\r%s\r", strings.Repeat(" ", 30))
			}
			fmt.Printf("\n✓ Deployed!\n\n  %s\n\n  Link expires in 7 days.\n", finalURL)
			return nil
		case "failed":
			return fmt.Errorf("deployment failed")
		}

		if !hadNewLogs && tty {
			fmt.Printf("\r%s Waiting...", buildFrames[frameIdx%len(buildFrames)])
			frameIdx++
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("deployment timed out after 10 minutes")
		case <-ticker.C:
		}
	}
}

func getAnonymousDeployStatus(deploymentID string) (*deployStatusResponse, error) {
	url := apiPath("/api/anonymous/deploy/" + deploymentID)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to poll deployment status: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var status deployStatusResponse
	if err := json.Unmarshal(body, &status); err != nil {
		return nil, fmt.Errorf("failed to parse status response: %w", err)
	}
	return &status, nil
}

func printNewLogs(allLogs string, printedLogLen *int) {
	if len(allLogs) > *printedLogLen {
		fmt.Print(allLogs[*printedLogLen:])
		*printedLogLen = len(allLogs)
		return
	}

	if len(allLogs) < *printedLogLen {
		fmt.Print(allLogs)
		*printedLogLen = len(allLogs)
	}
}
