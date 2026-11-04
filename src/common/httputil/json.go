package httputil

import (
	"bytes"
	"encoding/json"
	"net/http"
)

// WriteJSON encodes v as indented JSON (without HTML-escaping) and writes it
// to w with the given HTTP status code. Shared by the server and handler
// packages so the response-encoding behavior (indentation, escaping, and the
// canonical SERVER_ERROR fallback on encode failure) never drifts between
// them (AI.md PART 14 "Response Formatting"). Error envelopes also pass through
// the Output Sanitization Pipeline (AI.md PART 11).
func WriteJSON(w http.ResponseWriter, status int, v interface{}) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"ok":false,"error":"SERVER_ERROR","message":"Internal server error"}` + "\n"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(sanitizeErrorBody(buf.Bytes()))
}
