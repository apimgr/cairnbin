//go:build gui && !freebsd && !netbsd && !openbsd

package main

import (
	"fmt"
	"os"

	"github.com/apimgr/cairnbin/src/client/gui"
	"github.com/apimgr/cairnbin/src/client/paths"
	"github.com/apimgr/cairnbin/src/client/setup"
)

// runGUI launches the native GUI window. When no server is configured, the
// GUI's setup screen collects it — the same contract as runTUI's wizard, so
// the two modes behave identically on a fresh install.
func runGUI(server, lang string, cfg cliConfig, token string) {
	guiCfg := gui.Config{
		Server: server,
		Token:  token,
		Lang:   lang,
		TestSetup: func(serverURL, apiToken string) (setup.ServerInfo, error) {
			return setup.TestConnection(serverURL, apiToken, cfg.Server.APIVersion)
		},
		SaveSetup:    saveCLISetup,
		SetAutostart: gui.SetAutostart,
		CfgPath:      paths.Resolved(),
		Theme:        cfg.TUI.Theme,
	}
	if err := gui.LaunchGUI(&guiCfg); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_tui", "error", err))
		os.Exit(exitGeneral)
	}
}
