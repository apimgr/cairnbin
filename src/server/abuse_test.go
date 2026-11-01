package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apimgr/pastebin/src/config"
)

func newAbuseServer(readLimit, multiplier int) *Server {
	cfg := config.DefaultConfig()
	cfg.Server.RateLimit.Enabled = true
	cfg.Server.RateLimit.Read.Requests = readLimit
	cfg.Web.Security.AbuseDetection.RequestFlood.Multiplier = multiplier
	cfg.Web.Security.AbuseDetection.RequestFlood.BlockDuration = "1h"
	return &Server{cfg: cfg, ipBlocks: newIPBlocks()}
}

func serveAbuse(s *Server, remote string, allowlisted bool) int {
	h := s.abuseMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	if allowlisted {
		r = r.WithContext(context.WithValue(r.Context(), ctxKeyAllowlisted, true))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec.Code
}

func TestAbuse_RequestFloodBlocksIP(t *testing.T) {
	s := newAbuseServer(2, 2)
	for i := 0; i < 4; i++ {
		if code := serveAbuse(s, "203.0.113.5:1000", false); code != http.StatusOK {
			t.Fatalf("request %d status=%d, want 200 under the threshold", i+1, code)
		}
	}
	if code := serveAbuse(s, "203.0.113.5:1000", false); code != http.StatusForbidden {
		t.Errorf("request over threshold status=%d, want 403", code)
	}
	if code := serveAbuse(s, "203.0.113.5:1000", false); code != http.StatusForbidden {
		t.Errorf("blocked IP status=%d, want 403 while the block is active", code)
	}
	if code := serveAbuse(s, "203.0.113.6:1000", false); code != http.StatusOK {
		t.Errorf("a different IP status=%d, want 200", code)
	}
}

func TestAbuse_AllowlistedIPIsNeverBlocked(t *testing.T) {
	s := newAbuseServer(1, 1)
	for i := 0; i < 10; i++ {
		if code := serveAbuse(s, "198.51.100.1:1", true); code != http.StatusOK {
			t.Fatalf("allowlisted request %d status=%d, want 200", i+1, code)
		}
	}
}

func TestAbuse_AutoBlockOffRecordsNothing(t *testing.T) {
	s := newAbuseServer(1, 1)
	s.cfg.Web.Security.AbuseDetection.AutoBlockIP = false
	for i := 0; i < 5; i++ {
		if code := serveAbuse(s, "203.0.113.7:1", false); code != http.StatusOK {
			t.Fatalf("request %d status=%d, want 200 with auto_block_ip off", i+1, code)
		}
	}
}

func TestAbuse_PermanentBlockedIPsFromConfig(t *testing.T) {
	s := newAbuseServer(100, 10)
	s.cfg.Web.Security.BlockedIPs = []string{"192.0.2.0/24", "2001:db8::1"}
	if code := serveAbuse(s, "192.0.2.77:5", false); code != http.StatusForbidden {
		t.Errorf("CIDR-blocked IP status=%d, want 403", code)
	}
	if code := serveAbuse(s, "[2001:db8::1]:5", false); code != http.StatusForbidden {
		t.Errorf("single-IP-blocked status=%d, want 403", code)
	}
	if code := serveAbuse(s, "192.0.3.1:5", false); code != http.StatusOK {
		t.Errorf("unblocked IP status=%d, want 200", code)
	}
	if code := serveAbuse(s, "192.0.2.77:5", true); code != http.StatusOK {
		t.Errorf("allowlisted IP in a blocked range status=%d, want 200", code)
	}
}

func TestAbuse_TemporaryBlockExpiresAndSweeps(t *testing.T) {
	b := newIPBlocks()
	now := time.Now()
	if n := b.block("203.0.113.9", "request flood", now, time.Minute); n != 1 {
		t.Errorf("first offense count = %d, want 1", n)
	}
	if !b.blocked("203.0.113.9", now.Add(30*time.Second)) {
		t.Error("block should be active before expiry")
	}
	if b.blocked("203.0.113.9", now.Add(2*time.Minute)) {
		t.Error("block should have expired")
	}

	b.block("203.0.113.10", "request flood", now, time.Minute)
	if n := b.block("203.0.113.10", "request flood", now, time.Minute); n != 2 {
		t.Errorf("repeat offense count = %d, want 2", n)
	}
	released := b.sweep(now.Add(2 * time.Minute))
	if len(released) != 1 || released[0] != "203.0.113.10" {
		t.Errorf("sweep released %v, want [203.0.113.10]", released)
	}
	if b.release("203.0.113.10") {
		t.Error("release of an already-swept block should report false")
	}
}

func TestAbuse_ValidateDefaults(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Web.Security.AbuseDetection.RequestFlood.Multiplier = 0
	cfg.Web.Security.AbuseDetection.RequestFlood.BlockDuration = "bogus"
	config.Validate(cfg)
	rf := cfg.Web.Security.AbuseDetection.RequestFlood
	if rf.Multiplier != 10 || rf.BlockDuration != "1h" {
		t.Errorf("invalid abuse settings not replaced by defaults: %+v", rf)
	}
}
