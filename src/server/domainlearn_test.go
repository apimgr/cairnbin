package server

// Unit tests for domainlearn.go — newDomainLearner, Observe, BaseDomain,
// WildcardDomain, and CORSOrigins.

import (
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/config"
)

func cfg3samples() *config.URLDetectionConfig {
	return &config.URLDetectionConfig{
		Learning:     true,
		MinSamples:   3,
		SampleWindow: 5 * time.Minute,
		LogChanges:   false,
	}
}

// ─── newDomainLearner ─────────────────────────────────────────────────────────

func TestNewDomainLearner_NilCfg(t *testing.T) {
	d := newDomainLearner(nil)
	if d == nil {
		t.Fatal("expected non-nil learner")
	}
	// Nil cfg → Observe is a no-op.
	d.Observe("example.com")
	if d.BaseDomain() != "" {
		t.Errorf("BaseDomain should be empty with nil cfg, got %q", d.BaseDomain())
	}
}

func TestNewDomainLearner_LearningDisabled(t *testing.T) {
	cfg := &config.URLDetectionConfig{Learning: false, MinSamples: 1}
	d := newDomainLearner(cfg)
	d.Observe("example.com")
	d.Observe("example.com")
	if d.BaseDomain() != "" {
		t.Errorf("BaseDomain should be empty when learning disabled, got %q", d.BaseDomain())
	}
}

// ─── Observe — threshold ──────────────────────────────────────────────────────

func TestObserve_PromotesAfterMinSamples(t *testing.T) {
	d := newDomainLearner(cfg3samples())
	// 2 observations — below threshold.
	d.Observe("example.com")
	d.Observe("example.com")
	if d.BaseDomain() != "" {
		t.Errorf("BaseDomain should be empty before MinSamples, got %q", d.BaseDomain())
	}
	// 3rd observation — should promote.
	d.Observe("example.com")
	if d.BaseDomain() != "example.com" {
		t.Errorf("BaseDomain = %q, want example.com", d.BaseDomain())
	}
}

func TestObserve_Subdomain_ExtractsETLD1(t *testing.T) {
	d := newDomainLearner(cfg3samples())
	for i := 0; i < 3; i++ {
		d.Observe("sub.example.com")
	}
	if d.BaseDomain() != "example.com" {
		t.Errorf("BaseDomain = %q, want example.com (eTLD+1)", d.BaseDomain())
	}
}

func TestObserve_EmptyHostname_Ignored(t *testing.T) {
	d := newDomainLearner(cfg3samples())
	d.Observe("")
	if d.BaseDomain() != "" {
		t.Errorf("BaseDomain should be empty after empty hostname, got %q", d.BaseDomain())
	}
}

func TestObserve_DefaultWindow_WhenZero(t *testing.T) {
	cfg := &config.URLDetectionConfig{
		Learning:     true,
		MinSamples:   2,
		SampleWindow: 0,
	}
	d := newDomainLearner(cfg)
	d.Observe("example.com")
	d.Observe("example.com")
	if d.BaseDomain() != "example.com" {
		t.Errorf("BaseDomain = %q, want example.com with default window", d.BaseDomain())
	}
}

func TestObserve_DefaultMinSamples_WhenZero(t *testing.T) {
	cfg := &config.URLDetectionConfig{
		Learning:     true,
		MinSamples:   0,
		SampleWindow: 5 * time.Minute,
	}
	d := newDomainLearner(cfg)
	// MinSamples defaults to 3 when 0.
	d.Observe("example.com")
	d.Observe("example.com")
	if d.BaseDomain() != "" {
		t.Errorf("expected empty before 3 samples, got %q", d.BaseDomain())
	}
	d.Observe("example.com")
	if d.BaseDomain() != "example.com" {
		t.Errorf("BaseDomain = %q, want example.com after 3 observations", d.BaseDomain())
	}
}

// ─── WildcardDomain ───────────────────────────────────────────────────────────

func TestWildcardDomain_BeforePromotion(t *testing.T) {
	d := newDomainLearner(cfg3samples())
	if w := d.WildcardDomain(); w != "" {
		t.Errorf("WildcardDomain before promotion = %q, want empty", w)
	}
}

func TestWildcardDomain_AfterPromotion(t *testing.T) {
	d := newDomainLearner(cfg3samples())
	for i := 0; i < 3; i++ {
		d.Observe("example.com")
	}
	if w := d.WildcardDomain(); w != "*.example.com" {
		t.Errorf("WildcardDomain = %q, want *.example.com", w)
	}
}

// ─── CORSOrigins ──────────────────────────────────────────────────────────────

func TestCORSOrigins_EmptyBeforePromotion(t *testing.T) {
	d := newDomainLearner(cfg3samples())
	if origins := d.CORSOrigins(); len(origins) != 0 {
		t.Errorf("CORSOrigins before promotion = %v, want empty", origins)
	}
}

func TestCORSOrigins_AfterPromotion(t *testing.T) {
	d := newDomainLearner(cfg3samples())
	for i := 0; i < 3; i++ {
		d.Observe("example.com")
	}
	origins := d.CORSOrigins()
	if len(origins) != 4 {
		t.Fatalf("CORSOrigins len = %d, want 4", len(origins))
	}
	want := map[string]bool{
		"https://example.com":   true,
		"http://example.com":    true,
		"https://*.example.com": true,
		"http://*.example.com":  true,
	}
	for _, o := range origins {
		if !want[o] {
			t.Errorf("unexpected origin: %q", o)
		}
	}
}

// ─── LogChanges path ──────────────────────────────────────────────────────────

func TestObserve_LogChanges_NoError(t *testing.T) {
	cfg := &config.URLDetectionConfig{
		Learning:     true,
		MinSamples:   1,
		SampleWindow: 5 * time.Minute,
		LogChanges:   true,
	}
	d := newDomainLearner(cfg)
	// Should not panic.
	d.Observe("example.com")
	d.Observe("other.org")
}
