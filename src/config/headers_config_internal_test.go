package config

import "testing"

func TestValidateHeadersConfigDefaults(t *testing.T) {
	h := HeadersConfig{DNSPrefetchControl: "bogus", NEL: NELConfig{MaxAgeSeconds: -1, SampleRate: 7}}
	validateHeadersConfig(&h)
	if h.COOP != "unsafe-none" || h.COEP != "unsafe-none" || h.CORP != "cross-origin" || h.CrossDomainPolicies != "none" {
		t.Errorf("empty values not defaulted: %+v", h)
	}
	if h.DNSPrefetchControl != "" {
		t.Errorf("invalid dns_prefetch_control kept: %q", h.DNSPrefetchControl)
	}
	if h.NEL.MaxAgeSeconds != 2592000 || h.NEL.SampleRate != 1.0 {
		t.Errorf("NEL not defaulted: %+v", h.NEL)
	}
}

func TestDefaultPermissionsPolicy(t *testing.T) {
	m := DefaultPermissionsPolicy()
	if m["camera"] != "()" || m["fullscreen"] != "(self)" || m["interest-cohort"] != "()" {
		t.Errorf("unexpected defaults: %v", m)
	}
}

func TestDeniedAIBots(t *testing.T) {
	r := RobotsConfig{}
	if got := r.DeniedAIBots(); len(got) != 0 {
		t.Errorf("default should deny none, got %v", got)
	}
	r.AIBots = AIBotsConfig{Default: "allow", Bots: map[string]string{"GPTBot": "deny", "Custom": "deny"}}
	got := r.DeniedAIBots()
	if len(got) != 2 || got[0] != "GPTBot" || got[1] != "Custom" {
		t.Errorf("explicit deny = %v", got)
	}
	r.AIBots = AIBotsConfig{Default: "deny", Bots: map[string]string{"ClaudeBot": "allow"}}
	got = r.DeniedAIBots()
	if len(got) != len(RecognizedAIBots)-1 {
		t.Errorf("default deny count = %d, want %d", len(got), len(RecognizedAIBots)-1)
	}
	for _, b := range got {
		if b == "ClaudeBot" {
			t.Errorf("explicitly allowed bot was denied")
		}
	}
}
