package server

import (
	"bufio"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/pgp"
)

type capturedSMTPMessage struct {
	recipient string
	body      string
}

func startSecurityReportSMTP(t *testing.T, rejectedRecipient string) (string, int, <-chan struct{}, <-chan capturedSMTPMessage) {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	attempts := make(chan struct{}, 8)
	messages := make(chan capturedSMTPMessage, 8)
	var handlers sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			attempts <- struct{}{}
			handlers.Add(1)
			go func() {
				defer handlers.Done()
				serveSecurityReportSMTP(conn, rejectedRecipient, messages)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
		handlers.Wait()
	})
	return "127.0.0.1", addr.Port, attempts, messages
}

func serveSecurityReportSMTP(conn net.Conn, rejectedRecipient string, messages chan<- capturedSMTPMessage) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	write := func(line string) bool {
		_, err := fmt.Fprintf(conn, "%s\r\n", line)
		return err == nil
	}
	if !write("220 localhost ESMTP") {
		return
	}
	recipient := ""
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.TrimSpace(line)
		upper := strings.ToUpper(command)
		switch {
		case strings.HasPrefix(upper, "EHLO "), strings.HasPrefix(upper, "HELO "):
			if !write("250 localhost") {
				return
			}
		case strings.HasPrefix(upper, "MAIL FROM:"):
			if !write("250 sender accepted") {
				return
			}
		case strings.HasPrefix(upper, "RCPT TO:"):
			recipient = strings.Trim(strings.TrimSpace(command[len("RCPT TO:"):]), "<>")
			if recipient == rejectedRecipient {
				if !write("550 recipient rejected") {
					return
				}
				continue
			}
			if !write("250 recipient accepted") {
				return
			}
		case upper == "DATA":
			if !write("354 end with dot") {
				return
			}
			var body strings.Builder
			for {
				dataLine, readErr := reader.ReadString('\n')
				if readErr != nil {
					return
				}
				dataLine = strings.TrimSuffix(strings.TrimSuffix(dataLine, "\n"), "\r")
				if dataLine == "." {
					break
				}
				if strings.HasPrefix(dataLine, "..") {
					dataLine = dataLine[1:]
				}
				body.WriteString(dataLine)
				body.WriteString("\r\n")
			}
			messages <- capturedSMTPMessage{recipient: recipient, body: body.String()}
			if !write("250 message accepted") {
				return
			}
		case upper == "QUIT":
			_ = write("221 closing connection")
			return
		default:
			if !write("250 ok") {
				return
			}
		}
	}
}

func configureSecurityReportSMTP(s *Server, host string, port int) {
	s.cfg.Server.Notifications.Email.Enabled = true
	s.cfg.Server.Notifications.Email.SMTP.Host = host
	s.cfg.Server.Notifications.Email.SMTP.Port = port
	s.cfg.Server.Notifications.Email.SMTP.TLS = "none"
	s.cfg.Server.Notifications.Email.From.Name = "Cairnbin Security"
	s.cfg.Server.Notifications.Email.From.Email = "sender@example.test"
	s.cfg.Web.Security.ContactEmail = "maintainer@example.test"
}

func sendTestSecurityReport(s *Server, researcherKey string) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	s.sendSecurityReportEmails(r, s.cfg, securityEmailCtx{
		trackingID:     "sec_tracking123",
		timestamp:      "2030-01-02T03:04:05Z",
		researcher:     "researcher@example.test",
		severity:       "High",
		summary:        "private vulnerability summary",
		component:      "api",
		disclosureDays: 90,
		encMethod:      "aes-256-gcm",
		statusToken:    "onetime-secret",
		sealed:         []byte("encrypted-report"),
		researcherGPG:  researcherKey,
	})
}

func TestSecurityReportEmailsSendOneMaintainerMessageAndMinimalAcknowledgment(t *testing.T) {
	s := newSecurityReportTestServer(t)
	host, port, attempts, messages := startSecurityReportSMTP(t, "")
	configureSecurityReportSMTP(s, host, port)
	sendTestSecurityReport(s, "")

	first := <-messages
	second := <-messages
	if first.recipient != "maintainer@example.test" || second.recipient != "researcher@example.test" {
		t.Fatalf("message recipients = %q, %q", first.recipient, second.recipient)
	}
	if !strings.Contains(first.body, `From: "Cairnbin Security" <sender@example.test>`) ||
		!strings.Contains(second.body, `From: "Cairnbin Security" <sender@example.test>`) {
		t.Fatalf("messages did not use configured sender:\n%s\n---\n%s", first.body, second.body)
	}
	ackBody, err := plainTextEmailBody(second.body)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sec_tracking123", "http://example.com/server/security/report/sec_tracking123?token=onetime-secret"} {
		if !strings.Contains(ackBody, want) {
			t.Errorf("acknowledgment missing %q: %s", want, ackBody)
		}
	}
	for _, forbidden := range []string{"private vulnerability summary", "2030-01-02", "Cairnbin (", "app_url"} {
		if strings.Contains(ackBody, forbidden) {
			t.Errorf("acknowledgment contains forbidden content %q: %s", forbidden, ackBody)
		}
	}
	assertSMTPAttempts(t, attempts, 2)
}

func plainTextEmailBody(raw string) (string, error) {
	message, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		return "", err
	}
	mediaType, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(mediaType, "multipart/") {
		return "", fmt.Errorf("email content type is %q, want multipart", mediaType)
	}
	reader := multipart.NewReader(message.Body, params["boundary"])
	for {
		part, partErr := reader.NextPart()
		if partErr == io.EOF {
			break
		}
		if partErr != nil {
			return "", partErr
		}
		partType, _, parseErr := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if parseErr != nil {
			return "", parseErr
		}
		body, readErr := io.ReadAll(part)
		_ = part.Close()
		if readErr != nil {
			return "", readErr
		}
		if partType == "text/plain" {
			return string(body), nil
		}
	}
	return "", fmt.Errorf("email has no text/plain part")
}

func TestSecurityReportEncryptedAcknowledgmentIsNotRetried(t *testing.T) {
	s := newSecurityReportTestServer(t)
	host, port, attempts, messages := startSecurityReportSMTP(t, "researcher@example.test")
	configureSecurityReportSMTP(s, host, port)
	key, err := pgp.Generate("Researcher", "researcher@example.test", time.Now(), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	sendTestSecurityReport(s, key.PublicArmored)

	if got := len(messages); got != 1 {
		t.Fatalf("successful deliveries = %d, want only maintainer notification", got)
	}
	assertSMTPAttempts(t, attempts, 2)
}

func assertSMTPAttempts(t *testing.T, attempts <-chan struct{}, want int) {
	t.Helper()
	for i := 0; i < want; i++ {
		select {
		case <-attempts:
		case <-time.After(time.Second):
			t.Fatalf("SMTP attempts = %d, want %d", i, want)
		}
	}
	select {
	case <-attempts:
		t.Fatalf("SMTP attempts exceeded %d", want)
	default:
	}
}
