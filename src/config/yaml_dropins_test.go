package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMergesYAMLDropInsInFilenameOrder(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "server.yml")
	if err := os.WriteFile(root, []byte("server:\n  address: 127.0.0.1\n  seo:\n    sitemap:\n      max_urls: 100\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dropDir := filepath.Join(dir, "yaml.d")
	if err := os.Mkdir(dropDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"10.yml":     "server:\n  address: 0.0.0.0\n  seo:\n    sitemap:\n      max_urls: 200\n",
		"20.yaml":    "server:\n  address: 127.0.0.2\n",
		"hidden.yml": "server:\n  address: should-not-load\n",
	} {
		if name == "hidden.yml" {
			name = ".hidden.yml"
		}
		if err := os.WriteFile(filepath.Join(dropDir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Address != "127.0.0.2" {
		t.Fatalf("address = %q", cfg.Server.Address)
	}
	if cfg.Server.SEO.Sitemap.MaxURLs != 200 {
		t.Fatalf("max_urls = %d", cfg.Server.SEO.Sitemap.MaxURLs)
	}
	if !cfg.IsDropInOverride("server.address") {
		t.Fatal("address provenance should identify drop-in")
	}
}

func TestLoadRejectsDuplicateAndNullDropInKeys(t *testing.T) {
	for _, content := range []string{
		"server:\n  address: a\n  address: b\n",
		"server:\n  address: null\n",
	} {
		dir := t.TempDir()
		root := filepath.Join(dir, "server.yml")
		if err := os.WriteFile(root, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		dropDir := filepath.Join(dir, "yaml.d")
		if err := os.Mkdir(dropDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dropDir, "broken.yml"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "broken.yml:") {
			t.Fatalf("expected sourced parse error, got %v", err)
		}
	}
}

func TestLoadSecuresWritableDropInBeforeReading(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "server.yml")
	if err := os.WriteFile(root, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dropDir := filepath.Join(dir, "yaml.d")
	if err := os.Mkdir(dropDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dropIn := filepath.Join(dropDir, "10-listener.yml")
	if err := os.WriteFile(dropIn, []byte("server:\n  address: 127.0.0.2\n"), 0o666); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Address != "127.0.0.2" {
		t.Fatalf("address = %q, want secured drop-in value", cfg.Server.Address)
	}
	info, err := os.Stat(dropIn)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("drop-in permissions = %#o, want 0600", got)
	}
}

func TestSEOFeedDefaultsAndValidation(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Server.SEO.Feed.MaxItems != 50 {
		t.Fatalf("feed max_items default = %d", cfg.Server.SEO.Feed.MaxItems)
	}
	if cfg.Server.SEO.Feed.Title != "Cairnbin" {
		t.Fatalf("feed title default = %q", cfg.Server.SEO.Feed.Title)
	}
	cfg.Server.SEO.Feed.MaxItems = 201
	Validate(cfg)
	if cfg.Server.SEO.Feed.MaxItems != 50 {
		t.Fatalf("invalid feed max_items = %d", cfg.Server.SEO.Feed.MaxItems)
	}
}

func TestAreaMigrationPreservesDropInPrecedenceAndAreaValues(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "server.yml")
	dropDir := filepath.Join(dir, "yaml.d")
	if err := os.Mkdir(dropDir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		root:                                    "server:\n  address: 127.0.0.1\n  branding:\n    title: Legacy\n  seo:\n    sitemap:\n      max_urls: 100\n",
		filepath.Join(dropDir, "10-custom.yml"): "server:\n  seo:\n    sitemap:\n      max_urls: 200\n",
		filepath.Join(dropDir, "branding.yml"):  "server:\n  branding:\n    title: Current\n",
	}
	for path, contents := range files {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateAreaFiles(root); err != nil {
		t.Fatalf("migrate area files: %v", err)
	}
	cfg := DefaultConfig()
	if _, err := decodeConfigFiles(root, cfg); err != nil {
		t.Fatalf("decode migrated config: %v", err)
	}
	if cfg.Server.Branding.Title != "Current" {
		t.Errorf("branding title = %q, want existing area-file value Current", cfg.Server.Branding.Title)
	}
	if cfg.Server.SEO.Sitemap.MaxURLs != 200 {
		t.Errorf("sitemap max_urls = %d, want latest drop-in value 200", cfg.Server.SEO.Sitemap.MaxURLs)
	}
	if _, err := os.Stat(root + ".bak"); err != nil {
		t.Errorf("server.yml backup missing: %v", err)
	}
}

func TestSecretMigrationMovesDropInValuesAndRemovesDuplicates(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "server.yml")
	dropDir := filepath.Join(dir, "yaml.d")
	if err := os.Mkdir(dropDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root, []byte("server:\n  token: root-token\n  cache:\n    password: root-cache\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.yml"), []byte("server:\n  token: saved-token\n  cache:\n    password: saved-cache\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dropIn := filepath.Join(dropDir, "10-operator.yml")
	if err := os.WriteFile(dropIn, []byte("server:\n  notifications:\n    email:\n      smtp:\n        password: smtp-secret\n  token: dropin-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateRuntimeSecrets(root); err != nil {
		t.Fatalf("migrate secrets: %v", err)
	}
	secretBytes, err := os.ReadFile(filepath.Join(dir, "secrets.yml"))
	if err != nil {
		t.Fatal(err)
	}
	secretText := string(secretBytes)
	for _, expected := range []string{"saved-token", "saved-cache", "smtp-secret"} {
		if !strings.Contains(secretText, expected) {
			t.Errorf("secrets.yml does not contain %q: %s", expected, secretText)
		}
	}
	for _, path := range []string{root, dropIn} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"root-token", "dropin-token", "root-cache", "smtp-secret"} {
			if strings.Contains(string(contents), forbidden) {
				t.Errorf("secret %q remains in %s: %s", forbidden, path, contents)
			}
		}
	}
}
