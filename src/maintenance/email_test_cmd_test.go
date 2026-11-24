package maintenance

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/config"
)

func writeEmailTestConfig(t *testing.T, configDir string) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Server.Security.EncryptionKey = strings.Repeat("1", 64)
	cfg.Server.Notifications.Email.TemplateDir = filepath.Join(configDir, "template", "email")
	if err := config.Save(filepath.Join(configDir, "server.yml"), cfg); err != nil {
		t.Fatal(err)
	}
}

func TestRunEmailDryRunValidatesTemplatesWithoutSMTP(t *testing.T) {
	configDir := t.TempDir()
	writeEmailTestConfig(t, configDir)
	var output bytes.Buffer
	err := RunEmail("test", EmailOptions{ConfigDir: configDir, Stdout: &output, Lang: "en", DryRun: true})
	if err != nil {
		t.Fatalf("RunEmail dry-run: %v", err)
	}
	if !strings.Contains(output.String(), "templates are valid") {
		t.Errorf("dry-run output = %q, want template validation success", output.String())
	}
}

func TestRunEmailDryRunReportsInvalidOverride(t *testing.T) {
	configDir := t.TempDir()
	writeEmailTestConfig(t, configDir)
	templateDir := filepath.Join(configDir, "template", "email")
	if err := os.MkdirAll(templateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(templateDir, "test.txt"), []byte("Subject: test\n---\n{unknown_variable}"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := RunEmail("test", EmailOptions{ConfigDir: configDir, Stdout: &output, Lang: "en", DryRun: true})
	var validationErr *TemplateValidationError
	if !errors.As(err, &validationErr) || validationErr.Count == 0 {
		t.Fatalf("RunEmail error = %v, want TemplateValidationError", err)
	}
	if !strings.Contains(output.String(), "Unknown variable: {unknown_variable}") {
		t.Errorf("dry-run output = %q, want exact invalid-variable diagnostic", output.String())
	}
}
