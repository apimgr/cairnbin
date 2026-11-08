package maintenance

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunEmailRejectsUnknownAction(t *testing.T) {
	err := RunEmail("bogus", EmailOptions{Lang: "es"})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
	if !strings.Contains(err.Error(), "acción desconocida") {
		t.Fatalf("Spanish unknown-action error = %q", err)
	}
}

func TestRunEmailNeedsSMTPUsesRequestedLanguage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "server.yml"), []byte("server:\n  fqdn: example.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := RunEmail("test", EmailOptions{ConfigDir: dir, LogDir: dir, Stdout: &out, Lang: "es"})
	if err == nil || !strings.Contains(err.Error(), "prueba de correo requiere configuración SMTP") {
		t.Fatalf("Spanish SMTP-required error = %v", err)
	}
}

func TestRunEmailNeedsSMTP(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "server.yml"), []byte("server:\n  fqdn: example.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SMTP_HOST", "")
	var out bytes.Buffer
	err := RunEmail("test", EmailOptions{ConfigDir: dir, LogDir: dir, Stdout: &out})
	if err == nil || !strings.Contains(err.Error(), "SMTP") {
		t.Fatalf("want SMTP-required error, got %v", err)
	}
}
