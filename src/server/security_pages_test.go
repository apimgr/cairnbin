package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/config"
	"github.com/apimgr/cairnbin/src/database"
)

// ─── sameUTCDate ─────────────────────────────────────────────────────────────

func TestSameUTCDate(t *testing.T) {
	base := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		a, b time.Time
		want bool
	}{
		{
			name: "same instant",
			a:    base,
			b:    base,
			want: true,
		},
		{
			name: "same day different time",
			a:    time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC),
			b:    time.Date(2026, 7, 5, 23, 59, 59, 0, time.UTC),
			want: true,
		},
		{
			name: "different day",
			a:    time.Date(2026, 7, 5, 23, 59, 59, 0, time.UTC),
			b:    time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC),
			want: false,
		},
		{
			name: "same local time different UTC day",
			a:    time.Date(2026, 7, 5, 23, 0, 0, 0, time.FixedZone("EST", -5*3600)),
			b:    time.Date(2026, 7, 6, 4, 0, 0, 0, time.UTC),
			want: true,
		},
		{
			name: "different month",
			a:    time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC),
			b:    time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
			want: false,
		},
		{
			name: "different year",
			a:    time.Date(2025, 12, 31, 12, 0, 0, 0, time.UTC),
			b:    time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameUTCDate(tc.a, tc.b); got != tc.want {
				t.Errorf("sameUTCDate = %v, want %v", got, tc.want)
			}
		})
	}
}

// ─── securityReportClosedAt ───────────────────────────────────────────────────

func TestSecurityReportClosedAt(t *testing.T) {
	disclosedTime := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	updatedTime := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)

	cases := []struct {
		name       string
		rep        *database.SecurityReport
		wantClosed bool
		wantTime   time.Time
	}{
		{
			name: "disclosed with disclosed_at set",
			rep: &database.SecurityReport{
				Status:      database.SecStatusDisclosed,
				DisclosedAt: &disclosedTime,
				UpdatedAt:   updatedTime,
			},
			wantClosed: true,
			wantTime:   disclosedTime,
		},
		{
			name: "disclosed with nil disclosed_at falls back to updated_at",
			rep: &database.SecurityReport{
				Status:      database.SecStatusDisclosed,
				DisclosedAt: nil,
				UpdatedAt:   updatedTime,
			},
			wantClosed: true,
			wantTime:   updatedTime,
		},
		{
			name: "wont fix uses updated_at",
			rep: &database.SecurityReport{
				Status:    database.SecStatusWontFix,
				UpdatedAt: updatedTime,
			},
			wantClosed: true,
			wantTime:   updatedTime,
		},
		{
			name: "received is not closed",
			rep: &database.SecurityReport{
				Status:    database.SecStatusReceived,
				UpdatedAt: updatedTime,
			},
			wantClosed: false,
		},
		{
			name: "triaged is not closed",
			rep: &database.SecurityReport{
				Status:    database.SecStatusTriaged,
				UpdatedAt: updatedTime,
			},
			wantClosed: false,
		},
		{
			name: "patching is not closed",
			rep: &database.SecurityReport{
				Status:    database.SecStatusPatching,
				UpdatedAt: updatedTime,
			},
			wantClosed: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, closed := securityReportClosedAt(tc.rep)
			if closed != tc.wantClosed {
				t.Errorf("closed = %v, want %v", closed, tc.wantClosed)
			}
			if tc.wantClosed && !got.Equal(tc.wantTime) {
				t.Errorf("time = %v, want %v", got, tc.wantTime)
			}
			if !tc.wantClosed && !got.IsZero() {
				t.Errorf("expected zero time when not closed, got %v", got)
			}
		})
	}
}

// ─── buildAckEntries ──────────────────────────────────────────────────────────

func TestBuildAckEntries_Empty(t *testing.T) {
	entries := buildAckEntries(nil)
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for nil input, got %d", len(entries))
	}
}

func TestBuildAckEntries_Named(t *testing.T) {
	disclosedAt := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	reports := []*database.SecurityReport{
		{
			CreditPreference: "name",
			CreditName:       "Alice Example",
			Severity:         "High",
			Component:        "auth",
			DisclosedAt:      &disclosedAt,
		},
	}
	entries := buildAckEntries(reports)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Display != "Alice Example" {
		t.Errorf("Display = %q, want %q", e.Display, "Alice Example")
	}
	if e.Year != 2026 {
		t.Errorf("Year = %d, want 2026", e.Year)
	}
	if e.Severity != "High" {
		t.Errorf("Severity = %q, want %q", e.Severity, "High")
	}
	if e.Component != "auth" {
		t.Errorf("Component = %q, want %q", e.Component, "auth")
	}
}

func TestBuildAckEntries_AnonymousPreference(t *testing.T) {
	reports := []*database.SecurityReport{
		{CreditPreference: "anonymous", CreditName: "Bob", Severity: "Low", Component: "api"},
		{CreditPreference: "anonymous", CreditName: "", Severity: "Medium", Component: "ui"},
	}
	entries := buildAckEntries(reports)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Display != "Anonymous Researcher #1" {
		t.Errorf("first anon = %q, want %q", entries[0].Display, "Anonymous Researcher #1")
	}
	if entries[1].Display != "Anonymous Researcher #2" {
		t.Errorf("second anon = %q, want %q", entries[1].Display, "Anonymous Researcher #2")
	}
}

func TestBuildAckEntries_EmptyNameBecomesAnon(t *testing.T) {
	reports := []*database.SecurityReport{
		{CreditPreference: "name", CreditName: "  ", Severity: "Critical", Component: "core"},
	}
	entries := buildAckEntries(reports)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Display != "Anonymous Researcher #1" {
		t.Errorf("empty-name entry = %q, want %q", entries[0].Display, "Anonymous Researcher #1")
	}
}

func TestBuildAckEntries_NilDisclosedAt(t *testing.T) {
	reports := []*database.SecurityReport{
		{CreditPreference: "name", CreditName: "Charlie", Severity: "Low", Component: "misc", DisclosedAt: nil},
	}
	entries := buildAckEntries(reports)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Year != 0 {
		t.Errorf("Year = %d, want 0 for nil DisclosedAt", entries[0].Year)
	}
}

// ─── handlePGPKey ─────────────────────────────────────────────────────────────

func TestHandlePGPKey_DisabledConfig(t *testing.T) {
	cfg := &config.Config{}
	s := newMinimalServer(cfg)
	s.configDir = t.TempDir()

	r := httptest.NewRequest(http.MethodGet, "/.well-known/pgp-key.asc", nil)
	w := httptest.NewRecorder()
	s.handlePGPKey(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("handlePGPKey with disabled config: status = %d, want 404", w.Code)
	}
}

func TestHasPGPKey_DisabledConfig(t *testing.T) {
	cfg := &config.Config{}
	s := newMinimalServer(cfg)
	s.configDir = t.TempDir()

	if s.hasPGPKey() {
		t.Error("hasPGPKey() should return false when config disables PGP publishing")
	}
}

// ─── handleSecurityOverview / handleSecurityPolicy / handleSecurityThanks ─────

func TestHandleSecurityOverview_Returns200(t *testing.T) {
	db := &stubDB{}
	cfg := config.DefaultConfig()
	s := New(db, cfg, nil, "1.0.0", "abc", "now", t.TempDir(), t.TempDir())

	r := httptest.NewRequest(http.MethodGet, "/server/security", nil)
	r.Host = "localhost"
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("/server/security status = %d, want 200", w.Code)
	}
}

func TestHandleSecurityPolicy_Returns200(t *testing.T) {
	db := &stubDB{}
	cfg := config.DefaultConfig()
	s := New(db, cfg, nil, "1.0.0", "abc", "now", t.TempDir(), t.TempDir())

	r := httptest.NewRequest(http.MethodGet, "/server/security/policy", nil)
	r.Host = "localhost"
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("/server/security/policy status = %d, want 200", w.Code)
	}
}

func TestHandleSecurityThanks_Returns200(t *testing.T) {
	db := &stubDB{}
	cfg := config.DefaultConfig()
	s := New(db, cfg, nil, "1.0.0", "abc", "now", t.TempDir(), t.TempDir())

	r := httptest.NewRequest(http.MethodGet, "/server/security/thanks", nil)
	r.Host = "localhost"
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("/server/security/thanks status = %d, want 200", w.Code)
	}
}

// ─── handleSecurityReportStatus ───────────────────────────────────────────────

func TestHandleSecurityReportStatus_MissingTrackingID(t *testing.T) {
	db := &stubDB{}
	cfg := config.DefaultConfig()
	s := New(db, cfg, nil, "1.0.0", "abc", "now", t.TempDir(), t.TempDir())

	r := httptest.NewRequest(http.MethodGet, "/server/security/report/%20%20%20?token=abc123", nil)
	r.Host = "localhost"
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)

	if w.Code == http.StatusOK {
		t.Errorf("expected non-200 for blank tracking_id, got 200")
	}
}

func TestHandleSecurityReportStatus_NotFound(t *testing.T) {
	db := &stubDB{}
	cfg := config.DefaultConfig()
	s := New(db, cfg, nil, "1.0.0", "abc", "now", t.TempDir(), t.TempDir())

	r := httptest.NewRequest(http.MethodGet, "/server/security/report/sec_missing?token=tok123", nil)
	r.Host = "localhost"
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("unknown report status = %d, want 404", w.Code)
	}
}

// ─── handleSecurityOverview / handleSecurityPolicy text/plain variant ─────────

func TestHandleSecurityOverview_TextPlain(t *testing.T) {
	db := &stubDB{}
	cfg := config.DefaultConfig()
	s := New(db, cfg, nil, "1.0.0", "abc", "now", t.TempDir(), t.TempDir())

	r := httptest.NewRequest(http.MethodGet, "/server/security", nil)
	r.Header.Set("Accept", "text/plain")
	r.Host = "localhost"
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("/server/security text/plain status = %d, want 200", w.Code)
	}
}

func TestHandleSecurityPolicy_TextPlain(t *testing.T) {
	db := &stubDB{}
	cfg := config.DefaultConfig()
	s := New(db, cfg, nil, "1.0.0", "abc", "now", t.TempDir(), t.TempDir())

	r := httptest.NewRequest(http.MethodGet, "/server/security/policy", nil)
	r.Header.Set("Accept", "text/plain")
	r.Host = "localhost"
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("/server/security/policy text/plain status = %d, want 200", w.Code)
	}
}

func TestHandleSecurityThanks_TextPlain(t *testing.T) {
	db := &stubDB{}
	cfg := config.DefaultConfig()
	s := New(db, cfg, nil, "1.0.0", "abc", "now", t.TempDir(), t.TempDir())

	r := httptest.NewRequest(http.MethodGet, "/server/security/thanks", nil)
	r.Header.Set("Accept", "text/plain")
	r.Host = "localhost"
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("/server/security/thanks text/plain status = %d, want 200", w.Code)
	}
}
