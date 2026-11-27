//go:build gui && !freebsd && !netbsd && !openbsd

package gui

import (
	_ "github.com/gogpu/gg/gpu"

	"github.com/gogpu/gogpu"
	"github.com/gogpu/ui/app"
	"github.com/gogpu/ui/core/checkbox"
	"github.com/gogpu/ui/core/textfield"
	"github.com/gogpu/ui/desktop"

	"github.com/apimgr/cairnbin/src/client/setup"
	"github.com/apimgr/cairnbin/src/common/display"
)

// Config carries the resolved client settings the GUI needs at launch.
// It mirrors tui.ClientConfig so the CLI's mode dispatch can build either
// with the same inputs (PART 32).
type Config struct {
	// Server is the base URL of the cairnbin server (may be empty on first run).
	Server string
	// Token is the optional API token used by setup and subsequent API requests.
	Token string
	// Lang is the resolved Accept-Language locale string.
	Lang string
	// SaveURL persists a newly configured server URL to cli.yml.
	SaveURL func(string) error
	// TestSetup validates a server and optional API token before configuration changes.
	TestSetup func(string, string) (setup.ServerInfo, error)
	// SaveSetup persists the settings selected by the user.
	SaveSetup func(setup.Options) error
	// SetAutostart creates or removes this user's OS login autostart entry.
	SetAutostart func(bool) error
	// CfgPath is the path to cli.yml (used for display in setup prompts).
	CfgPath string
	// Theme is the resolved gui theme value from cli.yml ("dark", "light",
	// or "auto"). Empty defaults to dark (AI.md "CLI Theme Configuration").
	Theme string
}

// guiState holds the mutable data and widget references for the GUI's
// screens. gogpu/ui is a retained-mode toolkit, so screens are rebuilt as
// fresh widget trees and swapped in via app.SetRoot whenever the user
// navigates or state changes, rather than re-laid-out every frame as Gio
// required.
type guiState struct {
	app    *app.App
	config *Config
	close  func()

	// Setup screen.
	serverField  *textfield.Widget
	tokenField   *textfield.Widget
	setupErr     string
	setupInfo    setup.ServerInfo
	saveConfig   bool
	setDefault   bool
	startOnLogin bool

	// List screen.
	pastes  []PasteListItem
	listErr string

	// Detail screen.
	deleteField  *textfield.Widget
	selectedID   string
	selectedBody string
	detailErr    string
	deleting     bool
}

// listPageSize is the fixed page size for the GUI's paste list. The list
// screen has no pagination controls yet, so it always fetches the first
// page at this size (AI.md PART 32 does not require pagination UI for GUI).
const (
	listPage     = 1
	listPageSize = 50
)

// IsGUIAvailable reports whether a display is present and this is not a
// remote (SSH/Mosh) session. It defers to the shared display package's
// DisplayModeGUI auto-detection (which also honors TERM=dumb) rather than
// re-implementing the checks inline.
func IsGUIAvailable() bool {
	return display.DetectDisplayEnv().IsAutoDetectDisplayModeGUI()
}

// LaunchGUI opens the gogpu/ui window. gogpu/ui is pure Go and cross-platform
// — the same window code runs unmodified on Linux (X11/Wayland), macOS,
// Windows, and BSD, so there is no per-platform launcher file to maintain.
func LaunchGUI(config *Config) error {
	setUILang(config.Lang)

	gogpuApp := gogpu.NewApp(gogpu.DefaultConfig().
		WithTitle("cairnbin CLI").
		WithSize(800, 600))

	uiApp := app.New(
		app.WithWindowProvider(gogpuApp),
		app.WithPlatformProvider(gogpuApp),
		app.WithEventSource(gogpuApp.EventSource()),
		app.WithTheme(newTheme(config.Theme)),
	)

	st := &guiState{app: uiApp, config: config, close: gogpuApp.Close, saveConfig: true}
	if config.Server == "" {
		st.showSetup()
	} else {
		st.showList()
	}

	return desktop.Run(gogpuApp, uiApp)
}
