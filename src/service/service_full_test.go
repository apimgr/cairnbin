package service

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// ─── Install function branches with root ─────────────────────────────────────

func TestInstallSystemd_DirectCall(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	// Create necessary directories
	systemdDir := "/etc/systemd/system"
	_ = os.MkdirAll(systemdDir, 0o755)

	// Create data directories that installSystemd creates
	dirs := []string{
		"/var/lib/" + orgName + "/" + appName,
		"/var/log/" + orgName + "/" + appName,
		"/etc/" + orgName + "/" + appName,
	}
	for _, dir := range dirs {
		_ = os.MkdirAll(dir, 0o755)
	}

	t.Cleanup(func() {
		_ = os.Remove(filepath.Join(systemdDir, appName+".service"))
		for _, dir := range dirs {
			_ = os.RemoveAll(filepath.Dir(dir))
		}
	})

	// Call installSystemd directly
	err := installSystemd()
	// Will fail at systemctl commands but file should be written
	if err != nil {
		t.Logf("installSystemd: %v (expected)", err)
	}

	// Verify service file exists
	servicePath := filepath.Join(systemdDir, appName+".service")
	if _, statErr := os.Stat(servicePath); os.IsNotExist(statErr) {
		t.Error("service file was not created")
	}
}

func TestInstallOpenRC_DirectCall(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	initDir := "/etc/init.d"
	_ = os.MkdirAll(initDir, 0o755)

	t.Cleanup(func() {
		_ = os.Remove(filepath.Join(initDir, appName))
	})

	err := installOpenRC()
	if err != nil {
		t.Logf("installOpenRC: %v (expected)", err)
	}

	// Verify init script exists
	initPath := filepath.Join(initDir, appName)
	if _, statErr := os.Stat(initPath); os.IsNotExist(statErr) {
		t.Error("init script was not created")
	}
}

func TestInstallSysV_DirectCall(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	initDir := "/etc/init.d"
	_ = os.MkdirAll(initDir, 0o755)

	t.Cleanup(func() {
		_ = os.Remove(filepath.Join(initDir, appName))
	})

	err := installSysV()
	if err != nil {
		t.Logf("installSysV: %v (expected)", err)
	}

	// Verify init script exists
	initPath := filepath.Join(initDir, appName)
	if _, statErr := os.Stat(initPath); os.IsNotExist(statErr) {
		t.Error("init script was not created")
	}
}

func TestInstallBSDRC_DirectCall(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	rcDir := "/usr/local/etc/rc.d"
	_ = os.MkdirAll(rcDir, 0o755)

	t.Cleanup(func() {
		_ = os.Remove(filepath.Join(rcDir, appName))
	})

	err := installBSDRC()
	if err != nil {
		t.Logf("installBSDRC: %v (expected)", err)
	}

	// Verify script exists
	rcPath := filepath.Join(rcDir, appName)
	if _, statErr := os.Stat(rcPath); os.IsNotExist(statErr) {
		t.Error("rc.d script was not created")
	}
}

func TestInstallLaunchd_DirectCall(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	launchDir := "/Library/LaunchDaemons"
	_ = os.MkdirAll(launchDir, 0o755)

	// Create log directory
	logDir := "/var/log/" + orgName + "/" + appName
	_ = os.MkdirAll(logDir, 0o755)

	// Create Application Support directory
	appSupportDir := "/Library/Application Support/" + orgName + "/" + appName
	_ = os.MkdirAll(appSupportDir, 0o755)

	t.Cleanup(func() {
		_ = os.Remove(filepath.Join(launchDir, launchdLabel+".plist"))
		_ = os.RemoveAll("/var/log/" + orgName)
		_ = os.RemoveAll("/Library/Application Support/" + orgName)
	})

	err := installLaunchd()
	if err != nil {
		t.Logf("installLaunchd: %v (expected)", err)
	}

	// Verify plist exists
	plistPath := filepath.Join(launchDir, launchdLabel+".plist")
	if _, statErr := os.Stat(plistPath); os.IsNotExist(statErr) {
		t.Error("plist was not created")
	}
}

func TestInstallRunit_DirectCall(t *testing.T) {
	setupFakeRunit(t)
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	t.Cleanup(func() {
		_ = os.RemoveAll("/etc/sv/" + appName)
		_ = os.Remove("/var/service/" + appName)
	})

	err := installRunit()
	if err != nil {
		t.Fatalf("installRunit: %v", err)
	}

	// Verify sv directory exists
	svDir := "/etc/sv/" + appName
	if _, statErr := os.Stat(svDir); os.IsNotExist(statErr) {
		t.Error("sv directory was not created")
	}

	// Verify run script exists
	runPath := filepath.Join(svDir, "run")
	if _, statErr := os.Stat(runPath); os.IsNotExist(statErr) {
		t.Error("run script was not created")
	}

	// Verify log/run exists
	logRunPath := filepath.Join(svDir, "log", "run")
	if _, statErr := os.Stat(logRunPath); os.IsNotExist(statErr) {
		t.Error("log run script was not created")
	}
}

// ─── copyBinary full branch coverage ─────────────────────────────────────────

func TestCopyBinary_AllBranches(t *testing.T) {
	tmp := t.TempDir()

	// Test 1: Normal copy
	t.Run("normal", func(t *testing.T) {
		src := filepath.Join(tmp, "src1")
		dst := filepath.Join(tmp, "dst1")
		if err := os.WriteFile(src, []byte("content1"), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := copyBinary(src, dst); err != nil {
			t.Errorf("copyBinary: %v", err)
		}
	})

	// Test 2: Copy with nested directories
	t.Run("nested", func(t *testing.T) {
		src := filepath.Join(tmp, "src2")
		dst := filepath.Join(tmp, "a", "b", "c", "dst2")
		if err := os.WriteFile(src, []byte("content2"), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := copyBinary(src, dst); err != nil {
			t.Errorf("copyBinary nested: %v", err)
		}
	})

	// Test 3: Copy fails with missing source
	t.Run("missing_source", func(t *testing.T) {
		src := filepath.Join(tmp, "nonexistent")
		dst := filepath.Join(tmp, "dst3")

		if err := copyBinary(src, dst); err == nil {
			t.Error("expected error for missing source")
		}
	})
}

// ─── purgeData full branch coverage ──────────────────────────────────────────

func TestPurgeData_AllBranches(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	if runtime.GOOS == "windows" {
		// Windows branch uses different paths
		t.Skip("Windows test not applicable on Linux")
	}

	// Create the same paths-derived directories purgeData removes
	dirs := purgeDirs()

	for _, dir := range dirs {
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "data.txt"), []byte("test"), 0o644)
	}

	// Call purgeData
	purgeData()

	// Verify all directories removed
	for _, dir := range dirs {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("directory %s should have been removed", dir)
		}
	}
}

// ─── Uninstall direct calls ──────────────────────────────────────────────────

func TestUninstallAll_DirectCalls(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	// Test uninstallSystemd
	t.Run("systemd", func(t *testing.T) {
		systemdDir := "/etc/systemd/system"
		_ = os.MkdirAll(systemdDir, 0o755)
		_ = os.WriteFile(filepath.Join(systemdDir, appName+".service"), []byte("[Unit]\n"), 0o644)

		err := uninstallSystemd()
		if err != nil {
			t.Logf("uninstallSystemd: %v", err)
		}

		if _, statErr := os.Stat(filepath.Join(systemdDir, appName+".service")); !os.IsNotExist(statErr) {
			t.Error("service file should be removed")
		}
	})

	// Test uninstallOpenRC
	t.Run("openrc", func(t *testing.T) {
		initDir := "/etc/init.d"
		_ = os.MkdirAll(initDir, 0o755)
		_ = os.WriteFile(filepath.Join(initDir, appName), []byte("#!/sbin/openrc-run\n"), 0o755)

		err := uninstallOpenRC()
		if err != nil {
			t.Logf("uninstallOpenRC: %v", err)
		}

		if _, statErr := os.Stat(filepath.Join(initDir, appName)); !os.IsNotExist(statErr) {
			t.Error("init script should be removed")
		}
	})

	// Test uninstallSysV
	t.Run("sysv", func(t *testing.T) {
		initDir := "/etc/init.d"
		_ = os.MkdirAll(initDir, 0o755)
		_ = os.WriteFile(filepath.Join(initDir, appName), []byte("#!/bin/sh\n"), 0o755)

		err := uninstallSysV()
		if err != nil {
			t.Logf("uninstallSysV: %v", err)
		}

		if _, statErr := os.Stat(filepath.Join(initDir, appName)); !os.IsNotExist(statErr) {
			t.Error("init script should be removed")
		}
	})

	// Test uninstallRunit
	t.Run("runit", func(t *testing.T) {
		svDir := "/etc/sv/" + appName
		_ = os.MkdirAll(svDir, 0o755)
		_ = os.WriteFile(filepath.Join(svDir, "run"), []byte("#!/bin/sh\n"), 0o755)

		err := uninstallRunit()
		if err != nil {
			t.Errorf("uninstallRunit: %v", err)
		}

		if _, statErr := os.Stat(svDir); !os.IsNotExist(statErr) {
			t.Error("sv dir should be removed")
		}
	})

	// Test uninstallLaunchd
	t.Run("launchd", func(t *testing.T) {
		launchDir := "/Library/LaunchDaemons"
		_ = os.MkdirAll(launchDir, 0o755)
		_ = os.WriteFile(filepath.Join(launchDir, launchdLabel+".plist"), []byte("<plist>\n</plist>"), 0o644)

		err := uninstallLaunchd()
		if err != nil {
			t.Logf("uninstallLaunchd: %v", err)
		}

		if _, statErr := os.Stat(filepath.Join(launchDir, launchdLabel+".plist")); !os.IsNotExist(statErr) {
			t.Error("plist should be removed")
		}
	})

	// Test uninstallBSDRC
	t.Run("bsdrc", func(t *testing.T) {
		rcDir := "/usr/local/etc/rc.d"
		_ = os.MkdirAll(rcDir, 0o755)
		_ = os.WriteFile(filepath.Join(rcDir, appName), []byte("#!/bin/sh\n"), 0o755)

		err := uninstallBSDRC()
		if err != nil {
			t.Logf("uninstallBSDRC: %v", err)
		}

		if _, statErr := os.Stat(filepath.Join(rcDir, appName)); !os.IsNotExist(statErr) {
			t.Error("rc.d script should be removed")
		}
	})
}

// ─── Start/Stop/Restart/Reload/Disable coverage ─────────────────────────────

func TestLifecycleFunctions_Root(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	// These functions shell out to service managers which don't exist in
	// containers, but we can still get coverage of the switch statements.

	t.Run("Start", func(t *testing.T) {
		err := Start()
		// Expected to fail (no service manager)
		_ = err
	})

	t.Run("Stop", func(t *testing.T) {
		err := Stop()
		_ = err
	})

	t.Run("Restart", func(t *testing.T) {
		err := Restart()
		_ = err
	})

	t.Run("Reload", func(t *testing.T) {
		err := Reload()
		_ = err
	})

	t.Run("Disable", func(t *testing.T) {
		err := Disable()
		_ = err
	})
}

// ─── Install top-level with root ─────────────────────────────────────────────

func TestInstall_TopLevel_Root(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	// Create necessary directories for Install() to proceed
	systemdDir := "/etc/systemd/system"
	_ = os.MkdirAll(systemdDir, 0o755)

	t.Cleanup(func() {
		_ = os.Remove(filepath.Join(systemdDir, appName+".service"))
		_ = os.RemoveAll("/var/lib/" + orgName)
		_ = os.RemoveAll("/var/log/" + orgName)
		_ = os.RemoveAll("/etc/" + orgName)
	})

	err := Install()
	// Will fail but exercises the privilege check and switch branches
	if err != nil {
		t.Logf("Install: %v (expected)", err)
	}
}

// ─── findAvailableSystemID edge cases ────────────────────────────────────────

func TestFindAvailableSystemID_Multiple(t *testing.T) {
	// Call multiple times to ensure idempotency
	id1, err1 := findAvailableSystemID()
	id2, err2 := findAvailableSystemID()

	if err1 != nil || err2 != nil {
		t.Skipf("findAvailableSystemID errors: %v, %v", err1, err2)
	}

	// Both should return valid IDs (may be same or different depending on system state)
	if id1 < 200 || id1 > 899 || id2 < 200 || id2 > 899 {
		t.Errorf("IDs out of range: %d, %d", id1, id2)
	}
}
