package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMigrateLegacyKeysWebSecurityMoves(t *testing.T) {
	in := []byte("web:\n  security:\n    expires: x\n    encryption_key: aa\n    allowlist: [10.0.0.1]\nserver:\n  security:\n    encryption_key: bb\n")
	out := string(migrateLegacyKeys(in))
	var doc struct {
		Web struct {
			Security map[string]any `yaml:"security"`
		} `yaml:"web"`
		Server struct {
			Security map[string]any `yaml:"security"`
		} `yaml:"server"`
	}
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Server.Security["encryption_key"] != "bb" {
		t.Errorf("existing server.security value overwritten: %v", doc.Server.Security)
	}
	if doc.Server.Security["allowlist"] == nil {
		t.Errorf("allowlist not moved: %v", doc.Server.Security)
	}
	if _, ok := doc.Web.Security["encryption_key"]; ok {
		t.Error("legacy key left under web.security")
	}
	if doc.Web.Security["expires"] != "x" {
		t.Error("security.txt field must stay under web.security")
	}
}

func TestAllowlistEntryAcceptsScalarAndMapping(t *testing.T) {
	var c ServerSecurityConfig
	in := "allowlist:\n  - 10.0.0.1\n  - cidr: 192.168.0.0/16\n    description: LAN\n"
	if err := yaml.Unmarshal([]byte(in), &c); err != nil {
		t.Fatal(err)
	}
	got := c.AllowlistCIDRs()
	if len(got) != 2 || got[0] != "10.0.0.1" || got[1] != "192.168.0.0/16" || c.Allowlist[1].Description != "LAN" {
		t.Fatalf("unexpected allowlist: %+v", c.Allowlist)
	}
}
