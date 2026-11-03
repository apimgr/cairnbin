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
