package server

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strings"

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

// contactRequest is the JSON body of POST /api/{api_version}/server/contact.
type contactRequest struct {
	Name         string `json:"name"`
	Email        string `json:"email"`
	Subject      string `json:"subject"`
	Message      string `json:"message"`
	CaptchaToken string `json:"captcha_token"`
	Captcha      string `json:"captcha"`
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
	name := strings.TrimSpace(req.Name)
	from := strings.TrimSpace(req.Email)
	subject := strings.TrimSpace(req.Subject)
	message := strings.TrimSpace(req.Message)
	if name == "" || from == "" || subject == "" || message == "" {
		apiPageError(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "errors.required")
		return
	}
	if !strings.Contains(from, "@") || strings.ContainsAny(from, " \t\r\n") {
		apiPageError(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "errors.invalid_email")
		return
	}
	if pc.Captcha == "simple" && !s.validateSimpleCaptcha(req.CaptchaToken, req.Captcha) {
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
