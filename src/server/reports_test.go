package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/apimgr/cairnbin/src/config"
)

func reportsRouter() http.Handler {
	s := &Server{}
	r := chi.NewRouter()
	r.Post("/api/v1/server/reports/{name}", s.handleReports)
	return r
}

func postReport(t *testing.T, name, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/server/reports/"+name, strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	req.RemoteAddr = "203.0.113.9:4444"
	rec := httptest.NewRecorder()
	reportsRouter().ServeHTTP(rec, req)
	return rec
}

func resetReportLimiter(t *testing.T, limit int) {
	t.Helper()
	old := reportLimiter
	reportLimiter = newRateLimiter(limit, time.Minute)
	t.Cleanup(func() { reportLimiter = old })
}

func TestReports_AcceptsLegacyAndModernCSP(t *testing.T) {
	resetReportLimiter(t, 100)
	legacy := `{"csp-report":{"blocked-uri":"https://evil.example/x?token=secret","violated-directive":"script-src"}}`
	if rec := postReport(t, "csp", "application/csp-report", legacy); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Errorf("legacy: status=%d body=%q, want 204 empty", rec.Code, rec.Body.String())
	}
	modern := `[{"type":"csp-violation","body":{"blockedURL":"inline","effectiveDirective":"script-src"}}]`
	if rec := postReport(t, "default", "application/reports+json", modern); rec.Code != http.StatusNoContent {
		t.Errorf("modern: status=%d, want 204", rec.Code)
	}
	if rec := postReport(t, "nel", "application/reports+json", `[]`); rec.Code != http.StatusNoContent {
		t.Errorf("nel: status=%d, want 204", rec.Code)
	}
}

func TestReports_NELLogsSanitizedFields(t *testing.T) {
	resetReportLimiter(t, 100)
	cfg := config.DefaultConfig()
	cfg.Web.Headers.NEL.SampleRate = 1
	s := &Server{cfg: cfg, logDir: t.TempDir()}
	r := chi.NewRouter()
	r.Post("/api/v1/server/reports/{name}", s.handleReports)
	body := `[{"type":"network-error","url":"https://example.com/path?token=secret","body":{"type":"tcp.dns.error","phase":"dns","method":"GET","protocol":"h2","status_code":0,"server_ip":"10.0.0.1","elapsed_time":3}}]`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/server/reports/nel", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/reports+json")
	req.RemoteAddr = "203.0.113.11:4444"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	logData, err := os.ReadFile(filepath.Join(s.logDir, "security.log"))
	if err != nil {
		t.Fatalf("read security log: %v", err)
	}
	got := string(logData)
	for _, want := range []string{"event=security.network_error_report", "type=tcp.dns.error", "phase=dns", "protocol=h2"} {
		if !strings.Contains(got, want) {
			t.Errorf("security log missing %q: %s", want, got)
		}
	}
	for _, secret := range []string{"token=secret", "10.0.0.1"} {
		if strings.Contains(got, secret) {
			t.Errorf("security log contains unneeded report data %q: %s", secret, got)
		}
	}
}

func TestReports_Rejections(t *testing.T) {
	resetReportLimiter(t, 100)
	if rec := postReport(t, "bogus", "application/json", `{}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown report name status=%d, want 404", rec.Code)
	}
	if rec := postReport(t, "csp", "text/html", `<b>`); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("bad content type status=%d, want 415", rec.Code)
	}
	big := strings.Repeat("a", reportsMaxBody+10)
	if rec := postReport(t, "csp", "application/json", big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized status=%d, want 413", rec.Code)
	}
}

func TestReports_RateLimitedPerIP(t *testing.T) {
	resetReportLimiter(t, 2)
	if rec := postReport(t, "csp", "application/json", `{}`); rec.Code != http.StatusNoContent {
		t.Fatalf("first request status=%d, want 204", rec.Code)
	}
	if rec := postReport(t, "csp", "application/json", `{}`); rec.Code != http.StatusNoContent {
		t.Fatalf("second request status=%d, want 204", rec.Code)
	}
	if rec := postReport(t, "csp", "application/json", `{}`); rec.Code != http.StatusTooManyRequests {
		t.Errorf("third request status=%d, want 429", rec.Code)
	}
}

func TestSanitizeReportURL(t *testing.T) {
	cases := map[string]string{
		"https://evil.example/x?token=secret#frag": "https://evil.example/x",
		"https://user:pw@evil.example/p?a=1":       "https://evil.example/p",
		"inline":                                   "inline",
		"https://e.example/\x00\x1bred":            "https://e.example/red",
		strings.Repeat("a", 500):                   strings.Repeat("a", reportField),
	}
	for in, want := range cases {
		if got := sanitizeReportURL(in); got != want {
			t.Errorf("sanitizeReportURL(%.30q) = %.40q, want %.40q", in, got, want)
		}
	}
}

func TestRateLimitAuditOncePerIPPerMinute(t *testing.T) {
	var calls int
	old := onRateLimited
	onRateLimited = func(*http.Request, string) { calls++ }
	t.Cleanup(func() { onRateLimited = old })

	rateLimitAuditMu.Lock()
	delete(rateLimitAuditLast, "198.51.100.77")
	rateLimitAuditMu.Unlock()

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	auditRateLimited(r, "198.51.100.77", "global_burst")
	auditRateLimited(r, "198.51.100.77", "global_burst")
	auditRateLimited(r, "198.51.100.77", "global_burst")
	if calls != 1 {
		t.Errorf("onRateLimited called %d times for one IP within a minute, want 1", calls)
	}
}

func TestConfigureReportLimits(t *testing.T) {
	oldMin, oldBurst := reportLimiter, reportBurstLimiter
	t.Cleanup(func() { reportLimiter, reportBurstLimiter = oldMin, oldBurst })

	configureReportLimits(100, 2)
	for i := 0; i < 2; i++ {
		if !reportBurstLimiter.allow("a") {
			t.Fatalf("report %d within the burst must pass", i+1)
		}
	}
	if reportBurstLimiter.allow("a") {
		t.Error("third report in the same second must exceed burst=2")
	}

	keepMin, keepBurst := reportLimiter, reportBurstLimiter
	configureReportLimits(0, 0)
	if reportLimiter != keepMin || reportBurstLimiter != keepBurst {
		t.Error("non-positive limits must leave the limiters unchanged")
	}
}

// TestReports_SampleRateZeroDropsCSPReports verifies a zero sample rate still
// answers 204 (the browser must never see an error) while skipping the log.
func TestReports_SampleRateZeroDropsCSPReports(t *testing.T) {
	resetReportLimiter(t, 100)
	cfg := config.DefaultConfig()
	cfg.Web.CSP.ReportsSampleRate = 0
	s := &Server{cfg: cfg}
	r := chi.NewRouter()
	r.Post("/api/v1/server/reports/{name}", s.handleReports)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/server/reports/csp",
		strings.NewReader(`{"csp-report":{"blocked-uri":"inline","violated-directive":"script-src"}}`))
	req.Header.Set("Content-Type", "application/csp-report")
	req.RemoteAddr = "203.0.113.10:4444"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("status=%d, want 204", rec.Code)
	}
}
