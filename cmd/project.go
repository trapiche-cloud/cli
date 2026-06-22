package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const projectConfigFile = "trapiche.json"

type projectConfig struct {
	DeploymentID string `json:"deploymentId"`
	Name         string `json:"name,omitempty"`
	URL          string `json:"url,omitempty"`
	RepoURL      string `json:"repoURL,omitempty"`
	Branch       string `json:"branch,omitempty"`
}

func projectConfigPath(dir string) string {
	return filepath.Join(dir, projectConfigFile)
}

func loadProjectConfig(dir string) (*projectConfig, error) {
	path := projectConfigPath(dir)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read %s: %w", projectConfigFile, err)
	}

	var cfg projectConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", projectConfigFile, err)
	}
	if cfg.DeploymentID == "" {
		return nil, fmt.Errorf("%s is missing deploymentId", projectConfigFile)
	}
	return &cfg, nil
}

func saveProjectConfig(dir string, cfg *projectConfig) error {
	if cfg == nil || cfg.DeploymentID == "" {
		return fmt.Errorf("cannot save empty project config")
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode %s: %w", projectConfigFile, err)
	}
	data = append(data, '\n')

	path := projectConfigPath(dir)
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", projectConfigFile, err)
	}
	return nil
}

func removeProjectConfig(dir string) error {
	path := projectConfigPath(dir)
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to remove %s: %w", projectConfigFile, err)
	}
	return nil
}

func refreshProjectConfig(dir string, dep *deploymentResponse, repoURL, branch string) error {
	cfg := &projectConfig{
		DeploymentID: dep.ID,
		Name:         dep.Name,
		URL:          dep.URL,
		RepoURL:      repoURL,
		Branch:       branch,
	}
	return saveProjectConfig(dir, cfg)
}

func deploymentNotFoundHelp() string {
	return fmt.Sprintf(`Deployment not found.

  • Link an existing deployment:  trapiche link dep_xxx
  • Start fresh:                  trapiche deploy --new
  • Remove stale link:            trapiche unlink`)
}
