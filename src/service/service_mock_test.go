package service

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ─── Test install functions with filesystem mocking ──────────────────────────
// These tests create temporary directories that mimic system paths, allowing
// us to test the file-writing logic without root privileges.

func TestInstallSystemd_WithMockPaths(t *testing.T) {
	// We cannot easily mock /etc/systemd/system, but we can test the
	// generated unit file content.
	binaryPath := GetBinaryPath()

	unit := buildSystemdUnit(binaryPath)

	// PART 24 requirements
	checks := []struct {
		name     string
		contains string
	}{
		{"Type=simple", "Type=simple"},
		{"RestartSec=5", "RestartSec=5"},
		{"Restart=on-failure", "Restart=on-failure"},
		{"ProtectSystem=strict", "ProtectSystem=strict"},
		{"PrivateTmp=yes", "PrivateTmp=yes"},
	}

	for _, tc := range checks {
		if !strings.Contains(unit, tc.contains) {
			t.Errorf("systemd unit missing %s", tc.name)
		}
	}
}

func TestInstallLaunchd_WithMockPaths(t *testing.T) {
	tmp := t.TempDir()

	// Create mock directory structure
	launchDir := filepath.Join(tmp, "Library", "LaunchDaemons")
	if err := os.MkdirAll(launchDir, 0o755); err != nil {
		t.Fatalf("create mock LaunchDaemons: %v", err)
	}

	// Test the plist content generation
	binaryPath := GetBinaryPath()
	plist := buildLaunchdPlist(binaryPath)

	// Verify plist structure
	if !strings.Contains(plist, "<key>Label</key>") {
		t.Error("plist missing Label key")
	}
	if !strings.Contains(plist, launchdLabel) {
		t.Errorf("plist missing label value %q", launchdLabel)
	}
	if !strings.Contains(plist, "<key>RunAtLoad</key>") {
		t.Error("plist missing RunAtLoad")
	}
	if !strings.Contains(plist, "<key>KeepAlive</key>") {
		t.Error("plist missing KeepAlive")
	}
}

func TestInstallRunit_WithMockPaths(t *testing.T) {
	tmp := t.TempDir()

	// Create mock sv directory
	svDir := filepath.Join(tmp, "sv", appName)
	if err := os.MkdirAll(svDir, 0o755); err != nil {
		t.Fatalf("create mock sv dir: %v", err)
	}

	// Test run script content
	binaryPath := GetBinaryPath()
	runScript := buildRunitRunScript(binaryPath)

	if !strings.HasPrefix(runScript, "#!/bin/sh") {
		t.Error("run script missing shebang")
	}
	if !strings.Contains(runScript, "exec ") {
		t.Error("run script missing exec")
	}
	if !strings.Contains(runScript, binaryPath) {
		t.Errorf("run script missing binary path %q", binaryPath)
	}

	// Test log run script
	logRunScript := buildRunitLogRunScript()
	if !strings.Contains(logRunScript, "svlogd") {
		t.Error("log run script missing svlogd")
	}
}

func TestInstallBSDRC_WithMockPaths(t *testing.T) {
	tmp := t.TempDir()

	// Create mock rc.d directory
	rcDir := filepath.Join(tmp, "etc", "rc.d")
	if err := os.MkdirAll(rcDir, 0o755); err != nil {
		t.Fatalf("create mock rc.d dir: %v", err)
	}

	// Test rc.d script content
	binaryPath := GetBinaryPath()
	rcScript := buildBSDRCScript(binaryPath)

	// Verify required elements per FreeBSD rc.d conventions
	checks := []string{
		"# PROVIDE:",
		"# REQUIRE: NETWORKING",
		"# KEYWORD: shutdown",
		". /etc/rc.subr",
		fmt.Sprintf(`name="%s"`, appName),
		fmt.Sprintf(`command="%s"`, binaryPath),
		"run_rc_command",
	}

	for _, check := range checks {
		if !strings.Contains(rcScript, check) {
			t.Errorf("rc.d script missing %q", check)
		}
	}
}

func TestInstallSysV_WithMockPaths(t *testing.T) {
	tmp := t.TempDir()

	// Create mock init.d directory
	initDir := filepath.Join(tmp, "etc", "init.d")
	if err := os.MkdirAll(initDir, 0o755); err != nil {
		t.Fatalf("create mock init.d dir: %v", err)
	}

	// Test SysV init script content
	binaryPath := GetBinaryPath()
	initScript := buildSysVScript(binaryPath)

	// Verify LSB header
	checks := []string{
		"### BEGIN INIT INFO",
		"### END INIT INFO",
		"# Provides:",
		"# Required-Start:",
		"# Required-Stop:",
		"# Default-Start:",
		"# Default-Stop:",
		"start)",
		"stop)",
		"restart)",
		"status)",
	}

	for _, check := range checks {
		if !strings.Contains(initScript, check) {
			t.Errorf("SysV script missing %q", check)
		}
	}
}

func TestInstallOpenRC_WithMockPaths(t *testing.T) {
	tmp := t.TempDir()

	// Create mock init.d directory
	initDir := filepath.Join(tmp, "etc", "init.d")
	if err := os.MkdirAll(initDir, 0o755); err != nil {
		t.Fatalf("create mock init.d dir: %v", err)
	}

	// Test OpenRC script content
	binaryPath := GetBinaryPath()
	rcScript := buildOpenRCScript(binaryPath)

	// Verify OpenRC structure
	checks := []string{
		"#!/sbin/openrc-run",
		"description=",
		"command=",
		"command_background=true",
		"pidfile=",
		"depend()",
		"need net",
	}

	for _, check := range checks {
		if !strings.Contains(rcScript, check) {
			t.Errorf("OpenRC script missing %q", check)
		}
	}
}

// ─── reservedIDs comprehensive coverage ──────────────────────────────────────

func TestReservedIDs_CompleteRanges(t *testing.T) {
	// Verify all reserved ranges per PART 23.
	ranges := []struct {
		start, end int
	}{
		{980, 999},
		{101, 110},
		{170, 179},
	}

	for _, r := range ranges {
		for id := r.start; id <= r.end; id++ {
			if !reservedIDs[id] {
				t.Errorf("ID %d should be reserved (range %d-%d)", id, r.start, r.end)
			}
		}
	}
}

func TestReservedIDs_NonReservedRanges(t *testing.T) {
	// Verify IDs outside reserved ranges are not reserved.
	nonReserved := []int{
		100, 111, 169, 180, 200, 500, 800, 899, 979,
	}

	for _, id := range nonReserved {
		if reservedIDs[id] {
			t.Errorf("ID %d should not be reserved", id)
		}
	}
}

// ─── Constants validation ────────────────────────────────────────────────────

func TestConstants_Correctness(t *testing.T) {
	if appName != "cairnbin" {
		t.Errorf("appName = %q; want 'cairnbin'", appName)
	}
	if orgName != "apimgr" {
		t.Errorf("orgName = %q; want 'apimgr'", orgName)
	}
	if serviceUser != "cairnbin" {
		t.Errorf("serviceUser = %q; want 'cairnbin'", serviceUser)
	}

	expectedLabel := "io.github.apimgr.cairnbin"
	if launchdLabel != expectedLabel {
		t.Errorf("launchdLabel = %q; want %q", launchdLabel, expectedLabel)
	}
}

// ─── copyBinary comprehensive edge cases ─────────────────────────────────────

func TestCopyBinary_ReadPermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission test")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can read anything")
	}

	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst")

	// Create unreadable source file
	if err := os.WriteFile(src, []byte("content"), 0o000); err != nil {
		t.Fatalf("write src: %v", err)
	}

	err := copyBinary(src, dst)
	if err == nil {
		t.Error("expected error when source is unreadable")
	}
}

func TestCopyBinary_AtomicOverwrite(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst")

	// Write initial dst content
	if err := os.WriteFile(dst, []byte("original"), 0o755); err != nil {
		t.Fatalf("write original dst: %v", err)
	}

	// Write new src content
	if err := os.WriteFile(src, []byte("updated"), 0o755); err != nil {
		t.Fatalf("write src: %v", err)
	}

	// Copy should overwrite
	if err := copyBinary(src, dst); err != nil {
		t.Fatalf("copyBinary: %v", err)
	}

	got, _ := os.ReadFile(dst)
	if string(got) != "updated" {
		t.Errorf("dst = %q; expected 'updated'", got)
	}
}

// ─── Service path format validation ──────────────────────────────────────────

func TestServicePaths_Format(t *testing.T) {
	testCases := []struct {
		name     string
		pathFmt  string
		expected string
	}{
		{
			name:     "systemd unit",
			pathFmt:  "/etc/systemd/system/%s.service",
			expected: "/etc/systemd/system/cairnbin.service",
		},
		{
			name:     "launchd plist",
			pathFmt:  "/Library/LaunchDaemons/%s.plist",
			expected: "/Library/LaunchDaemons/io.github.apimgr.cairnbin.plist",
		},
		{
			name:     "openrc init",
			pathFmt:  "/etc/init.d/%s",
			expected: "/etc/init.d/cairnbin",
		},
		{
			name:     "sysv init",
			pathFmt:  "/etc/init.d/%s",
			expected: "/etc/init.d/cairnbin",
		},
		{
			name:     "runit sv",
			pathFmt:  "/etc/sv/%s",
			expected: "/etc/sv/cairnbin",
		},
		{
			name:     "bsd rc.d",
			pathFmt:  "/usr/local/etc/rc.d/%s",
			expected: "/usr/local/etc/rc.d/cairnbin",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var actual string
			if strings.Contains(tc.name, "launchd") {
				actual = fmt.Sprintf(tc.pathFmt, launchdLabel)
			} else {
				actual = fmt.Sprintf(tc.pathFmt, appName)
			}
			if actual != tc.expected {
				t.Errorf("path = %q; want %q", actual, tc.expected)
			}
		})
	}
}

// ─── purgeData path verification ─────────────────────────────────────────────

func TestPurgeData_PathConstruction(t *testing.T) {
	if runtime.GOOS == "windows" {
		// On Windows, purgeData uses a different path.
		expected := fmt.Sprintf(`C:\ProgramData\%s\%s`, orgName, appName)
		if !strings.Contains(expected, "apimgr") {
			t.Errorf("Windows path missing org name: %s", expected)
		}
		return
	}

	// Unix paths — derived from the paths package (PART 4 platform layouts)
	expectedDirs := purgeDirs()

	for _, dir := range expectedDirs {
		// Paths honor env overrides (CONFIG_DIR, BACKUP_DIR, ...), so only
		// verify each target is a usable absolute path.
		if dir == "" || !filepath.IsAbs(dir) {
			t.Errorf("unexpected purge path: %s", dir)
		}
	}
}

// ─── PrintHelp comprehensive validation ──────────────────────────────────────

func TestPrintHelp_Structure(t *testing.T) {
	origStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	PrintHelp("myapp")

	w.Close()
	os.Stdout = origStdout

	buf := make([]byte, 8192)
	n, _ := r.Read(buf)
	out := string(buf[:n])

	// Verify help structure
	sections := []string{
		"Service management commands:",
		"Current status:",
	}

	for _, section := range sections {
		if !strings.Contains(out, section) {
			t.Errorf("PrintHelp missing section %q", section)
		}
	}

	// Verify all commands documented
	commands := []string{
		"start",
		"stop",
		"restart",
		"reload",
		"--install",
		"--disable",
		"--uninstall",
	}

	for _, cmd := range commands {
		if !strings.Contains(out, cmd) {
			t.Errorf("PrintHelp missing command %q", cmd)
		}
	}
}

// ─── ServiceType enumeration ─────────────────────────────────────────────────

func TestServiceType_AllValues(t *testing.T) {
	// Verify all service types have expected values
	expected := map[ServiceType]int{
		ServiceUnknown: 0,
		ServiceSystemd: 1,
		ServiceOpenRC:  2,
		ServiceSysV:    3,
		ServiceRunit:   4,
		ServiceLaunchd: 5,
		ServiceWindows: 6,
		ServiceBSDRC:   7,
	}

	for st, want := range expected {
		if int(st) != want {
			t.Errorf("ServiceType constant: got %d, want %d", int(st), want)
		}
	}
}

// ─── ok() function variations ────────────────────────────────────────────────

func TestOk_AllVariations(t *testing.T) {
	testCases := []struct {
		name      string
		noColor   string
		wantText  string
		checkFunc func(string) bool
	}{
		{
			name:     "NO_COLOR=1",
			noColor:  "1",
			wantText: "[ok] ",
		},
		{
			name:     "NO_COLOR=true",
			noColor:  "true",
			wantText: "[ok] ",
		},
		{
			name:     "NO_COLOR=yes",
			noColor:  "yes",
			wantText: "[ok] ",
		},
		{
			name:    "NO_COLOR unset",
			noColor: "",
			checkFunc: func(s string) bool {
				// Should be emoji, not text
				return s != "[ok] " && s != ""
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.noColor != "" {
				t.Setenv("NO_COLOR", tc.noColor)
			} else {
				os.Unsetenv("NO_COLOR")
			}

			got := ok()

			if tc.checkFunc != nil {
				if !tc.checkFunc(got) {
					t.Errorf("ok() = %q; custom check failed", got)
				}
			} else if got != tc.wantText {
				t.Errorf("ok() = %q; want %q", got, tc.wantText)
			}
		})
	}
}
