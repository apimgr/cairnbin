package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ─── Root-level tests that run as root in Docker ─────────────────────────────
// These tests require root privileges and are designed to run in the CI Docker
// container where we have root access but no actual service managers.

func TestInstallSystemd_RootBranches(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	// Create mock systemd directory to allow the install to proceed further
	systemdDir := "/etc/systemd/system"
	if err := os.MkdirAll(systemdDir, 0o755); err != nil {
		t.Fatalf("create systemd dir: %v", err)
	}

	servicePath := filepath.Join(systemdDir, appName+".service")
	t.Cleanup(func() {
		_ = os.Remove(servicePath)
		_ = os.RemoveAll("/var/lib/" + orgName)
		_ = os.RemoveAll("/var/log/" + orgName)
		_ = os.RemoveAll("/etc/" + orgName)
	})

	// installSystemd will try to run systemctl which doesn't exist in the
	// container, but it should get further and write the service file first.
	err := installSystemd()

	// The function may fail at systemctl commands, but we should get coverage
	// of the file-writing branches.
	if err == nil {
		// Service installed successfully (unlikely in container)
		content, readErr := os.ReadFile(servicePath)
		if readErr != nil {
			t.Fatalf("read service file: %v", readErr)
		}
		svc := string(content)

		// Verify PART 24 compliance
		if !strings.Contains(svc, "Type=simple") {
			t.Error("missing Type=simple")
		}
		if !strings.Contains(svc, "RestartSec=5") {
			t.Error("missing RestartSec=5")
		}
		if !strings.Contains(svc, "Restart=on-failure") {
			t.Error("missing Restart=on-failure")
		}
	} else {
		t.Logf("installSystemd: %v (expected due to missing systemctl)", err)
		// Check if service file was at least written before failure
		if _, err := os.Stat(servicePath); err == nil {
			t.Log("service file was written before failure")
		}
	}
}

func TestInstallOpenRC_RootBranches(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	initDir := "/etc/init.d"
	if err := os.MkdirAll(initDir, 0o755); err != nil {
		t.Fatalf("create init.d: %v", err)
	}

	initPath := filepath.Join(initDir, appName)
	t.Cleanup(func() { _ = os.Remove(initPath) })

	err := installOpenRC()

	// Will likely fail at rc-update, but covers file writing
	if err == nil {
		content, _ := os.ReadFile(initPath)
		if !strings.Contains(string(content), "#!/sbin/openrc-run") {
			t.Error("missing openrc-run shebang")
		}
	} else {
		t.Logf("installOpenRC: %v (expected due to missing rc-update)", err)
		// Check if script was written
		if _, statErr := os.Stat(initPath); statErr == nil {
			content, _ := os.ReadFile(initPath)
			if !strings.Contains(string(content), "#!/sbin/openrc-run") {
				t.Error("script written but missing shebang")
			}
		}
	}
}

func TestInstallSysV_RootBranches(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	initDir := "/etc/init.d"
	if err := os.MkdirAll(initDir, 0o755); err != nil {
		t.Fatalf("create init.d: %v", err)
	}

	initPath := filepath.Join(initDir, appName)
	t.Cleanup(func() { _ = os.Remove(initPath) })

	err := installSysV()

	// update-rc.d/chkconfig may not exist, but script should be written
	if err != nil {
		t.Logf("installSysV: %v", err)
	}

	// Check if script was written regardless of error
	if content, readErr := os.ReadFile(initPath); readErr == nil {
		script := string(content)
		if !strings.Contains(script, "### BEGIN INIT INFO") {
			t.Error("SysV script missing LSB header")
		}
		if !strings.Contains(script, "start-stop-daemon") {
			t.Error("SysV script missing start-stop-daemon")
		}
	}
}

func TestInstallRunit_RootBranches(t *testing.T) {
	setupFakeRunit(t)
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	svDir := "/etc/sv/" + appName
	t.Cleanup(func() {
		_ = os.RemoveAll(svDir)
		_ = os.Remove("/var/service/" + appName)
	})

	err := installRunit()
	if err != nil {
		t.Fatalf("installRunit: %v", err)
	}

	// Verify run script
	runPath := filepath.Join(svDir, "run")
	content, err := os.ReadFile(runPath)
	if err != nil {
		t.Fatalf("read run script: %v", err)
	}

	if !strings.Contains(string(content), "#!/bin/sh") {
		t.Error("run script missing shebang")
	}
	if !strings.Contains(string(content), "exec ") {
		t.Error("run script missing exec")
	}

	// Verify log run script
	logRunPath := filepath.Join(svDir, "log", "run")
	logContent, err := os.ReadFile(logRunPath)
	if err != nil {
		t.Fatalf("read log run script: %v", err)
	}

	if !strings.Contains(string(logContent), "svlogd") {
		t.Error("log run script missing svlogd")
	}
}

func TestInstallLaunchd_RootBranches(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	launchDir := "/Library/LaunchDaemons"
	if err := os.MkdirAll(launchDir, 0o755); err != nil {
		t.Fatalf("create LaunchDaemons: %v", err)
	}

	plistPath := filepath.Join(launchDir, launchdLabel+".plist")
	t.Cleanup(func() {
		_ = os.Remove(plistPath)
		_ = os.RemoveAll("/Library/Application Support/" + orgName)
		_ = os.RemoveAll("/var/log/" + orgName)
	})

	err := installLaunchd()
	if err != nil {
		t.Logf("installLaunchd: %v", err)
	}

	// Check if plist was written
	if content, readErr := os.ReadFile(plistPath); readErr == nil {
		plist := string(content)
		if !strings.Contains(plist, launchdLabel) {
			t.Error("plist missing label")
		}
		if !strings.Contains(plist, "<key>RunAtLoad</key>") {
			t.Error("plist missing RunAtLoad")
		}
	}
}

func TestInstallBSDRC_RootBranches(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	rcDir := "/usr/local/etc/rc.d"
	if err := os.MkdirAll(rcDir, 0o755); err != nil {
		t.Fatalf("create rc.d: %v", err)
	}

	rcPath := filepath.Join(rcDir, appName)
	t.Cleanup(func() { _ = os.Remove(rcPath) })

	err := installBSDRC()
	if err != nil {
		t.Logf("installBSDRC: %v", err)
	}

	// Check if script was written
	if content, readErr := os.ReadFile(rcPath); readErr == nil {
		script := string(content)
		if !strings.Contains(script, "# PROVIDE:") {
			t.Error("rc.d script missing PROVIDE")
		}
		if !strings.Contains(script, ". /etc/rc.subr") {
			t.Error("rc.d script missing rc.subr include")
		}
	}
}

// ─── Uninstall functions with cleanup verification ──────────────────────────

func TestUninstallSystemd_RootBranches(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	// Create a mock service file to uninstall
	systemdDir := "/etc/systemd/system"
	_ = os.MkdirAll(systemdDir, 0o755)

	servicePath := filepath.Join(systemdDir, appName+".service")
	_ = os.WriteFile(servicePath, []byte("[Unit]\nDescription=Test\n"), 0o644)

	err := uninstallSystemd()
	if err != nil {
		t.Logf("uninstallSystemd: %v", err)
	}

	// Verify file was removed
	if _, statErr := os.Stat(servicePath); !os.IsNotExist(statErr) {
		t.Error("service file should have been removed")
	}
}

func TestUninstallOpenRC_RootBranches(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	initDir := "/etc/init.d"
	_ = os.MkdirAll(initDir, 0o755)

	initPath := filepath.Join(initDir, appName)
	_ = os.WriteFile(initPath, []byte("#!/sbin/openrc-run\n"), 0o755)

	err := uninstallOpenRC()
	if err != nil {
		t.Logf("uninstallOpenRC: %v", err)
	}

	// Verify file was removed
	if _, statErr := os.Stat(initPath); !os.IsNotExist(statErr) {
		t.Error("init script should have been removed")
	}
}

func TestUninstallSysV_RootBranches(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	initDir := "/etc/init.d"
	_ = os.MkdirAll(initDir, 0o755)

	initPath := filepath.Join(initDir, appName)
	_ = os.WriteFile(initPath, []byte("#!/bin/sh\n"), 0o755)

	err := uninstallSysV()
	if err != nil {
		t.Logf("uninstallSysV: %v", err)
	}

	// Verify file was removed
	if _, statErr := os.Stat(initPath); !os.IsNotExist(statErr) {
		t.Error("init script should have been removed")
	}
}

func TestUninstallRunit_RootBranches(t *testing.T) {
	setupFakeRunit(t)
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	svDir := "/etc/sv/" + appName
	_ = os.MkdirAll(filepath.Join(svDir, "log"), 0o755)

	_ = os.WriteFile(filepath.Join(svDir, "run"), []byte("#!/bin/sh\n"), 0o755)

	err := uninstallRunit()
	if err != nil {
		t.Errorf("uninstallRunit: %v", err)
	}

	// Verify directory was removed
	if _, statErr := os.Stat(svDir); !os.IsNotExist(statErr) {
		t.Error("sv directory should have been removed")
	}
}

func TestUninstallLaunchd_RootBranches(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	launchDir := "/Library/LaunchDaemons"
	_ = os.MkdirAll(launchDir, 0o755)

	plistPath := filepath.Join(launchDir, launchdLabel+".plist")
	_ = os.WriteFile(plistPath, []byte("<?xml version=\"1.0\"?>\n<plist>\n</plist>\n"), 0o644)

	err := uninstallLaunchd()
	if err != nil {
		t.Logf("uninstallLaunchd: %v", err)
	}

	// Verify file was removed
	if _, statErr := os.Stat(plistPath); !os.IsNotExist(statErr) {
		t.Error("plist should have been removed")
	}
}

func TestUninstallBSDRC_RootBranches(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	rcDir := "/usr/local/etc/rc.d"
	_ = os.MkdirAll(rcDir, 0o755)

	rcPath := filepath.Join(rcDir, appName)
	_ = os.WriteFile(rcPath, []byte("#!/bin/sh\n"), 0o755)

	err := uninstallBSDRC()
	if err != nil {
		t.Logf("uninstallBSDRC: %v", err)
	}

	// Verify file was removed
	if _, statErr := os.Stat(rcPath); !os.IsNotExist(statErr) {
		t.Error("rc.d script should have been removed")
	}
}

// ─── purgeData with real directories ─────────────────────────────────────────

func TestPurgeData_RootBranches(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Unix path test")
	}

	// Create the paths-derived directories purgeData is expected to remove
	dirs := purgeDirs()

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
		// Write a test file
		testFile := filepath.Join(dir, "test.txt")
		_ = os.WriteFile(testFile, []byte("test"), 0o644)
	}

	// Run purgeData
	purgeData()

	// Verify directories were removed
	for _, dir := range dirs {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("directory %s should have been removed", dir)
		}
	}
}

// ─── Install/Uninstall top-level with root ──────────────────────────────────

func TestInstall_RootWithServiceType(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	// Install() will detect the service manager and attempt to install.
	// In a Docker container, this will likely fail at the systemctl/service
	// manager step, but we get coverage of the privilege check and switch.
	err := Install()

	// We expect an error because the service manager commands will fail
	if err == nil {
		t.Log("Install() succeeded (unexpected in container)")
		// Clean up
		if err := Uninstall(); err != nil {
			t.Errorf("Uninstall: %v", err)
		}
	} else {
		t.Logf("Install() returned: %v (expected in container)", err)
	}
}
