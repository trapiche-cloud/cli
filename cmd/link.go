package cmd

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func newLinkCommand() *cobra.Command {
	var dir string

	command := &cobra.Command{
		Use:   "link <deployment-id>",
		Short: "Link this project to an existing deployment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLink(args[0], dir)
		},
	}

	command.Flags().StringVar(&dir, "dir", ".", "Project directory")
	return command
}

func runLink(deploymentID, dir string) error {
	deploymentID = strings.TrimSpace(deploymentID)
	if deploymentID == "" {
		return fmt.Errorf("deployment id is required")
	}

	creds, err := loadCredentials()
	if err != nil {
		return err
	}

	dep, err := getDeployment(creds, deploymentID)
	if err != nil {
		if isHTTPStatus(err, http.StatusNotFound) {
			return fmt.Errorf("%s\n\n%s", err.Error(), deploymentNotFoundHelp())
		}
		return err
	}

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

	repoURL := dep.RepoURL
	if repoURL == "" {
		repoURL, err = gitOriginRemote()
		if err != nil {
			return err
		}
	}

	branch := dep.Branch
	if branch == "" {
		branch, err = gitCurrentBranch()
		if err != nil {
			return err
		}
	}

	if err := refreshProjectConfig(absDir, dep, repoURL, branch); err != nil {
		return err
	}

	fmt.Printf("Linked to %s\n", dep.Name)
	if dep.URL != "" {
		fmt.Printf("  %s\n", dep.URL)
	}
	fmt.Printf("\nRun trapiche deploy to redeploy this project.\n")
	return nil
}
