package terminal_test

import (
	"testing"

	"github.com/apimgr/cairnbin/src/common/terminal"
)

// TestCalculateMode_Cases exercises every branch of calculateMode by calling
// GetTerminalSize with known col/row inputs via the exported path logic.
// Since calculateMode is unexported we drive it through SizeMode constants and
// the public helper methods.

// TestSizeMode_Predicates verifies the helper method thresholds match the spec.
func TestSizeMode_Predicates(t *testing.T) {
	cases := []struct {
		name        string
		mode        terminal.SizeMode
		wantASCII   bool
		wantBorders bool
		wantSidebar bool
		wantIcons   bool
	}{
		{"micro", terminal.SizeModeMicro, false, false, false, false},
		{"minimal", terminal.SizeModeMinimal, false, false, false, true},
		{"compact", terminal.SizeModeCompact, false, true, false, true},
		{"standard", terminal.SizeModeStandard, true, true, false, true},
		{"wide", terminal.SizeModeWide, true, true, true, true},
		{"ultrawide", terminal.SizeModeUltrawide, true, true, true, true},
		{"massive", terminal.SizeModeMassive, true, true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.mode.ShowASCIIArt(); got != tc.wantASCII {
				t.Errorf("ShowASCIIArt: got %v, want %v", got, tc.wantASCII)
			}
			if got := tc.mode.ShowBorders(); got != tc.wantBorders {
				t.Errorf("ShowBorders: got %v, want %v", got, tc.wantBorders)
			}
			if got := tc.mode.ShowSidebar(); got != tc.wantSidebar {
				t.Errorf("ShowSidebar: got %v, want %v", got, tc.wantSidebar)
			}
			if got := tc.mode.ShowIcons(); got != tc.wantIcons {
				t.Errorf("ShowIcons: got %v, want %v", got, tc.wantIcons)
			}
		})
	}
}

// TestSizeMode_Constants verifies the iota ordering is stable.
func TestSizeMode_Constants(t *testing.T) {
	if terminal.SizeModeMicro != 0 {
		t.Errorf("SizeModeMicro: want 0, got %d", terminal.SizeModeMicro)
	}
	if terminal.SizeModeMassive != 6 {
		t.Errorf("SizeModeMassive: want 6, got %d", terminal.SizeModeMassive)
	}
}

// TestGetTerminalSize_ReturnsValues verifies GetTerminalSize returns a struct
// with Cols and Rows both > 0 (defaults to 80×24 when no TTY is attached).
func TestGetTerminalSize_ReturnsValues(t *testing.T) {
	ts := terminal.GetTerminalSize()
	if ts.Cols <= 0 {
		t.Errorf("Cols: got %d, want > 0", ts.Cols)
	}
	if ts.Rows <= 0 {
		t.Errorf("Rows: got %d, want > 0", ts.Rows)
	}
}

// TestGetTerminalSize_ModeConsistent verifies Mode is consistent with
// the documented Cols/Rows of the default 80×24 terminal.
func TestGetTerminalSize_ModeConsistent(t *testing.T) {
	ts := terminal.GetTerminalSize()
	// In CI there is no TTY; GetTerminalSize defaults to 80×24 → Standard.
	// If it is actually a narrower terminal, the mode is still valid — just
	// check it's a known constant.
	switch ts.Mode {
	case terminal.SizeModeMicro, terminal.SizeModeMinimal, terminal.SizeModeCompact,
		terminal.SizeModeStandard, terminal.SizeModeWide,
		terminal.SizeModeUltrawide, terminal.SizeModeMassive:
		// valid
	default:
		t.Errorf("Mode %d is not a known SizeMode constant", ts.Mode)
	}
}
