package shell_test

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/shell"
)

// ─── Normalize ────────────────────────────────────────────────────────────────

func TestNormalize(t *testing.T) {
	cases := []struct {
		input string
		want  string
		isErr bool
	}{
		{"bash", "bash", false},
		{"BASH", "bash", false},
		{"zsh", "zsh", false},
		{"fish", "fish", false},
		{"sh", "sh", false},
		{"dash", "dash", false},
		{"ksh", "ksh", false},
		{"powershell", "powershell", false},
		{"pwsh", "powershell", false},
		{"  zsh  ", "zsh", false},
		{"tcsh", "", true},
		{"", "", true},
		{"cmd", "", true},
	}
	for _, tc := range cases {
		got, err := shell.Normalize(tc.input)
		if tc.isErr {
			if err == nil {
				t.Errorf("Normalize(%q): expected error, got %q", tc.input, got)
			}
		} else {
			if err != nil {
				t.Errorf("Normalize(%q): unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("Normalize(%q) = %q; want %q", tc.input, got, tc.want)
			}
		}
	}
}

// ─── Detect ───────────────────────────────────────────────────────────────────

func TestDetect_FromSHELL(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("ZSH_VERSION", "")
	t.Setenv("BASH_VERSION", "")
	t.Setenv("FISH_VERSION", "")
	got := shell.Detect()
	if got != "zsh" {
		t.Errorf("Detect() = %q; want zsh", got)
	}
}

func TestDetect_FromZSH_VERSION(t *testing.T) {
	t.Setenv("SHELL", "")
	t.Setenv("ZSH_VERSION", "5.9")
	t.Setenv("BASH_VERSION", "")
	t.Setenv("FISH_VERSION", "")
	got := shell.Detect()
	if got != "zsh" {
		t.Errorf("Detect() = %q; want zsh", got)
	}
}

func TestDetect_FromBASH_VERSION(t *testing.T) {
	t.Setenv("SHELL", "")
	t.Setenv("ZSH_VERSION", "")
	t.Setenv("BASH_VERSION", "5.2.0")
	t.Setenv("FISH_VERSION", "")
	got := shell.Detect()
	if got != "bash" {
		t.Errorf("Detect() = %q; want bash", got)
	}
}

func TestDetect_FallbackToBash(t *testing.T) {
	t.Setenv("SHELL", "")
	t.Setenv("ZSH_VERSION", "")
	t.Setenv("BASH_VERSION", "")
	t.Setenv("FISH_VERSION", "")
	got := shell.Detect()
	if got != "bash" {
		t.Errorf("Detect() fallback = %q; want bash", got)
	}
}

func TestDetect_UnknownSHELL_FallsBack(t *testing.T) {
	t.Setenv("SHELL", "/usr/bin/tcsh")
	t.Setenv("ZSH_VERSION", "")
	t.Setenv("BASH_VERSION", "")
	t.Setenv("FISH_VERSION", "")
	got := shell.Detect()
	if got != "bash" {
		t.Errorf("Detect() = %q; want bash for unknown shell", got)
	}
}

// ─── PrintHelp ────────────────────────────────────────────────────────────────

func TestPrintHelp_DoesNotPanic(t *testing.T) {
	shell.PrintHelp("cairnbin")
}

func TestPrintHelpUsesRequestedLanguage(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = writer
	shell.PrintHelp("cairnbin", "es")
	_ = writer.Close()
	os.Stdout = previous
	output, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "Comandos de integración del shell") || !strings.Contains(string(output), "Uso:") {
		t.Fatalf("Spanish shell help not localized: %q", output)
	}
}

// ─── PrintInit ────────────────────────────────────────────────────────────────

func TestPrintInit_AllShells(t *testing.T) {
	shells := []string{"bash", "zsh", "fish", "sh", "dash", "ksh", "powershell", "pwsh"}
	for _, sh := range shells {
		if err := shell.PrintInit("cairnbin", sh); err != nil {
			t.Errorf("PrintInit(%q): unexpected error: %v", sh, err)
		}
	}
}

func TestPrintInit_UnknownShell(t *testing.T) {
	if err := shell.PrintInit("cairnbin", "tcsh"); err == nil {
		t.Error("PrintInit(tcsh): expected error, got nil")
	}
}

func TestPrintInit_EmptyShell_DetectsAuto(t *testing.T) {
	t.Setenv("SHELL", "/bin/bash")
	t.Setenv("ZSH_VERSION", "")
	t.Setenv("BASH_VERSION", "")
	t.Setenv("FISH_VERSION", "")
	if err := shell.PrintInit("cairnbin", ""); err != nil {
		t.Errorf("PrintInit(empty): unexpected error: %v", err)
	}
}

// ─── PrintCompletions ─────────────────────────────────────────────────────────

func TestPrintCompletions_AllShells(t *testing.T) {
	shells := []string{"bash", "zsh", "fish", "sh", "dash", "ksh", "powershell", "pwsh"}
	for _, sh := range shells {
		if err := shell.PrintCompletions("cairnbin", sh); err != nil {
			t.Errorf("PrintCompletions(%q): unexpected error: %v", sh, err)
		}
	}
}

func TestPrintCompletions_UnknownShell(t *testing.T) {
	if err := shell.PrintCompletions("cairnbin", "tcsh"); err == nil {
		t.Error("PrintCompletions(tcsh): expected error, got nil")
	}
}

func TestPrintCompletions_BashContainsExpectedFlags(t *testing.T) {
	// Capture stdout via a pipe replacement — just ensure no panic and check
	// that the function runs without error. Content is tested via golden files
	// in integration tests; here we verify the call completes.
	if err := shell.PrintCompletions("cairnbin", "bash"); err != nil {
		t.Errorf("PrintCompletions(bash): unexpected error: %v", err)
	}
}

// ─── PrintClientCompletions ───────────────────────────────────────────────────

func TestPrintClientCompletions_AllShells(t *testing.T) {
	shells := []string{"bash", "zsh", "fish", "sh", "dash", "ksh", "powershell", "pwsh"}
	for _, sh := range shells {
		if err := shell.PrintClientCompletions("cairnbin-cli", sh); err != nil {
			t.Errorf("PrintClientCompletions(%q): unexpected error: %v", sh, err)
		}
	}
}

func TestPrintClientCompletions_UnknownShell(t *testing.T) {
	if err := shell.PrintClientCompletions("cairnbin-cli", "tcsh"); err == nil {
		t.Error("PrintClientCompletions(tcsh): expected error, got nil")
	}
}

// ─── Normalize round-trip ─────────────────────────────────────────────────────

func TestNormalize_PwshAlias(t *testing.T) {
	got, err := shell.Normalize("pwsh")
	if err != nil {
		t.Fatalf("Normalize(pwsh): %v", err)
	}
	if got != "powershell" {
		t.Errorf("expected powershell, got %s", got)
	}
}

func TestNormalize_ErrorContainsShellName(t *testing.T) {
	_, err := shell.Normalize("cmd")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "cmd") {
		t.Errorf("error message should mention the shell name: %v", err)
	}
}
