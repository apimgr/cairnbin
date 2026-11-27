//go:build gui && windows

package gui

import "fmt"

func setAutostartLinux(bool, string) error {
	return fmt.Errorf("Linux autostart is unavailable on Windows")
}

func setAutostartDarwin(bool, string) error {
	return fmt.Errorf("macOS autostart is unavailable on Windows")
}
