package tui

import (
	"testing"

	"github.com/apimgr/cairnbin/src/common/terminal"
)

// Tests for size.go: GetLayoutConfig, sizeMode, helpLineForMode

func TestSizeModeFromDimensions(t *testing.T) {
	tests := []struct {
		cols int
		rows int
		want terminal.SizeMode
	}{
		{30, 8, terminal.SizeModeMicro},
		{39, 15, terminal.SizeModeMicro},
		{40, 9, terminal.SizeModeMicro},
		{50, 12, terminal.SizeModeMinimal},
		{59, 15, terminal.SizeModeMinimal},
		{60, 20, terminal.SizeModeCompact},
		{79, 23, terminal.SizeModeCompact},
		{80, 24, terminal.SizeModeStandard},
		{100, 30, terminal.SizeModeStandard},
		{119, 39, terminal.SizeModeStandard},
		{120, 40, terminal.SizeModeWide},
		{150, 50, terminal.SizeModeWide},
		{200, 60, terminal.SizeModeUltrawide},
		{300, 70, terminal.SizeModeUltrawide},
		{400, 80, terminal.SizeModeMassive},
		{500, 100, terminal.SizeModeMassive},
	}

	for _, tc := range tests {
		got := sizeMode(tc.cols, tc.rows)
		if got != tc.want {
			t.Errorf("sizeMode(%d, %d) = %v, want %v", tc.cols, tc.rows, got, tc.want)
		}
	}
}

func TestGetLayoutConfig(t *testing.T) {
	cases := []struct {
		mode         terminal.SizeMode
		maxColumns   int
		truncateAt   int
		showBorders  bool
		showSidebar  bool
		sidebarWidth int
	}{
		{terminal.SizeModeMicro, 2, 30, false, false, 0},
		{terminal.SizeModeMinimal, 3, 40, false, false, 0},
		{terminal.SizeModeCompact, 4, 60, true, false, 0},
		{terminal.SizeModeStandard, 6, 80, true, false, 0},
		{terminal.SizeModeWide, 8, 120, true, true, 30},
		{terminal.SizeModeUltrawide, 12, 200, true, true, 40},
		{terminal.SizeModeMassive, 20, 0, true, true, 50},
	}
	for _, tc := range cases {
		cfg := GetLayoutConfig(tc.mode)
		if cfg.MaxColumns != tc.maxColumns {
			t.Errorf("mode %v MaxColumns = %d, want %d", tc.mode, cfg.MaxColumns, tc.maxColumns)
		}
		if cfg.TruncateAt != tc.truncateAt {
			t.Errorf("mode %v TruncateAt = %d, want %d", tc.mode, cfg.TruncateAt, tc.truncateAt)
		}
		if cfg.ShowBorders != tc.showBorders {
			t.Errorf("mode %v ShowBorders = %v, want %v", tc.mode, cfg.ShowBorders, tc.showBorders)
		}
		if cfg.ShowSidebar != tc.showSidebar {
			t.Errorf("mode %v ShowSidebar = %v, want %v", tc.mode, cfg.ShowSidebar, tc.showSidebar)
		}
		if cfg.SidebarWidth != tc.sidebarWidth {
			t.Errorf("mode %v SidebarWidth = %d, want %d", tc.mode, cfg.SidebarWidth, tc.sidebarWidth)
		}
	}
}

func TestGetLayoutConfigFlags(t *testing.T) {
	if !GetLayoutConfig(terminal.SizeModeMicro).UseAbbrev {
		t.Error("Micro UseAbbrev should be true")
	}
	if GetLayoutConfig(terminal.SizeModeStandard).UseAbbrev {
		t.Error("Standard UseAbbrev should be false")
	}
	if !GetLayoutConfig(terminal.SizeModeWide).VerticalScroll {
		t.Error("Wide VerticalScroll should be true")
	}
	if GetLayoutConfig(terminal.SizeModeUltrawide).VerticalScroll {
		t.Error("Ultrawide VerticalScroll should be false")
	}
	if !GetLayoutConfig(terminal.SizeModeUltrawide).MultiPane {
		t.Error("Ultrawide MultiPane should be true")
	}
	if !GetLayoutConfig(terminal.SizeModeMassive).TileLayout {
		t.Error("Massive TileLayout should be true")
	}
	if GetLayoutConfig(terminal.SizeModeWide).TileLayout {
		t.Error("Wide TileLayout should be false")
	}
}

func TestHelpLineForModeMicro(t *testing.T) {
	line := helpLineForMode(terminal.SizeModeMicro)
	if line != "?:help q:quit" {
		t.Errorf("helpLineForMode(Micro) = %q, want %q", line, "?:help q:quit")
	}
}

func TestHelpLineForModeMinimal(t *testing.T) {
	line := helpLineForMode(terminal.SizeModeMinimal)
	if line != "?:help q:quit" {
		t.Errorf("helpLineForMode(Minimal) = %q, want %q", line, "?:help q:quit")
	}
}

func TestHelpLineForModeCompact(t *testing.T) {
	line := helpLineForMode(terminal.SizeModeCompact)
	expected := "↑↓:nav │ enter:open │ /:search │ ?:help │ q:quit"
	if line != expected {
		t.Errorf("helpLineForMode(Compact) = %q, want %q", line, expected)
	}
}

func TestHelpLineForModeStandard(t *testing.T) {
	line := helpLineForMode(terminal.SizeModeStandard)
	expected := "↑↓/jk:nav │ enter:open │ /:search │ r:refresh │ n:new │ d:delete │ ?:help │ q:quit"
	if line != expected {
		t.Errorf("helpLineForMode(Standard) = %q, want %q", line, expected)
	}
}
