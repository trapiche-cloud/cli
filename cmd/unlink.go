package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
)

func newUnlinkCommand() *cobra.Command {
	var dir string

	command := &cobra.Command{
		Use:   "unlink",
		Short: "Remove trapiche.json from this project",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUnlink(dir)
		},
	}

	command.Flags().StringVar(&dir, "dir", ".", "Project directory")
	return command
}

func runUnlink(dir string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("failed to resolve directory: %w", err)
	}

	cfg, err := loadProjectConfig(absDir)
	if err != nil {
		return err
	}
	if cfg == nil {
		return fmt.Errorf("no %s found in %s", projectConfigFile, absDir)
	}

	if err := removeProjectConfig(absDir); err != nil {
		return err
	}

	fmt.Printf("Removed %s\n", projectConfigFile)
	fmt.Println("Next trapiche deploy will create a new deployment (or use trapiche deploy --new).")
	return nil
}
