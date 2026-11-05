// Tests for the Manager: writer wiring, format selection, level gating,
// debug gating, nil-manager safety, and RotateCheck across all files.
package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readLog reads dir/name, failing the test on error.
func readLog(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

// testOptions returns Options with every file enabled in dir.
func testOptions(dir string) Options {
	return Options{
		Dir:      dir,
		Level:    "info",
		Hostname: "testhost",
		Tag:      "cairnbin",
		Access:   FileOptions{Filename: "access.log", Format: "apache", Rotate: "monthly", Keep: "none"},
		Server:   FileOptions{Filename: "server.log", Format: "text", Rotate: "weekly,50MB", Keep: "none"},
		Error:    FileOptions{Filename: "error.log", Format: "text", Rotate: "weekly,50MB", Keep: "none"},
		App:      FileOptions{Filename: "app.log", Format: "logfmt", Rotate: "weekly,50MB", Keep: "none"},
		Auth:     FileOptions{Enabled: true, Filename: "auth.log", Format: "syslog", Rotate: "weekly,50MB", Keep: "none"},
		Debug:    FileOptions{Enabled: true, Filename: "debug.log", Format: "text", Rotate: "weekly,50MB", Keep: "none"},
		Audit:    FileOptions{Enabled: true, Filename: "audit.log", Format: "json", Rotate: "daily", Keep: "none"},
		Security: FileOptions{Filename: "security.log", Format: "fail2ban", Rotate: "weekly,50MB", Keep: "none"},
	}
}

func TestManager_AccessApache(t *testing.T) {
	dir := t.TempDir()
	m := New(testOptions(dir))
	defer m.Close()

	m.Access(sampleEntry())
	got := readLog(t, dir, "access.log")
	if !strings.Contains(got, `"GET /path HTTP/1.1" 200 2326`) {
		t.Errorf("access line missing request: %q", got)
	}
	if !strings.HasPrefix(got, "127.0.0.1 - - [") {
		t.Errorf("access line not CLF: %q", got)
	}
}

func TestManager_AccessJSONFormat(t *testing.T) {
	dir := t.TempDir()
	opts := testOptions(dir)
	opts.Access.Format = "json"
	m := New(opts)
	defer m.Close()

	m.Access(sampleEntry())
	got := readLog(t, dir, "access.log")
	if !strings.HasPrefix(got, `{"ip":"127.0.0.1"`) {
		t.Errorf("access json line = %q", got)
	}
}

func TestManager_AccessCustomFormat(t *testing.T) {
	dir := t.TempDir()
	opts := testOptions(dir)
	opts.Access.Format = "custom"
	opts.Access.Custom = "{remote_ip} {status}"
	m := New(opts)
	defer m.Close()

	m.Access(sampleEntry())
	if got := readLog(t, dir, "access.log"); got != "127.0.0.1 200\n" {
		t.Errorf("custom access line = %q", got)
	}
}

func TestManager_ServerLevelGate(t *testing.T) {
	dir := t.TempDir()
	opts := testOptions(dir)
	opts.Level = "warn"
	m := New(opts)
	defer m.Close()

	m.Server("info", "suppressed")
	m.Server("error", "kept")
	got := readLog(t, dir, "server.log")
	if strings.Contains(got, "suppressed") {
		t.Errorf("info line should be gated at level=warn: %q", got)
	}
	if !strings.Contains(got, "[ERROR] kept") {
		t.Errorf("error line missing: %q", got)
	}
}

func TestManager_ErrorAlwaysWrites(t *testing.T) {
	dir := t.TempDir()
	opts := testOptions(dir)
	opts.Level = "error"
	m := New(opts)
	defer m.Close()

	m.Error("boom", "code", "500")
	got := readLog(t, dir, "error.log")
	if !strings.Contains(got, "[ERROR] boom code=500") {
		t.Errorf("error line = %q", got)
	}
}

func TestManager_AppLogfmt(t *testing.T) {
	dir := t.TempDir()
	m := New(testOptions(dir))
	defer m.Close()

	m.App("info", "paste created", "id", "abc123")
	got := readLog(t, dir, "app.log")
	if !strings.Contains(got, `level=INFO msg="paste created" id=abc123`) {
		t.Errorf("app logfmt line = %q", got)
	}
}

func TestManager_AuthSyslog(t *testing.T) {
	dir := t.TempDir()
	m := New(testOptions(dir))
	defer m.Close()

	m.Auth("operator", "1.2.3.4", "fail", "invalid_token")
	got := readLog(t, dir, "auth.log")
	if !strings.Contains(got, "testhost cairnbin[") {
		t.Errorf("auth line missing syslog host/tag: %q", got)
	}
	if !strings.Contains(got, "auth: token_id=operator ip=1.2.3.4 result=fail reason=invalid_token") {
		t.Errorf("auth payload wrong: %q", got)
	}
	// Auth log is 0600.
	st, err := os.Stat(filepath.Join(dir, "auth.log"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("auth.log perm = %o, want 600", st.Mode().Perm())
	}
}

func TestManager_AuthDisabled(t *testing.T) {
	dir := t.TempDir()
	opts := testOptions(dir)
	opts.Auth.Enabled = false
	m := New(opts)
	defer m.Close()

	m.Auth("operator", "1.2.3.4", "fail", "invalid_token")
	if _, err := os.Stat(filepath.Join(dir, "auth.log")); !os.IsNotExist(err) {
		t.Error("auth.log must not exist when disabled")
	}
}

func TestManager_DebugGate(t *testing.T) {
	dir := t.TempDir()
	opts := testOptions(dir)
	gate := false
	opts.DebugGate = func() bool { return gate }
	m := New(opts)
	defer m.Close()

	m.Debug("hidden")
	if _, err := os.Stat(filepath.Join(dir, "debug.log")); !os.IsNotExist(err) {
		t.Error("debug.log must not be written while the gate is closed")
	}

	gate = true
	m.Debug("visible", "k", "v")
	got := readLog(t, dir, "debug.log")
	if !strings.Contains(got, "[DEBUG] visible k=v") {
		t.Errorf("debug line = %q", got)
	}
}

func TestManager_DebugFileDisabled(t *testing.T) {
	dir := t.TempDir()
	opts := testOptions(dir)
	opts.Debug.Enabled = false
	opts.DebugGate = func() bool { return true }
	m := New(opts)
	defer m.Close()

	m.Debug("hidden")
	if _, err := os.Stat(filepath.Join(dir, "debug.log")); !os.IsNotExist(err) {
		t.Error("debug.log must not be written when server.logs.debug.enabled=false")
	}
}

func TestManager_SanitizesUserInput(t *testing.T) {
	dir := t.TempDir()
	m := New(testOptions(dir))
	defer m.Close()

	e := sampleEntry()
	e.UserAgent = "evil\x1b[31m\nagent \U0001F608"
	m.Access(e)
	got := readLog(t, dir, "access.log")
	if strings.Contains(got, "\x1b") {
		t.Errorf("ANSI escape leaked into access.log: %q", got)
	}
	if strings.Count(got, "\n") != 1 {
		t.Errorf("injected newline survived: %q", got)
	}
}

func TestManager_NilSafe(t *testing.T) {
	var m *Manager
	// None of these may panic.
	m.Access(sampleEntry())
	m.Server("info", "x")
	m.Error("x")
	m.App("info", "x")
	m.Auth("u", "ip", "fail", "r")
	m.Debug("x")
	m.Close()
	if err := m.RotateCheck(); err != nil {
		t.Errorf("nil RotateCheck = %v, want nil", err)
	}
}

func TestManager_EmptyDirDisablesFiles(t *testing.T) {
	m := New(testOptions(""))
	defer m.Close()
	// No writers configured — calls are no-ops and must not panic.
	m.Access(sampleEntry())
	m.Server("info", "x")
	if err := m.RotateCheck(); err != nil {
		t.Errorf("RotateCheck with no files = %v", err)
	}
}

func TestManager_RotateCheck(t *testing.T) {
	dir := t.TempDir()
	m := New(testOptions(dir))
	defer m.Close()

	m.Access(sampleEntry())
	m.App("info", "hello")
	if err := m.RotateCheck(); err != nil {
		t.Fatalf("RotateCheck: %v", err)
	}
	// Fresh files inside their period: nothing rotated.
	for _, name := range []string{"access.log", "app.log"} {
		if rotated, _ := filepath.Glob(filepath.Join(dir, name+".*")); len(rotated) != 0 {
			t.Errorf("%s rotated prematurely: %v", name, rotated)
		}
	}
}

func TestManager_InvalidPoliciesDefault(t *testing.T) {
	dir := t.TempDir()
	opts := testOptions(dir)
	opts.App.Rotate = "bogus"
	opts.App.Keep = "alsobogus"
	m := New(opts)
	defer m.Close()

	// Invalid policies warn-and-default; writes must still work.
	m.App("info", "still works")
	got := readLog(t, dir, "app.log")
	if !strings.Contains(got, "still works") {
		t.Errorf("app line = %q", got)
	}
}
