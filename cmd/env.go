package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type envPair struct {
	Key   string
	Value string
}

type envEntry struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func newEnvCommand() *cobra.Command {
	var (
		dir          string
		deploymentID string
	)

	command := &cobra.Command{
		Use:   "env",
		Short: "Manage environment variables of a deployment",
		Long: `Manage environment variables of a deployment.

The deployment is the one in trapiche.json (see --dir), or the one in --deployment.
Container apps get the variables at build time. A change applies on the next deploy.`,
	}
	command.PersistentFlags().StringVar(&dir, "dir", ".", "Project directory (to find trapiche.json)")
	command.PersistentFlags().StringVar(&deploymentID, "deployment", "", "Deployment id (default: from trapiche.json)")

	var reveal bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List environment variables",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnvList(dir, deploymentID, reveal)
		},
	}
	list.Flags().BoolVar(&reveal, "reveal", false, "Show the values (hidden by default)")

	var file string
	set := &cobra.Command{
		Use:   "set KEY=VALUE [KEY=VALUE...]",
		Short: "Set environment variables",
		Long: `Set one or more environment variables.

  trapiche env set API_KEY=abc123 NODE_ENV=production
  trapiche env set --file .env

Empty values are not allowed. Lines with an empty value in a file are skipped.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnvSet(dir, deploymentID, file, args)
		},
	}
	set.Flags().StringVar(&file, "file", "", "Read KEY=VALUE lines from a file (for example .env)")

	unset := &cobra.Command{
		Use:   "unset KEY [KEY...]",
		Short: "Remove environment variables",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnvUnset(dir, deploymentID, args)
		},
	}

	command.AddCommand(list, set, unset)
	return command
}

// resolveEnvDeployment returns the deployment id and the saved type ("" if not known).
func resolveEnvDeployment(dir, flagID string) (string, string, error) {
	if id := strings.TrimSpace(flagID); id != "" {
		return id, "", nil
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve directory: %w", err)
	}
	cfg, err := loadProjectConfig(absDir)
	if err != nil {
		return "", "", err
	}
	if cfg == nil {
		return "", "", fmt.Errorf("no %s in %s — run trapiche deploy first, or pass --deployment dep_xxx", projectConfigFile, absDir)
	}
	return cfg.DeploymentID, cfg.DeploymentType, nil
}

func envPath(deploymentID string) string {
	return "/api/deployments/" + url.PathEscape(deploymentID) + "/env"
}

// parseEnvArgs reads KEY=VALUE arguments.
func parseEnvArgs(args []string) ([]envPair, error) {
	var pairs []envPair
	for _, arg := range args {
		key, value, ok := strings.Cut(arg, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid argument %q — use KEY=VALUE", arg)
		}
		if value == "" {
			return nil, fmt.Errorf("the value of %s is empty — empty values are not allowed", key)
		}
		pairs = append(pairs, envPair{Key: key, Value: value})
	}
	return pairs, nil
}

// parseEnvValue reads the value part of a KEY=VALUE line of a .env file.
//   - A value in quotes ends at the closing quote. Text after it, such as
//     a "# comment", is ignored.
//   - An unquoted value ends at a "#" that follows a space or tab. A "#" with no
//     space before it stays in the value (for example a URL with #fragment).
//   - A quote with no closing quote is kept as it is.
func parseEnvValue(raw string) string {
	// KEY= # comment : a "#" after a space or tab, with nothing before it, is a comment.
	if trimmed := strings.TrimLeft(raw, " \t"); len(trimmed) < len(raw) && strings.HasPrefix(trimmed, "#") {
		return ""
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if raw[0] == '"' || raw[0] == '\'' {
		if end := strings.IndexByte(raw[1:], raw[0]); end >= 0 {
			return raw[1 : 1+end]
		}
		return raw
	}
	for i := 1; i < len(raw); i++ {
		if raw[i] == '#' && (raw[i-1] == ' ' || raw[i-1] == '\t') {
			return strings.TrimSpace(raw[:i])
		}
	}
	return raw
}

// parseEnvFile reads KEY=VALUE lines. It skips blank lines and comments, accepts
// an "export " prefix, and removes one pair of matching quotes around a value.
// Keys with an empty value are returned in skipped.
func parseEnvFile(path string) (pairs []envPair, skipped []string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, nil, fmt.Errorf("%s line %d: use KEY=VALUE", path, lineNo)
		}
		value = parseEnvValue(value)
		if value == "" {
			skipped = append(skipped, key)
			continue
		}
		pairs = append(pairs, envPair{Key: key, Value: value})
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}
	return pairs, skipped, nil
}

func runEnvList(dir, deploymentFlag string, reveal bool) error {
	creds, err := loadCredentials()
	if err != nil {
		return err
	}
	id, _, err := resolveEnvDeployment(dir, deploymentFlag)
	if err != nil {
		return err
	}

	path := envPath(id)
	if reveal {
		path += "?reveal=true"
	}
	body, status, err := apiGet(creds, path)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return apiError(status, body)
	}

	var entries []envEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}
	if len(entries) == 0 {
		fmt.Println("No environment variables.")
		return nil
	}
	for _, e := range entries {
		fmt.Printf("%s=%s\n", e.Key, e.Value)
	}
	return nil
}

func runEnvSet(dir, deploymentFlag, file string, args []string) error {
	var pairs []envPair
	if file != "" {
		filePairs, skipped, err := parseEnvFile(file)
		if err != nil {
			return err
		}
		if len(skipped) > 0 {
			fmt.Fprintf(os.Stderr, "warning: skipped keys with an empty value: %s\n", strings.Join(skipped, ", "))
		}
		pairs = append(pairs, filePairs...)
	}
	if len(args) > 0 {
		argPairs, err := parseEnvArgs(args)
		if err != nil {
			return err
		}
		pairs = append(pairs, argPairs...)
	}
	if len(pairs) == 0 {
		return fmt.Errorf("nothing to set — use KEY=VALUE or --file")
	}

	creds, err := loadCredentials()
	if err != nil {
		return err
	}
	id, savedType, err := resolveEnvDeployment(dir, deploymentFlag)
	if err != nil {
		return err
	}

	for i, p := range pairs {
		body, status, err := apiPostJSON(creds, envPath(id), map[string]string{"key": p.Key, "value": p.Value})
		if err != nil {
			return fmt.Errorf("%s: %w (%d of %d set)", p.Key, err, i, len(pairs))
		}
		if status != http.StatusOK {
			return fmt.Errorf("%s: %w (%d of %d set)", p.Key, apiError(status, body), i, len(pairs))
		}
		fmt.Printf("✓ set %s\n", p.Key)
	}

	printEnvApplyHint(savedType)
	return nil
}

func runEnvUnset(dir, deploymentFlag string, keys []string) error {
	creds, err := loadCredentials()
	if err != nil {
		return err
	}
	id, savedType, err := resolveEnvDeployment(dir, deploymentFlag)
	if err != nil {
		return err
	}

	for i, key := range keys {
		body, status, err := apiDelete(creds, envPath(id)+"/"+url.PathEscape(key))
		if err != nil {
			return fmt.Errorf("%s: %w (%d of %d removed)", key, err, i, len(keys))
		}
		if status == http.StatusNotFound {
			return fmt.Errorf("%s: not found (%d of %d removed)", key, i, len(keys))
		}
		if status != http.StatusOK {
			return fmt.Errorf("%s: %w (%d of %d removed)", key, apiError(status, body), i, len(keys))
		}
		fmt.Printf("✓ removed %s\n", key)
	}

	printEnvApplyHint(savedType)
	return nil
}

func printEnvApplyHint(savedType string) {
	if savedType == deployTypeStatic {
		fmt.Fprintln(os.Stderr, "\nnote: CLI uploads of static sites are built without environment variables.")
		return
	}
	fmt.Println("\nThe change applies on the next deploy: trapiche deploy")
}
