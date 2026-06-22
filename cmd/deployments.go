package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newDeploymentsCommand() *cobra.Command {
	var repo string

	cmd := &cobra.Command{
		Use:   "deployments",
		Short: "Manage deployments",
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List deployments",
		RunE: func(cmd *cobra.Command, args []string) error {
			creds, err := loadCredentials()
			if err != nil {
				return err
			}

			var deployments []deploymentResponse
			if cmd.Flags().Changed("repo") {
				repoValue := repo
				if repoValue == "" {
					origin, err := gitOriginRemote()
					if err != nil {
						return err
					}
					repoValue = origin
				}
				deployments, err = fetchDeployments(creds, repoValue, false)
			} else {
				deployments, err = fetchDeployments(creds, "", true)
			}
			if err != nil {
				return err
			}

			if len(deployments) == 0 {
				fmt.Println("No deployments found.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "ID\tNAME\tSTATUS\tURL")
			for _, dep := range deployments {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", dep.ID, dep.Name, dep.Status, dep.URL)
			}
			return w.Flush()
		},
	}

	listCmd.Flags().StringVar(&repo, "repo", "", "Filter by repo (owner/name or URL); defaults to git origin when inside a repo")
	cmd.AddCommand(listCmd)
	return cmd
}
