package server

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/apimgr/cairnbin/src/audit"
	"github.com/apimgr/cairnbin/src/common/email"
	"github.com/apimgr/cairnbin/src/config"
)

const lifecycleEmailTimeout = 5 * time.Second

// SetShutdownReason records the reason used in the graceful shutdown email.
func (s *Server) SetShutdownReason(reason string) {
	s.lifecycleMu.Lock()
	s.shutdownReason = strings.TrimSpace(reason)
	s.lifecycleMu.Unlock()
}

func (s *Server) getShutdownReason() string {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.shutdownReason == "" {
		return "signal"
	}
	return s.shutdownReason
}

func (s *Server) serverStarted(cfg *config.Config, addr string) {
	mode := strings.TrimSpace(cfg.Server.Mode)
	if mode == "" {
		mode = "production"
	}
	if s.logManager != nil {
		s.logManager.Server("info", "server started", "address", addr, "version", s.version, "mode", mode)
	}
	s.auditLog(nil, audit.Entry{
		Event:    "server.started",
		Severity: audit.SeverityInfo,
		Details:  map[string]any{"address": addr, "version": s.version, "mode": mode},
	})
	s.sendLifecycleEmail(cfg, "server_started", cfg.Server.Notifications.Email.Events.Startup, "")
}

func (s *Server) serverStopped(cfg *config.Config, reason string) {
	uptime := formatUptime(time.Since(s.startTime))
	if s.logManager != nil {
		s.logManager.Server("info", "server stopped", "reason", reason, "uptime", uptime)
	}
	s.auditLog(nil, audit.Entry{
		Event:    "server.stopped",
		Severity: audit.SeverityInfo,
		Details:  map[string]any{"reason": reason, "uptime": uptime},
	})
	s.sendLifecycleEmail(cfg, "server_stopped", cfg.Server.Notifications.Email.Events.Shutdown, reason)
}

func (s *Server) sendLifecycleEmail(cfg *config.Config, template string, enabled bool, reason string) {
	if !enabled {
		return
	}
	to := strings.TrimSpace(cfg.AdminEmail())
	mailer := email.New(&cfg.Server.Notifications.Email, cfg.Web.SiteTitle, lifecycleBaseURL(cfg), cfg.Server.FQDN, cfg.Server.Lang)
	if to == "" || !mailer.Enabled() {
		return
	}
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "unknown"
	}
	mode := strings.TrimSpace(cfg.Server.Mode)
	if mode == "" {
		mode = "production"
	}
	vars := map[string]string{
		"fqdn": cfg.Server.FQDN, "hostname": hostname, "version": s.version,
		"mode": mode, "timestamp": time.Now().UTC().Format(time.RFC3339),
	}
	if template == "server_stopped" {
		vars["reason"] = reason
	}
	done := make(chan error, 1)
	go func() { done <- mailer.Send(to, template, vars) }()
	timer := time.NewTimer(lifecycleEmailTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			log.Printf("lifecycle email %s: %v", template, err)
		}
	case <-timer.C:
		log.Printf("lifecycle email %s: send timed out after %s", template, lifecycleEmailTimeout)
	}
}

func lifecycleBaseURL(cfg *config.Config) string {
	if configured := strings.TrimSpace(cfg.Server.BaseURL); configured != "" {
		return strings.TrimRight(configured, "/")
	}
	proto := "http"
	if cfg.Server.TLS.Enabled || strings.TrimSpace(cfg.Server.Port) == "443" {
		proto = "https"
	}
	host := strings.TrimSpace(cfg.Server.FQDN)
	if host == "" {
		host = "localhost"
	}
	port := strings.TrimSpace(cfg.Server.Port)
	if port != "" && !strings.Contains(port, ",") && (proto != "http" || port != "80") && (proto != "https" || port != "443") {
		host += ":" + port
	}
	return fmt.Sprintf("%s://%s", proto, host)
}

func gracefulShutdown(ctx context.Context) bool { return ctx.Err() != nil }
