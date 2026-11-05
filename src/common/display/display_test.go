package display_test

import (
	"testing"

	"github.com/apimgr/cairnbin/src/common/display"
)

func TestDisplayMode_Constants(t *testing.T) {
	cases := []struct {
		name string
		mode display.DisplayMode
		want int
	}{
		{"Headless", display.DisplayModeHeadless, 0},
		{"CLI", display.DisplayModeCLI, 1},
		{"TUI", display.DisplayModeTUI, 2},
		{"GUI", display.DisplayModeGUI, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if int(tc.mode) != tc.want {
				t.Errorf("DisplayMode%s = %d, want %d", tc.name, int(tc.mode), tc.want)
			}
		})
	}
}

func TestIsDumbTerminal(t *testing.T) {
	cases := []struct {
		name         string
		terminalType string
		want         bool
	}{
		{"dumb", "dumb", true},
		{"xterm", "xterm", false},
		{"empty", "", false},
		{"xterm-256color", "xterm-256color", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := &display.DisplayEnv{TerminalType: tc.terminalType}
			if got := env.IsDumbTerminal(); got != tc.want {
				t.Errorf("IsDumbTerminal() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCanUseANSI(t *testing.T) {
	cases := []struct {
		name         string
		isTerminal   bool
		terminalType string
		noColor      string
		want         bool
	}{
		{"terminal no dumb no NO_COLOR", true, "xterm-256color", "", true},
		{"not a terminal", false, "xterm-256color", "", false},
		{"dumb terminal", true, "dumb", "", false},
		{"NO_COLOR set", true, "xterm-256color", "1", false},
		{"dumb and NO_COLOR", true, "dumb", "1", false},
		{"not terminal and NO_COLOR", false, "xterm", "1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.noColor != "" {
				t.Setenv("NO_COLOR", tc.noColor)
			} else {
				t.Setenv("NO_COLOR", "")
			}
			env := &display.DisplayEnv{
				IsTerminal:   tc.isTerminal,
				TerminalType: tc.terminalType,
			}
			if got := display.CanUseANSI(env); got != tc.want {
				t.Errorf("CanUseANSI() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDisplayEnv_Predicates(t *testing.T) {
	cases := []struct {
		name         string
		mode         display.DisplayMode
		wantGUI      bool
		wantTUI      bool
		wantCLI      bool
		wantHeadless bool
	}{
		{"Headless mode", display.DisplayModeHeadless, false, false, false, true},
		{"CLI mode", display.DisplayModeCLI, false, false, true, false},
		{"TUI mode", display.DisplayModeTUI, false, true, false, false},
		{"GUI mode", display.DisplayModeGUI, true, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := display.DisplayEnv{Mode: tc.mode}
			if got := env.IsAutoDetectDisplayModeGUI(); got != tc.wantGUI {
				t.Errorf("IsAutoDetectDisplayModeGUI() = %v, want %v", got, tc.wantGUI)
			}
			if got := env.IsAutoDetectDisplayModeTUI(); got != tc.wantTUI {
				t.Errorf("IsAutoDetectDisplayModeTUI() = %v, want %v", got, tc.wantTUI)
			}
			if got := env.IsAutoDetectDisplayModeCLI(); got != tc.wantCLI {
				t.Errorf("IsAutoDetectDisplayModeCLI() = %v, want %v", got, tc.wantCLI)
			}
			if got := env.IsAutoDetectDisplayModeHeadless(); got != tc.wantHeadless {
				t.Errorf("IsAutoDetectDisplayModeHeadless() = %v, want %v", got, tc.wantHeadless)
			}
		})
	}
}

func TestDetectDisplayEnv_ReturnsValidMode(t *testing.T) {
	env := display.DetectDisplayEnv()
	switch env.Mode {
	case display.DisplayModeHeadless, display.DisplayModeCLI, display.DisplayModeTUI, display.DisplayModeGUI:
	default:
		t.Errorf("DetectDisplayEnv().Mode = %d, not one of the 4 known constants", int(env.Mode))
	}
}

func TestDetectDisplayEnv_SSHDetection(t *testing.T) {
	t.Setenv("SSH_CLIENT", "1.2.3.4 port 22")
	env := display.DetectDisplayEnv()
	if !env.IsSSH {
		t.Error("DetectDisplayEnv().IsSSH = false, want true when SSH_CLIENT is set")
	}
}

func TestDetectDisplayEnv_SSHTTYDetection(t *testing.T) {
	t.Setenv("SSH_CLIENT", "")
	t.Setenv("SSH_TTY", "/dev/pts/0")
	env := display.DetectDisplayEnv()
	if !env.IsSSH {
		t.Error("DetectDisplayEnv().IsSSH = false, want true when SSH_TTY is set")
	}
}

func TestDetectDisplayEnv_MoshDetection(t *testing.T) {
	t.Setenv("MOSH", "1")
	env := display.DetectDisplayEnv()
	if !env.IsMosh {
		t.Error("DetectDisplayEnv().IsMosh = false, want true when MOSH is set")
	}
}

func TestDetectDisplayEnv_MoshTermDetection(t *testing.T) {
	t.Setenv("MOSH", "")
	t.Setenv("TERM", "mosh-xterm")
	env := display.DetectDisplayEnv()
	if !env.IsMosh {
		t.Error("DetectDisplayEnv().IsMosh = false, want true when TERM contains 'mosh'")
	}
}

func TestDetectDisplayEnv_ScreenDetection(t *testing.T) {
	t.Setenv("STY", "12345.pts-0.hostname")
	env := display.DetectDisplayEnv()
	if !env.IsScreen {
		t.Error("DetectDisplayEnv().IsScreen = false, want true when STY is set")
	}
}

func TestDetectDisplayEnv_TmuxDetection(t *testing.T) {
	t.Setenv("STY", "")
	t.Setenv("TMUX", "/tmp/tmux-1000/default,12345,0")
	env := display.DetectDisplayEnv()
	if !env.IsScreen {
		t.Error("DetectDisplayEnv().IsScreen = false, want true when TMUX is set")
	}
}
