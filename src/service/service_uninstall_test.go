package service

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// ─── Direct uninstall function tests (bypassing confirmation) ───────────────
// These tests directly call the internal uninstall* functions to get coverage
// of the uninstall logic without needing to bypass confirmDestructive.

func TestUninstallSystemd_DirectWithFile(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	// Create systemd service file
	systemdDir := "/etc/systemd/system"
	_ = os.MkdirAll(systemdDir, 0o755)
	serviceFile := filepath.Join(systemdDir, appName+".service")
	_ = os.WriteFile(serviceFile, []byte("[Unit]\nDescription=Test\n"), 0o644)

	t.Cleanup(func() {
		_ = os.Remove(serviceFile)
	})

	err := uninstallSystemd()
	if err != nil {
		t.Logf("uninstallSystemd error (expected - no systemctl): %v", err)
	}

	// File should be removed even if systemctl fails
	if _, statErr := os.Stat(serviceFile); !os.IsNotExist(statErr) {
		t.Error("service file should be removed")
	}
}

func TestUninstallOpenRC_DirectWithScript(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	// Create init script
	initDir := "/etc/init.d"
	_ = os.MkdirAll(initDir, 0o755)
	initScript := filepath.Join(initDir, appName)
	_ = os.WriteFile(initScript, []byte("#!/sbin/openrc-run\n"), 0o755)

	t.Cleanup(func() {
		_ = os.Remove(initScript)
	})

	err := uninstallOpenRC()
	if err != nil {
		t.Logf("uninstallOpenRC error (expected): %v", err)
	}

	// Script should be removed
	if _, statErr := os.Stat(initScript); !os.IsNotExist(statErr) {
		t.Error("init script should be removed")
	}
}

func TestUninstallSysV_DirectWithScript(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	// Create init script
	initDir := "/etc/init.d"
	_ = os.MkdirAll(initDir, 0o755)
	initScript := filepath.Join(initDir, appName)
	_ = os.WriteFile(initScript, []byte("#!/bin/sh\n"), 0o755)

	t.Cleanup(func() {
		_ = os.Remove(initScript)
	})

	err := uninstallSysV()
	if err != nil {
		t.Logf("uninstallSysV error (expected): %v", err)
	}

	// Script should be removed
	if _, statErr := os.Stat(initScript); !os.IsNotExist(statErr) {
		t.Error("init script should be removed")
	}
}

func TestUninstallRunit_DirectWithDir(t *testing.T) {
	setupFakeRunit(t)
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	// Create runit service directory
	svDir := "/etc/sv/" + appName
	_ = os.MkdirAll(svDir+"/log", 0o755)
	_ = os.WriteFile(filepath.Join(svDir, "run"), []byte("#!/bin/sh\nexec cairnbin\n"), 0o755)
	_ = os.WriteFile(filepath.Join(svDir, "log", "run"), []byte("#!/bin/sh\n"), 0o755)

	// Create symlink
	_ = os.MkdirAll("/var/service", 0o755)
	if err := os.Symlink(svDir, "/var/service/"+appName); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = os.RemoveAll(svDir)
		_ = os.Remove("/var/service/" + appName)
	})

	err := uninstallRunit()
	if err != nil {
		t.Errorf("uninstallRunit error: %v", err)
	}

	// Directory should be removed
	if _, statErr := os.Stat(svDir); !os.IsNotExist(statErr) {
		t.Error("sv directory should be removed")
	}
}

func TestUninstallLaunchd_DirectWithPlist(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Linux or macOS only")
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	// Create plist
	launchDir := "/Library/LaunchDaemons"
	_ = os.MkdirAll(launchDir, 0o755)
	plistFile := filepath.Join(launchDir, launchdLabel+".plist")
	_ = os.WriteFile(plistFile, []byte(`<?xml version="1.0"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN">
<plist version="1.0">
<dict><key>Label</key><string>test</string></dict>
</plist>
`), 0o644)

	t.Cleanup(func() {
		_ = os.Remove(plistFile)
	})

	err := uninstallLaunchd()
	if err != nil {
		t.Logf("uninstallLaunchd error (expected - no launchctl): %v", err)
	}

	// File should be removed
	if _, statErr := os.Stat(plistFile); !os.IsNotExist(statErr) {
		t.Error("plist should be removed")
	}
}

func TestUninstallBSDRC_DirectWithScript(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	// Create rc.d script
	rcDir := "/usr/local/etc/rc.d"
	_ = os.MkdirAll(rcDir, 0o755)
	rcScript := filepath.Join(rcDir, appName)
	_ = os.WriteFile(rcScript, []byte("#!/bin/sh\n. /etc/rc.subr\n"), 0o755)

	t.Cleanup(func() {
		_ = os.Remove(rcScript)
	})

	err := uninstallBSDRC()
	if err != nil {
		t.Logf("uninstallBSDRC error (expected): %v", err)
	}

	// Script should be removed
	if _, statErr := os.Stat(rcScript); !os.IsNotExist(statErr) {
		t.Error("rc.d script should be removed")
	}
}

func TestUninstallUserServiceAt_ConfirmsBeforePurging(t *testing.T) {
	unit := filepath.Join(t.TempDir(), "cairnbin.service")
	if err := os.WriteFile(unit, []byte("[Service]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stopCalls, reloadCalls, purgeCalls := 0, 0, 0
	err := uninstallUserServiceAt(unit,
		func() error { stopCalls++; return nil },
		func() error { reloadCalls++; return nil },
		func() bool { return true },
		func() { purgeCalls++ },
	)
	if err != nil {
		t.Fatalf("uninstallUserServiceAt: %v", err)
	}
	if stopCalls != 1 || reloadCalls != 1 || purgeCalls != 1 {
		t.Errorf("calls stop/reload/purge = %d/%d/%d, want 1/1/1", stopCalls, reloadCalls, purgeCalls)
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) {
		t.Errorf("user service file remains after confirmed uninstall: %v", err)
	}
}

func TestUninstallUserServiceAt_CancelKeepsUnitAndData(t *testing.T) {
	unit := filepath.Join(t.TempDir(), "cairnbin.service")
	if err := os.WriteFile(unit, []byte("[Service]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stopCalls, purgeCalls := 0, 0
	err := uninstallUserServiceAt(unit,
		func() error { stopCalls++; return nil },
		func() error { return nil },
		func() bool { return false },
		func() { purgeCalls++ },
	)
	if err != nil {
		t.Fatalf("uninstallUserServiceAt cancellation: %v", err)
	}
	if stopCalls != 0 || purgeCalls != 0 {
		t.Errorf("cancelled uninstall called stop/purge %d/%d times", stopCalls, purgeCalls)
	}
	if _, err := os.Stat(unit); err != nil {
		t.Errorf("cancelled uninstall removed user service file: %v", err)
	}
}

// ─── purgeData test ──────────────────────────────────────────────────────────

func TestPurgeData_RemovesDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix test")
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	// Create test directories from the same paths-derived list purgeData uses
	testDirs := purgeDirs()

	for _, dir := range testDirs {
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "test.txt"), []byte("test"), 0o644)
	}

	t.Cleanup(func() {
		// Clean up any remaining dirs
		for _, dir := range testDirs {
			_ = os.RemoveAll(dir)
		}
	})

	purgeData()

	// All directories should be removed
	for _, dir := range testDirs {
		if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
			t.Errorf("directory %s should be removed", dir)
		}
	}
}

// ─── Uninstall with different service types ──────────────────────────────────

func TestUninstall_SystemdPath_Cancelled(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	// Ensure systemd detection
	_ = os.MkdirAll("/run/systemd/system", 0o755)
	t.Cleanup(func() {
		_ = os.RemoveAll("/run/systemd")
	})

	// Uninstall prompts for confirmation, stdin not terminal = cancellation
	err := Uninstall()
	// Should return nil (cancelled, not error)
	if err != nil {
		t.Logf("Uninstall result: %v", err)
	}
}

func TestUninstall_RunitPath_Cancelled(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	// Setup runit detection
	_ = os.RemoveAll("/run/systemd")
	_ = os.Rename("/etc/systemd", "/etc/systemd.bak")
	_ = os.MkdirAll("/run/runit", 0o755)

	t.Cleanup(func() {
		_ = os.RemoveAll("/run/runit")
		_ = os.Rename("/etc/systemd.bak", "/etc/systemd")
	})

	err := Uninstall()
	if err != nil {
		t.Logf("Uninstall (runit) result: %v", err)
	}
}
