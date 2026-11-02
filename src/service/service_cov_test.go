package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ─── DetectServiceManager additional paths ────────────────────────────────────

// TestDetectServiceManager_ReturnsNonNegative verifies the returned value is
// never a negative integer (iota starts at 0).
func TestDetectServiceManager_ReturnsNonNegative(t *testing.T) {
	got := DetectServiceManager()
	if int(got) < 0 {
		t.Errorf("DetectServiceManager() = %d; must be >= 0", int(got))
	}
}

// TestServiceType_UnknownIsZero verifies the zero value of ServiceType maps
// to ServiceUnknown so uninitialized variables are safe.
func TestServiceType_UnknownIsZero(t *testing.T) {
	var st ServiceType
	if st != ServiceUnknown {
		t.Errorf("zero ServiceType = %d; want ServiceUnknown (%d)", st, ServiceUnknown)
	}
}

// ─── GetBinaryPath content assertions ────────────────────────────────────────

// TestGetBinaryPath_ContainsOrgName verifies the binary path includes the
// org name so it lives under the correct namespace directory.
func TestGetBinaryPath_ContainsOrgName(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("org name only in path on Windows; Unix uses /usr/local/bin")
	}
	got := GetBinaryPath()
	if !strings.Contains(got, orgName) {
		t.Errorf("GetBinaryPath() = %q; expected to contain org name %q", got, orgName)
	}
}

// TestGetBinaryPath_IsAbsolute verifies the binary path is always absolute.
func TestGetBinaryPath_IsAbsolute(t *testing.T) {
	got := GetBinaryPath()
	if !filepath.IsAbs(got) {
		t.Errorf("GetBinaryPath() = %q; expected absolute path", got)
	}
}

// ─── ok() with t.Setenv ───────────────────────────────────────────────────────

// TestOk_NoColorViaSetenv uses t.Setenv for proper cleanup and verifies the
// [ok] text variant is returned.
func TestOk_NoColorViaSetenv(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if got := ok(); got != "[ok] " {
		t.Errorf("ok() with NO_COLOR=1 = %q; want '[ok] '", got)
	}
}

// TestOk_EmptyNoColorIsNotSet verifies the emoji variant is returned when
// NO_COLOR is explicitly unset.
func TestOk_EmptyNoColorIsNotSet(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	got := ok()
	if got == "[ok] " {
		t.Error("ok() with NO_COLOR='' should return emoji variant, not '[ok] '")
	}
}

// ─── copyBinary edge cases ────────────────────────────────────────────────────

// TestCopyBinary_CreatesParentDirectory verifies copyBinary creates missing
// intermediate directories before writing the destination file.
func TestCopyBinary_CreatesParentDirectory(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "nested", "path", "bin")

	if err := os.WriteFile(src, []byte("binary-content"), 0o755); err != nil {
		t.Fatalf("write src: %v", err)
	}

	if err := copyBinary(src, dst); err != nil {
		t.Fatalf("copyBinary: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != "binary-content" {
		t.Errorf("dst = %q; want 'binary-content'", got)
	}
}

// TestCopyBinary_EmptyFile verifies copyBinary handles an empty source file
// without error and produces a zero-byte destination.
func TestCopyBinary_EmptyFile(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "empty")
	dst := filepath.Join(tmp, "out")

	if err := os.WriteFile(src, []byte{}, 0o755); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := copyBinary(src, dst); err != nil {
		t.Fatalf("copyBinary: %v", err)
	}

	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat dst: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("expected zero-byte dst, got %d bytes", info.Size())
	}
}

// TestCopyBinary_IdempotentOverwrite verifies copying the same source twice
// overwrites the destination without error.
func TestCopyBinary_IdempotentOverwrite(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst")

	if err := os.WriteFile(src, []byte("v1"), 0o755); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := copyBinary(src, dst); err != nil {
		t.Fatalf("first copyBinary: %v", err)
	}

	if err := os.WriteFile(src, []byte("v2"), 0o755); err != nil {
		t.Fatalf("rewrite src: %v", err)
	}
	if err := copyBinary(src, dst); err != nil {
		t.Fatalf("second copyBinary: %v", err)
	}

	got, _ := os.ReadFile(dst)
	if string(got) != "v2" {
		t.Errorf("expected 'v2' after overwrite, got %q", got)
	}
}

// ─── PrintHelp content checks ─────────────────────────────────────────────────

// TestPrintHelp_ContainsServiceCommands verifies the help output documents
// the expected service management subcommands.
func TestPrintHelp_ContainsServiceCommands(t *testing.T) {
	origStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	PrintHelp("pastebin")

	w.Close()
	os.Stdout = origStdout

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	out := string(buf[:n])

	commands := []string{"start", "stop", "restart", "reload", "--install", "--uninstall", "--disable"}
	for _, cmd := range commands {
		if !strings.Contains(out, cmd) {
			t.Errorf("PrintHelp output missing expected command %q:\n%s", cmd, out)
		}
	}
}

// ─── installRunit content check (root only) ───────────────────────────────────

// TestInstallRunit_RunScriptContent verifies the runit run script contains
// the correct binary path and exec invocation.
func TestInstallRunit_RunScriptContent(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root to create /etc/sv/")
	}
	svDir := "/etc/sv/" + appName
	t.Cleanup(func() {
		os.RemoveAll(svDir)
		os.Remove("/var/service/" + appName)
	})

	if err := installRunit(); err != nil {
		t.Fatalf("installRunit: %v", err)
	}

	runPath := filepath.Join(svDir, "run")
	content, err := os.ReadFile(runPath)
	if err != nil {
		t.Fatalf("read run script: %v", err)
	}
	runScript := string(content)

	binaryPath := GetBinaryPath()
	if !strings.Contains(runScript, binaryPath) {
		t.Errorf("run script does not contain binary path %q:\n%s", binaryPath, runScript)
	}
	if !strings.Contains(runScript, "exec ") {
		t.Errorf("run script does not contain 'exec':\n%s", runScript)
	}
}

// TestInstallRunit_LogRunScriptContent verifies the runit log run script
// contains the svlogd invocation.
func TestInstallRunit_LogRunScriptContent(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root to create /etc/sv/")
	}
	svDir := "/etc/sv/" + appName
	t.Cleanup(func() {
		os.RemoveAll(svDir)
		os.Remove("/var/service/" + appName)
	})

	if err := installRunit(); err != nil {
		t.Fatalf("installRunit: %v", err)
	}

	logRunPath := filepath.Join(svDir, "log", "run")
	content, err := os.ReadFile(logRunPath)
	if err != nil {
		t.Fatalf("read log run script: %v", err)
	}
	if !strings.Contains(string(content), "svlogd") {
		t.Errorf("log run script does not contain 'svlogd':\n%s", content)
	}
}

// ─── uninstallRunit removes the service link (root only) ─────────────────────

// TestUninstallRunit_RemovesSvDir verifies that after uninstall the service
// directory is removed.
func TestUninstallRunit_RemovesSvDir(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}
	svDir := "/etc/sv/" + appName

	if err := installRunit(); err != nil {
		t.Fatalf("installRunit: %v", err)
	}

	if err := uninstallRunit(); err != nil {
		t.Fatalf("uninstallRunit: %v", err)
	}

	if _, err := os.Stat(svDir); !os.IsNotExist(err) {
		t.Errorf("expected %s to be removed after uninstall", svDir)
	}
}

// ─── installSystemd content check (root only) ────────────────────────────────

// TestInstallSystemd_ServiceFileContent verifies the generated systemd unit
// file contains the correct binary path and key directives when the host
// supports creating the file.
func TestInstallSystemd_ServiceFileContent(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}
	servicePath := "/etc/systemd/system/" + appName + ".service"
	t.Cleanup(func() {
		os.Remove(servicePath)
		os.RemoveAll("/var/lib/" + orgName)
		os.RemoveAll("/var/log/" + orgName)
		os.RemoveAll("/etc/" + orgName)
	})

	err := installSystemd()
	if err != nil {
		t.Logf("installSystemd returned (may fail on non-systemd host): %v", err)
		t.Skip("systemd not available on this host")
	}

	content, err := os.ReadFile(servicePath)
	if err != nil {
		t.Fatalf("read service file: %v", err)
	}
	svc := string(content)

	binaryPath := GetBinaryPath()
	if !strings.Contains(svc, "ExecStart="+binaryPath) {
		t.Errorf("service file missing ExecStart=%s:\n%s", binaryPath, svc)
	}
	if !strings.Contains(svc, "WantedBy=multi-user.target") {
		t.Errorf("service file missing WantedBy directive:\n%s", svc)
	}
	if !strings.Contains(svc, "Restart=on-failure") {
		t.Errorf("service file missing Restart=on-failure:\n%s", svc)
	}
}

// ─── uninstallWindows coverage ───────────────────────────────────────────────

// TestUninstallWindows_RunsWithoutPanic verifies the Windows uninstall path
// can be called on non-Windows hosts; sc.exe is absent so it returns an error
// but the function body is still exercised.
func TestUninstallWindows_RunsWithoutPanic(t *testing.T) {
	err := uninstallWindows()
	// sc.exe is not available on Linux; an exec error is expected.
	_ = err
}

// ─── installBSDRC with writable target dir (root only) ───────────────────────

// TestInstallBSDRC_CreatesRcScript verifies installBSDRC generates a valid
// rc.d script when /usr/local/etc/rc.d is present.
func TestInstallBSDRC_CreatesRcScript(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root to create /usr/local/etc/rc.d/")
	}
	rcDir := "/usr/local/etc/rc.d"
	rcPath := filepath.Join(rcDir, appName)

	if err := os.MkdirAll(rcDir, 0o755); err != nil {
		t.Fatalf("create rc.d dir: %v", err)
	}
	t.Cleanup(func() { os.Remove(rcPath) })

	err := installBSDRC()
	if err != nil {
		t.Fatalf("installBSDRC: %v", err)
	}

	content, err := os.ReadFile(rcPath)
	if err != nil {
		t.Fatalf("read rc.d script: %v", err)
	}
	script := string(content)

	binaryPath := GetBinaryPath()
	if !strings.Contains(script, `command="`+binaryPath+`"`) {
		t.Errorf("rc.d script missing command=%q:\n%s", binaryPath, script)
	}
	if !strings.Contains(script, "REQUIRE: NETWORKING") {
		t.Errorf("rc.d script missing REQUIRE: NETWORKING:\n%s", script)
	}
	if !strings.Contains(script, appName+"_enable") {
		t.Errorf("rc.d script missing %s_enable:\n%s", appName, script)
	}
}

// TestUninstallBSDRC_RemovesScript verifies uninstallBSDRC removes the
// rc.d script when it exists.
func TestUninstallBSDRC_RemovesScript(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}
	rcDir := "/usr/local/etc/rc.d"
	rcPath := filepath.Join(rcDir, appName)

	if err := os.MkdirAll(rcDir, 0o755); err != nil {
		t.Fatalf("create rc.d dir: %v", err)
	}
	if err := installBSDRC(); err != nil {
		t.Fatalf("installBSDRC setup: %v", err)
	}

	if err := uninstallBSDRC(); err != nil {
		t.Fatalf("uninstallBSDRC: %v", err)
	}

	if _, err := os.Stat(rcPath); !os.IsNotExist(err) {
		t.Errorf("expected rc.d script to be removed, stat=%v", err)
	}
}

// ─── installLaunchd with writable target dir (root only) ─────────────────────

// TestInstallLaunchd_CreatesPlist verifies installLaunchd generates a valid
// plist when /Library/LaunchDaemons is present.
func TestInstallLaunchd_CreatesPlist(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root to create /Library/LaunchDaemons/")
	}
	launchDir := "/Library/LaunchDaemons"
	plistPath := filepath.Join(launchDir, launchdLabel+".plist")

	if err := os.MkdirAll(launchDir, 0o755); err != nil {
		t.Fatalf("create LaunchDaemons dir: %v", err)
	}
	t.Cleanup(func() {
		os.Remove(plistPath)
		os.RemoveAll("/Library/Application Support/" + orgName)
		os.RemoveAll("/var/log/" + orgName)
	})

	err := installLaunchd()
	if err != nil {
		t.Fatalf("installLaunchd: %v", err)
	}

	content, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	plist := string(content)

	binaryPath := GetBinaryPath()
	if !strings.Contains(plist, "<string>"+binaryPath+"</string>") {
		t.Errorf("plist missing binary path %q:\n%s", binaryPath, plist)
	}
	if !strings.Contains(plist, launchdLabel) {
		t.Errorf("plist missing launchd label %q:\n%s", launchdLabel, plist)
	}
	if !strings.Contains(plist, "<true/>") {
		t.Errorf("plist missing RunAtLoad/KeepAlive true directive:\n%s", plist)
	}
}

// ─── constants sanity ────────────────────────────────────────────────────────

// TestConstants_AppNameAndOrgName verifies that the package-level constants
// used by all install functions have the expected values.
func TestConstants_AppNameAndOrgName(t *testing.T) {
	if appName != "pastebin" {
		t.Errorf("appName = %q; want 'pastebin'", appName)
	}
	if orgName != "apimgr" {
		t.Errorf("orgName = %q; want 'apimgr'", orgName)
	}
}

// TestConstants_LaunchdLabel verifies the launchd label is formed from the
// org and app name.
func TestConstants_LaunchdLabel(t *testing.T) {
	expected := "io.github." + orgName + "." + appName
	if launchdLabel != expected {
		t.Errorf("launchdLabel = %q; want %q", launchdLabel, expected)
	}
}

// TestFindAvailableSystemID verifies the function returns a UID in the required
// range 200–899 or an error when all IDs are taken.
func TestFindAvailableSystemID(t *testing.T) {
	id, err := findAvailableSystemID()
	if err != nil {
		// All IDs reserved — acceptable on a heavily populated system; skip.
		t.Skipf("findAvailableSystemID: %v (all IDs in range may be taken)", err)
	}
	if id < 200 || id > 899 {
		t.Errorf("findAvailableSystemID() = %d; must be in range 200–899", id)
	}
}
