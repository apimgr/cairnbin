//go:build gui && !windows && !freebsd && !netbsd && !openbsd

package gui

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func setAutostartLinux(enabled bool, executable string) error {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("resolve user configuration directory: %w", err)
	}
	entry := filepath.Join(configDir, "autostart", "cairnbin-cli.desktop")
	if !enabled {
		if err := os.Remove(entry); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove login autostart entry: %w", err)
		}
		return nil
	}
	quoted := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "$", "\\$", "%", "%%").Replace(executable) + `"`
	body := "[Desktop Entry]\nType=Application\nName=Cairnbin CLI\nExec=" + quoted + "\nX-GNOME-Autostart-enabled=true\n"
	return writeAutostartFile(entry, []byte(body))
}

func setAutostartDarwin(enabled bool, executable string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve user home directory: %w", err)
	}
	entry := filepath.Join(home, "Library", "LaunchAgents", "com.apimgr.cairnbin-cli.plist")
	if !enabled {
		if err := os.Remove(entry); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove login autostart entry: %w", err)
		}
		return nil
	}
	var body bytes.Buffer
	body.WriteString(xml.Header)
	body.WriteString("<plist version=\"1.0\"><dict><key>Label</key><string>com.apimgr.cairnbin-cli</string><key>ProgramArguments</key><array><string>")
	if err := xml.EscapeText(&body, []byte(executable)); err != nil {
		return fmt.Errorf("encode launch agent executable: %w", err)
	}
	body.WriteString("</string></array><key>RunAtLoad</key><true/></dict></plist>\n")
	return writeAutostartFile(entry, body.Bytes())
}

func writeAutostartFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create login autostart directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write login autostart entry: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("protect login autostart entry: %w", err)
		}
	}
	return nil
}
