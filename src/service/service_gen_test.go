package service

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ─── Systemd Unit File Content Verification ──────────────────────────────────
// Tests that the generated systemd unit file contains all required directives
// as specified in PART 24: Type=simple, RestartSec=5, Restart=on-failure,
// ProtectSystem=strict, ProtectHome=yes, PrivateTmp=yes.

func TestSystemdUnitContent_RequiredDirectives(t *testing.T) {
	serviceContent := buildSystemdUnit(GetBinaryPath())

	requiredDirectives := []string{
		"Description=" + appName + " service",
		"Type=simple",
		"RestartSec=5",
		"Restart=on-failure",
		"StandardOutput=journal",
		"StandardError=journal",
		"ProtectSystem=strict",
		"ProtectHome=yes",
		"PrivateTmp=yes",
		"WantedBy=multi-user.target",
	}
	for _, directive := range requiredDirectives {
		if !strings.Contains(serviceContent, directive) {
			t.Errorf("systemd unit missing required directive %q", directive)
		}
	}

	for _, extra := range []string{"NoNewPrivileges", "RuntimeDirectory", "ReadWritePaths=/etc\n", "User="} {
		if strings.Contains(serviceContent, extra) {
			t.Errorf("systemd unit has directive outside PART 24: %q", extra)
		}
	}
}

func TestSystemdUnitContent_HasUnitSection(t *testing.T) {
	binaryPath := GetBinaryPath()
	serviceContent := buildSystemdUnit(binaryPath)

	if !strings.Contains(serviceContent, "[Unit]") {
		t.Error("systemd unit missing [Unit] section")
	}
	if !strings.Contains(serviceContent, "[Service]") {
		t.Error("systemd unit missing [Service] section")
	}
	if !strings.Contains(serviceContent, "[Install]") {
		t.Error("systemd unit missing [Install] section")
	}
}

func TestSystemdUnitContent_HasDocumentation(t *testing.T) {
	binaryPath := GetBinaryPath()
	serviceContent := buildSystemdUnit(binaryPath)

	if !strings.Contains(serviceContent, "Documentation=") {
		t.Error("systemd unit missing Documentation directive")
	}
	if !strings.Contains(serviceContent, "apimgr.github.io") {
		t.Error("systemd unit Documentation does not reference project docs site")
	}
}

func TestSystemdUnitContent_HasNetworkOrdering(t *testing.T) {
	binaryPath := GetBinaryPath()
	serviceContent := buildSystemdUnit(binaryPath)

	if !strings.Contains(serviceContent, "After=network-online.target") {
		t.Error("systemd unit missing After=network-online.target")
	}
	if !strings.Contains(serviceContent, "Wants=network-online.target") {
		t.Error("systemd unit missing Wants=network-online.target")
	}
}

func TestSystemdUnitContent_HasReadWritePaths(t *testing.T) {
	binaryPath := GetBinaryPath()
	serviceContent := buildSystemdUnit(binaryPath)

	expectedPaths := []string{
		fmt.Sprintf("/etc/%s/%s", orgName, appName),
		fmt.Sprintf("/var/lib/%s/%s", orgName, appName),
		fmt.Sprintf("/var/cache/%s/%s", orgName, appName),
		fmt.Sprintf("/var/log/%s/%s", orgName, appName),
	}

	for _, path := range expectedPaths {
		if !strings.Contains(serviceContent, "ReadWritePaths="+path) {
			t.Errorf("systemd unit missing ReadWritePaths=%s", path)
		}
	}
}

func TestEnsureSystemdWritablePathsCreatesDirectories(t *testing.T) {
	root := t.TempDir()
	paths := []string{
		filepath.Join(root, "etc", "pastebin"),
		filepath.Join(root, "lib", "pastebin"),
	}
	if err := ensureSystemdWritablePaths(paths); err != nil {
		t.Fatalf("ensure systemd writable paths: %v", err)
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("stat %s: %v", path, err)
		} else if !info.IsDir() {
			t.Errorf("%s is not a directory", path)
		}
	}
}

// ─── Launchd Plist Content Verification ──────────────────────────────────────

func TestLaunchdPlistContent_RequiredKeys(t *testing.T) {
	binaryPath := GetBinaryPath()
	plistContent := buildLaunchdPlist(binaryPath)

	requiredKeys := []string{
		"<key>Label</key>",
		"<key>ProgramArguments</key>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"<key>StandardOutPath</key>",
		"<key>StandardErrorPath</key>",
	}

	for _, key := range requiredKeys {
		if !strings.Contains(plistContent, key) {
			t.Errorf("launchd plist missing required key %q", key)
		}
	}
}

func TestLaunchdPlistContent_HasLabel(t *testing.T) {
	binaryPath := GetBinaryPath()
	plistContent := buildLaunchdPlist(binaryPath)

	expectedLabel := fmt.Sprintf("<string>%s</string>", launchdLabel)
	if !strings.Contains(plistContent, expectedLabel) {
		t.Errorf("launchd plist missing label %q", launchdLabel)
	}
}

func TestLaunchdPlistContent_HasBinaryPath(t *testing.T) {
	binaryPath := GetBinaryPath()
	plistContent := buildLaunchdPlist(binaryPath)

	expectedBinary := fmt.Sprintf("<string>%s</string>", binaryPath)
	if !strings.Contains(plistContent, expectedBinary) {
		t.Errorf("launchd plist missing binary path %q", binaryPath)
	}
}

func TestLaunchdPlistContent_HasLogPaths(t *testing.T) {
	binaryPath := GetBinaryPath()
	plistContent := buildLaunchdPlist(binaryPath)

	expectedStdout := fmt.Sprintf("/var/log/%s/%s/stdout.log", orgName, appName)
	expectedStderr := fmt.Sprintf("/var/log/%s/%s/stderr.log", orgName, appName)

	if !strings.Contains(plistContent, expectedStdout) {
		t.Errorf("launchd plist missing stdout log path %q", expectedStdout)
	}
	if !strings.Contains(plistContent, expectedStderr) {
		t.Errorf("launchd plist missing stderr log path %q", expectedStderr)
	}
}

func TestLaunchdPlistContent_HasXMLHeader(t *testing.T) {
	binaryPath := GetBinaryPath()
	plistContent := buildLaunchdPlist(binaryPath)

	if !strings.HasPrefix(plistContent, "<?xml version=") {
		t.Error("launchd plist missing XML declaration")
	}
	if !strings.Contains(plistContent, "<!DOCTYPE plist") {
		t.Error("launchd plist missing DOCTYPE")
	}
}

// ─── OpenRC Init Script Content Verification ─────────────────────────────────

func TestOpenRCScriptContent_RequiredElements(t *testing.T) {
	binaryPath := GetBinaryPath()
	rcContent := buildOpenRCScript(binaryPath)

	requiredElements := []string{
		"#!/sbin/openrc-run",
		"name=",
		"description=",
		"command=",
		"command_user=",
		"command_background=true",
		"pidfile=",
		"output_log=",
		"error_log=",
		"depend()",
		"need net",
		"use dns logger",
		"start_pre()",
		"checkpath",
	}

	for _, elem := range requiredElements {
		if !strings.Contains(rcContent, elem) {
			t.Errorf("OpenRC script missing required element %q", elem)
		}
	}
}

func TestOpenRCScriptContent_HasBinaryPath(t *testing.T) {
	binaryPath := GetBinaryPath()
	rcContent := buildOpenRCScript(binaryPath)

	expectedCommand := fmt.Sprintf(`command="%s"`, binaryPath)
	if !strings.Contains(rcContent, expectedCommand) {
		t.Errorf("OpenRC script missing command=%q", binaryPath)
	}
}

func TestOpenRCScriptContent_HasPidfile(t *testing.T) {
	binaryPath := GetBinaryPath()
	rcContent := buildOpenRCScript(binaryPath)

	expectedPidfile := fmt.Sprintf(`pidfile="/var/run/%s/%s.pid"`, orgName, appName)
	if !strings.Contains(rcContent, expectedPidfile) {
		t.Errorf("OpenRC script missing pidfile directive")
	}
}

func TestOpenRCScriptContent_HasCommandUser(t *testing.T) {
	binaryPath := GetBinaryPath()
	rcContent := buildOpenRCScript(binaryPath)

	expectedUser := fmt.Sprintf(`command_user="%s:%s"`, serviceUser, serviceUser)
	if !strings.Contains(rcContent, expectedUser) {
		t.Errorf("OpenRC script missing command_user directive")
	}
}

func TestOpenRCScriptContent_HasCheckpathCalls(t *testing.T) {
	binaryPath := GetBinaryPath()
	rcContent := buildOpenRCScript(binaryPath)

	expectedRunCheck := fmt.Sprintf("checkpath -d -m 0755 -o %s:%s /var/run/%s", serviceUser, serviceUser, orgName)
	if !strings.Contains(rcContent, expectedRunCheck) {
		t.Errorf("OpenRC script missing start_pre checkpath for /var/run/%s", orgName)
	}

	expectedLogCheck := fmt.Sprintf("checkpath -d -m 0755 -o %s:%s /var/log/%s/%s", serviceUser, serviceUser, orgName, appName)
	if !strings.Contains(rcContent, expectedLogCheck) {
		t.Errorf("OpenRC script missing start_pre checkpath for /var/log/%s/%s", orgName, appName)
	}
}

// ─── SysV Init Script Content Verification ───────────────────────────────────

func TestSysVScriptContent_RequiredElements(t *testing.T) {
	binaryPath := GetBinaryPath()
	initContent := buildSysVScript(binaryPath)

	requiredElements := []string{
		"#!/bin/sh",
		"### BEGIN INIT INFO",
		"### END INIT INFO",
		"# Provides:",
		"# Required-Start:",
		"# Default-Start:",
		"DAEMON_USER=",
		"LOGFILE=",
		"--chuid",
		"start)",
		"stop)",
		"restart)",
		"status)",
	}

	for _, elem := range requiredElements {
		if !strings.Contains(initContent, elem) {
			t.Errorf("SysV script missing required element %q", elem)
		}
	}
}

func TestSysVScriptContent_NoUndocumentedReloadCase(t *testing.T) {
	binaryPath := GetBinaryPath()
	initContent := buildSysVScript(binaryPath)

	// AI.md's SysVinit spec (PART 24) defines only start|stop|restart|status —
	// no reload case. An extra, undocumented reload case is a spec violation.
	if strings.Contains(initContent, "reload)") {
		t.Error("SysV script contains an undocumented reload case not present in AI.md spec")
	}
	if !strings.Contains(initContent, "Usage: $0 {start|stop|restart|status}") {
		t.Error("SysV script usage line does not match AI.md spec (start|stop|restart|status)")
	}
}

func TestSysVScriptContent_HasBinaryPath(t *testing.T) {
	binaryPath := GetBinaryPath()
	initContent := buildSysVScript(binaryPath)

	expectedDaemon := fmt.Sprintf("DAEMON=%s", binaryPath)
	if !strings.Contains(initContent, expectedDaemon) {
		t.Errorf("SysV script missing DAEMON=%q", binaryPath)
	}
}

func TestSysVScriptContent_HasPidfile(t *testing.T) {
	binaryPath := GetBinaryPath()
	initContent := buildSysVScript(binaryPath)

	expectedPidfile := fmt.Sprintf("PIDFILE=/var/run/%s/%s.pid", orgName, appName)
	if !strings.Contains(initContent, expectedPidfile) {
		t.Errorf("SysV script missing PIDFILE directive")
	}
}

func TestSysVScriptContent_HasDaemonUser(t *testing.T) {
	binaryPath := GetBinaryPath()
	initContent := buildSysVScript(binaryPath)

	expectedUser := fmt.Sprintf("DAEMON_USER=%s", serviceUser)
	if !strings.Contains(initContent, expectedUser) {
		t.Errorf("SysV script missing DAEMON_USER directive")
	}
}

func TestSysVScriptContent_HasRemoteFsDependency(t *testing.T) {
	binaryPath := GetBinaryPath()
	initContent := buildSysVScript(binaryPath)

	if !strings.Contains(initContent, "# Required-Start:    $network $remote_fs $syslog") {
		t.Error("SysV script missing $remote_fs in Required-Start")
	}
	if !strings.Contains(initContent, "# Required-Stop:     $network $remote_fs $syslog") {
		t.Error("SysV script missing $remote_fs in Required-Stop")
	}
}

func TestSysVScriptContent_HasRunlevels(t *testing.T) {
	binaryPath := GetBinaryPath()
	initContent := buildSysVScript(binaryPath)

	if !strings.Contains(initContent, "# Default-Start:     2 3 4 5") {
		t.Error("SysV script missing Default-Start runlevels")
	}
	if !strings.Contains(initContent, "# Default-Stop:      0 1 6") {
		t.Error("SysV script missing Default-Stop runlevels")
	}
}

// ─── BSD rc.d Script Content Verification ────────────────────────────────────

func TestBSDRCScriptContent_RequiredElements(t *testing.T) {
	binaryPath := GetBinaryPath()
	rcContent := buildBSDRCScript(binaryPath)

	requiredElements := []string{
		"#!/bin/sh",
		"# PROVIDE:",
		"# REQUIRE: NETWORKING",
		"# KEYWORD: shutdown",
		". /etc/rc.subr",
		"name=",
		"rcvar=",
		"command=",
		"load_rc_config",
		"run_rc_command",
	}

	for _, elem := range requiredElements {
		if !strings.Contains(rcContent, elem) {
			t.Errorf("BSD rc.d script missing required element %q", elem)
		}
	}
}

func TestBSDRCScriptContent_HasBinaryPath(t *testing.T) {
	binaryPath := GetBinaryPath()
	rcContent := buildBSDRCScript(binaryPath)

	expectedCommand := fmt.Sprintf(`command="%s"`, binaryPath)
	if !strings.Contains(rcContent, expectedCommand) {
		t.Errorf("BSD rc.d script missing command=%q", binaryPath)
	}
}

func TestBSDRCScriptContent_HasEnableVar(t *testing.T) {
	binaryPath := GetBinaryPath()
	rcContent := buildBSDRCScript(binaryPath)

	if want := fmt.Sprintf(`rcvar="%s_enable"`, appName); !strings.Contains(rcContent, want) {
		t.Errorf("BSD rc.d script missing %s", want)
	}
	if strings.Contains(rcContent, "pidfile=") {
		t.Error("BSD rc.d script has pidfile outside PART 24")
	}
}

// ─── Runit Run Script Content Verification ───────────────────────────────────

func TestRunitRunScriptContent_RequiredElements(t *testing.T) {
	binaryPath := GetBinaryPath()
	runScript := buildRunitRunScript(binaryPath)

	if !strings.HasPrefix(runScript, "#!/bin/sh") {
		t.Error("runit run script missing shebang")
	}
	if !strings.Contains(runScript, "exec ") {
		t.Error("runit run script missing exec directive")
	}
	if !strings.Contains(runScript, binaryPath) {
		t.Errorf("runit run script missing binary path %q", binaryPath)
	}
}

func TestRunitLogRunScriptContent_HasSvlogd(t *testing.T) {
	logRunScript := buildRunitLogRunScript()

	if !strings.Contains(logRunScript, "svlogd") {
		t.Error("runit log run script missing svlogd")
	}
	if !strings.Contains(logRunScript, "exec ") {
		t.Error("runit log run script missing exec directive")
	}
}

// ─── reserved IDs ────────────────────────────────────────────────────────────

func TestReservedIDs_ContainsExpectedRanges(t *testing.T) {
	// Per PART 23: Reserve ranges 980-999, 101-110, 170-179.
	testCases := []struct {
		id       int
		reserved bool
	}{
		{980, true},
		{999, true},
		{990, true},
		{101, true},
		{110, true},
		{105, true},
		{170, true},
		{179, true},
		{175, true},
		{200, false},
		{500, false},
		{899, false},
		{100, false},
		{111, false},
		{169, false},
		{180, false},
		{979, false},
	}

	for _, tc := range testCases {
		got := reservedIDs[tc.id]
		if got != tc.reserved {
			t.Errorf("reservedIDs[%d] = %v; want %v", tc.id, got, tc.reserved)
		}
	}
}

// ─── findAvailableSystemID edge cases ────────────────────────────────────────

func TestFindAvailableSystemID_RangeCheck(t *testing.T) {
	id, err := findAvailableSystemID()
	if err != nil {
		t.Skipf("findAvailableSystemID: %v (all IDs may be taken)", err)
	}
	if id < 200 || id > 899 {
		t.Errorf("findAvailableSystemID() = %d; must be in range 200-899", id)
	}
}

func TestFindAvailableSystemID_NotReserved(t *testing.T) {
	id, err := findAvailableSystemID()
	if err != nil {
		t.Skipf("findAvailableSystemID: %v", err)
	}
	if reservedIDs[id] {
		t.Errorf("findAvailableSystemID() = %d; should not be a reserved ID", id)
	}
}

// ─── ServiceType String Values ───────────────────────────────────────────────

func TestServiceType_EnumValuesAreSequential(t *testing.T) {
	expectedOrder := []ServiceType{
		ServiceUnknown,
		ServiceSystemd,
		ServiceOpenRC,
		ServiceSysV,
		ServiceRunit,
		ServiceLaunchd,
		ServiceWindows,
		ServiceBSDRC,
	}

	for i, st := range expectedOrder {
		if int(st) != i {
			t.Errorf("ServiceType enum at index %d = %d; expected sequential value", i, st)
		}
	}
}

// ─── GetBinaryPath platform variations ───────────────────────────────────────

func TestGetBinaryPath_WindowsFormat(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only test")
	}
	path := GetBinaryPath()
	if !strings.HasPrefix(path, `C:\Program Files\`) {
		t.Errorf("GetBinaryPath() on Windows = %q; expected C:\\Program Files\\ prefix", path)
	}
	if !strings.HasSuffix(path, ".exe") {
		t.Errorf("GetBinaryPath() on Windows = %q; expected .exe suffix", path)
	}
}

func TestGetBinaryPath_UnixFormat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix-only test")
	}
	path := GetBinaryPath()
	expected := fmt.Sprintf("/usr/local/bin/%s", appName)
	if path != expected {
		t.Errorf("GetBinaryPath() = %q; want %q", path, expected)
	}
}

// ─── copyBinary with write failure ───────────────────────────────────────────

func TestCopyBinary_FailsOnUnwritableDestDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission test")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere; skipping permission test")
	}

	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	unwritableDir := filepath.Join(tmp, "locked")

	if err := os.WriteFile(src, []byte("content"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := os.MkdirAll(unwritableDir, 0o555); err != nil {
		t.Fatalf("create locked dir: %v", err)
	}

	dst := filepath.Join(unwritableDir, "subdir", "binary")
	err := copyBinary(src, dst)
	if err == nil {
		t.Error("expected error when destination directory is not writable")
	}
}

// ─── purgeData with temp directories ─────────────────────────────────────────

func TestPurgeData_WindowsBranch(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only test")
	}
	// On Windows, purgeData removes C:\ProgramData\{orgName}\{appName}.
	// We cannot test the actual path removal without root; just verify no panic.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("purgeData panicked on Windows: %v", r)
		}
	}()
	purgeData()
}
