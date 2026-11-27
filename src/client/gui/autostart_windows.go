//go:build gui && windows

package gui

import (
	"fmt"

	"golang.org/x/sys/windows/registry"
)

func setAutostartWindows(enabled bool, executable string) error {
	const keyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	key, _, err := registry.CreateKey(registry.CURRENT_USER, keyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open current-user startup registry key: %w", err)
	}
	defer key.Close()
	if !enabled {
		if err := key.DeleteValue("Cairnbin CLI"); err != nil && err != registry.ErrNotExist {
			return fmt.Errorf("remove login autostart entry: %w", err)
		}
		return nil
	}
	if err := key.SetStringValue("Cairnbin CLI", executable); err != nil {
		return fmt.Errorf("write login autostart entry: %w", err)
	}
	return nil
}
