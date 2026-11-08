package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type credentials struct {
	Token     string    `json:"token"`
	Username  string    `json:"username,omitempty"`
	ExpiresAt time.Time `json:"expiresAt"`
	APIBase   string    `json:"apiBase,omitempty"`
}

var credentialsPath = defaultCredentialsPath

// errNotLoggedIn means there is no saved token (no file, or an empty token).
var errNotLoggedIn = errors.New("not logged in — run: trapiche auth login")

func defaultCredentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "trapiche", "credentials.json"), nil
}

func loadCredentials() (*credentials, error) {
	creds, err := loadCredentialsRaw()
	if err != nil {
		return nil, err
	}
	if !creds.ExpiresAt.IsZero() && time.Now().After(creds.ExpiresAt) {
		return nil, fmt.Errorf("session expired — run: trapiche auth login")
	}
	return creds, nil
}

// loadCredentialsRaw reads the saved token and does not check the expiry date.
// Logout uses it so an expired token is still sent for revocation.
func loadCredentialsRaw() (*credentials, error) {
	path, err := credentialsPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errNotLoggedIn
		}
		return nil, err
	}

	var creds credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("invalid credentials file: %w", err)
	}
	if creds.Token == "" {
		return nil, errNotLoggedIn
	}
	return &creds, nil
}

func saveCredentials(creds *credentials) error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	if creds.APIBase == "" {
		creds.APIBase = apiBase
	}

	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func clearCredentials() error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
