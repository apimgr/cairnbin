package tui

import (
	"strings"

	"github.com/apimgr/cairnbin/src/common/theme"
	"github.com/charmbracelet/lipgloss"
)

// TUIStyles holds the pre-built lipgloss styles for the TUI, derived from
// theme.TerminalPalette (ANSI-safe — see AI.md PART 16 "CLI/TUI Color
// Mapping" and PART 32 "CLI/TUI/GUI Theming"). The literal hex
// theme.ThemePalette is reserved for the web frontend only.
type TUIStyles struct {
	Base     lipgloss.Style
	Title    lipgloss.Style
	Header   lipgloss.Style
	Selected lipgloss.Style
	Normal   lipgloss.Style
	Muted    lipgloss.Style
	Error    lipgloss.Style
	Success  lipgloss.Style
	Warning  lipgloss.Style
	Border   lipgloss.Style
	Help     lipgloss.Style
	Input    lipgloss.Style
}

// darkTheme is the default ANSI-mapped palette.
var darkTheme = theme.TerminalPaletteDark

// lightTheme is the light ANSI-mapped palette.
var lightTheme = theme.TerminalPaletteLight

// CurrentTheme is the active ANSI-mapped palette used by the TUI. Defaults
// to dark. lipgloss (via termenv) auto-detects NO_COLOR and downgrades to
// plain output on its own — no additional check is needed here.
var CurrentTheme = darkTheme

var useUnicode = true

func asciiSymbols(s string) string {
	return strings.NewReplacer(
		"─", "-", "…", "...", "█", "|", "│", "|",
		"┌", "+", "┐", "+", "└", "+", "┘", "+",
		"├", "+", "┤", "+", "┬", "+", "┴", "+", "┼", "+",
		"╭", "+", "╮", "+", "╰", "+", "╯", "+",
		"↑", "up", "↓", "down", "→", "->", "←", "<-",
		"✓", "[ok]", "✗", "[x]",
	).Replace(s)
}

// StylesFromTerminalPalette builds a TUIStyles set from the given
// theme.TerminalPalette (AI.md PART 32 "TUI Styles from Palette").
// No background color is painted — the terminal's own background is
// left untouched; Selected uses Reverse() instead of a forced fill.
func StylesFromTerminalPalette(p theme.TerminalPalette) TUIStyles {
	fg := lipgloss.Color(p.Foreground)
	primary := lipgloss.Color(p.Primary)
	info := lipgloss.Color(p.Info)
	errColor := lipgloss.Color(p.Error)
	success := lipgloss.Color(p.Success)
	warning := lipgloss.Color(p.Warning)
	muted := lipgloss.Color(p.Muted)
	border := lipgloss.Color(p.Border)

	return TUIStyles{
		Base: lipgloss.NewStyle().
			Foreground(fg),

		Title: lipgloss.NewStyle().
			Bold(true).
			Foreground(primary),

		Header: lipgloss.NewStyle().
			Bold(true).
			Foreground(info),

		Selected: lipgloss.NewStyle().
			Reverse(true).
			Bold(true),

		Normal: lipgloss.NewStyle().
			Foreground(fg),

		Muted: lipgloss.NewStyle().
			Foreground(muted),

		Error: lipgloss.NewStyle().
			Foreground(errColor),

		Success: lipgloss.NewStyle().
			Foreground(success),

		Warning: lipgloss.NewStyle().
			Foreground(warning),

		Border: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(border),

		Help: lipgloss.NewStyle().
			Foreground(muted),

		Input: lipgloss.NewStyle().
			Foreground(fg).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(muted).
			Padding(0, 1),
	}
}

// DarkTheme returns the dark ANSI-mapped palette.
func DarkTheme() theme.TerminalPalette { return darkTheme }

// LightTheme returns the light ANSI-mapped palette.
func LightTheme() theme.TerminalPalette { return lightTheme }
