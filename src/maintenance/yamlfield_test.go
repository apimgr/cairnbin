package maintenance_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/maintenance"
)

// TestSetYAMLFieldCreatesFile verifies that SetYAMLField creates the config
// file (and any missing parent directories) when it does not yet exist.
func TestSetYAMLFieldCreatesFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "nested", "server.yml")
	if err := maintenance.SetYAMLField(cfgPath, "update_branch", "beta"); err != nil {
		t.Fatalf("SetYAMLField: %v", err)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("reading created file: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "update_branch: beta" {
		t.Errorf("created content = %q, want %q", got, "update_branch: beta")
	}
}

// TestSetYAMLFieldReplacesExisting verifies in-place replacement of an existing
// top-level key while leaving unrelated keys untouched.
func TestSetYAMLFieldReplacesExisting(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "server.yml")
	initial := "mode: production\nupdate_branch: stable\nport: 8090\n"
	if err := os.WriteFile(cfgPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("seeding config: %v", err)
	}
	if err := maintenance.SetYAMLField(cfgPath, "update_branch", "daily"); err != nil {
		t.Fatalf("SetYAMLField: %v", err)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "update_branch: daily") {
		t.Errorf("expected updated key, got:\n%s", content)
	}
	if !strings.Contains(content, "mode: production") || !strings.Contains(content, "port: 8090") {
		t.Errorf("unrelated keys altered, got:\n%s", content)
	}
}

// TestSetYAMLFieldFilePermissions verifies the config file is written with the
// 0o600 owner-only permission required for files that may carry secrets.
func TestSetYAMLFieldFilePermissions(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "server.yml")
	if err := maintenance.SetYAMLField(cfgPath, "token", "tok_example"); err != nil {
		t.Fatalf("SetYAMLField: %v", err)
	}
	info, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %v, want 0600", perm)
	}
}
