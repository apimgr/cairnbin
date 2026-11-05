package server

import (
	"log"
	"sync"
	"time"

	"github.com/apimgr/cairnbin/src/config"
	"golang.org/x/net/publicsuffix"
)

// domainLearner observes incoming Host headers and, once a hostname is seen
// MinSamples times within SampleWindow, promotes it to baseDomain (eTLD+1)
// and wildcardDomain ("*.eTLD+1").  The results drive CORS and URL building
// without requiring a restart (PART 12 url_detection).
type domainLearner struct {
	mu sync.RWMutex
	// observations maps a bare hostname to the list of times it was observed.
	observations   map[string][]time.Time
	cfg            *config.URLDetectionConfig
	baseDomain     string
	wildcardDomain string
}

// newDomainLearner returns a learner configured from cfg.  A nil cfg or a
// config with Learning:false creates an inert learner that never updates.
func newDomainLearner(cfg *config.URLDetectionConfig) *domainLearner {
	return &domainLearner{
		observations: make(map[string][]time.Time),
		cfg:          cfg,
	}
}

// Observe records one observation of hostname.  When hostname accumulates
// MinSamples observations within SampleWindow the learner updates baseDomain
// and wildcardDomain.  Observations outside the window are pruned.
func (d *domainLearner) Observe(hostname string) {
	if hostname == "" || d.cfg == nil || !d.cfg.Learning {
		return
	}
	now := time.Now()
	window := d.cfg.SampleWindow
	if window <= 0 {
		window = 5 * time.Minute
	}
	minSamples := d.cfg.MinSamples
	if minSamples <= 0 {
		minSamples = 3
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	// Append the new observation.
	d.observations[hostname] = append(d.observations[hostname], now)

	// Prune stale observations outside the window.
	cutoff := now.Add(-window)
	fresh := d.observations[hostname][:0]
	for _, t := range d.observations[hostname] {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	d.observations[hostname] = fresh

	// Not enough samples yet — nothing to promote.
	if len(d.observations[hostname]) < minSamples {
		return
	}

	// Extract eTLD+1 for this hostname.
	base, err := publicsuffix.EffectiveTLDPlusOne(hostname)
	if err != nil || base == "" {
		return
	}
	wildcard := "*." + base

	// Only log and update when the domain changes.
	if base == d.baseDomain {
		return
	}
	prev := d.baseDomain
	d.baseDomain = base
	d.wildcardDomain = wildcard

	if d.cfg.LogChanges {
		if prev == "" {
			log.Printf("url_detection: learned domain %s (wildcard %s)", base, wildcard)
		} else {
			log.Printf("url_detection: domain changed from %s to %s (wildcard %s)", prev, base, wildcard)
		}
	}
}

// BaseDomain returns the most-recently learned eTLD+1, or "" if none has been
// learned yet.
func (d *domainLearner) BaseDomain() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.baseDomain
}

// WildcardDomain returns "*.{eTLD+1}" for the most-recently learned domain, or
// "" if none has been learned yet.
func (d *domainLearner) WildcardDomain() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.wildcardDomain
}

// CORSOrigins returns the CORS allowed-origin values derived from the learned
// domain.  If no domain has been learned the slice is empty and the caller
// should fall back to the operator-configured CORS setting.
func (d *domainLearner) CORSOrigins() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.baseDomain == "" {
		return nil
	}
	return []string{
		"https://" + d.baseDomain,
		"http://" + d.baseDomain,
		"https://" + d.wildcardDomain,
		"http://" + d.wildcardDomain,
	}
}
