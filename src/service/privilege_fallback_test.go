//go:build !windows

package service

import (
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// ─── escalationTools ordering (AI.md PART 23 "Escalation Detection by OS") ──

func TestEscalationTools_MatchesCurrentPlatform(t *testing.T) {
	got := escalationTools()

	var want []string
	switch runtime.GOOS {
	case "darwin":
		want = []string{"sudo", "osascript"}
	case "freebsd", "openbsd", "netbsd", "dragonfly":
		want = []string{"doas", "sudo", "su"}
	default:
		want = []string{"sudo", "su", "pkexec", "doas"}
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("escalationTools() = %v; want %v for GOOS=%s", got, want, runtime.GOOS)
	}
}

func TestEscalationTools_IncludesSuAndOsascriptFallbacks(t *testing.T) {
	tools := escalationTools()
	joined := strings.Join(tools, ",")

	switch runtime.GOOS {
	case "darwin":
		if !strings.Contains(joined, "osascript") {
			t.Errorf("macOS escalationTools() = %v; expected osascript fallback", tools)
		}
	case "freebsd", "openbsd", "netbsd", "dragonfly":
		if !strings.Contains(joined, "su") {
			t.Errorf("BSD escalationTools() = %v; expected su fallback", tools)
		}
	default:
		if !strings.Contains(joined, "su") {
			t.Errorf("linux escalationTools() = %v; expected su fallback", tools)
		}
	}
}

// ─── buildElevationCmd ────────────────────────────────────────────────────────

func TestBuildElevationCmd_Sudo_PassesArgsDirectly(t *testing.T) {
	cmd := buildElevationCmd("/usr/bin/sudo", "sudo", "/usr/local/bin/cairnbin", []string{"--service", "start"})

	want := []string{"/usr/bin/sudo", "/usr/local/bin/cairnbin", "--service", "start"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("sudo cmd.Args = %v; want %v", cmd.Args, want)
	}
}

func TestBuildElevationCmd_Pkexec_PassesArgsDirectly(t *testing.T) {
	cmd := buildElevationCmd("/usr/bin/pkexec", "pkexec", "/usr/local/bin/cairnbin", []string{"--service", "stop"})

	want := []string{"/usr/bin/pkexec", "/usr/local/bin/cairnbin", "--service", "stop"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("pkexec cmd.Args = %v; want %v", cmd.Args, want)
	}
}

func TestBuildElevationCmd_Doas_PassesArgsDirectly(t *testing.T) {
	cmd := buildElevationCmd("/usr/bin/doas", "doas", "/usr/local/bin/cairnbin", []string{"--service", "restart"})

	want := []string{"/usr/bin/doas", "/usr/local/bin/cairnbin", "--service", "restart"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("doas cmd.Args = %v; want %v", cmd.Args, want)
	}
}

func TestBuildElevationCmd_Su_WrapsCommandInShellString(t *testing.T) {
	cmd := buildElevationCmd("/bin/su", "su", "/usr/local/bin/cairnbin", []string{"--service", "start"})

	if len(cmd.Args) != 3 {
		t.Fatalf("su cmd.Args = %v; want 3 elements (path, -c, command)", cmd.Args)
	}
	if cmd.Args[0] != "/bin/su" || cmd.Args[1] != "-c" {
		t.Errorf("su cmd.Args = %v; want [/bin/su -c ...]", cmd.Args)
	}
	joined := cmd.Args[2]
	if !strings.Contains(joined, "/usr/local/bin/cairnbin") || !strings.Contains(joined, "--service") || !strings.Contains(joined, "start") {
		t.Errorf("su command string %q missing expected components", joined)
	}
}

func TestBuildElevationCmd_Osascript_UsesAdministratorPrivileges(t *testing.T) {
	cmd := buildElevationCmd("/usr/bin/osascript", "osascript", "/usr/local/bin/cairnbin", []string{"--service", "start"})

	if len(cmd.Args) != 3 {
		t.Fatalf("osascript cmd.Args = %v; want 3 elements (path, -e, script)", cmd.Args)
	}
	if cmd.Args[0] != "/usr/bin/osascript" || cmd.Args[1] != "-e" {
		t.Errorf("osascript cmd.Args = %v; want [/usr/bin/osascript -e ...]", cmd.Args)
	}
	script := cmd.Args[2]
	if !strings.Contains(script, "do shell script") {
		t.Errorf("osascript script %q missing 'do shell script'", script)
	}
	if !strings.Contains(script, "with administrator privileges") {
		t.Errorf("osascript script %q missing 'with administrator privileges'", script)
	}
	if !strings.Contains(script, "/usr/local/bin/cairnbin") {
		t.Errorf("osascript script %q missing target binary path", script)
	}
}

// ─── shellJoin / shellQuote ───────────────────────────────────────────────────

func TestShellQuote_EscapesEmbeddedSingleQuotes(t *testing.T) {
	got := shellQuote("it's a test")
	want := `'it'\''s a test'`
	if got != want {
		t.Errorf("shellQuote() = %q; want %q", got, want)
	}
}

func TestShellJoin_QuotesEachArgument(t *testing.T) {
	got := shellJoin([]string{"/usr/local/bin/cairnbin", "--service", "start"})
	want := "'/usr/local/bin/cairnbin' '--service' 'start'"
	if got != want {
		t.Errorf("shellJoin() = %q; want %q", got, want)
	}
}
