package theme

// ThemePalette holds all color values for a UI theme
type ThemePalette struct {
	Background string `json:"background"`
	Foreground string `json:"foreground"`
	Primary    string `json:"primary"`
	Secondary  string `json:"secondary"`
	Accent     string `json:"accent"`
	Success    string `json:"success"`
	Warning    string `json:"warning"`
	Error      string `json:"error"`
	Info       string `json:"info"`
	Surface    string `json:"surface"`
	SurfaceAlt string `json:"surface_alt"`
	Border     string `json:"border"`
	Muted      string `json:"muted"`
}

// ThemePaletteDark is the default dark theme palette
var ThemePaletteDark = ThemePalette{
	Background: "#282a36", Foreground: "#f8f8f2",
	Primary: "#bd93f9", Secondary: "#50fa7b", Accent: "#ff79c6",
	Success: "#50fa7b", Warning: "#ffb86c", Error: "#ff5555", Info: "#8be9fd",
	Surface: "#2b2d3a", SurfaceAlt: "#21222c", Border: "#44475a", Muted: "#6272a4",
}

// ThemePaletteLight is the light theme palette
var ThemePaletteLight = ThemePalette{
	Background: "#ffffff", Foreground: "#1f2328",
	Primary: "#0969da", Secondary: "#1a7f37", Accent: "#8250df",
	Success: "#1a7f37", Warning: "#9a6700", Error: "#d1242f", Info: "#0969da",
	Surface: "#f6f8fa", SurfaceAlt: "#eff2f5", Border: "#d1d9e0", Muted: "#59636e",
}

// GetThemePalette returns a palette for the given theme mode string.
// Valid values: "dark", "light", "auto". Defaults to dark.
func GetThemePalette(themeMode string) ThemePalette {
	switch themeMode {
	case "light":
		return ThemePaletteLight
	case "auto":
		if IsSystemDarkTheme() {
			return ThemePaletteDark
		}
		return ThemePaletteLight
	default:
		return ThemePaletteDark
	}
}

// TerminalPalette holds ANSI 16-color indices (0-15) for CLI/TUI — never
// the literal hex ThemePalette. lipgloss.Color() and the ESC[38;5;{n}m
// escape both accept these indices directly (AI.md PART 16 "CLI/TUI Color
// Mapping", PART 32 "CLI/TUI/GUI Theming").
type TerminalPalette struct {
	Foreground string `json:"foreground"`
	Muted      string `json:"muted"`
	Primary    string `json:"primary"`
	Success    string `json:"success"`
	Warning    string `json:"warning"`
	Error      string `json:"error"`
	Info       string `json:"info"`
	Border     string `json:"border"`
}

// TerminalPaletteDark is the ANSI-mapped palette for dark-themed terminals.
var TerminalPaletteDark = TerminalPalette{
	Foreground: "15", Muted: "7", Primary: "13",
	Success: "10", Warning: "11", Error: "9", Info: "12", Border: "13",
}

// TerminalPaletteLight is the ANSI-mapped palette for light-themed terminals.
var TerminalPaletteLight = TerminalPalette{
	Foreground: "0", Muted: "8", Primary: "4",
	Success: "2", Warning: "3", Error: "1", Info: "4", Border: "4",
}

// GetTerminalPalette returns the ANSI-mapped palette for the given theme
// mode string. Valid values: "dark", "light", "auto". Defaults to dark.
func GetTerminalPalette(themeMode string) TerminalPalette {
	switch themeMode {
	case "light":
		return TerminalPaletteLight
	case "auto":
		if IsSystemDarkTheme() {
			return TerminalPaletteDark
		}
		return TerminalPaletteLight
	default:
		return TerminalPaletteDark
	}
}
