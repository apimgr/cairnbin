//go:build windows

// Package daemon provides daemonization support.
// On Windows, traditional Unix daemonization is not supported.
// Use --service install && --service start for background execution.
package daemon

import (
	"fmt"
	"os"

	"github.com/apimgr/cairnbin/src/common/i18n"
)

// Daemonize is a no-op on Windows. It prints a warning directing the user
// to use Windows Service management instead. lang selects the message
// locale (PART 30); an empty or unsupported value falls back to English.
func Daemonize(lang string) error {
	fmt.Fprintln(os.Stderr, i18n.Translate(lang, "cli.windows_daemon_warning"))
	fmt.Fprintln(os.Stderr, i18n.Translate(lang, "cli.windows_service_hint"))
	return nil
}
