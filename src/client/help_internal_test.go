package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestWriteUsage_SpecLayout checks the PART 32 --help layout: tagline, Usage,
// the universal flag rows with short forms, the shells line and footer.
func TestWriteUsage_SpecLayout(t *testing.T) {
	setUILang("en")
	var buf bytes.Buffer
	writeUsage(&buf)
	out := buf.String()
	for _, want := range []string{
		" - CLI for cairnbin", "Usage:", "# TUI mode (no args)", "Flags:",
		"-h, --help", "-v, --version", "--shell completions [SHELL]", "--shell init [SHELL]", "--shell help",
		"--server URL", "--token TOKEN", "--token-file FILE", "--config NAME", "--debug",
		"--color {auto|yes|no}", "--lang CODE",
		"Shells: bash, zsh, fish, sh, dash, ksh, powershell, pwsh",
		"Run without arguments for interactive TUI mode.",
		"command> help' for detailed help on any command.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help output missing %q", want)
		}
	}
}

func TestPrintCommandHelp(t *testing.T) {
	setUILang("en")
	for _, cmd := range []string{"create", "get", "delete", "list", "update"} {
		var buf bytes.Buffer
		if !printCommandHelp(&buf, cmd) || !strings.Contains(buf.String(), cmd) {
			t.Errorf("no help for %q: %q", cmd, buf.String())
		}
	}
	var buf bytes.Buffer
	if printCommandHelp(&buf, "bogus") || buf.Len() != 0 {
		t.Error("unknown command must report false and print nothing")
	}
}

func TestVisibilityName(t *testing.T) {
	if visibilityName(0) != "public" || visibilityName(1) != "unlisted" {
		t.Errorf("visibilityName mapping wrong: %q %q", visibilityName(0), visibilityName(1))
	}
}
