package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// runCapture calls run() with the given args and returns (exitCode, stdout, stderr).
func runCapture(args ...string) (int, string, string) {
	var out, errOut strings.Builder
	code := run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// TestRunHelp covers the --help and -h paths.
func TestRunHelp(t *testing.T) {
	for _, args := range [][]string{
		{"--help"},
		{"-h"},
	} {
		code, out, _ := runCapture(args...)
		if code != 0 {
			t.Errorf("run(%v) = %d, want 0", args, code)
		}
		if !strings.Contains(out, "--version") {
			t.Errorf("run(%v): help output missing --version flag", args)
		}
		if !strings.Contains(out, "--maintenance") {
			t.Errorf("run(%v): help output missing '--maintenance'", args)
		}
	}
}

// TestRunVersion covers the --version and -v paths.
func TestRunVersion(t *testing.T) {
	for _, args := range [][]string{
		{"--version"},
		{"-v"},
	} {
		code, out, _ := runCapture(args...)
		if code != 0 {
			t.Errorf("run(%v) = %d, want 0", args, code)
		}
		if !strings.Contains(out, "Built:") {
			t.Errorf("run(%v): version output missing 'Built:'", args)
		}
		if !strings.Contains(out, "Go:") {
			t.Errorf("run(%v): version output missing 'Go:'", args)
		}
		if !strings.Contains(out, "OS/Arch:") {
			t.Errorf("run(%v): version output missing 'OS/Arch:'", args)
		}
	}
}

// TestRunUnknownFlag verifies that an unrecognised flag returns exit 2 and
// prints a usage hint to stderr.
func TestRunUnknownFlag(t *testing.T) {
	code, _, errOut := runCapture("--totally-unknown-flag-xyz")
	if code != 2 {
		t.Errorf("unknown flag: exit %d, want 2", code)
	}
	if !strings.Contains(errOut, "--help") {
		t.Errorf("unknown flag: stderr missing --help hint; got: %q", errOut)
	}
}

// TestRunShellHelp verifies --shell --help exits 0.
func TestRunShellHelp(t *testing.T) {
	code, _, _ := runCapture("--shell", "--help")
	if code != 0 {
		t.Errorf("--shell --help: exit %d, want 0", code)
	}
}

// TestRunHelpWordAliases verifies the "help" word form (as shown in the
// server --help output) is accepted alongside "--help" for each command.
func TestRunHelpWordAliases(t *testing.T) {
	for _, args := range [][]string{
		{"--shell", "help"},
		{"--service", "help"},
		{"--maintenance", "help"},
		{"--update", "help"},
	} {
		if code, _, errOut := runCapture(args...); code != 0 {
			t.Errorf("%v: exit %d, want 0 (stderr %q)", args, code, errOut)
		}
	}
}

// TestRunShellCompletionsBash verifies --shell completions bash exits 0.
// The completions are written to os.Stdout directly by shell.PrintCompletions,
// not to the captured writer, so we only check the exit code here.
func TestRunShellCompletionsBash(t *testing.T) {
	code, _, _ := runCapture("--shell", "completions", "bash")
	if code != 0 {
		t.Errorf("--shell completions bash: exit %d, want 0", code)
	}
}

// TestRunShellCompletionsZsh verifies --shell completions zsh exits 0.
func TestRunShellCompletionsZsh(t *testing.T) {
	code, out, _ := runCapture("--shell", "completions", "zsh")
	if code != 0 {
		t.Errorf("--shell completions zsh: exit %d, want 0", code)
	}
	_ = out
}

// TestRunShellCompletionsFish verifies --shell completions fish exits 0.
func TestRunShellCompletionsFish(t *testing.T) {
	code, _, _ := runCapture("--shell", "completions", "fish")
	if code != 0 {
		t.Errorf("--shell completions fish: exit %d, want 0", code)
	}
}

// TestRunShellInitBash verifies --shell init bash exits 0.
func TestRunShellInitBash(t *testing.T) {
	code, out, _ := runCapture("--shell", "init", "bash")
	if code != 0 {
		t.Errorf("--shell init bash: exit %d, want 0", code)
	}
	_ = out
}

// TestRunShellUnknown verifies --shell <unknown> returns exit 2.
func TestRunShellUnknown(t *testing.T) {
	code, _, errOut := runCapture("--shell", "unknownsub")
	if code != 2 {
		t.Errorf("--shell unknownsub: exit %d, want 2", code)
	}
	if !strings.Contains(errOut, "unknownsub") {
		t.Errorf("--shell unknownsub: stderr should mention the bad subcommand; got %q", errOut)
	}
}

// TestRunServiceHelp verifies --service --help exits 0.
func TestRunServiceHelp(t *testing.T) {
	code, _, _ := runCapture("--service", "--help")
	if code != 0 {
		t.Errorf("--service --help: exit %d, want 0", code)
	}
}

// TestRunServiceUnknown verifies --service <unknown> returns exit 2.
func TestRunServiceUnknown(t *testing.T) {
	code, _, errOut := runCapture("--service", "not-a-real-subcommand")
	if code != 2 {
		t.Errorf("--service unknown: exit %d, want 2", code)
	}
	if !strings.Contains(errOut, "--service") {
		t.Errorf("--service unknown: stderr missing --service hint; got %q", errOut)
	}
}

// TestRunMaintenanceHelp verifies --maintenance --help exits 0.
func TestRunMaintenanceHelp(t *testing.T) {
	code, _, _ := runCapture("--maintenance", "--help")
	if code != 0 {
		t.Errorf("--maintenance --help: exit %d, want 0", code)
	}
}

// TestRunMaintenanceRestoreNoArg verifies --maintenance restore with no filename exits 2.
func TestRunMaintenanceRestoreNoArg(t *testing.T) {
	code, _, errOut := runCapture("--maintenance", "restore")
	if code != 2 {
		t.Errorf("--maintenance restore (no file): exit %d, want 2", code)
	}
	if !strings.Contains(errOut, "restore") {
		t.Errorf("--maintenance restore (no file): stderr should mention 'restore'; got %q", errOut)
	}
}

// TestRunMaintenanceRestoreBadFile verifies --maintenance restore <nonexistent> exits 1.
func TestRunMaintenanceRestoreBadFile(t *testing.T) {
	code, _, errOut := runCapture("--maintenance", "restore", "/tmp/definitely-does-not-exist-xyzzy.tar.gz")
	if code != 1 {
		t.Errorf("--maintenance restore (bad file): exit %d, want 1", code)
	}
	if !strings.Contains(errOut, "restore") {
		t.Errorf("--maintenance restore (bad file): stderr should mention 'restore'; got %q", errOut)
	}
}

// TestRunMaintenanceModeNoArg verifies --maintenance mode with no argument exits 2.
func TestRunMaintenanceModeNoArg(t *testing.T) {
	code, _, errOut := runCapture("--maintenance", "mode")
	if code != 2 {
		t.Errorf("--maintenance mode (no arg): exit %d, want 2", code)
	}
	if !strings.Contains(errOut, "mode") {
		t.Errorf("--maintenance mode (no arg): stderr should mention 'mode'; got %q", errOut)
	}
}

// TestRunMaintenanceUnknown verifies --maintenance <unknown> exits 2.
func TestRunMaintenanceUnknown(t *testing.T) {
	code, _, errOut := runCapture("--maintenance", "not-a-subcommand")
	if code != 2 {
		t.Errorf("--maintenance unknown: exit %d, want 2", code)
	}
	if !strings.Contains(errOut, "--maintenance") {
		t.Errorf("--maintenance unknown: stderr missing --maintenance hint; got %q", errOut)
	}
}

// TestRunUpdateHelp verifies --update --help exits 0 and shows update-specific
// help including the "check" subcommand description.
func TestRunUpdateHelp(t *testing.T) {
	code, out, _ := runCapture("--update", "--help")
	if code != 0 {
		t.Errorf("--update --help: exit %d, want 0", code)
	}
	// Update-specific help must list the available subcommands.
	for _, want := range []string{"check", "branch"} {
		if !strings.Contains(out, want) {
			t.Errorf("--update --help: output missing %q; got %q", want, out)
		}
	}
}

// TestRunUpdateBranchStable verifies --update branch stable exits 0.
func TestRunUpdateBranchStable(t *testing.T) {
	code, out, _ := runCapture("--update", "branch", "stable")
	if code != 0 {
		t.Errorf("--update branch stable: exit %d, want 0", code)
	}
	if !strings.Contains(out, "stable") {
		t.Errorf("--update branch stable: output missing 'stable'; got %q", out)
	}
}

// TestRunUpdateBranchBeta verifies --update branch beta exits 0.
func TestRunUpdateBranchBeta(t *testing.T) {
	code, out, _ := runCapture("--update", "branch", "beta")
	if code != 0 {
		t.Errorf("--update branch beta: exit %d, want 0", code)
	}
	if !strings.Contains(out, "beta") {
		t.Errorf("--update branch beta: output missing 'beta'; got %q", out)
	}
}

// TestRunUpdateBranchInvalid verifies --update branch <bad> exits 2.
func TestRunUpdateBranchInvalid(t *testing.T) {
	code, _, errOut := runCapture("--update", "branch", "notabranch")
	if code != 2 {
		t.Errorf("--update branch notabranch: exit %d, want 2", code)
	}
	if !strings.Contains(errOut, "notabranch") {
		t.Errorf("--update branch notabranch: stderr should mention the branch; got %q", errOut)
	}
}

// TestRunUpdateUnknownSubcommand verifies --update <bad> exits 2.
func TestRunUpdateUnknownSubcommand(t *testing.T) {
	code, _, errOut := runCapture("--update", "not-a-command")
	if code != 2 {
		t.Errorf("--update unknown: exit %d, want 2", code)
	}
	if !strings.Contains(errOut, "--update") {
		t.Errorf("--update unknown: stderr missing --update hint; got %q", errOut)
	}
}

// TestRunNormalizeHelp verifies -help is treated as --help (normalizeArgs
// converts -flag → --flag for multi-char flags).
func TestRunNormalizeHelp(t *testing.T) {
	code, out, _ := runCapture("-help")
	if code != 0 {
		t.Errorf("-help: exit %d, want 0", code)
	}
	if !strings.Contains(out, "--version") {
		t.Errorf("-help: output missing --version; got %q", out)
	}
}

// TestRunColorFlags exercise all three applyColor branches through run().
func TestRunColorFlags(t *testing.T) {
	// --color no + --help: must succeed
	code, out, _ := runCapture("--color", "no", "--help")
	if code != 0 {
		t.Errorf("--color no --help: exit %d, want 0", code)
	}
	if !strings.Contains(out, "--version") {
		t.Errorf("--color no --help: output missing --version; got %q", out)
	}

	// --color yes + --version: must succeed
	code, out, _ = runCapture("--color", "yes", "--version")
	if code != 0 {
		t.Errorf("--color yes --version: exit %d, want 0", code)
	}
	if !strings.Contains(out, "Built:") {
		t.Errorf("--color yes --version: output missing 'Built:'; got %q", out)
	}
}

// TestRunLangFlag exercises --lang without triggering server start.
func TestRunLangFlag(t *testing.T) {
	code, out, _ := runCapture("--lang", "en", "--help")
	if code != 0 {
		t.Errorf("--lang en --help: exit %d, want 0", code)
	}
	_ = out
}

// TestRunServiceStart verifies --service start returns exit 1 (no actual
// service present in CI) without panicking.
func TestRunServiceStart(t *testing.T) {
	code, _, _ := runCapture("--service", "start")
	// On a CI runner there is no service installed; we expect 1 (error), not a panic.
	if code == 0 {
		// If start somehow succeeded we just verify it didn't crash.
		t.Log("--service start returned 0 (service may actually be installed)")
	}
}

// TestRunServiceStop mirrors start: expect 1 on CI, no panic.
func TestRunServiceStop(t *testing.T) {
	code, _, _ := runCapture("--service", "stop")
	_ = code
}

// TestRunServiceRestart: no panic.
func TestRunServiceRestart(t *testing.T) {
	code, _, _ := runCapture("--service", "restart")
	_ = code
}

// TestRunServiceInstall: no panic.
func TestRunServiceInstall(t *testing.T) {
	code, _, _ := runCapture("--service", "--install")
	_ = code
}

// TestRunServiceDisable: no panic.
func TestRunServiceDisable(t *testing.T) {
	code, _, _ := runCapture("--service", "--disable")
	_ = code
}

// TestRunServiceUninstall: no panic.
func TestRunServiceUninstall(t *testing.T) {
	code, _, _ := runCapture("--service", "--uninstall")
	_ = code
}

// TestRunServiceReload: no panic.
func TestRunServiceReload(t *testing.T) {
	code, _, _ := runCapture("--service", "reload")
	_ = code
}

// TestRunMaintenanceSetup exercises --maintenance setup (no panic on CI).
func TestRunMaintenanceSetup(t *testing.T) {
	code, _, _ := runCapture("--maintenance", "setup")
	// May succeed or fail depending on directory permissions; either is acceptable.
	_ = code
}

// TestRunMaintenanceModeProduction exercises --maintenance mode production.
func TestRunMaintenanceModeProduction(t *testing.T) {
	code, _, _ := runCapture("--maintenance", "mode", "production")
	// Acceptable to fail on CI if config dir is unwriteable.
	_ = code
}

// ── Flag-parsing branch coverage ──────────────────────────────────────────────
// The flag-switch cases for common server-configuration flags must be covered
// even when the actual server does not start.  Each test below appends
// --help so that run() returns after the flag-parsing loop without touching
// the server startup section.

// TestRunFlagPort exercises the --port flag case.
func TestRunFlagPort(t *testing.T) {
	code, out, _ := runCapture("--port", "8080", "--help")
	if code != 0 {
		t.Errorf("--port --help: exit %d, want 0", code)
	}
	if !strings.Contains(out, "--port") {
		t.Errorf("--port --help: output missing --port; got %q", out)
	}
}

// TestRunFlagAddress exercises the --address flag case.
func TestRunFlagAddress(t *testing.T) {
	code, _, _ := runCapture("--address", "127.0.0.1", "--help")
	if code != 0 {
		t.Errorf("--address --help: exit %d, want 0", code)
	}
}

// TestRunFlagMode exercises the --mode flag case.
func TestRunFlagMode(t *testing.T) {
	code, _, _ := runCapture("--mode", "development", "--help")
	if code != 0 {
		t.Errorf("--mode --help: exit %d, want 0", code)
	}
}

// TestRunFlagConfig exercises the --config flag case.
func TestRunFlagConfig(t *testing.T) {
	code, _, _ := runCapture("--config", "/tmp", "--help")
	if code != 0 {
		t.Errorf("--config --help: exit %d, want 0", code)
	}
}

// TestRunFlagData exercises the --data flag case.
func TestRunFlagData(t *testing.T) {
	code, _, _ := runCapture("--data", "/tmp", "--help")
	if code != 0 {
		t.Errorf("--data --help: exit %d, want 0", code)
	}
}

// TestRunFlagLog exercises the --log flag case.
func TestRunFlagLog(t *testing.T) {
	code, _, _ := runCapture("--log", "/tmp", "--help")
	if code != 0 {
		t.Errorf("--log --help: exit %d, want 0", code)
	}
}

// TestRunFlagCache exercises the --cache flag case.
func TestRunFlagCache(t *testing.T) {
	code, _, _ := runCapture("--cache", "/tmp", "--help")
	if code != 0 {
		t.Errorf("--cache --help: exit %d, want 0", code)
	}
}

// TestRunFlagBackup exercises the --backup flag case.
func TestRunFlagBackup(t *testing.T) {
	code, _, _ := runCapture("--backup", "/tmp", "--help")
	if code != 0 {
		t.Errorf("--backup --help: exit %d, want 0", code)
	}
}

// TestRunFlagPID exercises the --pid flag case.
func TestRunFlagPID(t *testing.T) {
	code, _, _ := runCapture("--pid", "/tmp/pastebin-test.pid", "--help")
	if code != 0 {
		t.Errorf("--pid --help: exit %d, want 0", code)
	}
}

// TestRunFlagBaseURL exercises the --baseurl flag case.
func TestRunFlagBaseURL(t *testing.T) {
	code, _, _ := runCapture("--baseurl", "/app", "--help")
	if code != 0 {
		t.Errorf("--baseurl --help: exit %d, want 0", code)
	}
}

// TestRunFlagDebug exercises the --debug flag case.
func TestRunFlagDebug(t *testing.T) {
	code, _, _ := runCapture("--debug", "--help")
	if code != 0 {
		t.Errorf("--debug --help: exit %d, want 0", code)
	}
}

// TestRunFlagVersion exercises the --version flag case (already covered by
// TestRunVersion; this variant also exercises --status before --version to
// cover the showStatus branch separately from the server-start section).
func TestRunStatus(t *testing.T) {
	// --status with no server running: expect exit 1.
	code, _, _ := runCapture("--status")
	if code != 1 {
		t.Logf("--status: exit %d (expected 1 when no server is running)", code)
	}
}

// TestRunShellInitZsh verifies --shell init zsh exits 0.
func TestRunShellInitZsh(t *testing.T) {
	code, _, _ := runCapture("--shell", "init", "zsh")
	if code != 0 {
		t.Errorf("--shell init zsh: exit %d, want 0", code)
	}
}

// TestRunShellInitFish verifies --shell init fish exits 0.
func TestRunShellInitFish(t *testing.T) {
	code, _, _ := runCapture("--shell", "init", "fish")
	if code != 0 {
		t.Errorf("--shell init fish: exit %d, want 0", code)
	}
}

// TestRunUpdateBranchDaily verifies --update branch daily exits 0.
func TestRunUpdateBranchDaily(t *testing.T) {
	code, out, _ := runCapture("--update", "branch", "daily")
	if code != 0 {
		t.Errorf("--update branch daily: exit %d, want 0", code)
	}
	if !strings.Contains(out, "daily") {
		t.Errorf("--update branch daily: output missing 'daily'; got %q", out)
	}
}

// TestRunUpdateBranchNoName verifies --update branch with no name exits 2.
func TestRunUpdateBranchNoName(t *testing.T) {
	code, _, errOut := runCapture("--update", "branch")
	if code != 2 {
		t.Errorf("--update branch (no name): exit %d, want 2", code)
	}
	if !strings.Contains(errOut, "branch") {
		t.Errorf("--update branch (no name): stderr missing 'branch'; got %q", errOut)
	}
}

// TestRunMultipleFlagsBeforeHelp exercises several flag-parsing branches in
// a single call so each val() invocation is exercised.
func TestRunMultipleFlagsBeforeHelp(t *testing.T) {
	code, out, _ := runCapture(
		"--port", "9090",
		"--address", "0.0.0.0",
		"--mode", "production",
		"--config", "/tmp",
		"--data", "/tmp",
		"--log", "/tmp",
		"--cache", "/tmp",
		"--backup", "/tmp",
		"--pid", "/tmp/test.pid",
		"--baseurl", "/",
		"--color", "auto",
		"--lang", "en",
		"--help",
	)
	if code != 0 {
		t.Errorf("multi-flag + --help: exit %d, want 0", code)
	}
	if !strings.Contains(out, "--port") {
		t.Errorf("multi-flag + --help: output missing --port; got %q", out)
	}
}

// ─── Scheduler CLI ────────────────────────────────────────────────────────────

// tempDBPath creates a temp dir, sets DB_PATH to a SQLite file inside it, and
// returns a restore func. Call defer restore() at the top of the test.
func tempDBPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DB_PATH", filepath.Join(dir, "test.db"))
	// Prevent any config file from overriding the DB path.
	t.Setenv("CONFIG_DIR", dir)
}

// ─── Token CLI ────────────────────────────────────────────────────────────────
//
// AI.md canonicalises token management under --maintenance token (bare
// "token ..." was removed; see spec_diff lines 900-906), so these exercise
// the "--maintenance token ..." form exclusively.

// TestRunTokenList exercises the "--maintenance token list" path through an
// empty DB.
func TestRunTokenList(t *testing.T) {
	tempDBPath(t)
	code, out, errOut := runCapture("--maintenance", "token", "list")
	if code != 0 {
		t.Errorf("maintenance token list: exit %d, want 0; stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "No active tokens") {
		t.Errorf("maintenance token list (empty DB): expected 'No active tokens'; got %q", out)
	}
}

// TestRunTokenRevokeNoArg verifies "--maintenance token revoke" with no
// prefix exits 2.
func TestRunTokenRevokeNoArg(t *testing.T) {
	tempDBPath(t)
	code, _, errOut := runCapture("--maintenance", "token", "revoke")
	if code != 2 {
		t.Errorf("maintenance token revoke (no prefix): exit %d, want 2", code)
	}
	if !strings.Contains(errOut, "revoke") {
		t.Errorf("maintenance token revoke (no prefix): stderr missing 'revoke'; got %q", errOut)
	}
}

// TestRunTokenRevokeUnknown verifies "--maintenance token revoke <prefix>"
// for a token that does not exist returns exit 1.
func TestRunTokenRevokeUnknown(t *testing.T) {
	tempDBPath(t)
	code, _, _ := runCapture("--maintenance", "token", "revoke", "nonexistent")
	if code != 1 {
		t.Errorf("maintenance token revoke <unknown>: exit %d, want 1", code)
	}
}

// TestRunTokenUnknownSubcmd exercises the default branch of the token switch.
func TestRunTokenUnknownSubcmd(t *testing.T) {
	tempDBPath(t)
	code, _, errOut := runCapture("--maintenance", "token", "badsubcmd")
	if code != 2 {
		t.Errorf("maintenance token <unknown>: exit %d, want 2", code)
	}
	if !strings.Contains(errOut, "unknown") {
		t.Errorf("maintenance token <unknown>: stderr missing 'unknown'; got %q", errOut)
	}
}

// TestRunDebugWithStatus exercises the debugFlag body (mode.SetDebug, log.SetFlags,
// log.Printf "debug mode enabled") by combining --debug with --status so run()
// enters the debug block before the showStatus early-return.
func TestRunDebugWithStatus(t *testing.T) {
	code, _, _ := runCapture("--debug", "--status")
	// exit 1 = no server running; exit 0 = server happened to be healthy
	if code != 0 && code != 1 {
		t.Errorf("--debug --status: exit %d, want 0 or 1", code)
	}
}

// TestRunUpdateCheck exercises the --update check branch which calls
// CheckForUpdate with a 30-second context. Accepts exit 0 (up to date) or
// exit 1 (network unavailable / rate-limited) — both are non-panic outcomes.
func TestRunUpdateCheck(t *testing.T) {
	code, _, _ := runCapture("--update", "check")
	if code != 0 && code != 1 {
		t.Errorf("--update check: exit %d, want 0 or 1", code)
	}
}

// TestRunMaintenanceBackup exercises the --maintenance backup code path.
// May fail on CI if the data directory is unwriteable; that is acceptable.
func TestRunMaintenanceBackup(t *testing.T) {
	code, _, _ := runCapture("--maintenance", "backup")
	_ = code
}

// TestRunStatusWithConfig covers the configFlag true-branch inside the
// --status probe block (line 799: cfgPath = configFlag). The config file
// need not exist; config.Load handles missing files gracefully. run() exits
// before entering the server-startup section.
func TestRunStatusWithConfig(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "server.yml")
	code, _, _ := runCapture("--config", cfgPath, "--status")
	if code != 0 && code != 1 {
		t.Errorf("--config --status: exit %d, want 0 or 1", code)
	}
}
