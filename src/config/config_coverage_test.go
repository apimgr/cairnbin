package config_test

// Targeted coverage tests for config.go paths not covered by existing tests.
// Focuses on: Load() needSave paths, ContactWebhook abuse role,
// SecurityReportEmail with ContactEmail, WebhookSecret, WebhookTargets.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apimgr/pastebin/src/config"
)

// tempCovPath returns a path inside a fresh temp directory that does not exist.
func tempCovPath(t *testing.T) string {
	t.Helper()
	base := filepath.Join(os.TempDir(), "apimgr")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "pastebin-cov-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "server.yml")
}

// ─── Load() needSave paths ────────────────────────────────────────────────────

func TestLoad_ExistingFile_MissingEncryptionKey_GeneratesOne(t *testing.T) {
	path := tempCovPath(t)
	cfg := config.DefaultConfig()
	cfg.Server.Security.EncryptionKey = ""
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Server.Security.EncryptionKey == "" {
		t.Error("Load should generate EncryptionKey for existing config with blank key")
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load reload: %v", err)
	}
	if reloaded.Server.Security.EncryptionKey != loaded.Server.Security.EncryptionKey {
		t.Error("generated EncryptionKey should be persisted to disk")
	}
}

func TestLoad_ExistingFile_MissingToken_GeneratesOne(t *testing.T) {
	path := tempCovPath(t)
	cfg := config.DefaultConfig()
	cfg.Server.Token = ""
	cfg.Server.Security.EncryptionKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Server.Token == "" {
		t.Error("Load should generate Server.Token for existing config with blank token")
	}
}

func TestLoad_ExistingFile_WithWebhookURL_GeneratesSecret(t *testing.T) {
	path := tempCovPath(t)
	cfg := config.DefaultConfig()
	cfg.Server.Contact.Admin.Webhooks = map[string]string{
		"slack": "https://hooks.slack.com/services/test",
	}
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	secret := loaded.WebhookSecret("admin", "slack")
	if secret == "" {
		t.Error("Load should generate webhook secret for configured webhook URL")
	}
	if len(secret) < 16 {
		t.Errorf("generated webhook secret looks too short: %q", secret)
	}
}

// ─── SecurityReportEmail ──────────────────────────────────────────────────────

func TestSecurityReportEmail_WithContactEmail(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Web.Security.ContactEmail = "sec-report@example.com"
	got := cfg.SecurityReportEmail()
	if got != "sec-report@example.com" {
		t.Errorf("SecurityReportEmail = %q, want sec-report@example.com", got)
	}
}

func TestSecurityReportEmail_FallsBackToSecurityEmail(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.FQDN = "paste.example.com"
	cfg.Server.Contact.Security.Email = "security@example.com"
	got := cfg.SecurityReportEmail()
	if got != "security@example.com" {
		t.Errorf("SecurityReportEmail fallback = %q, want security@example.com", got)
	}
}

// ─── ContactWebhook abuse role ────────────────────────────────────────────────

func TestContactWebhook_AbuseRole_AbuseSpecific(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Contact.Abuse.Webhooks = map[string]string{
		"slack": "https://hooks.slack.com/abuse",
	}
	cfg.Server.Contact.General.Webhooks = map[string]string{
		"slack": "https://hooks.slack.com/general",
	}
	got := cfg.ContactWebhook("abuse", "slack")
	if got != "https://hooks.slack.com/abuse" {
		t.Errorf("ContactWebhook(abuse, slack) = %q, want abuse-specific hook", got)
	}
}

func TestContactWebhook_AbuseRole_FallsToGeneral(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Contact.General.Webhooks = map[string]string{
		"slack": "https://hooks.slack.com/general",
	}
	got := cfg.ContactWebhook("abuse", "slack")
	if got != "https://hooks.slack.com/general" {
		t.Errorf("ContactWebhook(abuse) should fall back to general; got %q", got)
	}
}

func TestContactWebhook_AbuseRole_FallsToAdmin(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Contact.Admin.Webhooks = map[string]string{
		"slack": "https://hooks.slack.com/admin",
	}
	got := cfg.ContactWebhook("abuse", "slack")
	if got != "https://hooks.slack.com/admin" {
		t.Errorf("ContactWebhook(abuse) should fall back to admin; got %q", got)
	}
}

// ─── WebhookSecret ────────────────────────────────────────────────────────────

func TestWebhookSecret_RoleSpecific(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Contact.Security.Webhooks = map[string]string{
		"slack":        "https://hooks.slack.com/sec",
		"slack_secret": "mysecret123",
	}
	got := cfg.WebhookSecret("security", "slack")
	if got != "mysecret123" {
		t.Errorf("WebhookSecret(security, slack) = %q, want mysecret123", got)
	}
}

func TestWebhookSecret_AdminFallback(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Contact.Admin.Webhooks = map[string]string{
		"slack":        "https://hooks.slack.com/admin",
		"slack_secret": "adminsecret",
	}
	got := cfg.WebhookSecret("general", "slack")
	if got != "adminsecret" {
		t.Errorf("WebhookSecret(general, slack) fallback to admin = %q, want adminsecret", got)
	}
}

func TestWebhookSecret_NoWebhooks_Empty(t *testing.T) {
	cfg := config.DefaultConfig()
	got := cfg.WebhookSecret("security", "slack")
	if got != "" {
		t.Errorf("WebhookSecret with no webhooks = %q, want empty", got)
	}
}

func TestWebhookSecret_RoleHasURL_NoSecret(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Contact.Security.Webhooks = map[string]string{
		"slack": "https://hooks.slack.com/sec",
	}
	got := cfg.WebhookSecret("security", "slack")
	if got != "" {
		t.Errorf("WebhookSecret with URL but no secret = %q, want empty", got)
	}
}

// ─── WebhookTargets ───────────────────────────────────────────────────────────

func TestWebhookTargets_AbuseRole_IncludesGeneralAndAdmin(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Contact.Abuse.Webhooks = map[string]string{
		"slack": "https://hooks.slack.com/abuse",
	}
	cfg.Server.Contact.General.Webhooks = map[string]string{
		"discord": "https://discord.com/general",
	}
	cfg.Server.Contact.Admin.Webhooks = map[string]string{
		"teams": "https://teams.com/admin",
	}
	targets := cfg.WebhookTargets("abuse")
	if len(targets) < 2 {
		t.Errorf("WebhookTargets(abuse) = %d targets, want at least 2 (abuse+general+admin)", len(targets))
	}
	transports := map[string]bool{}
	for _, tgt := range targets {
		transports[tgt.Transport] = true
	}
	if !transports["slack"] {
		t.Error("WebhookTargets(abuse) should include slack from abuse role")
	}
}

func TestWebhookTargets_SecurityRole(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Contact.Security.Webhooks = map[string]string{
		"slack":        "https://hooks.slack.com/sec",
		"slack_secret": "seckey",
	}
	targets := cfg.WebhookTargets("security")
	if len(targets) == 0 {
		t.Fatal("WebhookTargets(security) should return at least 1 target")
	}
	found := false
	for _, tgt := range targets {
		if tgt.Transport == "slack" {
			found = true
			if tgt.Secret != "seckey" {
				t.Errorf("target secret = %q, want seckey", tgt.Secret)
			}
			if !strings.Contains(tgt.URL, "slack.com") {
				t.Errorf("target URL = %q, want slack URL", tgt.URL)
			}
		}
	}
	if !found {
		t.Error("slack transport not found in WebhookTargets(security)")
	}
}

func TestWebhookTargets_Empty(t *testing.T) {
	cfg := config.DefaultConfig()
	targets := cfg.WebhookTargets("security")
	if len(targets) != 0 {
		t.Errorf("WebhookTargets with no webhooks = %d, want 0", len(targets))
	}
}
