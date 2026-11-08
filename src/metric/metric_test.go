package metric

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestNormalizePath(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", "/"},
		{"root", "/", "/"},
		{"short word", "/pastes", "/pastes"},
		{"api path no id", "/api/v1/pastes", "/api/v1/pastes"},
		{"single id segment", "/abc12345", "/:id"},
		{"api path with id", "/api/v1/pastes/abc12345def0", "/api/v1/pastes/:id"},
		{"static asset", "/static/app.css", "/static/app.css"},
		{"uuid path", "/api/v1/user/a1b2c3d4-e5f6-7890-abcd-ef1234567890", "/api/v1/user/:id"},
		{"nested no id", "/api/v1/health", "/api/v1/health"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizePath(tc.input)
			if got != tc.want {
				t.Errorf("normalizePath(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestIsIDSegment(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"empty", "", false},
		{"too short", "abc", false},
		{"word pastes", "pastes", false},
		{"8 hex chars", "abc12345", true},
		{"12 hex chars", "abc12345def0", true},
		{"uuid", "a1b2c3d4-e5f6-7890-abcd-ef1234567890", true},
		{"word users", "users", false},
		{"mostly non-hex", "helloworld", false},
		{"exactly 6 non-hex", "getall", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isIDSegment(tc.input)
			if got != tc.want {
				t.Errorf("isIDSegment(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestNew_ReturnsCollector(t *testing.T) {
	c := New("1.0.0", "abc", "2024-01-01", time.Now(), "")
	if c == nil {
		t.Fatal("New returned nil")
	}
}

// TestCollector_Handler_NoToken_ServiceDisabled verifies the PART 20 rule that
// there is no unauthenticated default: with no token configured for the
// prometheus service, the service is disabled and the endpoint answers 403.
func TestCollector_Handler_NoToken_ServiceDisabled(t *testing.T) {
	c := New("1.0.0", "abc", "2024-01-01", time.Now(), "")
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

// TestCollector_Handler_AllowUnauthenticated verifies the firewalled-internal
// escape hatch: auth.allow_unauthenticated skips token checks for every metrics
// service, so a token-less collector serves 200.
func TestCollector_Handler_AllowUnauthenticated(t *testing.T) {
	c := NewWithOptions(Options{
		Version:              "1.0.0",
		Commit:               "abc",
		BuildDate:            "2024-01-01",
		StartTime:            time.Now(),
		IncludeRuntime:       true,
		AllowUnauthenticated: true,
	})
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestCollector_Handler_WithAuth_Unauthorized(t *testing.T) {
	c := New("1.0.0", "abc", "2024-01-01", time.Now(), "secret")
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestCollector_Handler_WithAuth_Authorized(t *testing.T) {
	c := New("1.0.0", "abc", "2024-01-01", time.Now(), "secret")
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/metrics", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer secret")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestCollector_Middleware_SkipsMetricsPath(t *testing.T) {
	c := New("1.0.0", "abc", "2024-01-01", time.Now(), "")

	innerCalled := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	handler := c.Middleware()(inner)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !innerCalled {
		t.Error("inner handler was not called for /metrics path")
	}
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
}

func TestCollector_Middleware_RecordsRequest(t *testing.T) {
	c := New("1.0.0", "abc", "2024-01-01", time.Now(), "")

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := c.Middleware()(inner)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()

	// Verify no panic and correct response propagated.
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
}

// TestResponseWriter_Write verifies that the responseWriter.Write method
// delegates to the underlying ResponseWriter and accumulates the byte count.
// The Middleware wraps the recorder with a responseWriter; writing a body
// exercises the Write method that was previously unreachable.
func TestResponseWriter_Write(t *testing.T) {
	c := New("1.0.0", "abc", "2024-01-01", time.Now(), "")

	body := []byte("hello world")
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(body); err != nil {
			t.Errorf("Write returned error: %v", err)
		}
	})

	handler := c.Middleware()(inner)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pastes", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if rr.Body.String() != string(body) {
		t.Errorf("body = %q, want %q", rr.Body.String(), string(body))
	}
}

// TestNewWithOptions_RuntimeAndSystem verifies that gating flags are honored
// and that collectRuntime/collectSystem run without panic when enabled.
func TestNewWithOptions_RuntimeAndSystem(t *testing.T) {
	c := NewWithOptions(Options{
		Version:         "2.0.0",
		Commit:          "def",
		BuildDate:       "2024-02-02",
		StartTime:       time.Now(),
		IncludeRuntime:  true,
		IncludeSystem:   true,
		DurationBuckets: []float64{0.01, 0.1, 1},
		SizeBuckets:     []float64{10, 100, 1000},
		DataDir:         t.TempDir(),
	})
	if c == nil {
		t.Fatal("NewWithOptions returned nil")
	}
	if !c.includeRuntime {
		t.Error("includeRuntime should be true")
	}
	// includeSystem is true only when the platform supports it.
	if c.includeSystem != systemStatsSupported() {
		t.Errorf("includeSystem = %v, want %v", c.includeSystem, systemStatsSupported())
	}
	// Two scrapes so CPU delta logic executes on the second pass.
	c.collectRuntime()
	c.collectRuntime()
}

// TestNewWithOptions_RuntimeDisabled verifies the runtime family is skipped.
func TestNewWithOptions_RuntimeDisabled(t *testing.T) {
	c := NewWithOptions(Options{
		Version:        "2.0.0",
		StartTime:      time.Now(),
		IncludeRuntime: false,
		IncludeSystem:  false,
	})
	if c.includeRuntime {
		t.Error("includeRuntime should be false")
	}
	if c.includeSystem {
		t.Error("includeSystem should be false")
	}
	// Should not panic and should still set uptime.
	c.collectRuntime()
}

// TestSetTorProvider verifies the Tor callback drives the tor_* gauges.
func TestSetTorProvider(t *testing.T) {
	c := New("1.0.0", "abc", "2024-01-01", time.Now(), "")
	c.SetTorProvider(func() (bool, bool) { return true, true })
	c.collectRuntime()

	if got := testutil.ToFloat64(TorEnabled); got != 1 {
		t.Errorf("TorEnabled = %v, want 1", got)
	}
	if got := testutil.ToFloat64(TorRunning); got != 1 {
		t.Errorf("TorRunning = %v, want 1", got)
	}
	if got := testutil.ToFloat64(TorCircuitEstablished); got != 1 {
		t.Errorf("TorCircuitEstablished = %v, want 1", got)
	}

	c.SetTorProvider(func() (bool, bool) { return false, false })
	c.collectRuntime()
	if got := testutil.ToFloat64(TorRunning); got != 0 {
		t.Errorf("TorRunning after disable = %v, want 0", got)
	}
}

// TestSetBool verifies the gauge boolean helper.
func TestSetBool(t *testing.T) {
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_setbool_gauge"})
	setBool(g, true)
	if got := testutil.ToFloat64(g); got != 1 {
		t.Errorf("setBool(true) = %v, want 1", got)
	}
	setBool(g, false)
	if got := testutil.ToFloat64(g); got != 0 {
		t.Errorf("setBool(false) = %v, want 0", got)
	}
}

// TestMiddleware_TorOnionCounts verifies .onion Host requests increment the
// Tor request counter.
func TestMiddleware_TorOnionCounts(t *testing.T) {
	c := New("1.0.0", "abc", "2024-01-01", time.Now(), "")
	before := testutil.ToFloat64(TorRequestsTotal)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := c.Middleware()(inner)

	req := httptest.NewRequest(http.MethodGet, "/paste", nil)
	req.Host = "abcdef1234567890.onion"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if after := testutil.ToFloat64(TorRequestsTotal); after != before+1 {
		t.Errorf("TorRequestsTotal = %v, want %v", after, before+1)
	}
}

// TestReadSystemStats exercises the portable system-stats reader.
func TestReadSystemStats(t *testing.T) {
	st, ok := readSystemStats(t.TempDir())
	if systemStatsSupported() {
		if !ok {
			t.Fatal("readSystemStats returned ok=false on supported platform")
		}
		if !st.cpuValid && st.memTotal == 0 {
			t.Error("system stats should include CPU or memory data")
		}
	} else if ok {
		t.Error("readSystemStats returned ok=true on unsupported platform")
	}
}
