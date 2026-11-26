// email_test_cmd.go implements `{project_name} --maintenance email test` (AI.md PART 17):
// it validates templates locally or sends the "test" template to the
// configured reply-to address.
package maintenance

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/apimgr/cairnbin/src/common/email"
	"github.com/apimgr/cairnbin/src/common/i18n"
	"github.com/apimgr/cairnbin/src/config"
)

// EmailOptions carries the resolved paths an email command needs.
type EmailOptions struct {
	ConfigDir string
	DBPath    string
	LogDir    string
	// To overrides the recipient; empty uses server.notifications.email.reply_to.
	To     string
	DryRun bool
	Stdout io.Writer
	Lang   string
}

// TemplateValidationError marks dry-run failures that use the CLI's data-error
// exit status.
type TemplateValidationError struct{ Count int }

func (e *TemplateValidationError) Error() string {
	return fmt.Sprintf("email template validation failed with %d error(s)", e.Count)
}

// RunEmail executes `email <action>`. Only "test" exists.
func RunEmail(action string, opts EmailOptions) error {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	lang := i18n.GetLanguage(opts.Lang, "")
	if action != "test" {
		return errors.New(i18n.TranslateFormat(lang, "cli.email_unknown_action", i18n.Args("action", action)))
	}
	cfg, err := config.Load(filepath.Join(opts.ConfigDir, "server.yml"))
	if err != nil {
		return fmt.Errorf("email test: load config: %w", err)
	}
	mailer := email.New(&cfg.Server.Notifications.Email, cfg.Web.SiteTitle, cfg.StartupBaseURL(), cfg.ResolveFQDN(), cfg.Server.Lang)
	if opts.DryRun {
		issues := mailer.ValidateTemplates()
		for _, issue := range issues {
			_, _ = fmt.Fprintln(opts.Stdout, issue)
		}
		if len(issues) > 0 {
			return &TemplateValidationError{Count: len(issues)}
		}
		_, _ = fmt.Fprintln(opts.Stdout, i18n.TranslateFormat(lang, "cli.email_templates_valid", nil))
		return nil
	}
	if cfg.Server.Notifications.Email.SMTP.Host == "" {
		return errors.New(i18n.TranslateFormat(lang, "cli.email_smtp_required", nil))
	}
	to := strings.TrimSpace(opts.To)
	if to == "" {
		to = strings.TrimSpace(cfg.Server.Notifications.Email.ReplyTo)
	}
	if to == "" {
		return errors.New(i18n.TranslateFormat(lang, "cli.email_recipient_required", nil))
	}
	auth := AuthOptions{ConfigDir: opts.ConfigDir, DBPath: opts.DBPath, LogDir: opts.LogDir}
	cfg.Server.Notifications.Email.Enabled = true
	if err := mailer.TestSMTP(); err != nil {
		AuditMaintenanceEvent(auth, "email.test_failed", map[string]any{"stage": "connect"})
		return fmt.Errorf("%s: %w", i18n.TranslateFormat(lang, "cli.email_smtp_failed", nil), err)
	}
	subject, body, err := mailer.Render("test", nil)
	if err != nil {
		return err
	}
	if err := mailer.SendRawMessage(to, "[TEST] "+subject, body); err != nil {
		AuditMaintenanceEvent(auth, "email.test_failed", map[string]any{"stage": "send"})
		return fmt.Errorf("%s: %w", i18n.TranslateFormat(lang, "cli.email_send_failed", nil), err)
	}
	AuditMaintenanceEvent(auth, "email.test_sent", map[string]any{"recipient": to})
	_, _ = fmt.Fprintln(opts.Stdout, i18n.TranslateFormat(lang, "cli.email_test_sent", i18n.Args("recipient", to)))
	return nil
}
