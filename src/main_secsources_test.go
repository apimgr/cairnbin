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
		ExtraSources: []config.BlocklistSource{
			{ID: "a", Type: "ip", URL: "https://example.test/a"},
			{ID: "domains", Type: "domain", URL: "https://example.test/domains"},
			{ID: "firehol_level1", Type: "ip", URL: "https://example.test/override"},
			{ID: "bad-id", Type: "ip", URL: "https://example.test/invalid-id"},
			{ID: "bad-type", Type: "ua", URL: "https://example.test/invalid-type"},
			{ID: "a", Type: "domain", URL: "https://example.test/duplicate"},
			{ID: "", Type: "ip", URL: "https://example.test/skip"},
			{ID: "b", Type: "ip", URL: ""},
			{ID: "disabled", Type: "ip", URL: "https://example.test/disabled", Enabled: boolPointer(false)},
		},
	}
	got := blocklistSources(cfg)
	if len(got) != 3 {
		t.Fatalf("len = %d, want built-in plus two valid extras; got %+v", len(got), got)
	}
	if got[0].Name != "firehol_level1.txt" || got[1].Name != "a.txt" || got[1].URL != "https://example.test/a" || got[2].Name != "domains.txt" || got[2].Type != "domain" {
		t.Errorf("sources = %+v", got)
	}
}

// TestBlocklistSources_Disabled verifies disabled blocklists yield no sources.
func TestBlocklistSources_Disabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Security.Blocklists.Enabled = false
	if got := blocklistSources(cfg); len(got) != 1 || got[0].Name != "firehol_level1.txt" {
		t.Errorf("expected only immutable built-in source when extra lists are disabled, got %+v", got)
	}
}

func boolPointer(value bool) *bool { return &value }

// TestNVDTaskConfigMapsCanonicalDataSettings verifies data.cve values reach
// the scheduled updater, including its secret API key.
func TestNVDTaskConfigMapsCanonicalDataSettings(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Data.CVE = config.CVEConfig{
		Source: "https://example.test/cve", APIKey: "test-key",
	}
	got := nvdTaskConfig(cfg)
	if got.Source != "https://example.test/cve" || got.APIKey != "test-key" {
		t.Fatalf("got %+v", got)
	}
}
