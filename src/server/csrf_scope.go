package server

import (
	"net/http"
	"strings"
)

// csrfOutOfScope reports backend and compatibility routes that never receive
// CSRF middleware behavior, including token-cookie issuance.
func (s *Server) csrfOutOfScope(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	if path == "/api" || strings.HasPrefix(path, "/api/") || path == "/mcp" {
		return true
	}
	if path == "/documents" || strings.HasPrefix(path, "/documents/") {
		return true
	}
	if path == "/upload" || strings.HasPrefix(path, "/upload/") || strings.HasPrefix(path, "/view/") {
		return true
	}
	for _, rules := range s.compatOwnership() {
		for _, rule := range rules {
			if compatRuleMatches(rule, r.Method, path) {
				return true
			}
		}
	}
	if path == "/" && r.Method == http.MethodPost {
		switch compatModeFromLabel(s.requestCompatHostLabel(r)) {
		case "termbin", "ixio", "sprunge", "zerox0":
			return true
		}
		limit := int64(10 << 20)
		if s.pasteHandler != nil {
			limit = s.pasteHandler.EffectiveReadLimit()
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			if r.ParseMultipartForm(32<<10) != nil || r.MultipartForm == nil {
				return false
			}
			if r.MultipartForm.File != nil && len(r.MultipartForm.File["file"]) > 0 {
				return true
			}
			return r.Form.Has("sprunge") || r.Form.Has("f:1")
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			if r.ParseForm() != nil {
				return false
			}
			return r.Form.Has("sprunge") || r.Form.Has("f:1")
		}
	}
	return false
}

// hasBearerCredential accepts only bearer credentials that browsers do not
// attach automatically; other Authorization schemes do not bypass CSRF.
func hasBearerCredential(r *http.Request) bool {
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	scheme, credential, found := strings.Cut(authorization, " ")
	return found && strings.EqualFold(scheme, "Bearer") && strings.TrimSpace(credential) != ""
}
