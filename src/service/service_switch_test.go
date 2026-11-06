package service

import (
	"os"
	"runtime"
	"testing"
)

// ─── Force different service types by manipulating file system ──────────────
// These tests create specific indicator files to force DetectServiceManager to
// return different service types, then exercise the lifecycle functions.

func TestLifecycle_SystemdPath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	// Create systemd indicator
	_ = os.MkdirAll("/run/systemd/system", 0o755)
	t.Cleanup(func() {
		_ = os.RemoveAll("/run/systemd")
	})

	// Verify detection
	st := DetectServiceManager()
	if st != ServiceSystemd {
		t.Skipf("expected SystemSystemd, got %v", st)
	}

	// Exercise all lifecycle functions
	_ = Start()
	_ = Stop()
	_ = Restart()
	_ = Reload()
	_ = Disable()
}

func TestLifecycle_RunitPath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	// Remove systemd indicators
	_ = os.RemoveAll("/run/systemd")
	_ = os.Rename("/etc/systemd", "/etc/systemd.bak")
	_ = os.Remove("/sbin/openrc-run")

	// Create runit indicator
	_ = os.MkdirAll("/run/runit", 0o755)

	t.Cleanup(func() {
		_ = os.RemoveAll("/run/runit")
		_ = os.Rename("/etc/systemd.bak", "/etc/systemd")
	})

	st := DetectServiceManager()
	if st != ServiceRunit {
		t.Skipf("expected ServiceRunit, got %v", st)
	}

	// Exercise lifecycle functions for runit
	_ = Start()
	_ = Stop()
	_ = Restart()
	// Falls back to Restart for runit
	_ = Reload()
}

func TestLifecycle_OpenRCPath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	// Remove systemd indicators
	_ = os.RemoveAll("/run/systemd")
	_ = os.Rename("/etc/systemd", "/etc/systemd.bak")

	// Create openrc indicator
	_ = os.MkdirAll("/sbin", 0o755)
	_ = os.WriteFile("/sbin/openrc-run", []byte("#!/bin/sh\n"), 0o755)

	t.Cleanup(func() {
		_ = os.Remove("/sbin/openrc-run")
		_ = os.Rename("/etc/systemd.bak", "/etc/systemd")
	})

	st := DetectServiceManager()
	if st != ServiceOpenRC {
		t.Skipf("expected ServiceOpenRC, got %v", st)
	}

	// Exercise lifecycle functions for OpenRC
	_ = Start()
	_ = Stop()
	_ = Restart()
	_ = Reload()
	_ = Disable()
}

func TestLifecycle_SysVPath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	// Remove all other indicators
	_ = os.RemoveAll("/run/systemd")
	_ = os.Rename("/etc/systemd", "/etc/systemd.bak")
	_ = os.Remove("/sbin/openrc-run")
	_ = os.RemoveAll("/run/runit")

	// SysV detection: /etc/init.d exists + update-rc.d or chkconfig in PATH
	_ = os.MkdirAll("/etc/init.d", 0o755)

	// Create a fake update-rc.d
	_ = os.WriteFile("/usr/sbin/update-rc.d", []byte("#!/bin/sh\n"), 0o755)

	t.Cleanup(func() {
		_ = os.Rename("/etc/systemd.bak", "/etc/systemd")
		_ = os.Remove("/usr/sbin/update-rc.d")
	})

	// Note: This may still not detect SysV if update-rc.d isn't in PATH
	st := DetectServiceManager()
	t.Logf("DetectServiceManager with /etc/init.d: %v", st)

	// Exercise lifecycle regardless of detection result
	_ = Start()
	_ = Stop()
}

// ─── Install switch branches ─────────────────────────────────────────────────

func TestInstall_OpenRCBranch(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	// Remove systemd indicators
	_ = os.RemoveAll("/run/systemd")
	_ = os.Rename("/etc/systemd", "/etc/systemd.bak")

	// Create openrc indicator
	_ = os.MkdirAll("/sbin", 0o755)
	_ = os.WriteFile("/sbin/openrc-run", []byte("#!/bin/sh\n"), 0o755)

	t.Cleanup(func() {
		_ = os.Remove("/sbin/openrc-run")
		_ = os.Rename("/etc/systemd.bak", "/etc/systemd")
		_ = os.Remove("/etc/init.d/" + appName)
	})

	st := DetectServiceManager()
	if st != ServiceOpenRC {
		t.Skipf("expected ServiceOpenRC, got %v", st)
	}

	// Install should call installOpenRC
	err := Install()
	if err != nil {
		t.Logf("Install (OpenRC) error (expected): %v", err)
	}

	// Verify script was written
	if _, statErr := os.Stat("/etc/init.d/" + appName); os.IsNotExist(statErr) {
		t.Error("OpenRC init script was not created")
	}
}

func TestInstall_RunitBranch(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	// Remove other indicators
	_ = os.RemoveAll("/run/systemd")
	_ = os.Rename("/etc/systemd", "/etc/systemd.bak")
	_ = os.Remove("/sbin/openrc-run")

	// Create runit indicator
	_ = os.MkdirAll("/run/runit", 0o755)

	t.Cleanup(func() {
		_ = os.RemoveAll("/run/runit")
		_ = os.Rename("/etc/systemd.bak", "/etc/systemd")
		_ = os.RemoveAll("/etc/sv/" + appName)
		_ = os.Remove("/var/service/" + appName)
	})

	st := DetectServiceManager()
	if st != ServiceRunit {
		t.Skipf("expected ServiceRunit, got %v", st)
	}

	err := Install()
	if err != nil {
		t.Logf("Install (runit) error: %v", err)
	}

	// Verify sv directory was created
	if _, statErr := os.Stat("/etc/sv/" + appName); os.IsNotExist(statErr) {
		t.Error("runit sv directory was not created")
	}
}

// ─── Uninstall switch branches ───────────────────────────────────────────────

func TestUninstall_OpenRCBranch(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	// Set up OpenRC detection
	_ = os.RemoveAll("/run/systemd")
	_ = os.Rename("/etc/systemd", "/etc/systemd.bak")
	_ = os.MkdirAll("/sbin", 0o755)
	_ = os.WriteFile("/sbin/openrc-run", []byte("#!/bin/sh\n"), 0o755)

	// Create the init script to uninstall
	_ = os.MkdirAll("/etc/init.d", 0o755)
	_ = os.WriteFile("/etc/init.d/"+appName, []byte("#!/sbin/openrc-run\n"), 0o755)

	t.Cleanup(func() {
		_ = os.Remove("/sbin/openrc-run")
		_ = os.Rename("/etc/systemd.bak", "/etc/systemd")
		_ = os.Remove("/etc/init.d/" + appName)
	})

	st := DetectServiceManager()
	if st != ServiceOpenRC {
		t.Skipf("expected ServiceOpenRC, got %v", st)
	}

	// Uninstall requires confirmation - will cancel
	err := Uninstall()
	t.Logf("Uninstall (OpenRC) result: %v", err)
}

// ─── Default/unknown service type branch ─────────────────────────────────────

func TestLifecycle_UnknownType(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	// Remove ALL indicators
	_ = os.RemoveAll("/run/systemd")
	_ = os.Rename("/etc/systemd", "/etc/systemd.bak")
	_ = os.Remove("/sbin/openrc-run")
	_ = os.RemoveAll("/run/runit")
	_ = os.Rename("/etc/init.d", "/etc/init.d.bak")

	t.Cleanup(func() {
		_ = os.Rename("/etc/systemd.bak", "/etc/systemd")
		_ = os.Rename("/etc/init.d.bak", "/etc/init.d")
	})

	st := DetectServiceManager()
	if st != ServiceUnknown {
		t.Skipf("expected ServiceUnknown, got %v", st)
	}

	// All lifecycle functions should return "unsupported service manager"
	err := Start()
	if err == nil {
		t.Error("Start should fail with unknown service type")
	}

	err = Stop()
	if err == nil {
		t.Error("Stop should fail with unknown service type")
	}

	err = Install()
	if err == nil {
		t.Error("Install should fail with unknown service type")
	}
}
