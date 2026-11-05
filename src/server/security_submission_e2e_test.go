package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/config"
	"github.com/apimgr/cairnbin/src/database"
)

// securityReportTestKey is a valid 32-byte (64 hex char) AES-256-GCM key used to
// seal reports at rest during the test. It is not a real secret.
const securityReportTestKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// newSecurityReportTestServer builds a fully wired server for the coordinated
// disclosure pipeline: a real SQLite store, a valid at-rest encryption key, a
// deterministic installation secret (so security_id is reproducible), a security
// log directory, and production templates. The mailer and webhook notifier stay
// unconfigured, so no external delivery is attempted.
func newSecurityReportTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	db, err := database.NewDatabase("sqlite", filepath.Join(dir, "server.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}

	cfg := &config.Config{Web: config.WebConfig{SiteTitle: "Cairnbin", Theme: "dark"}}
	cfg.Server.Security.EncryptionKey = securityReportTestKey
	cfg.Server.Pages.Contact.Enabled = true

	s := &Server{
		cfg:           cfg,
		db:            db,
		installSecret: []byte("security-report-e2e-installation-secret"),
		version:       "test",
		commitID:      "deadbeef",
		configDir:     dir,
		logDir:        logDir,
	}
	tmpl, err := s.buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	s.templates = tmpl
	return s
}

// securityReportForm returns the complete set of required fields for a valid
// coordinated-disclosure submission. Callers mutate individual keys to exercise
// validation branches.
func securityReportForm() url.Values {
	return url.Values{
		"name":              {"Ada Researcher"},
		"email":             {"ada@example.com"},
		"component":         {"api"},
		"severity":          {"High"},
		"summary":           {"Stored XSS in paste rendering"},
		"steps":             {"1. Do X\n2. Do Y\n3. Observe Z"},
		"impact":            {"An attacker can run script in a paste viewer's browser."},
		"credit_preference": {"name"},
		"credit_name":       {"Ada Researcher"},
		"agreement":         {"yes"},
	}
}

// postSecurityReport drives a POST through the real contact dispatcher with the
// security_id in the query string and JSON content negotiation.
func postSecurityReport(s *Server, form url.Values) *httptest.ResponseRecorder {
	target := "/server/contact?security_id=" + url.QueryEscape(s.currentSecurityID())
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	s.handleContactPost(rec, req)
	return rec
}

// TestSecurityReportSubmissionEndToEnd exercises the full coordinated-disclosure
// pipeline (AI.md 14443-14460): a valid security_id routes into the encrypted
// report handler, the body is sealed at rest (plaintext never persisted), a DB
// row is created, the success envelope returns a sec_ tracking id, and the audit
// log records tracking id / severity / sanitized component with no PII or content.
func TestSecurityReportSubmissionEndToEnd(t *testing.T) {
	s := newSecurityReportTestServer(t)
	form := securityReportForm()

	rec := postSecurityReport(s, form)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content-type: got %q, want json", ct)
	}

	var resp struct {
		OK   bool `json:"ok"`
		Data struct {
			TrackingID string `json:"tracking_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	if !resp.OK {
		t.Errorf("ok: got false, want true")
	}
	trackingID := resp.Data.TrackingID
	if !strings.HasPrefix(trackingID, "sec_") {
		t.Fatalf("tracking_id: got %q, want sec_ prefix", trackingID)
	}
	if len(trackingID) != len("sec_")+16 {
		t.Errorf("tracking_id length: got %d, want %d", len(trackingID), len("sec_")+16)
	}

	// A durable, sealed row must exist.
	rep, err := s.db.GetSecurityReport(trackingID)
	if err != nil {
		t.Fatalf("get report: %v", err)
	}
	if rep == nil {
		t.Fatalf("report %s not stored", trackingID)
	}
	if rep.Severity != "High" {
		t.Errorf("stored severity: got %q, want High", rep.Severity)
	}
	if rep.Component != "api" {
		t.Errorf("stored component: got %q, want api", rep.Component)
	}

	// Plaintext must never be persisted: none of the sensitive fields may appear
	// in the sealed body.
	sealed := string(rep.EncryptedBody)
	for _, secret := range []string{
		"Stored XSS in paste rendering",
		"An attacker can run script in a paste viewer's browser.",
		"ada@example.com",
		"Do X",
	} {
		if strings.Contains(sealed, secret) {
			t.Errorf("sealed body leaks plaintext %q", secret)
		}
	}

	// The at-rest ciphertext must round-trip with the server encryption key.
	key, err := s.cfg.EncryptionKey()
	if err != nil {
		t.Fatalf("encryption key: %v", err)
	}
	plain, err := s.decryptSecurityReport(rep, key)
	if err != nil {
		t.Fatalf("decrypt report: %v", err)
	}
	for _, want := range []string{"Stored XSS in paste rendering", "ada@example.com", trackingID} {
		if !strings.Contains(string(plain), want) {
			t.Errorf("decrypted report missing %q", want)
		}
	}

	// The security log must be PII-free: tracking id, severity, and sanitized
	// component only — never the researcher's identity or the vulnerability body.
	logBytes, err := os.ReadFile(filepath.Join(s.logDir, "security.log"))
	if err != nil {
		t.Fatalf("read security log: %v", err)
	}
	logLine := string(logBytes)
	if !strings.Contains(logLine, "security.report_received") {
		t.Errorf("security log missing report_received event: %s", logLine)
	}
	if !strings.Contains(logLine, trackingID) {
		t.Errorf("security log missing tracking id: %s", logLine)
	}
	for _, pii := range []string{
		"ada@example.com",
		"Ada Researcher",
		"Stored XSS in paste rendering",
		"An attacker can run script",
	} {
		if strings.Contains(logLine, pii) {
			t.Errorf("security log leaks PII/content %q: %s", pii, logLine)
		}
	}
}

// TestSecurityReportAnonymousCreditNotStored verifies that anonymous and
// no-credit preferences keep the researcher's display name out of stored
// metadata (AI.md 14453), while the report still seals and returns a tracking id.
func TestSecurityReportAnonymousCreditNotStored(t *testing.T) {
	for _, pref := range []string{"anonymous", "no"} {
		t.Run(pref, func(t *testing.T) {
			s := newSecurityReportTestServer(t)
			form := securityReportForm()
			form.Set("credit_preference", pref)
			form.Set("credit_name", "Should Not Persist")

			rec := postSecurityReport(s, form)
			if rec.Code != http.StatusOK {
				t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			var resp struct {
				Data struct {
					TrackingID string `json:"tracking_id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			rep, err := s.db.GetSecurityReport(resp.Data.TrackingID)
			if err != nil || rep == nil {
				t.Fatalf("get report: %v", err)
			}
			if rep.CreditName != "" {
				t.Errorf("credit name persisted for %q preference: %q", pref, rep.CreditName)
			}
		})
	}
}

// TestSecurityReportValidationRejects checks that server-side validation rejects
// malformed submissions before anything is stored (AI.md 14449: the server
// re-validates authoritatively).
func TestSecurityReportValidationRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(url.Values)
	}{
		{"missing summary", func(f url.Values) { f.Del("summary") }},
		{"bad email", func(f url.Values) { f.Set("email", "not-an-email") }},
		{"invalid severity", func(f url.Values) { f.Set("severity", "Apocalyptic") }},
		{"invalid credit pref", func(f url.Values) { f.Set("credit_preference", "bribe") }},
		{"no agreement", func(f url.Values) { f.Set("agreement", "no") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSecurityReportTestServer(t)
			form := securityReportForm()
			tc.mutate(form)

			rec := postSecurityReport(s, form)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status: got %d, want 400", rec.Code)
			}
			if strings.Contains(rec.Body.String(), "sec_") {
				t.Errorf("rejected submission returned a tracking id: %s", rec.Body.String())
			}
		})
	}
}

// TestSecurityReportRequiresEncryptionKey confirms the handler refuses to persist
// plaintext when no at-rest encryption key is configured (AI.md 14451: encryption
// is mandatory), returning 503 rather than storing the report.
func TestSecurityReportRequiresEncryptionKey(t *testing.T) {
	s := newSecurityReportTestServer(t)
	s.cfg.Server.Security.EncryptionKey = ""

	rec := postSecurityReport(s, securityReportForm())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}

// TestSecurityReportInvalidSecurityIDFallsThrough verifies that a tampered
// security_id does not enter the encrypted pipeline: the request falls through to
// the standard contact form (AI.md 14446) and never yields a tracking id.
func TestSecurityReportInvalidSecurityIDFallsThrough(t *testing.T) {
	s := newSecurityReportTestServer(t)
	form := securityReportForm()
	req := httptest.NewRequest(http.MethodPost, "/server/contact?security_id=deadbeefdeadbeef", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()

	s.handleContactPost(rec, req)

	if strings.Contains(rec.Body.String(), "sec_") {
		t.Errorf("invalid security_id produced a tracking id: %s", rec.Body.String())
	}
}
