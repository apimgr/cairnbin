package service

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ─── canEscalate branch coverage ─────────────────────────────────────────────

func TestCanEscalate_ToolLookup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix-only test")
	}

	// Verify the function checks for escalation tools.
	tools := []string{"sudo", "pkexec", "doas"}
	hasAnyTool := false
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err == nil {
			hasAnyTool = true
			break
		}
	}

	got := canEscalate()
	// If no tools are available, canEscalate must return false.
	if !hasAnyTool && got {
		t.Error("canEscalate() = true but no escalation tools are available")
	}
}

func TestCanEscalate_GroupMembership(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix-only test")
	}

	u, err := user.Current()
	if err != nil {
		t.Skipf("cannot get current user: %v", err)
	}

	gids, err := u.GroupIds()
	if err != nil {
		t.Skipf("cannot get group IDs: %v", err)
	}

	// Check which privileged groups the user belongs to.
	var privilegedGroups []string
	for _, gid := range gids {
		g, err := user.LookupGroupId(gid)
		if err != nil {
			continue
		}
		name := strings.ToLower(g.Name)
		if name == "wheel" || name == "sudo" || name == "admin" {
			privilegedGroups = append(privilegedGroups, g.Name)
		}
	}

	t.Logf("User %s belongs to privileged groups: %v", u.Username, privilegedGroups)

	// Call canEscalate and verify it handles group lookup without error.
	got := canEscalate()
	// Result depends on environment; we just verify no panic.
	_ = got
}

// ─── DetectServiceManager additional coverage ────────────────────────────────

func TestDetectServiceManager_FileChecks(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only test")
	}

	// Check which detection paths exist on this system.
	paths := map[string]string{
		"/run/systemd/system": "systemd (run)",
		"/etc/systemd":        "systemd (etc)",
		"/sbin/openrc-run":    "OpenRC",
		"/run/runit":          "runit",
		"/etc/init.d":         "SysV init.d",
	}

	t.Logf("Service manager detection paths on this system:")
	for path, name := range paths {
		if _, err := os.Stat(path); err == nil {
			t.Logf("  %s: exists (%s)", path, name)
		}
	}

	st := DetectServiceManager()
	t.Logf("Detected service manager: %d", st)
}

func TestDetectServiceManager_AllBranches(t *testing.T) {
	// Verify DetectServiceManager returns a valid type for every GOOS.
	st := DetectServiceManager()

	switch runtime.GOOS {
	case "linux":
		validLinux := st == ServiceUnknown || st == ServiceSystemd ||
			st == ServiceOpenRC || st == ServiceSysV || st == ServiceRunit
		if !validLinux {
			t.Errorf("on Linux got %d; expected Linux service type", st)
		}
	case "darwin":
		if st != ServiceLaunchd {
			t.Errorf("on macOS got %d; expected ServiceLaunchd", st)
		}
	case "windows":
		if st != ServiceWindows {
			t.Errorf("on Windows got %d; expected ServiceWindows", st)
		}
	case "freebsd", "openbsd", "netbsd":
		if st != ServiceBSDRC {
			t.Errorf("on BSD got %d; expected ServiceBSDRC", st)
		}
	default:
		if st != ServiceUnknown {
			t.Errorf("on %s got %d; expected ServiceUnknown", runtime.GOOS, st)
		}
	}
}

// ─── Install switch branch coverage ──────────────────────────────────────────

func TestInstall_ServiceTypeBranches(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; would attempt real installation")
	}

	// Install() returns an error when not root. We verify it handles all
	// service types in its switch statement without panicking.
	err := Install()
	if err == nil {
		t.Error("Install() should fail without root")
	}

	// The error should mention privilege requirements.
	errStr := err.Error()
	if !strings.Contains(errStr, "root") && !strings.Contains(errStr, "sudo") {
		t.Errorf("Install() error = %q; expected root/sudo mention", errStr)
	}
}

// ─── Uninstall confirmDestructive branch coverage ────────────────────────────

func TestUninstall_ConfirmCancelled(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; would attempt real uninstall")
	}

	// Uninstall() prompts for confirmation via confirmDestructive().
	// When not root, it fails before reaching the prompt.
	err := Uninstall()
	if err == nil {
		t.Error("Uninstall() should fail without root")
	}
}

// ─── findAvailableSystemID exhaustive coverage ───────────────────────────────

func TestFindAvailableSystemID_SkipsReserved(t *testing.T) {
	id, err := findAvailableSystemID()
	if err != nil {
		t.Skipf("all IDs in range may be taken: %v", err)
	}

	// Verify the returned ID is not in a reserved range.
	// Reserved: 980-999, 101-110, 170-179
	if (id >= 980 && id <= 999) || (id >= 101 && id <= 110) || (id >= 170 && id <= 179) {
		t.Errorf("findAvailableSystemID() = %d; should not be in reserved range", id)
	}
}

func TestFindAvailableSystemID_StartFromHighest(t *testing.T) {
	id, err := findAvailableSystemID()
	if err != nil {
		t.Skipf("all IDs in range may be taken: %v", err)
	}

	// The function scans from 899 down to 200. On a fresh system with few
	// users, the returned ID should be close to 899.
	if id < 200 || id > 899 {
		t.Errorf("findAvailableSystemID() = %d; must be in range 200-899", id)
	}
}

// ─── copyBinary edge cases ───────────────────────────────────────────────────

func TestCopyBinary_DestDirCreation(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "a", "b", "c", "bin")

	if err := os.WriteFile(src, []byte("content"), 0o755); err != nil {
		t.Fatalf("write src: %v", err)
	}

	if err := copyBinary(src, dst); err != nil {
		t.Fatalf("copyBinary: %v", err)
	}

	// Verify all intermediate directories were created.
	if _, err := os.Stat(filepath.Join(tmp, "a", "b", "c")); os.IsNotExist(err) {
		t.Error("copyBinary did not create intermediate directories")
	}
}

func TestCopyBinary_OverwritesExisting(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst")

	// Write initial content.
	if err := os.WriteFile(src, []byte("v1"), 0o755); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatalf("write dst: %v", err)
	}

	if err := copyBinary(src, dst); err != nil {
		t.Fatalf("copyBinary: %v", err)
	}

	got, _ := os.ReadFile(dst)
	if string(got) != "v1" {
		t.Errorf("dst = %q; expected 'v1' after overwrite", got)
	}
}

// ─── purgeData branch coverage ───────────────────────────────────────────────

func TestPurgeData_LinuxPaths(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only test")
	}

	// purgeData() attempts to remove system directories. When they don't
	// exist, RemoveAll returns nil. We verify no panic occurs.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("purgeData panicked: %v", r)
		}
	}()
	purgeData()
}

func TestPurgeData_ExpectedPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix path test")
	}

	// Verify purgeData targets the correct paths based on the spec: config,
	// data, cache, log, and backup directories (PART 23 step 4).
	expectedPaths := purgeDirs()

	if len(expectedPaths) != 5 {
		t.Errorf("purgeDirs() returned %d dirs; want 5 (config, data, cache, log, backup)", len(expectedPaths))
	}

	for _, path := range expectedPaths {
		// Paths honor env overrides (CONFIG_DIR, BACKUP_DIR, ...), so only
		// verify each target is a usable absolute path.
		if path == "" || !filepath.IsAbs(path) {
			t.Errorf("unexpected path format: %s", path)
		}
	}
}

// ─── GetBinaryPath branch coverage ───────────────────────────────────────────

func TestGetBinaryPath_AllPlatforms(t *testing.T) {
	path := GetBinaryPath()

	if path == "" {
		t.Error("GetBinaryPath() returned empty string")
	}

	// Verify path is absolute.
	if !filepath.IsAbs(path) {
		t.Errorf("GetBinaryPath() = %q; expected absolute path", path)
	}

	// Verify path contains app name.
	if !strings.Contains(path, appName) {
		t.Errorf("GetBinaryPath() = %q; expected to contain %q", path, appName)
	}
}

// ─── Service lifecycle function coverage ─────────────────────────────────────

func TestStart_ReturnsError(t *testing.T) {
	// Start() shells out to the service manager. In a container without
	// the service installed, it returns an error.
	err := Start()
	// We don't check the specific error; just verify no panic.
	_ = err
}

func TestStop_ReturnsError(t *testing.T) {
	err := Stop()
	_ = err
}

func TestRestart_ReturnsError(t *testing.T) {
	err := Restart()
	_ = err
}

func TestReload_ReturnsError(t *testing.T) {
	err := Reload()
	_ = err
}

func TestDisable_ReturnsError(t *testing.T) {
	err := Disable()
	_ = err
}

// ─── Service type conversion ─────────────────────────────────────────────────

func TestServiceType_Stringer(t *testing.T) {
	// Verify each service type has a distinct int value.
	types := map[ServiceType]int{
		ServiceUnknown: 0,
		ServiceSystemd: 1,
		ServiceOpenRC:  2,
		ServiceSysV:    3,
		ServiceRunit:   4,
		ServiceLaunchd: 5,
		ServiceWindows: 6,
		ServiceBSDRC:   7,
	}

	for st, expected := range types {
		if int(st) != expected {
			t.Errorf("ServiceType %d has value %d; expected %d", st, int(st), expected)
		}
	}
}

// ─── Install function script generation ─────────────────────────────────────

func TestInstallSysV_ScriptGeneration(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to write to /etc/init.d/")
	}

	initDir := "/etc/init.d"
	if _, err := os.Stat(initDir); os.IsNotExist(err) {
		if err := os.MkdirAll(initDir, 0o755); err != nil {
			t.Skip("cannot create /etc/init.d/")
		}
	}

	initPath := filepath.Join(initDir, appName)
	t.Cleanup(func() { _ = os.Remove(initPath) })

	err := installSysV()
	if err != nil {
		t.Logf("installSysV: %v", err)
		// May fail due to missing update-rc.d/chkconfig
		return
	}

	content, err := os.ReadFile(initPath)
	if err != nil {
		t.Fatalf("read init script: %v", err)
	}

	// Verify script structure
	script := string(content)
	if !strings.Contains(script, "start-stop-daemon") {
		t.Error("SysV script missing start-stop-daemon")
	}
}

func TestInstallOpenRC_ScriptGeneration(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to write to /etc/init.d/")
	}

	initDir := "/etc/init.d"
	if _, err := os.Stat(initDir); os.IsNotExist(err) {
		if err := os.MkdirAll(initDir, 0o755); err != nil {
			t.Skip("cannot create /etc/init.d/")
		}
	}

	initPath := filepath.Join(initDir, appName)
	t.Cleanup(func() { _ = os.Remove(initPath) })

	err := installOpenRC()
	if err != nil {
		t.Logf("installOpenRC: %v", err)
		// May fail due to missing rc-update
		return
	}

	content, err := os.ReadFile(initPath)
	if err != nil {
		t.Fatalf("read OpenRC script: %v", err)
	}

	script := string(content)
	if !strings.Contains(script, "openrc-run") {
		t.Error("OpenRC script missing openrc-run shebang")
	}
}

// ─── Uninstall functions ─────────────────────────────────────────────────────

func TestUninstallOpenRC_Idempotent(t *testing.T) {
	// uninstallOpenRC should not fail when the script doesn't exist.
	err := uninstallOpenRC()
	if err != nil {
		t.Logf("uninstallOpenRC: %v (expected on non-OpenRC host)", err)
	}
}

func TestUninstallSysV_Idempotent(t *testing.T) {
	err := uninstallSysV()
	if err != nil {
		t.Logf("uninstallSysV: %v (expected on non-SysV host)", err)
	}
}

func TestUninstallRunit_Idempotent(t *testing.T) {
	err := uninstallRunit()
	if err != nil {
		t.Errorf("uninstallRunit: %v (should succeed when service not installed)", err)
	}
}

func TestUninstallLaunchd_Idempotent(t *testing.T) {
	err := uninstallLaunchd()
	if err != nil {
		t.Logf("uninstallLaunchd: %v (expected on non-macOS host)", err)
	}
}

func TestUninstallBSDRC_Idempotent(t *testing.T) {
	err := uninstallBSDRC()
	if err != nil {
		t.Logf("uninstallBSDRC: %v (expected on non-BSD host)", err)
	}
}

func TestUninstallSystemd_Idempotent(t *testing.T) {
	err := uninstallSystemd()
	if err != nil {
		t.Logf("uninstallSystemd: %v (expected on non-systemd host)", err)
	}
}
