package cmd

import (
	"strings"

	"github.com/spf13/cobra"
)

var apiBase string

var rootCmd = &cobra.Command{
	Use:   "trapiche",
	Short: "Trapiche CLI",
}

func init() {
	rootCmd.SilenceUsage = true
	rootCmd.PersistentFlags().StringVar(&apiBase, "api", "https://api.trapiche.cloud", "Trapiche API base URL")
	rootCmd.AddCommand(newAuthCommand())
	rootCmd.AddCommand(newDeployCommand())
	rootCmd.AddCommand(newLinkCommand())
	rootCmd.AddCommand(newUnlinkCommand())
	rootCmd.AddCommand(newLogsCommand())
	rootCmd.AddCommand(newDeploymentsCommand())
}

func Execute() error {
	return rootCmd.Execute()
}

func apiPath(path string) string {
	return strings.TrimRight(apiBase, "/") + path
}
