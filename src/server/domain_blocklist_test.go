package server

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/config"
)

func TestDomainBlocklistMiddlewareTargets(t *testing.T) {
	cases := []struct {
		name   string
		origin string
		body   string
		type_  string
		want   int
	}{
		{name: "origin host", origin: "https://bad.example/path", want: http.StatusForbidden},
		{name: "referer when origin absent", body: "", want: http.StatusOK},
		{name: "form email", body: "email=person%40bad.example", type_: "application/x-www-form-urlencoded", want: http.StatusForbidden},
		{name: "form URL", body: "reported_url=https%3A%2F%2Fbad.example%2Fa", type_: "application/x-www-form-urlencoded", want: http.StatusForbidden},
		{name: "JSON URL field", body: `{"nested":{"target_url":"https://bad.example/path"}}`, type_: "application/json", want: http.StatusForbidden},
		{name: "wildcard does not match apex", body: "url=https%3A%2F%2Fexample.co.uk", type_: "application/x-www-form-urlencoded", want: http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			blockDir := filepath.Join(dir, "security", "blocklists")
			if err := os.MkdirAll(blockDir, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(blockDir, "domains.txt"), []byte("bad.example\n*.example.co.uk\n"), 0o640); err != nil {
				t.Fatal(err)
			}
			cfg := config.DefaultConfig()
			cfg.Server.DataDir = dir
			cfg.Server.Security.Blocklists.Enabled = true
			cfg.Server.Security.Blocklists.ExtraSources = []config.BlocklistSource{{ID: "domains", Type: "domain", URL: "https://lists.invalid/domains"}}
			s := newMinimalServer(cfg)
			called := false
			handler := s.blocklistMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				_, _ = w.Write([]byte("accepted"))
			}))
			method := http.MethodPost
			if tc.body == "" {
				method = http.MethodGet
			}
			request := httptest.NewRequest(method, "http://service.example/submit", strings.NewReader(tc.body))
			request.RemoteAddr = "203.0.113.18:1234"
			if tc.origin != "" {
				request.Header.Set("Origin", tc.origin)
			}
			if tc.name == "referer when origin absent" {
				request.Header.Set("Referer", "https://bad.example/source")
				tc.want = http.StatusForbidden
			}
			if tc.type_ != "" {
				request.Header.Set("Content-Type", tc.type_)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.want {
				t.Fatalf("response status = %d, want %d: %s", response.Code, tc.want, response.Body.String())
			}
			if called != (tc.want == http.StatusOK) {
				t.Errorf("downstream handler called = %v, want %v", called, tc.want == http.StatusOK)
			}
		})
	}
}

func TestBlocklistHealthUsesCachedResultsUntilSchedulerRefresh(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Server.DataDir = dataDir
	cfg.Server.Security.Blocklists.Enabled = true
	cfg.Server.Security.Blocklists.ExtraSources = []config.BlocklistSource{{ID: "ads", Type: "domain", URL: "https://lists.example/ads.txt"}}
	s := newMinimalServer(cfg)
	s.dataDir = dataDir
	s.resolvedProxyIPs = make(map[string][]net.IP)
	s.registerBlocklistHealthChecks(cfg)
	s.ReloadBlocklists()
	if got := s.datasetChecks.Snapshot()[1].Status; got != "error" {
		t.Fatalf("missing configured list status = %q, want error", got)
	}
	path := filepath.Join(dataDir, "security", "blocklists", "ads.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("ads.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := s.datasetChecks.Snapshot()[1].Status; got != "error" {
		t.Fatalf("snapshot re-read files without refresh: got %q, want cached error", got)
	}
	s.RefreshDatasetChecks()
	if got := s.datasetChecks.Snapshot()[1].Status; got != "ok" {
		t.Fatalf("refreshed list status = %q, want ok", got)
	}
}

func TestBlocklistHealthUsesFixed72HourMaxAge(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Security.Blocklists.Enabled = true
	cfg.Server.Security.Blocklists.ExtraSources = []config.BlocklistSource{{ID: "ads", Type: "domain", URL: "https://lists.example/ads.txt"}}
	s := newMinimalServer(cfg)
	s.registerBlocklistHealthChecks(cfg)
	loadedAt := time.Now().UTC()
	s.blocklistCache = &blocklistStore{sources: map[string]blocklistSourceHealth{"ads": {loaded: true, loadedAt: loadedAt}}}
	if got := s.datasetChecks.Refresh(loadedAt.Add(71 * time.Hour))[1].Status; got != "ok" {
		t.Fatalf("blocklist status at 71 hours = %q, want ok", got)
	}
	if got := s.datasetChecks.Refresh(loadedAt.Add(73 * time.Hour))[1].Status; got != "degraded" {
		t.Fatalf("blocklist status at 73 hours = %q, want degraded", got)
	}
}

func TestRequestDomainTargetsPreferOriginAndIgnoreHost(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "http://service.example/?url=https%3A%2F%2Fsubmitted.example", strings.NewReader(url.Values{"email": {"user@contact.example"}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Host", "server.example")
	request.Header.Set("Origin", "https://origin.example/path")
	request.Header.Set("Referer", "https://referer.example/path")
	targets := requestDomainTargets(request)
	joined := strings.Join(targets, " ")
	for _, want := range []string{"https://origin.example/path", "https://submitted.example", "user@contact.example"} {
		if !strings.Contains(joined, want) {
			t.Errorf("domain targets %q omit %q", joined, want)
		}
	}
	if strings.Contains(joined, "referer.example") || strings.Contains(joined, "server.example") {
		t.Errorf("domain targets must prefer Origin and ignore request Host: %q", joined)
	}
}
