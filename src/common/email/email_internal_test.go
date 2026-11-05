package email

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/config"
)

// ─── parseTemplate ────────────────────────────────────────────────────────────

func TestParseTemplate_Valid(t *testing.T) {
	raw := "Subject: Hello World\n---\nThis is the body.\nSecond line."
	subject, body, ok := parseTemplate(raw)
	if !ok {
		t.Fatal("parseTemplate: expected ok")
	}
	if subject != "Hello World" {
		t.Errorf("subject = %q; want %q", subject, "Hello World")
	}
	if !strings.Contains(body, "This is the body.") {
		t.Errorf("body does not contain expected text: %q", body)
	}
}

func TestParseTemplate_MissingSubjectLine(t *testing.T) {
	raw := "From: someone@example.com\n---\nbody"
	_, _, ok := parseTemplate(raw)
	if ok {
		t.Error("expected ok=false for missing Subject: prefix")
	}
}

func TestParseTemplate_MissingSeparator(t *testing.T) {
	raw := "Subject: Hi\nNo separator here\nbody"
	_, _, ok := parseTemplate(raw)
	if ok {
		t.Error("expected ok=false when no --- separator")
	}
}

func TestParseTemplate_TooFewLines(t *testing.T) {
	raw := "Subject: Hi"
	_, _, ok := parseTemplate(raw)
	if ok {
		t.Error("expected ok=false for too few lines")
	}
}

func TestParseTemplate_SubjectTrimmed(t *testing.T) {
	raw := "Subject:   Spaced Subject   \n---\nbody"
	subject, _, ok := parseTemplate(raw)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if subject != "Spaced Subject" {
		t.Errorf("subject not trimmed: %q", subject)
	}
}

// ─── buildMessage ─────────────────────────────────────────────────────────────

func TestBuildMessage_Basic(t *testing.T) {
	msg := buildMessage("Sender", "from@example.com", "to@example.com", "Test Subject", "Hello body", "")
	s := string(msg)

	checks := []string{
		"From: Sender <from@example.com>",
		"To: to@example.com",
		"Subject: Test Subject",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"Hello body",
	}
	for _, want := range checks {
		if !strings.Contains(s, want) {
			t.Errorf("buildMessage: missing %q in output:\n%s", want, s)
		}
	}
}

func TestBuildMessage_WithReplyTo(t *testing.T) {
	msg := buildMessage("Name", "from@example.com", "to@example.com", "Subj", "Body", "reply@example.com")
	s := string(msg)
	if !strings.Contains(s, "Reply-To: reply@example.com") {
		t.Errorf("expected Reply-To header, got:\n%s", s)
	}
}

func TestBuildMessage_NoReplyTo(t *testing.T) {
	msg := buildMessage("Name", "from@example.com", "to@example.com", "Subj", "Body", "")
	if strings.Contains(string(msg), "Reply-To:") {
		t.Error("unexpected Reply-To header when replyTo is empty")
	}
}

// TestBuildMessage_HeaderInjection verifies CR/LF supplied in a header value
// (e.g. a researcher-controlled security-report summary folded into Subject)
// cannot inject additional headers or a forged body (CWE-93).
func TestBuildMessage_HeaderInjection(t *testing.T) {
	subject := "Legit\r\nBcc: evil@example.com\r\n\r\nInjected body"
	msg := string(buildMessage("N", "from@example.com", "to@example.com", subject, "Body", ""))
	headerBlock := msg
	if i := strings.Index(msg, "\r\n\r\n"); i >= 0 {
		headerBlock = msg[:i]
	}
	for _, line := range strings.Split(headerBlock, "\r\n") {
		if strings.HasPrefix(line, "Bcc:") {
			t.Errorf("CRLF injection produced a standalone Bcc header:\n%s", headerBlock)
		}
	}
	if strings.Count(headerBlock, "\r\nSubject:") > 1 || strings.Count(headerBlock, "Subject:") != 1 {
		t.Errorf("expected exactly one Subject header, got:\n%s", headerBlock)
	}
}

// TestBuildMultipartMessage_FilenameInjection verifies a quote or CR/LF in the
// attachment filename cannot break out of the Content-Disposition quoted string.
func TestBuildMultipartMessage_FilenameInjection(t *testing.T) {
	msg := string(buildMultipartMessage("N", "from@e.com", "to@e.com", "S", "B", "",
		"a\"\r\nX-Evil: 1.enc", []byte("data")))
	for _, line := range strings.Split(msg, "\r\n") {
		if strings.HasPrefix(line, "X-Evil:") {
			t.Errorf("filename injection produced a standalone X-Evil header:\n%s", msg)
		}
	}
	if !strings.Contains(msg, `filename="aX-Evil: 1.enc"`) {
		t.Errorf("expected injected chars folded into the quoted filename, got:\n%s", msg)
	}
}

// ─── buildMultipartMessage ────────────────────────────────────────────────────

func TestBuildMultipartMessage_Attachment(t *testing.T) {
	data := []byte("this is the encrypted report payload bytes")
	msg := buildMultipartMessage("Sender", "from@example.com", "to@example.com",
		"Encrypted Report", "See attached encrypted file.", "", "sec_abc123.enc", data)
	s := string(msg)

	checks := []string{
		"From: Sender <from@example.com>",
		"To: to@example.com",
		"Subject: Encrypted Report",
		"MIME-Version: 1.0",
		"Content-Type: multipart/mixed; boundary=",
		"Content-Type: text/plain; charset=utf-8",
		"See attached encrypted file.",
		"Content-Transfer-Encoding: base64",
		`Content-Disposition: attachment; filename="sec_abc123.enc"`,
	}
	for _, want := range checks {
		if !strings.Contains(s, want) {
			t.Errorf("buildMultipartMessage: missing %q in output:\n%s", want, s)
		}
	}

	// The attachment must be the base64 encoding of the payload, not the raw bytes.
	if strings.Contains(s, string(data)) {
		t.Error("buildMultipartMessage: raw payload leaked unencoded into message")
	}
	if !strings.Contains(s, base64.StdEncoding.EncodeToString(data)) {
		t.Errorf("buildMultipartMessage: base64 payload missing:\n%s", s)
	}
}

func TestBuildMultipartMessage_WithReplyTo(t *testing.T) {
	msg := buildMultipartMessage("Name", "from@example.com", "to@example.com",
		"Subj", "Body", "reply@example.com", "f.enc", []byte("x"))
	if !strings.Contains(string(msg), "Reply-To: reply@example.com") {
		t.Errorf("expected Reply-To header, got:\n%s", msg)
	}
}

// ─── renderTemplate ───────────────────────────────────────────────────────────

func TestRenderTemplate_EmbeddedTest(t *testing.T) {
	cfg := &config.EmailConfig{Enabled: true, SMTP: config.SMTPConfig{Host: "smtp.example.com"}}
	m := &Mailer{
		cfg:     cfg,
		appName: "TestApp",
		appURL:  "https://test.example.com",
		fqdn:    "test.example.com",
	}

	body, err := m.renderTemplate("test", map[string]string{"extra_key": "extra_value"})
	if err != nil {
		t.Fatalf("renderTemplate: %v", err)
	}
	if body == "" {
		t.Error("expected non-empty rendered template")
	}
	// Global variables should be substituted.
	if strings.Contains(body, "{app_name}") {
		t.Error("renderTemplate: {app_name} not substituted")
	}
	if !strings.Contains(body, "TestApp") {
		t.Errorf("renderTemplate: expected 'TestApp' in output, got:\n%s", body)
	}
}

func TestRenderTemplate_NonexistentTemplate(t *testing.T) {
	cfg := &config.EmailConfig{}
	m := &Mailer{cfg: cfg, appName: "App", appURL: "https://a.com", fqdn: "a.com"}

	_, err := m.renderTemplate("nonexistent_template_xyz", nil)
	if err == nil {
		t.Error("expected error for nonexistent template, got nil")
	}
}

func TestRenderTemplate_CustomDir(t *testing.T) {
	dir := t.TempDir()
	// Write a custom template that overrides the embedded default.
	content := "Subject: Custom Subject\n---\nCustom body for {app_name}"
	if err := os.WriteFile(dir+"/custom.txt", []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.EmailConfig{TemplateDir: dir}
	m := &Mailer{cfg: cfg, appName: "MyApp", appURL: "https://b.com", fqdn: "b.com"}

	body, err := m.renderTemplate("custom", nil)
	if err != nil {
		t.Fatalf("renderTemplate custom: %v", err)
	}
	if !strings.Contains(body, "MyApp") {
		t.Errorf("expected 'MyApp' in custom template output, got:\n%s", body)
	}
}

// TestRenderTemplate_OnionI2PReplyTo verifies the PART 17 global variables for
// Tor/I2P addresses and the notification reply-to are substituted, and resolve
// to empty strings when the hidden services are not configured.
func TestRenderTemplate_OnionI2PReplyTo(t *testing.T) {
	dir := t.TempDir()
	content := "Subject: S\n---\nonion={onion_url} addr={onion_address} i2p={i2p_url} i2paddr={i2p_address} reply={notification_reply_to}"
	if err := os.WriteFile(dir+"/vars.txt", []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.EmailConfig{TemplateDir: dir, ReplyTo: "noc@b.com"}
	m := &Mailer{cfg: cfg, appName: "App", appURL: "https://b.com", fqdn: "b.com"}

	// No hidden services set: onion/i2p resolve to empty, reply-to from config.
	body, err := m.renderTemplate("vars", nil)
	if err != nil {
		t.Fatalf("renderTemplate vars: %v", err)
	}
	if strings.Contains(body, "{onion_url}") || strings.Contains(body, "{i2p_url}") ||
		strings.Contains(body, "{onion_address}") || strings.Contains(body, "{i2p_address}") ||
		strings.Contains(body, "{notification_reply_to}") {
		t.Errorf("unsubstituted variable remained:\n%s", body)
	}
	if !strings.Contains(body, "reply=noc@b.com") {
		t.Errorf("expected reply-to substitution, got:\n%s", body)
	}

	// With hidden services set, URLs are populated.
	m.SetOnionAddress("abc123.onion")
	m.SetI2PAddress("xyz789.b32.i2p")
	body, err = m.renderTemplate("vars", nil)
	if err != nil {
		t.Fatalf("renderTemplate vars (with hidden services): %v", err)
	}
	if !strings.Contains(body, "onion=http://abc123.onion") || !strings.Contains(body, "addr=abc123.onion") {
		t.Errorf("expected onion substitution, got:\n%s", body)
	}
	if !strings.Contains(body, "i2p=http://xyz789.b32.i2p") || !strings.Contains(body, "i2paddr=xyz789.b32.i2p") {
		t.Errorf("expected i2p substitution, got:\n%s", body)
	}
}

// ─── defaultGatewayIP ─────────────────────────────────────────────────────────

func TestDefaultGatewayIP_DoesNotPanic(t *testing.T) {
	// defaultGatewayIP uses net.Dial("udp", ...) which does NOT send any network
	// packets — it just resolves a local address. Safe to call in any environment.
	got := defaultGatewayIP()
	// Result is either a valid IP string or empty — both are acceptable.
	if got != "" {
		// Validate it looks like an IP address.
		if !strings.Contains(got, ".") && !strings.Contains(got, ":") {
			t.Errorf("defaultGatewayIP returned non-IP string: %q", got)
		}
	}
}

// ─── globalIPv4 ───────────────────────────────────────────────────────────────

func TestGlobalIPv4_DoesNotPanic(t *testing.T) {
	// globalIPv4 enumerates network interfaces — safe in all environments.
	// We only verify it does not panic and returns a valid IPv4 or empty string.
	got := globalIPv4()
	if got != "" {
		parts := strings.Split(got, ".")
		if len(parts) != 4 {
			t.Errorf("globalIPv4 returned non-IPv4 string: %q", got)
		}
	}
}
