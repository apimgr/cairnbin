package server

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/apimgr/cairnbin/src/common/i18n"
)

// pageContentHTML renders an operator's server.pages.<page>.content Markdown
// override (AI.md "Pages Configuration"). Empty content yields an empty value
// so templates fall back to their default body.
func pageContentHTML(md string) template.HTML {
	if strings.TrimSpace(md) == "" {
		return ""
	}
	return renderMarkdown(md)
}

// apiPageOK writes the canonical success envelope for the /server/* page APIs.
func apiPageOK(w http.ResponseWriter, data map[string]interface{}) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "data": data})
}

// handleAboutAPI serves GET /api/{api_version}/server/about.
func (s *Server) handleAboutAPI(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveCfg()
	b := cfg.Server.Branding
	links := make([]map[string]string, 0)
	for _, l := range b.EffectiveLinks() {
		links = append(links, map[string]string{"label": l.Label, "url": l.URL})
	}
	apiPageOK(w, map[string]interface{}{
		"name":        cfg.Web.SiteTitle,
		"version":     s.version,
		"build_date":  s.buildDate,
		"tagline":     b.EffectiveTagline(),
		"description": b.EffectiveDescription(),
		"features":    b.EffectiveFeatures(),
		"links":       links,
		"content":     cfg.Server.Pages.About.Content,
	})
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
		"content":            cfg.Server.Pages.Privacy.Content,
		"url":                s.baseURL(r) + "/server/privacy",
	})
}

// handleHelpAPI serves GET /api/{api_version}/server/help.
func (s *Server) handleHelpAPI(w http.ResponseWriter, r *http.Request) {
	base := s.baseURL(r)
	apiPageOK(w, map[string]interface{}{
		"content": s.liveCfg().Server.Pages.Help.Content,
		"url":     base + "/server/help",
		"docs": map[string]string{
			"swagger": base + "/server/docs/swagger",
			"graphql": base + "/server/docs/graphql",
		},
	})
}

// handleTermsAPI serves GET /api/{api_version}/server/terms.
func (s *Server) handleTermsAPI(w http.ResponseWriter, r *http.Request) {
	apiPageOK(w, map[string]interface{}{
		"content": s.liveCfg().Server.Pages.Terms.Content,
		"url":     s.baseURL(r) + "/server/terms",
	})
}

func apiPageError(w http.ResponseWriter, r *http.Request, status int, code, messageKey string) {
	writeJSON(w, status, map[string]interface{}{
		"ok": false, "error": code,
		"message": i18n.Translate(i18n.LangFromRequest(r), messageKey),
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
	provider := s.contactCaptchaProvider(r)
	fields := []map[string]interface{}{
		{"name": "name", "type": "text", "required": true, "max_length": 100},
		{"name": "email", "type": "email", "required": true, "max_length": 254},
		{"name": "subject", "type": "text", "required": true, "max_length": 200},
		{"name": "message", "type": "textarea", "required": true, "max_length": 5000},
		{"name": "captcha", "type": "captcha", "required": true, "max_length": nil},
	}
	if securityID := strings.TrimSpace(r.URL.Query().Get("security_id")); securityID != "" && s.validSecurityID(securityID) {
		fields = []map[string]interface{}{
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
	captcha := map[string]interface{}{"provider": provider}
	switch provider {
	case "simple":
		challenge, err := s.generateSimpleCaptcha()
		if err != nil {
			apiPageError(w, r, http.StatusInternalServerError, "SERVER_ERROR", "errors.server_error")
			return
		}
		captcha["challenge_id"] = challenge.Token
		captcha["question"] = challenge.Question
	case "recaptcha", "hcaptcha":
		captcha["site_key"] = pc.CaptchaSiteKey
	}
	apiPageOK(w, map[string]interface{}{
		"fields": fields, "captcha": captcha,
		"security_url": s.baseURL(r) + "/server/security",
		"abuse_email":  s.publicAbuseEmail(r),
	})
}

// contactRequest is the JSON body of POST /api/{api_version}/server/contact.
type contactRequest struct {
	Name              string `json:"name"`
	Email             string `json:"email"`
	Subject           string `json:"subject"`
	Message           string `json:"message"`
	CaptchaToken      string `json:"captcha_token"`
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
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		apiPageError(w, r, http.StatusBadRequest, "BAD_REQUEST", "errors.bad_request")
		return
	}
	securityID := strings.TrimSpace(req.SecurityID)
	if securityID == "" {
		securityID = strings.TrimSpace(r.URL.Query().Get("security_id"))
	}
	if securityID != "" && s.validSecurityID(securityID) {
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
				apiPageValidationFieldError(w, r, field.name, limit)
				return
			}
		}
		values := url.Values{
			"security_id": {securityID}, "name": {req.Name}, "email": {req.Email},
			"researcher_gpg": {req.ResearcherGPG}, "component": {req.Component}, "component_other": {req.ComponentOther},
			"endpoint": {req.Endpoint}, "severity": {req.Severity}, "summary": {req.Summary},
			"steps": {req.Steps}, "impact": {req.Impact}, "suggested_fix": {req.SuggestedFix},
			"captcha_token": {req.CaptchaToken}, "captcha": {req.Captcha},
			"g-recaptcha-response": {req.RecaptchaResponse}, "h-captcha-response": {req.HCaptchaResponse},
			"credit_preference": {req.CreditPreference}, "credit_name": {req.CreditName},
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
		s.handleSecurityReport(w, r, cfg)
		return
	}
	name := strings.TrimSpace(req.Name)
	from := strings.TrimSpace(req.Email)
	subject := strings.TrimSpace(req.Subject)
	message := strings.TrimSpace(req.Message)
	for _, field := range []struct {
		name  string
		value string
		limit int
	}{
		{"name", name, 100}, {"email", from, 254},
		{"subject", subject, 200}, {"message", message, 5000},
	} {
		if utf8.RuneCountInString(field.value) > field.limit {
			apiPageValidationFieldError(w, r, field.name, field.limit)
			return
		}
	}
	if name == "" || from == "" || subject == "" || message == "" {
		apiPageError(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "errors.required")
		return
	}
	if !strings.Contains(from, "@") || strings.ContainsAny(from, " \t\r\n") {
		apiPageError(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "errors.invalid_email")
		return
	}
	captchaToken := req.CaptchaToken
	if captchaToken == "" {
		captchaToken = req.ChallengeID
	}
	providerResponse := req.RecaptchaResponse
	if providerResponse == "" {
		providerResponse = req.HCaptchaResponse
	}
	if !s.verifyContactCaptcha(r, captchaToken, req.Captcha, providerResponse) {
		apiPageError(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "errors.invalid_format")
		return
	}
	if status, _ := s.deliverContact(r, cfg, name, from, subject, message); status != http.StatusOK {
		code := "SERVER_ERROR"
		if status == http.StatusServiceUnavailable {
			code = "MAINTENANCE"
		}
		messageKey := "errors.server_error"
		if code == "MAINTENANCE" {
			messageKey = "errors.maintenance_mode"
		}
		apiPageError(w, r, status, code, messageKey)
		return
	}
	apiPageOK(w, map[string]interface{}{"message": pc.SuccessMessage})
}

func apiPageValidationFieldError(w http.ResponseWriter, r *http.Request, field string, maxLength int) {
	writeJSON(w, http.StatusBadRequest, map[string]interface{}{
		"ok":      false,
		"error":   "VALIDATION_FAILED",
		"message": i18n.Translate(i18n.LangFromRequest(r), "errors.invalid_format"),
		"details": map[string]interface{}{
			"field": field, "reason": "max_length", "max_length": maxLength,
		},
	})
}
