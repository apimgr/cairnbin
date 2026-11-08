package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadLanguageReadsOnlyConfiguredLanguage(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "server.yml")
	if err := os.WriteFile(configPath, []byte("server:\n  lang: es\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadLanguage(configPath)
	if err != nil || got != "es" {
		t.Fatalf("ReadLanguage() = %q, %v; want es, nil", got, err)
	}
}

func TestReadLanguageDoesNotCreateMissingConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "missing", "server.yml")
	if _, err := ReadLanguage(configPath); err == nil {
		t.Fatal("ReadLanguage() error = nil for missing config")
	}
	if _, err := os.Stat(filepath.Dir(configPath)); !os.IsNotExist(err) {
		t.Fatalf("ReadLanguage() created config directory: stat error = %v", err)
	}
}
