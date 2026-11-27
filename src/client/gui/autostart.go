//go:build gui && !freebsd && !netbsd && !openbsd

package gui

import (
	"fmt"
	"os"
	"runtime"
)

// SetAutostart enables or removes the current user's GUI login autostart entry.
func SetAutostart(enabled bool) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve CLI executable: %w", err)
	}
	switch runtime.GOOS {
	case "linux":
		return setAutostartLinux(enabled, executable)
	case "darwin":
		return setAutostartDarwin(enabled, executable)
	case "windows":
		return setAutostartWindows(enabled, executable)
	default:
		return fmt.Errorf("login autostart is unsupported on %s", runtime.GOOS)
	}
}
