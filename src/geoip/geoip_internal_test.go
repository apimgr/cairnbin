package geoip

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

type geoipRoundTripper func(*http.Request) (*http.Response, error)

func (f geoipRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOpenDefersMissingDatabaseDownload(t *testing.T) {
	called := false
	client := &http.Client{Transport: geoipRoundTripper(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("unexpected outbound request before Tor bootstrap")
	})}
	db, err := Open(Config{Dir: t.TempDir(), EnableCountry: true, HTTPClient: client, DeferDownloads: true})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if called {
		t.Fatal("Open attempted a network download despite DeferDownloads")
	}
}

// ─── mustStat ─────────────────────────────────────────────────────────────────

func TestMustStat_ExistingFile(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "test.bin")
	content := []byte("hello world")
	if err := os.WriteFile(f, content, 0o644); err != nil {
		t.Fatal(err)
	}
	got := mustStat(f)
	if got != int64(len(content)) {
		t.Errorf("mustStat = %d; want %d", got, len(content))
	}
}

func TestMustStat_MissingFile(t *testing.T) {
	got := mustStat("/nonexistent/path/to/file.bin")
	if got != 0 {
		t.Errorf("mustStat missing = %d; want 0", got)
	}
}

// ─── ensureFile ───────────────────────────────────────────────────────────────

func TestEnsureFile_AlreadyPresent(t *testing.T) {
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "data.mmdb")
	if err := os.WriteFile(dst, []byte("non-empty content"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Should not touch the network — src is an invalid URL that would fail.
	if err := ensureFile(dst, "http://127.0.0.1:1/nonexistent"); err != nil {
		t.Errorf("ensureFile for existing file should return nil, got: %v", err)
	}
}

func TestEnsureFile_Downloads(t *testing.T) {
	payload := []byte("fake mmdb binary content")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	dst := filepath.Join(tmp, "asn.mmdb")

	if err := ensureFile(dst, srv.URL+"/asn.mmdb"); err != nil {
		t.Fatalf("ensureFile download: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile after download: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("downloaded content = %q; want %q", got, payload)
	}
}

func TestEnsureFile_BadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	dst := filepath.Join(tmp, "bad.mmdb")

	err := ensureFile(dst, srv.URL+"/bad.mmdb")
	if err == nil {
		t.Error("expected error for non-200 status, got nil")
	}
}

func TestEnsureFile_Unreachable(t *testing.T) {
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "unreach.mmdb")

	err := ensureFile(dst, "http://127.0.0.1:1/unreach.mmdb")
	if err == nil {
		t.Error("expected error for unreachable server, got nil")
	}
}

// ─── Open ─────────────────────────────────────────────────────────────────────

func TestOpen_NoDBsEnabled(t *testing.T) {
	tmp := t.TempDir()
	cfg := Config{Dir: tmp}

	db, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open with no DBs enabled: %v", err)
	}
	if db == nil {
		t.Fatal("Open returned nil DB")
	}
	db.Close()
}

func TestOpen_InvalidMmdbFile_ASN(t *testing.T) {
	// Pre-create a non-empty fake file so ensureFile is a no-op;
	// maxminddb.Open will fail with an invalid file format error,
	// covering the "geoip: warning: open asn.mmdb" log path.
	tmp := t.TempDir()
	asnPath := filepath.Join(tmp, "asn.mmdb")
	if err := os.WriteFile(asnPath, []byte("not a real mmdb file"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Dir: tmp, EnableASN: true}

	db, err := Open(cfg)
	// Open should succeed even when the DB file is invalid (logs a warning).
	if err != nil {
		t.Fatalf("Open with invalid mmdb: unexpected error %v", err)
	}
	if db == nil {
		t.Fatal("Open returned nil DB")
	}
	db.Close()
}

func TestOpen_InvalidMmdbFile_Country(t *testing.T) {
	tmp := t.TempDir()
	countryPath := filepath.Join(tmp, "country.mmdb")
	if err := os.WriteFile(countryPath, []byte("not a real mmdb file"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Dir: tmp, EnableCountry: true}

	db, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open with invalid country mmdb: %v", err)
	}
	if db == nil {
		t.Fatal("Open returned nil DB")
	}
	db.Close()
}

func TestOpen_InvalidMmdbFile_City(t *testing.T) {
	// Guard against a download: Open looks for the two PART 19 city files by
	// their exact names, so a stub written under any other name would send
	// ensureFile to the real CDN and block on its 5-minute context timeout.
	serveDownloadFailure(t)

	tmp := t.TempDir()
	// PART 19 requires separate IPv4 and IPv6 city databases; a single
	// combined city.mmdb is not sufficient. Both must be stubbed.
	for _, name := range []string{"dbip-city-ipv4.mmdb", "dbip-city-ipv6.mmdb"} {
		p := filepath.Join(tmp, name)
		if err := os.WriteFile(p, []byte("not a real mmdb file"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{Dir: tmp, EnableCity: true}

	db, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open with invalid city mmdb: %v", err)
	}
	if db == nil {
		t.Fatal("Open returned nil DB")
	}
	db.Close()
}

// ─── Update ───────────────────────────────────────────────────────────────────

func TestUpdate_NoDBsEnabled(t *testing.T) {
	tmp := t.TempDir()
	cfg := Config{Dir: tmp}

	db, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Update with no DBs enabled is a no-op returning nil.
	if err := db.Update(); err != nil {
		t.Errorf("Update with no DBs: expected nil, got %v", err)
	}
}

// ─── Lookup ───────────────────────────────────────────────────────────────────

func TestLookup_NilDB(t *testing.T) {
	var d *DB
	if got := d.Lookup(nil); got != nil {
		t.Errorf("nil DB Lookup = %v; want nil", got)
	}
}

func TestLookup_NilIP(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(Config{Dir: tmp})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := db.Lookup(nil); got != nil {
		t.Errorf("Lookup(nil IP) = %v; want nil", got)
	}
}

func TestLookup_SkipsPrivateAndInternalIPs(t *testing.T) {
	db, err := Open(Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	called := false
	db.countryOverride = func(net.IP) string {
		called = true
		return "US"
	}
	for _, value := range []string{"10.1.2.3", "192.168.1.1", "127.0.0.1", "169.254.1.1", "fc00::1", "::1"} {
		if got := db.Lookup(net.ParseIP(value)); got != nil {
			t.Errorf("Lookup(%s) = %+v; internal addresses must not be looked up", value, got)
		}
	}
	if called {
		t.Error("country data lookup ran for a private/internal address")
	}
}

func TestLookup_ValidIP_NoDBsLoaded(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(Config{Dir: tmp})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ip := net.ParseIP("8.8.8.8")
	got := db.Lookup(ip)
	if got == nil {
		t.Fatal("Lookup(8.8.8.8) = nil; want non-nil info")
	}
	if got.IP != "8.8.8.8" {
		t.Errorf("info.IP = %q; want 8.8.8.8", got.IP)
	}
}

// ─── Close ────────────────────────────────────────────────────────────────────

func TestClose_WithNoDB(t *testing.T) {
	tmp := t.TempDir()
	db, err := Open(Config{Dir: tmp})
	if err != nil {
		t.Fatal(err)
	}
	// Call Close twice — should not panic.
	db.Close()
	db.Close()
}

// ─── IsBlocked ────────────────────────────────────────────────────────────────

func TestIsBlocked_NilDB(t *testing.T) {
	var d *DB
	if d.IsBlocked(net.ParseIP("1.2.3.4")) {
		t.Error("nil DB IsBlocked should return false")
	}
}

func TestIsBlocked_NilIP(t *testing.T) {
	tmp := t.TempDir()
	db, _ := Open(Config{Dir: tmp})
	defer db.Close()
	if db.IsBlocked(nil) {
		t.Error("IsBlocked(nil) should return false")
	}
}

func TestIsBlocked_PrivateIP_NotBlocked(t *testing.T) {
	tmp := t.TempDir()
	db, _ := Open(Config{Dir: tmp, AllowCountries: []string{"US"}})
	defer db.Close()

	for _, ipStr := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "::1"} {
		ip := net.ParseIP(ipStr)
		if db.IsBlocked(ip) {
			t.Errorf("private/loopback IP %s should not be blocked", ipStr)
		}
	}
}

func TestIsBlocked_AllowlistIP_NotBlocked(t *testing.T) {
	tmp := t.TempDir()
	db, _ := Open(Config{
		Dir:            tmp,
		Allowlist:      []string{"8.8.8.8"},
		AllowCountries: []string{"US"},
	})
	defer db.Close()

	if db.IsBlocked(net.ParseIP("8.8.8.8")) {
		t.Error("allowlisted IP 8.8.8.8 should not be blocked")
	}
}

func TestIsBlocked_AllowlistCIDR_NotBlocked(t *testing.T) {
	tmp := t.TempDir()
	db, _ := Open(Config{
		Dir:            tmp,
		Allowlist:      []string{"8.8.0.0/16"},
		AllowCountries: []string{"US"},
	})
	defer db.Close()

	if db.IsBlocked(net.ParseIP("8.8.4.4")) {
		t.Error("IP in allowlisted CIDR should not be blocked")
	}
}

func TestIsBlocked_DenyCountriesEmpty_NotBlocked(t *testing.T) {
	tmp := t.TempDir()
	db, _ := Open(Config{Dir: tmp})
	defer db.Close()

	// No country DBs loaded → country is "" → neither allow nor deny matches → not blocked
	if db.IsBlocked(net.ParseIP("8.8.8.8")) {
		t.Error("should not block when no country policy configured")
	}
}

// ─── LookupRequest ────────────────────────────────────────────────────────────

func TestLookupRequest_RemoteAddr(t *testing.T) {
	tmp := t.TempDir()
	db, _ := Open(Config{Dir: tmp})
	defer db.Close()

	req, _ := http.NewRequest("GET", "/", nil)
	req.RemoteAddr = "1.2.3.4:5678"
	got := db.LookupRequest(req)
	if got == nil {
		t.Error("LookupRequest with valid RemoteAddr should return non-nil")
	}
}

// TestLookupRequest_XRealIPIgnored verifies that LookupRequest reads
// r.RemoteAddr only. X-Real-IP is never read here; the server's
// realIPMiddleware (PART 12) handles proxy-header extraction before geoip runs.
func TestLookupRequest_XRealIPIgnored(t *testing.T) {
	tmp := t.TempDir()
	db, _ := Open(Config{Dir: tmp})
	defer db.Close()

	req, _ := http.NewRequest("GET", "/", nil)
	req.Header.Set("X-Real-IP", "5.6.7.8")
	req.RemoteAddr = "203.0.113.3:9999"
	got := db.LookupRequest(req)
	if got == nil {
		t.Fatal("LookupRequest with valid RemoteAddr should return non-nil")
	}
	if got.IP != "203.0.113.3" {
		t.Errorf("got.IP = %q; want 203.0.113.3 (RemoteAddr, not X-Real-IP)", got.IP)
	}
}

// TestLookupRequest_XForwardedForIgnored verifies that LookupRequest reads
// r.RemoteAddr only. X-Forwarded-For is never read here; the server's
// realIPMiddleware (PART 12) handles proxy-header extraction before geoip runs.
func TestLookupRequest_XForwardedForIgnored(t *testing.T) {
	tmp := t.TempDir()
	db, _ := Open(Config{Dir: tmp})
	defer db.Close()

	req, _ := http.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-For", "9.10.11.12, 1.1.1.1")
	req.RemoteAddr = "203.0.113.4:8888"
	got := db.LookupRequest(req)
	if got == nil {
		t.Fatal("LookupRequest with valid RemoteAddr should return non-nil")
	}
	if got.IP != "203.0.113.4" {
		t.Errorf("got.IP = %q; want 203.0.113.4 (RemoteAddr, not X-Forwarded-For)", got.IP)
	}
}

// ─── Middleware ────────────────────────────────────────────────────────────────

func TestMiddleware_AllowsNonBlockedIP(t *testing.T) {
	tmp := t.TempDir()
	db, _ := Open(Config{Dir: tmp})
	defer db.Close()

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	mw := db.Middleware()(next)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	mw.ServeHTTP(w, req)

	if !called {
		t.Error("next handler should be called for non-blocked IP")
	}
}

func TestMiddleware_BlocksPublicIPNotInAllowCountries(t *testing.T) {
	tmp := t.TempDir()
	// AllowCountries=["US"] and the IP resolves to CN (override stands in for a
	// real MMDB) → CN is not allowed → the request must be blocked with 403.
	db, _ := Open(Config{Dir: tmp, AllowCountries: []string{"US"}})
	db.countryOverride = func(net.IP) string { return "CN" }
	defer db.Close()

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mw := db.Middleware()(next)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/", nil)
	req.RemoteAddr = "8.8.8.8:1234"
	mw.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for blocked IP, got %d", w.Code)
	}
}

// ─── IsBlocked — additional paths ────────────────────────────────────────────

func TestIsBlocked_AllowCountries_BlocksNonAllowedCountry(t *testing.T) {
	tmp := t.TempDir()
	db, _ := Open(Config{Dir: tmp, AllowCountries: []string{"US"}})
	db.countryOverride = func(net.IP) string { return "CN" }
	defer db.Close()

	// 8.8.8.8 resolves to CN which is not in AllowCountries=[US] → blocked.
	if !db.IsBlocked(net.ParseIP("8.8.8.8")) {
		t.Error("CN IP should be blocked when AllowCountries=[US]")
	}
}

func TestIsBlocked_AllowCountries_FailOpenUnknownCountry(t *testing.T) {
	tmp := t.TempDir()
	db, _ := Open(Config{Dir: tmp, AllowCountries: []string{"US"}})
	defer db.Close()

	// No country DB → cc="" → fail-open, never blocked (PART 19, AI.md:27279).
	if db.IsBlocked(net.ParseIP("8.8.8.8")) {
		t.Error("8.8.8.8 must not be blocked when its country is undeterminable")
	}
}

func TestIsBlocked_DenyCountries_NoMatch_NotBlocked(t *testing.T) {
	tmp := t.TempDir()
	db, _ := Open(Config{Dir: tmp, DenyCountries: []string{"CN"}})
	defer db.Close()

	// No country DB → cc="" → doesn't match "CN" → not blocked.
	if db.IsBlocked(net.ParseIP("8.8.8.8")) {
		t.Error("8.8.8.8 should not be blocked when DenyCountries=[CN] and no country DB")
	}
}

// ─── isPrivate — additional CIDR ranges ──────────────────────────────────────

func TestIsPrivate_RFC172Range(t *testing.T) {
	tmp := t.TempDir()
	db, _ := Open(Config{Dir: tmp, AllowCountries: []string{"US"}})
	defer db.Close()

	// 172.16.0.1 is in the 172.16.0.0/12 private range.
	if db.IsBlocked(net.ParseIP("172.16.0.1")) {
		t.Error("172.16.0.1 is RFC 1918 private — should not be blocked")
	}
}

func TestIsPrivate_CGNATRange(t *testing.T) {
	tmp := t.TempDir()
	db, _ := Open(Config{Dir: tmp, AllowCountries: []string{"US"}})
	defer db.Close()

	// 100.64.0.1 is in CGNAT range 100.64.0.0/10.
	if db.IsBlocked(net.ParseIP("100.64.0.1")) {
		t.Error("100.64.0.1 is CGNAT — should not be blocked")
	}
}

func TestIsPrivate_ULAIPv6(t *testing.T) {
	tmp := t.TempDir()
	db, _ := Open(Config{Dir: tmp, AllowCountries: []string{"US"}})
	defer db.Close()

	// fc00::1 is in the ULA range fc00::/7.
	if db.IsBlocked(net.ParseIP("fc00::1")) {
		t.Error("fc00::1 is ULA IPv6 — should not be blocked")
	}
}

// ─── Update — with DBs enabled (download errors expected in test) ─────────────

// TestUpdate_WithEnabledDBs_ReturnsOnDownloadError verifies that Update
// returns a non-nil error when every database download fails. The URLs point at
// a local server that always answers 500, so the failure is immediate and
// deterministic — without this the test would contact the real CDN and block on
// refreshMMDB's 5-minute context timeout, per database.
func TestUpdate_WithEnabledDBs_ReturnsOnDownloadError(t *testing.T) {
	serveDownloadFailure(t)

	tmp := t.TempDir()
	cfg := Config{
		Dir:           tmp,
		EnableASN:     true,
		EnableCountry: true,
		EnableCity:    true,
	}
	db, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Update re-downloads each configured database; every download fails.
	err = db.Update()
	if err == nil {
		t.Fatal("Update returned nil error; want non-nil when all downloads fail")
	}
}

func TestUpdate_PreservesExistingDatabasesWhenReplacementDownloadFails(t *testing.T) {
	serveMinimalMMDB(t)
	tmp := t.TempDir()
	db, err := Open(Config{Dir: tmp, EnableASN: true, EnableCountry: true, EnableCity: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	paths := []string{"asn.mmdb", "country.mmdb", "dbip-city-ipv4.mmdb", "dbip-city-ipv6.mmdb"}
	before := make(map[string][]byte, len(paths))
	for _, name := range paths {
		before[name], err = os.ReadFile(filepath.Join(tmp, name))
		if err != nil {
			t.Fatal(err)
		}
	}
	serveDownloadFailure(t)
	if err := db.Update(); err == nil {
		t.Fatal("Update returned nil error; want replacement download failure")
	}
	for _, name := range paths {
		after, readErr := os.ReadFile(filepath.Join(tmp, name))
		if readErr != nil {
			t.Fatalf("existing database %s was removed: %v", name, readErr)
		}
		if !bytes.Equal(after, before[name]) {
			t.Errorf("existing database %s changed after failed update", name)
		}
	}
	if db.asnDB == nil || db.countDB == nil || db.cityDB == nil || db.cityDBv6 == nil {
		t.Fatal("failed update discarded an active database reader")
	}
}

// ─── country blocking modes (AI.md PART 19) ──────────────────────────────────

// TestCountryMode_NoLists verifies the default: with both lists empty there is
// no country blocking at all, so every country is allowed.
func TestCountryMode_NoLists(t *testing.T) {
	tmp := t.TempDir()
	db, _ := Open(Config{Dir: tmp})
	db.countryOverride = func(net.IP) string { return "CN" }
	defer db.Close()

	if db.IsBlocked(net.ParseIP("8.8.8.8")) {
		t.Error("empty deny+allow lists must not country-block")
	}
}

// TestCountryMode_Deny verifies deny_countries blocks the listed countries and
// allows all others.
func TestCountryMode_Deny(t *testing.T) {
	tmp := t.TempDir()
	db, _ := Open(Config{
		Dir:           tmp,
		DenyCountries: []string{"CN"},
	})
	db.countryOverride = func(net.IP) string { return "CN" }
	defer db.Close()

	if !db.IsBlocked(net.ParseIP("8.8.8.8")) {
		t.Error("deny mode must block a denied country")
	}

	db.countryOverride = func(net.IP) string { return "DE" }
	if db.IsBlocked(net.ParseIP("8.8.8.8")) {
		t.Error("deny mode must not block a country outside the deny list")
	}
}

// TestCountryMode_Allow verifies allow_countries blocks everything not listed,
// passes listed countries, and fails open when the country is undeterminable.
func TestCountryMode_Allow(t *testing.T) {
	tmp := t.TempDir()
	db, _ := Open(Config{
		Dir:            tmp,
		AllowCountries: []string{"US"},
	})
	db.countryOverride = func(net.IP) string { return "CN" }
	defer db.Close()

	if !db.IsBlocked(net.ParseIP("8.8.8.8")) {
		t.Error("allow mode must block a non-allowed country")
	}

	db.countryOverride = func(net.IP) string { return "US" }
	if db.IsBlocked(net.ParseIP("8.8.8.8")) {
		t.Error("allow mode must pass an allowed country")
	}

	// GeoIP is a risk signal, never the sole gate: a lookup that cannot
	// resolve a country must not block the request.
	db.countryOverride = nil
	if db.IsBlocked(net.ParseIP("8.8.8.8")) {
		t.Error("allow mode must fail open when country is undeterminable")
	}
}

// TestCountryMode_AllowWins verifies that when both lists are set,
// allow_countries takes precedence — the spec's documented behavior.
func TestCountryMode_AllowWins(t *testing.T) {
	tmp := t.TempDir()
	db, _ := Open(Config{
		Dir:            tmp,
		DenyCountries:  []string{"DE"},
		AllowCountries: []string{"US"},
	})
	db.countryOverride = func(net.IP) string { return "DE" }
	defer db.Close()

	// DE is in the deny list but not the allow list; allow mode wins, so it
	// is blocked.
	if !db.IsBlocked(net.ParseIP("8.8.8.8")) {
		t.Error("allow_countries must take precedence when both lists are set")
	}
}
