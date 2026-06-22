package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func newAuthedClient(creds *credentials) *http.Client {
	return &http.Client{Timeout: 2 * time.Minute}
}

func authRequest(creds *credentials, method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+creds.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func apiGet(creds *credentials, path string) ([]byte, int, error) {
	url := apiPath(path)
	req, err := authRequest(creds, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := newAuthedClient(creds).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return data, resp.StatusCode, nil
}

func apiPostJSON(creds *credentials, path string, payload any) ([]byte, int, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
		body = bytes.NewReader(encoded)
	}
	url := apiPath(path)
	req, err := authRequest(creds, http.MethodPost, url, body)
	if err != nil {
		return nil, 0, err
	}
	resp, err := newAuthedClient(creds).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return data, resp.StatusCode, nil
}

func apiError(status int, body []byte) error {
	var parsed struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &parsed)
	msg := strings.TrimSpace(parsed.Error)
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	return fmt.Errorf("request failed (%d): %s", status, msg)
}

func isHTTPStatus(err error, status int) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), fmt.Sprintf("(%d)", status))
}
