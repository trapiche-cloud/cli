package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"
)

type cliSessionCreateResponse struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	ExpiresAt  time.Time `json:"expiresAt"`
	BrowserURL string    `json:"browserUrl"`
}

type cliSessionPollResponse struct {
	Status    string    `json:"status"`
	Token     string    `json:"token"`
	Username  string    `json:"username"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func newAuthCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authenticate with Trapiche",
	}

	cmd.AddCommand(newAuthLoginCommand())
	cmd.AddCommand(newAuthLogoutCommand())
	cmd.AddCommand(newAuthStatusCommand())
	return cmd
}

func newAuthLoginCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Log in via browser",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuthLogin()
		},
	}
}

func newAuthLogoutCommand() *cobra.Command {
	var force bool
	command := &cobra.Command{
		Use:   "logout",
		Short: "Revoke the token and remove local credentials",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuthLogout(force)
		},
	}
	command.Flags().BoolVar(&force, "force", false, "Remove local credentials even if the server did not revoke the token")
	return command
}

func newAuthStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "status",
		Aliases: []string{"whoami"},
		Short:   "Show login status",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuthStatus()
		},
	}
}

// runAuthLogout revokes the token on the server. The local file is removed
// only when the server confirms, or when force is set. An expired token is
// still sent, because the server can hold it as valid.
func runAuthLogout(force bool) error {
	creds, err := loadCredentialsRaw()
	if err != nil {
		if errors.Is(err, errNotLoggedIn) {
			// No token in the file: nothing to revoke. Remove any stale file.
			if clearErr := clearCredentials(); clearErr != nil {
				return clearErr
			}
			fmt.Println("Not logged in.")
			return nil
		}
		// The file exists but cannot be read. The token may be valid on the
		// server, and this is the only local copy. Keep the file unless forced.
		if !force {
			return fmt.Errorf("%w. The credentials file was kept. Fix it, or use --force to remove it", err)
		}
		fmt.Fprintf(os.Stderr, "warning: %v. The token may stay valid on the server.\n", err)
		if clearErr := clearCredentials(); clearErr != nil {
			return clearErr
		}
		fmt.Println("Local credentials removed.")
		return nil
	}

	_, status, reqErr := apiDelete(creds, "/api/auth/cli/token")
	var failure error
	switch {
	case reqErr != nil:
		failure = fmt.Errorf("could not revoke token: %w", reqErr)
	case status == http.StatusOK || status == http.StatusNoContent || status == http.StatusUnauthorized:
		fmt.Println("Token revoked.")
	default:
		failure = fmt.Errorf("could not revoke token (status %d)", status)
	}

	if failure != nil {
		if !force {
			return fmt.Errorf("%w. The token stays valid on the server and local credentials are kept. Try again, or use --force to remove them anyway", failure)
		}
		fmt.Fprintf(os.Stderr, "warning: %v. The token may stay valid on the server.\n", failure)
	}

	if err := clearCredentials(); err != nil {
		return err
	}
	if failure != nil {
		fmt.Println("Local credentials removed.")
		return nil
	}
	fmt.Println("Logged out.")
	return nil
}

// runAuthStatus checks the saved token with the API. It returns an error
// (exit code 1) unless the server confirms the user.
func runAuthStatus() error {
	creds, err := loadCredentials()
	if err != nil {
		return err
	}

	data, status, reqErr := apiGet(creds, "/api/auth/me")
	if reqErr != nil {
		return fmt.Errorf("could not verify token: %w", reqErr)
	}

	switch status {
	case http.StatusOK:
		// The API answers {"user": {"Username": ...}, ...}. The user model has no
		// JSON tags, so the key is "Username". Go matches the key without case.
		var me struct {
			User struct {
				Username string `json:"username"`
			} `json:"user"`
		}
		if err := json.Unmarshal(data, &me); err != nil || me.User.Username == "" {
			return fmt.Errorf("could not verify token: unexpected response from /api/auth/me")
		}
		fmt.Printf("Logged in as %s\n", me.User.Username)
		printTokenExpiry(creds)
		return nil
	case http.StatusUnauthorized:
		return fmt.Errorf("token not valid — run: trapiche auth login")
	default:
		return apiError(status, data)
	}
}

func printTokenExpiry(creds *credentials) {
	if !creds.ExpiresAt.IsZero() {
		fmt.Printf("Token expires: %s\n", creds.ExpiresAt.Format(time.RFC3339))
	}
}

func runAuthLogin() error {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(apiPath("/api/auth/cli/session"), "application/json", nil)
	if err != nil {
		return fmt.Errorf("failed to create login session: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to create login session (%d)", resp.StatusCode)
	}

	var created cliSessionCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return fmt.Errorf("failed to parse session response: %w", err)
	}
	if created.ID == "" || created.BrowserURL == "" {
		return fmt.Errorf("invalid session response")
	}

	fmt.Println("Opening browser to log in...")
	if err := openBrowser(created.BrowserURL); err != nil {
		fmt.Printf("Open this URL in your browser:\n  %s\n", created.BrowserURL)
	}

	deadline := time.Now().Add(10 * time.Minute)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		pollURL := apiPath("/api/auth/cli/session/" + created.ID)
		pollResp, err := client.Get(pollURL)
		if err != nil {
			return fmt.Errorf("failed to poll login session: %w", err)
		}

		var polled cliSessionPollResponse
		decodeErr := json.NewDecoder(pollResp.Body).Decode(&polled)
		pollResp.Body.Close()
		if decodeErr != nil {
			return fmt.Errorf("failed to parse poll response: %w", decodeErr)
		}

		if pollResp.StatusCode == http.StatusGone {
			return fmt.Errorf("login session expired — run trapiche auth login again")
		}
		if pollResp.StatusCode == http.StatusNotFound {
			return fmt.Errorf("login session not found")
		}

		if polled.Status == "complete" && polled.Token != "" {
			creds := &credentials{
				Token:     polled.Token,
				Username:  polled.Username,
				ExpiresAt: polled.ExpiresAt,
				APIBase:   apiBase,
			}
			if err := saveCredentials(creds); err != nil {
				return err
			}
			if polled.Username != "" {
				fmt.Printf("✓ Logged in as %s\n", polled.Username)
			} else {
				fmt.Println("✓ Logged in")
			}
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("login timed out after 10 minutes")
		}

		select {
		case <-ticker.C:
		}
	}
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "linux":
		return exec.Command("xdg-open", url).Start()
	default:
		return fmt.Errorf("unsupported platform (macOS and Linux only)")
	}
}
