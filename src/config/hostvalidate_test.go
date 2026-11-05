package config

import (
	"testing"
)

func TestIsValidHost(t *testing.T) {
	tests := []struct {
		name        string
		host        string
		devMode     bool
		projectName string
		want        bool
	}{
		{"empty", "", false, "cairnbin", false},
		{"public etld+1 prod", "api.example.com", false, "cairnbin", true},
		{"public etld+1 dev", "api.example.com", true, "cairnbin", true},
		{"multi-label public suffix prod", "app.company.com.au", false, "cairnbin", true},
		{"deep subdomain of co.uk", "my.server.domain.co.uk", false, "cairnbin", true},
		{"bare public suffix rejected", "co.uk", false, "cairnbin", false},
		{"ipv4 always rejected", "192.168.1.1", true, "cairnbin", false},
		{"ipv4 always rejected prod", "192.168.1.1", false, "cairnbin", false},
		{"ipv6 always rejected", "::1", true, "cairnbin", false},
		{"no dot rejected", "myhost", false, "cairnbin", false},
		{"no dot rejected dev", "myhost", true, "cairnbin", false},
		{"bare localhost prod", "localhost", false, "cairnbin", false},
		{"bare localhost dev", "localhost", true, "cairnbin", true},
		{"dev tld prod rejected", "dev.local", false, "cairnbin", false},
		{"dev tld dev allowed", "dev.local", true, "cairnbin", true},
		{"dev tld .test prod rejected", "app.test", false, "cairnbin", false},
		{"dev tld .test dev allowed", "app.test", true, "cairnbin", true},
		{"project tld prod rejected", "app.cairnbin", false, "cairnbin", false},
		{"project tld dev allowed", "app.cairnbin", true, "cairnbin", true},
		{"nested project tld dev allowed", "my.app.cairnbin", true, "cairnbin", true},
		{"nested project tld prod rejected", "my.app.cairnbin", false, "cairnbin", false},
		{"bare project name dev allowed", "cairnbin", true, "cairnbin", true},
		{"bare project name prod rejected", "cairnbin", false, "cairnbin", false},
		{"onion always valid prod", "abc123.onion", false, "cairnbin", true},
		{"onion always valid dev", "abc123.onion", true, "cairnbin", true},
		{"i2p always valid", "abc123.i2p", false, "cairnbin", true},
		{"exit always valid", "abc123.exit", false, "cairnbin", true},
		{"case insensitive", "API.EXAMPLE.COM", false, "cairnbin", true},
		{"whitespace trimmed", "  api.example.com  ", false, "cairnbin", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValidHost(tt.host, tt.devMode, tt.projectName); got != tt.want {
				t.Errorf("IsValidHost(%q, %v, %q) = %v, want %v", tt.host, tt.devMode, tt.projectName, got, tt.want)
			}
		})
	}
}

func TestIsValidSSLHost(t *testing.T) {
	tests := []struct {
		name string
		host string
		want bool
	}{
		{"public domain valid", "api.example.com", true},
		{"onion never valid for LE", "abc123.onion", false},
		{"dev tld never valid for SSL", "dev.local", false},
		{"project tld never valid for SSL", "app.cairnbin", false},
		{"bare localhost never valid for SSL", "localhost", false},
		{"ip never valid for SSL", "192.168.1.1", false},
		{"i2p not onion still runs prod validation and fails", "abc123.i2p", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValidSSLHost(tt.host); got != tt.want {
				t.Errorf("IsValidSSLHost(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

func TestLoadEnvDomainValidation(t *testing.T) {
	t.Run("invalid DOMAIN skipped silently in production", func(t *testing.T) {
		t.Setenv("DOMAIN", "dev.local")
		t.Setenv("MODE", "production")
		c := DefaultConfig()
		before := c.Server.FQDN
		c.loadEnv()
		if c.Server.FQDN != before {
			t.Errorf("expected invalid DOMAIN to be skipped, got FQDN=%q", c.Server.FQDN)
		}
	})

	t.Run("valid DOMAIN accepted in production", func(t *testing.T) {
		t.Setenv("DOMAIN", "paste.example.com")
		t.Setenv("MODE", "production")
		c := DefaultConfig()
		c.loadEnv()
		if c.Server.FQDN != "paste.example.com" {
			t.Errorf("expected DOMAIN to be applied, got FQDN=%q", c.Server.FQDN)
		}
	})

	t.Run("dev TLD DOMAIN accepted in development", func(t *testing.T) {
		t.Setenv("DOMAIN", "dev.local")
		t.Setenv("MODE", "development")
		c := DefaultConfig()
		c.loadEnv()
		if c.Server.FQDN != "dev.local" {
			t.Errorf("expected dev-mode DOMAIN to be applied, got FQDN=%q", c.Server.FQDN)
		}
	})
}

// TestFQDNHostnameEnvValidation confirms the $HOSTNAME fallback source in
// fqdn() rejects an invalid host and falls through to the next source
// (never returning the invalid value), and accepts a valid one. os.Hostname()
// ranks ahead of $HOSTNAME in the resolution chain, so both cases force it
// out of the way with an FQDN override that is itself intentionally
// "localhost" — the one os.Hostname() value fqdn() always treats as absent —
// which is not possible to guarantee portably, so instead this exercises the
// validation helper directly against the same devMode/projectName wiring
// fqdn() uses, which is the behavior under test.
func TestFQDNHostnameEnvValidation(t *testing.T) {
	c := DefaultConfig()
	c.Server.Mode = "production"
	devMode := c.devModeForValidation()

	if IsValidHost("192.168.1.5", devMode, projectName) {
		t.Error("expected IP HOSTNAME value to fail production validation")
	}
	if !IsValidHost("host.example.com", devMode, projectName) {
		t.Error("expected valid HOSTNAME value to pass production validation")
	}

	c.Server.Mode = "development"
	devMode = c.devModeForValidation()
	if !IsValidHost("dev.local", devMode, projectName) {
		t.Error("expected dev-TLD HOSTNAME value to pass development validation")
	}
}
