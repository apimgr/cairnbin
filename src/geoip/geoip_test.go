package geoip_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apimgr/cairnbin/src/geoip"
)

// ─── Open with all features disabled (no-op DB) ───────────────────────────────

func TestOpen_AllDisabled(t *testing.T) {
	dir := t.TempDir()
	db, err := geoip.Open(geoip.Config{
		Dir:           dir,
		EnableASN:     false,
		EnableCountry: false,
		EnableCity:    false,
	})
	if err != nil {
		t.Fatalf("Open(all disabled): %v", err)
	}
	if db == nil {
		t.Fatal("expected non-nil DB")
	}
	db.Close()
}

// ─── Lookup on no-op DB ───────────────────────────────────────────────────────

func TestLookup_NoopDB_ReturnsInfo(t *testing.T) {
	dir := t.TempDir()
	db, _ := geoip.Open(geoip.Config{Dir: dir})

	ip := net.ParseIP("8.8.8.8")
	info := db.Lookup(ip)
	// No databases loaded — info is returned with just the IP set, no country/ASN.
	if info == nil {
		t.Fatal("expected non-nil info from no-op DB")
	}
	if info.IP != "8.8.8.8" {
		t.Errorf("info.IP = %q; want 8.8.8.8", info.IP)
	}
}

func TestLookup_NilIP(t *testing.T) {
	dir := t.TempDir()
	db, _ := geoip.Open(geoip.Config{Dir: dir})

	if got := db.Lookup(nil); got != nil {
		t.Errorf("Lookup(nil): expected nil, got %+v", got)
	}
}

func TestLookup_NilDB(t *testing.T) {
	var db *geoip.DB
	if got := db.Lookup(net.ParseIP("1.2.3.4")); got != nil {
		t.Errorf("nil DB Lookup: expected nil, got %+v", got)
	}
}

// ─── IsBlocked ────────────────────────────────────────────────────────────────

func TestIsBlocked_NilDB(t *testing.T) {
	var db *geoip.DB
	if db.IsBlocked(net.ParseIP("1.2.3.4")) {
		t.Error("nil DB should never block")
	}
}

func TestIsBlocked_NilIP(t *testing.T) {
	dir := t.TempDir()
	db, _ := geoip.Open(geoip.Config{Dir: dir})
	if db.IsBlocked(nil) {
		t.Error("nil IP should never block")
	}
}

func TestIsBlocked_PrivateIP_NeverBlocked(t *testing.T) {
	dir := t.TempDir()
	db, _ := geoip.Open(geoip.Config{
		Dir:           dir,
		DenyCountries: []string{"US"},
	})

	privateIPs := []string{
		"127.0.0.1",
		"10.0.0.1",
		"172.16.0.1",
		"192.168.1.1",
	}
	for _, ipStr := range privateIPs {
		ip := net.ParseIP(ipStr)
		if db.IsBlocked(ip) {
			t.Errorf("IsBlocked(%s): private IP should never be blocked", ipStr)
		}
	}
}

func TestIsBlocked_AllowlistBypass(t *testing.T) {
	dir := t.TempDir()
	db, _ := geoip.Open(geoip.Config{
		Dir: dir,
		// only allow Germany
		AllowCountries: []string{"DE"},
		Allowlist:      []string{"1.2.3.4"},
	})

	// 1.2.3.4 is in the allowlist so should pass even though country is unknown.
	ip := net.ParseIP("1.2.3.4")
	if db.IsBlocked(ip) {
		t.Error("allowlisted IP should not be blocked")
	}
}

func TestIsBlocked_AllowlistCIDR(t *testing.T) {
	dir := t.TempDir()
	db, _ := geoip.Open(geoip.Config{
		Dir:            dir,
		AllowCountries: []string{"DE"},
		Allowlist:      []string{"1.2.3.0/24"},
	})

	ip := net.ParseIP("1.2.3.100")
	if db.IsBlocked(ip) {
		t.Error("IP in allowlisted CIDR should not be blocked")
	}
}

func TestIsBlocked_NoDenyList_NeverBlocks(t *testing.T) {
	dir := t.TempDir()
	db, _ := geoip.Open(geoip.Config{Dir: dir})

	// With no deny/allow lists, no public IP should be blocked.
	ip := net.ParseIP("8.8.8.8")
	if db.IsBlocked(ip) {
		t.Error("should not block when no country lists configured")
	}
}

// ─── Close ────────────────────────────────────────────────────────────────────

func TestClose_NoopDB(t *testing.T) {
	dir := t.TempDir()
	db, _ := geoip.Open(geoip.Config{Dir: dir})
	// Double-close should not panic.
	db.Close()
	db.Close()
}

// ─── LookupRequest ───────────────────────────────────────────────────────────

// TestLookupRequest_XRealIPIgnored verifies that LookupRequest reads
// r.RemoteAddr, not X-Real-IP. The server's realIPMiddleware (PART 12) is
// responsible for proxy-header extraction before geoip middleware runs.
func TestLookupRequest_XRealIPIgnored(t *testing.T) {
	dir := t.TempDir()
	db, _ := geoip.Open(geoip.Config{Dir: dir})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Real-IP", "8.8.8.8")
	req.RemoteAddr = "203.0.113.1:1234"

	info := db.LookupRequest(req)
	if info == nil {
		t.Fatal("expected non-nil info")
	}
	// Must reflect RemoteAddr, not the X-Real-IP header.
	if info.IP != "203.0.113.1" {
		t.Errorf("info.IP = %q; want 203.0.113.1 (RemoteAddr)", info.IP)
	}
}

// TestLookupRequest_XForwardedForIgnored verifies that LookupRequest reads
// r.RemoteAddr, not X-Forwarded-For. Proxy-header extraction is the server's
// realIPMiddleware responsibility (PART 12).
func TestLookupRequest_XForwardedForIgnored(t *testing.T) {
	dir := t.TempDir()
	db, _ := geoip.Open(geoip.Config{Dir: dir})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 10.0.0.1")
	req.RemoteAddr = "203.0.113.2:5678"

	info := db.LookupRequest(req)
	if info == nil {
		t.Fatal("expected non-nil info")
	}
	// Must reflect RemoteAddr, not the X-Forwarded-For header.
	if info.IP != "203.0.113.2" {
		t.Errorf("info.IP = %q; want 203.0.113.2 (RemoteAddr)", info.IP)
	}
}

func TestLookupRequest_RemoteAddr(t *testing.T) {
	dir := t.TempDir()
	db, _ := geoip.Open(geoip.Config{Dir: dir})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "5.6.7.8:12345"

	info := db.LookupRequest(req)
	if info == nil {
		t.Fatal("expected non-nil info")
	}
	if info.IP != "5.6.7.8" {
		t.Errorf("info.IP = %q; want 5.6.7.8", info.IP)
	}
}

// ─── Middleware ───────────────────────────────────────────────────────────────

func TestMiddleware_PassesNonBlocked(t *testing.T) {
	dir := t.TempDir()
	db, _ := geoip.Open(geoip.Config{Dir: dir})

	handler := db.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Real-IP", "8.8.8.8")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestMiddleware_FailOpenWhenCountryUnknown(t *testing.T) {
	dir := t.TempDir()
	// With no databases loaded, country lookups return an empty string. Per PART 19
	// (AI.md:26584) GeoIP must fail-open: a request whose country cannot be
	// determined is never blocked, even in allow-countries (allowlist) mode.
	db, _ := geoip.Open(geoip.Config{
		Dir:            dir,
		AllowCountries: []string{"US"},
	})

	handler := db.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Real-IP", "8.8.8.8")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 (fail-open) when country is unknown, got %d", rec.Code)
	}
}
