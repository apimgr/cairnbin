package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/apimgr/cairnbin/src/common/i18n"
	"github.com/apimgr/cairnbin/src/config"
	"github.com/apimgr/cairnbin/src/database"
)

// apiPageOK writes the canonical success envelope for the /server/* page APIs.
func apiPageOK(w http.ResponseWriter, data map[string]interface{}) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "data": data})
}

// handleAboutAPI serves GET /api/{api_version}/server/about.
func (s *Server) handleAboutAPI(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveCfg()
	entry := cfg.Server.Branding.Content["about"]
	content, format := brandingContentSource(entry, i18n.LangFromRequest(r))
	apiPageOK(w, map[string]interface{}{
		"title":   cfg.Server.Branding.EffectiveTitle(),
		"content": s.expandFooterVars(content),
		"format":  apiPageFormat(format),
	})
}

func apiPageFormat(format string) string {
	switch format {
	case "html":
		return "html"
	case "txt":
		return "text"
	default:
		return "markdown"
	}
}

// handlePrivacyAPI serves GET /api/{api_version}/server/privacy.
func (s *Server) handlePrivacyAPI(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveCfg()
	p := cfg.Server.Privacy
	services := make([]map[string]string, 0)
	for _, t := range cfg.EffectiveThirdParty() {
		services = append(services, map[string]string{
			"name": t.Name, "purpose": t.Purpose, "data_sent": t.DataSent, "policy_url": t.PolicyURL,
		})
	}
	apiPageOK(w, map[string]interface{}{
		"data_sold":          p.Data.Sold,
		"stored_on_server":   p.Data.StoredOnServer,
		"retention":          p.Retention.Period,
		"export_available":   p.Retention.ExportAvailable,
		"deletion_available": p.Retention.DeletionAvailable,
		"analytics":          cfg.Server.Tracking.Enabled(),
		"third_parties":      services,
		"url":                s.baseURL(r) + "/server/privacy",
	})
}

// handleHelpAPI serves GET /api/{api_version}/server/help.
func (s *Server) handleHelpAPI(w http.ResponseWriter, r *http.Request) {
	base := s.baseURL(r)
	content := s.liveCfg().Server.Pages.Help.Content
	apiPageOK(w, map[string]interface{}{
		"title":   s.liveCfg().Server.Branding.EffectiveTitle(),
		"content": content,
		"format":  "markdown",
		"url":     base + "/server/help",
		"docs": map[string]string{
			"swagger": base + "/server/docs/swagger",
			"graphql": base + "/server/docs/graphql",
		},
	})
}

// handleTermsAPI serves GET /api/{api_version}/server/terms.
func (s *Server) handleTermsAPI(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveCfg()
	entry, hasEntry := cfg.Server.Branding.Content["terms"]
	content, format := brandingContentSource(entry, i18n.LangFromRequest(r))
	if !hasEntry {
		content = builtInTermsMarkdown(i18n.LangFromRequest(r))
		format = "md"
	}
	apiPageOK(w, map[string]interface{}{
		"title":   cfg.Server.Branding.EffectiveTitle(),
		"content": content,
		"format":  apiPageFormat(format),
		"url":     s.baseURL(r) + "/server/terms",
	})
}

func builtInTermsMarkdown(lang string) string {
	return strings.Join([]string{
		"## " + i18n.Translate(lang, "terms.acceptance"),
		i18n.Translate(lang, "terms.acceptance_text"),
		"## " + i18n.Translate(lang, "terms.acceptable_use"),
		i18n.Translate(lang, "terms.use_intro"),
		"- " + i18n.Translate(lang, "terms.use_illegal"),
		"- " + i18n.Translate(lang, "terms.use_malware"),
		"- " + i18n.Translate(lang, "terms.use_credentials"),
		"- " + i18n.Translate(lang, "terms.use_spam"),
		i18n.Translate(lang, "terms.use_note"),
		"## " + i18n.Translate(lang, "terms.liability"),
		i18n.Translate(lang, "terms.liability_text"),
		"## " + i18n.Translate(lang, "terms.changes"),
		i18n.Translate(lang, "terms.changes_text"),
		"## " + i18n.Translate(lang, "terms.governing_law"),
		i18n.Translate(lang, "terms.governing_law_text"),
	}, "\n\n")
}

// handleDPOAPI serves the public data-protection contact description.
func (s *Server) handleDPOAPI(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveCfg()
	apiPageOK(w, map[string]interface{}{
		"name":        cfg.Server.Contact.DPO.Name,
		"email":       cfg.DPOEmailPublic(),
		"privacy_url": s.baseURL(r) + "/server/privacy",
	})
}

// handleSecurityAPI serves the public security.txt metadata in JSON form.
func (s *Server) handleSecurityAPI(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveCfg()
	base := strings.TrimRight(s.baseURL(r), "/")
	contacts := make([]map[string]string, 0, 3)
	if reportURL := cfg.SecurityReportURL(); reportURL != "" {
		contacts = append(contacts, map[string]string{"type": "github", "value": reportURL})
	}
	if id := s.currentSecurityID(); id != "" && cfg.Server.Pages.Contact.Enabled {
		contacts = append(contacts, map[string]string{"type": "contact_form", "value": base + "/server/contact?security_id=" + id})
	}
	if email := s.publicSecurityEmail(r); email != "" {
		contacts = append(contacts, map[string]string{"type": "email", "value": email})
	}
	expires := strings.TrimSpace(cfg.Web.Security.Expires)
	if expires == "" {
		expires = time.Now().AddDate(1, 0, 0).UTC().Format(time.RFC3339)
	}
	var pgpData map[string]interface{} = map[string]interface{}{"available": false, "url": nil, "fingerprint": nil}
	if s.hasPGPKey() {
		pgpData["available"] = true
		pgpData["url"] = base + "/.well-known/pgp-key.asc"
		if keypair, err := s.db.GetSecurityKeypair(); err == nil && keypair != nil && keypair.Fingerprint != "" {
			pgpData["fingerprint"] = strings.ToUpper(keypair.Fingerprint)
		}
	}
	data := map[string]interface{}{
		"contacts": contacts, "expires": expires, "pgp": pgpData,
		"policy_url": base + "/server/security/policy", "thanks_url": base + "/server/security/thanks",
	}
	if cfg.Server.Pages.Contact.Enabled {
		data["report_url"] = base + "/server/contact"
	}
	apiPageOK(w, data)
}

// handleSecurityPolicyAPI serves the disclosure policy in Markdown form.
func (s *Server) handleSecurityPolicyAPI(w http.ResponseWriter, r *http.Request) {
	lang := i18n.LangFromRequest(r)
	var policy strings.Builder
	fmt.Fprintf(&policy, "# %s\n\n", i18n.Translate(lang, "security.policy_title"))
	sections := []struct {
		title string
		keys  []string
	}{
		{"security.reporting_vuln", []string{"security.reporting_text"}},
		{"security.in_scope", []string{"security.in_scope_1", "security.in_scope_2", "security.in_scope_3", "security.in_scope_4"}},
		{"security.out_of_scope", []string{"security.out_scope_1", "security.out_scope_2", "security.out_scope_3", "security.out_scope_4", "security.out_scope_5"}},
		{"security.safe_harbor", []string{"security.safe_harbor_text"}},
	}
	for _, section := range sections {
		fmt.Fprintf(&policy, "## %s\n\n", i18n.Translate(lang, section.title))
		for _, key := range section.keys {
			fmt.Fprintf(&policy, "%s\n\n", i18n.Translate(lang, key))
		}
	}
	apiPageOK(w, map[string]interface{}{"policy": policy.String()})
}

// handleSecurityThanksAPI serves the public researcher acknowledgment list.
func (s *Server) handleSecurityThanksAPI(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"ok": false, "error": "INTERNAL_ERROR"})
		return
	}
	reports, err := s.db.ListDisclosedSecurityReports()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"ok": false, "error": "INTERNAL_ERROR"})
		return
	}
	entries := make([]map[string]string, 0)
	for _, report := range reports {
		entry := buildAckEntries([]*database.SecurityReport{report})[0]
		date := ""
		if report.DisclosedAt != nil {
			date = report.DisclosedAt.Format("2006-01-02")
		}
		entries = append(entries, map[string]string{
			"credit": entry.Display, "date": date, "severity": strings.ToLower(entry.Severity),
		})
	}
	apiPageOK(w, map[string]interface{}{"entries": entries})
}

func apiPageError(w http.ResponseWriter, r *http.Request, status int, code, messageKey string) {
	writeJSON(w, status, map[string]interface{}{
		"ok": false, "error": code,
		"message": i18n.Translate(i18n.LangFromRequest(r), messageKey),
	})
}

func (s *Server) contactCaptchaDescriptor(r *http.Request, cfg *config.Config) (map[string]interface{}, error) {
	provider := s.contactCaptchaProvider(r)
	captcha := map[string]interface{}{"provider": provider}
	challenge, err := s.generateSimpleCaptcha()
	if err != nil {
		return nil, err
	}
	simple := map[string]string{"provider": "simple", "captcha_id": challenge.Token, "question": challenge.Question}
	switch provider {
	case "simple":
		captcha["captcha_id"] = challenge.Token
		captcha["question"] = challenge.Question
	case "recaptcha", "hcaptcha":
		captcha["site_key"] = cfg.Server.Pages.Contact.CaptchaSiteKey
		captcha["fallback"] = simple
	}
	return captcha, nil
}

func (s *Server) contactRefreshData(r *http.Request, cfg *config.Config) (map[string]interface{}, error) {
	captcha, err := s.contactCaptchaDescriptor(r, cfg)
	if err != nil {
		return nil, err
	}
	a := contactAntispam(cfg)
	return map[string]interface{}{
		"form_token": s.issueContactFormToken(time.Now(), a),
		"captcha":    captcha,
	}, nil
}

func (s *Server) contactAPIReviewData(r *http.Request, cfg *config.Config, fields map[string]string, flags string, now time.Time) (map[string]interface{}, error) {
	data, err := s.contactRefreshData(r, cfg)
	if err != nil {
		return nil, err
	}
	data["review"] = map[string]string{
		"type": fields["type"], "email": fields["email"],
		"subject": firstContactValue(fields["subject"], fields["summary"]),
	}
	data["review_token"] = s.issueContactReviewToken(reviewableContactFields(fields), flags, now)
	data["confirm_after_seconds"] = contactConfirmDelay(contactAntispam(cfg))
	return data, nil
}

func (s *Server) contactAPISuccessData(r *http.Request, cfg *config.Config, values map[string]interface{}) (map[string]interface{}, error) {
	data, err := s.contactRefreshData(r, cfg)
	if err != nil {
		return nil, err
	}
	for key, value := range values {
		data[key] = value
	}
	return data, nil
}

func (s *Server) writeContactAPIErrorWithRefresh(w http.ResponseWriter, r *http.Request, cfg *config.Config, status int, code, messageKey string, details map[string]interface{}) {
	data, err := s.contactRefreshData(r, cfg)
	if err != nil {
		apiPageError(w, r, http.StatusInternalServerError, "SERVER_ERROR", "errors.server_error")
		return
	}
	if details != nil {
		data["details"] = details
	}
	writeJSON(w, status, map[string]interface{}{
		"ok": false, "error": code,
		"message": i18n.Translate(i18n.LangFromRequest(r), messageKey),
		"data":    data,
	})
}

// handleContactAPIGet serves GET /api/{api_version}/server/contact with the
// public form definition and a fresh challenge when simple captcha is active.
func (s *Server) handleContactAPIGet(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveCfg()
	pc := cfg.Server.Pages.Contact
	if !pc.Enabled {
		apiPageError(w, r, http.StatusNotFound, "NOT_FOUND", "errors.not_found")
		return
	}
	fields := []map[string]interface{}{
		{"name": "form_token", "type": "hidden", "required": true, "max_length": 128},
		{"name": "website", "type": "text", "required": false, "max_length": 200},
		{"name": "name", "type": "text", "required": true, "max_length": 100},
		{"name": "email", "type": "email", "required": true, "max_length": 254},
		{"name": "type", "type": "select", "required": true, "max_length": nil},
		{"name": "reported_url", "type": "url", "required": false, "max_length": 2048, "required_when": map[string]interface{}{"field": "type", "in": []string{"abuse", "dmca"}}},
		{"name": "reason", "type": "select", "required": false, "max_length": nil, "required_when": map[string]interface{}{"field": "type", "equals": "abuse"}},
		{"name": "copyrighted_work", "type": "textarea", "required": false, "max_length": 2000, "required_when": map[string]interface{}{"field": "type", "equals": "dmca"}},
		{"name": "good_faith", "type": "checkbox", "required": false, "max_length": nil, "required_when": map[string]interface{}{"field": "type", "equals": "dmca"}},
		{"name": "accuracy", "type": "checkbox", "required": false, "max_length": nil, "required_when": map[string]interface{}{"field": "type", "equals": "dmca"}},
		{"name": "signature", "type": "text", "required": false, "max_length": 100, "required_when": map[string]interface{}{"field": "type", "equals": "dmca"}},
		{"name": "request_kind", "type": "select", "required": false, "max_length": nil, "required_when": map[string]interface{}{"field": "type", "equals": "privacy"}},
		{"name": "subject", "type": "text", "required": true, "max_length": 200},
		{"name": "message", "type": "textarea", "required": true, "max_length": 5000},
		{"name": "captcha", "type": "captcha", "required": true, "max_length": nil},
	}
	types := []string{"abuse", "dmca", "privacy"}
	if securityID := strings.TrimSpace(r.URL.Query().Get("security_id")); securityID != "" && s.validSecurityID(securityID) {
		types = []string{"security"}
		fields = []map[string]interface{}{
			{"name": "type", "type": "hidden", "required": true, "max_length": nil},
			{"name": "name", "type": "text", "required": true, "max_length": 100},
			{"name": "email", "type": "email", "required": true, "max_length": 254},
			{"name": "researcher_gpg", "type": "textarea", "required": false, "max_length": 16384},
			{"name": "component", "type": "select", "required": true, "max_length": 255},
			{"name": "component_other", "type": "text", "required": false, "max_length": 255},
			{"name": "endpoint", "type": "text", "required": false, "max_length": 2048},
			{"name": "severity", "type": "select", "required": true, "max_length": nil},
			{"name": "summary", "type": "text", "required": true, "max_length": 200},
			{"name": "steps", "type": "textarea", "required": true, "max_length": 10000},
			{"name": "impact", "type": "textarea", "required": true, "max_length": 5000},
			{"name": "suggested_fix", "type": "textarea", "required": false, "max_length": 5000},
			{"name": "cve_requested", "type": "checkbox", "required": false, "max_length": nil},
			{"name": "disclosure_days", "type": "number", "required": false, "max_length": nil},
			{"name": "credit_preference", "type": "select", "required": true, "max_length": nil},
			{"name": "credit_name", "type": "text", "required": false, "max_length": 100},
			{"name": "agreement", "type": "checkbox", "required": true, "max_length": nil},
			{"name": "security_id", "type": "hidden", "required": true, "max_length": nil},
			{"name": "captcha", "type": "captcha", "required": true, "max_length": nil},
		}
	}
	captcha, err := s.contactCaptchaDescriptor(r, cfg)
	if err != nil {
		apiPageError(w, r, http.StatusInternalServerError, "SERVER_ERROR", "errors.server_error")
		return
	}
	apiPageOK(w, map[string]interface{}{
		"fields": fields, "captcha": captcha,
		"form_token":   s.issueContactFormToken(time.Now(), contactAntispam(cfg)),
		"types":        types,
		"security_url": s.baseURL(r) + "/server/security",
		"abuse_email":  s.publicAbuseEmail(r),
	})
}

// contactRequest is the JSON body of POST /api/{api_version}/server/contact.
type contactRequest struct {
	Step              string `json:"step"`
	ReviewToken       string `json:"review_token"`
	Confirmed         bool   `json:"confirmed"`
	Type              string `json:"type"`
	ReportedURL       string `json:"reported_url"`
	Reason            string `json:"reason"`
	CopyrightedWork   string `json:"copyrighted_work"`
	GoodFaith         bool   `json:"good_faith"`
	Accuracy          bool   `json:"accuracy"`
	Signature         string `json:"signature"`
	RequestKind       string `json:"request_kind"`
	Name              string `json:"name"`
	Email             string `json:"email"`
	Subject           string `json:"subject"`
	Message           string `json:"message"`
	CaptchaToken      string `json:"captcha_token"`
	CaptchaID         string `json:"captcha_id"`
	CaptchaAnswer     string `json:"captcha_answer"`
	ChallengeID       string `json:"challenge_id"`
	Captcha           string `json:"captcha"`
	RecaptchaResponse string `json:"g-recaptcha-response"`
	HCaptchaResponse  string `json:"h-captcha-response"`
	SecurityID        string `json:"security_id"`
	ResearcherGPG     string `json:"researcher_gpg"`
	Component         string `json:"component"`
	ComponentOther    string `json:"component_other"`
	Endpoint          string `json:"endpoint"`
	Severity          string `json:"severity"`
	Summary           string `json:"summary"`
	Steps             string `json:"steps"`
	Impact            string `json:"impact"`
	SuggestedFix      string `json:"suggested_fix"`
	CVERequested      bool   `json:"cve_requested"`
	DisclosureDays    int    `json:"disclosure_days"`
	CreditPreference  string `json:"credit_preference"`
	CreditName        string `json:"credit_name"`
	Agreement         bool   `json:"agreement"`
	FormToken         string `json:"form_token"`
	Website           string `json:"website"`
}

// handleContactAPI serves POST /api/{api_version}/server/contact. It applies
// the same validation, captcha and delivery rules as the /server/contact form.
func (s *Server) handleContactAPI(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveCfg()
	pc := cfg.Server.Pages.Contact
	if !pc.Enabled {
		apiPageError(w, r, http.StatusNotFound, "NOT_FOUND", "errors.not_found")
		return
	}
	var req contactRequest
	r.Body = http.MaxBytesReader(w, r.Body, contactBodyLimit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		s.recordContactBlock(contactAntispam(cfg), r, "shape", false, time.Now())
		s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusBadRequest, "BAD_REQUEST", "errors.bad_request", nil)
		return
	}
	securityID := strings.TrimSpace(req.SecurityID)
	if securityID == "" {
		securityID = strings.TrimSpace(r.URL.Query().Get("security_id"))
	}
	if securityID != "" && s.validSecurityID(securityID) {
		fields := securityContactAPIFields(req)
		fields["security_id"] = securityID
		step := req.Step
		if step == "" {
			step = "submit"
		}
		fields["step"] = step
		if securityContactShapeError(fields) != "" {
			s.recordContactBlock(contactAntispam(cfg), r, "shape", false, time.Now())
			s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusBadRequest, "BAD_REQUEST", "errors.bad_request", nil)
			return
		}
		guard := s.contactFormGuard(r, cfg, fields, time.Now())
		if guard == "honeypot" {
			data, err := s.contactAPIReviewData(r, cfg, fields, "d", time.Now())
			if err != nil {
				apiPageError(w, r, http.StatusInternalServerError, "SERVER_ERROR", "errors.server_error")
				return
			}
			apiPageOK(w, data)
			return
		}
		if guard != "" {
			if guard == "banned" {
				w.Header().Set("Retry-After", strconv.Itoa(contactBanRetryAfter(contactAntispam(cfg))))
				s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusTooManyRequests, "RATE_LIMITED", "errors.rate_limited", nil)
			} else if guard == "fill_time" {
				s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusBadRequest, "VALIDATION_FAILED", "errors.invalid_format", nil)
			} else {
				s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusBadRequest, "VALIDATION_FAILED", "errors.invalid_format", nil)
			}
			return
		}
		a := contactAntispam(cfg)
		now := time.Now()
		if step == "submit" {
			providerResponse := req.RecaptchaResponse
			if providerResponse == "" {
				providerResponse = req.HCaptchaResponse
			}
			captchaToken := req.CaptchaID
			if captchaToken == "" {
				captchaToken = req.CaptchaToken
			}
			if captchaToken == "" {
				captchaToken = req.ChallengeID
			}
			captchaAnswer := req.CaptchaAnswer
			if captchaAnswer == "" {
				captchaAnswer = req.Captcha
			}
			if !s.verifyContactCaptcha(r, captchaToken, captchaAnswer, providerResponse) {
				s.recordContactBlock(a, r, "captcha", true, now)
				s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusBadRequest, "VALIDATION_FAILED", "errors.invalid_format", map[string]interface{}{"field": "captcha"})
				return
			}
			if reason := s.checkContactSubmission(a, r, req.Email, now, false); reason != "" {
				s.recordContactBlock(a, r, reason, false, now)
				w.Header().Set("Retry-After", strconv.Itoa(contactRetryAfter(a)))
				s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusTooManyRequests, "RATE_LIMITED", "errors.rate_limited", nil)
				return
			}
			token := s.issueContactReviewToken(reviewableContactFields(fields), "", now)
			refresh, refreshErr := s.contactRefreshData(r, cfg)
			if refreshErr != nil {
				apiPageError(w, r, http.StatusInternalServerError, "SERVER_ERROR", "errors.server_error")
				return
			}
			refresh["review"] = map[string]string{"type": "security", "email": req.Email, "subject": req.Summary}
			refresh["review_token"] = token
			refresh["confirm_after_seconds"] = contactConfirmDelay(a)
			apiPageOK(w, refresh)
			return
		}
		fields["review_token"] = req.ReviewToken
		fields["confirmed"] = strconv.FormatBool(req.Confirmed)
		valid, flags := s.contactReviewConfirmed(r, fields, now, a)
		if !valid {
			s.recordContactBlock(a, r, "review", true, now)
			s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusBadRequest, "VALIDATION_FAILED", "errors.invalid_format", nil)
			return
		}
		if flags == "d" {
			trackingID, err := randomHex(8)
			if err != nil {
				apiPageError(w, r, http.StatusInternalServerError, "SERVER_ERROR", "errors.server_error")
				return
			}
			data, err := s.contactAPISuccessData(r, cfg, map[string]interface{}{"tracking_id": "sec_" + trackingID})
			if err != nil {
				apiPageError(w, r, http.StatusInternalServerError, "SERVER_ERROR", "errors.server_error")
				return
			}
			apiPageOK(w, data)
			return
		}
		securityFields := []struct {
			name, value string
			limit       int
		}{
			{"name", req.Name, 100}, {"email", req.Email, 254}, {"researcher_gpg", req.ResearcherGPG, 16384},
			{"component", req.Component, 255}, {"component_other", req.ComponentOther, 255}, {"endpoint", req.Endpoint, 2048},
			{"summary", req.Summary, 200}, {"steps", req.Steps, 10000}, {"impact", req.Impact, 5000},
			{"suggested_fix", req.SuggestedFix, 5000}, {"credit_name", req.CreditName, 100},
		}
		for _, field := range securityFields {
			if field.name == "credit_name" && req.CreditPreference != "handle" {
				continue
			}
			limit := field.limit
			if field.name == "researcher_gpg" && strings.HasPrefix(strings.ToLower(strings.TrimSpace(field.value)), "https://") {
				limit = 2048
			}
			if utf8.RuneCountInString(strings.TrimSpace(field.value)) > limit {
				s.recordContactBlock(contactAntispam(cfg), r, "shape", false, now)
				s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusBadRequest, "VALIDATION_FAILED", "errors.invalid_format", map[string]interface{}{"field": field.name, "reason": "max_length", "max_length": limit})
				return
			}
		}
		values := url.Values{
			"security_id": {securityID}, "name": {req.Name}, "email": {req.Email},
			"researcher_gpg": {req.ResearcherGPG}, "component": {req.Component}, "component_other": {req.ComponentOther},
			"endpoint": {req.Endpoint}, "severity": {req.Severity}, "summary": {req.Summary},
			"steps": {req.Steps}, "impact": {req.Impact}, "suggested_fix": {req.SuggestedFix},
			"captcha_token": {firstContactValue(req.CaptchaID, req.CaptchaToken, req.ChallengeID)}, "captcha_id": {firstContactValue(req.CaptchaID, req.CaptchaToken, req.ChallengeID)}, "captcha_answer": {firstContactValue(req.CaptchaAnswer, req.Captcha)}, "captcha": {req.Captcha},
			"g-recaptcha-response": {req.RecaptchaResponse}, "h-captcha-response": {req.HCaptchaResponse},
			"credit_preference": {req.CreditPreference}, "credit_name": {req.CreditName},
			"form_token": {req.FormToken}, "website": {req.Website},
		}
		if req.CVERequested {
			values.Set("cve_requested", "1")
		}
		if req.Agreement {
			values.Set("agreement", "1")
		}
		if req.DisclosureDays != 0 {
			values.Set("disclosure_days", strconv.Itoa(req.DisclosureDays))
		}
		r.PostForm = values
		r.Form = values
		s.handleSecurityReport(w, markContactCaptchaVerified(r), cfg)
		return
	}
	if hasSecurityContactFields(req) {
		s.recordContactBlock(contactAntispam(cfg), r, "shape", false, time.Now())
		s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusBadRequest, "BAD_REQUEST", "errors.bad_request", nil)
		return
	}
	name := strings.TrimSpace(req.Name)
	from := strings.TrimSpace(req.Email)
	subject := strings.TrimSpace(req.Subject)
	message := strings.TrimSpace(req.Message)
	fields := standardContactAPIFields(req)
	if contactShapeError(fields, []string{"name", "email", "subject", "message", "type", "reported_url", "reason", "copyrighted_work", "good_faith", "accuracy", "signature", "request_kind"}, []string{"name", "email", "subject", "message", "type", "reported_url", "reason", "copyrighted_work", "good_faith", "accuracy", "signature", "request_kind", "form_token", "website", "captcha", "captcha_token", "captcha_id", "captcha_answer", "g-recaptcha-response", "h-captcha-response", "step", "review_token", "confirmed"}) != "" {
		s.recordContactBlock(contactAntispam(cfg), r, "shape", false, time.Now())
		s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusBadRequest, "BAD_REQUEST", "errors.bad_request", nil)
		return
	}
	for _, field := range []struct {
		name  string
		value string
		limit int
	}{
		{"name", name, 100}, {"email", from, 254},
		{"subject", subject, 200}, {"message", message, 5000},
	} {
		if utf8.RuneCountInString(field.value) > field.limit {
			s.recordContactBlock(contactAntispam(cfg), r, "shape", false, time.Now())
			s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusBadRequest, "VALIDATION_FAILED", "errors.invalid_format", map[string]interface{}{"field": field.name, "reason": "max_length", "max_length": field.limit})
			return
		}
	}
	if name == "" || from == "" || subject == "" || message == "" {
		s.recordContactBlock(contactAntispam(cfg), r, "shape", false, time.Now())
		s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusBadRequest, "VALIDATION_FAILED", "errors.required", nil)
		return
	}
	if !validContactEmail(from) {
		s.recordContactBlock(contactAntispam(cfg), r, "shape", false, time.Now())
		s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusBadRequest, "VALIDATION_FAILED", "errors.invalid_email", nil)
		return
	}
	guard := s.contactFormGuard(r, cfg, fields, time.Now())
	step := req.Step
	if step == "" {
		step = "submit"
	}
	fields["step"] = step
	if guard == "honeypot" {
		data, err := s.contactAPIReviewData(r, cfg, fields, "d", time.Now())
		if err != nil {
			apiPageError(w, r, http.StatusInternalServerError, "SERVER_ERROR", "errors.server_error")
			return
		}
		apiPageOK(w, data)
		return
	}
	if guard != "" {
		if guard == "banned" {
			w.Header().Set("Retry-After", strconv.Itoa(contactBanRetryAfter(contactAntispam(cfg))))
			s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusTooManyRequests, "RATE_LIMITED", "errors.rate_limited", nil)
		} else if guard == "fill_time" {
			s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusBadRequest, "VALIDATION_FAILED", "errors.invalid_format", nil)
		} else {
			s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusBadRequest, "VALIDATION_FAILED", "errors.invalid_format", nil)
		}
		return
	}
	a := contactAntispam(cfg)
	now := time.Now()
	captchaToken := req.CaptchaToken
	if captchaToken == "" {
		captchaToken = req.CaptchaID
	}
	if captchaToken == "" {
		captchaToken = req.ChallengeID
	}
	if captchaToken == "" {
		captchaToken = req.CaptchaToken
	}
	providerResponse := req.RecaptchaResponse
	if providerResponse == "" {
		providerResponse = req.HCaptchaResponse
	}
	captchaAnswer := req.CaptchaAnswer
	if captchaAnswer == "" {
		captchaAnswer = req.Captcha
	}
	if step == "submit" && !s.verifyContactCaptcha(r, captchaToken, captchaAnswer, providerResponse) {
		banned := s.recordContactBlock(contactAntispam(cfg), r, "captcha", true, time.Now())
		if banned {
			w.Header().Set("Retry-After", strconv.Itoa(contactBanRetryAfter(contactAntispam(cfg))))
			s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusTooManyRequests, "RATE_LIMITED", "errors.rate_limited", map[string]interface{}{"field": "captcha"})
			return
		}
		s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusBadRequest, "VALIDATION_FAILED", "errors.invalid_format", map[string]interface{}{"field": "captcha"})
		return
	}
	if step == "submit" {
		if reason := contactContentReason(a, name, from, subject, message); reason != "" {
			banned := s.recordContactBlock(a, r, reason, true, time.Now())
			if banned {
				w.Header().Set("Retry-After", strconv.Itoa(contactBanRetryAfter(a)))
				s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusTooManyRequests, "RATE_LIMITED", "errors.rate_limited", nil)
			} else {
				s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusBadRequest, "VALIDATION_FAILED", "errors.invalid_format", nil)
			}
			return
		}
		if field, limit, tooLong := contactReportFieldLimitError(fields); tooLong {
			s.recordContactBlock(a, r, "shape", false, now)
			s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusBadRequest, "VALIDATION_FAILED", "errors.invalid_format", map[string]interface{}{"field": field, "reason": "max_length", "max_length": limit})
			return
		}
		if reason := s.reportContentReason(cfg, r, fields); reason != "" {
			s.recordContactBlock(a, r, reason, true, now)
			s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusBadRequest, "VALIDATION_FAILED", "errors.invalid_format", nil)
			return
		}
		if reason := s.checkContactSubmission(a, r, from, now, false); reason != "" {
			s.recordContactBlock(a, r, reason, false, now)
			w.Header().Set("Retry-After", strconv.Itoa(contactRetryAfter(a)))
			s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusTooManyRequests, "RATE_LIMITED", "errors.rate_limited", nil)
			return
		}
		data, err := s.contactAPIReviewData(r, cfg, fields, "", now)
		if err != nil {
			apiPageError(w, r, http.StatusInternalServerError, "SERVER_ERROR", "errors.server_error")
			return
		}
		apiPageOK(w, data)
		return
	}
	if step != "confirm" {
		s.recordContactBlock(a, r, "review", true, now)
		s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusBadRequest, "VALIDATION_FAILED", "errors.invalid_format", nil)
		return
	}
	fields["review_token"] = req.ReviewToken
	fields["confirmed"] = strconv.FormatBool(req.Confirmed)
	valid, flags := s.contactReviewConfirmed(r, fields, now, a)
	if !valid {
		s.recordContactBlock(a, r, "review", true, now)
		s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusBadRequest, "VALIDATION_FAILED", "errors.invalid_format", nil)
		return
	}
	if flags == "d" {
		data, err := s.contactAPISuccessData(r, cfg, map[string]interface{}{"message": pc.SuccessMessage})
		if err != nil {
			apiPageError(w, r, http.StatusInternalServerError, "SERVER_ERROR", "errors.server_error")
			return
		}
		apiPageOK(w, data)
		return
	}
	if step != "submit" && step != "confirm" {
		s.recordContactBlock(a, r, "review", true, now)
		s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusBadRequest, "VALIDATION_FAILED", "errors.invalid_format", nil)
		return
	}
	if step == "confirm" {
		if field, limit, tooLong := contactReportFieldLimitError(fields); tooLong {
			s.recordContactBlock(a, r, "shape", false, now)
			s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusBadRequest, "VALIDATION_FAILED", "errors.invalid_format", map[string]interface{}{"field": field, "reason": "max_length", "max_length": limit})
			return
		}
		if reason := s.reportContentReason(cfg, r, fields); reason != "" {
			s.recordContactBlock(a, r, reason, true, now)
			s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusBadRequest, "VALIDATION_FAILED", "errors.invalid_format", nil)
			return
		}
		if reason := contactContentReason(a, name, from, subject, message); reason != "" {
			s.recordContactBlock(a, r, reason, true, now)
			s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusBadRequest, "VALIDATION_FAILED", "errors.invalid_format", nil)
			return
		}
	}
	if s.contactDuplicate(a, from, subject, message, now) {
		banned := s.recordContactBlock(a, r, "duplicate", true, now)
		if banned {
			w.Header().Set("Retry-After", strconv.Itoa(contactBanRetryAfter(a)))
			s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusTooManyRequests, "RATE_LIMITED", "errors.rate_limited", nil)
			return
		}
		data, err := s.contactAPISuccessData(r, cfg, map[string]interface{}{"message": pc.SuccessMessage})
		if err != nil {
			apiPageError(w, r, http.StatusInternalServerError, "SERVER_ERROR", "errors.server_error")
			return
		}
		apiPageOK(w, data)
		return
	}
	if reason := s.allowContactSubmission(a, r, from, now); reason != "" {
		s.recordContactBlock(a, r, reason, false, now)
		w.Header().Set("Retry-After", strconv.Itoa(contactRetryAfter(a)))
		s.writeContactAPIErrorWithRefresh(w, r, cfg, http.StatusTooManyRequests, "RATE_LIMITED", "errors.rate_limited", nil)
		return
	}
	if status, _ := s.deliverReportContact(r, cfg, fields); status != http.StatusOK {
		code := "SERVER_ERROR"
		if status == http.StatusServiceUnavailable {
			code = "MAINTENANCE"
		}
		messageKey := "errors.server_error"
		if code == "MAINTENANCE" {
			messageKey = "errors.maintenance_mode"
		}
		s.writeContactAPIErrorWithRefresh(w, r, cfg, status, code, messageKey, nil)
		return
	}
	data, err := s.contactAPISuccessData(r, cfg, map[string]interface{}{"message": pc.SuccessMessage})
	if err != nil {
		apiPageError(w, r, http.StatusInternalServerError, "SERVER_ERROR", "errors.server_error")
		return
	}
	apiPageOK(w, data)
}
