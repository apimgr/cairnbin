//go:build e2e

// Package e2e contains browser/end-to-end tests for cairnbin (AI.md PART 28).
// This file is compiled only with the e2e build tag and targets the live test
// stack started by tests/e2e.sh.
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"golang.org/x/net/html"
)

const e2eOperatorToken = "e2e-operator-token-only"

var e2eDoubleHits sync.Map
var e2eSMTPMessages = make(chan string, 8)

func TestMain(m *testing.M) {
	listener, err := net.Listen("tcp", ":64980")
	if err != nil {
		fmt.Fprintln(os.Stderr, "start E2E integration doubles:", err)
		os.Exit(2)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e2eDoubleHits.Store(r.URL.Path, true)
		if r.URL.Path == "/update/latest" {
			http.Error(w, "local update fixture failure", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "127.0.0.1/32 # local e2e fixture\n")
	})}
	go func() { _ = server.Serve(listener) }()
	smtpListener, err := net.Listen("tcp", ":64981")
	if err != nil {
		fmt.Fprintln(os.Stderr, "start E2E SMTP double:", err)
		_ = server.Close()
		os.Exit(2)
	}
	go serveE2ESMTP(smtpListener)
	code := m.Run()
	_ = smtpListener.Close()
	_ = server.Close()
	os.Exit(code)
}

func serveE2ESMTP(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go handleE2ESMTP(conn)
	}
}

func handleE2ESMTP(conn net.Conn) {
	defer conn.Close()
	_, _ = io.WriteString(conn, "220 cairnbin-e2e ESMTP\r\n")
	scanner := bufio.NewScanner(conn)
	var message strings.Builder
	data := false
	for scanner.Scan() {
		line := scanner.Text()
		if data {
			if line == "." {
				data = false
				e2eSMTPMessages <- message.String()
				message.Reset()
				_, _ = io.WriteString(conn, "250 message accepted\r\n")
				continue
			}
			message.WriteString(line)
			message.WriteByte('\n')
			continue
		}
		switch {
		case strings.HasPrefix(strings.ToUpper(line), "EHLO"), strings.HasPrefix(strings.ToUpper(line), "HELO"):
			_, _ = io.WriteString(conn, "250-cairnbin-e2e\r\n250 OK\r\n")
		case strings.HasPrefix(strings.ToUpper(line), "MAIL FROM:"), strings.HasPrefix(strings.ToUpper(line), "RCPT TO:"):
			_, _ = io.WriteString(conn, "250 OK\r\n")
		case strings.EqualFold(line, "DATA"):
			data = true
			_, _ = io.WriteString(conn, "354 end with dot\r\n")
		case strings.EqualFold(line, "QUIT"):
			_, _ = io.WriteString(conn, "221 Bye\r\n")
			return
		default:
			_, _ = io.WriteString(conn, "250 OK\r\n")
		}
	}
}

// baseURL returns the E2E_BASE_URL the running stack is reachable at,
// set by tests/e2e.sh before invoking `go test -tags e2e`.
func baseURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("E2E_BASE_URL")
	if u == "" {
		t.Skip("E2E_BASE_URL not set — run via tests/e2e.sh against a live stack")
	}
	return strings.TrimRight(u, "/")
}

func client() *http.Client {
	return &http.Client{Timeout: 15 * time.Second}
}

func hasSelectedOption(page, field, value string) bool {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return false
	}
	var selectedIn func(*html.Node) bool
	selectedIn = func(node *html.Node) bool {
		if node.Type == html.ElementNode && node.Data == "option" {
			selected, optionValue := false, ""
			for _, attr := range node.Attr {
				selected = selected || attr.Key == "selected"
				if attr.Key == "value" {
					optionValue = attr.Val
				}
			}
			if selected && optionValue == value {
				return true
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if selectedIn(child) {
				return true
			}
		}
		return false
	}
	var findSelect func(*html.Node) bool
	findSelect = func(node *html.Node) bool {
		if node.Type == html.ElementNode && node.Data == "select" {
			for _, attr := range node.Attr {
				if attr.Key == "name" && attr.Val == field {
					return selectedIn(node)
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if findSelect(child) {
				return true
			}
		}
		return false
	}
	return findSelect(doc)
}

func csrfClient(t *testing.T, base string) (*http.Client, string) {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Client{Timeout: 15 * time.Second, Jar: jar}
	resp, err := getHTML(c, base+"/create")
	if err != nil {
		t.Fatalf("GET /create for CSRF token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /create for CSRF token returned %d", resp.StatusCode)
	}
	doc, err := html.Parse(resp.Body)
	if err != nil {
		t.Fatalf("parse create form: %v", err)
	}
	var token string
	var find func(*html.Node)
	find = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "input" {
			name, value := "", ""
			for _, attr := range node.Attr {
				if attr.Key == "name" {
					name = attr.Val
				}
				if attr.Key == "value" {
					value = attr.Val
				}
			}
			if name == "csrf_token" {
				token = value
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			find(child)
		}
	}
	find(doc)
	if token == "" {
		t.Fatal("create form omitted CSRF token")
	}
	return c, token
}

func addCSRFToken(req *http.Request, token string) {
	req.Header.Set("X-CSRF-Token", token)
}

func getHTML(c *http.Client, endpoint string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/html")
	return c.Do(req)
}

func createPaste(t *testing.T, base string, payload map[string]interface{}) (string, string) {
	t.Helper()
	c, csrfToken := csrfClient(t, base)
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, base+"/api/v1/pastes", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 cairnbin-e2e")
	addCSRFToken(req, csrfToken)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("create paste: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("create paste status = %d, body=%s", resp.StatusCode, body)
	}
	var result struct {
		Data struct {
			ID         string `json:"id"`
			OwnerToken string `json:"owner_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.Data.ID == "" {
		t.Fatalf("create response lacks paste id: %s (%v)", body, err)
	}
	return result.Data.ID, result.Data.OwnerToken
}

func TestTier1OwnerAndLinkedTokenLifecycleAPI(t *testing.T) {
	base := baseURL(t)
	pasteID, primary := createPaste(t, base, map[string]interface{}{"content": "owner lifecycle seed"})
	if primary == "" {
		t.Fatal("new owner response omitted primary token")
	}
	preferencesReq, err := http.NewRequest(http.MethodGet, base+"/server/preferences?lang=es", nil)
	if err != nil {
		t.Fatal(err)
	}
	preferencesReq.Header.Set("User-Agent", "Mozilla/5.0 cairnbin-e2e")
	preferencesReq.AddCookie(&http.Cookie{Name: "cairnbin_owner_token_rU3uW5Ze", Value: primary})
	preferencesResp, err := client().Do(preferencesReq)
	if err != nil {
		t.Fatalf("render localized owner-token preferences: %v", err)
	}
	preferencesHTML, err := io.ReadAll(preferencesResp.Body)
	preferencesResp.Body.Close()
	if err != nil {
		t.Fatalf("read localized owner-token preferences: %v", err)
	}
	if preferencesResp.StatusCode != http.StatusOK || !strings.Contains(string(preferencesHTML), "Tokens de propietario") || !strings.Contains(string(preferencesHTML), "Crear token vinculado") || !strings.Contains(string(preferencesHTML), "Rotar") {
		t.Fatalf("localized owner-token preferences status=%d missing expected controls", preferencesResp.StatusCode)
	}
	request := func(method, path, token string, body io.Reader) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, base+path, body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp, data
	}
	resp, body := request(http.MethodGet, "/api/v1/server/preferences/tokens", primary, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("primary token list status=%d body=%s", resp.StatusCode, body)
	}
	var list struct {
		Data struct {
			Tokens []struct {
				ID    int64  `json:"id"`
				Kind  string `json:"kind"`
				Token string `json:"token"`
				Name  string `json:"name"`
			} `json:"tokens"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil || len(list.Data.Tokens) != 1 || list.Data.Tokens[0].Kind != "primary" {
		t.Fatalf("invalid masked token list: %s (%v)", body, err)
	}
	if strings.Contains(string(body), primary) {
		t.Fatalf("token list exposed the primary secret: %s", body)
	}
	resp, body = request(http.MethodPost, "/api/v1/server/preferences/tokens/primary/reveal", primary, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), primary) {
		t.Fatalf("primary reveal status=%d body=%s", resp.StatusCode, body)
	}
	linkedBody := strings.NewReader(`{"name":"automation","expires":"7d"}`)
	resp, body = request(http.MethodPost, "/api/v1/server/preferences/tokens", primary, linkedBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("linked token creation status=%d body=%s", resp.StatusCode, body)
	}
	var linked struct {
		Data struct {
			ID    int64  `json:"id"`
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &linked); err != nil || linked.Data.ID == 0 || linked.Data.Token == "" {
		t.Fatalf("invalid linked token response: %s (%v)", body, err)
	}
	linkedID := linked.Data.ID
	resp, body = request(http.MethodGet, "/api/v1/server/preferences/tokens", linked.Data.Token, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("linked token management status=%d body=%s, want 403", resp.StatusCode, body)
	}
	resp, body = request(http.MethodGet, "/api/v1/server/preferences/tokens", primary, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "automation") || strings.Contains(string(body), linked.Data.Token) {
		t.Fatalf("updated token list status=%d body=%s", resp.StatusCode, body)
	}
	resp, body = request(http.MethodPost, fmt.Sprintf("/api/v1/server/preferences/tokens/%d/reveal", linkedID), primary, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), linked.Data.Token) {
		t.Fatalf("linked reveal status=%d body=%s", resp.StatusCode, body)
	}
	resp, body = request(http.MethodPost, fmt.Sprintf("/api/v1/server/preferences/tokens/%d/rotate", linkedID), primary, nil)
	var rotatedLinked struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &rotatedLinked) != nil || rotatedLinked.Data.Token == "" || rotatedLinked.Data.Token == linked.Data.Token {
		t.Fatalf("linked rotation failed: status=%d", resp.StatusCode)
	}
	createdOwned, ownedBody := request(http.MethodPost, "/api/v1/pastes", primary, strings.NewReader(`{"content":"linked token rotation authorization"}`))
	var ownedPaste struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if createdOwned.StatusCode != http.StatusCreated || json.Unmarshal(ownedBody, &ownedPaste) != nil || ownedPaste.Data.ID == "" {
		t.Fatalf("create paste for linked-token rotation: status=%d", createdOwned.StatusCode)
	}
	resp, body = request(http.MethodDelete, "/api/v1/pastes/"+ownedPaste.Data.ID, linked.Data.Token, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("rotated-away linked token still authorized deletion: status=%d body=%s", resp.StatusCode, body)
	}
	resp, body = request(http.MethodDelete, "/api/v1/pastes/"+ownedPaste.Data.ID, rotatedLinked.Data.Token, nil)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("rotated linked token delete status=%d body=%s", resp.StatusCode, body)
	}
	updateBody := strings.NewReader(`{"name":"automation-updated","expires":"30d"}`)
	resp, body = request(http.MethodPatch, fmt.Sprintf("/api/v1/server/preferences/tokens/%d", linkedID), primary, updateBody)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "automation-updated") {
		t.Fatalf("linked token update status=%d body=%s", resp.StatusCode, body)
	}
	resp, body = request(http.MethodDelete, fmt.Sprintf("/api/v1/server/preferences/tokens/%d", linkedID), primary, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("linked token revoke status=%d body=%s", resp.StatusCode, body)
	}
	resp, body = request(http.MethodPost, "/api/v1/server/preferences/tokens/primary/rotate", primary, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("primary rotation status=%d body=%s", resp.StatusCode, body)
	}
	var rotated struct {
		Data struct {
			Token string `json:"owner_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &rotated); err != nil || rotated.Data.Token == "" || rotated.Data.Token == primary {
		t.Fatalf("invalid rotated primary response: %s (%v)", body, err)
	}
	resp, body = request(http.MethodGet, "/api/v1/server/preferences/tokens", primary, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old primary remained valid after rotation: %d %s", resp.StatusCode, body)
	}
	resp, body = request(http.MethodGet, "/api/v1/server/preferences/tokens", rotated.Data.Token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rotated primary did not restore access: %d %s", resp.StatusCode, body)
	}
	formClient, csrf := csrfClient(t, base)
	form := url.Values{"owner_token": {rotated.Data.Token}, "csrf_token": {csrf}}
	restoreReq, err := http.NewRequest(http.MethodPost, base+"/server/preferences/token/restore", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	restoreReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addCSRFToken(restoreReq, csrf)
	restoreClient := *formClient
	restoreClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	restoreResp, err := restoreClient.Do(restoreReq)
	if err != nil {
		t.Fatalf("restore owner identity: %v", err)
	}
	restoreResp.Body.Close()
	if restoreResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("owner restore status=%d", restoreResp.StatusCode)
	}
	ownerCookieSet := strings.Join(restoreResp.Header.Values("Set-Cookie"), "\n")
	if !strings.Contains(ownerCookieSet, "cairnbin_owner_token_rU3uW5Ze=") || !strings.Contains(ownerCookieSet, "HttpOnly") || !strings.Contains(ownerCookieSet, "Secure") {
		t.Fatalf("restore did not issue a protected owner cookie: %s", ownerCookieSet)
	}
	resp, body = request(http.MethodDelete, "/api/v1/pastes/"+pasteID, rotated.Data.Token, nil)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("linked owner delete status=%d body=%s", resp.StatusCode, body)
	}
}

// TestTier1Healthz verifies /server/healthz responds and is not redirected.
func TestTier1Healthz(t *testing.T) {
	base := baseURL(t)
	resp, err := getHTML(client(), base+"/server/healthz")
	if err != nil {
		t.Fatalf("GET /server/healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestTier1OptionalTorUnavailable(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, baseURL(t)+"/server/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client().Do(req)
	if err != nil {
		t.Fatalf("GET health JSON: %v", err)
	}
	defer resp.Body.Close()
	var health struct {
		Status   string `json:"status"`
		Features struct {
			Tor struct {
				Enabled bool   `json:"enabled"`
				Status  string `json:"status"`
			} `json:"tor"`
			GeoIP bool `json:"geoip"`
		} `json:"features"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("decode health JSON: %v", err)
	}
	if resp.StatusCode != http.StatusOK || health.Status != "healthy" || health.Features.Tor.Enabled || health.Features.Tor.Status != "disabled" || !health.Features.GeoIP {
		t.Fatalf("optional network features did not fail open: HTTP %d, status=%q, tor=%+v, geoip=%t", resp.StatusCode, health.Status, health.Features.Tor, health.Features.GeoIP)
	}
}

func TestTier1RecentSearchAndSavedSort(t *testing.T) {
	base := baseURL(t)
	createPaste(t, base, map[string]interface{}{"content": "recent target", "title": "e2e-recent-target", "expires_in": "never"})
	createPaste(t, base, map[string]interface{}{"content": "other recent", "title": "e2e-recent-other", "expires_in": "never"})
	createPaste(t, base, map[string]interface{}{"content": "hidden recent", "title": "e2e-recent-hidden", "visibility": "unlisted", "expires_in": "never"})
	resp, err := getHTML(client(), base+"/recent?search=e2e-recent-target&sort=name&order=asc&limit=50")
	if err != nil {
		t.Fatalf("GET filtered recent page: %v", err)
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	page := string(body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, "e2e-recent-target") || strings.Contains(page, "e2e-recent-other") || strings.Contains(page, "e2e-recent-hidden") {
		t.Fatalf("recent filtering/visibility returned %d with unexpected results", resp.StatusCode)
	}
	if !hasSelectedOption(page, "sort", "name") || !hasSelectedOption(page, "order", "asc") {
		t.Fatal("recent page did not render selected sort and order controls")
	}
	setCookies := strings.Join(resp.Header.Values("Set-Cookie"), "\n")
	for _, preference := range []string{"cairnbin_pref_sort=name", "cairnbin_pref_order=asc", "cairnbin_pref_per_page=50"} {
		if !strings.Contains(setCookies, preference) {
			t.Errorf("recent request did not save %q preference: %s", preference, setCookies)
		}
	}
}

func TestTier1LocalIntegrationDoubles(t *testing.T) {
	base := baseURL(t)
	for _, taskID := range []string{"blocklist_update", "cve_update"} {
		req, err := http.NewRequest(http.MethodPost, base+"/api/v1/schedulers/"+taskID+"/run", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+e2eOperatorToken)
		resp, err := client().Do(req)
		if err != nil {
			t.Fatalf("run %s scheduler task: %v", taskID, err)
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("run %s status = %d, body=%s", taskID, resp.StatusCode, body)
		}
	}
	for _, path := range []string{"/blocklist", "/cve"} {
		if _, ok := e2eDoubleHits.Load(path); !ok {
			t.Errorf("local integration double did not receive %s download", path)
		}
	}
	updateReq, err := http.NewRequest(http.MethodPost, base+"/api/v1/schedulers/update_check/run", nil)
	if err != nil {
		t.Fatal(err)
	}
	updateReq.Header.Set("Authorization", "Bearer "+e2eOperatorToken)
	updateResp, err := client().Do(updateReq)
	if err != nil {
		t.Fatalf("run update_check scheduler task: %v", err)
	}
	updateBody, readErr := io.ReadAll(updateResp.Body)
	updateResp.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if updateResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("failed update source status = %d, want %d; body=%s", updateResp.StatusCode, http.StatusBadRequest, updateBody)
	}
	if _, ok := e2eDoubleHits.Load("/update/latest"); !ok {
		t.Fatal("local integration double did not receive update check")
	}
	select {
	case message := <-e2eSMTPMessages:
		if !strings.Contains(message, "Update Check") || !strings.Contains(message, "e2e-admin@example.test") {
			t.Errorf("SMTP double received unexpected scheduler notification: %s", message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler failure notification did not reach local SMTP double")
	}
	geoIPReq, err := http.NewRequest(http.MethodPost, base+"/api/v1/schedulers/geoip_update/run", nil)
	if err != nil {
		t.Fatal(err)
	}
	geoIPReq.Header.Set("Authorization", "Bearer "+e2eOperatorToken)
	geoIPResp, err := client().Do(geoIPReq)
	if err != nil {
		t.Fatalf("run geoip_update scheduler task: %v", err)
	}
	geoIPBody, readErr := io.ReadAll(geoIPResp.Body)
	geoIPResp.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if geoIPResp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid GeoIP fixture status = %d, want %d; body=%s", geoIPResp.StatusCode, http.StatusBadRequest, geoIPBody)
	}
	for _, path := range []string{"/geoip/asn", "/geoip/country", "/geoip/city-ipv4", "/geoip/city-ipv6"} {
		if _, ok := e2eDoubleHits.Load(path); !ok {
			t.Errorf("local integration double did not receive GeoIP download %s", path)
		}
	}
}

// TestTier1HomeSSR verifies the homepage renders its server-side create link
// and metadata without requiring JavaScript.
func TestTier1HomeSSR(t *testing.T) {
	base := baseURL(t)
	resp, err := getHTML(client(), base+"/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	html := string(body)
	if !strings.Contains(html, `href="/create"`) {
		t.Error("home page has no server-rendered create link")
	}
	for _, marker := range []string{"<title>", `name="description"`, `name="viewport"`, `lang="en"`} {
		if !strings.Contains(html, marker) {
			t.Errorf("home page is missing required SSR metadata %q", marker)
		}
	}
	if strings.Contains(html, "<script") && !strings.Contains(html, "static/js/app.js") {
		t.Error("home page references JS other than the single app.js bundle")
	}
}

func TestTier1BrowserCacheRefresh(t *testing.T) {
	base := baseURL(t)
	request, _ := http.NewRequest(http.MethodGet, base+"/", nil)
	request.Header.Set("Accept", "text/html")
	request.Header.Set("Cookie", "cairnbin_build=stale-build")
	resp, err := client().Do(request)
	if err != nil {
		t.Fatalf("GET / with stale build cookie: %v", err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Clear-Site-Data"); got != `"cache", "storage"` {
		t.Errorf("stale build Clear-Site-Data = %q", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("HTML Cache-Control = %q, want no-store", got)
	}
	if !strings.Contains(strings.Join(resp.Header.Values("Set-Cookie"), "\n"), "cairnbin_build=") {
		t.Error("HTML response did not refresh the build stamp cookie")
	}

	assetResp, err := client().Get(base + "/static/js/app.js")
	if err != nil {
		t.Fatalf("GET unversioned static asset: %v", err)
	}
	assetResp.Body.Close()
	stamp := strings.Trim(assetResp.Header.Get("ETag"), `"`)
	if assetResp.StatusCode != http.StatusOK || stamp == "" || assetResp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("unversioned asset response = %d, cache=%q, etag=%q", assetResp.StatusCode, assetResp.Header.Get("Cache-Control"), assetResp.Header.Get("ETag"))
	}
	versionedResp, err := client().Get(base + "/static/js/app.js?v=" + url.QueryEscape(stamp))
	if err != nil {
		t.Fatalf("GET versioned static asset: %v", err)
	}
	versionedResp.Body.Close()
	if versionedResp.StatusCode != http.StatusOK || versionedResp.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("versioned asset response = %d, cache=%q", versionedResp.StatusCode, versionedResp.Header.Get("Cache-Control"))
	}
}

// TestTier1InternalLinkCrawl follows same-origin links and rejects broken
// routes, server errors, and blank HTML pages.
func TestTier1InternalLinkCrawl(t *testing.T) {
	base := baseURL(t)
	baseURL, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse base URL: %v", err)
	}
	queue := []string{"/"}
	visited := make(map[string]bool)
	assets := make(map[string]bool)
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		if visited[path] {
			continue
		}
		visited[path] = true
		if len(visited) > 200 {
			t.Fatal("internal link crawl exceeded 200 pages")
		}
		resp, err := getHTML(client(), base+path)
		if err != nil {
			t.Errorf("GET %s: %v", path, err)
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			t.Errorf("read %s: %v", path, readErr)
			continue
		}
		if resp.StatusCode >= http.StatusInternalServerError {
			t.Errorf("GET %s returned %d", path, resp.StatusCode)
			continue
		}
		if !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
			continue
		}
		if len(strings.TrimSpace(string(body))) == 0 {
			t.Errorf("GET %s returned blank HTML", path)
			continue
		}
		doc, err := html.Parse(strings.NewReader(string(body)))
		if err != nil {
			t.Errorf("parse HTML at %s: %v", path, err)
			continue
		}
		if err := validatePageMetadata(doc); err != nil {
			t.Errorf("GET %s: %v", path, err)
		}
		var visit func(*html.Node)
		visit = func(node *html.Node) {
			if node.Type == html.ElementNode {
				for _, attr := range node.Attr {
					isLink := node.Data == "a" && attr.Key == "href"
					isAsset := (node.Data == "script" && attr.Key == "src") || (node.Data == "img" && attr.Key == "src") || (node.Data == "link" && attr.Key == "href")
					if (!isLink && !isAsset) || attr.Val == "" || strings.HasPrefix(attr.Val, "#") {
						continue
					}
					ref, parseErr := url.Parse(attr.Val)
					if parseErr != nil {
						continue
					}
					dest := baseURL.ResolveReference(ref)
					if dest.Scheme == baseURL.Scheme && dest.Host == baseURL.Host {
						if isAsset {
							assetPath := dest.RequestURI()
							if !assets[assetPath] {
								assets[assetPath] = true
								assetResp, assetErr := client().Get(base + assetPath)
								if assetErr != nil {
									t.Errorf("load referenced asset %s: %v", assetPath, assetErr)
								} else {
									assetResp.Body.Close()
									if assetResp.StatusCode != http.StatusOK {
										t.Errorf("referenced asset %s returned %d", assetPath, assetResp.StatusCode)
									}
								}
							}
						} else {
							queue = append(queue, dest.RequestURI())
						}
					}
				}
			}
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				visit(child)
			}
		}
		visit(doc)
	}
}

func TestTier1RegisteredGETRoutes(t *testing.T) {
	base := baseURL(t)
	resp, err := client().Get(base + "/debug/routes")
	if err != nil {
		t.Fatalf("GET debug route inventory: %v", err)
	}
	var inventory struct {
		Data struct {
			Routes []struct {
				Method string `json:"method"`
				Route  string `json:"route"`
			} `json:"routes"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&inventory); err != nil {
		resp.Body.Close()
		t.Fatalf("decode debug route inventory: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(inventory.Data.Routes) == 0 {
		t.Fatalf("debug route inventory status/count = %d/%d", resp.StatusCode, len(inventory.Data.Routes))
	}
	seen := make(map[string]bool)
	for _, route := range inventory.Data.Routes {
		if route.Method != http.MethodGet || strings.Contains(route.Route, "*") {
			continue
		}
		path := route.Route
		for {
			start := strings.IndexByte(path, '{')
			if start < 0 {
				break
			}
			end := strings.IndexByte(path[start:], '}')
			if end < 0 {
				t.Fatalf("malformed route pattern %q", route.Route)
			}
			path = path[:start] + "e2e-unknown" + path[start+end+1:]
		}
		if seen[path] || path == "/__e2e__/panic" {
			continue
		}
		seen[path] = true
		response, err := client().Get(base + path)
		if err != nil {
			t.Errorf("GET registered route %s: %v", path, err)
			continue
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode >= http.StatusInternalServerError {
			t.Errorf("GET registered route %s returned %d", path, response.StatusCode)
		}
	}
}

func TestTier1ThemeAndLocaleRendering(t *testing.T) {
	base := baseURL(t)
	for _, tc := range []struct {
		cookie string
		lang   string
		class  string
	}{
		{"theme=dark", "en", "theme-dark"},
		{"theme=light", "es", "theme-light"},
		{"theme=auto", "fr", "theme-auto"},
	} {
		req, _ := http.NewRequest(http.MethodGet, base+"/?lang="+tc.lang, nil)
		req.Header.Set("Accept", "text/html")
		req.Header.Set("Cookie", tc.cookie)
		resp, err := client().Do(req)
		if err != nil {
			t.Errorf("GET home for %s/%s: %v", tc.cookie, tc.lang, err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), tc.class) || !strings.Contains(string(body), `lang="`+tc.lang+`"`) {
			t.Errorf("home page did not render %s and lang %s", tc.class, tc.lang)
		}
	}
}

func validatePageMetadata(doc *html.Node) error {
	var title, language, charset, viewport string
	var inspect func(*html.Node)
	inspect = func(node *html.Node) {
		if node.Type == html.ElementNode {
			switch node.Data {
			case "html":
				for _, attr := range node.Attr {
					if attr.Key == "lang" {
						language = strings.TrimSpace(attr.Val)
					}
				}
			case "title":
				if node.FirstChild != nil {
					title = strings.TrimSpace(node.FirstChild.Data)
				}
			case "meta":
				name, content := "", ""
				for _, attr := range node.Attr {
					switch attr.Key {
					case "name":
						name = strings.ToLower(attr.Val)
					case "content":
						content = attr.Val
					case "charset":
						charset = strings.ToLower(attr.Val)
					}
				}
				if name == "viewport" {
					viewport = content
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			inspect(child)
		}
	}
	inspect(doc)
	var missing []string
	if title == "" {
		missing = append(missing, "non-empty <title>")
	}
	if language == "" {
		missing = append(missing, "<html lang>")
	}
	if charset != "utf-8" && charset != "utf8" {
		missing = append(missing, "UTF-8 charset meta")
	}
	if viewport == "" {
		missing = append(missing, "viewport meta")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing %s", strings.Join(missing, ", "))
	}
	return nil
}

// TestTier1PasteLifecycle exercises create -> view -> raw -> delete via the
// native JSON API end to end (AI.md IDEA.md API Endpoints table).
func TestTier1PasteLifecycle(t *testing.T) {
	base := baseURL(t)
	c, csrfToken := csrfClient(t, base)

	createBody := strings.NewReader(`{"content":"e2e tier1 content","title":"e2e","language":"text","expiry":"1h"}`)
	req, err := http.NewRequest(http.MethodPost, base+"/api/v1/pastes", createBody)
	if err != nil {
		t.Fatalf("build create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 cairnbin-e2e")
	addCSRFToken(req, csrfToken)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("POST /api/v1/pastes: %v", err)
	}
	defer resp.Body.Close()
	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read create response: %v", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 200/201, body=%s", resp.StatusCode, rawBody)
	}

	var created struct {
		OK   bool `json:"ok"`
		Data struct {
			ID         string `json:"id"`
			OwnerToken string `json:"owner_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rawBody, &created); err != nil {
		t.Fatalf("decode create response: %v (body=%s)", err, rawBody)
	}
	if created.Data.ID == "" {
		t.Fatalf("create response missing id: %s", rawBody)
	}
	id := created.Data.ID

	// View metadata + content.
	viewResp, err := c.Get(base + "/api/v1/pastes/" + id)
	if err != nil {
		t.Fatalf("GET /api/v1/pastes/%s: %v", id, err)
	}
	defer viewResp.Body.Close()
	if viewResp.StatusCode != http.StatusOK {
		t.Errorf("view status = %d, want 200", viewResp.StatusCode)
	}

	// Raw content never redirects.
	rawResp, err := c.Get(base + "/" + id + "/raw")
	if err != nil {
		t.Fatalf("GET /%s/raw: %v", id, err)
	}
	defer rawResp.Body.Close()
	if rawResp.StatusCode != http.StatusOK {
		t.Errorf("raw status = %d, want 200", rawResp.StatusCode)
	}
	rawText, _ := io.ReadAll(rawResp.Body)
	if !strings.Contains(string(rawText), "e2e tier1 content") {
		t.Errorf("raw content mismatch: %s", rawText)
	}

	// Owner-token delete, if the API returned one.
	if created.Data.OwnerToken != "" {
		delReq, err := http.NewRequest(http.MethodDelete, base+"/api/v1/pastes/"+id, nil)
		if err != nil {
			t.Fatalf("build delete request: %v", err)
		}
		delReq.Header.Set("Authorization", "Bearer "+created.Data.OwnerToken)
		delResp, err := c.Do(delReq)
		if err != nil {
			t.Fatalf("DELETE /api/v1/pastes/%s: %v", id, err)
		}
		defer delResp.Body.Close()
		if delResp.StatusCode != http.StatusOK && delResp.StatusCode != http.StatusNoContent {
			t.Errorf("delete status = %d, want 200/204", delResp.StatusCode)
		}
	}
}

func TestTier1PasteVisibilitySyntaxDownloadEmbedAndQR(t *testing.T) {
	base := baseURL(t)
	id, ownerToken := createPaste(t, base, map[string]interface{}{
		"content": "package main\nfunc main() {}", "title": "e2e-go.go",
		"language": "go", "visibility": "unlisted", "expires_in": "never",
	})
	if ownerToken == "" {
		t.Fatal("create response omitted owner_token")
	}
	c := client()
	listResp, err := c.Get(base + "/api/v1/pastes?limit=250")
	if err != nil {
		t.Fatal(err)
	}
	listBody, _ := io.ReadAll(listResp.Body)
	listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK || strings.Contains(string(listBody), id) {
		t.Fatalf("unlisted paste appeared in recent listing (status %d): %s", listResp.StatusCode, listBody)
	}
	viewResp, err := getHTML(c, base+"/"+id)
	if err != nil {
		t.Fatal(err)
	}
	viewBody, _ := io.ReadAll(viewResp.Body)
	viewResp.Body.Close()
	if viewResp.StatusCode != http.StatusOK || !strings.Contains(string(viewBody), "e2e-go.go") || !strings.Contains(string(viewBody), "<span") {
		t.Fatalf("paste view did not render title and server syntax highlighting (status %d)", viewResp.StatusCode)
	}
	for _, endpoint := range []struct {
		path string
		want string
	}{{"/dl/" + id, "application/octet-stream"}, {"/emb/" + id, "text/html"}, {"/qr/" + id + "/image", "image/png"}} {
		resp, err := c.Get(base + endpoint.path)
		if err != nil {
			t.Errorf("GET %s: %v", endpoint.path, err)
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), endpoint.want) {
			t.Errorf("GET %s status/content-type = %d/%q; want 200/%q", endpoint.path, resp.StatusCode, resp.Header.Get("Content-Type"), endpoint.want)
		}
	}
	delReq, _ := http.NewRequest(http.MethodDelete, base+"/api/v1/pastes/"+id, nil)
	delReq.Header.Set("Authorization", "Bearer "+ownerToken)
	delResp, err := c.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK && delResp.StatusCode != http.StatusNoContent {
		t.Errorf("owner-token delete status = %d", delResp.StatusCode)
	}
}

func TestTier1LinkPasteRedirectsOnlyFromShortURL(t *testing.T) {
	base := baseURL(t)
	const target = "https://example.invalid/path?q=paste"
	id, _ := createPaste(t, base, map[string]interface{}{"content": "  " + target + "  ", "expires_in": "never"})

	shortClient := *client()
	shortClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	shortResp, err := shortClient.Get(base + "/" + id)
	if err != nil {
		t.Fatalf("GET short link: %v", err)
	}
	shortResp.Body.Close()
	if shortResp.StatusCode != http.StatusFound || shortResp.Header.Get("Location") != target {
		t.Fatalf("short link response = %d, Location=%q; want 302 to %q", shortResp.StatusCode, shortResp.Header.Get("Location"), target)
	}

	for _, path := range []string{"/raw/" + id, "/dl/" + id} {
		resp, err := client().Get(base + path)
		if err != nil {
			t.Errorf("GET %s: %v", path, err)
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil || resp.StatusCode != http.StatusOK || !strings.Contains(string(body), target) {
			t.Errorf("GET %s returned %d with body %q (%v), want stored target text", path, resp.StatusCode, body, readErr)
		}
	}
}

func TestTier1BurnAfterReadDeletesAtThreshold(t *testing.T) {
	base := baseURL(t)
	id, _ := createPaste(t, base, map[string]interface{}{"content": "burn once", "burn_after": 1, "expires_in": "never"})
	first, err := client().Get(base + "/raw/" + id)
	if err != nil {
		t.Fatal(err)
	}
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first read status = %d, want 200", first.StatusCode)
	}
	second, err := client().Get(base + "/raw/" + id)
	if err != nil {
		t.Fatal(err)
	}
	second.Body.Close()
	if second.StatusCode != http.StatusNotFound && second.StatusCode != http.StatusGone {
		t.Fatalf("second read status = %d, want 404/410", second.StatusCode)
	}
}

func TestTier1ExpiredPasteReturnsNotFound(t *testing.T) {
	base := baseURL(t)
	id, _ := createPaste(t, base, map[string]interface{}{"content": "expire soon", "expires_in": "1"})
	time.Sleep(2 * time.Second)
	resp, err := client().Get(base + "/raw/" + id)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusGone {
		t.Fatalf("expired paste status = %d, want 404/410", resp.StatusCode)
	}
}

func TestTier1CSRFAndThemedErrorPage(t *testing.T) {
	base := baseURL(t)
	req, _ := http.NewRequest(http.MethodPost, base+"/create", strings.NewReader("content=csrf+probe"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://attacker.invalid")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site form submission status = %d, want 403", resp.StatusCode)
	}
	errorResp, err := getHTML(client(), base+"/missing-e2e-route")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(errorResp.Body)
	errorResp.Body.Close()
	if errorResp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "theme-") || strings.Contains(strings.ToLower(string(body)), "goroutine ") {
		t.Errorf("404 page did not render as a themed error page (status %d)", errorResp.StatusCode)
	}
}

func TestTier1ThemedInternalServerError(t *testing.T) {
	base := baseURL(t)
	resp, err := getHTML(client(), base+"/__e2e__/panic")
	if err != nil {
		t.Fatalf("GET E2E panic route: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusInternalServerError || !strings.Contains(string(body), "500") || !strings.Contains(string(body), "theme-") || strings.Contains(strings.ToLower(string(body)), "goroutine ") {
		t.Errorf("500 response was not a themed error page (status %d)", resp.StatusCode)
	}
}

func TestTier1FormValidationErrorsRenderOnServer(t *testing.T) {
	base := baseURL(t)
	c, csrfToken := csrfClient(t, base)
	form := url.Values{"content": {""}, "csrf_token": {csrfToken}}
	req, err := http.NewRequest(http.MethodPost, base+"/create", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 cairnbin-e2e")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("POST invalid create form: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("invalid create form response = %d/%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if !strings.Contains(strings.ToLower(string(body)), "content") {
		t.Errorf("server-rendered validation response omitted the content error: %s", body)
	}
}

func TestTier1InvalidOwnerTokenDoesNotRevealPasteExistence(t *testing.T) {
	base := baseURL(t)
	id, _ := createPaste(t, base, map[string]interface{}{"content": "protected owner-token paste"})
	req, err := http.NewRequest(http.MethodDelete, base+"/api/v1/pastes/"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer tok_invalid-owner-token")
	resp, err := client().Do(req)
	if err != nil {
		t.Fatalf("DELETE with invalid owner token: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("invalid owner-token delete status = %d, want 404", resp.StatusCode)
	}
	if strings.Contains(string(body), "protected owner-token paste") || strings.Contains(string(body), id) {
		t.Errorf("invalid owner-token response revealed paste data: %s", body)
	}
}

func TestTier1CompatibilityAndDocsRoutes(t *testing.T) {
	base := baseURL(t)
	c, csrfToken := csrfClient(t, base)
	compatReq, _ := http.NewRequest(http.MethodPost, base+"/documents", strings.NewReader("hastebin compatible content"))
	addCSRFToken(compatReq, csrfToken)
	compatResp, err := c.Do(compatReq)
	if err != nil {
		t.Fatal(err)
	}
	compatBody, _ := io.ReadAll(compatResp.Body)
	compatResp.Body.Close()
	var doc struct {
		Key string `json:"key"`
	}
	if compatResp.StatusCode != http.StatusOK || json.Unmarshal(compatBody, &doc) != nil || doc.Key == "" {
		t.Fatalf("hastebin compatibility response = %d %s", compatResp.StatusCode, compatBody)
	}
	documentResp, err := client().Get(base + "/documents/" + url.PathEscape(doc.Key))
	if err != nil {
		t.Fatalf("GET hastebin document: %v", err)
	}
	var document struct {
		Key  string `json:"key"`
		Data string `json:"data"`
	}
	documentBody, err := io.ReadAll(documentResp.Body)
	documentResp.Body.Close()
	if err != nil || documentResp.StatusCode != http.StatusOK || json.Unmarshal(documentBody, &document) != nil || document.Key != doc.Key || document.Data != "hastebin compatible content" {
		t.Fatalf("hastebin retrieval = %d %s (%v)", documentResp.StatusCode, documentBody, err)
	}
	for _, route := range []string{"/server/docs/swagger", "/server/docs/graphql"} {
		resp, err := getHTML(client(), base+route)
		if err != nil {
			t.Errorf("GET %s: %v", route, err)
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200", route, resp.StatusCode)
		}
	}
}

func TestTier1MicrobinRoundTrip(t *testing.T) {
	base := baseURL(t)
	c, csrfToken := csrfClient(t, base)
	const content = "microbin protocol content"
	payload, _ := json.Marshal(map[string]string{"content": content, "syntax": "text"})
	req, err := http.NewRequest(http.MethodPost, base+"/api/v1/pasta", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 cairnbin-e2e")
	addCSRFToken(req, csrfToken)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("create microbin paste: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var created struct {
		Data struct {
			ID         string `json:"id"`
			OwnerToken string `json:"owner_token"`
		} `json:"data"`
	}
	if (resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK) || json.Unmarshal(body, &created) != nil || created.Data.ID == "" || created.Data.OwnerToken == "" {
		t.Fatalf("microbin create response = %d %s", resp.StatusCode, body)
	}
	getReq, err := http.NewRequest(http.MethodGet, base+"/api/v1/pasta/"+url.PathEscape(created.Data.ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	getReq.Header.Set("Accept", "application/json")
	getReq.Header.Set("User-Agent", "Mozilla/5.0 cairnbin-e2e")
	getResp, err := client().Do(getReq)
	if err != nil {
		t.Fatalf("get microbin paste: %v", err)
	}
	var result struct {
		Data struct {
			Content string `json:"content"`
		} `json:"data"`
	}
	getBody, err := io.ReadAll(getResp.Body)
	getResp.Body.Close()
	if err != nil || getResp.StatusCode != http.StatusOK || json.Unmarshal(getBody, &result) != nil || result.Data.Content != content {
		t.Fatalf("microbin get response = %d %s (%v)", getResp.StatusCode, getBody, err)
	}
	delReq, _ := http.NewRequest(http.MethodDelete, base+"/api/v1/pasta/"+url.PathEscape(created.Data.ID), nil)
	delReq.Header.Set("Authorization", "Bearer "+created.Data.OwnerToken)
	delResp, err := client().Do(delReq)
	if err != nil {
		t.Fatalf("delete microbin paste: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK && delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("microbin delete status = %d", delResp.StatusCode)
	}
}

func TestTier1ZeroXUploadProtocol(t *testing.T) {
	base := baseURL(t)
	c, csrfToken := csrfClient(t, base)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "e2e.txt")
	if err != nil {
		t.Fatal(err)
	}
	const content = "0x0.st protocol content"
	if _, err := io.WriteString(part, content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, base+"/", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	addCSRFToken(req, csrfToken)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("upload 0x0 file: %v", err)
	}
	responseBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(resp.Header.Get("X-Token")) == "" {
		t.Fatalf("0x0 create response = %d %s", resp.StatusCode, responseBody)
	}
	parsed, err := url.Parse(strings.TrimSpace(string(responseBody)))
	if err != nil || parsed == nil {
		t.Fatalf("parse 0x0 raw URL %q: %v", responseBody, err)
	}
	rawResp, err := client().Get(base + parsed.Path)
	if err != nil {
		t.Fatalf("get 0x0 raw paste: %v", err)
	}
	raw, _ := io.ReadAll(rawResp.Body)
	rawResp.Body.Close()
	if rawResp.StatusCode != http.StatusOK || string(raw) != content {
		t.Fatalf("0x0 raw response = %d %q, want %q", rawResp.StatusCode, raw, content)
	}
}

func TestTier1TermbinTCPProtocol(t *testing.T) {
	base := baseURL(t)
	baseURL, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(baseURL.Hostname(), "9999"), 5*time.Second)
	if err != nil {
		t.Fatalf("connect to termbin listener: %v", err)
	}
	defer conn.Close()
	const content = "termbin raw TCP protocol content"
	if _, err := io.WriteString(conn, content); err != nil {
		t.Fatalf("write termbin paste: %v", err)
	}
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		if err := tcpConn.CloseWrite(); err != nil {
			t.Fatalf("finish termbin upload: %v", err)
		}
	}
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read termbin URL: %v", err)
	}
	pasteURL, err := url.Parse(strings.TrimSpace(string(response)))
	if err != nil || pasteURL.Scheme != "http" || pasteURL.Host == "" || pasteURL.Path == "" ||
		!strings.HasSuffix(string(response), "\n") {
		t.Fatalf("parse termbin response %q: %v", response, err)
	}
	rawResp, err := client().Get(base + "/raw" + pasteURL.Path)
	if err != nil {
		t.Fatalf("get termbin paste: %v", err)
	}
	raw, _ := io.ReadAll(rawResp.Body)
	rawResp.Body.Close()
	if rawResp.StatusCode != http.StatusOK || string(raw) != content {
		t.Fatalf("termbin raw response = %d %q, want %q", rawResp.StatusCode, raw, content)
	}
}

// TestTier1CompatibilityCreateProtocols covers the form and raw-body protocols
// that expose distinct wire formats instead of the native JSON API.
func TestTier1CompatibilityCreateProtocols(t *testing.T) {
	base := baseURL(t)
	c, csrfToken := csrfClient(t, base)
	tests := []struct {
		name     string
		endpoint string
		form     url.Values
		want     string
	}{
		{name: "pastebin", endpoint: "/api/api_post.php", form: url.Values{"api_paste_code": {"pastebin protocol content"}}, want: "pastebin protocol content"},
		{name: "lenpaste", endpoint: "/api/new", form: url.Values{"body": {"lenpaste protocol content"}}, want: "lenpaste protocol content"},
		{name: "stikked", endpoint: "/api/create", form: url.Values{"text": {"stikked protocol content"}}, want: "stikked protocol content"},
		{name: "dpaste", endpoint: "/api/v2/", form: url.Values{"content": {"dpaste protocol content"}, "format": {"json"}}, want: "dpaste protocol content"},
		{name: "sprunge", endpoint: "/", form: url.Values{"sprunge": {"sprunge protocol content"}}, want: "sprunge protocol content"},
		{name: "ixio", endpoint: "/", form: url.Values{"f:1": {"ixio protocol content"}}, want: "ixio protocol content"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, base+tc.endpoint, strings.NewReader(tc.form.Encode()))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			addCSRFToken(req, csrfToken)
			resp, err := c.Do(req)
			if err != nil {
				t.Fatalf("POST %s: %v", tc.endpoint, err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("POST %s status = %d, body=%s", tc.endpoint, resp.StatusCode, body)
			}
			var pasteURL string
			if tc.name == "dpaste" || tc.name == "lenpaste" {
				var result struct {
					URL string `json:"url"`
				}
				if err := json.Unmarshal(body, &result); err != nil {
					t.Fatalf("parse dpaste response: %v; body=%s", err, body)
				}
				pasteURL = result.URL
			} else {
				pasteURL = strings.TrimSpace(string(body))
			}
			parsed, err := url.Parse(pasteURL)
			if err != nil || parsed == nil || parsed.Path == "" {
				t.Fatalf("invalid create response URL %q from body %s: %v", pasteURL, body, err)
			}
			parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
			id := parts[len(parts)-1]
			rawResp, err := client().Get(base + "/raw/" + id)
			if err != nil {
				t.Fatalf("GET raw paste: %v", err)
			}
			raw, err := io.ReadAll(rawResp.Body)
			rawResp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if rawResp.StatusCode != http.StatusOK || string(raw) != tc.want {
				t.Fatalf("raw response = %d %q, want %q", rawResp.StatusCode, raw, tc.want)
			}
		})
	}
}

func TestTier2CompatibilityCreateProtocolsWithoutJavaScript(t *testing.T) {
	base := baseURL(t)
	c, csrf := csrfClient(t, base)
	assertCompatibilityProtocols(t, base, c, csrf, false)
}

func TestTier3CompatibilityCreateProtocolsWithJavaScript(t *testing.T) {
	base := baseURL(t)
	ctx, cancel := browserContext(t)
	defer cancel()
	if err := chromedp.Run(ctx, chromedp.Navigate(base+"/create"), chromedp.WaitVisible(`#paste-form`, chromedp.ByQuery)); err != nil {
		t.Fatalf("load CSRF form in browser: %v", err)
	}
	assertCompatibilityProtocols(t, base, nil, "", true, ctx)
}

func assertCompatibilityProtocols(t *testing.T, base string, c *http.Client, csrf string, javascript bool, browser ...context.Context) {
	t.Helper()
	tests := []struct {
		name, endpoint, field, value string
		jsonResponse                 bool
	}{
		{"pastebin", "/api/api_post.php", "api_paste_code", "Tier compatibility pastebin", false},
		{"lenpaste", "/api/new", "body", "Tier compatibility lenpaste", true},
		{"stikked", "/api/create", "text", "Tier compatibility stikked", false},
		{"dpaste", "/api/v2/", "content", "Tier compatibility dpaste", true},
		{"sprunge", "/", "sprunge", "Tier compatibility sprunge", false},
		{"ixio", "/", "f:1", "Tier compatibility ixio", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var body []byte
			if javascript {
				fields, _ := json.Marshal(map[string]string{tc.field: tc.value, "format": "json"})
				expression := fmt.Sprintf(`(async()=>{const f=%s;const form=await fetch('/create').then(r=>r.text());const d=new DOMParser().parseFromString(form,'text/html');const csrf=d.querySelector('[name="csrf_token"]').value;const response=await fetch(%q,{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/x-www-form-urlencoded','X-CSRF-Token':csrf},body:new URLSearchParams(f)});return await response.text()})()`, string(fields), tc.endpoint)
				var result string
				if err := chromedp.Run(browser[0], chromedp.Evaluate(expression, &result, func(params *runtime.EvaluateParams) *runtime.EvaluateParams {
					return params.WithAwaitPromise(true)
				})); err != nil {
					t.Fatalf("JavaScript POST %s: %v", tc.endpoint, err)
				}
				body = []byte(result)
			} else {
				values := url.Values{tc.field: {tc.value}}
				if tc.name == "dpaste" {
					values.Set("format", "json")
				}
				req, err := http.NewRequest(http.MethodPost, base+tc.endpoint, strings.NewReader(values.Encode()))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				addCSRFToken(req, csrf)
				resp, err := c.Do(req)
				if err != nil {
					t.Fatalf("POST %s: %v", tc.endpoint, err)
				}
				body, err = io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil || resp.StatusCode != http.StatusOK {
					t.Fatalf("POST %s: status=%d err=%v body=%s", tc.endpoint, resp.StatusCode, err, body)
				}
			}
			pasteURL := strings.TrimSpace(string(body))
			if tc.jsonResponse {
				var result struct {
					URL string `json:"url"`
				}
				if err := json.Unmarshal(body, &result); err != nil {
					t.Fatalf("decode compatibility response %s: %v; body=%s", tc.name, err, body)
				}
				pasteURL = result.URL
			}
			parsed, err := url.Parse(pasteURL)
			if err != nil || parsed == nil || parsed.Path == "" {
				t.Fatalf("invalid compatibility URL %q: %v", pasteURL, err)
			}
			id := parsed.Path[strings.LastIndex(parsed.Path, "/")+1:]
			resp, err := client().Get(base + "/raw/" + id)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK || string(raw) != tc.value {
				t.Fatalf("compatibility paste raw = %d %q, want %q", resp.StatusCode, raw, tc.value)
			}
		})
	}
}

// TestTier1UnknownID verifies an unknown paste ID returns 404 via both the
// web route and the API route, never a 500 or a redirect to a login page
// that doesn't exist in this project.
func TestTier1UnknownID(t *testing.T) {
	base := baseURL(t)
	c := client()

	const bogus = "0000000000000000000000000000zz"

	webResp, err := c.Get(base + "/" + bogus)
	if err != nil {
		t.Fatalf("GET /%s: %v", bogus, err)
	}
	defer webResp.Body.Close()
	if webResp.StatusCode != http.StatusNotFound {
		t.Errorf("web unknown-id status = %d, want 404", webResp.StatusCode)
	}

	apiResp, err := c.Get(base + "/api/v1/pastes/" + bogus)
	if err != nil {
		t.Fatalf("GET /api/v1/pastes/%s: %v", bogus, err)
	}
	defer apiResp.Body.Close()
	if apiResp.StatusCode != http.StatusNotFound {
		t.Errorf("api unknown-id status = %d, want 404", apiResp.StatusCode)
	}
}

func browserContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	endpoint := os.Getenv("E2E_BROWSER_URL")
	if endpoint == "" {
		t.Skip("E2E_BROWSER_URL not set — run via tests/e2e.sh")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	allocCtx, allocCancel := chromedp.NewRemoteAllocator(ctx, endpoint)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	cleanup := func() {
		if t.Failed() {
			captureFailureArtifacts(t, browserCtx)
		}
		browserCancel()
		allocCancel()
		cancel()
	}
	if err := chromedp.Run(browserCtx); err != nil {
		cleanup()
		t.Fatalf("connect to headless browser: %v", err)
	}
	return browserCtx, cleanup
}

func captureFailureArtifacts(t *testing.T, ctx context.Context) {
	t.Helper()
	dir := os.Getenv("E2E_ARTIFACT_DIR")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "apimgr", "cairnbin-e2e-artifacts")
	}
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	name := strings.NewReplacer("/", "_", "\\", "_").Replace(t.Name())
	var screenshot []byte
	var pageHTML string
	if chromedp.Run(ctx, chromedp.CaptureScreenshot(&screenshot), chromedp.OuterHTML("html", &pageHTML)) != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, name+".png"), screenshot, 0o600)
	_ = os.WriteFile(filepath.Join(dir, name+".html"), []byte(pageHTML), 0o600)
}

// TestTier2NoJS verifies that the server-rendered paste creation form is usable
// in a real browser with script execution disabled.
func TestTier2NoJS(t *testing.T) {
	base := baseURL(t)
	ctx, cancel := browserContext(t)
	defer cancel()
	var html string
	err := chromedp.Run(ctx,
		emulation.SetScriptExecutionDisabled(true),
		chromedp.Navigate(base+"/create"),
		chromedp.OuterHTML("html", &html),
	)
	if err != nil {
		t.Fatalf("load create page without JavaScript: %v", err)
	}
	if !strings.Contains(html, `id="paste-form"`) || !strings.Contains(html, `name="content"`) {
		t.Fatalf("no-JS page is missing the server-rendered paste form")
	}
	content := "e2e browser no javascript paste"
	title := "e2e-nojs-unlisted"
	var viewLink string
	var ownerToken string
	err = chromedp.Run(ctx,
		chromedp.SetValue(`#title`, title, chromedp.ByQuery),
		chromedp.SetValue(`#language`, "go", chromedp.ByQuery),
		chromedp.SetValue(`#expires`, "1h", chromedp.ByQuery),
		chromedp.SetValue(`#visibility`, "1", chromedp.ByQuery),
		chromedp.SetValue(`#burn_after`, "2", chromedp.ByQuery),
		chromedp.SendKeys(`#content`, content),
		chromedp.Click(`#paste-form button[type="submit"]`),
	)
	if err != nil {
		t.Fatalf("submit create form without JavaScript: %v", err)
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, 20*time.Second)
	err = chromedp.Run(waitCtx, chromedp.WaitVisible(`.result a.long-string`, chromedp.ByQuery))
	waitCancel()
	if err != nil {
		_ = chromedp.Run(ctx, chromedp.OuterHTML("html", &html))
		t.Fatalf("create paste without JavaScript: %v; page=%s", err, html)
	}
	if err := chromedp.Run(ctx,
		chromedp.AttributeValue(`.result a.long-string`, "href", &viewLink, nil, chromedp.ByQuery),
		chromedp.Text(`#created-token`, &ownerToken, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("read no-JS create result: %v", err)
	}
	viewURL, err := url.Parse(viewLink)
	if err != nil {
		t.Fatalf("parse created paste URL: %v", err)
	}
	var renderedMarkup string
	err = chromedp.Run(ctx,
		chromedp.Navigate(base+viewURL.EscapedPath()),
		chromedp.WaitVisible(`#paste-code`, chromedp.ByQuery),
		chromedp.Text(`#paste-code`, &html, chromedp.ByQuery),
		chromedp.OuterHTML(`#paste-code`, &renderedMarkup, chromedp.ByQuery),
	)
	if err != nil {
		t.Fatalf("create and view paste without JavaScript: %v", err)
	}
	if !strings.Contains(html, content) {
		t.Fatalf("no-JS paste view is missing created content: %s", html)
	}
	if !strings.Contains(renderedMarkup, "<span") {
		t.Errorf("no-JS paste view did not render server-side syntax highlighting: %s", html)
	}
	pasteID := viewURL.Path[strings.LastIndex(viewURL.Path, "/")+1:]
	recentResp, err := client().Get(base + "/recent?search=" + url.QueryEscape(title))
	if err != nil {
		t.Fatalf("search recent pastes for unlisted title: %v", err)
	}
	recentHTML, readErr := io.ReadAll(recentResp.Body)
	recentResp.Body.Close()
	if readErr != nil {
		t.Fatalf("read recent paste results: %v", readErr)
	}
	if recentResp.StatusCode != http.StatusOK || strings.Contains(string(recentHTML), pasteID) {
		t.Errorf("unlisted paste appeared in recent results: status=%d", recentResp.StatusCode)
	}
	if strings.TrimSpace(ownerToken) == "" {
		t.Fatal("no-JS create flow did not display the newly issued primary token")
	}

	const linkTarget = "https://example.invalid/no-js-link"
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/create"),
		chromedp.WaitVisible(`#paste-form`, chromedp.ByQuery),
		chromedp.SendKeys(`#content`, linkTarget),
		chromedp.Click(`#paste-form button[type="submit"]`),
		chromedp.WaitVisible(`.result a.long-string`, chromedp.ByQuery),
		chromedp.AttributeValue(`.result a.long-string`, "href", &viewLink, nil, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("create link paste without JavaScript: %v", err)
	}
	linkURL, err := url.Parse(viewLink)
	if err != nil {
		t.Fatalf("parse no-JS link paste URL %q: %v", viewLink, err)
	}
	linkID := linkURL.Path[strings.LastIndex(linkURL.Path, "/")+1:]
	redirectClient := *client()
	redirectClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	linkResp, err := redirectClient.Get(base + "/" + linkID)
	if err != nil {
		t.Fatalf("request no-JS created short link: %v", err)
	}
	linkResp.Body.Close()
	if linkResp.StatusCode != http.StatusFound || linkResp.Header.Get("Location") != linkTarget {
		t.Errorf("no-JS link paste response = %d, Location=%q", linkResp.StatusCode, linkResp.Header.Get("Location"))
	}
	rawLinkResp, err := client().Get(base + "/raw/" + linkID)
	if err != nil {
		t.Fatalf("read no-JS link paste raw content: %v", err)
	}
	rawLink, _ := io.ReadAll(rawLinkResp.Body)
	rawLinkResp.Body.Close()
	if rawLinkResp.StatusCode != http.StatusOK || string(rawLink) != linkTarget {
		t.Errorf("no-JS link raw response = %d %q", rawLinkResp.StatusCode, rawLink)
	}

	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/remove/"+viewURL.Path[strings.LastIndex(viewURL.Path, "/")+1:]),
		chromedp.WaitVisible(`form[action^="/remove/"]`, chromedp.ByQuery),
		chromedp.OuterHTML("html", &html),
	); err != nil {
		t.Fatalf("open owner-token management form without JavaScript: %v", err)
	}
	if !strings.Contains(html, `name="token"`) || !strings.Contains(html, `name="csrf_token"`) {
		t.Fatal("no-JS owner-token form omitted its token or CSRF field")
	}
}

func TestTier2OwnerTokenManagementFormsWithoutJavaScript(t *testing.T) {
	base := baseURL(t)
	_, primary := createPaste(t, base, map[string]interface{}{"content": "tier 2 owner token forms"})
	formClient, csrf := csrfClient(t, base)
	postForm := func(path string, values url.Values, token string) (*http.Response, []byte) {
		t.Helper()
		values.Set("csrf_token", csrf)
		req, err := http.NewRequest(http.MethodPost, base+path, strings.NewReader(values.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("User-Agent", "Mozilla/5.0 cairnbin-e2e")
		addCSRFToken(req, csrf)
		req.AddCookie(&http.Cookie{Name: "cairnbin_owner_token_rU3uW5Ze", Value: token})
		resp, err := formClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp, body
	}
	revealed, body := postForm("/server/preferences/token/reveal", url.Values{}, primary)
	if revealed.StatusCode != http.StatusOK || revealed.Header.Get("Cache-Control") != "no-store" || extractRawOwnerToken(body) != primary {
		t.Fatalf("no-JS primary reveal failed: status=%d", revealed.StatusCode)
	}
	created, body := postForm("/server/preferences/tokens", url.Values{"name": {"tier2-automation"}, "expires": {"7d"}}, primary)
	linked := extractRawOwnerToken(body)
	if created.StatusCode != http.StatusOK || linked == "" || linked == primary {
		t.Fatalf("no-JS linked-token creation failed: status=%d", created.StatusCode)
	}
	linkedID := extractLinkedTokenID(body)
	if linkedID == "" {
		t.Fatal("no-JS linked-token response omitted its management controls")
	}
	revealedLinked, body := postForm("/server/preferences/tokens/"+linkedID+"/reveal", url.Values{}, primary)
	if revealedLinked.StatusCode != http.StatusOK || extractRawOwnerToken(body) != linked {
		t.Fatalf("no-JS linked-token reveal failed: status=%d", revealedLinked.StatusCode)
	}
	rotatedLinked, body := postForm("/server/preferences/tokens/"+linkedID+"/rotate", url.Values{}, primary)
	newLinked := extractRawOwnerToken(body)
	if rotatedLinked.StatusCode != http.StatusOK || newLinked == "" || newLinked == linked {
		t.Fatalf("no-JS linked-token rotation failed: status=%d", rotatedLinked.StatusCode)
	}
	rotated, body := postForm("/server/preferences/token/rotate", url.Values{}, primary)
	newPrimary := extractRawOwnerToken(body)
	if rotated.StatusCode != http.StatusOK || newPrimary == "" || newPrimary == primary {
		t.Fatalf("no-JS primary rotation failed: status=%d", rotated.StatusCode)
	}
}

func TestTier3OwnerTokenManagementWithJavaScript(t *testing.T) {
	base := baseURL(t)
	id, primary := createPaste(t, base, map[string]interface{}{"content": "tier 3 owner token browser flow"})
	ctx, cancel := browserContext(t)
	defer cancel()
	if err := chromedp.Run(ctx,
		network.Enable(),
		chromedp.Navigate(base+"/remove/"+id),
		chromedp.WaitReady("body", chromedp.ByQuery),
	); err != nil {
		t.Fatalf("open owner-token page in browser: %v", err)
	}
	setOwnerCookie := func() {
		t.Helper()
		if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			return network.SetCookie("cairnbin_owner_token_rU3uW5Ze", primary).WithURL(base).WithSecure(false).Do(ctx)
		})); err != nil {
			t.Fatalf("set browser owner cookie: %v", err)
		}
	}
	post := func(path string, values map[string]string) (int, []byte) {
		t.Helper()
		setOwnerCookie()
		pathJSON, _ := json.Marshal(path)
		valuesJSON, _ := json.Marshal(values)
		expression := fmt.Sprintf(`(async()=>{const csrf=document.querySelector('[name="csrf_token"]')?.value;if(!csrf)throw new Error('CSRF token missing');const r=await fetch(%s,{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/x-www-form-urlencoded','X-CSRF-Token':csrf},body:new URLSearchParams(%s)});return JSON.stringify({status:r.status,body:await r.text()})})()`, pathJSON, valuesJSON)
		var encoded string
		if err := chromedp.Run(ctx, chromedp.Evaluate(expression, &encoded, func(params *runtime.EvaluateParams) *runtime.EvaluateParams {
			return params.WithAwaitPromise(true)
		})); err != nil {
			t.Fatalf("JavaScript POST %s: %v", path, err)
		}
		var result struct {
			Status int    `json:"status"`
			Body   string `json:"body"`
		}
		if err := json.Unmarshal([]byte(encoded), &result); err != nil {
			t.Fatalf("decode JavaScript POST %s response: %v", path, err)
		}
		return result.Status, []byte(result.Body)
	}
	status, body := post("/server/preferences/token/reveal", map[string]string{})
	if status != http.StatusOK || extractRawOwnerToken(body) != primary {
		t.Fatalf("browser primary reveal failed: status=%d", status)
	}
	status, body = post("/server/preferences/tokens", map[string]string{"name": "tier3-browser", "expires": "7d"})
	linked := extractRawOwnerToken(body)
	linkedID := extractLinkedTokenID(body)
	if status != http.StatusOK || linked == "" || linkedID == "" {
		t.Fatalf("browser linked-token creation failed: status=%d", status)
	}
	status, body = post("/server/preferences/tokens/"+linkedID+"/rotate", map[string]string{})
	if status != http.StatusOK || extractRawOwnerToken(body) == "" || extractRawOwnerToken(body) == linked {
		t.Fatalf("browser linked-token rotation failed: status=%d", status)
	}
}

func extractLinkedTokenID(body []byte) string {
	const marker = "/server/preferences/tokens/"
	value := string(body)
	for offset := 0; offset < len(value); {
		index := strings.Index(value[offset:], marker)
		if index < 0 {
			return ""
		}
		start := offset + index + len(marker)
		endOffset := strings.IndexByte(value[start:], '/')
		if endOffset > 0 {
			id := value[start : start+endOffset]
			valid := true
			for _, char := range id {
				if char < '0' || char > '9' {
					valid = false
					break
				}
			}
			if valid {
				return id
			}
		}
		offset = start
	}
	return ""
}

func extractRawOwnerToken(body []byte) string {
	const prefix = "tok_"
	for offset := 0; ; {
		index := strings.Index(string(body[offset:]), prefix)
		if index < 0 {
			return ""
		}
		start := offset + index
		end := start + len(prefix) + 32
		if end <= len(body) {
			candidate := string(body[start:end])
			valid := true
			for _, char := range candidate[len(prefix):] {
				if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9') {
					valid = false
					break
				}
			}
			if valid {
				return candidate
			}
		}
		offset = start + len(prefix)
		if offset >= len(body) {
			return ""
		}
	}
}

func TestTier2PasteViewsWithoutJavaScript(t *testing.T) {
	base := baseURL(t)
	id, _ := createPaste(t, base, map[string]interface{}{"content": "tier 2 paste views without JavaScript", "expires_in": "never"})
	ctx, cancel := browserContext(t)
	defer cancel()
	var rendered string
	for _, path := range []string{"/" + id, "/emb/" + id, "/qr/" + id} {
		if err := chromedp.Run(ctx,
			emulation.SetScriptExecutionDisabled(true),
			chromedp.Navigate(base+path),
			chromedp.WaitReady("body", chromedp.ByQuery),
			chromedp.OuterHTML("html", &rendered),
		); err != nil {
			t.Fatalf("render %s without JavaScript: %v", path, err)
		}
		want := "tier 2 paste views without JavaScript"
		if strings.HasPrefix(path, "/qr/") {
			want = "/qr/" + id + "/image"
		}
		if !strings.Contains(rendered, want) {
			t.Errorf("%s omitted expected server-rendered content %q", path, want)
		}
	}
	for _, path := range []string{"/raw/" + id, "/dl/" + id, "/qr/" + id + "/image"} {
		resp, err := client().Get(base + path)
		if err != nil {
			t.Errorf("GET %s without JavaScript: %v", path, err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || len(body) == 0 {
			t.Errorf("GET %s without JavaScript status/body = %d/%d", path, resp.StatusCode, len(body))
		}
	}
}

func TestTier2PasteExpiryAndBurnWithoutJavaScript(t *testing.T) {
	base := baseURL(t)
	burnID, _ := createPaste(t, base, map[string]interface{}{"content": "tier 2 burn after one read", "burn_after": 1, "expires_in": "never"})
	expiredID, _ := createPaste(t, base, map[string]interface{}{"content": "tier 2 expired paste", "expires_in": "1"})
	time.Sleep(2 * time.Second)
	ctx, cancel := browserContext(t)
	defer cancel()
	var rendered string
	if err := chromedp.Run(ctx,
		emulation.SetScriptExecutionDisabled(true),
		chromedp.Navigate(base+"/"+burnID),
		chromedp.WaitVisible(`#paste-code`, chromedp.ByQuery),
		chromedp.Text(`#paste-code`, &rendered, chromedp.ByQuery),
	); err != nil || !strings.Contains(rendered, "tier 2 burn after one read") {
		t.Fatalf("first no-JS burn paste read = %q, err=%v", rendered, err)
	}
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"+burnID),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.OuterHTML("html", &rendered),
	); err != nil || !strings.Contains(rendered, "404") {
		t.Fatalf("second no-JS burn paste read did not show 404, err=%v", err)
	}
	expiredResp, err := client().Get(base + "/raw/" + expiredID)
	if err != nil {
		t.Fatal(err)
	}
	expiredResp.Body.Close()
	if expiredResp.StatusCode != http.StatusNotFound && expiredResp.StatusCode != http.StatusGone {
		t.Errorf("expired no-JS paste status = %d, want 404/410", expiredResp.StatusCode)
	}
}

func TestTier2OptionalTorUnavailable(t *testing.T) {
	base := baseURL(t)
	ctx, cancel := browserContext(t)
	defer cancel()
	var featureHTML string
	if err := chromedp.Run(ctx,
		emulation.SetScriptExecutionDisabled(true),
		chromedp.Navigate(base+"/server/healthz"),
		chromedp.OuterHTML(".feature-list", &featureHTML, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("load health page without JavaScript: %v", err)
	}
	if !strings.Contains(featureHTML, "Tor") || !strings.Contains(featureHTML, "feature-disabled") {
		t.Fatalf("health page did not show Tor as unavailable without JavaScript: %s", featureHTML)
	}
}

func TestTier2RecentSearchWithoutJavaScript(t *testing.T) {
	base := baseURL(t)
	createPaste(t, base, map[string]interface{}{"content": "no-js recent fixture", "title": "e2e-nojs-recent-target", "expires_in": "never"})
	ctx, cancel := browserContext(t)
	defer cancel()
	var pageHTML string
	if err := chromedp.Run(ctx,
		emulation.SetScriptExecutionDisabled(true),
		chromedp.Navigate(base+"/recent"),
		chromedp.SetValue(`#recent-search`, "e2e-nojs-recent-target", chromedp.ByQuery),
		chromedp.SetValue(`#recent-sort`, "name", chromedp.ByQuery),
		chromedp.SetValue(`#recent-order`, "asc", chromedp.ByQuery),
		chromedp.Click(`.filter-bar button[type="submit"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.paste-item`, chromedp.ByQuery),
		chromedp.OuterHTML("html", &pageHTML),
	); err != nil {
		t.Fatalf("submit recent search without JavaScript: %v", err)
	}
	if !strings.Contains(pageHTML, "e2e-nojs-recent-target") || !hasSelectedOption(pageHTML, "sort", "name") || !hasSelectedOption(pageHTML, "order", "asc") {
		t.Fatal("no-JS recent search did not render its result and selected filters")
	}
}

// TestTier3FullBrowser checks the home page and all resources it loads for
// browser console errors and failed network requests.
func TestTier3FullBrowser(t *testing.T) {
	base := baseURL(t)
	ctx, cancel := browserContext(t)
	defer cancel()
	var failures []string
	var failuresMu sync.Mutex
	chromedp.ListenTarget(ctx, func(event any) {
		var failure string
		switch e := event.(type) {
		case *runtime.EventExceptionThrown:
			failure = e.ExceptionDetails.Text
		case *runtime.EventConsoleAPICalled:
			if e.Type == runtime.APITypeError {
				for _, arg := range e.Args {
					failure += " " + arg.Value.String()
				}
			}
		case *network.EventLoadingFailed:
			failure = e.ErrorText
		case *network.EventResponseReceived:
			if e.Type != network.ResourceTypeDocument && e.Response.Status >= 400 {
				failure = fmt.Sprintf("%s returned %v", e.Response.URL, e.Response.Status)
			}
		}
		if failure != "" {
			failuresMu.Lock()
			failures = append(failures, failure)
			failuresMu.Unlock()
		}
	})
	var html string
	err := chromedp.Run(ctx,
		network.Enable(),
		chromedp.Navigate(base+"/"),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(500*time.Millisecond),
		chromedp.OuterHTML("html", &html),
	)
	if err != nil {
		t.Fatalf("load home page in browser: %v", err)
	}
	if !strings.Contains(html, `href="/create"`) {
		t.Fatal("home page is missing the server-rendered create link")
	}
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/create"),
		chromedp.WaitVisible(`#paste-form`, chromedp.ByQuery),
		chromedp.OuterHTML("html", &html),
	); err != nil {
		t.Fatalf("load create page in browser: %v", err)
	}
	if !strings.Contains(html, `name="content"`) {
		t.Fatal("create page is missing the server-rendered paste content field")
	}
	const linkTarget = "https://example.invalid/browser-link"
	var linkPasteURL string
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/create"),
		chromedp.WaitVisible(`#paste-form`, chromedp.ByQuery),
		chromedp.SendKeys(`#content`, linkTarget),
		chromedp.Click(`#paste-form button[type="submit"]`),
		chromedp.WaitVisible(`.result a.long-string`, chromedp.ByQuery),
		chromedp.AttributeValue(`.result a.long-string`, "href", &linkPasteURL, nil, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("create link paste through full browser form: %v", err)
	}
	parsedLinkURL, err := url.Parse(linkPasteURL)
	if err != nil {
		t.Fatalf("parse browser-created link URL %q: %v", linkPasteURL, err)
	}
	linkID := parsedLinkURL.Path[strings.LastIndex(parsedLinkURL.Path, "/")+1:]
	shortClient := *client()
	shortClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	shortResp, err := shortClient.Get(base + "/" + linkID)
	if err != nil {
		t.Fatalf("request browser-created link: %v", err)
	}
	shortResp.Body.Close()
	if shortResp.StatusCode != http.StatusFound || shortResp.Header.Get("Location") != linkTarget {
		t.Errorf("browser-created link response = %d, Location=%q", shortResp.StatusCode, shortResp.Header.Get("Location"))
	}
	var englishTitle, spanishTitle, localeHTML string
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/create?lang=en"),
		chromedp.Evaluate(`document.querySelector(".section-title").textContent.trim()`, &englishTitle),
		chromedp.Navigate(base+"/create?lang=es"),
		chromedp.Evaluate(`document.querySelector(".section-title").textContent.trim()`, &spanishTitle),
		chromedp.OuterHTML("html", &localeHTML),
	); err != nil {
		t.Errorf("switch browser locale: %v", err)
	} else if englishTitle == "" || spanishTitle == "" || englishTitle == spanishTitle || !strings.Contains(localeHTML, `lang="es"`) {
		t.Errorf("browser locale did not change a rendered string: en=%q es=%q", englishTitle, spanishTitle)
	}
	var darkBackground, lightBackground string
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`document.cookie = "theme=dark; path=/"`, nil),
		chromedp.Navigate(base+"/"),
		chromedp.Evaluate(`getComputedStyle(document.documentElement).getPropertyValue("--color-bg").trim()`, &darkBackground),
		chromedp.Evaluate(`document.cookie = "theme=light; path=/"`, nil),
		chromedp.Navigate(base+"/"),
		chromedp.Evaluate(`getComputedStyle(document.documentElement).getPropertyValue("--color-bg").trim()`, &lightBackground),
	); err != nil {
		t.Errorf("read browser theme styles: %v", err)
	} else if darkBackground == "" || lightBackground == "" || darkBackground == lightBackground {
		t.Errorf("dark and light theme styles did not differ: dark=%q light=%q", darkBackground, lightBackground)
	}
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`localStorage.removeItem("cairnbin_owner_token_rU3uW5Ze")`, nil),
		chromedp.Navigate(base+"/remove/"+linkID),
	); err != nil {
		t.Fatalf("open owner-token form in browser: %v", err)
	}
	if err := chromedp.Run(ctx, chromedp.WaitVisible(`#token`, chromedp.ByQuery)); err != nil {
		t.Fatalf("wait for owner-token form in browser: %v", err)
	}
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`document.querySelector("#token").form.requestSubmit()`, nil),
	); err != nil {
		t.Fatalf("submit empty owner-token form in browser: %v", err)
	}
	var jsValidation string
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(() => { const token = document.querySelector("#token"); const error = document.querySelector("#token-error"); return JSON.stringify({required:token.required, valid:token.form.checkValidity(), ariaInvalid:token.getAttribute("aria-invalid"), errorVisible:!error.hidden}); })()`, &jsValidation),
	); err != nil {
		t.Errorf("check client-side form validation: %v", err)
	} else if !strings.Contains(jsValidation, `"required":true`) || !strings.Contains(jsValidation, `"valid":false`) || !strings.Contains(jsValidation, `"ariaInvalid":"true"`) || !strings.Contains(jsValidation, `"errorVisible":true`) {
		t.Errorf("browser form validation is not active: %s", jsValidation)
	}
	var notFoundHTML string
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/missing-e2e-browser-route"),
		chromedp.OuterHTML("html", &notFoundHTML),
	); err != nil {
		t.Errorf("load themed 404 page in browser: %v", err)
	} else if !strings.Contains(notFoundHTML, "404") || !strings.Contains(notFoundHTML, "theme-") || strings.Contains(strings.ToLower(notFoundHTML), "goroutine ") {
		t.Errorf("browser 404 page was not themed or exposed a stack trace")
	}
	const browserFormContent = "e2e tier 3 browser form paste"
	var formPasteLink string
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/create"),
		chromedp.WaitVisible(`#paste-form`, chromedp.ByQuery),
		chromedp.SendKeys(`#content`, browserFormContent),
		chromedp.Click(`#paste-form button[type="submit"]`),
		chromedp.WaitVisible(`.result a.long-string`, chromedp.ByQuery),
		chromedp.AttributeValue(`.result a.long-string`, "href", &formPasteLink, nil, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("create paste through full browser form: %v", err)
	}
	formPasteURL, err := url.Parse(formPasteLink)
	if err != nil {
		t.Fatalf("parse full browser create link %q: %v", formPasteLink, err)
	}
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+formPasteURL.EscapedPath()),
		chromedp.WaitVisible(`#paste-code`, chromedp.ByQuery),
		chromedp.Text(`#paste-code`, &html, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("view paste created through full browser form: %v", err)
	} else if !strings.Contains(html, browserFormContent) {
		t.Errorf("browser-created paste view omitted its content: %q", html)
	}
	for _, page := range []string{"/recent", "/server/about", "/server/help", "/server/privacy", "/server/terms", "/server/docs/swagger", "/server/docs/graphql"} {
		if err := chromedp.Run(ctx,
			chromedp.Navigate(base+page),
			chromedp.WaitReady("body", chromedp.ByQuery),
			chromedp.Evaluate(`document.title`, &html),
		); err != nil {
			t.Errorf("load %s in browser: %v", page, err)
		} else if strings.TrimSpace(html) == "" {
			t.Errorf("%s rendered without a page title", page)
		}
	}
	baseParsed, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse browser crawl base URL: %v", err)
	}
	queue := []string{"/"}
	visited := make(map[string]bool)
	for len(queue) > 0 {
		page := queue[0]
		queue = queue[1:]
		if visited[page] {
			continue
		}
		visited[page] = true
		if len(visited) > 200 {
			t.Fatal("full browser internal-link crawl exceeded 200 pages")
		}
		var links []string
		if err := chromedp.Run(ctx,
			chromedp.Navigate(base+page),
			chromedp.WaitReady("body", chromedp.ByQuery),
			chromedp.Evaluate(`Array.from(document.querySelectorAll('a[href]')).map(a => a.href)`, &links),
		); err != nil {
			t.Errorf("browser crawl %s: %v", page, err)
			continue
		}
		for _, link := range links {
			ref, parseErr := url.Parse(link)
			if parseErr != nil {
				t.Errorf("parse browser link %q from %s: %v", link, page, parseErr)
				continue
			}
			dest := baseParsed.ResolveReference(ref)
			if dest.Scheme == baseParsed.Scheme && dest.Host == baseParsed.Host {
				queue = append(queue, dest.RequestURI())
			}
		}
	}
	const browserPasteContent = "e2e full browser rendered paste content"
	pasteID, _ := createPaste(t, base, map[string]interface{}{"content": browserPasteContent, "syntax": "text"})
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"+pasteID),
		chromedp.WaitVisible(`#paste-code`, chromedp.ByQuery),
		chromedp.Text(`#paste-code`, &html, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("load created paste in full browser: %v", err)
	}
	if !strings.Contains(html, browserPasteContent) {
		t.Errorf("full browser paste content = %q, want it to contain %q", strings.TrimSpace(html), browserPasteContent)
	}
	var overflowDetails string
	if err := chromedp.Run(ctx,
		emulation.SetDeviceMetricsOverride(375, 812, 1, false),
		chromedp.Navigate(base+"/"),
		chromedp.Evaluate(`JSON.stringify(Array.from(document.querySelectorAll('body *')).filter(el => !(el.closest('.nav-panel') && !document.querySelector('.nav-checkbox:checked'))).map(el => { const r = el.getBoundingClientRect(); return {tag: el.tagName, id: el.id, className: typeof el.className === 'string' ? el.className : '', left: Math.round(r.left), right: Math.round(r.right), width: Math.round(r.width), scrollWidth: el.scrollWidth, clientWidth: el.clientWidth}; }).filter(el => el.right > window.innerWidth || el.left < 0).slice(0, 20))`, &overflowDetails),
	); err != nil {
		t.Errorf("check mobile layout: %v", err)
	} else if overflowDetails != "[]" {
		t.Errorf("home page has horizontal overflow at 375px viewport: %s", overflowDetails)
	}
	failuresMu.Lock()
	defer failuresMu.Unlock()
	if len(failures) > 0 {
		t.Errorf("browser reported console or network errors: %s", strings.Join(failures, "; "))
	}
}

func TestTier3PasteViewsWithJavaScript(t *testing.T) {
	base := baseURL(t)
	id, _ := createPaste(t, base, map[string]interface{}{"content": "tier 3 paste views with JavaScript", "expires_in": "never"})
	ctx, cancel := browserContext(t)
	defer cancel()
	var rendered string
	for _, path := range []string{"/" + id, "/emb/" + id, "/qr/" + id} {
		if err := chromedp.Run(ctx,
			chromedp.Navigate(base+path),
			chromedp.WaitReady("body", chromedp.ByQuery),
			chromedp.OuterHTML("html", &rendered),
		); err != nil {
			t.Fatalf("render %s with JavaScript: %v", path, err)
		}
		want := "tier 3 paste views with JavaScript"
		if strings.HasPrefix(path, "/qr/") {
			want = "/qr/" + id + "/image"
		}
		if !strings.Contains(rendered, want) {
			t.Errorf("%s omitted expected rendered content %q", path, want)
		}
	}
	for _, path := range []string{"/raw/" + id, "/dl/" + id, "/qr/" + id + "/image"} {
		resp, err := client().Get(base + path)
		if err != nil {
			t.Errorf("GET %s with JavaScript: %v", path, err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || len(body) == 0 {
			t.Errorf("GET %s with JavaScript status/body = %d/%d", path, resp.StatusCode, len(body))
		}
	}
}

func TestTier3PasteExpiryAndBurnWithJavaScript(t *testing.T) {
	base := baseURL(t)
	burnID, _ := createPaste(t, base, map[string]interface{}{"content": "tier 3 burn after one read", "burn_after": 1, "expires_in": "never"})
	expiredID, _ := createPaste(t, base, map[string]interface{}{"content": "tier 3 expired paste", "expires_in": "1"})
	time.Sleep(2 * time.Second)
	ctx, cancel := browserContext(t)
	defer cancel()
	var rendered string
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"+burnID),
		chromedp.WaitVisible(`#paste-code`, chromedp.ByQuery),
		chromedp.Text(`#paste-code`, &rendered, chromedp.ByQuery),
	); err != nil || !strings.Contains(rendered, "tier 3 burn after one read") {
		t.Fatalf("first browser burn paste read = %q, err=%v", rendered, err)
	}
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"+burnID),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.OuterHTML("html", &rendered),
	); err != nil || !strings.Contains(rendered, "404") {
		t.Fatalf("second browser burn paste read did not show 404, err=%v", err)
	}
	expiredResp, err := client().Get(base + "/raw/" + expiredID)
	if err != nil {
		t.Fatal(err)
	}
	expiredResp.Body.Close()
	if expiredResp.StatusCode != http.StatusNotFound && expiredResp.StatusCode != http.StatusGone {
		t.Errorf("expired browser paste status = %d, want 404/410", expiredResp.StatusCode)
	}
}

func TestTier3ThemedInternalServerError(t *testing.T) {
	base := baseURL(t)
	ctx, cancel := browserContext(t)
	defer cancel()
	var pageHTML string
	if err := chromedp.Run(ctx,
		network.Enable(),
		chromedp.Navigate(base+"/__e2e__/panic"),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.OuterHTML("html", &pageHTML),
	); err != nil {
		t.Fatalf("load 500 page in browser: %v", err)
	}
	if !strings.Contains(pageHTML, "500") || !strings.Contains(pageHTML, "theme-") || strings.Contains(strings.ToLower(pageHTML), "goroutine ") {
		t.Fatal("browser 500 response was not a themed error page")
	}
}

func TestTier3OptionalTorUnavailable(t *testing.T) {
	base := baseURL(t)
	ctx, cancel := browserContext(t)
	defer cancel()
	var featureHTML string
	if err := chromedp.Run(ctx,
		network.Enable(),
		chromedp.Navigate(base+"/server/healthz"),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.OuterHTML(".feature-list", &featureHTML, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("load health page in browser: %v", err)
	}
	if !strings.Contains(featureHTML, "Tor") || !strings.Contains(featureHTML, "feature-disabled") {
		t.Fatalf("health page did not show Tor as unavailable: %s", featureHTML)
	}
}

func TestTier3RecentSearchAndSavedSort(t *testing.T) {
	base := baseURL(t)
	createPaste(t, base, map[string]interface{}{"content": "browser recent fixture", "title": "e2e-browser-recent-target", "expires_in": "never"})
	ctx, cancel := browserContext(t)
	defer cancel()
	var resultTitle, pageURL, cookies string
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/recent"),
		chromedp.SetValue(`#recent-search`, "e2e-browser-recent-target", chromedp.ByQuery),
		chromedp.SetValue(`#recent-sort`, "name", chromedp.ByQuery),
		chromedp.SetValue(`#recent-order`, "asc", chromedp.ByQuery),
		chromedp.SetValue(`#recent-limit`, "50", chromedp.ByQuery),
		chromedp.Click(`.filter-bar button[type="submit"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.paste-item`, chromedp.ByQuery),
		chromedp.Text(`.paste-title`, &resultTitle, chromedp.ByQuery),
		chromedp.Location(&pageURL),
		chromedp.Evaluate(`document.cookie`, &cookies),
	); err != nil {
		t.Fatalf("search recent pastes in browser: %v", err)
	}
	if !strings.Contains(resultTitle, "e2e-browser-recent-target") || !strings.Contains(pageURL, "sort=name") || !strings.Contains(cookies, "cairnbin_pref_sort=name") {
		t.Fatalf("browser recent search/saved sort mismatch: title=%q URL=%q cookies=%q", resultTitle, pageURL, cookies)
	}
}

func TestTier3RegisteredPageRoutes(t *testing.T) {
	base := baseURL(t)
	resp, err := client().Get(base + "/debug/routes")
	if err != nil {
		t.Fatalf("GET debug route inventory: %v", err)
	}
	var inventory struct {
		Data struct {
			Routes []struct {
				Method string `json:"method"`
				Route  string `json:"route"`
			} `json:"routes"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&inventory); err != nil {
		resp.Body.Close()
		t.Fatalf("decode debug route inventory: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(inventory.Data.Routes) == 0 {
		t.Fatalf("debug route inventory status/count = %d/%d", resp.StatusCode, len(inventory.Data.Routes))
	}

	ctx, cancel := browserContext(t)
	defer cancel()
	var failures []string
	var failuresMu sync.Mutex
	chromedp.ListenTarget(ctx, func(event any) {
		var failure string
		switch e := event.(type) {
		case *runtime.EventExceptionThrown:
			failure = e.ExceptionDetails.Text
		case *runtime.EventConsoleAPICalled:
			if e.Type == runtime.APITypeError {
				for _, arg := range e.Args {
					failure += " " + arg.Value.String()
				}
			}
		case *network.EventLoadingFailed:
			failure = e.ErrorText
		case *network.EventResponseReceived:
			if e.Type != network.ResourceTypeDocument && e.Response.Status >= 400 {
				failure = fmt.Sprintf("%s returned %v", e.Response.URL, e.Response.Status)
			}
		}
		if failure != "" {
			failuresMu.Lock()
			failures = append(failures, failure)
			failuresMu.Unlock()
		}
	})
	if err := chromedp.Run(ctx, network.Enable()); err != nil {
		t.Fatalf("enable browser network events: %v", err)
	}

	baseParsed, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse base URL: %v", err)
	}
	visited := make(map[string]bool)
	for _, route := range inventory.Data.Routes {
		if route.Method != http.MethodGet || strings.Contains(route.Route, "*") {
			continue
		}
		path := route.Route
		for {
			start := strings.IndexByte(path, '{')
			if start < 0 {
				break
			}
			end := strings.IndexByte(path[start:], '}')
			if end < 0 {
				t.Fatalf("malformed route pattern %q", route.Route)
			}
			path = path[:start] + "e2e-unknown" + path[start+end+1:]
		}
		if visited[path] || strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/debug/") ||
			strings.HasPrefix(path, "/static/") || strings.HasPrefix(path, "/.well-known/") ||
			path == "/metrics" || strings.HasSuffix(path, ".txt") || path == "/server/healthz" ||
			path == "/__e2e__/panic" {
			continue
		}
		visited[path] = true
		if len(visited) > 200 {
			t.Fatal("registered browser-page crawl exceeded 200 routes")
		}
		pageURL := baseParsed.ResolveReference(&url.URL{Path: path})
		pageClient := *client()
		pageClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
		pageResp, err := pageClient.Get(pageURL.String())
		if err != nil {
			t.Errorf("request registered route %s: %v", path, err)
			continue
		}
		contentType := pageResp.Header.Get("Content-Type")
		statusCode := pageResp.StatusCode
		_, _ = io.Copy(io.Discard, pageResp.Body)
		pageResp.Body.Close()
		if statusCode >= http.StatusInternalServerError {
			t.Errorf("registered browser route %s returned %d", path, statusCode)
			continue
		}
		if statusCode >= http.StatusMultipleChoices || !strings.Contains(contentType, "text/html") {
			continue
		}
		var title string
		if err := chromedp.Run(ctx,
			chromedp.Navigate(pageURL.String()),
			chromedp.WaitReady("body", chromedp.ByQuery),
			chromedp.Evaluate(`document.title`, &title),
		); err != nil {
			t.Errorf("open registered browser page %s: %v", path, err)
			continue
		}
		if strings.TrimSpace(title) == "" {
			t.Errorf("registered browser page %s has no document title", path)
		}
	}
	failuresMu.Lock()
	defer failuresMu.Unlock()
	if len(failures) > 0 {
		t.Errorf("registered browser pages reported console or resource failures: %s", strings.Join(failures, "; "))
	}
}
