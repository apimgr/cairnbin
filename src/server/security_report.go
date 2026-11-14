package server

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/apimgr/cairnbin/src/audit"
	"github.com/apimgr/cairnbin/src/common/email"
	"github.com/apimgr/cairnbin/src/config"
	"github.com/apimgr/cairnbin/src/database"
	"github.com/apimgr/cairnbin/src/pgp"
)

// maxResearcherKeyBytes caps the researcher public-key fetch to guard against a
// hostile or misconfigured URL streaming an unbounded body.
const maxResearcherKeyBytes = 1 << 20

// pgpPublicKeyHeader marks the start of an ASCII-armored PGP public key block.
const pgpPublicKeyHeader = "-----BEGIN PGP PUBLIC KEY BLOCK-----"

// isPublicIP reports whether ip is a globally routable unicast address. Loopback,
// RFC1918/ULA private, link-local, unspecified, and multicast ranges are rejected
// so a researcher-supplied key URL cannot target internal services (SSRF guard).
func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return false
	}
	return true
}

// fetchResearcherPubKey resolves the researcher's ASCII-armored PGP public key
// from their submitted value (AI.md 14438): a pasted armored block is returned
// as-is; an https:// URL is fetched over an SSRF-hardened client that refuses any
// non-public resolved address and non-https redirect. The returned key is
// structurally validated before use. Any error leaves the caller to fall back to
// a plaintext acknowledgment.
func fetchResearcherPubKey(src string) (string, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return "", fmt.Errorf("no researcher key supplied")
	}
	if strings.Contains(src, pgpPublicKeyHeader) {
		if _, err := pgp.FingerprintFromPublic(src); err != nil {
			return "", fmt.Errorf("pasted researcher key invalid: %w", err)
		}
		return src, nil
	}

	u, err := url.Parse(src)
	if err != nil {
		return "", fmt.Errorf("parse researcher key url: %w", err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("researcher key url must be https")
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	control := func(_, address string, _ syscall.RawConn) error {
		host, _, splitErr := net.SplitHostPort(address)
		if splitErr != nil {
			return fmt.Errorf("parse dial address: %w", splitErr)
		}
		if ip := net.ParseIP(host); ip == nil || !isPublicIP(ip) {
			return fmt.Errorf("researcher key url resolves to a non-public address")
		}
		return nil
	}
	dialer.Control = control
	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
			Proxy:               http.ProxyFromEnvironment,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to non-https blocked")
			}
			if len(via) >= 3 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return "", fmt.Errorf("build researcher key request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch researcher key: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch researcher key: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResearcherKeyBytes))
	if err != nil {
		return "", fmt.Errorf("read researcher key: %w", err)
	}
	key := string(body)
	if !strings.Contains(key, pgpPublicKeyHeader) {
		return "", fmt.Errorf("fetched researcher key is not an armored public key")
	}
	if _, err := pgp.FingerprintFromPublic(key); err != nil {
		return "", fmt.Errorf("fetched researcher key invalid: %w", err)
	}
	return key, nil
}

// defaultDisclosureDays is the coordinated-disclosure window offered by default
// (AI.md 14138: "Number of days (default 90)").
const defaultDisclosureDays = 90

// securityComponents populates the affected-component dropdown on the security
// form. AI.md 14439 says to list the project's own features; the spec's
// "(auth, API, frontend, CLI, etc.)" is generic example text — cairnbin has no
// auth, so it is omitted. "other" pairs with the free-text component_other field.
var securityComponents = []string{
	"api", "frontend", "cli", "paste", "storage",
	"config", "scheduler", "email", "other",
}

// validSeverities is the researcher self-assessment set (AI.md 14132).
var validSeverities = map[string]bool{
	"Critical": true, "High": true, "Medium": true, "Low": true, "Informational": true,
}

// validCreditPrefs is the acknowledgments preference set (AI.md 14139).
var validCreditPrefs = map[string]bool{
	"name": true, "handle": true, "no": true, "anonymous": true,
}

// clientIP extracts the peer IP from RemoteAddr for security.log entries.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// randomHex returns n random hex characters (n/2 random bytes).
func randomHex(n int) (string, error) {
	b := make([]byte, (n+1)/2)
	if _, err := crand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b)[:n], nil
}

// sanitizeComponent reduces an affected-component label to a short, safe token
// for at-rest metadata: it keeps letters, digits, '-', '_', '/' and '.', drops
// everything else, and caps the length. This prevents free-text PII or markup
// from landing in the plaintext metadata columns (AI.md 14151 "sanitized
// affected-component, NO researcher PII").
func sanitizeComponent(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '/', r == '.':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
		if b.Len() >= 64 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "unspecified"
	}
	return out
}

// handleSecurityReport processes a validated coordinated-disclosure submission
// (AI.md 14143-14151). The security_id has already been re-validated by the
// caller. It gathers the security-research fields, encrypts the full report at
// rest, allocates a tracking id, notifies the maintainer and researcher by
// email, logs a PII-free security.log entry, and renders a content-negotiated
// success response. The plaintext report body is NEVER persisted.
func (s *Server) handleSecurityReport(w http.ResponseWriter, r *http.Request, cfg *config.Config) {
	data := s.contactData(r)
	// Force security-mode rendering: contactData only enables it when a valid
	// security_id rides in the query string, but a re-validated id may have
	// arrived via the hidden field. This keeps the form in security mode on any
	// validation-error re-render.
	data["SecurityMode"] = true
	data["SecurityID"] = s.currentSecurityID()
	data["Components"] = securityComponents

	name := strings.TrimSpace(r.PostFormValue("name"))
	fromEmail := strings.TrimSpace(r.PostFormValue("email"))
	gpg := strings.TrimSpace(r.PostFormValue("researcher_gpg"))
	component := strings.TrimSpace(r.PostFormValue("component"))
	componentOther := strings.TrimSpace(r.PostFormValue("component_other"))
	endpoint := strings.TrimSpace(r.PostFormValue("endpoint"))
	severity := strings.TrimSpace(r.PostFormValue("severity"))
	summary := strings.TrimSpace(r.PostFormValue("summary"))
	steps := strings.TrimSpace(r.PostFormValue("steps"))
	impact := strings.TrimSpace(r.PostFormValue("impact"))
	suggestedFix := strings.TrimSpace(r.PostFormValue("suggested_fix"))
	cveRequested := config.IsTruthy(r.PostFormValue("cve_requested"))
	creditPref := strings.TrimSpace(r.PostFormValue("credit_preference"))
	creditName := strings.TrimSpace(r.PostFormValue("credit_name"))
	agreement := config.IsTruthy(r.PostFormValue("agreement"))

	disclosureDays := defaultDisclosureDays
	if v := strings.TrimSpace(r.PostFormValue("disclosure_days")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 365 {
			disclosureDays = n
		}
	}

	// Preserve entered values so a validation error does not clear the form.
	data["FormName"] = name
	data["FormEmail"] = fromEmail
	data["FormGPG"] = gpg
	data["FormComponentOther"] = componentOther
	data["FormEndpoint"] = endpoint
	data["FormSummary"] = summary
	data["FormSteps"] = steps
	data["FormImpact"] = impact
	data["FormSuggestedFix"] = suggestedFix
	data["FormDisclosureDays"] = disclosureDays
	data["FormCreditName"] = creditName

	fail := func(status int, msg string) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			code := "SERVER_ERROR"
			if status == http.StatusBadRequest {
				code = "VALIDATION_FAILED"
			}
			if status == http.StatusServiceUnavailable {
				code = "MAINTENANCE"
			}
			writeJSON(w, status, map[string]interface{}{"ok": false, "error": code, "message": msg})
			return
		}
		data["ContactError"] = msg
		w.WriteHeader(status)
		s.renderTemplate(w, r, "contact.html", data)
	}

	// Server-side validation — the server is authoritative (never trust client).
	if name == "" || fromEmail == "" || component == "" || severity == "" ||
		summary == "" || steps == "" || impact == "" || creditPref == "" {
		fail(http.StatusBadRequest, "Please complete all required fields.")
		return
	}
	if !strings.Contains(fromEmail, "@") || strings.ContainsAny(fromEmail, " \t\r\n") {
		fail(http.StatusBadRequest, "Please enter a valid email address.")
		return
	}
	lengths := map[string]int{"name": 100, "email": 254, "researcher_gpg": 16384, "component": 255, "component_other": 255, "endpoint": 2048, "summary": 200, "steps": 10000, "impact": 5000, "suggested_fix": 5000}
	values := map[string]string{"name": name, "email": fromEmail, "researcher_gpg": gpg, "component": component, "component_other": componentOther, "endpoint": endpoint, "summary": summary, "steps": steps, "impact": impact, "suggested_fix": suggestedFix, "credit_name": creditName}
	for field, limit := range lengths {
		value := values[field]
		if strings.HasPrefix(field, "component") && component != "other" && field == "component_other" {
			continue
		}
		if utf8.RuneCountInString(value) > limit || (field == "researcher_gpg" && strings.HasPrefix(strings.ToLower(value), "https://") && utf8.RuneCountInString(value) > 2048) {
			fail(http.StatusBadRequest, fmt.Sprintf("%s must be at most %d characters.", field, limit))
			return
		}
	}
	if creditPref == "handle" && utf8.RuneCountInString(creditName) > 100 {
		fail(http.StatusBadRequest, "credit_name must be at most 100 characters.")
		return
	}
	if !validSeverities[severity] {
		fail(http.StatusBadRequest, "Please choose a valid severity.")
		return
	}
	if !validCreditPrefs[creditPref] {
		fail(http.StatusBadRequest, "Please choose a valid credit preference.")
		return
	}
	if creditPref == "handle" && creditName == "" {
		fail(http.StatusBadRequest, "Please enter the credit handle to display.")
		return
	}
	if !agreement {
		fail(http.StatusBadRequest, "You must agree to coordinated disclosure to submit a report.")
		return
	}

	response := r.PostFormValue("g-recaptcha-response")
	if response == "" {
		response = r.PostFormValue("h-captcha-response")
	}
	if !s.verifyContactCaptcha(r, r.PostFormValue("captcha_token"), r.PostFormValue("captcha"), response) {
		fail(http.StatusBadRequest, "Captcha answer was incorrect. Please try again.")
		return
	}

	// Resolve the affected component: "other" defers to the free-text field.
	componentLabel := component
	if component == "other" && componentOther != "" {
		componentLabel = componentOther
	}
	sanitized := sanitizeComponent(componentLabel)

	// Encryption at rest is MANDATORY — refuse rather than persist plaintext.
	key, err := cfg.EncryptionKey()
	if err != nil {
		log.Printf("security report: encryption key unavailable: %v", err)
		fail(http.StatusServiceUnavailable, "Security reporting is temporarily unavailable. Please email the security contact directly.")
		return
	}

	trackingID, err := randomHex(16)
	if err != nil {
		fail(http.StatusInternalServerError, "Could not process your report. Please try again.")
		return
	}
	trackingID = "sec_" + trackingID

	rawToken, err := randomHex(32)
	if err != nil {
		fail(http.StatusInternalServerError, "Could not process your report. Please try again.")
		return
	}
	tokenHash := sha256.Sum256([]byte(rawToken))

	timestamp := time.Now().Format(time.RFC3339)
	report := buildReportBody(reportFields{
		trackingID:     trackingID,
		timestamp:      timestamp,
		name:           name,
		email:          fromEmail,
		gpg:            gpg,
		component:      componentLabel,
		endpoint:       endpoint,
		severity:       severity,
		summary:        summary,
		steps:          steps,
		impact:         impact,
		suggestedFix:   suggestedFix,
		cveRequested:   cveRequested,
		disclosureDays: disclosureDays,
		creditPref:     creditPref,
		creditName:     creditName,
		userAgent:      r.UserAgent(),
		remoteIP:       clientIP(r),
		appVersion:     s.version,
		commitHash:     s.commitID,
	})

	// Seal the report at rest: prefer the project PGP key, fall back to
	// AES-256-GCM keyed by server.security.encryption_key (PART 11).
	sealed, encMethod, err := s.encryptSecurityReport([]byte(report), key)
	if err != nil {
		log.Printf("security report: seal failed: %v", err)
		fail(http.StatusInternalServerError, "Could not securely store your report. Please try again.")
		return
	}

	// Credit is only displayed for name/handle; anonymous and no-credit keep the
	// display name out of the stored metadata.
	storedCreditName := ""
	switch creditPref {
	case "name":
		storedCreditName = name
	case "handle":
		storedCreditName = creditName
	}

	rec := &database.SecurityReport{
		TrackingID:       trackingID,
		Severity:         severity,
		Component:        sanitized,
		EncryptedBody:    sealed,
		EncMethod:        encMethod,
		CreditPreference: creditPref,
		CreditName:       storedCreditName,
		TokenHash:        hex.EncodeToString(tokenHash[:]),
		DisclosureDays:   disclosureDays,
	}
	if err := s.db.CreateSecurityReport(rec); err != nil {
		log.Printf("security report: store failed: %v", err)
		fail(http.StatusInternalServerError, "Could not record your report. Please try again.")
		return
	}

	// Notifications are best-effort: the report is already durably stored, so an
	// email failure must not lose it — log and continue to the success response.
	s.sendSecurityReportEmails(r, cfg, securityEmailCtx{
		trackingID:     trackingID,
		timestamp:      timestamp,
		researcher:     fromEmail,
		severity:       severity,
		summary:        summary,
		component:      componentLabel,
		cveRequested:   cveRequested,
		disclosureDays: disclosureDays,
		encMethod:      encMethod,
		statusToken:    rawToken,
		sealed:         sealed,
		researcherGPG:  gpg,
	})

	// Admin webhook summary — server-internal event, NEVER user content (PART 17).
	// Only the tracking id, severity, and sanitized component leave the server.
	s.notifyRole(cfg, "admin", "admin.security_report_received",
		"Security report received: "+trackingID,
		"A new security report was received. Severity: "+severity+"; component: "+sanitized+". Decrypt the at-rest report to read the full submission.",
		severity)

	// PII-free audit line — tracking id, severity, sanitized component only.
	s.securityLog("security.report_received",
		"tracking_id", trackingID, "severity", severity, "component", sanitized)
	s.auditLog(r, audit.Entry{
		Event:    "security.report_received",
		Severity: audit.SeverityWarn,
		Target:   &audit.Target{Type: "security_report", ID: trackingID},
		Details:  map[string]any{"severity": severity, "component": sanitized},
	})

	// Content-negotiated success (AI.md 14150).
	if ct := detectClientType(r); ct == "json" || ct == "text" {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":   true,
			"data": map[string]string{"tracking_id": trackingID},
		})
		return
	}

	data["ContactSuccess"] = fmt.Sprintf(
		"Thank you — your security report was received and encrypted. Your tracking ID is %s. Check your email for confirmation.",
		trackingID)
	// Clear the preserved fields so the form resets after a successful submit.
	for _, k := range []string{"FormName", "FormEmail", "FormGPG", "FormComponentOther",
		"FormEndpoint", "FormSummary", "FormSteps", "FormImpact", "FormSuggestedFix",
		"FormCreditName"} {
		delete(data, k)
	}
	s.renderTemplate(w, r, "contact.html", data)
}

// reportFields carries the plaintext values assembled into the encrypted report
// body. This struct never leaves this file and is never persisted in the clear.
type reportFields struct {
	trackingID, timestamp                       string
	name, email, gpg                            string
	component, endpoint, severity, summary      string
	steps, impact, suggestedFix                 string
	cveRequested                                bool
	disclosureDays                              int
	creditPref, creditName                      string
	userAgent, remoteIP, appVersion, commitHash string
}

// buildReportBody renders the full researcher submission as a plain-text report.
// This is the ONLY representation of the vulnerability detail and is encrypted
// before it touches disk (AI.md 14147).
func buildReportBody(f reportFields) string {
	yn := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Tracking ID: %s\n", f.trackingID)
	fmt.Fprintf(&b, "Received: %s\n\n", f.timestamp)
	fmt.Fprintf(&b, "Reporter: %s <%s>\n", f.name, f.email)
	if f.gpg != "" {
		fmt.Fprintf(&b, "Reporter PGP: %s\n", f.gpg)
	}
	fmt.Fprintf(&b, "Credit preference: %s", f.creditPref)
	if f.creditName != "" {
		fmt.Fprintf(&b, " (%s)", f.creditName)
	}
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "Severity (self-assessed): %s\n", f.severity)
	fmt.Fprintf(&b, "Affected component: %s\n", f.component)
	if f.endpoint != "" {
		fmt.Fprintf(&b, "Affected endpoint: %s\n", f.endpoint)
	}
	fmt.Fprintf(&b, "CVE requested: %s\n", yn(f.cveRequested))
	fmt.Fprintf(&b, "Requested disclosure window: %d days\n\n", f.disclosureDays)
	fmt.Fprintf(&b, "Summary:\n%s\n\n", f.summary)
	fmt.Fprintf(&b, "Steps to reproduce:\n%s\n\n", f.steps)
	fmt.Fprintf(&b, "Impact:\n%s\n\n", f.impact)
	if f.suggestedFix != "" {
		fmt.Fprintf(&b, "Suggested fix:\n%s\n\n", f.suggestedFix)
	}
	b.WriteString("-- triage metadata --\n")
	fmt.Fprintf(&b, "app_version: %s\n", f.appVersion)
	fmt.Fprintf(&b, "commit_hash: %s\n", f.commitHash)
	fmt.Fprintf(&b, "request_user_agent: %s\n", f.userAgent)
	fmt.Fprintf(&b, "request_ip: %s\n", f.remoteIP)
	return b.String()
}

// securityEmailCtx carries the values needed for the two notification emails.
type securityEmailCtx struct {
	trackingID, timestamp, researcher string
	severity, summary, component      string
	cveRequested                      bool
	disclosureDays                    int
	encMethod, statusToken            string
	// sealed is the encrypted report body delivered to the maintainer — inline
	// when PGP-armored, as an attachment when AES-256-GCM (AI.md 14457).
	sealed []byte
	// researcherGPG is the researcher's submitted key (armored block or https URL);
	// when present and valid, the acknowledgment is PGP-encrypted (AI.md 14458).
	researcherGPG string
}

// sendSecurityReportEmails dispatches the maintainer notification and the
// researcher acknowledgment (AI.md 14457-14458). Both are best-effort; failures
// are logged and do not affect the already-stored report. The maintainer sees the
// full report only via this encrypted email (AI.md 14460): a PGP-sealed body is
// delivered inline as an armored PGP message; an AES-256-GCM body is delivered as
// an encrypted attachment with a warning that no PGP key was configured.
func (s *Server) sendSecurityReportEmails(r *http.Request, cfg *config.Config, c securityEmailCtx) {
	m := email.New(&cfg.Server.Notifications.Email, cfg.Web.SiteTitle, s.baseURL(r), cfg.Server.FQDN, cfg.Server.Lang)
	if !m.Enabled() {
		return
	}
	yn := "no"
	if c.cveRequested {
		yn = "yes"
	}

	if to := strings.TrimSpace(cfg.SecurityReportEmail()); to != "" {
		subject := fmt.Sprintf("[Security] %s: %s", c.severity, c.summary)
		cover := fmt.Sprintf(
			"SECURITY REPORT RECEIVED\r\n\r\n"+
				"Received: %s\r\n"+
				"Site: %s (%s)\r\n\r\n"+
				"Tracking ID: %s\r\n"+
				"Severity (researcher self-assessment): %s\r\n"+
				"Affected component: %s\r\n"+
				"CVE requested: %s\r\n"+
				"Requested disclosure window: %d days\r\n\r\n",
			c.timestamp, cfg.Web.SiteTitle, cfg.Server.FQDN,
			c.trackingID, c.severity, c.component, yn, c.disclosureDays)

		var err error
		if c.encMethod == "pgp" {
			// Inline PGP-encrypted email: the armored ciphertext is the payload;
			// the maintainer decrypts it with the project PGP private key.
			body := cover +
				"The full encrypted report follows. Decrypt it with the project PGP private key.\r\n\r\n" +
				string(c.sealed) + "\r\n"
			err = m.SendRawMessage(to, subject, body)
		} else {
			// No PGP key configured: attach the AES-256-GCM ciphertext and direct
			// the maintainer to decrypt it with the server encryption key.
			body := cover +
				"No PGP key is configured, so the full report is attached as an\r\n" +
				"AES-256-GCM encrypted file. Decrypt it with the server encryption\r\n" +
				"key (server.security.encryption_key).\r\n"
			err = m.SendWithAttachment(to, subject, body, c.trackingID+".enc", c.sealed)
		}
		if err != nil {
			log.Printf("security report: maintainer notification failed: %v", err)
		}
	}

	statusURL := fmt.Sprintf("%s/server/security/report/%s?token=%s",
		s.baseURL(r), c.trackingID, c.statusToken)
	ackVars := map[string]string{
		"timestamp":   c.timestamp,
		"tracking_id": c.trackingID,
		"status_url":  statusURL,
	}

	// When the researcher supplied a valid public key, PGP-encrypt the whole
	// acknowledgment and deliver the armored ciphertext inline (AI.md 14458). Any
	// failure resolving, validating, or encrypting to their key falls back to the
	// plaintext template — the acknowledgment carries no vulnerability content.
	if strings.TrimSpace(c.researcherGPG) != "" {
		if pub, err := fetchResearcherPubKey(c.researcherGPG); err != nil {
			log.Printf("security report: researcher key unusable, sending plaintext ack: %v", err)
		} else if subject, body, err := m.Render("security_report_ack", ackVars); err != nil {
			log.Printf("security report: render ack failed: %v", err)
		} else if armored, err := pgp.Encrypt(pub, []byte(body)); err != nil {
			log.Printf("security report: encrypt ack to researcher key failed, sending plaintext: %v", err)
		} else if err := m.SendRawMessage(c.researcher, subject, armored); err != nil {
			log.Printf("security report: encrypted researcher acknowledgment failed: %v", err)
		} else {
			return
		}
	}

	if err := m.Send(c.researcher, "security_report_ack", ackVars); err != nil {
		log.Printf("security report: researcher acknowledgment failed: %v", err)
	}
}
