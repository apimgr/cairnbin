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
