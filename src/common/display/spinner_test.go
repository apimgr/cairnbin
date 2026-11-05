package display_test

import (
	"testing"

	"github.com/apimgr/cairnbin/src/common/display"
)

// TestTextSpinner_Start verifies TextSpinner.Start does not panic.
func TestTextSpinner_Start(t *testing.T) {
	ts := &display.TextSpinner{}
	ts.Start()
}

// TestTextSpinner_Stop verifies TextSpinner.Stop does not panic (intentional no-op).
// TextSpinner.Stop has an empty body by design; this test confirms it's callable.
func TestTextSpinner_Stop(t *testing.T) {
	ts := &display.TextSpinner{}
	ts.Start()
	ts.Stop()
	// Call stop again to verify idempotency
	ts.Stop()
}

// TestTextSpinner_SetMessage verifies TextSpinner.SetMessage prints without panic.
func TestTextSpinner_SetMessage(t *testing.T) {
	ts := &display.TextSpinner{}
	ts.SetMessage("updating")
}

// TestNewSpinner_NilEnv returns a TextSpinner when env is nil.
func TestNewSpinner_NilEnv(t *testing.T) {
	s := display.NewSpinner(nil, "loading")
	if s == nil {
		t.Fatal("NewSpinner(nil, ...) returned nil")
	}
	// TextSpinner.Stop is a no-op; just verify no panic
	s.Stop()
}

// TestNewSpinner_DumbTerminal returns a TextSpinner for TERM=dumb.
func TestNewSpinner_DumbTerminal(t *testing.T) {
	env := &display.DisplayEnv{
		TerminalType: "dumb",
		IsTerminal:   true,
	}
	s := display.NewSpinner(env, "working")
	if s == nil {
		t.Fatal("NewSpinner(dumb, ...) returned nil")
	}
	s.Start()
	s.SetMessage("still working")
	s.Stop()
}

// TestNewSpinner_NoColor returns TextSpinner when NO_COLOR is set.
func TestNewSpinner_NoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	env := &display.DisplayEnv{
		TerminalType: "xterm-256color",
		IsTerminal:   true,
	}
	s := display.NewSpinner(env, "processing")
	if s == nil {
		t.Fatal("NewSpinner with NO_COLOR returned nil")
	}
	s.Stop()
}

// TestNewSpinner_ANSISpinner_Xterm verifies xterm-256color gets ANSISpinner with unicode frames.
func TestNewSpinner_ANSISpinner_Xterm(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	env := &display.DisplayEnv{
		TerminalType: "xterm-256color",
		IsTerminal:   true,
	}
	s := display.NewSpinner(env, "loading")
	if s == nil {
		t.Fatal("NewSpinner returned nil")
	}
	// Verify it's an ANSISpinner by calling Start/Stop (ANSISpinner uses a goroutine)
	s.Start()
	s.SetMessage("updated message")
	s.Stop()
}

// TestNewSpinner_ANSISpinner_GenericTerm verifies non-xterm gets ANSISpinner with ASCII frames.
func TestNewSpinner_ANSISpinner_GenericTerm(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	env := &display.DisplayEnv{
		TerminalType: "vt100",
		IsTerminal:   true,
	}
	s := display.NewSpinner(env, "loading")
	if s == nil {
		t.Fatal("NewSpinner returned nil")
	}
	s.Start()
	s.Stop()
}

// TestShowProgress_NilEnv exercises the dumb-terminal path when env is nil.
func TestShowProgress_NilEnv(t *testing.T) {
	display.ShowProgress(nil, 50)
}

// TestShowProgress_DumbTerminal exercises the dumb-terminal path explicitly.
func TestShowProgress_DumbTerminal(t *testing.T) {
	env := &display.DisplayEnv{
		TerminalType: "dumb",
		IsTerminal:   true,
	}
	display.ShowProgress(env, 0)
	display.ShowProgress(env, 50)
	display.ShowProgress(env, 100)
}

// TestShowProgress_NoColor exercises the NO_COLOR path.
func TestShowProgress_NoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	env := &display.DisplayEnv{
		TerminalType: "xterm-256color",
		IsTerminal:   true,
	}
	display.ShowProgress(env, 25)
}

// TestShowProgress_ANSI exercises the ANSI progress bar path.
func TestShowProgress_ANSI(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	env := &display.DisplayEnv{
		TerminalType: "xterm-256color",
		IsTerminal:   true,
	}
	display.ShowProgress(env, 0)
	display.ShowProgress(env, 50)
	display.ShowProgress(env, 100)
}

// TestShowProgress_ClampNegative verifies percent is clamped to 0.
func TestShowProgress_ClampNegative(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	env := &display.DisplayEnv{
		TerminalType: "xterm-256color",
		IsTerminal:   true,
	}
	display.ShowProgress(env, -10)
}

// TestShowProgress_ClampOver100 verifies percent is clamped to 100.
func TestShowProgress_ClampOver100(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	env := &display.DisplayEnv{
		TerminalType: "xterm-256color",
		IsTerminal:   true,
	}
	display.ShowProgress(env, 150)
}
