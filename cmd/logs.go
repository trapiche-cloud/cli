package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newLogsCommand() *cobra.Command {
	var (
		repo     string
		noFollow bool
	)

	cmd := &cobra.Command{
		Use:   "logs [deployment-id]",
		Short: "Stream deployment build logs",
		RunE: func(cmd *cobra.Command, args []string) error {
			creds, err := loadCredentials()
			if err != nil {
				return err
			}

			var deploymentID string
			if len(args) > 0 {
				deploymentID = args[0]
			} else {
				deploymentID, err = resolveLatestDeploymentID(creds, repo)
				if err != nil {
					return err
				}
			}

			ctx := context.Background()
			if !noFollow {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel()
			}

			return streamDeploymentLogs(ctx, creds, deploymentID, noFollow)
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "Filter repo when resolving latest deployment")
	cmd.Flags().BoolVar(&noFollow, "no-follow", false, "Print current logs and exit")
	return cmd
}

func resolveLatestDeploymentID(creds *credentials, repoFilter string) (string, error) {
	deployments, err := listDeployments(creds, repoFilter)
	if err != nil {
		return "", err
	}
	if len(deployments) == 0 {
		return "", fmt.Errorf("no deployments found for this repo")
	}
	return deployments[0].ID, nil
}

func listDeployments(creds *credentials, repoFilter string) ([]deploymentResponse, error) {
	return fetchDeployments(creds, repoFilter, false)
}

func fetchDeployments(creds *credentials, repoFilter string, all bool) ([]deploymentResponse, error) {
	body, status, err := apiGet(creds, "/api/deployments")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, apiError(status, body)
	}

	var deployments []deploymentResponse
	if err := json.Unmarshal(body, &deployments); err != nil {
		return nil, err
	}

	if all {
		return deployments, nil
	}

	filter := strings.TrimSpace(repoFilter)
	if filter == "" {
		origin, err := gitOriginRemote()
		if err != nil {
			return nil, fmt.Errorf("no deployments matched — pass --repo or run from a git repository")
		}
		filter = origin
	} else {
		normalized, err := normalizeRepoArg(filter)
		if err != nil {
			return nil, err
		}
		filter = normalized
	}

	var filtered []deploymentResponse
	for _, dep := range deployments {
		if repoURLsMatch(dep.RepoURL, filter) {
			filtered = append(filtered, dep)
		}
	}
	return filtered, nil
}
