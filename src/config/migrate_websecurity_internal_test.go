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

func TestValidateDebugConfig(t *testing.T) {
	d := DebugConfig{BlockProfileRate: -1, MutexProfileFraction: -3, MaxBodyLogSize: "junk"}
	validateDebugConfig(&d)
	if d.BlockProfileRate != 1 || d.MutexProfileFraction != 1 || d.MaxBodyLogBytes() != 10*1024 {
		t.Fatalf("defaults not applied: %+v", d)
	}
	if def := DefaultConfig().Server.Debug; !def.Pprof || !def.RuntimeEndpoints || def.LogBodies {
		t.Fatalf("unexpected defaults: %+v", def)
	}
}

func TestValidateReportsConfig(t *testing.T) {
	r := ReportsConfig{RateLimitPerMinute: -1, RateLimitPerIPBurst: 0}
	validateReportsConfig(&r)
	if r.RateLimitPerMinute != 60 || r.RateLimitPerIPBurst != 10 {
		t.Errorf("got %+v, want defaults 60/10", r)
	}
	ok := ReportsConfig{RateLimitPerMinute: 5, RateLimitPerIPBurst: 2}
	validateReportsConfig(&ok)
	if ok.RateLimitPerMinute != 5 || ok.RateLimitPerIPBurst != 2 {
		t.Errorf("valid values changed: %+v", ok)
	}
}

func TestAuditRetentionYears(t *testing.T) {
	if got := (ComplianceConfig{}).AuditRetentionYears(); got != 0 {
		t.Errorf("none enabled: got %d, want 0", got)
	}
	if got := (ComplianceConfig{GDPR: true, ISO27001: true}).AuditRetentionYears(); got != 3 {
		t.Errorf("gdpr+iso: got %d, want 3", got)
	}
	if got := (ComplianceConfig{GDPR: true, HIPAA: true}).AuditRetentionYears(); got != 6 {
		t.Errorf("gdpr+hipaa: got %d, want 6", got)
	}
}
