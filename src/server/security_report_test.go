package server

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/database"
)

func TestIsPublicIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "::1", "10.1.2.3", "192.168.0.5", "172.16.9.9",
		"169.254.1.1", "0.0.0.0", "224.0.0.1", "fc00::1", "fe80::1",
	}
	for _, s := range blocked {
		if isPublicIP(net.ParseIP(s)) {
			t.Errorf("isPublicIP(%s) = true; want false", s)
		}
	}
	allowed := []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"}
	for _, s := range allowed {
		if !isPublicIP(net.ParseIP(s)) {
			t.Errorf("isPublicIP(%s) = false; want true", s)
		}
	}
	if isPublicIP(nil) {
		t.Error("isPublicIP(nil) = true; want false")
	}
}

func TestFetchResearcherPubKeyRejectsNonHTTPS(t *testing.T) {
	if _, err := fetchResearcherPubKey("http://keys.example.com/k.asc"); err == nil {
		t.Error("expected non-https url to be rejected")
	}
	if _, err := fetchResearcherPubKey("ftp://keys.example.com/k.asc"); err == nil {
		t.Error("expected non-https scheme to be rejected")
	}
}

func TestFetchResearcherPubKeyEmpty(t *testing.T) {
	if _, err := fetchResearcherPubKey("   "); err == nil {
		t.Error("expected empty key to be rejected")
	}
}

func TestFetchResearcherPubKeyRejectsInvalidPastedBlock(t *testing.T) {
	bogus := pgpPublicKeyHeader + "\nnot-a-real-key\n-----END PGP PUBLIC KEY BLOCK-----"
	if _, err := fetchResearcherPubKey(bogus); err == nil {
		t.Error("expected structurally invalid pasted key to be rejected")
	}
}

func TestSanitizeComponent(t *testing.T) {
	cases := map[string]string{
		"auth":                 "auth",
		"  API  ":              "api",
		"Login Form":           "login-form",
		"api/v1/pastes":        "api/v1/pastes",
		"drop table users; --": "drop-table-users",
		"<script>":             "script",
		"":                     "unspecified",
		"---":                  "unspecified",
		"e-mail_service.v2":    "e-mail_service.v2",
	}
	for in, want := range cases {
		if got := sanitizeComponent(in); got != want {
			t.Errorf("sanitizeComponent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeComponentCaps64(t *testing.T) {
	got := sanitizeComponent(strings.Repeat("a", 200))
	if len(got) > 64 {
		t.Errorf("sanitizeComponent length %d exceeds 64", len(got))
	}
}

func TestRandomHexLengthAndUniqueness(t *testing.T) {
	for _, n := range []int{1, 16, 32, 33} {
		a, err := randomHex(n)
		if err != nil {
			t.Fatalf("randomHex(%d): %v", n, err)
		}
		if len(a) != n {
			t.Errorf("randomHex(%d) length = %d", n, len(a))
		}
		for _, r := range a {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
				t.Errorf("randomHex(%d) has non-hex char %q", n, r)
			}
		}
	}
	a, _ := randomHex(32)
	b, _ := randomHex(32)
	if a == b {
		t.Error("two randomHex(32) calls collided")
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("POST", "/server/contact", nil)
	r.RemoteAddr = "203.0.113.7:54321"
	if got := clientIP(r); got != "203.0.113.7" {
		t.Errorf("clientIP host = %q, want 203.0.113.7", got)
	}
	r.RemoteAddr = "not-a-host-port"
	if got := clientIP(r); got != "not-a-host-port" {
		t.Errorf("clientIP fallback = %q", got)
	}
}

func TestBuildReportBody(t *testing.T) {
	body := buildReportBody(reportFields{
		trackingID:     "sec_deadbeef",
		timestamp:      "2026-07-02T12:00:00Z",
		name:           "Jane Researcher",
		email:          "jane@example.com",
		gpg:            "0xABC",
		component:      "auth",
		endpoint:       "/api/v1/pastes",
		severity:       "High",
		summary:        "sample summary",
		steps:          "step 1",
		impact:         "account takeover",
		suggestedFix:   "validate token",
		cveRequested:   true,
		disclosureDays: 45,
		creditPref:     "name",
		creditName:     "Jane",
		userAgent:      "curl/8",
		remoteIP:       "203.0.113.7",
		appVersion:     "1.2.3",
		commitHash:     "abc123",
	})
	for _, want := range []string{
		"Tracking ID: sec_deadbeef",
		"Reporter: Jane Researcher <jane@example.com>",
		"Reporter PGP: 0xABC",
		"Credit preference: name (Jane)",
		"Severity (self-assessed): High",
		"Affected component: auth",
		"Affected endpoint: /api/v1/pastes",
		"CVE requested: yes",
		"Requested disclosure window: 45 days",
		"Summary:\nsample summary",
		"Steps to reproduce:\nstep 1",
		"Impact:\naccount takeover",
		"Suggested fix:\nvalidate token",
		"app_version: 1.2.3",
		"commit_hash: abc123",
		"request_ip: 203.0.113.7",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("report body missing %q\n---\n%s", want, body)
		}
	}
}

func TestBuildReportBodyOmitsOptional(t *testing.T) {
	body := buildReportBody(reportFields{
		trackingID: "sec_x", timestamp: "t", name: "n", email: "e",
		component: "api", severity: "Low", summary: "s", steps: "st",
		impact: "i", creditPref: "anonymous", disclosureDays: 90,
	})
	if strings.Contains(body, "Reporter PGP:") {
		t.Error("empty gpg should not render a PGP line")
	}
	if strings.Contains(body, "Suggested fix:") {
		t.Error("empty suggested fix should not render a section")
	}
	if strings.Contains(body, "Affected endpoint:") {
		t.Error("empty endpoint should not render a line")
	}
	if !strings.Contains(body, "CVE requested: no") {
		t.Error("cveRequested=false should render 'no'")
	}
}

// ─── handleSecurityReport additional paths ────────────────────────────────────

// failCreateReportDB wraps stubDB but returns an error for CreateSecurityReport.
type failCreateReportDB struct {
	*stubDB
}

func (d *failCreateReportDB) CreateSecurityReport(_ *database.SecurityReport) error {
	return errors.New("db write failed")
}

// TestHandleSecurityReportComponentOther verifies that when the researcher selects
// "other" for the affected component, the free-text component_other field is used
// as the component label and stored (sanitized) in the DB record.
func TestHandleSecurityReportComponentOther(t *testing.T) {
	s := newSecurityReportTestServer(t)
	form := securityReportForm()
	form.Set("component", "other")
	form.Set("component_other", "my custom thing")

	rec := postSecurityReport(s, form)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			TrackingID string `json:"tracking_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	rep, err := s.db.GetSecurityReport(resp.Data.TrackingID)
	if err != nil || rep == nil {
		t.Fatalf("get stored report: %v", err)
	}
	// sanitizeComponent("my custom thing") → "my-custom-thing"
	if rep.Component != "my-custom-thing" {
		t.Errorf("component = %q, want my-custom-thing", rep.Component)
	}
}

// TestHandleSecurityReportDBCreateFails verifies that when the DB write fails the
// handler returns 500 without a tracking id.
func TestHandleSecurityReportDBCreateFails(t *testing.T) {
	s := newSecurityReportTestServer(t)
	s.db = &failCreateReportDB{stubDB: &stubDB{}}

	rec := postSecurityReport(s, securityReportForm())
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d; body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sec_") {
		t.Error("DB failure should not return a tracking id")
	}
}

// TestHandleSecurityReportDisclosureDaysBounds verifies valid and boundary values
// for the disclosure_days field: values in range are accepted, out-of-range values
// fall back to the default 90 days.
func TestHandleSecurityReportDisclosureDaysBounds(t *testing.T) {
	cases := []struct {
		input   string
		wantMax bool
	}{
		{"30", false},
		{"365", false},
		// out of range → default
		{"0", true},
		// out of range → default
		{"366", true},
		// non-numeric → default
		{"bad", true},
	}
	for _, tc := range cases {
		t.Run("days="+tc.input, func(t *testing.T) {
			s := newSecurityReportTestServer(t)
			form := securityReportForm()
			form.Set("disclosure_days", tc.input)

			rec := postSecurityReport(s, form)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d; body=%s", rec.Code, rec.Body.String())
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
			if tc.wantMax && rep.DisclosureDays != defaultDisclosureDays {
				t.Errorf("disclosure_days = %d, want default %d", rep.DisclosureDays, defaultDisclosureDays)
			}
		})
	}
}

// ─── sendSecurityReportEmails paths ──────────────────────────────────────────

// TestSendSecurityReportEmailsAESEncMethod exercises the maintainer email path
// when the report was sealed with AES-256-GCM (SendWithAttachment branch) and the
// researcher GPG field is empty (plaintext ack branch). Email is configured to a
// non-listening address so all Send calls fail silently — the test verifies the
// function does not panic or crash.
func TestSendSecurityReportEmailsAESEncMethod(t *testing.T) {
	s := newSecurityReportTestServer(t)
	s.cfg.Server.Notifications.Email.Enabled = true
	s.cfg.Server.Notifications.Email.SMTP.Host = "127.0.0.1"
	s.cfg.Server.Notifications.Email.SMTP.Port = 1

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	s.sendSecurityReportEmails(r, s.cfg, securityEmailCtx{
		trackingID:     "sec_aes",
		timestamp:      "2026-07-07T00:00:00Z",
		researcher:     "researcher@example.com",
		severity:       "High",
		summary:        "A test vulnerability",
		component:      "api",
		cveRequested:   false,
		disclosureDays: 90,
		encMethod:      "aes-256-gcm",
		statusToken:    "rawtoken123",
		sealed:         []byte("encrypted-payload"),
		researcherGPG:  "",
	})
}

// TestSendSecurityReportEmailsPGPEncMethod exercises the inline-PGP maintainer
// email path (SendRawMessage branch) and a researcher GPG key that fails
// validation (falls back to plaintext ack).
func TestSendSecurityReportEmailsPGPEncMethod(t *testing.T) {
	s := newSecurityReportTestServer(t)
	s.cfg.Server.Notifications.Email.Enabled = true
	s.cfg.Server.Notifications.Email.SMTP.Host = "127.0.0.1"
	s.cfg.Server.Notifications.Email.SMTP.Port = 1

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	s.sendSecurityReportEmails(r, s.cfg, securityEmailCtx{
		trackingID:     "sec_pgp",
		timestamp:      "2026-07-07T00:00:00Z",
		researcher:     "researcher@example.com",
		severity:       "Critical",
		summary:        "PGP-sealed finding",
		component:      "storage",
		cveRequested:   true,
		disclosureDays: 45,
		encMethod:      "pgp",
		statusToken:    "rawtoken456",
		sealed:         []byte("-----BEGIN PGP MESSAGE-----\nfakepgp\n-----END PGP MESSAGE-----"),
		// An invalid pasted key block triggers the fetchResearcherPubKey error path
		// and the function falls back to a plaintext acknowledgment.
		researcherGPG: pgpPublicKeyHeader + "\nnot-a-real-key\n-----END PGP PUBLIC KEY BLOCK-----",
	})
}

// TestSendSecurityReportEmailsEmailDisabledNoop confirms the function is a no-op
// when email is not configured (Enabled=false, no Host).
func TestSendSecurityReportEmailsEmailDisabledNoop(t *testing.T) {
	s := newSecurityReportTestServer(t)
	// Email disabled by default in the test server; this must be silent.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	s.sendSecurityReportEmails(r, s.cfg, securityEmailCtx{
		trackingID: "sec_noop",
		researcher: "x@example.com",
		encMethod:  "aes-256-gcm",
		sealed:     []byte("payload"),
	})
}
