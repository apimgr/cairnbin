package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ─── detectMode — comprehensive coverage ─────────────────────────────────────
// The detectMode function has several branches:
// 1. Exit-immediately flags (-h, --help, -v, --version) -> "cli"
// 2. Not a terminal -> "plain"
// 3. Config-only flags with no command -> "tui"
// 4. Non-flag args (commands) -> "cli"
// 5. Unknown flags -> "cli"

func TestDetectMode_HelpShort(t *testing.T) {
	got := detectMode([]string{"-h"}, "")
	if got != "cli" {
		t.Errorf("detectMode([-h]) = %q; want cli", got)
	}
}

func TestDetectMode_HelpLong(t *testing.T) {
	got := detectMode([]string{"--help"}, "")
	if got != "cli" {
		t.Errorf("detectMode([--help]) = %q; want cli", got)
	}
}

func TestDetectMode_VersionShort(t *testing.T) {
	got := detectMode([]string{"-v"}, "")
	if got != "cli" {
		t.Errorf("detectMode([-v]) = %q; want cli", got)
	}
}

func TestDetectMode_VersionLong(t *testing.T) {
	got := detectMode([]string{"--version"}, "")
	if got != "cli" {
		t.Errorf("detectMode([--version]) = %q; want cli", got)
	}
}

func TestDetectMode_HelpWithOtherFlags(t *testing.T) {
	got := detectMode([]string{"--server=https://x.com", "--help"}, "")
	if got != "cli" {
		t.Errorf("detectMode([--server=..., --help]) = %q; want cli", got)
	}
}

func TestDetectMode_VersionWithOtherFlags(t *testing.T) {
	got := detectMode([]string{"--debug", "-v"}, "")
	if got != "cli" {
		t.Errorf("detectMode([--debug, -v]) = %q; want cli", got)
	}
}

func TestDetectMode_CreateCommand(t *testing.T) {
	got := detectMode([]string{"create"}, "")
	// In test environment without TTY, could be "plain" or "cli"
	if got != "cli" && got != "plain" {
		t.Errorf("detectMode([create]) = %q; want cli or plain", got)
	}
}

func TestDetectMode_GetCommand(t *testing.T) {
	got := detectMode([]string{"get"}, "")
	if got != "cli" && got != "plain" {
		t.Errorf("detectMode([get]) = %q; want cli or plain", got)
	}
}

func TestDetectMode_DeleteCommand(t *testing.T) {
	got := detectMode([]string{"delete"}, "")
	if got != "cli" && got != "plain" {
		t.Errorf("detectMode([delete]) = %q; want cli or plain", got)
	}
}

func TestDetectMode_ListCommand(t *testing.T) {
	got := detectMode([]string{"list"}, "")
	if got != "cli" && got != "plain" {
		t.Errorf("detectMode([list]) = %q; want cli or plain", got)
	}
}

func TestDetectMode_UpdateCommand(t *testing.T) {
	got := detectMode([]string{"update"}, "")
	if got != "cli" && got != "plain" {
		t.Errorf("detectMode([update]) = %q; want cli or plain", got)
	}
}

func TestDetectMode_ServerFlagEqualsForm(t *testing.T) {
	got := detectMode([]string{"--server=https://example.com"}, "")
	if got != "tui" && got != "plain" {
		t.Errorf("detectMode([--server=...]) = %q; want tui or plain", got)
	}
}

func TestDetectMode_TokenFlag(t *testing.T) {
	got := detectMode([]string{"--token=abc123"}, "")
	if got != "tui" && got != "plain" {
		t.Errorf("detectMode([--token=...]) = %q; want tui or plain", got)
	}
}

func TestDetectMode_TokenFileFlag(t *testing.T) {
	got := detectMode([]string{"--token-file=/path/to/token"}, "")
	if got != "tui" && got != "plain" {
		t.Errorf("detectMode([--token-file=...]) = %q; want tui or plain", got)
	}
}

func TestDetectMode_DebugFlag(t *testing.T) {
	got := detectMode([]string{"--debug"}, "")
	if got != "tui" && got != "plain" {
		t.Errorf("detectMode([--debug]) = %q; want tui or plain", got)
	}
}

func TestDetectMode_ColorFlag(t *testing.T) {
	got := detectMode([]string{"--color=always"}, "")
	if got != "tui" && got != "plain" {
		t.Errorf("detectMode([--color=...]) = %q; want tui or plain", got)
	}
}

func TestDetectMode_JSONFlag(t *testing.T) {
	got := detectMode([]string{"--json"}, "")
	if got != "tui" && got != "plain" {
		t.Errorf("detectMode([--json]) = %q; want tui or plain", got)
	}
}

func TestDetectMode_LangFlag(t *testing.T) {
	got := detectMode([]string{"--lang=fr"}, "")
	if got != "tui" && got != "plain" {
		t.Errorf("detectMode([--lang=...]) = %q; want tui or plain", got)
	}
}

func TestDetectMode_ConfigFlag(t *testing.T) {
	got := detectMode([]string{"--config=/path/to/config"}, "")
	if got != "tui" && got != "plain" {
		t.Errorf("detectMode([--config=...]) = %q; want tui or plain", got)
	}
}

func TestDetectMode_NonConfigFlagFollowedByCommand(t *testing.T) {
	// --unknown is not in configFlags, triggers immediate CLI return
	got := detectMode([]string{"--unknown-flag", "create"}, "")
	if got != "cli" && got != "plain" {
		t.Errorf("detectMode([--unknown-flag, create]) = %q; want cli or plain", got)
	}
}

func TestDetectMode_EmptyArgsNoTTY(t *testing.T) {
	// In test environment stdout is not a terminal
	got := detectMode([]string{}, "")
	// Should be "plain" when not a TTY, or "tui" if somehow is
	if got != "tui" && got != "plain" {
		t.Errorf("detectMode([]) = %q; want tui or plain", got)
	}
}

// ─── cmdList — truncation and output paths ───────────────────────────────────

func TestCmdList_LongTitleTruncation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{
					"id":         "p1",
					"title":      "This is a very long title that definitely exceeds the forty character limit for display",
					"language":   "go",
					"views":      10,
					"created_at": "2025-06-01T12:00:00Z",
				},
			},
			"pagination": map[string]int{"total": 1, "pages": 1},
		})
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	// Just verify no panic with long title
	c.cmdList([]string{})
}

func TestCmdList_ExactlyFortyCharTitle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{
					"id": "p2",
					// exactly 40 chars
					"title":      "1234567890123456789012345678901234567890",
					"language":   "text",
					"views":      1,
					"created_at": "2025-06-01T12:00:00Z",
				},
			},
			"pagination": map[string]int{"total": 1, "pages": 1},
		})
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	c.cmdList([]string{})
}

func TestCmdList_MultiplePastes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{
					"id": "p1", "title": "First", "language": "go", "views": 1, "created_at": "2025-06-01T00:00:00Z",
				},
				map[string]interface{}{
					"id": "p2", "title": "Second", "language": "py", "views": 2, "created_at": "2025-06-02T00:00:00Z",
				},
				map[string]interface{}{
					"id": "p3", "title": "Third", "language": "js", "views": 3, "created_at": "2025-06-03T00:00:00Z",
				},
			},
			"pagination": map[string]int{"total": 3, "pages": 1},
		})
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	c.cmdList([]string{})
}

// ─── cmdUpdate — request path verification ───────────────────────────────────

func TestCmdUpdate_HitsCorrectEndpoint(t *testing.T) {
	orig := Version
	Version = "1.0.0"
	defer func() { Version = orig }()

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewEncoder(w).Encode(autodiscoverResponse{
			CLIVersions: map[string]cliVersionInfo{},
		})
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	c.cmdUpdate("check")

	if gotPath != "/api/autodiscover" {
		t.Errorf("update path = %q; want /api/autodiscover", gotPath)
	}
}

func TestCmdUpdate_SendsUserAgent(t *testing.T) {
	orig := Version
	Version = "2.0.0"
	defer func() { Version = orig }()

	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		json.NewEncoder(w).Encode(autodiscoverResponse{
			CLIVersions: map[string]cliVersionInfo{},
		})
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	c.cmdUpdate("check")

	if !strings.HasPrefix(gotUA, "cairnbin-cli/") {
		t.Errorf("User-Agent = %q; want cairnbin-cli/...", gotUA)
	}
}

// ─── cmdCreate — visibility and expiry fields ─────────────────────────────────

func TestCmdCreate_DefaultVisibilityIsUnlisted(t *testing.T) {
	var received map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"id": "x", "link": "http://example.com/x"})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	f := filepath.Join(tmp, "test.txt")
	if err := os.WriteFile(f, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &client{server: srv.URL}
	c.cmdCreate([]string{f})

	// Anonymous pastes default to unlisted, so they do not appear in recent listings.
	if vis, ok := received["visibility"].(string); !ok || vis != "unlisted" {
		t.Errorf("visibility = %v; want \"unlisted\"", received["visibility"])
	}
}

func TestCmdCreate_DefaultExpiryIsNever(t *testing.T) {
	var received map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"id": "x", "link": "http://example.com/x"})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	f := filepath.Join(tmp, "test.txt")
	if err := os.WriteFile(f, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &client{server: srv.URL}
	c.cmdCreate([]string{f})

	if exp, _ := received["expires_in"].(string); exp != "never" {
		t.Errorf("expires_in = %q; want never", exp)
	}
}

func TestCmdCreate_DefaultBurnIsZero(t *testing.T) {
	var received map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"id": "x", "link": "http://example.com/x"})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	f := filepath.Join(tmp, "test.txt")
	if err := os.WriteFile(f, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &client{server: srv.URL}
	c.cmdCreate([]string{f})

	if burn, ok := received["burn_after"].(float64); !ok || burn != 0 {
		t.Errorf("burn_after = %v; want 0", received["burn_after"])
	}
}

// ─── cmdCreate — output when no delete token ─────────────────────────────────

func TestCmdCreate_NoDeleteToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{
			"id":   "xyz",
			"link": "http://example.com/xyz",
			// empty token
			"delete_token": "",
		})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	f := filepath.Join(tmp, "test.txt")
	if err := os.WriteFile(f, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &client{server: srv.URL}
	// Should not panic when delete_token is empty
	c.cmdCreate([]string{f})
}

// ─── checkCLIUpdate — various edge cases ─────────────────────────────────────

func TestCheckCLIUpdate_CurrentVersionMatches(t *testing.T) {
	orig := Version
	Version = "2.0.0"
	defer func() { Version = orig }()

	osArch := runtime.GOOS + "-" + runtime.GOARCH
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(autodiscoverResponse{
			CLIMinVersion: "1.0.0",
			CLIVersions: map[string]cliVersionInfo{
				osArch: {Version: "2.0.0", SHA256: "abc"},
			},
		})
	}))
	defer srv.Close()

	// Should return nil when current version equals available
	err := checkCLIUpdate(srv.URL, "")
	if err != nil {
		t.Errorf("expected nil for current version match, got %v", err)
	}
}

func TestCheckCLIUpdate_CurrentVersionNewer(t *testing.T) {
	orig := Version
	Version = "3.0.0"
	defer func() { Version = orig }()

	osArch := runtime.GOOS + "-" + runtime.GOARCH
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(autodiscoverResponse{
			CLIMinVersion: "1.0.0",
			CLIVersions: map[string]cliVersionInfo{
				osArch: {Version: "2.0.0", SHA256: "abc"},
			},
		})
	}))
	defer srv.Close()

	// Should return nil when current version is newer
	err := checkCLIUpdate(srv.URL, "")
	if err != nil {
		t.Errorf("expected nil for newer version, got %v", err)
	}
}

func TestCheckCLIUpdate_NoPlatformBinary(t *testing.T) {
	orig := Version
	Version = "1.0.0"
	defer func() { Version = orig }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(autodiscoverResponse{
			CLIMinVersion: "0.5.0",
			CLIVersions:   map[string]cliVersionInfo{
				// No entry for current platform
			},
		})
	}))
	defer srv.Close()

	// Should return nil when no binary available for platform
	err := checkCLIUpdate(srv.URL, "")
	if err != nil {
		t.Errorf("expected nil for missing platform binary, got %v", err)
	}
}

// cliConfigPath's OS branches moved with the function itself to the paths
// package; see src/client/paths/paths_test.go.

// ─── saveIfUnset — additional edge cases ────────────────────────────

func TestSaveIfEmptyOrInvalid_CurrentEmptyFlagEmpty(t *testing.T) {
	valid := func(s string) bool { return s != "" }
	resolved, persist := saveIfUnset("", "", valid)
	if resolved != "" {
		t.Errorf("resolved = %q; want empty", resolved)
	}
	if persist {
		t.Error("expected persist=false when both are empty")
	}
}

// ─── cmdDelete — path escaping ───────────────────────────────────────────────

func TestCmdDelete_IDWithSpecialChars(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"message": "deleted"})
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	c.cmdDelete([]string{"paste/with/slashes", "token"})

	// The path should be escaped
	if !strings.HasPrefix(gotPath, "/api/v1/pastes/paste") {
		t.Errorf("path = %q; should start with /api/v1/pastes/paste", gotPath)
	}
}

// ─── cmdGet — path escaping ──────────────────────────────────────────────────

func TestCmdGet_IDWithSpecialChars(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("content"))
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	c.cmdGet([]string{"paste%20id"})

	// Path should preserve encoding
	if !strings.Contains(gotPath, "paste") {
		t.Errorf("path = %q; should contain paste", gotPath)
	}
}

// ─── envOrDefault — all branches ─────────────────────────────────────────────

func TestEnvOrDefault_NonEmptyValue(t *testing.T) {
	t.Setenv("TEST_CAIRNBIN_CLI_NON_EMPTY", "value")
	got := envOrDefault("TEST_CAIRNBIN_CLI_NON_EMPTY", "default")
	if got != "value" {
		t.Errorf("got %q; want value", got)
	}
}

// ─── detectLocale — more edge cases ──────────────────────────────────────────

func TestDetectLocale_LocaleWithOnlyTerritory(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "en_US")
	t.Setenv("LANGUAGE", "")
	got := detectLocale("auto", "")
	if got != "en" {
		t.Errorf("detectLocale(auto) with LANG=en_US = %q; want en", got)
	}
}

func TestDetectLocale_EmptyLCALLUsesLANG(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "it_IT.UTF-8")
	t.Setenv("LANGUAGE", "")
	got := detectLocale("auto", "")
	if got != "it" {
		t.Errorf("detectLocale(auto) with empty LC_ALL, LANG=it_IT.UTF-8 = %q; want it", got)
	}
}

// ─── version constant checks ─────────────────────────────────────────────────

func TestVersionConstants_Default(t *testing.T) {
	// Version should be "devel" when not overridden by ldflags
	// This documents the expected default value
	if Version != "devel" && Version != "dev" && Version != "unknown" && !strings.Contains(Version, ".") {
		t.Logf("Version = %q (may be overridden by ldflags)", Version)
	}
}

func TestCommitID_Exists(t *testing.T) {
	if CommitID == "" {
		t.Error("CommitID should not be empty")
	}
}

func TestBuildDate_Exists(t *testing.T) {
	if BuildDate == "" {
		t.Error("BuildDate should not be empty")
	}
}

// ─── exit code constants ─────────────────────────────────────────────────────

func TestExitCodes_Values(t *testing.T) {
	// Verify exit codes match PART 32 spec
	if exitSuccess != 0 {
		t.Errorf("exitSuccess = %d; want 0", exitSuccess)
	}
	if exitGeneral != 1 {
		t.Errorf("exitGeneral = %d; want 1", exitGeneral)
	}
	if exitConfig != 2 {
		t.Errorf("exitConfig = %d; want 2", exitConfig)
	}
	if exitConnection != 3 {
		t.Errorf("exitConnection = %d; want 3", exitConnection)
	}
	if exitAuth != 4 {
		t.Errorf("exitAuth = %d; want 4", exitAuth)
	}
	if exitNotFound != 5 {
		t.Errorf("exitNotFound = %d; want 5", exitNotFound)
	}
	if exitUsage != 64 {
		t.Errorf("exitUsage = %d; want 64", exitUsage)
	}
}

// ─── projectName constant ────────────────────────────────────────────────────

func TestProjectName_Value(t *testing.T) {
	if projectName != "cairnbin" {
		t.Errorf("projectName = %q; want cairnbin", projectName)
	}
}
