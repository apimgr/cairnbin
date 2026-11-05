package server

import (
	"time"

	"github.com/apimgr/cairnbin/src/config"
	"github.com/apimgr/cairnbin/src/notify"
)

// notifyRole resolves every configured webhook destination for a role and
// dispatches a single message to all of them in the background (PART 17). It is
// a no-op when the dispatcher is unset or the role has no webhooks configured.
func (s *Server) notifyRole(cfg *config.Config, role, event, subject, body, severity string) {
	if s.notifier == nil || cfg == nil {
		return
	}
	resolved := cfg.WebhookTargets(role)
	if len(resolved) == 0 {
		return
	}
	targets := make([]notify.Target, 0, len(resolved))
	for _, t := range resolved {
		targets = append(targets, notify.Target{Transport: t.Transport, URL: t.URL, Secret: t.Secret})
	}
	s.notifier.Dispatch(notify.Message{
		Role:      role,
		Event:     event,
		Subject:   subject,
		Body:      body,
		Severity:  severity,
		Timestamp: time.Now(),
	}, targets...)
}
