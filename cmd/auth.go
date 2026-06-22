package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
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
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove local credentials",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := clearCredentials(); err != nil {
				return err
			}
			fmt.Println("Logged out.")
			return nil
		},
	}
}

func newAuthStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show login status",
		RunE: func(cmd *cobra.Command, args []string) error {
			creds, err := loadCredentials()
			if err != nil {
				fmt.Println("Not logged in.")
				return nil
			}
			fmt.Printf("Logged in as %s\n", creds.Username)
			if !creds.ExpiresAt.IsZero() {
				fmt.Printf("Token expires: %s\n", creds.ExpiresAt.Format(time.RFC3339))
			}
			return nil
		},
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
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return fmt.Errorf("unsupported platform")
	}
}
