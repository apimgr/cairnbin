package tui

import "github.com/apimgr/cairnbin/src/common/terminal"

// LayoutConfig describes how a view should be rendered for a given SizeMode.
type LayoutConfig struct {
	ShowBorders    bool
	ShowHeader     bool
	ShowFooter     bool
	ShowSidebar    bool
	SidebarWidth   int
	MaxColumns     int
	TruncateAt     int
	UseAbbrev      bool
	VerticalScroll bool
	MultiPane      bool
	TileLayout     bool
}

// GetLayoutConfig returns the LayoutConfig for the given SizeMode, per the
// PART 32 GetLayoutConfig table (AI.md "TUI Responsive Layout").
func GetLayoutConfig(mode terminal.SizeMode) LayoutConfig {
	configs := map[terminal.SizeMode]LayoutConfig{
		terminal.SizeModeMicro: {
			MaxColumns:     2,
			TruncateAt:     30,
			UseAbbrev:      true,
			VerticalScroll: true,
		},
		terminal.SizeModeMinimal: {
			ShowHeader:     true,
			ShowFooter:     true,
			MaxColumns:     3,
			TruncateAt:     40,
			UseAbbrev:      true,
			VerticalScroll: true,
		},
		terminal.SizeModeCompact: {
			ShowBorders:    true,
			ShowHeader:     true,
			ShowFooter:     true,
			MaxColumns:     4,
			TruncateAt:     60,
			VerticalScroll: true,
		},
		terminal.SizeModeStandard: {
			ShowBorders:    true,
			ShowHeader:     true,
			ShowFooter:     true,
			MaxColumns:     6,
			TruncateAt:     80,
			VerticalScroll: true,
		},
		terminal.SizeModeWide: {
			ShowBorders:    true,
			ShowHeader:     true,
			ShowFooter:     true,
			ShowSidebar:    true,
			SidebarWidth:   30,
			MaxColumns:     8,
			TruncateAt:     120,
			VerticalScroll: true,
		},
		terminal.SizeModeUltrawide: {
			ShowBorders:  true,
			ShowHeader:   true,
			ShowFooter:   true,
			ShowSidebar:  true,
			SidebarWidth: 40,
			MaxColumns:   12,
			TruncateAt:   200,
			MultiPane:    true,
		},
		terminal.SizeModeMassive: {
			ShowBorders:  true,
			ShowHeader:   true,
			ShowFooter:   true,
			ShowSidebar:  true,
			SidebarWidth: 50,
			MaxColumns:   20,
			TruncateAt:   0,
			MultiPane:    true,
			TileLayout:   true,
		},
	}
	return configs[mode]
}

// sizeMode derives a SizeMode from raw terminal dimensions, mirroring the
// logic in the terminal package without depending on the unexported calculateMode.
func sizeMode(cols, rows int) terminal.SizeMode {
	switch {
	case cols < 40 || rows < 10:
		return terminal.SizeModeMicro
	case cols < 60 || rows < 16:
		return terminal.SizeModeMinimal
	case cols < 80 || rows < 24:
		return terminal.SizeModeCompact
	case cols < 120 || rows < 40:
		return terminal.SizeModeStandard
	case cols < 200 || rows < 60:
		return terminal.SizeModeWide
	case cols < 400 || rows < 80:
		return terminal.SizeModeUltrawide
	default:
		return terminal.SizeModeMassive
	}
}

// helpLineForMode returns the condensed help line appropriate for a given SizeMode.
func helpLineForMode(mode terminal.SizeMode) string {
	switch {
	case mode <= terminal.SizeModeMinimal:
		return t("footer_hint_xs")
	case mode == terminal.SizeModeCompact:
		return t("footer_hint_sm")
	default:
		return t("footer_hint_full")
	}
}
