//go:build windows

package service

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/svc/mgr"
)

// installWindows creates a Windows Service using the golang.org/x/sys/windows/svc/mgr
// package instead of sc.exe, giving more control and robustness (PART 24).
func installWindows() error {
	binaryPath := GetBinaryPath()

	// Create binary directory and copy binary.
	binDir := filepath.Dir(binaryPath)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	if exePath, err := os.Executable(); err == nil && exePath != binaryPath {
		if err := copyBinary(exePath, binaryPath); err != nil {
			return fmt.Errorf("failed to copy binary: %w", err)
		}
	}

	// Connect to the Windows Service Control Manager.
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to service manager: %w", err)
	}
	defer m.Disconnect()

	// Create the service with automatic start and a Virtual Service Account
	// (empty ServiceStartName = NT SERVICE\<name>).
	displayName := "Cairnbin API"
	s, err := m.CreateService(appName, binaryPath, mgr.Config{
		StartType:        mgr.StartAutomatic,
		DisplayName:      displayName,
		Description:      "Cairnbin API Server — fast, secure paste service",
		ServiceStartName: "",
	})
	if err != nil {
		return fmt.Errorf("failed to create Windows service: %w", err)
	}
	defer s.Close()

	fmt.Printf("%sWindows service '%s' installed\n", ok(), appName)
	return nil
}

// uninstallWindows removes the Windows Service using the mgr package (PART 24).
func uninstallWindows() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(appName)
	if err != nil {
		return fmt.Errorf("service %q not found: %w", appName, err)
	}
	defer s.Close()

	if err := s.Delete(); err != nil {
		return fmt.Errorf("failed to delete Windows service: %w", err)
	}

	fmt.Printf("%sWindows service '%s' uninstalled\n", ok(), appName)
	return nil
}
