// Package email handles outbound SMTP notifications for the cairnbin server.
// When no SMTP server is configured or reachable, all email features are silently
// disabled — no queuing, no retry, no "would have sent" logging.
package email

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/apimgr/cairnbin/src/common/i18n"
	"github.com/apimgr/cairnbin/src/config"
	emailtemplates "github.com/apimgr/cairnbin/src/server/template/email"
)

var defaultTemplatesFS = emailtemplates.FS

// Mailer sends notification emails using configured SMTP settings.
type Mailer struct {
	cfg     atomic.Pointer[config.EmailConfig]
	appName string
	appURL  string
	fqdn    string
	lang    atomic.Pointer[string]
	// onionAddress and i2pAddress are set at runtime once the corresponding
	// hidden service is available (PART 17 global template variables).
	onionAddress string
	i2pAddress   string
}

// New creates a Mailer from the provided email config, app name, and base URL.
func New(cfg *config.EmailConfig, appName, appURL, fqdn string, language ...string) *Mailer {
	lang := ""
	if len(language) > 0 {
		lang = language[0]
	}
	m := &Mailer{
		appName: appName,
		appURL:  appURL,
		fqdn:    fqdn,
	}
	m.SetLanguage(lang)
	m.SetConfig(cfg)
	return m
}

// SetLanguage updates the locale used for embedded email templates.
func (m *Mailer) SetLanguage(language string) {
	resolved := i18n.GetLanguage("", language)
	m.lang.Store(&resolved)
}

// SetConfig atomically replaces the active SMTP and template configuration.
func (m *Mailer) SetConfig(cfg *config.EmailConfig) {
	if cfg == nil {
		cfg = &config.EmailConfig{}
	}
	copy := *cfg
	m.cfg.Store(&copy)
}

func (m *Mailer) config() *config.EmailConfig {
	if cfg := m.cfg.Load(); cfg != nil {
		return cfg
	}
	return &config.EmailConfig{}
}

// SetOnionAddress records the Tor .onion address used to populate the
// {onion_url} and {onion_address} template variables. Pass "" to clear it.
func (m *Mailer) SetOnionAddress(addr string) { m.onionAddress = addr }

// SetI2PAddress records the I2P address used to populate the {i2p_url} and
// {i2p_address} template variables. Pass "" to clear it.
func (m *Mailer) SetI2PAddress(addr string) { m.i2pAddress = addr }

// Enabled returns true if SMTP is configured and email sending is enabled.
func (m *Mailer) Enabled() bool {
	cfg := m.config()
	return cfg.Enabled && cfg.SMTP.Host != ""
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
	msg, err := buildRFCMessage(fromName, fromAddr, to, subject, bodyText, m.config().ReplyTo, m.fqdn)
	if err != nil {
		return err
	}
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
	cfg := m.config()
	addr = cfg.From.Email
	if addr == "" {
		fqdn := m.fqdn
		if fqdn == "" {
			fqdn = "localhost"
		}
		addr = "no-reply@" + fqdn
	}
	name = cfg.From.Name
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
	msg, err := buildRFCMessage(fromName, fromAddr, to, subject, body, m.config().ReplyTo, m.fqdn)
	if err != nil {
		return err
	}
	return m.sendSMTP(fromAddr, to, msg)
}

// SendWithAttachment sends a multipart/alternative email with a single binary
// attachment in a multipart/mixed message. Used to deliver an AES-encrypted
// security report when no PGP key is configured (AI.md 14457). Silent no-op when
// SMTP is off.
func (m *Mailer) SendWithAttachment(to, subject, body, filename string, data []byte) error {
	if !m.Enabled() {
		return nil
	}
	fromName, fromAddr := m.resolveFrom()
	msg, err := buildMultipartRFCMessage(fromName, fromAddr, to, subject, body, m.config().ReplyTo, filename, data, m.fqdn)
	if err != nil {
		return err
	}
	return m.sendSMTP(fromAddr, to, msg)
}

// TestSMTP attempts an SMTP EHLO handshake using the configured TLS policy.
func (m *Mailer) TestSMTP() error {
	cfg := m.config()
	if cfg.SMTP.Host == "" {
		return fmt.Errorf("smtp: no host configured")
	}
	client, conn, _, err := connectSMTP(cfg.SMTP.Host, cfg.SMTP.Port, cfg.SMTP.TLS, 5*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close(); _ = conn.Close() }()
	return nil
}

// SMTPPrivateCertVerificationSkipped reports whether TLS certificate checks are
// relaxed for an explicitly configured private or local IP literal.
func SMTPPrivateCertVerificationSkipped(host, tlsMode string) bool {
	mode := strings.ToLower(strings.TrimSpace(tlsMode))
	if mode == "none" {
		return false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && isPrivateSMTPIP(ip)
}

func isPrivateSMTPIP(ip net.IP) bool {
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return true
	}
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	return cgnat.Contains(ip)
}

func smtpTLSConfig(host string) *tls.Config {
	return &tls.Config{
		ServerName:         host,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: SMTPPrivateCertVerificationSkipped(host, "tls"), // #nosec G402 -- private literal only, as required by PART 17
	}
}

// connectSMTP opens a session using the configured SMTP TLS mode and completes
// EHLO. AUTH is deliberately left to the caller and must follow TLS negotiation.
func connectSMTP(host string, port int, tlsMode string, timeout time.Duration) (*smtp.Client, net.Conn, bool, error) {
	mode := strings.ToLower(strings.TrimSpace(tlsMode))
	if mode == "" {
		mode = "auto"
	}
	switch mode {
	case "auto", "starttls", "tls", "none":
	default:
		return nil, nil, false, fmt.Errorf("smtp: unsupported TLS mode %q", tlsMode)
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	rawConn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, nil, false, fmt.Errorf("smtp: connect %s: %w", addr, err)
	}
	if err := rawConn.SetDeadline(time.Now().Add(timeout)); err != nil {
		_ = rawConn.Close()
		return nil, nil, false, fmt.Errorf("smtp: set deadline %s: %w", addr, err)
	}
	conn := rawConn
	tlsEstablished := false
	if mode == "tls" || (mode == "auto" && port == 465) {
		tlsConn := tls.Client(rawConn, smtpTLSConfig(host))
		if err := tlsConn.Handshake(); err != nil {
			_ = rawConn.Close()
			return nil, nil, false, fmt.Errorf("smtp tls handshake: %w", err)
		}
		conn = tlsConn
		tlsEstablished = true
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return nil, nil, false, fmt.Errorf("smtp: handshake %s: %w", addr, err)
	}
	if err := client.Hello("localhost"); err != nil {
		_ = client.Close()
		_ = conn.Close()
		return nil, nil, false, fmt.Errorf("smtp: EHLO %s: %w", addr, err)
	}
	if !tlsEstablished && (mode == "auto" || mode == "starttls") {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(smtpTLSConfig(host)); err != nil {
				_ = client.Close()
				_ = conn.Close()
				return nil, nil, false, fmt.Errorf("smtp STARTTLS: %w", err)
			}
			tlsEstablished = true
		} else if mode == "starttls" {
			_ = client.Close()
			_ = conn.Close()
			return nil, nil, false, fmt.Errorf("smtp: server does not offer STARTTLS")
		}
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	return client, conn, tlsEstablished, nil
}

// smtpHandshake dials host:port and performs an EHLO handshake for auto-detect.
func smtpHandshake(host string, port int, timeout time.Duration) error {
	mode := "auto"
	if port == 465 {
		mode = "tls"
	}
	client, conn, _, err := connectSMTP(host, port, mode, timeout)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close(); _ = conn.Close() }()
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
	localizedDefault := strings.Contains(raw, "{email_subject}") || strings.Contains(raw, "{email_body}")
	if localizedDefault {
		values := make(map[string]string, len(global)+len(vars))
		for k, v := range global {
			values[k] = v
		}
		for k, v := range vars {
			values[k] = v
		}
		prefix := "email.templates." + name + "."
		lang := "en"
		if current := m.lang.Load(); current != nil {
			lang = *current
		}
		global["email_subject"] = i18n.TranslateFormat(lang, prefix+"subject", values)
		global["email_body"] = i18n.TranslateFormat(lang, prefix+"body", values)
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
	cfg := m.config()
	if cfg != nil {
		global["notification_reply_to"] = cfg.ReplyTo
	} else {
		global["notification_reply_to"] = ""
	}

	s := raw
	for k, v := range global {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	if !localizedDefault {
		for k, v := range vars {
			s = strings.ReplaceAll(s, "{"+k+"}", v)
		}
	}
	return s, nil
}

// loadTemplate reads the template content. Custom override wins over embedded default.
func (m *Mailer) loadTemplate(name string) (string, error) {
	if templateDir := m.config().TemplateDir; templateDir != "" {
		path := templateDir + "/" + name + ".txt"
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

// buildMessage builds an RFC 5322 email message for package callers that do not
// have an installation FQDN. Production sends use buildRFCMessage directly.
func buildMessage(fromName, fromAddr, to, subject, body, replyTo string) []byte {
	msg, _ := buildRFCMessage(fromName, fromAddr, to, subject, body, replyTo, "localhost")
	return msg
}

// buildRFCMessage creates a standards-compliant multipart/alternative message.
func buildRFCMessage(fromName, fromAddr, to, subject, body, replyTo, fqdn string) ([]byte, error) {
	for field, value := range map[string]string{
		"From name": fromName, "From address": fromAddr, "To": to,
		"Subject": subject, "Reply-To": replyTo,
	} {
		for _, r := range value {
			if r < 0x20 || r == 0x7f {
				return nil, fmt.Errorf("email: %s contains a control character", field)
			}
		}
	}
	from, err := mail.ParseAddress(fromAddr)
	if err != nil || from.Address != fromAddr || len(fromAddr) > 254 {
		return nil, fmt.Errorf("email: invalid From address")
	}
	recipient, err := mail.ParseAddress(to)
	if err != nil || recipient.Address != to || len(to) > 254 {
		return nil, fmt.Errorf("email: invalid recipient address")
	}
	if replyTo != "" {
		reply, parseErr := mail.ParseAddress(replyTo)
		if parseErr != nil || reply.Address != replyTo || len(replyTo) > 254 {
			return nil, fmt.Errorf("email: invalid Reply-To address")
		}
	}
	if strings.TrimSpace(subject) == "" {
		return nil, fmt.Errorf("email: subject cannot be empty")
	}
	if strings.TrimSpace(body) == "" {
		return nil, fmt.Errorf("email: body cannot be empty")
	}
	fqdn = strings.TrimSpace(fqdn)
	if fqdn == "" || len(fqdn) > 200 || strings.ContainsAny(fqdn, "\r\n <>@") {
		fqdn = "localhost"
	}
	messageID, err := newMessageID(fqdn)
	if err != nil {
		return nil, fmt.Errorf("email: generate Message-ID: %w", err)
	}

	var bodyBuf bytes.Buffer
	alternative := multipart.NewWriter(&bodyBuf)
	if boundary, boundaryErr := newMIMEBoundary(); boundaryErr != nil {
		return nil, fmt.Errorf("email: generate MIME boundary: %w", boundaryErr)
	} else if err := alternative.SetBoundary(boundary); err != nil {
		return nil, fmt.Errorf("email: set MIME boundary: %w", err)
	}
	boundary := alternative.Boundary()
	plain, err := alternative.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"text/plain; charset=UTF-8"},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	if err != nil {
		return nil, fmt.Errorf("email: create plain text part: %w", err)
	}
	plainWriter := quotedprintable.NewWriter(plain)
	if _, err = plainWriter.Write([]byte(body)); err != nil {
		return nil, fmt.Errorf("email: encode plain text part: %w", err)
	}
	if err = plainWriter.Close(); err != nil {
		return nil, fmt.Errorf("email: finish plain text part: %w", err)
	}
	htmlPart, err := alternative.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"text/html; charset=UTF-8"},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	if err != nil {
		return nil, fmt.Errorf("email: create HTML part: %w", err)
	}
	htmlWriter := quotedprintable.NewWriter(htmlPart)
	htmlBody := "<!doctype html><html><body><pre>" + html.EscapeString(body) + "</pre></body></html>"
	if _, err = htmlWriter.Write([]byte(htmlBody)); err != nil {
		return nil, fmt.Errorf("email: encode HTML part: %w", err)
	}
	if err = htmlWriter.Close(); err != nil {
		return nil, fmt.Errorf("email: finish HTML part: %w", err)
	}
	if err = alternative.Close(); err != nil {
		return nil, fmt.Errorf("email: finish multipart message: %w", err)
	}

	fromHeader := formatMailbox(fromName, from.Address)
	subjectHeader := encodeHeaderWords(subject)
	var headers strings.Builder
	writeFoldedHeader(&headers, "From", fromHeader)
	writeFoldedHeader(&headers, "To", recipient.String())
	writeFoldedHeader(&headers, "Subject", subjectHeader)
	if replyTo != "" {
		parsedReply, _ := mail.ParseAddress(replyTo)
		writeFoldedHeader(&headers, "Reply-To", parsedReply.String())
	}
	writeFoldedHeader(&headers, "Date", time.Now().Format(time.RFC1123Z))
	writeFoldedHeader(&headers, "Message-ID", messageID)
	headers.WriteString("MIME-Version: 1.0\r\nAuto-Submitted: auto-generated\r\n")
	writeFoldedHeader(&headers, "Content-Type", fmt.Sprintf("multipart/alternative; boundary=\"%s\"", boundary))
	headers.WriteString("\r\n")
	return append([]byte(headers.String()), bodyBuf.Bytes()...), nil
}

// writeFoldedHeader folds at existing whitespace boundaries when a header
// would exceed the RFC 5322 recommended 78 octets. An unbreakable token (such
// as an addr-spec or Message-ID) is kept intact; the 998-octet hard limit is
// enforced by validation and the generated values are bounded accordingly.
func writeFoldedHeader(dst *strings.Builder, name, value string) {
	prefix := name + ": "
	line := prefix
	for _, token := range strings.Fields(value) {
		if len(line) > len(prefix) && len(line)+1+len(token) > 78 {
			dst.WriteString(strings.TrimRight(line, " ") + "\r\n ")
			line = " " + token
			continue
		}
		if line != prefix {
			line += " "
		}
		line += token
	}
	dst.WriteString(strings.TrimRight(line, " ") + "\r\n")
}

func newMIMEBoundary() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return "=_cairnbin_" + hex.EncodeToString(id[:]), nil
}

// encodeHeaderWords splits long or non-ASCII values into RFC 2047 encoded
// words that can be folded without breaking UTF-8 sequences.
func encodeHeaderWords(value string) string {
	if len(value) <= 60 && isASCII(value) {
		return value
	}
	var words []string
	var chunk strings.Builder
	chunkBytes := 0
	flush := func() {
		if chunk.Len() == 0 {
			return
		}
		words = append(words, mime.BEncoding.Encode("UTF-8", chunk.String()))
		chunk.Reset()
		chunkBytes = 0
	}
	for _, r := range value {
		runeText := string(r)
		if chunkBytes+len(runeText) > 30 {
			flush()
		}
		chunk.WriteString(runeText)
		chunkBytes += len(runeText)
	}
	flush()
	return strings.Join(words, "\r\n ")
}

func isASCII(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] >= 0x80 {
			return false
		}
	}
	return true
}

func formatMailbox(name, address string) string {
	if name == "" {
		return address
	}
	if isASCII(name) && len(name) <= 60 {
		mailbox := (&mail.Address{Name: name, Address: address}).String()
		if len("From: ")+len(mailbox) <= 78 {
			return mailbox
		}
		if index := strings.LastIndex(mailbox, " <"); index >= 0 {
			return mailbox[:index] + "\r\n " + mailbox[index+1:]
		}
		return mailbox
	}
	encodedName := encodeHeaderWords(name)
	if len("From: ")+len(encodedName)+len(" <")+len(address)+1 > 78 {
		return encodedName + "\r\n <" + address + ">"
	}
	return encodedName + " <" + address + ">"
}

func newMessageID(fqdn string) (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return "<" + hex.EncodeToString(id[:]) + "@" + fqdn + ">", nil
}

// buildMultipartMessage is retained for package-local callers without an
// installation FQDN. Production sends use buildMultipartRFCMessage directly.
func buildMultipartMessage(fromName, fromAddr, to, subject, body, replyTo, filename string, data []byte) []byte {
	msg, _ := buildMultipartRFCMessage(fromName, fromAddr, to, subject, body, replyTo, filename, data, "localhost")
	return msg
}

// buildMultipartRFCMessage wraps the required multipart/alternative body in a
// multipart/mixed container alongside a binary attachment.
func buildMultipartRFCMessage(fromName, fromAddr, to, subject, body, replyTo, filename string, data []byte, fqdn string) ([]byte, error) {
	inner, err := buildRFCMessage(fromName, fromAddr, to, subject, body, replyTo, fqdn)
	if err != nil {
		return nil, err
	}
	parts := bytes.SplitN(inner, []byte("\r\n\r\n"), 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("email: malformed generated message")
	}
	headerReader := textproto.NewReader(bufio.NewReader(bytes.NewReader(append(parts[0], '\r', '\n', '\r', '\n'))))
	messageHeaders, err := headerReader.ReadMIMEHeader()
	if err != nil {
		return nil, fmt.Errorf("email: parse generated headers: %w", err)
	}
	alternativeType := messageHeaders.Get("Content-Type")
	var headers strings.Builder
	for name, values := range messageHeaders {
		if strings.EqualFold(name, "Content-Type") {
			continue
		}
		for _, value := range values {
			writeFoldedHeader(&headers, name, value)
		}
	}
	mediaType, params, err := mime.ParseMediaType(alternativeType)
	if err != nil || mediaType != "multipart/alternative" || params["boundary"] == "" {
		return nil, fmt.Errorf("email: malformed alternative message")
	}
	if strings.ContainsAny(filename, "\r\n") {
		return nil, fmt.Errorf("email: attachment filename contains a line break")
	}
	filename = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			return -1
		}
		return r
	}, filename)
	if len([]rune(filename)) > 200 {
		filename = string([]rune(filename)[:200])
	}

	var bodyBuf bytes.Buffer
	mixed := multipart.NewWriter(&bodyBuf)
	if boundary, boundaryErr := newMIMEBoundary(); boundaryErr != nil {
		return nil, fmt.Errorf("email: generate MIME boundary: %w", boundaryErr)
	} else if err := mixed.SetBoundary(boundary); err != nil {
		return nil, fmt.Errorf("email: set MIME boundary: %w", err)
	}
	writeFoldedHeader(&headers, "Content-Type", fmt.Sprintf("multipart/mixed; boundary=\"%s\"", mixed.Boundary()))
	headers.WriteString("\r\n")
	alternative, err := mixed.CreatePart(textproto.MIMEHeader{
		"Content-Type": {alternativeType},
	})
	if err != nil {
		return nil, fmt.Errorf("email: create alternative attachment part: %w", err)
	}
	if _, err := alternative.Write(parts[1]); err != nil {
		return nil, fmt.Errorf("email: write alternative attachment part: %w", err)
	}
	contentDisposition := mime.FormatMediaType("attachment", map[string]string{"filename": filename})
	attachment, err := mixed.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"application/octet-stream"},
		"Content-Transfer-Encoding": {"base64"},
		"Content-Disposition":       {contentDisposition},
	})
	if err != nil {
		return nil, fmt.Errorf("email: create attachment part: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	for i := 0; i < len(encoded); i += 76 {
		end := min(i+76, len(encoded))
		if _, err := io.WriteString(attachment, encoded[i:end]+"\r\n"); err != nil {
			return nil, fmt.Errorf("email: write attachment: %w", err)
		}
	}
	if err := mixed.Close(); err != nil {
		return nil, fmt.Errorf("email: finish mixed message: %w", err)
	}
	return append([]byte(headers.String()), bodyBuf.Bytes()...), nil
}

// sendSMTP delivers the message via the configured SMTP server.
func (m *Mailer) sendSMTP(from, to string, msg []byte) error {
	cfg := m.config()
	client, conn, tlsEstablished, err := connectSMTP(cfg.SMTP.Host, cfg.SMTP.Port, cfg.SMTP.TLS, 10*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close(); _ = conn.Close() }()
	if cfg.SMTP.Username != "" {
		if !tlsEstablished {
			return fmt.Errorf("smtp: refusing AUTH without an established TLS connection")
		}
		auth := smtp.PlainAuth("", cfg.SMTP.Username, cfg.SMTP.Password, cfg.SMTP.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	return sendViaClient(client, from, to, msg)
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
