package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Version is set at build time with -ldflags "-X github.com/trapiche/cli/cmd.Version=v1.2.3".
var Version = "dev"

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(cmd.OutOrStdout(), Version)
		},
	}
}
