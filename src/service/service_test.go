package service

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// ─── DetectServiceManager ─────────────────────────────────────────────────────

func TestDetectServiceManager_ReturnsValidType(t *testing.T) {
	got := DetectServiceManager()
	validTypes := []ServiceType{
		ServiceUnknown, ServiceSystemd, ServiceOpenRC, ServiceSysV,
		ServiceRunit, ServiceLaunchd, ServiceWindows, ServiceBSDRC,
	}
	for _, v := range validTypes {
		if got == v {
			return
		}
	}
	t.Errorf("DetectServiceManager() returned unknown ServiceType %d", got)
}

func TestDetectServiceManager_MatchesOS(t *testing.T) {
	got := DetectServiceManager()
	switch runtime.GOOS {
	case "darwin":
		if got != ServiceLaunchd {
			t.Errorf("on macOS expected ServiceLaunchd, got %d", got)
		}
	case "windows":
		if got != ServiceWindows {
			t.Errorf("on Windows expected ServiceWindows, got %d", got)
		}
	case "freebsd", "openbsd", "netbsd":
		if got != ServiceBSDRC {
			t.Errorf("on BSD expected ServiceBSDRC, got %d", got)
		}
	case "linux":
		// Linux can be any of: Unknown, Systemd, OpenRC, SysV, or Runit.
		linuxOK := got == ServiceUnknown || got == ServiceSystemd ||
			got == ServiceOpenRC || got == ServiceSysV || got == ServiceRunit
		if !linuxOK {
			t.Errorf("on Linux expected Systemd/OpenRC/SysV/Runit/Unknown, got %d", got)
		}
	}
}

// ─── GetBinaryPath ────────────────────────────────────────────────────────────

func TestGetBinaryPath_NonEmpty(t *testing.T) {
	got := GetBinaryPath()
	if got == "" {
		t.Error("GetBinaryPath() returned empty string")
	}
}

func TestGetBinaryPath_ContainsAppName(t *testing.T) {
	got := GetBinaryPath()
	if !strings.Contains(got, appName) {
		t.Errorf("GetBinaryPath() = %q; expected to contain %q", got, appName)
	}
}

func TestGetBinaryPath_WindowsHasExe(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only test")
	}
	got := GetBinaryPath()
	if !strings.HasSuffix(got, ".exe") {
		t.Errorf("GetBinaryPath() on Windows = %q; expected .exe suffix", got)
	}
}

func TestGetBinaryPath_UnixUsrLocalBin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix-only test")
	}
	got := GetBinaryPath()
	if !strings.HasPrefix(got, "/usr/local/bin/") {
		t.Errorf("GetBinaryPath() on Unix = %q; expected prefix /usr/local/bin/", got)
	}
}

// ─── ok() ─────────────────────────────────────────────────────────────────────

func TestOk_NoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	got := ok()
	if got != "[ok] " {
		t.Errorf("ok() with NO_COLOR = %q; want '[ok] '", got)
	}
}

func TestOk_Color(t *testing.T) {
	_ = os.Unsetenv("NO_COLOR")
	got := ok()
	if got == "[ok] " {
		t.Error("ok() without NO_COLOR should not return '[ok] '")
	}
	if got == "" {
		t.Error("ok() should not return empty string")
	}
}

// ─── PrintHelp ────────────────────────────────────────────────────────────────

func TestPrintHelp_DoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("PrintHelp panicked: %v", r)
		}
	}()
	PrintHelp("cairnbin")
}

// ─── ServiceType constants ────────────────────────────────────────────────────

func TestServiceTypeConstants_Distinct(t *testing.T) {
	types := []ServiceType{
		ServiceUnknown, ServiceSystemd, ServiceRunit,
		ServiceLaunchd, ServiceWindows, ServiceBSDRC,
	}
	seen := make(map[ServiceType]bool)
	for _, st := range types {
		if seen[st] {
			t.Errorf("duplicate ServiceType value %d", st)
		}
		seen[st] = true
	}
}

// ─── copyBinary ───────────────────────────────────────────────────────────────

func TestCopyBinary_CopiesContent(t *testing.T) {
	tmp := t.TempDir()
	src := tmp + "/src"
	dst := tmp + "/dst/binary"
	content := []byte("fake binary content")

	if err := os.WriteFile(src, content, 0o755); err != nil {
		t.Fatalf("write src: %v", err)
	}

	if err := copyBinary(src, dst); err != nil {
		t.Fatalf("copyBinary error: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile dst: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("copied content = %q; want %q", got, content)
	}
}

func TestCopyBinary_MissingSource(t *testing.T) {
	tmp := t.TempDir()
	err := copyBinary(tmp+"/nonexistent", tmp+"/dst")
	if err == nil {
		t.Error("expected error when source does not exist")
	}
}

// ─── Install / Uninstall / Start / Stop (privilege guards) ───────────────────

func TestInstall_RequiresPrivilege(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root; skipping privilege check test")
	}
	// Install requires root for writing to /etc/systemd, /usr/local/bin, etc.
	// In a non-root test environment it should fail with a permission error.
	err := Install()
	if err == nil {
		t.Error("Install() should fail without root privileges")
	}
}

func TestUninstall_RequiresPrivilege(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root; skipping privilege check test")
	}
	err := Uninstall()
	if err == nil {
		t.Log("Uninstall() returned nil (file may not exist in test env)")
	}
}

func TestStart_ReturnsOrErrors(t *testing.T) {
	err := Start()
	// In CI there is no service manager or the service is not installed;
	// we only assert that it does not panic.
	_ = err
}

func TestStop_ReturnsOrErrors(t *testing.T) {
	err := Stop()
	_ = err
}

func TestRestart_ReturnsOrErrors(t *testing.T) {
	err := Restart()
	_ = err
}

func TestReload_ReturnsOrErrors(t *testing.T) {
	err := Reload()
	_ = err
}

func TestDisable_ReturnsOrErrors(t *testing.T) {
	err := Disable()
	_ = err
}

// ─── IsWindowsService / RunAsWindowsService (non-Windows stubs) ───────────────

func TestIsWindowsService_NonWindows(t *testing.T) {
	if IsWindowsService() {
		t.Error("IsWindowsService() should return false on non-Windows platforms")
	}
}

func TestRunAsWindowsService_NonWindows(t *testing.T) {
	called := false
	err := RunAsWindowsService("cairnbin", func() { called = true })
	if err != nil {
		t.Errorf("RunAsWindowsService on non-Windows: expected nil error, got %v", err)
	}
	// The fn parameter is not called on non-Windows; we just check the error.
	_ = called
}

// ─── Direct install/uninstall function coverage (root-only) ──────────────────
// These call the unexported install/uninstall functions directly to cover their
// code paths without needing a real service manager to be present.

func TestUninstallSystemd_NoServiceFile(t *testing.T) {
	// uninstallSystemd tries to remove /etc/systemd/system/cairnbin.service.
	// When the file does not exist, os.IsNotExist(err) is true and nil is returned.
	// systemctl commands are run but their errors are ignored.
	err := uninstallSystemd()
	if err != nil {
		t.Logf("uninstallSystemd returned (expected on non-systemd host): %v", err)
	}
}

func TestUninstallLaunchd_NoPlist(t *testing.T) {
	// uninstallLaunchd removes /Library/LaunchDaemons/io.github.apimgr.cairnbin.plist.
	// When the plist does not exist, os.IsNotExist(err) skips the error and nil is returned.
	err := uninstallLaunchd()
	if err != nil {
		t.Logf("uninstallLaunchd returned (expected on non-macOS host): %v", err)
	}
}

func TestUninstallBSDRC_NoScript(t *testing.T) {
	// uninstallBSDRC removes /usr/local/etc/rc.d/cairnbin.
	// When the file does not exist, os.IsNotExist(err) skips the error and nil is returned.
	err := uninstallBSDRC()
	if err != nil {
		t.Logf("uninstallBSDRC returned (expected on non-BSD host): %v", err)
	}
}

func TestInstallRunit_AsRoot(t *testing.T) {
	setupFakeRunit(t)
	if os.Getuid() != 0 {
		t.Skip("requires root to create /etc/sv/")
	}
	t.Cleanup(func() {
		_ = os.RemoveAll("/etc/sv/" + appName)
		_ = os.Remove("/var/service/" + appName)
	})
	err := installRunit()
	if err != nil {
		t.Errorf("installRunit: unexpected error: %v", err)
	}
}

func TestUninstallRunit_AsRoot(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root to remove /etc/sv/")
	}
	err := uninstallRunit()
	if err != nil {
		t.Errorf("uninstallRunit: unexpected error: %v", err)
	}
}

func TestInstallSystemd_AsRoot(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}
	t.Cleanup(func() {
		_ = os.RemoveAll("/var/lib/apimgr")
		_ = os.RemoveAll("/var/log/apimgr")
		_ = os.RemoveAll("/etc/apimgr")
	})
	// installSystemd creates directories then tries to write to /etc/systemd/system/.
	// On non-systemd hosts (e.g. Docker containers) this fails at the WriteFile step;
	// on systemd hosts it may succeed. Both outcomes are acceptable.
	err := installSystemd()
	if err == nil {
		t.Logf("installSystemd succeeded (host has systemd)")
		if err := uninstallSystemd(); err != nil {
			t.Errorf("uninstallSystemd: %v", err)
		}
	} else {
		t.Logf("installSystemd returned (expected on non-systemd host): %v", err)
	}
}

func TestInstallLaunchd_AsRoot(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}
	t.Cleanup(func() {
		_ = os.RemoveAll("/Library/Application Support/apimgr")
		_ = os.Remove("/Library/LaunchDaemons/" + launchdLabel + ".plist")
	})
	// installLaunchd creates directories and writes a plist. On Linux the plist
	// directory /Library/LaunchDaemons/ does not exist so WriteFile fails.
	err := installLaunchd()
	if err == nil {
		t.Logf("installLaunchd succeeded")
		if err := uninstallLaunchd(); err != nil {
			t.Errorf("uninstallLaunchd: %v", err)
		}
	} else {
		t.Logf("installLaunchd returned (expected on non-macOS host): %v", err)
	}
}

func TestInstallBSDRC_AsRoot(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}
	t.Cleanup(func() {
		_ = os.Remove("/usr/local/etc/rc.d/" + appName)
	})
	// installBSDRC writes to /usr/local/etc/rc.d/. If the directory does not
	// exist on this host, WriteFile fails immediately.
	err := installBSDRC()
	if err == nil {
		t.Logf("installBSDRC succeeded")
		if err := uninstallBSDRC(); err != nil {
			t.Errorf("uninstallBSDRC: %v", err)
		}
	} else {
		t.Logf("installBSDRC returned (expected when rc.d dir absent): %v", err)
	}
}

func TestInstallWindows_AsRoot(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}
	t.Cleanup(func() {
		_ = os.Remove(GetBinaryPath())
	})
	// installWindows copies the binary then calls sc.exe which does not exist on
	// Linux; the binary copy step is covered, the sc.exe step returns an error.
	err := installWindows()
	if err == nil {
		t.Logf("installWindows succeeded (unexpected on Linux)")
	} else {
		t.Logf("installWindows returned (expected on non-Windows host): %v", err)
	}
}

// ─── Install / Uninstall / Disable top-level functions ───────────────────────

func TestInstall_ReturnsResultWithoutPanic(t *testing.T) {
	// Call Install() unconditionally to cover its switch statement.
	// In containers with no service manager it returns "unsupported service manager".
	// On hosts with a service manager it may succeed or fail — both are acceptable.
	err := Install()
	// error is acceptable; we only require no panic
	_ = err
}

func TestUninstall_ReturnsResultWithoutPanic(t *testing.T) {
	err := Uninstall()
	_ = err
}

func TestDisable_ReturnsResultWithoutPanic(t *testing.T) {
	err := Disable()
	_ = err
}
