package config

import (
	"strings"
	"testing"
)

func TestDecodeConfigAcceptsFlexibleBooleans(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Server.Debug.LogBodies = false
	cfg.Server.Debug.Pprof = true
	in := "server:\n  debug:\n    log_bodies: enable\n    pprof: \"non\"\n    log_cache: oui\n"
	if err := decodeConfig([]byte(in), cfg); err != nil {
		t.Fatal(err)
	}
	d := cfg.Server.Debug
	if !d.LogBodies || d.Pprof || !d.LogCache {
		t.Fatalf("flexible booleans not applied: %+v", d)
	}
}

func TestDecodeConfigInvalidBooleanKeepsDefault(t *testing.T) {
	cfg := DefaultConfig()
	if err := decodeConfig([]byte("server:\n  debug:\n    pprof: maybe\n"), cfg); err != nil {
		t.Fatalf("invalid boolean must not fail startup: %v", err)
	}
	if !cfg.Server.Debug.Pprof {
		t.Fatal("invalid boolean must leave the default (true)")
	}
}

func TestDecodeConfigStillReportsUnknownFields(t *testing.T) {
	err := decodeConfig([]byte("server:\n  nonsense_key: 1\n"), DefaultConfig())
	if err == nil || !strings.Contains(err.Error(), "nonsense_key") {
		t.Fatalf("unknown field must still error, got %v", err)
	}
}
