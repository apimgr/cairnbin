package email

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/config"
)

// fakeSMTPServer binds on a random port and speaks just enough SMTP to let
// smtp.SendMail complete successfully: 220 greeting, 250 for EHLO/HELO/MAIL/RCPT,
// 354 for DATA, 250 after the dot, 221 for QUIT.
// Returns the listening address, a capture pointer, and a stop function.
func fakeSMTPServer(t *testing.T) (addr string, recorded *smtpCapture, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fakeSMTPServer: listen: %v", err)
	}

	cap := &smtpCapture{}
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				handleFakeSMTPConn(c, cap)
			}(conn)
		}
	}()

	return ln.Addr().String(), cap, func() {
		_ = ln.Close()
		wg.Wait()
	}
}

// smtpCapture records the envelope and message body received by the fake server.
type smtpCapture struct {
	mu   sync.Mutex
	from string
	rcpt []string
	body string
}

func (c *smtpCapture) record(from string, rcpt []string, body string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.from = from
	c.rcpt = rcpt
	c.body = body
}

// handleFakeSMTPConn processes a single SMTP connection with minimal protocol support.
func handleFakeSMTPConn(conn net.Conn, cap *smtpCapture) {
	defer func() { _ = conn.Close() }()

	var from string
	var rcpt []string
	var body strings.Builder

	send := func(line string) {
		_, _ = fmt.Fprintf(conn, "%s\r\n", line)
	}

	send("220 localhost ESMTP fake")
	scanner := bufio.NewScanner(conn)
	collectingData := false

	for scanner.Scan() {
		line := scanner.Text()

		if collectingData {
			if line == "." {
				collectingData = false
				cap.record(from, rcpt, body.String())
				send("250 OK message accepted")
				continue
			}
			// Strip leading dot-stuffing per RFC 5321.
			if strings.HasPrefix(line, "..") {
				line = line[1:]
			}
			body.WriteString(line)
			body.WriteString("\n")
			continue
		}

		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO") || strings.HasPrefix(upper, "HELO"):
			send("250-localhost Hello")
			send("250 OK")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			from = extractAngle(line)
			send("250 OK")
		case strings.HasPrefix(upper, "RCPT TO:"):
			rcpt = append(rcpt, extractAngle(line))
			send("250 OK")
		case upper == "DATA":
			send("354 Start mail input; end with <CRLF>.<CRLF>")
			collectingData = true
		case strings.HasPrefix(upper, "QUIT"):
			send("221 Bye")
			return
		case strings.HasPrefix(upper, "AUTH"):
			send("235 Authentication successful")
		default:
			send("500 Unknown command")
		}
	}
}

// extractAngle pulls the address out of "MAIL FROM:<addr>" or "RCPT TO:<addr>".
func extractAngle(line string) string {
	start := strings.Index(line, "<")
	end := strings.LastIndex(line, ">")
	if start < 0 || end <= start {
		fields := strings.Fields(line)
		if len(fields) > 1 {
			return fields[len(fields)-1]
		}
		return ""
	}
	return line[start+1 : end]
}

// splitFakeAddr parses "host:port" from a fake SMTP listener address and
// returns host, port (as int).
func splitFakeAddr(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("splitFakeAddr %q: %v", addr, err)
	}
	var port int
	if _, err := fmt.Sscan(portStr, &port); err != nil {
		t.Fatalf("splitFakeAddr parse port %q: %v", portStr, err)
	}
	return host, port
}

// ─── Enabled() / Send() early return ─────────────────────────────────────────

// TestSend_DisabledReturnsNil verifies that Send returns nil immediately when
// SMTP is not enabled — no network attempt is made.
func TestSend_DisabledReturnsNil(t *testing.T) {
	cfg := &config.EmailConfig{
		Enabled: false,
		SMTP:    config.SMTPConfig{Host: "smtp.example.com", Port: 587},
	}
	m := New(cfg, "App", "https://app.example.com", "app.example.com")

	if err := m.Send("user@example.com", "test", nil); err != nil {
		t.Errorf("Send disabled: want nil, got %v", err)
	}
}

// TestSend_NoHostReturnsNil verifies that Send returns nil immediately when
// SMTP host is empty (Enabled() is false even if Enabled flag is true).
func TestSend_NoHostReturnsNil(t *testing.T) {
	cfg := &config.EmailConfig{
		Enabled: true,
		SMTP:    config.SMTPConfig{Host: "", Port: 587},
	}
	m := New(cfg, "App", "https://app.example.com", "app.example.com")

	if err := m.Send("user@example.com", "test", nil); err != nil {
		t.Errorf("Send empty host: want nil, got %v", err)
	}
}

// TestSend_BadTemplateReturnsError verifies that Send propagates a render error
// when the template name does not exist.
func TestSend_BadTemplateReturnsError(t *testing.T) {
	cfg := &config.EmailConfig{
		Enabled: true,
		SMTP:    config.SMTPConfig{Host: "127.0.0.1", Port: 9},
	}
	m := New(cfg, "App", "https://app.example.com", "app.example.com")

	err := m.Send("user@example.com", "no_such_template_xyz", nil)
	if err == nil {
		t.Error("expected error for nonexistent template, got nil")
	}
	if !strings.Contains(err.Error(), "no_such_template_xyz") {
		t.Errorf("error does not mention template name: %v", err)
	}
}

// TestSend_MissingSubjectLineError verifies that Send returns a descriptive
// error when a custom template lacks the required Subject: header.
func TestSend_MissingSubjectLineError(t *testing.T) {
	dir := t.TempDir()
	content := "No subject line here\n---\nsome body"
	if err := os.WriteFile(dir+"/broken.txt", []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.EmailConfig{
		Enabled:     true,
		TemplateDir: dir,
		SMTP:        config.SMTPConfig{Host: "127.0.0.1", Port: 9},
	}
	m := New(cfg, "App", "https://app.com", "app.com")

	err := m.Send("user@example.com", "broken", nil)
	if err == nil {
		t.Fatal("expected error for template without Subject: line, got nil")
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Errorf("error should mention template name; got: %v", err)
	}
}

// ─── sendSMTP — "auto" / default TLS mode ────────────────────────────────────

// TestSendSMTP_AutoMode exercises the optional-STARTTLS auto path against the
// in-process fake SMTP server.
func TestSendSMTP_AutoMode(t *testing.T) {
	addr, cap, stop := fakeSMTPServer(t)
	defer stop()

	host, port := splitFakeAddr(t, addr)
	cfg := &config.EmailConfig{
		Enabled: true,
		SMTP: config.SMTPConfig{
			Host: host,
			Port: port,
			// Empty TLS → defaults to "auto" inside sendSMTP.
			TLS: "",
		},
	}
	m := New(cfg, "TestApp", "https://example.com", "example.com")

	msg := buildMessage("TestApp", "from@example.com", "to@example.com", "Hello", "Body text", "")
	if err := m.sendSMTP("from@example.com", "to@example.com", msg); err != nil {
		t.Fatalf("sendSMTP auto: %v", err)
	}

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.from != "from@example.com" {
		t.Errorf("MAIL FROM = %q; want from@example.com", cap.from)
	}
	if len(cap.rcpt) == 0 || cap.rcpt[0] != "to@example.com" {
		t.Errorf("RCPT TO = %v; want [to@example.com]", cap.rcpt)
	}
	if !strings.Contains(cap.body, "Body text") {
		t.Errorf("message body missing expected content; got:\n%s", cap.body)
	}
}

// TestSendSMTP_StarttlsModeRequiresExtension verifies that strict STARTTLS
// refuses a server that does not advertise the extension.
func TestSendSMTP_StarttlsModeRequiresExtension(t *testing.T) {
	addr, _, stop := fakeSMTPServer(t)
	defer stop()

	host, port := splitFakeAddr(t, addr)
	cfg := &config.EmailConfig{
		Enabled: true,
		SMTP: config.SMTPConfig{
			Host: host,
			Port: port,
			TLS:  "starttls",
		},
	}
	m := New(cfg, "App", "https://example.com", "example.com")

	msg := buildMessage("App", "from@example.com", "to@example.com", "Subj", "Body", "")
	if err := m.TestSMTP(); err == nil || !strings.Contains(err.Error(), "does not offer STARTTLS") {
		t.Fatalf("TestSMTP error = %v; want startup connection test to require STARTTLS", err)
	}
	if err := m.sendSMTP("from@example.com", "to@example.com", msg); err == nil || !strings.Contains(err.Error(), "does not offer STARTTLS") {
		t.Fatalf("sendSMTP starttls error = %v; want server-without-STARTTLS failure", err)
	}
}

// TestSendSMTP_TLSModeConnRefused verifies that the "tls" branch returns an
// error when the remote port is closed (TLS handshake failure / connection refused).
func TestSendSMTP_TLSModeConnRefused(t *testing.T) {
	cfg := &config.EmailConfig{
		Enabled: true,
		SMTP: config.SMTPConfig{
			// Port 1 is never open, guaranteeing a connection-refused error.
			Host: "127.0.0.1",
			Port: 1,
			TLS:  "tls",
		},
	}
	m := New(cfg, "App", "https://example.com", "example.com")

	msg := buildMessage("App", "from@example.com", "to@example.com", "Subj", "Body", "")
	err := m.sendSMTP("from@example.com", "to@example.com", msg)
	if err == nil {
		t.Error("expected error for TLS dial to closed port, got nil")
	}
}

// TestSendSMTP_AutoModeWithCredentials verifies credentials are never sent
// when auto mode connects to a server that does not establish TLS.
func TestSendSMTP_AutoModeWithCredentials(t *testing.T) {
	addr, _, stop := fakeSMTPServer(t)
	defer stop()

	host, port := splitFakeAddr(t, addr)
	cfg := &config.EmailConfig{
		Enabled: true,
		SMTP: config.SMTPConfig{
			Host:     host,
			Port:     port,
			TLS:      "auto",
			Username: "user",
			Password: "pass",
		},
	}
	m := New(cfg, "App", "https://example.com", "example.com")

	msg := buildMessage("App", "from@example.com", "to@example.com", "Subj", "Body", "")
	if err := m.sendSMTP("from@example.com", "to@example.com", msg); err == nil || !strings.Contains(err.Error(), "refusing AUTH without") {
		t.Fatalf("sendSMTP error = %v; want credentials refused without TLS", err)
	}
}

// ─── sendViaClient — direct smtp.Client ──────────────────────────────────────

// TestSendViaClient_Success verifies the full sendViaClient flow (MAIL, RCPT,
// DATA, write, Close) against the in-process fake SMTP server.
func TestSendViaClient_Success(t *testing.T) {
	addr, cap, stop := fakeSMTPServer(t)
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial fake SMTP: %v", err)
	}
	c, err := smtp.NewClient(conn, "127.0.0.1")
	if err != nil {
		t.Fatalf("smtp.NewClient: %v", err)
	}
	defer func() { _ = c.Close() }()

	msg := []byte("From: a@example.com\r\nTo: b@example.com\r\nSubject: Hi\r\n\r\nHello world")
	if err := sendViaClient(c, "a@example.com", "b@example.com", msg); err != nil {
		t.Fatalf("sendViaClient: %v", err)
	}

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.from != "a@example.com" {
		t.Errorf("from = %q; want a@example.com", cap.from)
	}
	if len(cap.rcpt) == 0 || cap.rcpt[0] != "b@example.com" {
		t.Errorf("rcpt = %v; want [b@example.com]", cap.rcpt)
	}
	if !strings.Contains(cap.body, "Hello world") {
		t.Errorf("body missing expected content; got:\n%s", cap.body)
	}
}

// TestSendViaClient_MailError verifies that sendViaClient propagates a MAIL FROM
// error when the underlying connection has been closed before the call.
func TestSendViaClient_MailError(t *testing.T) {
	addr, _, stop := fakeSMTPServer(t)
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial fake SMTP: %v", err)
	}
	c, err := smtp.NewClient(conn, "127.0.0.1")
	if err != nil {
		t.Fatalf("smtp.NewClient: %v", err)
	}

	// Closing conn forces a broken-pipe / EOF on the next SMTP command.
	_ = conn.Close()

	err = sendViaClient(c, "a@example.com", "b@example.com", []byte("msg"))
	if err == nil {
		t.Error("expected error from sendViaClient after conn closed, got nil")
	}
}

// ─── Send() end-to-end via fake SMTP ─────────────────────────────────────────

// TestSend_EndToEnd exercises the complete Send() pipeline: template render →
// parseTemplate → buildMessage → sendSMTP against the in-process fake SMTP server.
func TestSend_EndToEnd(t *testing.T) {
	addr, cap, stop := fakeSMTPServer(t)
	defer stop()

	host, port := splitFakeAddr(t, addr)
	cfg := &config.EmailConfig{
		Enabled: true,
		SMTP: config.SMTPConfig{
			Host: host,
			Port: port,
			TLS:  "",
		},
		From: config.EmailFrom{Name: "CairnbinApp", Email: "noreply@example.com"},
	}
	m := New(cfg, "CairnbinApp", "https://example.com", "example.com")

	if err := m.Send("dest@example.com", "test", map[string]string{"extra_key": "hello"}); err != nil {
		t.Fatalf("Send end-to-end: %v", err)
	}

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.from != "noreply@example.com" {
		t.Errorf("envelope from = %q; want noreply@example.com", cap.from)
	}
	if len(cap.rcpt) == 0 || cap.rcpt[0] != "dest@example.com" {
		t.Errorf("rcpt = %v; want [dest@example.com]", cap.rcpt)
	}
	if !strings.Contains(cap.body, "CairnbinApp") {
		t.Errorf("body missing app name; got:\n%s", cap.body)
	}
}

// TestSend_DefaultFromAddress verifies that Send uses "no-reply@{fqdn}" when
// no From.Email is configured.
func TestSend_DefaultFromAddress(t *testing.T) {
	addr, cap, stop := fakeSMTPServer(t)
	defer stop()

	host, port := splitFakeAddr(t, addr)
	cfg := &config.EmailConfig{
		Enabled: true,
		SMTP:    config.SMTPConfig{Host: host, Port: port},
		From:    config.EmailFrom{},
	}
	m := New(cfg, "MyApp", "https://myapp.example.com", "myapp.example.com")

	if err := m.Send("user@example.com", "test", nil); err != nil {
		t.Fatalf("Send with default from: %v", err)
	}

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.from != "no-reply@myapp.example.com" {
		t.Errorf("default from = %q; want no-reply@myapp.example.com", cap.from)
	}
}

// TestSend_DefaultFromName verifies that Send uses the appName when From.Name
// is empty, visible in the From header of the captured message.
func TestSend_DefaultFromName(t *testing.T) {
	addr, cap, stop := fakeSMTPServer(t)
	defer stop()

	host, port := splitFakeAddr(t, addr)
	cfg := &config.EmailConfig{
		Enabled: true,
		SMTP:    config.SMTPConfig{Host: host, Port: port},
		From:    config.EmailFrom{Email: "from@example.com"},
	}
	m := New(cfg, "DefaultNameApp", "https://example.com", "example.com")

	if err := m.Send("user@example.com", "test", nil); err != nil {
		t.Fatalf("Send with default from name: %v", err)
	}

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if !strings.Contains(cap.body, "DefaultNameApp") {
		t.Errorf("body should contain default app name; got:\n%s", cap.body)
	}
}

func TestSMTPPrivateCertVerificationSkipped(t *testing.T) {
	cases := []struct {
		host string
		mode string
		want bool
	}{
		{"10.1.2.3", "tls", true},
		{"172.31.1.2", "starttls", true},
		{"192.168.1.2", "auto", true},
		{"100.64.0.1", "tls", true},
		{"127.0.0.1", "tls", true},
		{"169.254.1.1", "tls", true},
		{"fc00::1", "tls", true},
		{"fe80::1", "tls", true},
		{"8.8.8.8", "tls", false},
		{"smtp.example.com", "tls", false},
		{"10.1.2.3", "none", false},
	}
	for _, tc := range cases {
		t.Run(tc.host+"/"+tc.mode, func(t *testing.T) {
			if got := SMTPPrivateCertVerificationSkipped(tc.host, tc.mode); got != tc.want {
				t.Errorf("SMTPPrivateCertVerificationSkipped(%q, %q) = %t, want %t", tc.host, tc.mode, got, tc.want)
			}
		})
	}
}

func TestSendSMTP_NoneMode(t *testing.T) {
	addr, cap, stop := fakeSMTPServer(t)
	defer stop()
	host, port := splitFakeAddr(t, addr)
	m := New(&config.EmailConfig{
		Enabled: true,
		SMTP:    config.SMTPConfig{Host: host, Port: port, TLS: "none"},
	}, "App", "https://example.com", "example.com")
	if err := m.sendSMTP("from@example.com", "to@example.com", buildMessage("App", "from@example.com", "to@example.com", "Subject", "Body", "")); err != nil {
		t.Fatalf("sendSMTP none: %v", err)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.from != "from@example.com" {
		t.Errorf("MAIL FROM = %q", cap.from)
	}
}

func TestSMTPPrivateIPTLSAcceptsUntrustedRelayCertificate(t *testing.T) {
	tlsSource := httptest.NewTLSServer(http.NotFoundHandler())
	certificate := tlsSource.TLS.Certificates[0]
	tlsSource.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer func() { _ = conn.Close() }()
		tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{certificate}})
		if err := tlsConn.Handshake(); err != nil {
			serverErr <- err
			return
		}
		_, _ = fmt.Fprint(tlsConn, "220 localhost ESMTP fake\r\n")
		reader := bufio.NewReader(tlsConn)
		if _, err := reader.ReadString('\n'); err != nil {
			serverErr <- err
			return
		}
		_, _ = fmt.Fprint(tlsConn, "250 localhost\r\n")
		if _, err := reader.ReadString('\n'); err != nil {
			serverErr <- err
			return
		}
		_, _ = fmt.Fprint(tlsConn, "221 closing\r\n")
		serverErr <- nil
	}()
	host, port := splitFakeAddr(t, listener.Addr().String())
	client, conn, established, err := connectSMTP(host, port, "tls", time.Second)
	if err != nil {
		t.Fatalf("private SMTP relay with self-signed cert should connect: %v", err)
	}
	if !established {
		t.Fatal("TLS must be established")
	}
	if err := client.Quit(); err != nil {
		t.Errorf("close fake SMTP session: %v", err)
	}
	_ = conn.Close()
	if err := <-serverErr; err != nil {
		t.Errorf("fake TLS relay: %v", err)
	}
	if smtpTLSConfig("8.8.8.8").InsecureSkipVerify {
		t.Error("public IP certificate verification must remain enabled")
	}
}
