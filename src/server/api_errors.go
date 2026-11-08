package server

import (
	"net/http"

	"github.com/apimgr/cairnbin/src/common/i18n"
)

// writeAPIError writes the stable API error envelope with a request-localized message.
func writeAPIError(w http.ResponseWriter, r *http.Request, status int, code, messageKey string) {
	writeJSON(w, status, map[string]any{
		"ok": false, "error": code,
		"message": i18n.Translate(i18n.LangFromRequest(r), messageKey),
	})
}

func apiErrorTranslationKey(code, message string) string {
	if key := map[string]string{
		"name must be 1 to 64 characters": "errors.invalid_format",
		"token name already exists":      "errors.conflict",
		"linked token limit reached":      "errors.rate_limited",
		"token not found":                 "errors.not_found",
	}[message]; key != "" {
		return key
	}
	if key := map[string]string{
		"BAD_REQUEST":       "errors.bad_request",
		"VALIDATION_FAILED": "errors.invalid_format",
		"UNAUTHORIZED":      "errors.unauthorized",
		"FORBIDDEN":         "errors.forbidden",
		"NOT_FOUND":         "errors.not_found",
		"CONFLICT":          "errors.conflict",
		"RATE_LIMITED":      "errors.rate_limited",
		"MAINTENANCE":       "errors.maintenance_mode",
		"SERVER_ERROR":      "errors.server_error",
	}[code]; key != "" {
		return key
	}
	return "errors.server_error"
}
