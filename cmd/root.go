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
	rootCmd.AddCommand(newDeployCommand())
}

func Execute() error {
	return rootCmd.Execute()
}

func apiPath(path string) string {
	return strings.TrimRight(apiBase, "/") + path
}
