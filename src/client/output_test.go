package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestResolveOutputFormat(t *testing.T) {
	for _, tc := range []struct {
		output string
		json   bool
		want   string
	}{
		{"yaml", false, "yaml"},
		{"csv", false, "csv"},
		{"table", false, "table"},
		{"plain", false, "plain"},
		{"", true, "json"},
		{"json", false, "json"},
	} {
		if got := resolveOutputFormat(tc.json, tc.output); got != tc.want {
			t.Errorf("resolveOutputFormat(%q, %v) = %q, want %q", tc.output, tc.json, got, tc.want)
		}
	}
}

func TestSilentAutoUpdate(t *testing.T) {
	var cfg cliConfig
	cfg.Update.Auto = true
	for _, tc := range []struct {
		action      string
		interactive bool
		want        bool
	}{
		{action: "yes", want: true},
		{action: "yes", interactive: true},
		{action: "check"},
	} {
		if got := silentAutoUpdate(cfg, tc.action, tc.interactive); got != tc.want {
			t.Errorf("silentAutoUpdate(auto=true, %q, interactive=%t) = %t, want %t", tc.action, tc.interactive, got, tc.want)
		}
	}
	cfg.Update.Auto = false
	if silentAutoUpdate(cfg, "yes", false) {
		t.Error("silentAutoUpdate() enabled with update.auto=false")
	}
}

func TestApplyCLIConfigEnvOverrides(t *testing.T) {
	var cfg cliConfig
	cfg.Output.Format = "table"
	cfg.Output.Pager = "auto"
	cfg.Defaults.Public = "unlisted"
	cfg.Cache.TTL = "5m"
	t.Setenv("CAIRNBIN_FORMAT", "yaml")
	t.Setenv("CAIRNBIN_OUTPUT_PAGER", "never")
	t.Setenv("CAIRNBIN_PUBLIC", "public")
	t.Setenv("CAIRNBIN_CACHE_TTL", "30s")

	got := applyCLIConfigEnv(cfg)
	if got.Output.Format != "yaml" || got.Output.Pager != "never" || got.Defaults.Public != "public" || got.Cache.TTL != "30s" {
		t.Fatalf("environment overrides were not applied: %+v", got)
	}
}

func TestShouldUsePager(t *testing.T) {
	longOutput := []byte(strings.Repeat("line\n", 25))
	for _, tc := range []struct {
		mode     string
		terminal bool
		content  []byte
		want     bool
	}{
		{mode: "always", content: []byte("short"), want: true},
		{mode: "never", terminal: true, content: longOutput},
		{mode: "auto", content: longOutput},
		{mode: "auto", terminal: true, content: []byte("short")},
		{mode: "auto", terminal: true, content: longOutput, want: true},
	} {
		if got := shouldUsePager(tc.mode, tc.terminal, tc.content); got != tc.want {
			t.Errorf("shouldUsePager(%q, terminal=%t, bytes=%d) = %t, want %t", tc.mode, tc.terminal, len(tc.content), got, tc.want)
		}
	}
}

func TestReorderCommandArgs(t *testing.T) {
	got := reorderCommandArgs([]string{"notes.txt", "--lang", "go", "--output=csv", "--title", "Example"}, "--lang", "--output", "--title")
	want := []string{"--lang", "go", "--output=csv", "--title", "Example", "notes.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reorderCommandArgs() = %#v, want %#v", got, want)
	}
}

func TestNormalizeExpiry(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"never", "never"}, {"disabled", "never"}, {"off", "never"}, {"0", "never"},
		{"1h", "1h"}, {"18m", "18m"}, {"3600", "3600"},
	} {
		got, err := normalizeExpiry(tc.input)
		if err != nil || got != tc.want {
			t.Errorf("normalizeExpiry(%q) = %q, %v; want %q", tc.input, got, err, tc.want)
		}
	}
	for _, value := range []string{"0.5", "-1", "24h", "invalid"} {
		if _, err := normalizeExpiry(value); err == nil {
			t.Errorf("normalizeExpiry(%q) unexpectedly succeeded", value)
		}
	}
}

func TestParseVisibility(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  int
	}{
		{"public", 0}, {"yes", 0}, {"on", 0}, {"1", 0},
		{"unlisted", 1}, {"no", 1}, {"off", 1}, {"0", 1},
	} {
		got, err := parseVisibility(tc.input)
		if err != nil || got != tc.want {
			t.Errorf("parseVisibility(%q) = %d, %v; want %d", tc.input, got, err, tc.want)
		}
	}
	if _, err := parseVisibility("private"); err == nil {
		t.Error("private visibility unexpectedly accepted")
	}
}
