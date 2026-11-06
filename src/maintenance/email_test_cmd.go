// email_test_cmd.go implements `{project_name} email test` (AI.md PART 17):
// it proves the configured SMTP server actually delivers by sending the
// embedded (or customised) "test" template to the admin contact.
package maintenance

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/apimgr/cairnbin/src/common/email"
	"github.com/apimgr/cairnbin/src/config"
)

// EmailOptions carries the resolved paths an email command needs.
type EmailOptions struct {
	ConfigDir string
	DBPath    string
	LogDir    string
	// To overrides the recipient; empty means the admin contact.
	To     string
	Stdout io.Writer
}

// RunEmail executes `email <action>`. Only "test" exists.
func RunEmail(action string, opts EmailOptions) error {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if action != "test" {
		return fmt.Errorf("email: unknown action %q (want test)", action)
	}
	cfg, _ := config.Load(filepath.Join(opts.ConfigDir, "server.yml"))
	mailer := email.New(&cfg.Server.Notifications.Email, cfg.Web.SiteTitle, cfg.StartupBaseURL(), cfg.Server.FQDN)
	if !mailer.Enabled() {
		return errors.New("email features require SMTP configuration (server.notifications.email.smtp.host)")
	}
	to := strings.TrimSpace(opts.To)
	if to == "" {
		to = strings.TrimSpace(cfg.AdminEmail())
	}
	if to == "" {
		return errors.New("email test: no recipient (set server.contact.admin.email)")
	}
	auth := AuthOptions{ConfigDir: opts.ConfigDir, DBPath: opts.DBPath, LogDir: opts.LogDir}
	if err := mailer.TestSMTP(); err != nil {
		AuditMaintenanceEvent(auth, "email.test_failed", map[string]any{"stage": "connect"})
		return fmt.Errorf("email test: SMTP connection failed: %w", err)
	}
	subject, body, err := mailer.Render("test", nil)
	if err != nil {
		return err
	}
	if err := mailer.SendRawMessage(to, "[TEST] "+subject, body); err != nil {
		AuditMaintenanceEvent(auth, "email.test_failed", map[string]any{"stage": "send"})
		return fmt.Errorf("email test: send failed: %w", err)
	}
	AuditMaintenanceEvent(auth, "email.test_sent", map[string]any{"recipient": to})
	_, _ = fmt.Fprintf(opts.Stdout, "test email sent to %s\n", to)
	return nil
}
