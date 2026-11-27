package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Options is the complete first-run connection and persistence choice.
type Options struct {
	Server       string
	Token        string
	SaveToConfig bool
	SetDefault   bool
	StartOnLogin bool
}

// ServerInfo contains public server details shown after a successful test.
type ServerInfo struct {
	Name      string
	Version   string
	TokenKind string
}

type healthResponse struct {
	Project struct {
		Name string `json:"name"`
	} `json:"project"`
	Version string `json:"version"`
}

type authResponse struct {
	Data struct {
		Kind string `json:"kind"`
	} `json:"data"`
}

// TestConnection checks the public health endpoint and, when supplied, the API token.
func TestConnection(server, token, apiVersion string) (ServerInfo, error) {
	parsed, err := url.Parse(strings.TrimSpace(server))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery {
		return ServerInfo{}, errors.New("server URL must be an absolute http:// or https:// URL without credentials")
	}
	base := strings.TrimRight(parsed.String(), "/")
	if apiVersion == "" {
		apiVersion = "v1"
	}
	client := &http.Client{Timeout: 15 * time.Second}
	request, err := http.NewRequest(http.MethodGet, base+"/api/"+url.PathEscape(apiVersion)+"/server/healthz", nil)
	if err != nil {
		return ServerInfo{}, fmt.Errorf("create health request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "cairnbin-cli/setup")
	response, err := client.Do(request)
	if err != nil {
		return ServerInfo{}, fmt.Errorf("test server connection: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return ServerInfo{}, fmt.Errorf("server health check returned HTTP %d", response.StatusCode)
	}
	var health healthResponse
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		return ServerInfo{}, fmt.Errorf("decode server health response: %w", err)
	}
	info := ServerInfo{Name: health.Project.Name, Version: health.Version}
	if token == "" {
		return info, nil
	}
	request, err = http.NewRequest(http.MethodGet, base+"/api/"+url.PathEscape(apiVersion)+"/server/auth/check", nil)
	if err != nil {
		return ServerInfo{}, fmt.Errorf("create authentication request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("User-Agent", "cairnbin-cli/setup")
	response, err = client.Do(request)
	if err != nil {
		return ServerInfo{}, fmt.Errorf("test API token: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusUnauthorized {
		return ServerInfo{}, errors.New("API token is invalid")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return ServerInfo{}, fmt.Errorf("API token check returned HTTP %d", response.StatusCode)
	}
	var auth authResponse
	if err := json.NewDecoder(response.Body).Decode(&auth); err != nil {
		return ServerInfo{}, fmt.Errorf("decode API token response: %w", err)
	}
	info.TokenKind = auth.Data.Kind
	return info, nil
}
