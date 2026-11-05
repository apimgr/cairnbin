package main

import (
	"testing"

	"github.com/apimgr/cairnbin/src/config"
)

// TestBlocklistSources_Enabled verifies configured sources map to task.Source
// values, with incomplete entries dropped.
func TestBlocklistSources_Enabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Security.Blocklists = config.BlocklistsConfig{
		Enabled: true,
		Sources: []config.BlocklistSource{
			{File: "a.txt", URL: "https://example.test/a"},
			{File: "", URL: "https://example.test/skip"},
			{File: "b.txt", URL: ""},
		},
	}
	got := blocklistSources(cfg)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (incomplete entries dropped); got %+v", len(got), got)
	}
	if got[0].Name != "a.txt" || got[0].URL != "https://example.test/a" {
		t.Errorf("source = %+v", got[0])
	}
}

// TestBlocklistSources_Disabled verifies disabled blocklists yield no sources.
func TestBlocklistSources_Disabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Security.Blocklists.Enabled = false
	if got := blocklistSources(cfg); got != nil {
		t.Errorf("expected nil sources when disabled, got %+v", got)
	}
}

// TestCVESources_Enabled verifies an enabled, fully configured CVE source maps
// to a single task.Source.
func TestCVESources_Enabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Security.CVE = config.CVEConfig{
		Enabled: true,
		File:    "nvd.json",
		Source:  "https://example.test/cve",
	}
	got := cveSources(cfg)
	if len(got) != 1 || got[0].Name != "nvd.json" || got[0].URL != "https://example.test/cve" {
		t.Fatalf("got %+v", got)
	}
}

// TestCVESources_DisabledOrIncomplete verifies disabled or incomplete CVE
// config yields no sources.
func TestCVESources_DisabledOrIncomplete(t *testing.T) {
	cfg := config.DefaultConfig()
	// Default config has CVE disabled.
	if got := cveSources(cfg); got != nil {
		t.Errorf("expected nil for default (disabled) CVE config, got %+v", got)
	}
	cfg.Server.Security.CVE = config.CVEConfig{Enabled: true, File: "", Source: "https://x"}
	if got := cveSources(cfg); got != nil {
		t.Errorf("expected nil when File empty, got %+v", got)
	}
}
