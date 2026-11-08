package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/tor"
)

// writeTorTestServerYML writes a minimal server.yml with the given port so
// torServerBaseURL resolves it the same way --status does.
func writeTorTestServerYML(t *testing.T, dir, port string) {
	t.Helper()
	content := "server:\n  port: \"" + port + "\"\n  api_version: v1\n"
	if err := os.WriteFile(filepath.Join(dir, "server.yml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write server.yml: %v", err)
	}
}

// TestTorServerBaseURL_NoServer covers the case where no server is listening
// on the resolved port — torServerBaseURL must report serverUp=false so
// runTorCommand falls back to on-disk state.
func TestTorServerBaseURL_NoServer(t *testing.T) {
	dir := t.TempDir()
	writeTorTestServerYML(t, dir, "1") // port 1 is reserved, nothing listens there

	baseURL, up := torServerBaseURL(dir)
	if up {
		t.Errorf("torServerBaseURL(%q) = (%q, true), want up=false", dir, baseURL)
	}
	if baseURL != "" {
		t.Errorf("torServerBaseURL(%q) baseURL = %q, want empty when down", dir, baseURL)
	}
}

// TestTorServerBaseURL_NoConfig covers a config dir with no server.yml at
// all — config.Load errors are ignored and the default port/api_version
// fallback is used, so this must not panic and must report down.
func TestTorServerBaseURL_NoConfig(t *testing.T) {
	dir := t.TempDir()
	baseURL, up := torServerBaseURL(dir)
	if up || baseURL != "" {
		t.Errorf("torServerBaseURL(missing config) = (%q, %v), want (\"\", false)", baseURL, up)
	}
}

// TestTorServerBaseURL_ServerRunning starts a real HTTP server bound to
// 127.0.0.1 serving the healthz path the probe checks, and verifies
// torServerBaseURL detects it and returns the matching loopback base URL.
func TestTorServerBaseURL_ServerRunning(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/server/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	_, port, err := netSplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split host:port %q: %v", u.Host, err)
	}

	dir := t.TempDir()
	writeTorTestServerYML(t, dir, port)

	baseURL, up := torServerBaseURL(dir)
	if !up {
		t.Fatalf("torServerBaseURL(%q) up=false, want true (test server on port %s)", dir, port)
	}
	want := "http://127.0.0.1:" + port
	if baseURL != want {
		t.Errorf("torServerBaseURL(%q) baseURL = %q, want %q", dir, baseURL, want)
	}
}

func TestRunTorVanityUnknownActionUsesRequestedLanguage(t *testing.T) {
	var stdout, stderr strings.Builder
	code := runTorVanity("cairnbin", "bogus", nil, "", true, &stdout, &stderr, "es")
	if code != 2 {
		t.Fatalf("runTorVanity unknown action exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "desconocido") || !strings.Contains(stderr.String(), "Uso:") {
		t.Fatalf("Spanish unknown vanity action output = %q", stderr.String())
	}
}

func TestValidateTorConfigUsesRequestedLanguage(t *testing.T) {
	root := t.TempDir()
	cfg := tor.Config{ConfigDir: filepath.Join(root, "config"), DataDir: filepath.Join(root, "data"), VirtualPort: 70000, Binary: filepath.Join(root, "missing-tor")}
	var stdout, stderr strings.Builder
	code := validateTorConfig("cairnbin", cfg, &stdout, &stderr, "es")
	if code != 1 {
		t.Fatalf("validateTorConfig exit = %d, want 1", code)
	}
	for _, want := range []string{"AVISO", "ERROR", "No se encontró el binario Tor", "fuera del intervalo"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("Spanish validation output %q missing %q", stdout.String(), want)
		}
	}
}

// netSplitHostPort is a tiny wrapper so this file doesn't need to import
// net solely for SplitHostPort in one place.
func netSplitHostPort(hostport string) (host, port string, err error) {
	i := strings.LastIndex(hostport, ":")
	if i < 0 {
		return hostport, "", errNoPort
	}
	return hostport[:i], hostport[i+1:], nil
}

var errNoPort = &strconvErr{"missing port in address"}

type strconvErr struct{ s string }

func (e *strconvErr) Error() string { return e.s }

// TestTorControlRequest_Success covers a successful envelope decode.
func TestTorControlRequest_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"data":{"hostname":"abc.onion"}}`))
	}))
	defer srv.Close()

	env, err := torControlRequest(http.MethodPost, srv.URL, "/server/tor/regenerate", []byte(`{}`), "application/json")
	if err != nil {
		t.Fatalf("torControlRequest: unexpected error: %v", err)
	}
	if !strings.Contains(string(env.Data), "abc.onion") {
		t.Errorf("env.Data = %s, want to contain abc.onion", env.Data)
	}
}

// TestTorControlRequest_ErrorEnvelope covers a well-formed but ok:false
// response — torControlRequest must surface env.Message as the error.
func TestTorControlRequest_ErrorEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"ok":false,"error":"CONFLICT","message":"tor manager not running"}`))
	}))
	defer srv.Close()

	_, err := torControlRequest(http.MethodPost, srv.URL, "/server/tor/restart", nil, "")
	if err == nil {
		t.Fatal("torControlRequest: expected error for ok:false envelope, got nil")
	}
	if !strings.Contains(err.Error(), "tor manager not running") {
		t.Errorf("error = %v, want to contain server message", err)
	}
}

// TestTorControlRequest_ErrorEnvelopeNoMessage covers the ok:false-but-empty-
// message fallback text.
func TestTorControlRequest_ErrorEnvelopeNoMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"ok":false}`))
	}))
	defer srv.Close()

	_, err := torControlRequest(http.MethodPost, srv.URL, "/server/tor/restart", nil, "")
	if err == nil || !strings.Contains(err.Error(), "request failed") {
		t.Errorf("error = %v, want fallback 'request failed'", err)
	}
}

// TestTorControlRequest_NetworkError covers a request to an address nothing
// is listening on.
func TestTorControlRequest_NetworkError(t *testing.T) {
	_, err := torControlRequest(http.MethodGet, "http://127.0.0.1:1", "/server/tor/status", nil, "")
	if err == nil {
		t.Fatal("torControlRequest: expected network error, got nil")
	}
}

// TestTorControlRequest_BadJSON covers a response body that isn't valid JSON.
func TestTorControlRequest_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	_, err := torControlRequest(http.MethodGet, srv.URL, "/server/tor/status", nil, "")
	if err == nil {
		t.Fatal("torControlRequest: expected decode error, got nil")
	}
}

// TestPrintTorInfo covers all three rendering branches.
func TestPrintTorInfo(t *testing.T) {
	cases := []struct {
		name string
		info torControlTorInfo
		want []string
		none []string
	}{
		{
			name: "disabled",
			info: torControlTorInfo{Enabled: false},
			want: []string{"disabled"},
			none: []string{"Address:"},
		},
		{
			name: "enabled_not_running",
			info: torControlTorInfo{Enabled: true, Running: false},
			want: []string{"not running"},
			none: []string{"Address:"},
		},
		{
			name: "running_with_hostname",
			info: torControlTorInfo{Enabled: true, Running: true, Hostname: "abc.onion"},
			want: []string{"Connected", "abc.onion"},
		},
		{
			name: "running_without_hostname",
			info: torControlTorInfo{Enabled: true, Running: true},
			want: []string{"Connected"},
			none: []string{"Address:"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			printTorInfo(&out, tc.info)
			got := out.String()
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("printTorInfo(%+v) = %q, want to contain %q", tc.info, got, w)
				}
			}
			for _, n := range tc.none {
				if strings.Contains(got, n) {
					t.Errorf("printTorInfo(%+v) = %q, want NOT to contain %q", tc.info, got, n)
				}
			}
		})
	}
}

func TestPrintTorInfoUsesRequestedLanguage(t *testing.T) {
	var out strings.Builder
	printTorInfo(&out, torControlTorInfo{Enabled: true, Running: true, Hostname: "example.onion"}, "es")
	got := out.String()
	if !strings.Contains(got, "conectado") || !strings.Contains(got, "Dirección") || !strings.Contains(got, "example.onion") {
		t.Fatalf("Spanish Tor status = %q", got)
	}
}

// TestRunTorVanity_NoServer covers every "tor vanity" subcommand's hard
// failure when no running server is detected — these mutate server-owned
// state and have no on-disk fallback (AI.md PART 31.1).
func TestRunTorVanity_NoServer(t *testing.T) {
	for _, action := range []string{"start", "stop", "apply"} {
		var out, errOut strings.Builder
		code := runTorVanity("cairnbin", action, []string{"abcdef"}, "", false, &out, &errOut)
		if code != 1 {
			t.Errorf("runTorVanity(%q, serverUp=false) = %d, want 1", action, code)
		}
		if !strings.Contains(strings.ToLower(errOut.String()), "no running server detected") {
			t.Errorf("runTorVanity(%q, serverUp=false) stderr = %q, want 'no running server detected'", action, errOut.String())
		}
	}
}

// TestRunTorVanity_StartNoPrefix covers the missing-prefix usage error.
func TestRunTorVanity_StartNoPrefix(t *testing.T) {
	var out, errOut strings.Builder
	code := runTorVanity("cairnbin", "start", nil, "http://127.0.0.1:9", true, &out, &errOut)
	if code != 2 {
		t.Errorf("runTorVanity(start, no prefix) = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "prefix") {
		t.Errorf("runTorVanity(start, no prefix) stderr = %q, want to mention 'prefix'", errOut.String())
	}
}

// TestRunTorVanity_UnknownAction covers the default case.
func TestRunTorVanity_UnknownAction(t *testing.T) {
	var out, errOut strings.Builder
	code := runTorVanity("cairnbin", "bogus", nil, "http://127.0.0.1:9", true, &out, &errOut)
	if code != 2 {
		t.Errorf("runTorVanity(bogus) = %d, want 2", code)
	}
	if !strings.Contains(strings.ToLower(errOut.String()), "unknown tor vanity subcommand") {
		t.Errorf("runTorVanity(bogus) stderr = %q, want 'unknown tor vanity subcommand'", errOut.String())
	}
}

// TestRunTorVanity_StartSuccess covers a full round trip against a fake
// control-channel server for "tor vanity start", including the --workers
// flag being forwarded in the JSON body.
func TestRunTorVanity_StartSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/server/tor/vanity/start" {
			t.Errorf("path = %s, want /server/tor/vanity/start", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"prefix":"abcdef"`) {
			t.Errorf("request body = %s, want prefix abcdef", body)
		}
		if !strings.Contains(string(body), `"workers":2`) {
			t.Errorf("request body = %s, want workers 2", body)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"data":{"state":"running","prefix":"abcdef","workers":2}}`))
	}))
	defer srv.Close()

	var out, errOut strings.Builder
	code := runTorVanity("cairnbin", "start", []string{"abcdef", "--workers", "2"}, srv.URL, true, &out, &errOut)
	if code != 0 {
		t.Fatalf("runTorVanity(start) = %d, want 0; stderr=%q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "abcdef") || !strings.Contains(out.String(), "2 worker") {
		t.Errorf("runTorVanity(start) stdout = %q, want to mention prefix and worker count", out.String())
	}
}

// TestRunTorVanity_StartBadWorkers covers the flag-parsing usage error path.
func TestRunTorVanity_StartBadWorkers(t *testing.T) {
	var out, errOut strings.Builder
	code := runTorVanity("cairnbin", "start", []string{"abcdef", "--workers=notanumber"}, "http://127.0.0.1:9", true, &out, &errOut)
	if code != 2 {
		t.Errorf("runTorVanity(start, bad --workers) = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "invalid --workers value") {
		t.Errorf("stderr = %q, want 'invalid --workers value'", errOut.String())
	}
}

// TestParseVanityStartArgs covers both accepted flag syntaxes and the error
// cases, per the project-wide "--flag=value and --flag value" rule.
func TestParseVanityStartArgs(t *testing.T) {
	prefix, workers, err := parseVanityStartArgs([]string{"abc"})
	if err != nil || prefix != "abc" || workers != 0 {
		t.Fatalf("bare prefix = (%q, %d, %v)", prefix, workers, err)
	}
	prefix, workers, err = parseVanityStartArgs([]string{"--workers=4", "abc"})
	if err != nil || prefix != "abc" || workers != 4 {
		t.Fatalf("--workers=N = (%q, %d, %v)", prefix, workers, err)
	}
	prefix, workers, err = parseVanityStartArgs([]string{"abc", "--workers", "3"})
	if err != nil || prefix != "abc" || workers != 3 {
		t.Fatalf("--workers N = (%q, %d, %v)", prefix, workers, err)
	}
	if _, _, err := parseVanityStartArgs([]string{"abc", "--workers"}); err == nil {
		t.Fatal("--workers without a value must error")
	}
	if _, _, err := parseVanityStartArgs([]string{"abc", "--bogus"}); err == nil {
		t.Fatal("unknown flag must error")
	}
	if _, _, err := parseVanityStartArgs([]string{"abc", "def"}); err == nil {
		t.Fatal("second positional argument must error")
	}
}

// TestRunTorVanity_StopSuccess covers both stop outcomes: an actually
// cancelled search and the no-op message when nothing was running.
func TestRunTorVanity_StopSuccess(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "stopped",
			body: `{"ok":true,"data":{"stopped":true,"vanity":{"state":"found","candidates":["abcdefxyz.onion"]}}}`,
			want: "Vanity search stopped",
		},
		{
			name: "not_running",
			body: `{"ok":true,"data":{"stopped":false,"vanity":{"state":"idle"}}}`,
			want: "No vanity search is running",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/server/tor/vanity/stop" {
					t.Errorf("path = %s, want /server/tor/vanity/stop", r.URL.Path)
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			var out, errOut strings.Builder
			code := runTorVanity("cairnbin", "stop", nil, srv.URL, true, &out, &errOut)
			if code != 0 {
				t.Fatalf("runTorVanity(stop) = %d, want 0; stderr=%q", code, errOut.String())
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Errorf("runTorVanity(stop) stdout = %q, want %q", out.String(), tc.want)
			}
		})
	}
}

// TestRunTorVanity_ApplySuccess covers a full round trip for "tor vanity
// apply", including the destructive-action confirmation prompt.
func TestRunTorVanity_ApplySuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/server/tor/vanity/apply" {
			t.Errorf("path = %s, want /server/tor/vanity/apply", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"data":{"hostname":"abcdefxyz.onion"}}`))
	}))
	defer srv.Close()

	restore := confirmInput
	confirmInput = strings.NewReader("y\n")
	defer func() { confirmInput = restore }()

	var out, errOut strings.Builder
	code := runTorVanity("cairnbin", "apply", nil, srv.URL, true, &out, &errOut)
	if code != 0 {
		t.Fatalf("runTorVanity(apply) = %d, want 0; stderr=%q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "abcdefxyz.onion") {
		t.Errorf("runTorVanity(apply) stdout = %q, want to mention hostname", out.String())
	}
}

// TestRunTorVanity_ApplyDeclined covers the operator answering "no" at the
// destructive-action prompt — nothing is sent to the server.
func TestRunTorVanity_ApplyDeclined(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server must not be contacted after a declined confirmation")
	}))
	defer srv.Close()

	restore := confirmInput
	confirmInput = strings.NewReader("n\n")
	defer func() { confirmInput = restore }()

	var out, errOut strings.Builder
	code := runTorVanity("cairnbin", "apply", nil, srv.URL, true, &out, &errOut)
	if code != 1 {
		t.Errorf("runTorVanity(apply, declined) = %d, want 1", code)
	}
	if !strings.Contains(out.String(), "Aborted") {
		t.Errorf("stdout = %q, want 'Aborted'", out.String())
	}
}

// TestRunTorVanity_StartServerError covers the control channel returning an
// error envelope (e.g. a search is already running server-side).
func TestRunTorVanity_StartServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"ok":false,"error":"CONFLICT","message":"a vanity search for \"abcdef\" is already running"}`))
	}))
	defer srv.Close()

	var out, errOut strings.Builder
	code := runTorVanity("cairnbin", "start", []string{"abcdef"}, srv.URL, true, &out, &errOut)
	if code != 1 {
		t.Errorf("runTorVanity(start, server error) = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "already running") {
		t.Errorf("runTorVanity(start, server error) stderr = %q, want server message", errOut.String())
	}
}

// TestPrintVanityInfo covers the progress rendering branches: silent when
// idle with nothing found, running with sampled counters, and found with
// candidates listed.
func TestPrintVanityInfo(t *testing.T) {
	var idle strings.Builder
	printVanityInfo(&idle, torControlVanityInfo{State: "idle"})
	if idle.String() != "" {
		t.Errorf("idle state should render nothing, got %q", idle.String())
	}

	var empty strings.Builder
	printVanityInfo(&empty, torControlVanityInfo{})
	if empty.String() != "" {
		t.Errorf("absent vanity block should render nothing, got %q", empty.String())
	}

	var running strings.Builder
	printVanityInfo(&running, torControlVanityInfo{
		State:          "running",
		Prefix:         "abc",
		Workers:        3,
		Attempts:       4096,
		Rate:           512,
		ElapsedSeconds: 8,
	})
	for _, want := range []string{"running", "abc", "Workers: 3", "4096"} {
		if !strings.Contains(strings.ToLower(running.String()), strings.ToLower(want)) {
			t.Errorf("running output = %q, want to contain %q", running.String(), want)
		}
	}

	var found strings.Builder
	printVanityInfo(&found, torControlVanityInfo{State: "found", Candidates: []string{"abcxyz.onion"}})
	if !strings.Contains(found.String(), "Candidate: abcxyz.onion") {
		t.Errorf("found output = %q, want the candidate listed", found.String())
	}
}

// TestRunTorCommand_RestartNoServer covers the hard-fail path shared by
// restart/regenerate/import-keys when no running server is detected.
func TestRunTorCommand_RestartNoServer(t *testing.T) {
	oldConfig := os.Getenv("CONFIG_DIR")
	oldData := os.Getenv("DATA_DIR")
	dir := t.TempDir()
	t.Setenv("CONFIG_DIR", dir)
	t.Setenv("DATA_DIR", dir)
	defer func() {
		_ = os.Setenv("CONFIG_DIR", oldConfig)
		_ = os.Setenv("DATA_DIR", oldData)
	}()
	writeTorTestServerYML(t, dir, "1")

	var out, errOut strings.Builder
	code := runTorCommand("cairnbin", []string{"restart"}, &out, &errOut)
	if code != 1 {
		t.Errorf("runTorCommand(restart, no server) = %d, want 1; stderr=%q", code, errOut.String())
	}
	if !strings.Contains(strings.ToLower(errOut.String()), "no running server detected") {
		t.Errorf("runTorCommand(restart, no server) stderr = %q, want 'no running server detected'", errOut.String())
	}
}

// TestRunTorCommand_NoSubcommand covers the usage error when "tor" is
// invoked with no subcommand.
func TestRunTorCommand_NoSubcommand(t *testing.T) {
	var out, errOut strings.Builder
	code := runTorCommand("cairnbin", nil, &out, &errOut)
	if code != 2 {
		t.Errorf("runTorCommand(no args) = %d, want 2", code)
	}
	if !strings.Contains(strings.ToLower(errOut.String()), "tor requires an action") {
		t.Errorf("runTorCommand(no args) stderr = %q, want 'tor requires an action'", errOut.String())
	}
}

var _ = strconv.Itoa // keep strconv import stable if future edits trim usage above
