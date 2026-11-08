package email

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
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
		`From: "Sender" <from@example.com>`,
		"To: <to@example.com>",
		"Subject: Test Subject",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
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
	if !strings.Contains(s, "Reply-To: <reply@example.com>") {
		t.Errorf("expected Reply-To header, got:\n%s", s)
	}
}

func TestBuildMessage_NoReplyTo(t *testing.T) {
	msg := buildMessage("Name", "from@example.com", "to@example.com", "Subj", "Body", "")
	if strings.Contains(string(msg), "Reply-To:") {
		t.Error("unexpected Reply-To header when replyTo is empty")
	}
}

// TestBuildRFCMessage_RejectsHeaderInjection verifies caller-controlled header
// values containing CR or LF are rejected before a message is generated.
func TestBuildRFCMessage_RejectsHeaderInjection(t *testing.T) {
	cases := []struct { name string; args []string }{
		{"from name", []string{"Name\r\nBcc: bad@example.com", "from@example.com", "to@example.com", "Subject", "Body", "", "example.com"}},
		{"from address", []string{"Name", "from@example.com\nBcc: bad@example.com", "to@example.com", "Subject", "Body", "", "example.com"}},
		{"recipient", []string{"Name", "from@example.com", "to@example.com\r\nBcc: bad@example.com", "Subject", "Body", "", "example.com"}},
		{"subject", []string{"Name", "from@example.com", "to@example.com", "Subject\r\nBcc: bad@example.com", "Body", "", "example.com"}},
		{"reply-to", []string{"Name", "from@example.com", "to@example.com", "Subject", "Body", "reply@example.com\nBcc: bad@example.com", "example.com"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildRFCMessage(tc.args[0], tc.args[1], tc.args[2], tc.args[3], tc.args[4], tc.args[5], tc.args[6]); err == nil {
				t.Fatal("expected header injection input to be rejected")
			}
		})
	}
}

func TestBuildRFCMessage_RequiredHeadersAndAlternativeParts(t *testing.T) {
	msg, err := buildRFCMessage("Cairnbín", "from@example.com", "to@example.com", "Résumé – report", "<script>alert('x')</script>", "reply@example.com", "example.com")
	if err != nil { t.Fatal(err) }
	parsed, err := mail.ReadMessage(bytes.NewReader(msg))
	if err != nil { t.Fatal(err) }
	for _, name := range []string{"From", "To", "Subject", "Date", "Message-Id", "MIME-Version", "Auto-Submitted"} {
		if parsed.Header.Get(name) == "" { t.Errorf("required %s header missing", name) }
	}
	if parsed.Header.Get("Auto-Submitted") != "auto-generated" { t.Errorf("Auto-Submitted = %q", parsed.Header.Get("Auto-Submitted")) }
	if !strings.Contains(parsed.Header.Get("From"), "=?UTF-8?b?") || !strings.Contains(parsed.Header.Get("Subject"), "=?UTF-8?b?") {
		t.Errorf("non-ASCII display name and subject must use RFC 2047 encoding: %#v", parsed.Header)
	}
	decodedSubject, err := (&mime.WordDecoder{}).DecodeHeader(parsed.Header.Get("Subject"))
	if err != nil || decodedSubject != "Résumé – report" {
		t.Errorf("RFC 2047 subject decode = %q, %v", decodedSubject, err)
	}
	fromAddress, err := mail.ParseAddress(parsed.Header.Get("From"))
	if err != nil || fromAddress.Name != "Cairnbín" {
		t.Errorf("RFC 2047 display name decode = %#v, %v", fromAddress, err)
	}
	if _, err := mail.ParseDate(parsed.Header.Get("Date")); err != nil { t.Errorf("Date is not RFC 5322 formatted: %v", err) }
	if !strings.HasPrefix(parsed.Header.Get("Message-Id"), "<") || !strings.HasSuffix(parsed.Header.Get("Message-Id"), "@example.com>") {
		t.Errorf("Message-ID has wrong format: %q", parsed.Header.Get("Message-Id"))
	}
	mediaType, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" { t.Fatalf("Content-Type = %q (%v)", parsed.Header.Get("Content-Type"), err) }
	reader := multipart.NewReader(parsed.Body, params["boundary"])
	plain, err := reader.NextPart()
	if err != nil { t.Fatal(err) }
	if plain.Header.Get("Content-Type") != "text/plain; charset=UTF-8" { t.Errorf("first part content type = %q", plain.Header.Get("Content-Type")) }
	if !strings.Contains(string(msg), "Content-Transfer-Encoding: quoted-printable") { t.Error("quoted-printable transfer encoding missing from raw MIME parts") }
	plainBody, err := io.ReadAll(plain)
	if err != nil || strings.TrimSpace(string(plainBody)) == "" { t.Errorf("plain-text part must be non-empty: body=%q err=%v", plainBody, err) }
	htmlPart, err := reader.NextPart()
	if err != nil { t.Fatal(err) }
	if htmlPart.Header.Get("Content-Type") != "text/html; charset=UTF-8" { t.Errorf("second part content type = %q", htmlPart.Header.Get("Content-Type")) }
	htmlBody, err := io.ReadAll(htmlPart)
	if err != nil { t.Fatal(err) }
	if !strings.Contains(string(htmlBody), "&lt;script&gt;") { t.Errorf("HTML part does not escape the message body: %q", htmlBody) }
	for _, line := range strings.Split(string(msg), "\r\n") {
		if len(line) > 998 { t.Errorf("RFC 5322 line exceeds 998 octets (%d): %q", len(line), line) }
	}
	second, err := buildRFCMessage("Cairnbín", "from@example.com", "to@example.com", "Résumé – report", "Body", "reply@example.com", "example.com")
	if err != nil { t.Fatal(err) }
	secondMessage, err := mail.ReadMessage(bytes.NewReader(second))
	if err != nil { t.Fatal(err) }
	if parsed.Header.Get("Message-Id") == secondMessage.Header.Get("Message-Id") { t.Error("Message-ID must be unique per message") }
}

func TestBuildRFCMessage_FoldsLongHeadersWithoutBreakingAddressTokens(t *testing.T) {
	longAddress := strings.Repeat("a", 64) + "@" + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 45) + ".com"
	longFQDN := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63)
	msg, err := buildRFCMessage(strings.Repeat("Résumé ", 12), "from@example.com", longAddress, strings.Repeat("long subject ", 12), "Body", "reply@example.com", longFQDN)
	if err != nil {
		t.Fatalf("buildRFCMessage with long legal values: %v", err)
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(msg))
	if err != nil {
		t.Fatalf("parse folded message: %v", err)
	}
	to, err := mail.ParseAddress(parsed.Header.Get("To"))
	if err != nil || to.Address != longAddress {
		t.Errorf("folded recipient changed address: %q, %v", to, err)
	}
	rawHeaders := strings.SplitN(string(msg), "\r\n\r\n", 2)[0]
	for _, line := range strings.Split(rawHeaders, "\r\n") {
		if len(line) > 78 && !strings.HasPrefix(line, "To: ") && !strings.HasPrefix(line, "Message-ID: ") {
			t.Errorf("foldable header line exceeds 78 octets (%d): %q", len(line), line)
		}
	}
	for _, line := range strings.Split(string(msg), "\r\n") {
		if len(line) > 998 {
			t.Errorf("RFC 5322 line exceeds 998 octets (%d)", len(line))
		}
	}
}

func TestBuildMultipartMessage_FilenameInjection(t *testing.T) {
	if _, err := buildMultipartRFCMessage("N", "from@e.com", "to@e.com", "S", "B", "", "a.enc\r\nX-Evil: 1", []byte("data"), "example.com"); err == nil {
		t.Fatal("expected CR/LF in the attachment filename to be rejected")
	}
}

// ─── buildMultipartMessage ────────────────────────────────────────────────────

func TestBuildMultipartMessage_Attachment(t *testing.T) {
	data := []byte("this is the encrypted report payload bytes")
	msg := buildMultipartMessage("Sender", "from@example.com", "to@example.com",
		"Encrypted Report", "See attached encrypted file.", "", "sec_abc123.enc", data)
	s := string(msg)
	parsed, err := mail.ReadMessage(bytes.NewReader(msg))
	if err != nil {
		t.Fatalf("parse multipart message: %v", err)
	}
	mediaType, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/mixed" || params["boundary"] == "" {
		t.Fatalf("top-level Content-Type = %q (%v)", parsed.Header.Get("Content-Type"), err)
	}

	checks := []string{
		`From: "Sender" <from@example.com>`,
		"To: <to@example.com>",
		"Subject: Encrypted Report",
		"Content-Type: text/plain; charset=UTF-8",
		"See attached encrypted file.",
		"Content-Transfer-Encoding: base64",
		"Content-Disposition: attachment; filename=sec_abc123.enc",
	}
	if parsed.Header.Get("MIME-Version") != "1.0" {
		t.Errorf("MIME-Version = %q", parsed.Header.Get("MIME-Version"))
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
	if !strings.Contains(string(msg), "Reply-To: <reply@example.com>") {
		t.Errorf("expected Reply-To header, got:\n%s", msg)
	}
}

// ─── renderTemplate ───────────────────────────────────────────────────────────

func TestRenderTemplate_EmbeddedTest(t *testing.T) {
	cfg := &config.EmailConfig{Enabled: true, SMTP: config.SMTPConfig{Host: "smtp.example.com"}}
	m := New(cfg, "TestApp", "https://test.example.com", "test.example.com")

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
	m := New(cfg, "App", "https://a.com", "a.com")

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
	m := New(cfg, "MyApp", "https://b.com", "b.com")

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
	m := New(cfg, "App", "https://b.com", "b.com")

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
