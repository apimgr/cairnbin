package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ─── versionLessThan ─────────────────────────────────────────────────────────

func TestVersionLessThan_MajorDifference(t *testing.T) {
	if !versionLessThan("1.0.0", "2.0.0") {
		t.Error("1.0.0 should be less than 2.0.0")
	}
	if versionLessThan("2.0.0", "1.0.0") {
		t.Error("2.0.0 should not be less than 1.0.0")
	}
}

func TestVersionLessThan_MinorDifference(t *testing.T) {
	if !versionLessThan("1.0.0", "1.1.0") {
		t.Error("1.0.0 should be less than 1.1.0")
	}
	if !versionLessThan("0.9.0", "0.10.0") {
		t.Error("0.9.0 should be less than 0.10.0 (numeric, not lexicographic)")
	}
}

func TestVersionLessThan_PatchDifference(t *testing.T) {
	if !versionLessThan("1.0.0", "1.0.1") {
		t.Error("1.0.0 should be less than 1.0.1")
	}
	if versionLessThan("1.0.1", "1.0.0") {
		t.Error("1.0.1 should not be less than 1.0.0")
	}
}

func TestVersionLessThan_EqualVersions(t *testing.T) {
	if versionLessThan("1.2.3", "1.2.3") {
		t.Error("equal versions should not be less than")
	}
}

func TestVersionLessThan_DevVersions(t *testing.T) {
	if versionLessThan("dev", "1.0.0") {
		t.Error("dev should not compare as less than any release")
	}
	if versionLessThan("1.0.0", "dev") {
		t.Error("release should not compare as less than dev")
	}
	if versionLessThan("unknown", "1.0.0") {
		t.Error("unknown should not compare as less than any release")
	}
}

func TestVersionLessThan_TwoPartVersion(t *testing.T) {
	// Short versions are padded with .0
	if !versionLessThan("1.0", "1.1") {
		t.Error("1.0 should be less than 1.1 (short form)")
	}
}

func TestVersionLessThan_NonNumericComponent(t *testing.T) {
	// Non-numeric — falls back to string comparison; must not panic.
	_ = versionLessThan("1.0.alpha", "1.0.beta")
	_ = versionLessThan("1.0.beta", "1.0.alpha")
}

// ─── detectLang ───────────────────────────────────────────────────────────────

func TestDetectLang_KnownExtensions(t *testing.T) {
	cases := []struct {
		file string
		want string
	}{
		{"main.go", "go"},
		{"script.py", "python"},
		{"app.js", "javascript"},
		{"app.ts", "typescript"},
		{"lib.rs", "rust"},
		{"Main.java", "java"},
		{"main.c", "c"},
		{"main.cpp", "cpp"},
		{"Main.cs", "csharp"},
		{"index.php", "php"},
		{"script.rb", "ruby"},
		{"run.sh", "bash"},
		{"run.bash", "bash"},
		{"run.zsh", "bash"},
		{"script.ps1", "powershell"},
		{"index.html", "html"},
		{"index.htm", "html"},
		{"style.css", "css"},
		{"data.json", "json"},
		{"config.yaml", "yaml"},
		{"config.yml", "yaml"},
		{"Cargo.toml", "toml"},
		{"data.xml", "xml"},
		{"query.sql", "sql"},
		{"README.md", "markdown"},
		{"notes.txt", "text"},
		{"file.cc", "cpp"},
	}
	for _, tc := range cases {
		got := detectLang(tc.file)
		if got != tc.want {
			t.Errorf("detectLang(%q) = %q; want %q", tc.file, got, tc.want)
		}
	}
}

func TestDetectLang_UnknownExtension(t *testing.T) {
	if got := detectLang("file.zig"); got != "text" {
		t.Errorf("detectLang(file.zig) = %q; want text", got)
	}
}

func TestDetectLang_NoExtension(t *testing.T) {
	if got := detectLang("Makefile"); got != "text" {
		t.Errorf("detectLang(Makefile) = %q; want text", got)
	}
}

func TestDetectLang_UppercaseExtension(t *testing.T) {
	// Extension lookup is case-insensitive on the whole filename.
	if got := detectLang("main.GO"); got != "go" {
		t.Errorf("detectLang(main.GO) = %q; want go", got)
	}
}

// ─── isValidURL ───────────────────────────────────────────────────────────────

func TestIsValidURL_HTTP(t *testing.T) {
	if !isValidURL("http://example.com") {
		t.Error("http:// should be valid")
	}
}

func TestIsValidURL_HTTPS(t *testing.T) {
	if !isValidURL("https://example.com") {
		t.Error("https:// should be valid")
	}
}

func TestIsValidURL_FTP(t *testing.T) {
	if isValidURL("ftp://example.com") {
		t.Error("ftp:// should not be valid")
	}
}

func TestIsValidURL_Empty(t *testing.T) {
	if isValidURL("") {
		t.Error("empty string should not be valid")
	}
}

func TestIsValidURL_NoScheme(t *testing.T) {
	if isValidURL("example.com") {
		t.Error("bare domain should not be valid")
	}
}

// ─── envOrDefault ─────────────────────────────────────────────────────────────

func TestEnvOrDefault_Unset(t *testing.T) {
	_ = os.Unsetenv("TEST_CAIRNBIN_CLI_KEY_XYZ")
	if got := envOrDefault("TEST_CAIRNBIN_CLI_KEY_XYZ", "fallback"); got != "fallback" {
		t.Errorf("got %q; want fallback", got)
	}
}

func TestEnvOrDefault_Set(t *testing.T) {
	t.Setenv("TEST_CAIRNBIN_CLI_KEY_XYZ", "fromenv")
	defer func() { _ = os.Unsetenv("TEST_CAIRNBIN_CLI_KEY_XYZ") }()
	if got := envOrDefault("TEST_CAIRNBIN_CLI_KEY_XYZ", "fallback"); got != "fromenv" {
		t.Errorf("got %q; want fromenv", got)
	}
}

// ─── saveIfUnset ─────────────────────────────────────────────────────

func TestSaveIfEmptyOrInvalid_FlagEmpty(t *testing.T) {
	valid := func(s string) bool { return s != "" }
	resolved, persist := saveIfUnset("current", "", valid)
	if resolved != "current" {
		t.Errorf("resolved = %q; want current", resolved)
	}
	if persist {
		t.Error("expected persist=false when flag is empty")
	}
}

func TestSaveIfEmptyOrInvalid_FlagInvalid(t *testing.T) {
	neverValid := func(s string) bool { return false }
	resolved, persist := saveIfUnset("current", "invalid-value", neverValid)
	if resolved != "current" {
		t.Errorf("resolved = %q; want current", resolved)
	}
	if persist {
		t.Error("expected persist=false when flag value is invalid")
	}
}

func TestSaveIfEmptyOrInvalid_CurrentEmptyFlagValid(t *testing.T) {
	valid := func(s string) bool { return s != "" }
	resolved, persist := saveIfUnset("", "https://new.example.com", valid)
	if resolved != "https://new.example.com" {
		t.Errorf("resolved = %q; want https://new.example.com", resolved)
	}
	if !persist {
		t.Error("expected persist=true when current is empty and flag is valid")
	}
}

func TestSaveIfEmptyOrInvalid_BothValid_NoAutoSave(t *testing.T) {
	valid := func(s string) bool { return s != "" }
	resolved, persist := saveIfUnset("https://old.example.com", "https://new.example.com", valid)
	if resolved != "https://new.example.com" {
		t.Errorf("resolved = %q; want https://new.example.com", resolved)
	}
	if persist {
		t.Error("expected persist=false when both current and flag are valid")
	}
}

func TestSaveIfEmptyOrInvalid_CurrentInvalidFlagValid(t *testing.T) {
	onlyNew := func(s string) bool { return s == "https://new.example.com" }
	resolved, persist := saveIfUnset("invalid-old", "https://new.example.com", onlyNew)
	if resolved != "https://new.example.com" {
		t.Errorf("resolved = %q; want https://new.example.com", resolved)
	}
	if !persist {
		t.Error("expected persist=true when current is invalid and flag is valid")
	}
}

// ─── detectMode ───────────────────────────────────────────────────────────────

func TestDetectMode_HelpFlag(t *testing.T) {
	for _, flag := range []string{"-h", "--help"} {
		if got := detectMode([]string{flag}, ""); got != "cli" {
			t.Errorf("detectMode([%s]) = %q; want cli", flag, got)
		}
	}
}

func TestDetectMode_VersionFlag(t *testing.T) {
	for _, flag := range []string{"-v", "--version"} {
		if got := detectMode([]string{flag}, ""); got != "cli" {
			t.Errorf("detectMode([%s]) = %q; want cli", flag, got)
		}
	}
}

func TestDetectMode_CommandArg(t *testing.T) {
	// A non-flag arg like "list" means CLI mode (also gets "plain" when not a tty).
	got := detectMode([]string{"list"}, "")
	if got != "cli" && got != "plain" {
		t.Errorf("detectMode([list]) = %q; want cli or plain", got)
	}
}

func TestDetectMode_EmptyArgs_NotTTY(t *testing.T) {
	// In the test environment, stdout is not a terminal — expect "plain" or "tui"
	// when no args are given. The exact result depends on whether we can detect
	// the terminal, so we just check it does not panic.
	got := detectMode([]string{}, "")
	if got != "tui" && got != "plain" && got != "cli" {
		t.Errorf("detectMode([]) = %q; want tui, plain, or cli", got)
	}
}

func TestDetectMode_ConfigFlagOnly(t *testing.T) {
	// --server is a config flag; with only config flags the mode is still tui/plain.
	got := detectMode([]string{"--server=https://example.com"}, "")
	if got != "tui" && got != "plain" {
		t.Errorf("detectMode([--server=...]) = %q; want tui or plain", got)
	}
}

func TestDetectMode_UnknownFlagWithValue(t *testing.T) {
	// An unknown flag triggers CLI mode.
	got := detectMode([]string{"--unknown-flag"}, "")
	if got != "cli" && got != "plain" {
		t.Errorf("detectMode([--unknown-flag]) = %q; want cli or plain", got)
	}
}

// cliConfigPath itself has moved to the paths package (paths.ConfigFile);
// see src/client/paths/paths_test.go for its coverage.

// ─── loadCLIConfig ────────────────────────────────────────────────────────────

func TestLoadCLIConfig_MissingFile_ReturnsDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLI_CONFIG", filepath.Join(dir, "nonexistent.yml"))

	cfg, err := loadCLIConfig()
	if err != nil {
		t.Fatalf("expected nil error for missing file, got %v", err)
	}
	if cfg.Update.Channel != "stable" {
		t.Errorf("default channel = %q; want stable", cfg.Update.Channel)
	}
	if cfg.Update.CheckInterval != "per_invocation" {
		t.Errorf("default check_interval = %q; want per_invocation", cfg.Update.CheckInterval)
	}
	if cfg.Display.Mode != "auto" {
		t.Errorf("default display mode = %q; want auto", cfg.Display.Mode)
	}
}

func TestLoadCLIConfig_ValidYAML(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "cli.yml")
	content := "server:\n  primary: https://paste.example.com\n"
	if err := os.WriteFile(cfgFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLI_CONFIG", cfgFile)

	cfg, err := loadCLIConfig()
	if err != nil {
		t.Fatalf("loadCLIConfig: %v", err)
	}
	if cfg.Server.Primary != "https://paste.example.com" {
		t.Errorf("server = %q; want https://paste.example.com", cfg.Server.Primary)
	}
}

func TestLoadCLIConfig_InvalidYAML_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "cli.yml")
	if err := os.WriteFile(cfgFile, []byte(":\t: invalid yaml\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLI_CONFIG", cfgFile)

	_, err := loadCLIConfig()
	if err == nil {
		t.Error("expected error for invalid YAML, got nil")
	}
}

// ─── saveCLIConfig ────────────────────────────────────────────────────────────

func TestSaveCLIConfig_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "subdir", "cli.yml")
	t.Setenv("CLI_CONFIG", cfgFile)

	cfg := cliConfig{}
	cfg.Server.Primary = "https://paste.example.com"
	if err := saveCLIConfig(cfg); err != nil {
		t.Fatalf("saveCLIConfig: %v", err)
	}

	data, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) == 0 {
		t.Error("saved cli.yml is empty")
	}
}

// ─── checkCLIUpdate ───────────────────────────────────────────────────────────

func TestCheckCLIUpdate_EmptyServerReturnsNil(t *testing.T) {
	if err := checkCLIUpdate("", ""); err != nil {
		t.Errorf("expected nil for empty server, got %v", err)
	}
}

func TestCheckCLIUpdate_DevVersionReturnsNil(t *testing.T) {
	orig := Version
	Version = "devel"
	defer func() { Version = orig }()

	if err := checkCLIUpdate("https://example.com", ""); err != nil {
		t.Errorf("expected nil for dev version, got %v", err)
	}
}

func TestCheckCLIUpdate_UnreachableServerReturnsNil(t *testing.T) {
	orig := Version
	Version = "1.0.0"
	defer func() { Version = orig }()

	// Unreachable server — the function swallows network errors and returns nil.
	if err := checkCLIUpdate("http://127.0.0.1:1/", ""); err != nil {
		t.Errorf("expected nil for unreachable server, got %v", err)
	}
}

// ─── printUsage ───────────────────────────────────────────────────────────────

func TestPrintUsage_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("printUsage panicked: %v", r)
		}
	}()
	printUsage()
}

// ─── client.url ──────────────────────────────────────────────────────────────

func TestClientURL_ConcatsPath(t *testing.T) {
	c := &client{server: "https://paste.example.com"}
	got := c.url("/raw/abc123")
	if got != "https://paste.example.com/raw/abc123" {
		t.Errorf("url() = %q; want https://paste.example.com/raw/abc123", got)
	}
}

func TestClientURL_EmptyPath(t *testing.T) {
	c := &client{server: "https://paste.example.com"}
	got := c.url("")
	if got != "https://paste.example.com" {
		t.Errorf("url() = %q; want https://paste.example.com", got)
	}
}

// ─── client.get ──────────────────────────────────────────────────────────────

func TestClientGet_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "cairnbin-cli/") {
			t.Errorf("expected cairnbin-cli User-Agent, got %q", r.Header.Get("User-Agent"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("paste content"))
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	resp, err := c.get("/raw/test123")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d; want 200", resp.StatusCode)
	}
}

func TestClientGet_InvalidURL_ReturnsError(t *testing.T) {
	c := &client{server: "http://\x00invalid"}
	_, err := c.get("/path")
	if err == nil {
		t.Error("expected error for invalid URL, got nil")
	}
}

// ─── client.postJSON ─────────────────────────────────────────────────────────

func TestClientPostJSON_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q; want application/json", ct)
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body["content"] != "hello" {
			t.Errorf("body content = %v; want hello", body["content"])
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "abc", "link": "http://example.com/abc"})
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	resp, err := c.postJSON("/api/v1/pastes", map[string]interface{}{"content": "hello"})
	if err != nil {
		t.Fatalf("postJSON: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status = %d; want 201", resp.StatusCode)
	}
}

func TestClientPostJSON_UnmarshalableBody(t *testing.T) {
	c := &client{server: "http://example.com"}
	// A channel cannot be JSON-marshaled.
	_, err := c.postJSON("/path", make(chan int))
	if err == nil {
		t.Error("expected error for unmarshalable body, got nil")
	}
}

// ─── cmdList ─────────────────────────────────────────────────────────────────

func TestCmdList_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data":       []interface{}{},
			"pagination": map[string]int{"total": 0, "pages": 0},
		})
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	// cmdList prints to os.Stdout; it must not call log.Fatal.
	c.cmdList([]string{})
}

func TestCmdList_WithData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{
					"id": "abc123", "title": "Hello", "language": "go",
					"views": 5, "created_at": "2025-01-01T00:00:00Z",
				},
			},
			"pagination": map[string]int{"total": 1, "pages": 1},
		})
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	c.cmdList([]string{"--limit=5"})
}

func TestCmdList_AsJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data":       []interface{}{},
			"pagination": map[string]int{"total": 0, "pages": 0},
		})
	}))
	defer srv.Close()

	c := &client{server: srv.URL, asJSON: true}
	c.cmdList([]string{})
}

// ─── cmdCreate ───────────────────────────────────────────────────────────────

func TestCmdCreate_FromFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id": "abc123", "link": "http://example.com/abc123", "delete_token": "tok",
		})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	f := tmp + "/hello.go"
	if err := os.WriteFile(f, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &client{server: srv.URL}
	c.cmdCreate([]string{f})
}

func TestCmdCreate_AsJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id": "abc123", "link": "http://example.com/abc123",
		})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	f := tmp + "/data.txt"
	if err := os.WriteFile(f, []byte("hello world"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &client{server: srv.URL, asJSON: true}
	c.cmdCreate([]string{f})
}

// ─── cmdGet ──────────────────────────────────────────────────────────────────

func TestCmdGet_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("paste content here"))
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	c.cmdGet([]string{"abc123"})
}

// ─── cmdDelete ───────────────────────────────────────────────────────────────

func TestCmdDelete_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "deleted"})
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	c.cmdDelete([]string{"abc123", "my-delete-token"})
}

func TestCmdDelete_AsJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "deleted"})
	}))
	defer srv.Close()

	c := &client{server: srv.URL, asJSON: true}
	c.cmdDelete([]string{"abc123", "tok"})
}

// ─── checkCLIUpdate — min-version enforcement and update notice ───────────────

func TestCheckCLIUpdate_MinVersionEnforced_ReturnsError(t *testing.T) {
	orig := Version
	Version = "1.0.0"
	defer func() { Version = orig }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"cli_min_version": "2.0.0",
			"cli_versions":    map[string]interface{}{},
		})
	}))
	defer srv.Close()

	err := checkCLIUpdate(srv.URL, "")
	if err == nil {
		t.Error("expected error when CLI is below min_version, got nil")
	}
	if !strings.Contains(err.Error(), "too old") {
		t.Errorf("error message should mention 'too old', got: %v", err)
	}
}

func TestCheckCLIUpdate_NoticeWhenNewerAvailable(t *testing.T) {
	orig := Version
	Version = "1.0.0"
	defer func() { Version = orig }()

	osArch := runtime.GOOS + "-" + runtime.GOARCH
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"cli_min_version": "0.1.0",
			"cli_versions": map[string]interface{}{
				osArch: map[string]string{"version": "9.9.9", "sha256": "abc"},
			},
		})
	}))
	defer srv.Close()

	// Should return nil (notice printed to stderr, no enforcement).
	err := checkCLIUpdate(srv.URL, "")
	if err != nil {
		t.Errorf("expected nil for update notice, got %v", err)
	}
}

func TestCheckCLIUpdate_NonOKStatus_ReturnsNil(t *testing.T) {
	orig := Version
	Version = "1.0.0"
	defer func() { Version = orig }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	if err := checkCLIUpdate(srv.URL, ""); err != nil {
		t.Errorf("expected nil for non-200 status, got %v", err)
	}
}

func TestCheckCLIUpdate_InvalidJSONBody_ReturnsNil(t *testing.T) {
	orig := Version
	Version = "1.0.0"
	defer func() { Version = orig }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	if err := checkCLIUpdate(srv.URL, ""); err != nil {
		t.Errorf("expected nil for invalid JSON, got %v", err)
	}
}

// ─── cmdCreate — flag paths ───────────────────────────────────────────────────

func TestCmdCreate_WithFlags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id": "abc123", "link": "http://example.com/abc123", "delete_token": "",
		})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	f := tmp + "/script.sh"
	if err := os.WriteFile(f, []byte("#!/bin/sh\necho hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &client{server: srv.URL}
	// --unlisted sets visibility=1; --lang overrides auto-detect; --title sets title
	c.cmdCreate([]string{"--unlisted", "--lang=bash", "--title=My Script", "--expiry=1d", f})
}

func TestCmdCreate_WithBurnFlag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "xyz", "link": "http://example.com/xyz"})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	f := tmp + "/data.txt"
	if err := os.WriteFile(f, []byte("burn after reading"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &client{server: srv.URL}
	c.cmdCreate([]string{"--burn=1", f})
}

func TestCmdList_WithPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{
					"id": "p1", "title": "A very long title that exceeds forty characters in length for truncation",
					"language": "go", "views": 3,
					"created_at": "2025-01-01T00:00:00Z",
				},
			},
			"pagination": map[string]int{"total": 10, "pages": 2},
		})
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	c.cmdList([]string{"--page=2", "--limit=5"})
}

// ─── cmdUpdate ───────────────────────────────────────────────────────────────

func TestCmdUpdate_NoBinaryForPlatform(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(autodiscoverResponse{
			CLIVersions: map[string]cliVersionInfo{},
		})
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	// Should print "no CLI binary available for <os-arch>" and return without panic.
	c.cmdUpdate("check")
}

func TestCmdUpdate_AlreadyUpToDate(t *testing.T) {
	orig := Version
	Version = "1.0.0"
	defer func() { Version = orig }()

	osArch := runtime.GOOS + "-" + runtime.GOARCH
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(autodiscoverResponse{
			CLIVersions: map[string]cliVersionInfo{
				osArch: {Version: "1.0.0"},
			},
		})
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	c.cmdUpdate("check")
}

func TestCmdUpdate_UpdateAvailable_CheckOnly(t *testing.T) {
	orig := Version
	Version = "1.0.0"
	defer func() { Version = orig }()

	osArch := runtime.GOOS + "-" + runtime.GOARCH
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(autodiscoverResponse{
			CLIVersions: map[string]cliVersionInfo{
				osArch: {Version: "2.0.0"},
			},
		})
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	// action="check" → prints "run ... to install" but does not call log.Fatal
	c.cmdUpdate("check")
}

// ─── cmdCreate — stdin path ───────────────────────────────────────────────────

func TestCmdCreate_FromStdin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id": "stdin1", "link": "http://example.com/stdin1",
		})
	}))
	defer srv.Close()

	// Redirect os.Stdin to a temp file so cmdCreate reads from it.
	tmp := t.TempDir()
	stdinPath := filepath.Join(tmp, "stdin.txt")
	if err := os.WriteFile(stdinPath, []byte("hello from stdin"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	origStdin := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = origStdin }()

	c := &client{server: srv.URL}
	// no args → reads from os.Stdin
	c.cmdCreate([]string{})
}

func TestDefaultServerURL(t *testing.T) {
	tests := []struct {
		name     string
		resolved string
		official string
		want     string
	}{
		{"empty falls back to OfficialSite", "", "https://paste.example.com", "https://paste.example.com"},
		{"both empty stays empty", "", "", ""},
		{"resolved wins over OfficialSite", "https://my.server", "https://paste.example.com", "https://my.server"},
		{"resolved kept when OfficialSite empty", "https://my.server", "", "https://my.server"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := defaultServerURL(tt.resolved, tt.official); got != tt.want {
				t.Errorf("defaultServerURL(%q, %q) = %q, want %q", tt.resolved, tt.official, got, tt.want)
			}
		})
	}
}
