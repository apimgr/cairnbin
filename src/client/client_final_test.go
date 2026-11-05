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

// ─── detectMode — additional comprehensive tests ─────────────────────────────
// detectMode has several code paths:
// 1. Exit-immediately flags: loop checks each arg for -h/--help/-v/--version
// 2. TTY check: term.IsTerminal on stdout
// 3. Config-only flags loop: checks if arg starts with "-" and if it's a config flag
// 4. Command detection: non-flag arg triggers "cli" return

func TestDetectMode_VersionFlagAmongOthers(t *testing.T) {
	// Version flag should be detected even among other flags
	got := detectMode([]string{"--server=https://x.com", "--version", "--debug"}, "")
	if got != "cli" {
		t.Errorf("detectMode with --version = %q; want cli", got)
	}
}

func TestDetectMode_HelpFlagAtEnd(t *testing.T) {
	got := detectMode([]string{"--debug", "--token=x", "-h"}, "")
	if got != "cli" {
		t.Errorf("detectMode with -h at end = %q; want cli", got)
	}
}

func TestDetectMode_ConfigFlagWithEquals(t *testing.T) {
	// Config flags use SplitN to handle both --flag and --flag=value
	got := detectMode([]string{"--server=https://test.com"}, "")
	if got != "tui" && got != "plain" {
		t.Errorf("detectMode([--server=...]) = %q; want tui or plain", got)
	}
}

func TestDetectMode_ConfigFlagWithoutEquals(t *testing.T) {
	// Flag without value
	got := detectMode([]string{"--debug"}, "")
	if got != "tui" && got != "plain" {
		t.Errorf("detectMode([--debug]) = %q; want tui or plain", got)
	}
}

func TestDetectMode_UnrecognizedFlagWithEquals(t *testing.T) {
	// Unknown flag should trigger CLI mode
	got := detectMode([]string{"--unknown=value"}, "")
	if got != "cli" && got != "plain" {
		t.Errorf("detectMode([--unknown=value]) = %q; want cli or plain", got)
	}
}

func TestDetectMode_CommandArgOnly(t *testing.T) {
	// Non-flag argument (no leading dash) triggers immediate return
	for _, cmd := range []string{"create", "get", "delete", "list", "update", "version"} {
		got := detectMode([]string{cmd}, "")
		if got != "cli" && got != "plain" {
			t.Errorf("detectMode([%s]) = %q; want cli or plain", cmd, got)
		}
	}
}

func TestDetectMode_ConfigFlagsThenCommand(t *testing.T) {
	// Config flags followed by command
	got := detectMode([]string{"--server=https://x.com", "--debug", "list"}, "")
	if got != "cli" && got != "plain" {
		t.Errorf("detectMode([config flags then list]) = %q; want cli or plain", got)
	}
}

// ─── detectMode — GUI mode ───────────────────────────────────────────────────
// PART 32's display.mode table: `gui` forces the native GUI (erroring when no
// display exists), and `auto` picks GUI whenever a display is present.

func TestDetectMode_ExplicitGUIOverride(t *testing.T) {
	// display.mode: gui must select GUI even with no args at all. This branch
	// is reached without consulting guiAvailable() — main is the layer that
	// turns "no display" into the gui_unavailable error, so that the error is
	// reported only for a mode the user actually asked for.
	for _, args := range [][]string{
		{},
		{"--config=dev"},
		{"--server=https://x.com"},
		{"--token=abc"},
	} {
		if got := detectMode(args, "gui"); got != "gui" {
			t.Errorf("detectMode(%v, \"gui\") = %q; want gui", args, got)
		}
	}
}

func TestDetectMode_ExplicitGUIOverrideLosesToCommand(t *testing.T) {
	// A command argument must beat display.mode: gui — `cairnbin-cli list`
	// with GUI forced has to print a list, not open a window. Same for a flag
	// outside the config allowlist.
	for _, args := range [][]string{
		{"list"},
		{"--server=https://x.com", "list"},
		{"create", "hello"},
		{"--unknown-flag"},
	} {
		got := detectMode(args, "gui")
		if got != "cli" && got != "plain" {
			t.Errorf("detectMode(%v, \"gui\") = %q; want cli or plain", args, got)
		}
	}
}

func TestDetectMode_ExplicitGUIOverrideLosesToExitFlag(t *testing.T) {
	// -h/--version must still short-circuit to CLI ahead of GUI.
	for _, args := range [][]string{
		{"--config=x", "-h"},
		{"--version"},
	} {
		if got := detectMode(args, "gui"); got != "cli" {
			t.Errorf("detectMode(%v, \"gui\") = %q; want cli", args, got)
		}
	}
}

func TestDetectMode_TUIOverrideUnaffectedByCommands(t *testing.T) {
	// display.mode: tui keeps its existing meaning: force the interactive
	// launch for config-only invocations, but still yield to a command.
	if got := detectMode([]string{"--config=dev"}, "tui"); got != "tui" {
		t.Errorf("detectMode([--config=dev], \"tui\") = %q; want tui", got)
	}
	if got := detectMode([]string{"list"}, "tui"); got != "cli" && got != "plain" {
		t.Errorf("detectMode([list], \"tui\") = %q; want cli or plain", got)
	}
}

func TestArgsAreConfigOnly(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"no args", nil, true},
		{"single flag with equals", []string{"--config=dev"}, true},
		{"several config flags", []string{"--server=https://x.com", "--debug"}, true},
		{"space-syntax value consumed", []string{"--config", "dev"}, true},
		{"command name", []string{"list"}, false},
		{"flag after command", []string{"--debug", "list"}, false},
		{"unknown flag", []string{"--nope"}, false},
		{"positional search term", []string{"golang", "tutorials"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := argsAreConfigOnly(tc.args); got != tc.want {
				t.Errorf("argsAreConfigOnly(%v) = %v; want %v", tc.args, got, tc.want)
			}
		})
	}
}

// TestDetectMode_GUIAvailableFalseNeverSelectsGUI documents the no-tag build
// contract: guiAvailable() compiles to a constant false there, so auto must
// never return "gui" and every config-only invocation stays tui/plain.
func TestDetectMode_GUIAvailableFalseNeverSelectsGUI(t *testing.T) {
	if guiAvailable() {
		t.Skip("built with -tags gui; guiAvailable() is display-dependent")
	}
	if got := detectMode([]string{"--config=dev"}, "auto"); got == "gui" {
		t.Error("detectMode selected gui without a display")
	}
}

func TestDetectMode_AllSupportedConfigFlags(t *testing.T) {
	// Each supported config flag
	flags := []string{
		"--config=/path",
		"--server=https://x.com",
		"--token=abc",
		"--debug",
		"--color=auto",
		"--json",
		"--lang=en",
	}
	for _, flag := range flags {
		got := detectMode([]string{flag}, "")
		if got != "tui" && got != "plain" {
			t.Errorf("detectMode([%s]) = %q; want tui or plain", flag, got)
		}
	}
}

// ─── cmdList — verify table truncation logic ─────────────────────────────────

func TestCmdList_TitleTruncatedAt40(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{
					"id": "p1",
					// 41 chars
					"title":      "12345678901234567890123456789012345678901",
					"language":   "text",
					"views":      1,
					"created_at": "2025-01-01T00:00:00Z",
				},
			},
			"pagination": map[string]int{"total": 1, "pages": 1},
		})
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	// This should trigger title truncation (len > 40)
	c.cmdList([]string{})
}

// ─── cmdCreate — verify all flag combinations ─────────────────────────────────

func TestCmdCreate_AllFlagsAtOnce(t *testing.T) {
	var received map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"id": "x", "link": "http://example.com/x"})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	f := filepath.Join(tmp, "test.rs")
	if err := os.WriteFile(f, []byte("fn main() {}"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &client{server: srv.URL}
	// All flags combined
	c.cmdCreate([]string{
		"--lang=rust",
		"--expiry=1h",
		"--burn=5",
		"--unlisted",
		"--title=My Rust Code",
		f,
	})

	if lang, _ := received["language"].(string); lang != "rust" {
		t.Errorf("language = %q; want rust", lang)
	}
	if exp, _ := received["expires_in"].(string); exp != "1h" {
		t.Errorf("expires_in = %q; want 1h", exp)
	}
	if burn, _ := received["burn_after"].(float64); burn != 5 {
		t.Errorf("burn_after = %v; want 5", burn)
	}
	if vis, _ := received["visibility"].(string); vis != "unlisted" {
		t.Errorf("visibility = %v; want 1", vis)
	}
	if title, _ := received["title"].(string); title != "My Rust Code" {
		t.Errorf("title = %q; want My Rust Code", title)
	}
}

// ─── cmdUpdate — verify action parameter ──────────────────────────────────────

func TestCmdUpdate_YesAction_NoDownloadNeeded(t *testing.T) {
	orig := Version
	Version = "2.0.0"
	defer func() { Version = orig }()

	osArch := runtime.GOOS + "-" + runtime.GOARCH
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(autodiscoverResponse{
			CLIVersions: map[string]cliVersionInfo{
				// same as current
				osArch: {Version: "2.0.0"},
			},
		})
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	// action="yes" but no update available
	c.cmdUpdate("yes")
}

// ─── cliConfig struct — verify all fields serialize ──────────────────────────

func TestCLIConfig_AllFieldsSerialized(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "cli.yml")
	t.Setenv("CLI_CONFIG", cfgFile)

	cfg := cliConfig{}
	cfg.Server.Primary = "https://test.example.com"
	cfg.Update.Auto = true
	cfg.Update.CheckInterval = "daily"
	cfg.Update.Channel = "beta"
	cfg.Display.Mode = "tui"
	cfg.Auth.Token = "tok_test"
	cfg.Auth.TokenFile = "/path/to/token"
	cfg.Output.Format = "json"
	cfg.Output.Color = "always"
	cfg.Output.Pager = "less"
	cfg.Output.Quiet = true
	cfg.Output.Verbose = true
	cfg.TUI.Enabled = true
	cfg.TUI.Theme = "dracula"
	cfg.TUI.Mouse = true
	cfg.TUI.Unicode = true
	cfg.Logging.Level = "debug"
	cfg.Logging.File = "/var/log/cli.log"
	cfg.Logging.MaxSize = "10MB"
	cfg.Logging.MaxFiles = 5
	cfg.Cache.Enabled = true
	cfg.Cache.TTL = "10m"
	cfg.Cache.MaxSize = "200MB"
	cfg.Debug = true
	cfg.Defaults.Lang = "es"
	cfg.Defaults.Public = "public"
	cfg.Defaults.Expire = "1d"
	cfg.Defaults.Syntax = "go"
	cfg.Defaults.Output = "raw"
	cfg.Defaults.Limit = 100

	if err := saveCLIConfig(cfg); err != nil {
		t.Fatalf("saveCLIConfig: %v", err)
	}

	loaded, err := loadCLIConfig()
	if err != nil {
		t.Fatalf("loadCLIConfig: %v", err)
	}

	// Verify all fields
	if loaded.Server.Primary != cfg.Server.Primary {
		t.Errorf("Server mismatch")
	}
	if loaded.Update.Auto != cfg.Update.Auto {
		t.Errorf("Update.Auto mismatch")
	}
	if loaded.Auth.Token != cfg.Auth.Token {
		t.Errorf("Auth.Token mismatch")
	}
	if loaded.Output.Format != cfg.Output.Format {
		t.Errorf("Output.Format mismatch")
	}
	if loaded.TUI.Theme != cfg.TUI.Theme {
		t.Errorf("TUI.Theme mismatch")
	}
	if loaded.Logging.Level != cfg.Logging.Level {
		t.Errorf("Logging.Level mismatch")
	}
	if loaded.Cache.MaxSize != cfg.Cache.MaxSize {
		t.Errorf("Cache.MaxSize mismatch")
	}
	if loaded.Defaults.Limit != cfg.Defaults.Limit {
		t.Errorf("Defaults.Limit mismatch")
	}
}

// ─── client struct — verify lang header ──────────────────────────────────────

func TestClient_LangEmptyDoesNotAddHeader(t *testing.T) {
	var headers http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := &client{server: srv.URL, lang: ""}
	resp, _ := c.get("/test")
	resp.Body.Close()

	if h := headers.Get("Accept-Language"); h != "" {
		t.Errorf("Accept-Language should be empty, got %q", h)
	}
}

func TestClient_LangSetAddsHeader(t *testing.T) {
	var headers http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := &client{server: srv.URL, lang: "pt-BR"}
	resp, _ := c.get("/test")
	resp.Body.Close()

	if h := headers.Get("Accept-Language"); h != "pt-BR" {
		t.Errorf("Accept-Language = %q; want pt-BR", h)
	}
}

// ─── autodiscoverResponse struct ─────────────────────────────────────────────

func TestAutodiscoverResponse_Parsing(t *testing.T) {
	jsonData := `{
		"cli_min_version": "1.5.0",
		"cli_versions": {
			"linux-amd64": {"version": "2.0.0", "sha256": "abc123"},
			"darwin-arm64": {"version": "2.0.1", "sha256": "def456"}
		}
	}`

	var resp autodiscoverResponse
	if err := json.Unmarshal([]byte(jsonData), &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if resp.CLIMinVersion != "1.5.0" {
		t.Errorf("CLIMinVersion = %q; want 1.5.0", resp.CLIMinVersion)
	}
	if len(resp.CLIVersions) != 2 {
		t.Errorf("CLIVersions count = %d; want 2", len(resp.CLIVersions))
	}
	if v, ok := resp.CLIVersions["linux-amd64"]; !ok || v.Version != "2.0.0" {
		t.Errorf("linux-amd64 version = %+v; want 2.0.0", v)
	}
	if v, ok := resp.CLIVersions["darwin-arm64"]; !ok || v.SHA256 != "def456" {
		t.Errorf("darwin-arm64 sha256 = %+v; want def456", v)
	}
}

// ─── detectLang — all extension branches ─────────────────────────────────────

func TestDetectLang_AllMappedExtensions(t *testing.T) {
	cases := map[string]string{
		"main.go":   "go",
		"script.py": "python",
		"app.js":    "javascript",
		"app.ts":    "typescript",
		"lib.rs":    "rust",
		"Main.java": "java",
		"file.c":    "c",
		"file.cpp":  "cpp",
		"file.cc":   "cpp",
		"file.cs":   "csharp",
		"file.php":  "php",
		"file.rb":   "ruby",
		"file.sh":   "bash",
		"file.bash": "bash",
		"file.zsh":  "bash",
		"file.ps1":  "powershell",
		"file.html": "html",
		"file.htm":  "html",
		"file.css":  "css",
		"file.json": "json",
		"file.yaml": "yaml",
		"file.yml":  "yaml",
		"file.toml": "toml",
		"file.xml":  "xml",
		"file.sql":  "sql",
		"file.md":   "markdown",
		"file.txt":  "text",
	}

	for filename, expected := range cases {
		got := detectLang(filename)
		if got != expected {
			t.Errorf("detectLang(%q) = %q; want %q", filename, got, expected)
		}
	}
}

// ─── versionLessThan — comprehensive coverage ────────────────────────────────

func TestVersionLessThan_Table(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.0.0", "2.0.0", true},
		{"2.0.0", "1.0.0", false},
		{"1.0.0", "1.1.0", true},
		{"1.1.0", "1.0.0", false},
		{"1.0.0", "1.0.1", true},
		{"1.0.1", "1.0.0", false},
		{"1.2.3", "1.2.3", false},
		{"dev", "1.0.0", false},
		{"1.0.0", "dev", false},
		{"unknown", "2.0.0", false},
		{"1", "2", true},
		{"1.0", "1.1", true},
		{"0.9.0", "0.10.0", true},
		{"0.10.0", "0.9.0", false},
		{"1.0.alpha", "1.0.beta", true},
		{"1.0.beta", "1.0.alpha", false},
	}

	for _, tc := range cases {
		got := versionLessThan(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("versionLessThan(%q, %q) = %v; want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// ─── isValidURL — comprehensive coverage ─────────────────────────────────────

func TestIsValidURL_Table(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"http://example.com", true},
		{"https://example.com", true},
		{"http://localhost:8080", true},
		{"https://example.com/path", true},
		{"https://example.com:443/path?query=1", true},
		{"ftp://example.com", false},
		{"file:///path", false},
		{"example.com", false},
		{"", false},
		{"://invalid", false},
	}

	for _, tc := range cases {
		got := isValidURL(tc.url)
		if got != tc.want {
			t.Errorf("isValidURL(%q) = %v; want %v", tc.url, got, tc.want)
		}
	}
}

// ─── readTokenFile — PART 32 --token-file / auth.token_file ──────────────────

func TestReadTokenFile_TrimsWhitespace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("tok_abc123\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := readTokenFile(path)
	if err != nil {
		t.Fatalf("readTokenFile: %v", err)
	}
	if got != "tok_abc123" {
		t.Errorf("readTokenFile() = %q; want %q", got, "tok_abc123")
	}
}

func TestReadTokenFile_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := readTokenFile(path); err == nil {
		t.Error("readTokenFile() on empty file: want error, got nil")
	}
}

func TestReadTokenFile_Missing(t *testing.T) {
	if _, err := readTokenFile(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("readTokenFile() on missing file: want error, got nil")
	}
}

// ─── saveIfUnset — comprehensive coverage ───────────────────────────

func TestSaveIfEmptyOrInvalid_Table(t *testing.T) {
	valid := func(s string) bool { return strings.HasPrefix(s, "http") }

	cases := []struct {
		name         string
		current      string
		flagValue    string
		wantResolved string
		wantPersist  bool
	}{
		{"both empty", "", "", "", false},
		{"flag empty", "http://old", "", "http://old", false},
		{"flag invalid", "http://old", "invalid", "http://old", false},
		{"current empty flag valid", "", "http://new", "http://new", true},
		{"current invalid flag valid", "invalid", "http://new", "http://new", true},
		{"both valid", "http://old", "http://new", "http://new", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolved, persist := saveIfUnset(tc.current, tc.flagValue, valid)
			if resolved != tc.wantResolved {
				t.Errorf("resolved = %q; want %q", resolved, tc.wantResolved)
			}
			if persist != tc.wantPersist {
				t.Errorf("persist = %v; want %v", persist, tc.wantPersist)
			}
		})
	}
}

// ─── envOrDefault — comprehensive coverage ───────────────────────────────────

func TestEnvOrDefault_Table(t *testing.T) {
	envKey := "TEST_CAIRNBIN_CLI_ENV_TABLE"

	cases := []struct {
		envVal string
		setEnv bool
		defVal string
		want   string
	}{
		{"value", true, "default", "value"},
		{"", true, "default", "default"},
		{"", false, "default", "default"},
	}

	for _, tc := range cases {
		if tc.setEnv {
			t.Setenv(envKey, tc.envVal)
		} else {
			os.Unsetenv(envKey)
		}
		got := envOrDefault(envKey, tc.defVal)
		if got != tc.want {
			t.Errorf("envOrDefault with env=%q set=%v default=%q = %q; want %q",
				tc.envVal, tc.setEnv, tc.defVal, got, tc.want)
		}
	}
}
