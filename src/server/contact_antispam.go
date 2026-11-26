package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"html"
	"io"
	"log"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/apimgr/cairnbin/src/config"
	"github.com/apimgr/cairnbin/src/metric"
	"github.com/go-chi/chi/v5"
)

type contactCaptchaVerifiedKey struct{}

func markContactCaptchaVerified(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), contactCaptchaVerifiedKey{}, true))
}

func contactCaptchaWasVerified(r *http.Request) bool {
	verified, _ := r.Context().Value(contactCaptchaVerifiedKey{}).(bool)
	return verified
}

const contactBodyLimit = 256 << 10

var (
	contactDomainPattern = regexp.MustCompile(`(?i)(?:[a-z][a-z0-9+.-]*://|www\.)?[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+`)
	contactMarkupPattern = regexp.MustCompile(`(?i)\[(?:url|img|b)(?:[=\s\]]|$)|\[/`)
)

type contactSpamState struct {
	accepted   map[string][]time.Time
	global     []time.Time
	strikes    map[string][]time.Time
	banned     map[string]time.Time
	duplicates map[[32]byte]time.Time
	usedTokens map[string]time.Time
}

func (s *Server) contactSpamState() *contactSpamState {
	s.contactSpamMu.Lock()
	defer s.contactSpamMu.Unlock()
	if s.contactSpam == nil {
		s.contactSpam = &contactSpamState{
			accepted: make(map[string][]time.Time), strikes: make(map[string][]time.Time),
			banned: make(map[string]time.Time), duplicates: make(map[[32]byte]time.Time),
			usedTokens: make(map[string]time.Time),
		}
	}
	return s.contactSpam
}

func (s *Server) contactBodyLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		isContact := path == "/server/contact" || strings.HasSuffix(path, "/server/contact")
		if r.Method != http.MethodPost || !isContact || r.Body == nil {
			next.ServeHTTP(w, r)
			return
		}
		cfg := s.liveCfg()
		if !cfg.Server.Pages.Contact.Enabled {
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, contactBodyLimit+1))
		_ = r.Body.Close()
		if err != nil || len(body) > contactBodyLimit {
			s.recordContactBlock(contactAntispam(cfg), r, "shape", false, time.Now())
			if strings.HasPrefix(path, "/api/") {
				s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusBadRequest, "BAD_REQUEST", "errors.bad_request", nil)
			} else {
				w.Header().Set("X-Robots-Tag", "noindex, nofollow")
				w.WriteHeader(http.StatusBadRequest)
				s.renderTemplate(w, r, "contact.html", s.contactData(r))
			}
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}

func (s *Server) validContactOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	referer := strings.TrimSpace(r.Header.Get("Referer"))
	fetchSite := strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))
	if origin == "" && referer == "" && fetchSite == "" {
		return false
	}
	if fetchSite != "" && fetchSite != "same-origin" {
		return false
	}
	got := origin
	isOrigin := got != ""
	if got == "" {
		got = referer
	}
	if got == "" {
		return true
	}
	parsed, err := url.Parse(got)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	if isOrigin && (parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil) {
		return false
	}
	expected, err := url.Parse(s.baseURL(r))
	return err == nil && strings.EqualFold(parsed.Scheme, expected.Scheme) && strings.EqualFold(parsed.Host, expected.Host)
}

func contactAntispam(cfg *config.Config) config.ContactAntispamConfig {
	return cfg.Server.Pages.Contact.Antispam
}

func contactConfirmDelay(a config.ContactAntispamConfig) int {
	if a.MinFillSeconds > a.ReviewMinSeconds {
		return a.MinFillSeconds
	}
	return a.ReviewMinSeconds
}

func contactWindow(a config.ContactAntispamConfig) time.Duration {
	d, err := time.ParseDuration(a.Window)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

func (s *Server) issueContactFormToken(now time.Time, a config.ContactAntispamConfig) string {
	issued := strconv.FormatInt(now.Unix(), 10)
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return ""
	}
	nonce := hex.EncodeToString(nonceBytes)
	mac := hmac.New(sha256.New, s.installSecret)
	_, _ = mac.Write([]byte("contact-form|" + issued + "|" + nonce))
	return issued + "." + nonce + "." + hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) validContactFormToken(token string, now time.Time, a config.ContactAntispamConfig) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(parts[0]) == 0 || len(parts[1]) != 32 || len(parts[2]) != sha256.Size*2 || len(s.installSecret) == 0 {
		return false
	}
	issued, nonce, signature := parts[0], parts[1], parts[2]
	seconds, err := strconv.ParseInt(issued, 10, 64)
	if err != nil {
		return false
	}
	t := time.Unix(seconds, 0)
	age := now.Sub(t)
	if (a.MinFillSeconds > 0 && age < time.Duration(a.MinFillSeconds)*time.Second) ||
		(a.MaxAgeSeconds > 0 && age > time.Duration(a.MaxAgeSeconds)*time.Second) {
		return false
	}
	got, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, s.installSecret)
	_, _ = mac.Write([]byte("contact-form|" + issued + "|" + nonce))
	return hmac.Equal(got, mac.Sum(nil))
}

func (s *Server) consumeContactFormToken(token string, now time.Time, a config.ContactAntispamConfig) bool {
	if !s.validContactFormToken(token, now, a) {
		return false
	}
	parts := strings.Split(token, ".")
	issued, _ := strconv.ParseInt(parts[0], 10, 64)
	lifetime := a.MaxAgeSeconds
	if lifetime <= 0 {
		lifetime = 86400
	}
	expires := time.Unix(issued, 0).Add(time.Duration(lifetime) * time.Second)
	state := s.contactSpamState()
	s.contactSpamMu.Lock()
	defer s.contactSpamMu.Unlock()
	for nonce, expiry := range state.usedTokens {
		if !expiry.After(now) {
			delete(state.usedTokens, nonce)
		}
	}
	return consumeContactNonce(state, "form:"+parts[1], expires, now)
}

func consumeContactNonce(state *contactSpamState, nonce string, expires, now time.Time) bool {
	for key, expiry := range state.usedTokens {
		if !expiry.After(now) {
			delete(state.usedTokens, key)
		}
	}
	if _, exists := state.usedTokens[nonce]; exists {
		return false
	}
	if len(state.usedTokens) >= 50000 {
		var oldest string
		var oldestExpiry time.Time
		for key, expiry := range state.usedTokens {
			if oldestExpiry.IsZero() || expiry.Before(oldestExpiry) {
				oldest, oldestExpiry = key, expiry
			}
		}
		delete(state.usedTokens, oldest)
	}
	state.usedTokens[nonce] = expires
	return true
}

func contactFieldsHash(fields map[string]string) [32]byte {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var normalized strings.Builder
	for _, key := range keys {
		value := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(fields[key])), " "))
		normalized.WriteString(key)
		normalized.WriteByte(0)
		normalized.WriteString(value)
		normalized.WriteByte(0)
	}
	return sha256.Sum256([]byte(normalized.String()))
}

func reviewableContactFields(fields map[string]string) map[string]string {
	review := make(map[string]string, len(fields))
	for key, value := range fields {
		switch key {
		case "csrf_token", "form_token", "website", "step", "review_token", "confirmed",
			"captcha", "captcha_token", "captcha_id", "captcha_answer", "captcha_response",
			"g-recaptcha-response", "h-captcha-response":
			continue
		}
		review[key] = value
	}
	return review
}

func (s *Server) setContactReview(data map[string]interface{}, fields map[string]string, flags string, now time.Time) {
	reviewFields := reviewableContactFields(fields)
	data["ContactReview"] = true
	data["ReviewEmail"] = fields["email"]
	data["ReviewType"] = fields["type"]
	data["ReviewSubject"] = fields["subject"]
	if data["ReviewSubject"] == "" {
		data["ReviewSubject"] = fields["summary"]
	}
	data["ReviewFields"] = reviewFields
	data["ReviewToken"] = s.issueContactReviewToken(reviewFields, flags, now)
	data["ContactFormToken"] = s.issueContactFormToken(now, config.ContactAntispamConfig{})
}

func (s *Server) contactReviewConfirmed(r *http.Request, fields map[string]string, now time.Time, a config.ContactAntispamConfig) (bool, string) {
	if fields["confirmed"] != "1" && !strings.EqualFold(fields["confirmed"], "true") {
		return false, "review"
	}
	parts := strings.Split(fields["review_token"], ".")
	if len(parts) != 4 {
		return false, "review"
	}
	flags := parts[2]
	reviewFields := reviewableContactFields(fields)
	if !s.consumeContactReviewToken(fields["review_token"], reviewFields, flags, now, a) {
		return false, "review"
	}
	return true, flags
}

func (s *Server) issueContactReviewToken(fields map[string]string, flags string, now time.Time) string {
	issued := strconv.FormatInt(now.Unix(), 10)
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return ""
	}
	nonce := hex.EncodeToString(nonceBytes)
	fieldHash := contactFieldsHash(fields)
	mac := hmac.New(sha256.New, s.installSecret)
	_, _ = mac.Write([]byte("contact-review|" + issued + "|" + nonce + "|" + flags + "|" + hex.EncodeToString(fieldHash[:])))
	return strings.Join([]string{issued, nonce, flags, hex.EncodeToString(mac.Sum(nil))}, ".")
}

func (s *Server) consumeContactReviewToken(token string, fields map[string]string, flags string, now time.Time, a config.ContactAntispamConfig) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 4 || len(parts[1]) != 32 || len(parts[3]) != sha256.Size*2 || len(s.installSecret) == 0 {
		return false
	}
	issued, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || parts[2] != flags {
		return false
	}
	created := time.Unix(issued, 0)
	age := now.Sub(created)
	if age < time.Duration(a.ReviewMinSeconds)*time.Second || (a.ReviewMaxSeconds > 0 && age > time.Duration(a.ReviewMaxSeconds)*time.Second) {
		return false
	}
	fieldHash := contactFieldsHash(fields)
	mac := hmac.New(sha256.New, s.installSecret)
	_, _ = mac.Write([]byte("contact-review|" + parts[0] + "|" + parts[1] + "|" + flags + "|" + hex.EncodeToString(fieldHash[:])))
	want, err := hex.DecodeString(parts[3])
	if err != nil || !hmac.Equal(want, mac.Sum(nil)) {
		return false
	}
	state := s.contactSpamState()
	s.contactSpamMu.Lock()
	defer s.contactSpamMu.Unlock()
	lifetime := a.ReviewMaxSeconds
	if lifetime <= 0 {
		lifetime = 86400
	}
	return consumeContactNonce(state, "review:"+parts[1], created.Add(time.Duration(lifetime)*time.Second), now)
}

func contactShapeError(fields map[string]string, required, allowed []string) string {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return "shape"
		}
	}
	for key := range fields {
		if _, ok := allowedSet[key]; !ok {
			return "shape"
		}
	}
	for _, value := range fields {
		if !utf8.ValidString(value) {
			return "shape"
		}
		for _, r := range value {
			if isContactInvisible(r) {
				return "shape"
			}
		}
	}
	for _, key := range []string{"name", "email", "subject", "message"} {
		value, ok := fields[key]
		if !ok || !utf8.ValidString(value) {
			return "shape"
		}
		if key != "message" {
			for _, r := range value {
				if unicode.IsControl(r) {
					return "shape"
				}
			}
		}
	}
	return ""
}

func contactFieldValues(values url.Values) (map[string]string, bool) {
	fields := make(map[string]string, len(values))
	for name, entries := range values {
		if len(entries) != 1 {
			return fields, false
		}
		fields[name] = entries[0]
	}
	return fields, true
}

func standardContactFields() ([]string, []string) {
	required := []string{"name", "email", "subject", "message", "type", "reported_url", "reason", "copyrighted_work", "good_faith", "accuracy", "signature", "request_kind", "csrf_token", "form_token", "website"}
	allowed := []string{"name", "email", "subject", "message", "type", "reported_url", "reason", "copyrighted_work", "good_faith", "accuracy", "signature", "request_kind", "csrf_token", "form_token", "website", "captcha", "captcha_token", "captcha_id", "captcha_answer", "g-recaptcha-response", "h-captcha-response", "step", "review_token", "confirmed"}
	return required, allowed
}

func securityContactShapeError(fields map[string]string) string {
	required := []string{"name", "email", "type", "form_token", "website", "csrf_token", "security_id", "researcher_gpg", "component", "component_other", "endpoint", "severity", "summary", "steps", "impact", "suggested_fix", "disclosure_days", "credit_preference", "credit_name", "agreement"}
	allowed := append(append([]string{}, required...), "good_faith", "accuracy", "cve_requested", "captcha", "captcha_token", "captcha_id", "captcha_answer", "g-recaptcha-response", "h-captcha-response", "step", "review_token", "confirmed")
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	for key := range fields {
		if _, ok := allowedSet[key]; !ok {
			return "shape"
		}
	}
	for _, key := range []string{"name", "email", "type", "form_token", "website", "csrf_token", "security_id", "researcher_gpg", "component", "component_other", "endpoint", "severity", "summary", "steps", "impact", "suggested_fix", "disclosure_days", "credit_preference", "credit_name"} {
		if _, ok := fields[key]; !ok {
			return "shape"
		}
	}
	if fields["type"] != "security" {
		return "shape"
	}
	if _, ok := fields["cve_requested"]; !ok {
		fields["cve_requested"] = "0"
	}
	for _, key := range []string{"name", "email"} {
		value, ok := fields[key]
		if !ok || !utf8.ValidString(value) {
			return "shape"
		}
		for _, r := range value {
			if unicode.IsControl(r) || isContactInvisible(r) {
				return "shape"
			}
		}
	}
	if !validContactEmail(fields["email"]) {
		return "shape"
	}
	for _, value := range fields {
		if !utf8.ValidString(value) {
			return "shape"
		}
		for _, r := range value {
			if isContactInvisible(r) {
				return "shape"
			}
		}
	}
	limits := map[string]int{"name": 100, "email": 254, "researcher_gpg": 16384, "component": 255, "component_other": 255, "endpoint": 2048, "summary": 200, "steps": 10000, "impact": 5000, "suggested_fix": 5000, "credit_name": 100}
	for field, limit := range limits {
		value := strings.TrimSpace(fields[field])
		if field == "researcher_gpg" && strings.HasPrefix(strings.ToLower(value), "https://") {
			limit = 2048
		}
		if utf8.RuneCountInString(value) > limit {
			return "shape"
		}
	}
	return ""
}

func standardContactAPIFields(req contactRequest) map[string]string {
	return map[string]string{
		"type": req.Type, "step": req.Step, "review_token": req.ReviewToken,
		"confirmed":    strconv.FormatBool(req.Confirmed),
		"reported_url": req.ReportedURL, "reason": req.Reason, "copyrighted_work": req.CopyrightedWork,
		"good_faith": strconv.FormatBool(req.GoodFaith), "accuracy": strconv.FormatBool(req.Accuracy),
		"signature": req.Signature, "request_kind": req.RequestKind,
		"name": req.Name, "email": req.Email, "subject": req.Subject, "message": req.Message,
		"form_token": req.FormToken, "website": req.Website, "captcha": req.Captcha,
		"captcha_token": req.CaptchaToken, "captcha_id": req.CaptchaID, "captcha_answer": req.CaptchaAnswer, "g-recaptcha-response": req.RecaptchaResponse,
		"h-captcha-response": req.HCaptchaResponse,
	}
}

func securityContactAPIFields(req contactRequest) map[string]string {
	return map[string]string{
		"step": req.Step, "review_token": req.ReviewToken,
		"confirmed": strconv.FormatBool(req.Confirmed),
		"type":      req.Type,
		"name":      req.Name, "email": req.Email, "form_token": req.FormToken, "website": req.Website,
		"security_id": req.SecurityID, "researcher_gpg": req.ResearcherGPG, "component": req.Component,
		"component_other": req.ComponentOther, "endpoint": req.Endpoint, "severity": req.Severity,
		"summary": req.Summary, "steps": req.Steps, "impact": req.Impact,
		"suggested_fix": req.SuggestedFix, "credit_preference": req.CreditPreference,
		"credit_name": req.CreditName, "captcha": req.Captcha, "captcha_token": req.CaptchaToken, "captcha_id": req.CaptchaID, "captcha_answer": req.CaptchaAnswer,
		"g-recaptcha-response": req.RecaptchaResponse, "h-captcha-response": req.HCaptchaResponse,
		"cve_requested": strconv.FormatBool(req.CVERequested), "agreement": strconv.FormatBool(req.Agreement),
		"disclosure_days": strconv.Itoa(req.DisclosureDays),
	}
}

func hasSecurityContactFields(req contactRequest) bool {
	return req.SecurityID != "" || req.ResearcherGPG != "" || req.Component != "" || req.ComponentOther != "" ||
		req.Endpoint != "" || req.Severity != "" || req.Summary != "" || req.Steps != "" || req.Impact != "" ||
		req.SuggestedFix != "" || req.CVERequested || req.DisclosureDays != 0 || req.CreditPreference != "" ||
		req.CreditName != "" || req.Agreement
}

func (s *Server) contactFormGuard(r *http.Request, cfg *config.Config, fields map[string]string, now time.Time) string {
	a := contactAntispam(cfg)
	if s.contactIsBanned(r, now) {
		metric.ContactBlockedTotal.WithLabelValues("banned").Inc()
		log.Printf("contact.spam_blocked reason=banned ip=%s", clientIP(r))
		return "banned"
	}
	if a.Honeypot && strings.TrimSpace(fields["website"]) != "" {
		if s.recordContactBlock(a, r, "honeypot", true, now) {
			return "banned"
		}
		return "honeypot"
	}
	if !s.validContactFormToken(fields["form_token"], now, a) {
		s.recordContactBlock(a, r, "fill_time", false, now)
		return "fill_time"
	}
	if !s.consumeContactFormToken(fields["form_token"], now, a) {
		s.recordContactBlock(a, r, "replay", true, now)
		return "replay"
	}
	return ""
}

func isContactInvisible(r rune) bool {
	switch r {
	case '\u200b', '\u200c', '\u200d', '\u2060', '\ufeff',
		'\u202a', '\u202b', '\u202c', '\u202d', '\u202e',
		'\u2066', '\u2067', '\u2068', '\u2069':
		return true
	default:
		return false
	}
}

func validContactEmail(value string) bool {
	if strings.ContainsAny(value, ",;<>\r\n") {
		return false
	}
	parsed, err := mail.ParseAddress(value)
	return err == nil && parsed.Address == value
}

func contactContentReason(a config.ContactAntispamConfig, name, email, subject, message string) string {
	if a.RejectMarkup {
		for _, value := range []string{name, subject, message} {
			decoded := html.UnescapeString(value)
			if strings.Contains(decoded, "<") || strings.Contains(decoded, ">") || contactMarkupPattern.MatchString(value) {
				return "markup"
			}
		}
	}
	if hasContactLink(name) || hasContactLink(subject) || (a.MaxLinks > 0 && countContactLinks(message) > a.MaxLinks) {
		return "content"
	}
	for _, host := range contactLinkHosts(message) {
		for _, blocked := range a.BlockedLinkDomains {
			blocked = strings.Trim(strings.ToLower(strings.TrimSpace(blocked)), ".")
			if blocked != "" && (host == blocked || strings.HasSuffix(host, "."+blocked)) {
				return "content"
			}
		}
	}
	for _, blocked := range a.BlockedWords {
		blocked = strings.TrimSpace(blocked)
		if blocked != "" && (strings.Contains(strings.ToLower(name), strings.ToLower(blocked)) ||
			strings.Contains(strings.ToLower(subject), strings.ToLower(blocked)) ||
			strings.Contains(strings.ToLower(message), strings.ToLower(blocked))) {
			return "content"
		}
	}
	if at := strings.LastIndex(email, "@"); at >= 0 {
		domain := strings.ToLower(email[at+1:])
		for _, blocked := range a.BlockedEmailDomains {
			blocked = strings.Trim(strings.ToLower(strings.TrimSpace(blocked)), ".")
			if blocked != "" && (domain == blocked || strings.HasSuffix(domain, "."+blocked)) {
				return "content"
			}
		}
	}
	return ""
}

func contactLinkMatches(value string) []string {
	indices := contactDomainPattern.FindAllStringIndex(value, -1)
	links := make([]string, 0, len(indices))
	for _, index := range indices {
		if index[0] > 0 && value[index[0]-1] == '@' {
			continue
		}
		links = append(links, value[index[0]:index[1]])
	}
	return links
}

func hasContactLink(value string) bool { return len(contactLinkMatches(value)) > 0 }

func countContactLinks(value string) int { return len(contactLinkMatches(value)) }

func contactLinkHosts(value string) []string {
	links := contactLinkMatches(value)
	hosts := make([]string, 0, len(links))
	for _, link := range links {
		link = strings.Trim(link, ".,;:!?()[]{}<>\"'`")
		if strings.HasPrefix(strings.ToLower(link), "www.") {
			link = link[4:]
		}
		if parsed, err := url.Parse(link); err == nil && parsed.Hostname() != "" {
			link = parsed.Hostname()
		}
		hosts = append(hosts, strings.TrimSuffix(strings.ToLower(link), "."))
	}
	return hosts
}

func (s *Server) reportContentReason(cfg *config.Config, r *http.Request, fields map[string]string) string {
	if _, _, tooLong := contactReportFieldLimitError(fields); tooLong {
		return "content"
	}
	typeName := fields["type"]
	if typeName != "abuse" && typeName != "dmca" && typeName != "privacy" {
		return "content"
	}
	if typeName == "abuse" {
		switch fields["reason"] {
		case "illegal_content", "terms_violation", "harassment", "malware_phishing", "other":
		default:
			return "content"
		}
	}
	if typeName == "privacy" {
		switch fields["request_kind"] {
		case "access", "export", "deletion", "correction", "opt_out", "other":
		default:
			return "content"
		}
	}
	if typeName == "dmca" && (strings.TrimSpace(fields["copyrighted_work"]) == "" || !config.IsTruthy(fields["good_faith"]) || !config.IsTruthy(fields["accuracy"]) || strings.TrimSpace(fields["signature"]) == "") {
		return "content"
	}
	if (typeName == "abuse" || typeName == "dmca") && strings.TrimSpace(fields["reported_url"]) == "" {
		return "content"
	}
	if fields["reported_url"] != "" {
		parsed, err := url.Parse(strings.TrimSpace(fields["reported_url"]))
		if err != nil || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" || !s.reportHostAllowed(cfg, r, parsed.Hostname()) {
			return "content"
		}
		path := parsed.EscapedPath()
		if path == "" {
			path = "/"
		}
		lowerPath := strings.ToLower(path)
		if path == "/" || lowerPath == "/server/contact" || strings.HasPrefix(lowerPath, "/api/") || strings.HasPrefix(lowerPath, "/server/admin") {
			return "content"
		}
		rctx := chi.NewRouteContext()
		if !s.router.Match(rctx, http.MethodGet, path) {
			return "content"
		}
		pattern := rctx.RoutePattern()
		patternParts := strings.Split(strings.Trim(pattern, "/"), "/")
		idPart := -1
		for i, part := range patternParts {
			if part == "{id}" {
				idPart = i
				break
			}
		}
		if idPart >= 0 && isPasteResourcePattern(pattern, cfg) {
			pathParts := strings.Split(strings.Trim(path, "/"), "/")
			if idPart >= len(pathParts) {
				return "content"
			}
			id, decodeErr := url.PathUnescape(pathParts[idPart])
			if decodeErr != nil {
				return "content"
			}
			exists, existsErr := s.pasteHandler.Exists(id)
			if existsErr != nil || !exists {
				return "content"
			}
		}
	}
	values := []string{fields["message"], fields["reported_url"], fields["copyrighted_work"]}
	joined := strings.Join(values, " ")
	a := contactAntispam(cfg)
	if a.MaxLinks > 0 && countContactLinks(joined) > a.MaxLinks {
		return "content"
	}
	if a.RejectMarkup {
		for _, value := range []string{fields["name"], fields["subject"], fields["message"], fields["reported_url"], fields["copyrighted_work"]} {
			if strings.Contains(html.UnescapeString(value), "<") || strings.Contains(html.UnescapeString(value), ">") || contactMarkupPattern.MatchString(value) {
				return "markup"
			}
		}
	}
	for _, host := range contactLinkHosts(joined) {
		if !s.reportHostAllowed(cfg, r, host) {
			return "content"
		}
	}
	return ""
}

func contactReportFieldLimitError(fields map[string]string) (string, int, bool) {
	for _, field := range []struct {
		name  string
		limit int
	}{
		{"reported_url", 2048}, {"copyrighted_work", 2000}, {"signature", 100},
	} {
		if utf8.RuneCountInString(strings.TrimSpace(fields[field.name])) > field.limit {
			return field.name, field.limit, true
		}
	}
	return "", 0, false
}

func isPasteResourcePattern(pattern string, cfg *config.Config) bool {
	if pattern == "/{id}" || pattern == "/view/{id}" || pattern == "/view/raw/{id}" ||
		pattern == "/api/paste/{id}" || pattern == "/documents/{id}" || pattern == "/pasta/{id}" ||
		pattern == "/{id}/raw" || pattern == "/raw/{id}" || pattern == "/r/{id}" ||
		pattern == "/dl/{id}" || pattern == "/download/{id}" || pattern == "/file/{id}" ||
		pattern == "/emb/{id}" || pattern == "/qr/{id}" || pattern == "/qr/{id}/image" ||
		pattern == "/remove/{id}" || pattern == "/upload/{id}" || pattern == "/p/{id}" ||
		pattern == "/url/{id}" || pattern == "/u/{id}" {
		return true
	}
	return pattern == "/api/"+cfg.Server.APIVersion+"/pastes/{id}" || pattern == "/api/"+cfg.Server.APIVersion+"/pastes/{id}/raw"
}

func (s *Server) reportHostAllowed(cfg *config.Config, r *http.Request, host string) bool {
	host = strings.Trim(strings.ToLower(host), ".")
	allowed := map[string]struct{}{}
	add := func(value string) {
		value = strings.ToLower(strings.TrimSpace(value))
		if parsed, err := url.Parse("//" + value); err == nil && parsed.Hostname() != "" {
			value = parsed.Hostname()
		}
		value = strings.Trim(value, ".")
		if value != "" {
			allowed[value] = struct{}{}
		}
	}
	add(cfg.ResolveFQDN())
	add(r.Host)
	for _, value := range strings.FieldsFunc(os.Getenv("DOMAIN"), func(r rune) bool { return r == ',' || r == ';' || unicode.IsSpace(r) }) {
		add(value)
	}
	for value := range allowed {
		if value == host {
			return true
		}
	}
	return false
}

func contactDuplicateKey(email, subject, message string) [32]byte {
	normalize := func(value string) string {
		return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
	}
	return sha256.Sum256([]byte(normalize(email) + "\x00" + normalize(subject) + "\x00" + normalize(message)))
}

func (s *Server) contactDuplicate(a config.ContactAntispamConfig, email, subject, message string, now time.Time) bool {
	window := time.Duration(a.DuplicateWindowHours) * time.Hour
	if window <= 0 {
		return false
	}
	key := contactDuplicateKey(email, subject, message)
	state := s.contactSpamState()
	s.contactSpamMu.Lock()
	defer s.contactSpamMu.Unlock()
	for k, seen := range state.duplicates {
		if now.Sub(seen) > window {
			delete(state.duplicates, k)
		}
	}
	if seen, ok := state.duplicates[key]; ok && now.Sub(seen) <= window {
		return true
	}
	if len(state.duplicates) >= 10000 {
		var oldestKey [32]byte
		var oldest time.Time
		for k, seen := range state.duplicates {
			if oldest.IsZero() || seen.Before(oldest) {
				oldestKey, oldest = k, seen
			}
		}
		delete(state.duplicates, oldestKey)
	}
	state.duplicates[key] = now
	return false
}

func (s *Server) recordContactBlock(a config.ContactAntispamConfig, r *http.Request, reason string, strike bool, now time.Time) bool {
	ip := clientIP(r)
	metric.ContactBlockedTotal.WithLabelValues(reason).Inc()
	log.Printf("contact.spam_blocked reason=%s ip=%s", reason, ip)
	if !strike || s.isOverlayRequest(r) || a.Strikes <= 0 || ip == "" {
		return false
	}
	window := contactWindow(a)
	if window <= 0 {
		return false
	}
	state := s.contactSpamState()
	s.contactSpamMu.Lock()
	defer s.contactSpamMu.Unlock()
	pruneContactSpamState(state, now, window)
	if until := state.banned[ip]; until.After(now) {
		return true
	}
	delete(state.banned, ip)
	entries := state.strikes[ip]
	kept := entries[:0]
	for _, t := range entries {
		if now.Sub(t) <= window {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	if _, exists := state.strikes[ip]; !exists && len(state.strikes) >= 50000 {
		evictOldestContactTimes(state.strikes)
	}
	state.strikes[ip] = kept
	if len(kept) < a.Strikes || a.BanHours <= 0 {
		return false
	}
	state.banned[ip] = now.Add(time.Duration(a.BanHours) * time.Hour)
	if len(state.banned) > 50000 {
		evictOldestContactExpiry(state.banned)
	}
	return true
}

func (s *Server) contactIsBanned(r *http.Request, now time.Time) bool {
	if s.isOverlayRequest(r) {
		return false
	}
	ip := clientIP(r)
	state := s.contactSpamState()
	s.contactSpamMu.Lock()
	defer s.contactSpamMu.Unlock()
	for bannedIP, until := range state.banned {
		if !until.After(now) {
			delete(state.banned, bannedIP)
		}
	}
	until := state.banned[ip]
	if until.After(now) {
		return true
	}
	delete(state.banned, ip)
	return false
}

func (s *Server) allowContactSubmission(a config.ContactAntispamConfig, r *http.Request, email string, now time.Time) string {
	return s.checkContactSubmission(a, r, email, now, true)
}

func (s *Server) checkContactSubmission(a config.ContactAntispamConfig, r *http.Request, email string, now time.Time, count bool) string {
	overlay := s.isOverlayRequest(r)
	ip := clientIP(r)
	if s.contactIsBanned(r, now) {
		return "banned"
	}
	window := contactWindow(a)
	if window <= 0 {
		return ""
	}
	state := s.contactSpamState()
	s.contactSpamMu.Lock()
	defer s.contactSpamMu.Unlock()
	pruneContactSpamState(state, now, window)
	if !overlay && a.PerIP > 0 {
		key := "ip:" + ip
		if len(state.accepted[key]) >= a.PerIP {
			return "rate_ip"
		}
	}
	if a.PerEmail > 0 {
		key := "email:" + strings.ToLower(email)
		if len(state.accepted[key]) >= a.PerEmail {
			return "rate_email"
		}
	}
	keptGlobal := state.global[:0]
	for _, t := range state.global {
		if now.Sub(t) < window {
			keptGlobal = append(keptGlobal, t)
		}
	}
	state.global = keptGlobal
	if a.Global > 0 && len(state.global) >= a.Global {
		return "rate_global"
	}
	if !count {
		return ""
	}
	if !overlay && ip != "" && a.PerIP > 0 {
		key := "ip:" + ip
		if _, exists := state.accepted[key]; !exists && len(state.accepted) >= 50000 {
			evictOldestContactTimes(state.accepted)
		}
		state.accepted[key] = append(state.accepted[key], now)
	}
	if a.PerEmail > 0 {
		key := "email:" + strings.ToLower(email)
		if _, exists := state.accepted[key]; !exists && len(state.accepted) >= 50000 {
			evictOldestContactTimes(state.accepted)
		}
		state.accepted[key] = append(state.accepted[key], now)
	}
	state.global = append(state.global, now)
	return ""
}

func pruneContactSpamState(state *contactSpamState, now time.Time, window time.Duration) {
	for _, entriesByKey := range []map[string][]time.Time{state.accepted, state.strikes} {
		for key, entries := range entriesByKey {
			kept := entries[:0]
			for _, seen := range entries {
				if window > 0 && now.Sub(seen) < window {
					kept = append(kept, seen)
				}
			}
			if len(kept) == 0 {
				delete(entriesByKey, key)
			} else {
				entriesByKey[key] = kept
			}
		}
	}
	for ip, until := range state.banned {
		if !until.After(now) {
			delete(state.banned, ip)
		}
	}
}

func evictOldestContactTimes(entries map[string][]time.Time) {
	var oldestKey string
	var oldest time.Time
	for key, times := range entries {
		if len(times) > 0 && (oldest.IsZero() || times[len(times)-1].Before(oldest)) {
			oldestKey, oldest = key, times[len(times)-1]
		}
	}
	if oldestKey != "" {
		delete(entries, oldestKey)
	}
}

func evictOldestContactExpiry(entries map[string]time.Time) {
	var oldestKey string
	var oldest time.Time
	for key, expiry := range entries {
		if oldest.IsZero() || expiry.Before(oldest) {
			oldestKey, oldest = key, expiry
		}
	}
	if oldestKey != "" {
		delete(entries, oldestKey)
	}
}

func contactRetryAfter(a config.ContactAntispamConfig) int {
	seconds := int(contactWindow(a).Seconds())
	if seconds < 1 {
		return 1
	}
	return seconds
}

func firstContactValue(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func contactBanRetryAfter(a config.ContactAntispamConfig) int {
	if a.BanHours > 0 {
		return int((time.Duration(a.BanHours) * time.Hour).Seconds())
	}
	return contactRetryAfter(a)
}
