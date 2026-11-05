//go:build gui && !freebsd && !netbsd && !openbsd

package gui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// PasteListItem represents a single paste entry in the list view.
type PasteListItem struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Language  string    `json:"language"`
	Views     int       `json:"views"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// listResponse is the API shape for /api/v1/pastes: the paste array is the
// canonical top-level "data" field, with pagination as a sibling (PART 14).
type listResponse struct {
	Pastes     []PasteListItem `json:"data"`
	Pagination struct {
		Total int `json:"total"`
		Pages int `json:"pages"`
	} `json:"pagination"`
}

// apiVersion is the {api_version} route segment (PART 14) this GUI targets.
const apiVersion = "v1"

// apiClient is a lightweight HTTP client used by the GUI API helpers.
type apiClient struct {
	server string
	lang   string
}

// newAPIClient creates an apiClient with the given server base URL and language.
func newAPIClient(server, lang string) *apiClient {
	return &apiClient{server: server, lang: lang}
}

// get performs a GET request, setting the standard GUI headers.
func (a *apiClient) get(path string) (*http.Response, error) {
	hc := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodGet, a.server+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "cairnbin-cli/"+clientVersion)
	req.Header.Set("Accept", "application/json")
	if a.lang != "" {
		req.Header.Set("Accept-Language", a.lang)
	}
	return hc.Do(req)
}

// fetchPastes retrieves a page of public pastes from the server.
func fetchPastes(server, lang string, page, limit int) ([]PasteListItem, error) {
	a := newAPIClient(server, lang)
	path := fmt.Sprintf("/api/%s/pastes?page=%d&limit=%d", apiVersion, page, limit)
	resp, err := a.get(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", t("err_list"), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(tf("err_server_status", "status", resp.StatusCode))
	}

	var lr listResponse
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return nil, fmt.Errorf("%s: %w", t("err_decode"), err)
	}
	return lr.Pastes, nil
}

// fetchPasteRaw retrieves the raw text content of a single paste.
func fetchPasteRaw(server, lang, id string) (string, error) {
	a := newAPIClient(server, lang)
	resp, err := a.get("/raw/" + url.PathEscape(id))
	if err != nil {
		return "", fmt.Errorf("%s: %w", t("err_get"), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", errors.New(tf("err_paste_not_found_expired", "id", id))
	}
	if resp.StatusCode != http.StatusOK {
		return "", errors.New(tf("err_server_status", "status", resp.StatusCode))
	}

	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("%s: %w", t("err_read_body"), err)
	}
	return string(buf), nil
}

// deletePaste sends a DELETE request to remove a paste using its delete token.
func deletePaste(server, lang, id, token string) error {
	hc := &http.Client{Timeout: 15 * time.Second}
	path := server + "/api/" + apiVersion + "/pastes/" + url.PathEscape(id) + "?token=" + url.QueryEscape(token)
	req, err := http.NewRequest(http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "cairnbin-cli/"+clientVersion)
	if lang != "" {
		req.Header.Set("Accept-Language", lang)
	}

	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", t("err_delete"), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return errors.New(tf("err_paste_not_found_invalid_token", "id", id))
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return errors.New(tf("err_server_status", "status", resp.StatusCode))
	}
	return nil
}

// clientVersion is set at build time; the GUI uses it in User-Agent.
// It mirrors the Version var in main.go which is injected via ldflags.
// We use a package-level var so tests can override it without forking the binary.
var clientVersion = "devel"
