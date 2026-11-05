package email_test

import (
	"net"
	"os"
	"testing"

	"github.com/apimgr/cairnbin/src/common/email"
	"github.com/apimgr/cairnbin/src/config"
)

// ─── New + Enabled ────────────────────────────────────────────────────────────

func TestNew_ReturnsMailer(t *testing.T) {
	cfg := &config.EmailConfig{Enabled: true, SMTP: config.SMTPConfig{Host: "smtp.example.com", Port: 587}}
	m := email.New(cfg, "cairnbin", "https://example.com", "example.com")
	if m == nil {
		t.Fatal("New returned nil")
	}
}

func TestEnabled_True(t *testing.T) {
	cfg := &config.EmailConfig{Enabled: true, SMTP: config.SMTPConfig{Host: "smtp.example.com", Port: 587}}
	m := email.New(cfg, "cairnbin", "https://example.com", "example.com")
	if !m.Enabled() {
		t.Error("expected Enabled()=true when SMTP host is set and enabled=true")
	}
}

func TestEnabled_FalseWhenDisabled(t *testing.T) {
	cfg := &config.EmailConfig{Enabled: false, SMTP: config.SMTPConfig{Host: "smtp.example.com"}}
	m := email.New(cfg, "cairnbin", "https://example.com", "example.com")
	if m.Enabled() {
		t.Error("expected Enabled()=false when Enabled=false")
	}
}

func TestEnabled_FalseWhenNoHost(t *testing.T) {
	cfg := &config.EmailConfig{Enabled: true, SMTP: config.SMTPConfig{Host: ""}}
	m := email.New(cfg, "cairnbin", "https://example.com", "example.com")
	if m.Enabled() {
		t.Error("expected Enabled()=false when SMTP host is empty")
	}
}

// ─── Send when disabled ───────────────────────────────────────────────────────

func TestSend_Disabled_ReturnsNil(t *testing.T) {
	cfg := &config.EmailConfig{Enabled: false}
	m := email.New(cfg, "cairnbin", "https://example.com", "example.com")

	if err := m.Send("user@example.com", "paste_created", map[string]string{
		"paste_id": "abc123",
	}); err != nil {
		t.Errorf("Send when disabled: expected nil, got %v", err)
	}
}

func TestSend_NoHost_ReturnsNil(t *testing.T) {
	cfg := &config.EmailConfig{Enabled: true, SMTP: config.SMTPConfig{Host: ""}}
	m := email.New(cfg, "cairnbin", "https://example.com", "example.com")

	if err := m.Send("user@example.com", "paste_created", nil); err != nil {
		t.Errorf("Send with no SMTP host: expected nil, got %v", err)
	}
}

// ─── TestSMTP when no host ────────────────────────────────────────────────────

func TestTestSMTP_NoHost_ReturnsError(t *testing.T) {
	cfg := &config.EmailConfig{Enabled: false, SMTP: config.SMTPConfig{Host: ""}}
	m := email.New(cfg, "cairnbin", "https://example.com", "example.com")

	if err := m.TestSMTP(); err == nil {
		t.Error("TestSMTP with no host: expected error, got nil")
	}
}

// ─── AutoDetect ───────────────────────────────────────────────────────────────

func TestAutoDetect_NoCandidatesReachable(t *testing.T) {
	// AutoDetect probes 127.0.0.1:587/465/25 and other candidates.
	// In a test environment these ports are typically closed, so ok should be false.
	// We cannot guarantee ports are closed, so we only verify it does not panic.
	_, _, _ = email.AutoDetect("test.example.invalid")
}

func TestAutoDetect_EmptyFQDN(t *testing.T) {
	// With empty FQDN the fqdn-based candidates are skipped — still must not panic.
	_, _, _ = email.AutoDetect("")
}

// ─── TestSMTP with a real listener ────────────────────────────────────────────

func TestTestSMTP_WithListeningServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("could not open local listener: %v", err)
	}
	defer ln.Close()

	// TestSMTP performs a real EHLO handshake, so the fake server must speak
	// just enough SMTP to complete it: a 220 greeting, then a 250 reply to EHLO.
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("220 localhost ESMTP\r\n"))
		buf := make([]byte, 512)
		if _, err := conn.Read(buf); err != nil {
			return
		}
		_, _ = conn.Write([]byte("250 localhost\r\n"))
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	cfg := &config.EmailConfig{
		Enabled: true,
		SMTP:    config.SMTPConfig{Host: "127.0.0.1", Port: port},
	}
	m := email.New(cfg, "cairnbin", "https://example.com", "example.com")
	// TestSMTP connects and completes an EHLO handshake against the fake server above.
	if err := m.TestSMTP(); err != nil {
		t.Errorf("TestSMTP with listening server: %v", err)
	}
}

func TestTestSMTP_UnreachableHost_ReturnsError(t *testing.T) {
	cfg := &config.EmailConfig{
		Enabled: true,
		SMTP:    config.SMTPConfig{Host: "127.0.0.1", Port: 1},
	}
	m := email.New(cfg, "cairnbin", "https://example.com", "example.com")
	if err := m.TestSMTP(); err == nil {
		t.Error("expected error for unreachable SMTP host, got nil")
	}
}

// ─── Send — enabled + error paths ────────────────────────────────────────────

func TestSend_TemplateNotFound_ReturnsError(t *testing.T) {
	cfg := &config.EmailConfig{
		Enabled: true,
		SMTP:    config.SMTPConfig{Host: "smtp.example.com", Port: 587},
	}
	m := email.New(cfg, "cairnbin", "https://example.com", "example.com")
	err := m.Send("user@example.com", "nonexistent_xyz_template", nil)
	if err == nil {
		t.Error("expected error for nonexistent template, got nil")
	}
}

func TestSend_TemplateMissingSubject_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	// Template file without a "Subject:" prefix line.
	if err := os.WriteFile(dir+"/no_subject.txt", []byte("---\nBody without subject"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.EmailConfig{
		Enabled:     true,
		SMTP:        config.SMTPConfig{Host: "smtp.example.com", Port: 587},
		TemplateDir: dir,
	}
	m := email.New(cfg, "cairnbin", "https://example.com", "example.com")
	err := m.Send("user@example.com", "no_subject", nil)
	if err == nil {
		t.Error("expected error for template missing Subject:, got nil")
	}
}
