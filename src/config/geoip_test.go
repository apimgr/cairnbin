package config_test

// Tests for the PART 19 GeoIP configuration: the country-blocking presets map
// and the documented default behaviour of the country list fields.

import (
	"testing"

	"github.com/apimgr/cairnbin/src/config"
	"gopkg.in/yaml.v3"
)

// TestGeoIPPresets_DefaultEmpty verifies presets ships empty.
//
// PART 19 "Country Blocking Presets" is explicit that no preset is
// pre-populated and that the project must not bundle a hardcoded regulatory
// country list as a built-in default. An empty default is what enforces that.
func TestGeoIPPresets_DefaultEmpty(t *testing.T) {
	p := config.DefaultConfig().Server.GeoIP
	if len(p.Presets) != 0 {
		t.Errorf("GeoIP.Presets: got %d preset(s) %v, want none bundled", len(p.Presets), p.Presets)
	}
}

// TestGeoIPPresets_RoundTrip verifies an operator-authored preset survives a
// YAML load, and that the map is a name -> []code mapping as specified.
func TestGeoIPPresets_RoundTrip(t *testing.T) {
	const in = `
server:
  geoip:
    enabled: true
    presets:
      my-list:
        - CN
        - RU
      another:
        - KP
`
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(in), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := cfg.Server.GeoIP.Presets
	if len(got) != 2 {
		t.Fatalf("Presets: got %d preset(s), want 2", len(got))
	}
	if codes := got["my-list"]; len(codes) != 2 || codes[0] != "CN" || codes[1] != "RU" {
		t.Errorf("Presets[\"my-list\"]: got %v, want [CN RU]", codes)
	}
	if codes := got["another"]; len(codes) != 1 || codes[0] != "KP" {
		t.Errorf("Presets[\"another\"]: got %v, want [KP]", codes)
	}
}

// TestGeoIPPresets_DoNotAffectEnforcement verifies that defining presets leaves
// the enforced lists untouched.
//
// PART 19 requires that presets are "a pure config-reuse convenience" and that
// enforcement is always driven by deny_countries/allow_countries. Merely
// declaring a preset must not seed either list.
func TestGeoIPPresets_DoNotAffectEnforcement(t *testing.T) {
	const in = `
server:
  geoip:
    presets:
      my-list:
        - CN
        - RU
`
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(in), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(cfg.Server.GeoIP.DenyCountries) != 0 {
		t.Errorf("DenyCountries: got %v, want empty (presets must not pre-fill it)",
			cfg.Server.GeoIP.DenyCountries)
	}
	if len(cfg.Server.GeoIP.AllowCountries) != 0 {
		t.Errorf("AllowCountries: got %v, want empty (presets must not pre-fill it)",
			cfg.Server.GeoIP.AllowCountries)
	}
}

// TestGeoIPDefaults_CountryListsEmpty verifies the fresh-install country
// blocking default: both lists empty, so every country is allowed regardless
// of which presets happen to exist.
func TestGeoIPDefaults_CountryListsEmpty(t *testing.T) {
	p := config.DefaultConfig().Server.GeoIP
	if len(p.DenyCountries) != 0 {
		t.Errorf("DenyCountries: got %v, want empty on a fresh install", p.DenyCountries)
	}
	if len(p.AllowCountries) != 0 {
		t.Errorf("AllowCountries: got %v, want empty on a fresh install", p.AllowCountries)
	}
	if !p.Enabled {
		t.Error("GeoIP.Enabled: got false, want true (PART 19: enabled by default)")
	}
}
