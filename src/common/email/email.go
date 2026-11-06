// Package email handles outbound SMTP notifications for the cairnbin server.
// When no SMTP server is configured or reachable, all email features are silently
// disabled — no queuing, no retry, no "would have sent" logging.
package email

import (
	"bytes"
	"crypto/tls"
	"embed"
	"encoding/base64"
	"fmt"
	"log"
	"mime/multipart"
	"net"
	"net/smtp"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/apimgr/cairnbin/src/config"
)

//go:embed templates/*.txt
var defaultTemplatesFS embed.FS

// Mailer sends notification emails using configured SMTP settings.
type Mailer struct {
	cfg     *config.EmailConfig
	appName string
	appURL  string
	fqdn    string
	// onionAddress and i2pAddress are set at runtime once the corresponding
	// hidden service is available (PART 17 global template variables).
	onionAddress string
	i2pAddress   string
}

// New creates a Mailer from the provided email config, app name, and base URL.
func New(cfg *config.EmailConfig, appName, appURL, fqdn string) *Mailer {
	return &Mailer{
		cfg:     cfg,
		appName: appName,
		appURL:  appURL,
		fqdn:    fqdn,
	}
}

// SetOnionAddress records the Tor .onion address used to populate the
// {onion_url} and {onion_address} template variables. Pass "" to clear it.
func (m *Mailer) SetOnionAddress(addr string) { m.onionAddress = addr }

// SetI2PAddress records the I2P address used to populate the {i2p_url} and
// {i2p_address} template variables. Pass "" to clear it.
func (m *Mailer) SetI2PAddress(addr string) { m.i2pAddress = addr }

// Enabled returns true if SMTP is configured and email sending is enabled.
func (m *Mailer) Enabled() bool {
	return m.cfg.Enabled && m.cfg.SMTP.Host != ""
}

// Send renders template tmplName with the provided variables and sends the email.
// If SMTP is not configured, it returns nil silently without attempting delivery.
func (m *Mailer) Send(to, tmplName string, vars map[string]string) error {
	if !m.Enabled() {
		return nil
	}

	body, err := m.renderTemplate(tmplName, vars)
	if err != nil {
		return fmt.Errorf("email: render %s: %w", tmplName, err)
	}

	subject, bodyText, ok := parseTemplate(body)
	if !ok {
		return fmt.Errorf("email: template %s missing Subject: line", tmplName)
	}

	fromName, fromAddr := m.resolveFrom()
	msg := buildMessage(fromName, fromAddr, to, subject, bodyText, m.cfg.ReplyTo)
	return m.sendSMTP(fromAddr, to, msg)
}

// Render renders template tmplName with the provided variables and returns its
// parsed subject and body without sending. Used when the caller must transform the
// body before delivery — e.g. PGP-encrypting a researcher acknowledgment before
// handing it to SendRawMessage (AI.md 14458).
func (m *Mailer) Render(tmplName string, vars map[string]string) (subject, body string, err error) {
	raw, err := m.renderTemplate(tmplName, vars)
	if err != nil {
		return "", "", fmt.Errorf("email: render %s: %w", tmplName, err)
	}
	subject, body, ok := parseTemplate(raw)
	if !ok {
		return "", "", fmt.Errorf("email: template %s missing Subject: line", tmplName)
	}
	return subject, body, nil
}

// resolveFrom returns the From display name and address, falling back to the app
// name and no-reply@{fqdn} when the config leaves either unset.
func (m *Mailer) resolveFrom() (name, addr string) {
	addr = m.cfg.From.Email
	if addr == "" {
		addr = "no-reply@" + m.fqdn
	}
	name = m.cfg.From.Name
	if name == "" {
		name = m.appName
	}
	return name, addr
}

// SendRawMessage sends a plain-text email whose subject and body are assembled by
// the caller, bypassing the template system. Used to deliver a message whose body
// is an inline PGP-encrypted payload (AI.md 14457). Silent no-op when SMTP is off.
func (m *Mailer) SendRawMessage(to, subject, body string) error {
	if !m.Enabled() {
		return nil
	}
	fromName, fromAddr := m.resolveFrom()
	msg := buildMessage(fromName, fromAddr, to, subject, body, m.cfg.ReplyTo)
	return m.sendSMTP(fromAddr, to, msg)
}

// SendWithAttachment sends a plain-text email with a single binary attachment
// encoded as base64 in a multipart/mixed message. Used to deliver an AES-encrypted
// security report when no PGP key is configured (AI.md 14457). Silent no-op when
// SMTP is off.
func (m *Mailer) SendWithAttachment(to, subject, body, filename string, data []byte) error {
	if !m.Enabled() {
		return nil
	}
	fromName, fromAddr := m.resolveFrom()
	msg := buildMultipartMessage(fromName, fromAddr, to, subject, body, m.cfg.ReplyTo, filename, data)
	return m.sendSMTP(fromAddr, to, msg)
}

// TestSMTP attempts an SMTP EHLO handshake to verify connectivity.
// Returns nil when the connection succeeds, an error otherwise.
func (m *Mailer) TestSMTP() error {
	if m.cfg.SMTP.Host == "" {
		return fmt.Errorf("smtp: no host configured")
	}
	return smtpHandshake(m.cfg.SMTP.Host, m.cfg.SMTP.Port, 5*time.Second)
}

// smtpHandshake dials host:port and performs an SMTP EHLO/HELO handshake,
// per PART 17's "Attempt SMTP handshake (EHLO)" requirement for both
// auto-detection and startup connection tests. The connection is closed
// with QUIT after a successful handshake.
func smtpHandshake(host string, port int, timeout time.Duration) error {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return fmt.Errorf("smtp: connect %s: %w", addr, err)
	}
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp: set deadline %s: %w", addr, err)
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp: handshake %s: %w", addr, err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Hello("localhost"); err != nil {
		return fmt.Errorf("smtp: EHLO %s: %w", addr, err)
	}
	return nil
}

// smtpPorts are the ports probed at every candidate host, in the exact order
// required by PART 17 § "Auto-Detection Process": 25, 465, 587.
var smtpPorts = []int{25, 465, 587}

// addHostCandidates appends host paired with each of smtpPorts, in port order.
func addHostCandidates(candidates []struct {
	host string
	port int
}, host string) []struct {
	host string
	port int
} {
	for _, p := range smtpPorts {
		candidates = append(candidates, struct {
			host string
			port int
		}{host, p})
	}
	return candidates
}

// AutoDetect probes common SMTP host/port combinations in priority order and
// returns the first reachable config. Returns false if none are reachable.
func AutoDetect(fqdn string) (host string, port int, ok bool) {
	gatewayIP := defaultGatewayIP()
	globalIP := globalIPv4()

	var candidates []struct {
		host string
		port int
	}
	candidates = addHostCandidates(candidates, "127.0.0.1")
	candidates = addHostCandidates(candidates, "172.17.0.1")
	if gatewayIP != "" {
		candidates = addHostCandidates(candidates, gatewayIP)
	}
	// Bare FQDN is probed before the global IP (PART 17 priority order).
	if fqdn != "" && fqdn != "localhost" {
		candidates = addHostCandidates(candidates, fqdn)
	}
	if globalIP != "" {
		candidates = addHostCandidates(candidates, globalIP)
	}
	// mail.fqdn and smtp.fqdn are tried last, after the global IP.
	if fqdn != "" && fqdn != "localhost" {
		for _, prefix := range []string{"mail.", "smtp."} {
			candidates = addHostCandidates(candidates, prefix+fqdn)
		}
	}

	for _, c := range candidates {
		if err := smtpHandshake(c.host, c.port, 2*time.Second); err != nil {
			continue
		}
		log.Printf("[email] SMTP auto-detected: %s:%d", c.host, c.port)
		return c.host, c.port, true
	}
	return "", 0, false
}

// renderTemplate loads the named template (custom override or embedded default),
// substitutes global and caller-provided variables, and returns the full text.
func (m *Mailer) renderTemplate(name string, vars map[string]string) (string, error) {
	raw, err := m.loadTemplate(name)
	if err != nil {
		return "", err
	}

	global := map[string]string{
		"app_name":  m.appName,
		"app_url":   m.appURL,
		"fqdn":      m.fqdn,
		"timestamp": time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
		"year":      time.Now().UTC().Format("2006"),
	}

	// Tor / I2P address variables are only populated when the hidden service
	// is active; otherwise they resolve to empty strings (PART 17).
	if m.onionAddress != "" {
		global["onion_address"] = m.onionAddress
		global["onion_url"] = "http://" + m.onionAddress
	} else {
		global["onion_address"] = ""
		global["onion_url"] = ""
	}
	if m.i2pAddress != "" {
		global["i2p_address"] = m.i2pAddress
		global["i2p_url"] = "http://" + m.i2pAddress
	} else {
		global["i2p_address"] = ""
		global["i2p_url"] = ""
	}

	// notification_reply_to comes from server.notifications.email.reply_to.
	if m.cfg != nil {
		global["notification_reply_to"] = m.cfg.ReplyTo
	} else {
		global["notification_reply_to"] = ""
	}

	s := raw
	for k, v := range global {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return s, nil
}

// loadTemplate reads the template content. Custom override wins over embedded default.
func (m *Mailer) loadTemplate(name string) (string, error) {
	if m.cfg.TemplateDir != "" {
		path := m.cfg.TemplateDir + "/" + name + ".txt"
		if data, err := os.ReadFile(path); err == nil {
			errs, warns := ValidateTemplate(string(data))
			for _, w := range warns {
				log.Printf("email: template %s: warning: %s", path, w)
			}
			if len(errs) == 0 {
				return string(data), nil
			}
			log.Printf("email: template %s ignored, using the default: %s", path, strings.Join(errs, "; "))
		}
	}

	data, err := defaultTemplatesFS.ReadFile("templates/" + name + ".txt")
	if err != nil {
		return "", fmt.Errorf("email: no template %q (embedded or custom)", name)
	}
	return string(data), nil
}

// parseTemplate splits a rendered template into subject and body.
// Expected format: first line "Subject: ...", then "---", then body.
func parseTemplate(text string) (subject, body string, ok bool) {
	lines := strings.Split(text, "\n")
	if len(lines) < 3 {
		return "", "", false
	}
	if !strings.HasPrefix(lines[0], "Subject: ") {
		return "", "", false
	}
	subject = strings.TrimPrefix(lines[0], "Subject: ")
	subject = strings.TrimSpace(subject)

	sepIdx := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "---" {
			sepIdx = i
			break
		}
	}
	if sepIdx < 0 {
		return "", "", false
	}
	body = strings.Join(lines[sepIdx+1:], "\n")
	return subject, strings.TrimSpace(body), true
}

// sanitizeHeader strips CR, LF, and other C0 control characters (tab excepted)
// from an email header value to prevent header/body injection (CWE-93). Header
// field values must occupy a single line; an embedded newline in caller-supplied
// text — e.g. researcher-controlled report content assembled into a Subject —
// could otherwise inject additional headers or forge a message body.
func sanitizeHeader(v string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return -1
		}
		if r < 0x20 && r != '\t' {
			return -1
		}
		return r
	}, v)
}

// buildMessage builds an RFC 2822 email message.
func buildMessage(fromName, fromAddr, to, subject, body, replyTo string) []byte {
	fromName = sanitizeHeader(fromName)
	fromAddr = sanitizeHeader(fromAddr)
	to = sanitizeHeader(to)
	subject = sanitizeHeader(subject)
	replyTo = sanitizeHeader(replyTo)
	var sb strings.Builder
	sb.WriteString("From: " + fromName + " <" + fromAddr + ">\r\n")
	sb.WriteString("To: " + to + "\r\n")
	sb.WriteString("Subject: " + subject + "\r\n")
	if replyTo != "" {
		sb.WriteString("Reply-To: " + replyTo + "\r\n")
	}
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(body)
	return []byte(sb.String())
}

// buildMultipartMessage builds a multipart/mixed message with a text/plain body
// and one base64-encoded binary attachment. The multipart boundary is generated
// by mime/multipart.
func buildMultipartMessage(fromName, fromAddr, to, subject, body, replyTo, filename string, data []byte) []byte {
	fromName = sanitizeHeader(fromName)
	fromAddr = sanitizeHeader(fromAddr)
	to = sanitizeHeader(to)
	subject = sanitizeHeader(subject)
	replyTo = sanitizeHeader(replyTo)
	// The attachment filename lands in a quoted Content-Disposition string; strip
	// control characters and any embedded quote that could break out of it.
	filename = strings.ReplaceAll(sanitizeHeader(filename), "\"", "")
	var partBuf bytes.Buffer
	w := multipart.NewWriter(&partBuf)

	var hdr bytes.Buffer
	hdr.WriteString("From: " + fromName + " <" + fromAddr + ">\r\n")
	hdr.WriteString("To: " + to + "\r\n")
	hdr.WriteString("Subject: " + subject + "\r\n")
	if replyTo != "" {
		hdr.WriteString("Reply-To: " + replyTo + "\r\n")
	}
	hdr.WriteString("MIME-Version: 1.0\r\n")
	hdr.WriteString("Content-Type: multipart/mixed; boundary=\"" + w.Boundary() + "\"\r\n")
	hdr.WriteString("\r\n")

	textPart, _ := w.CreatePart(textproto.MIMEHeader{
		"Content-Type": {"text/plain; charset=utf-8"},
	})
	_, _ = textPart.Write([]byte(body))

	attachPart, _ := w.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"application/octet-stream"},
		"Content-Transfer-Encoding": {"base64"},
		"Content-Disposition":       {"attachment; filename=\"" + filename + "\""},
	})
	encoded := base64.StdEncoding.EncodeToString(data)
	for i := 0; i < len(encoded); i += 76 {
		end := i + 76
		if end > len(encoded) {
			end = len(encoded)
		}
		_, _ = attachPart.Write([]byte(encoded[i:end] + "\r\n"))
	}
	_ = w.Close()

	return append(hdr.Bytes(), partBuf.Bytes()...)
}

// sendSMTP delivers the message via the configured SMTP server.
func (m *Mailer) sendSMTP(from, to string, msg []byte) error {
	addr := fmt.Sprintf("%s:%d", m.cfg.SMTP.Host, m.cfg.SMTP.Port)
	tlsMode := strings.ToLower(m.cfg.SMTP.TLS)
	if tlsMode == "" {
		tlsMode = "auto"
	}

	var auth smtp.Auth
	if m.cfg.SMTP.Username != "" {
		auth = smtp.PlainAuth("", m.cfg.SMTP.Username, m.cfg.SMTP.Password, m.cfg.SMTP.Host)
	}

	switch tlsMode {
	case "tls":
		tlsCfg := &tls.Config{ServerName: m.cfg.SMTP.Host, MinVersion: tls.VersionTLS12}
		conn, err := tls.Dial("tcp", addr, tlsCfg)
		if err != nil {
			return fmt.Errorf("smtp tls dial: %w", err)
		}
		defer func() { _ = conn.Close() }()
		c, err := smtp.NewClient(conn, m.cfg.SMTP.Host)
		if err != nil {
			return fmt.Errorf("smtp new client: %w", err)
		}
		defer func() { _ = c.Close() }()
		if auth != nil {
			if err := c.Auth(auth); err != nil {
				return fmt.Errorf("smtp auth: %w", err)
			}
		}
		return sendViaClient(c, from, to, msg)

	default:
		// "auto" and "starttls" — use smtp.SendMail which negotiates STARTTLS.
		return smtp.SendMail(addr, auth, from, []string{to}, msg)
	}
}

// sendViaClient sends through an already-connected smtp.Client.
func sendViaClient(c *smtp.Client, from, to string, msg []byte) error {
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("smtp MAIL: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp RCPT: %w", err)
	}
	wc, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := wc.Write(msg); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	return wc.Close()
}

// defaultGatewayIP attempts to determine the system default gateway IP.
func defaultGatewayIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer func() { _ = conn.Close() }()
	addr := conn.LocalAddr().(*net.UDPAddr)
	return addr.IP.String()
}

// globalIPv4 returns the first globally routable IPv4 address found.
func globalIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate() {
				continue
			}
			if v4 := ip.To4(); v4 != nil {
				return v4.String()
			}
		}
	}
	return ""
}
