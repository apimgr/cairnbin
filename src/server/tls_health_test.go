package server

import (
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/config"
)

func TestTLSHealthCheckTracksAvailability(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.TLS.Enabled = true
	s := &Server{}
	s.registerTLSHealthCheck(cfg)

	checks := s.datasetChecks.Refresh(time.Now())
	if len(checks) != 1 || checks[0].Key != "certificates_tls" || checks[0].Status != "error" || checks[0].Critical {
		t.Fatalf("initial TLS check = %#v, want non-critical certificate error", checks)
	}

	s.setTLSHealth(true)
	checks = s.datasetChecks.Snapshot()
	if len(checks) != 1 || checks[0].Status != "ok" {
		t.Fatalf("TLS check after certificate loads = %#v, want ok", checks)
	}

	s.setTLSHealth(false)
	checks = s.datasetChecks.Snapshot()
	if len(checks) != 1 || checks[0].Status != "error" {
		t.Fatalf("TLS check after certificate failure = %#v, want error", checks)
	}
}
