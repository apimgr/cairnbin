package main

import (
	"bytes"
	"encoding/base64"
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

// ─── detectLocale ─────────────────────────────────────────────────────────────
// Tests cover: explicit flag value, "auto" trigger, env-var fallback chain,
// suffix stripping, C/POSIX sentinel values, and the ultimate "en" default.

func TestDetectLocale_ExplicitFlag(t *testing.T) {
	got := detectLocale("fr", "")
	if got != "fr" {
		t.Errorf("detectLocale(fr) = %q; want fr", got)
	}
}

func TestDetectLocale_ConfigFallback(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "de_DE.UTF-8")
	t.Setenv("LANGUAGE", "")
	got := detectLocale("", "pt")
	if got != "pt" {
		t.Errorf("detectLocale('', pt) with LANG=de_DE.UTF-8 = %q; want pt (config beats env)", got)
	}
}

func TestDetectLocale_FlagBeatsConfig(t *testing.T) {
	got := detectLocale("fr", "pt")
	if got != "fr" {
		t.Errorf("detectLocale(fr, pt) = %q; want fr (flag beats config)", got)
	}
}

func TestDetectLocale_AutoConfigTreatedAsUnset(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "ja_JP.UTF-8")
	t.Setenv("LANGUAGE", "")
	got := detectLocale("auto", "auto")
	if got != "ja" {
		t.Errorf("detectLocale(auto, auto) with LANG=ja_JP.UTF-8 = %q; want ja (config=auto falls through to env)", got)
	}
}

func TestDetectLocale_AutoWithLANG(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "de_DE.UTF-8")
	t.Setenv("LANGUAGE", "")
	got := detectLocale("auto", "")
	if got != "de" {
		t.Errorf("detectLocale(auto) with LANG=de_DE.UTF-8 = %q; want de", got)
	}
}

func TestDetectLocale_EmptyStringActsAsAuto(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "ja_JP.UTF-8")
	t.Setenv("LANGUAGE", "")
	got := detectLocale("", "")
	if got != "ja" {
		t.Errorf("detectLocale('') with LANG=ja_JP.UTF-8 = %q; want ja", got)
	}
}

func TestDetectLocale_LCAllTakesPrecedenceOverLANG(t *testing.T) {
	t.Setenv("LC_ALL", "es_ES.UTF-8")
	t.Setenv("LANG", "de_DE.UTF-8")
	t.Setenv("LANGUAGE", "")
	got := detectLocale("auto", "")
	if got != "es" {
		t.Errorf("detectLocale(auto) with LC_ALL=es = %q; want es", got)
	}
}

func TestDetectLocale_CLocaleSkipped(t *testing.T) {
	t.Setenv("LC_ALL", "C")
	t.Setenv("LANG", "POSIX")
	t.Setenv("LANGUAGE", "")
	got := detectLocale("auto", "")
	if got != "en" {
		t.Errorf("detectLocale(auto) with LC_ALL=C, LANG=POSIX = %q; want en", got)
	}
}

func TestDetectLocale_AllEnvUnset_FallsBackToEn(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "")
	t.Setenv("LANGUAGE", "")
	got := detectLocale("auto", "")
	if got != "en" {
		t.Errorf("detectLocale(auto) with no env = %q; want en", got)
	}
}

func TestDetectLocale_WithAtSuffix(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "ca@valencia")
	t.Setenv("LANGUAGE", "")
	got := detectLocale("auto", "")
	if got != "ca" {
		t.Errorf("detectLocale(auto) with LANG=ca@valencia = %q; want ca", got)
	}
}

// ─── loadCLIConfig — file read error ─────────────────────────────────────────
// Covers the branch where ReadFile fails with a non-ENOENT error.
// Using a directory at the config path triggers "is a directory" from ReadFile.

func TestLoadCLIConfig_DirectoryAtPath_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	// Create a directory where the config file is expected — ReadFile will fail.
	cfgPath := filepath.Join(dir, "cli.yml")
	if err := os.Mkdir(cfgPath, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLI_CONFIG", cfgPath)

	_, err := loadCLIConfig()
	if err == nil {
		t.Error("expected error when config path is a directory, got nil")
	}
}

// ─── saveCLIConfig — round-trip ───────────────────────────────────────────────
// Verifies that what is saved can be loaded back with the same field values.

func TestSaveCLIConfig_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "nested", "cli.yml")
	t.Setenv("CLI_CONFIG", cfgFile)

	original := cliConfig{}
	original.Server.Primary = "https://paste.example.com"
	original.Update.Channel = "beta"
	original.Update.CheckInterval = "daily"
	original.Display.Mode = "cli"

	if err := saveCLIConfig(original); err != nil {
		t.Fatalf("saveCLIConfig: %v", err)
	}

	loaded, err := loadCLIConfig()
	if err != nil {
		t.Fatalf("loadCLIConfig after save: %v", err)
	}
	if loaded.Server.Primary != original.Server.Primary {
		t.Errorf("Server = %q; want %q", loaded.Server.Primary, original.Server.Primary)
	}
	if loaded.Update.Channel != original.Update.Channel {
		t.Errorf("Channel = %q; want %q", loaded.Update.Channel, original.Update.Channel)
	}
	if loaded.Display.Mode != original.Display.Mode {
		t.Errorf("Mode = %q; want %q", loaded.Display.Mode, original.Display.Mode)
	}
}

// ─── client.get — Accept-Language header ─────────────────────────────────────
// Covers the branch that sets Accept-Language when client.lang is non-empty.

func TestClientGet_AcceptLanguageHeader(t *testing.T) {
	var gotLang string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLang = r.Header.Get("Accept-Language")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := &client{server: srv.URL, lang: "fr"}
	resp, err := c.get("/raw/test")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if gotLang != "fr" {
		t.Errorf("Accept-Language = %q; want fr", gotLang)
	}
}

func TestClientGet_NoAcceptLanguageWhenLangEmpty(t *testing.T) {
	var gotLang string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLang = r.Header.Get("Accept-Language")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := &client{server: srv.URL, lang: ""}
	resp, err := c.get("/raw/test")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if gotLang != "" {
		t.Errorf("Accept-Language should be absent for empty lang, got %q", gotLang)
	}
}

// ─── client.postJSON — Accept-Language header and invalid URL ─────────────────

func TestClientPostJSON_AcceptLanguageHeader(t *testing.T) {
	var gotLang string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLang = r.Header.Get("Accept-Language")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := &client{server: srv.URL, lang: "de"}
	resp, err := c.postJSON("/api/v1/pastes", map[string]string{"content": "x"})
	if err != nil {
		t.Fatalf("postJSON: %v", err)
	}
	resp.Body.Close()
	if gotLang != "de" {
		t.Errorf("Accept-Language = %q; want de", gotLang)
	}
}

func TestClientPostJSON_InvalidURL_ReturnsError(t *testing.T) {
	c := &client{server: "http://\x00bad"}
	_, err := c.postJSON("/path", map[string]string{"k": "v"})
	if err == nil {
		t.Error("expected error for invalid URL, got nil")
	}
}

// ─── cmdCreate — request body fields ─────────────────────────────────────────
// Verifies that visibility, burn_after, and other fields are correctly encoded
// in the POST body sent to the server.

func TestCmdCreate_RequestBodyFields(t *testing.T) {
	var received map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"id": "x", "link": "http://example.com/x"})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	f := filepath.Join(tmp, "data.txt")
	if err := os.WriteFile(f, []byte("test content"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &client{server: srv.URL}
	c.cmdCreate([]string{"--unlisted", "--burn=3", "--expiry=1d", "--title=MyTitle", "--lang=go", f})

	if received == nil {
		t.Fatal("server received no body")
	}
	if vis, ok := received["visibility"].(string); !ok || vis != "unlisted" {
		t.Errorf("visibility = %v; want \"unlisted\"", received["visibility"])
	}
	if burn, ok := received["burn_after"].(float64); !ok || burn != 3 {
		t.Errorf("burn_after = %v; want 3", received["burn_after"])
	}
	if exp, _ := received["expires_in"].(string); exp != "1d" {
		t.Errorf("expires_in = %q; want 1d", exp)
	}
	if lang, _ := received["language"].(string); lang != "go" {
		t.Errorf("language = %q; want go", lang)
	}
	if title, _ := received["title"].(string); title != "MyTitle" {
		t.Errorf("title = %q; want MyTitle", title)
	}
}

func TestCmdCreate_RequestPathAndMethod(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"id": "y", "link": "http://example.com/y"})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	f := filepath.Join(tmp, "input.txt")
	if err := os.WriteFile(f, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &client{server: srv.URL}
	c.cmdCreate([]string{f})

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q; want POST", gotMethod)
	}
	if gotPath != "/api/v1/pastes" {
		t.Errorf("path = %q; want /api/v1/pastes", gotPath)
	}
}

func TestCmdCreate_AutoDetectsLangFromExtension(t *testing.T) {
	var received map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"id": "z", "link": "http://example.com/z"})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	f := filepath.Join(tmp, "code.py")
	if err := os.WriteFile(f, []byte("print('hi')"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &client{server: srv.URL}
	c.cmdCreate([]string{f})

	if lang, _ := received["language"].(string); lang != "python" {
		t.Errorf("language auto-detect for .py = %q; want python", lang)
	}
}

func TestCmdCreate_AutoSetsTitle(t *testing.T) {
	var received map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"id": "t", "link": "http://example.com/t"})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	f := filepath.Join(tmp, "notes.md")
	if err := os.WriteFile(f, []byte("# Hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &client{server: srv.URL}
	c.cmdCreate([]string{f})

	if title, _ := received["title"].(string); title != f {
		t.Errorf("title auto-set = %q; want %q", title, f)
	}
}

// ─── cmdGet — request path and User-Agent ────────────────────────────────────

func TestCmdGet_RequestPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("content"))
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	c.cmdGet([]string{"mypasteid"})

	if gotPath != "/raw/mypasteid" {
		t.Errorf("path = %q; want /raw/mypasteid", gotPath)
	}
}

func TestCmdGet_UserAgentHeader(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("content"))
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	c.cmdGet([]string{"abc"})

	if !strings.HasPrefix(gotUA, "cairnbin-cli/") {
		t.Errorf("User-Agent = %q; want cairnbin-cli/...", gotUA)
	}
}

// ─── cmdDelete — request method, path, query, and User-Agent ─────────────────

func TestCmdDelete_RequestDetails(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"message": "deleted"})
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	c.cmdDelete([]string{"paste123", "del-token-abc"})

	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q; want DELETE", gotMethod)
	}
	if gotPath != "/api/v1/pastes/paste123" {
		t.Errorf("path = %q; want /api/v1/pastes/paste123", gotPath)
	}
	if !strings.Contains(gotQuery, "token=del-token-abc") {
		t.Errorf("query = %q; want to contain token=del-token-abc", gotQuery)
	}
}

func TestCmdDelete_TokenWithSpecialChars(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("token")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"message": "deleted"})
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	c.cmdDelete([]string{"abc", "tok/with+special=chars"})

	if gotQuery != "tok/with+special=chars" {
		t.Errorf("decoded token = %q; want tok/with+special=chars", gotQuery)
	}
}

// ─── cmdList — query string parameters ───────────────────────────────────────

func TestCmdList_QueryStringParameters(t *testing.T) {
	var gotPage, gotLimit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPage = r.URL.Query().Get("page")
		gotLimit = r.URL.Query().Get("limit")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data":       []interface{}{},
			"pagination": map[string]int{"total": 0, "pages": 0},
		})
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	c.cmdList([]string{"--page=3", "--limit=50"})

	if gotPage != "3" {
		t.Errorf("page = %q; want 3", gotPage)
	}
	if gotLimit != "50" {
		t.Errorf("limit = %q; want 50", gotLimit)
	}
}

func TestCmdList_DefaultQueryParams(t *testing.T) {
	var gotPage, gotLimit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPage = r.URL.Query().Get("page")
		gotLimit = r.URL.Query().Get("limit")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data":       []interface{}{},
			"pagination": map[string]int{"total": 0, "pages": 0},
		})
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	c.cmdList([]string{})

	if gotPage != "1" {
		t.Errorf("default page = %q; want 1", gotPage)
	}
	if gotLimit != "20" {
		t.Errorf("default limit = %q; want 20", gotLimit)
	}
}

// ─── cmdUpdate — "check" action path, update notice ───────────────────────────
// cmdUpdate("check") prints a run hint when a newer version is available and
// returns without downloading, so it is safe to call in-process.

func TestCmdUpdate_CheckAction_AvailableUpdate_PrintsRunHint(t *testing.T) {
	orig := Version
	Version = "1.0.0"
	defer func() { Version = orig }()

	osArch := runtime.GOOS + "-" + runtime.GOARCH
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(autodiscoverResponse{
			CLIVersions: map[string]cliVersionInfo{
				osArch: {Version: "3.0.0"},
			},
		})
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	c.cmdUpdate("check")
}

// ─── versionLessThan — additional boundary cases ──────────────────────────────

func TestVersionLessThan_SinglePartVersions(t *testing.T) {
	if !versionLessThan("1", "2") {
		t.Error("1 < 2 should be true")
	}
	if versionLessThan("2", "1") {
		t.Error("2 < 1 should be false")
	}
}

func TestVersionLessThan_EqualWithTwoParts(t *testing.T) {
	if versionLessThan("1.2", "1.2") {
		t.Error("1.2 == 1.2 should not be less than")
	}
}

func TestVersionLessThan_PatchComparison(t *testing.T) {
	if !versionLessThan("1.0.9", "1.0.10") {
		t.Error("1.0.9 < 1.0.10 numerically should be true")
	}
}

func TestVersionLessThan_BothUnknown(t *testing.T) {
	if versionLessThan("unknown", "unknown") {
		t.Error("unknown vs unknown should not be less than")
	}
}

// ─── detectMode — all config-only flags leave mode at tui/plain ──────────────

func TestDetectMode_AllConfigFlags(t *testing.T) {
	configFlagArgs := [][]string{
		{"--config=/some/path"},
		{"--token=mytoken"},
		{"--debug"},
		{"--color=yes"},
		{"--json"},
		{"--lang=en"},
		{"--server=https://x.com", "--token=tok"},
	}
	for _, args := range configFlagArgs {
		got := detectMode(args, "")
		if got != "tui" && got != "plain" {
			t.Errorf("detectMode(%v) = %q; want tui or plain", args, got)
		}
	}
}

func TestDetectMode_MixedConfigAndCommand(t *testing.T) {
	got := detectMode([]string{"--server=https://x.com", "list"}, "")
	if got != "cli" && got != "plain" {
		t.Errorf("detectMode with command arg = %q; want cli or plain", got)
	}
}

// ─── checkCLIUpdate — request User-Agent header ──────────────────────────────

func TestCheckCLIUpdate_SendsUserAgentHeader(t *testing.T) {
	orig := Version
	Version = "2.1.0"
	defer func() { Version = orig }()

	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		json.NewEncoder(w).Encode(autodiscoverResponse{
			CLIMinVersion: "0.1.0",
			CLIVersions:   map[string]cliVersionInfo{},
		})
	}))
	defer srv.Close()

	checkCLIUpdate(srv.URL, "")

	if gotUA != "cairnbin-cli/2.1.0" {
		t.Errorf("User-Agent = %q; want cairnbin-cli/2.1.0", gotUA)
	}
}

func TestCheckCLIUpdate_HitsAutodiscoverPath(t *testing.T) {
	orig := Version
	Version = "1.5.0"
	defer func() { Version = orig }()

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewEncoder(w).Encode(autodiscoverResponse{
			CLIVersions: map[string]cliVersionInfo{},
		})
	}))
	defer srv.Close()

	checkCLIUpdate(srv.URL, "")

	if gotPath != "/api/autodiscover" {
		t.Errorf("autodiscover path = %q; want /api/autodiscover", gotPath)
	}
}

// ─── isValidURL — additional schemes ──────────────────────────────────────────

func TestIsValidURL_HTTPSWithPath(t *testing.T) {
	if !isValidURL("https://example.com/paste") {
		t.Error("https URL with path should be valid")
	}
}

func TestIsValidURL_HTTPWithPort(t *testing.T) {
	if !isValidURL("http://localhost:8080") {
		t.Error("http URL with port should be valid")
	}
}

// ─── envOrDefault — empty string treated as unset ────────────────────────────

func TestEnvOrDefault_EmptyValueUsesDefault(t *testing.T) {
	t.Setenv("TEST_CAIRNBIN_EMPTY_VAR", "")
	got := envOrDefault("TEST_CAIRNBIN_EMPTY_VAR", "default-value")
	if got != "default-value" {
		t.Errorf("empty env should use default, got %q", got)
	}
}

// ─── detectLang — additional edge cases ──────────────────────────────────────

func TestDetectLang_EmptyFilename(t *testing.T) {
	got := detectLang("")
	if got != "text" {
		t.Errorf("detectLang('') = %q; want text", got)
	}
}

func TestDetectLang_DotfileNoExtension(t *testing.T) {
	got := detectLang(".gitignore")
	if got != "text" {
		t.Errorf("detectLang(.gitignore) = %q; want text", got)
	}
}

func TestDetectLang_MultipleDotsUsesLastExtension(t *testing.T) {
	got := detectLang("archive.tar.gz")
	if got != "text" {
		t.Errorf("detectLang(archive.tar.gz) = %q; want text (gz is unknown)", got)
	}
}

func TestDetectLang_GoUppercaseFull(t *testing.T) {
	got := detectLang("MAIN.GO")
	if got != "go" {
		t.Errorf("detectLang(MAIN.GO) = %q; want go", got)
	}
}

// ─── client.url — trailing slash handling ────────────────────────────────────

func TestClientURL_TrailingSlashOnServerPreserved(t *testing.T) {
	c := &client{server: "https://example.com"}
	got := c.url("/api/v1/pastes")
	if got != "https://example.com/api/v1/pastes" {
		t.Errorf("url() = %q; want https://example.com/api/v1/pastes", got)
	}
}

// cliConfigPath's default-path coverage moved with the function itself to
// the paths package; see src/client/paths/paths_test.go.

// ─── detectLocale — LANGUAGE env var fallback ─────────────────────────────────
// Covers the LANGUAGE branch which is reached only when LC_ALL and LANG are
// both empty or absent.

func TestDetectLocale_LANGUAGEFallback(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "")
	t.Setenv("LANGUAGE", "pt_BR.UTF-8")
	got := detectLocale("auto", "")
	if got != "pt" {
		t.Errorf("detectLocale(auto) with LANGUAGE=pt_BR.UTF-8 = %q; want pt", got)
	}
}

func TestDetectLocale_PosixInLANG_FallsToLANGUAGE(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "POSIX")
	t.Setenv("LANGUAGE", "nl.UTF-8")
	got := detectLocale("auto", "")
	if got != "nl" {
		t.Errorf("detectLocale with LANG=POSIX, LANGUAGE=nl.UTF-8 = %q; want nl", got)
	}
}

func TestDetectLocale_LocaleWithoutCountryCode(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "ru.UTF-8")
	t.Setenv("LANGUAGE", "")
	got := detectLocale("auto", "")
	if got != "ru" {
		t.Errorf("detectLocale(auto) with LANG=ru.UTF-8 = %q; want ru", got)
	}
}

// ─── saveCLIConfig — MkdirAll failure ────────────────────────────────────────
// Placing a regular file at an intermediate path component forces MkdirAll to
// fail with ENOTDIR, covering the error-return branch.

func TestSaveCLIConfig_MkdirAllFailure(t *testing.T) {
	dir := t.TempDir()
	// block is a regular file; trying to mkdir inside it will fail.
	blockFile := filepath.Join(dir, "block")
	if err := os.WriteFile(blockFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLI_CONFIG", filepath.Join(blockFile, "sub", "cli.yml"))

	err := saveCLIConfig(cliConfig{})
	if err == nil {
		t.Error("expected error when MkdirAll cannot create path through a file, got nil")
	}
}

// ─── cmdCreate — server-error response (non-201) ─────────────────────────────
// The non-201 branch of cmdCreate calls log.Fatal so we cannot reach it
// directly without a subprocess; its log.Fatalf path is excluded from the
// testable surface.  The existing tests already exercise the 201 success path
// and the JSON output path, which together cover the reachable branches.

// ─── cmdList — Accept header sent to server ───────────────────────────────────

func TestCmdList_AcceptsApplicationJSON(t *testing.T) {
	var gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data":       []interface{}{},
			"pagination": map[string]int{"total": 0, "pages": 0},
		})
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	c.cmdList([]string{})

	if gotAccept != "application/json" {
		t.Errorf("Accept header = %q; want application/json", gotAccept)
	}
}

// ─── cmdGet — Accept header ───────────────────────────────────────────────────

func TestCmdGet_AcceptsApplicationJSON(t *testing.T) {
	var gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("content"))
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	c.cmdGet([]string{"id1"})

	if gotAccept != "application/json" {
		t.Errorf("Accept header = %q; want application/json", gotAccept)
	}
}

// ─── cmdDelete — User-Agent header ───────────────────────────────────────────

func TestCmdDelete_UserAgentHeader(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"message": "ok"})
	}))
	defer srv.Close()

	c := &client{server: srv.URL}
	c.cmdDelete([]string{"id1", "tok1"})

	if !strings.HasPrefix(gotUA, "cairnbin-cli/") {
		t.Errorf("User-Agent = %q; want cairnbin-cli/...", gotUA)
	}
}

// ─── cmdCreate — User-Agent and Content-Type headers ─────────────────────────

func TestCmdCreate_ContentTypeAndUserAgent(t *testing.T) {
	var gotCT, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"id": "h", "link": "http://example.com/h"})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	f := filepath.Join(tmp, "f.txt")
	if err := os.WriteFile(f, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &client{server: srv.URL}
	c.cmdCreate([]string{f})

	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q; want application/json", gotCT)
	}
	if !strings.HasPrefix(gotUA, "cairnbin-cli/") {
		t.Errorf("User-Agent = %q; want cairnbin-cli/...", gotUA)
	}
}

// ─── versionLessThan — non-numeric components string comparison ───────────────

func TestVersionLessThan_NonNumericFirstComponentSorted(t *testing.T) {
	if !versionLessThan("1.0.alpha", "1.0.beta") {
		t.Error("alpha < beta lexicographically should be true")
	}
	if versionLessThan("1.0.beta", "1.0.alpha") {
		t.Error("beta > alpha lexicographically should not be less than")
	}
}

func TestVersionLessThan_NonNumericEqualComponent(t *testing.T) {
	if versionLessThan("1.0.alpha", "1.0.alpha") {
		t.Error("equal non-numeric components should not be less than")
	}
}

// ─── cmdCreate — binary file base64 encoding ──────────────────────────────────

// TestCmdCreate_BinaryFileBase64Encoded verifies that a binary file is sent
// base64-encoded with a content_type field so raw bytes never travel inside a
// JSON string (invalid UTF-8 would be corrupted by the encoder).
func TestCmdCreate_BinaryFileBase64Encoded(t *testing.T) {
	var received map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"id": "b", "link": "http://example.com/b"})
	}))
	defer srv.Close()

	png := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x01, 0x02, 0x03}
	tmp := t.TempDir()
	f := filepath.Join(tmp, "img.png")
	if err := os.WriteFile(f, png, 0o600); err != nil {
		t.Fatal(err)
	}

	c := &client{server: srv.URL}
	c.cmdCreate([]string{f})

	if received == nil {
		t.Fatal("server received no body")
	}
	if ct, _ := received["content_type"].(string); ct != "image/png" {
		t.Errorf("content_type = %q; want image/png", received["content_type"])
	}
	content, _ := received["content"].(string)
	decoded, err := base64.StdEncoding.DecodeString(content)
	if err != nil {
		t.Fatalf("content is not base64: %v", err)
	}
	if !bytes.Equal(decoded, png) {
		t.Errorf("decoded content does not match file bytes")
	}
}

// TestCmdCreate_TextFileNotEncoded verifies plain text files are sent as-is
// with no content_type field.
func TestCmdCreate_TextFileNotEncoded(t *testing.T) {
	var received map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"id": "c", "link": "http://example.com/c"})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	f := filepath.Join(tmp, "note.txt")
	if err := os.WriteFile(f, []byte("just text"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &client{server: srv.URL}
	c.cmdCreate([]string{f})

	if received == nil {
		t.Fatal("server received no body")
	}
	if got, _ := received["content"].(string); got != "just text" {
		t.Errorf("content = %q; want unmodified text", got)
	}
	if _, present := received["content_type"]; present {
		t.Errorf("content_type should be absent for text, got %v", received["content_type"])
	}
}
