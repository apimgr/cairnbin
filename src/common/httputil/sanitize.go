package httputil

import (
	"bytes"
	"encoding/json"
	"regexp"

	"github.com/apimgr/cairnbin/src/mode"
)

// Output Sanitization Pipeline limits (AI.md PART 11, stage 4).
const (
	maxMessageLen = 200
	maxFieldLen   = 256
)

var (
	// sensitiveQueryRE matches known-sensitive query params, case-insensitive (stage 2).
	sensitiveQueryRE = regexp.MustCompile(`(?i)([?&])(token|session|code|key|password|secret|auth|pwd|api_key|apikey|access_token|refresh_token)=[^&\s"']*`)
	// internalIPRE matches RFC 1918, loopback and link-local addresses (stage 3).
	internalIPRE = regexp.MustCompile(`\b(?:10\.\d{1,3}|172\.(?:1[6-9]|2[0-9]|3[01])|192\.168|127\.\d{1,3}|169\.254)\.\d{1,3}\.\d{1,3}\b`)
	// fsPathRE matches absolute filesystem paths under common system roots (stage 3).
	fsPathRE = regexp.MustCompile(`/(?:etc|var|home|root|usr|tmp|opt|app|srv|proc|data|config)/[^\s"']*`)
)

// SanitizeString applies pipeline stages 2-4 to one string: redact sensitive
// query params, strip internal IPs and filesystem paths, then truncate to max
// characters (0 means maxFieldLen).
func SanitizeString(s string, max int) string {
	if max <= 0 {
		max = maxFieldLen
	}
	s = sensitiveQueryRE.ReplaceAllString(s, "${1}${2}=[redacted]")
	s = internalIPRE.ReplaceAllString(s, "[redacted]")
	s = fsPathRE.ReplaceAllString(s, "[redacted]")
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return s
}

// sanitizeValue walks a decoded JSON value, sanitizing every string.
func sanitizeValue(v any) any {
	switch t := v.(type) {
	case string:
		return SanitizeString(t, maxFieldLen)
	case []any:
		for i := range t {
			t[i] = sanitizeValue(t[i])
		}
		return t
	case map[string]any:
		for k, e := range t {
			t[k] = sanitizeValue(e)
		}
		return t
	}
	return v
}

// sanitizeErrorBody runs the Output Sanitization Pipeline over a canonical error
// envelope (`"ok": false`): the message and details are sanitized (stages 2-4)
// and the dev-only `_debug` field is dropped unless debug is on (stage 5).
// Success bodies carry user data (pastes) and are shaped by typed handler
// structs (stage 1), so they are not rewritten here. It returns the input
// unchanged when the body is not an error envelope.
func sanitizeErrorBody(encoded []byte) []byte {
	if !bytes.Contains(encoded, []byte(`"ok": false`)) {
		return encoded
	}
	var m map[string]any
	if err := json.Unmarshal(encoded, &m); err != nil {
		return encoded
	}
	if ok, isBool := m["ok"].(bool); !isBool || ok {
		return encoded
	}
	if !mode.IsDebugEnabled() {
		delete(m, "_debug")
	}
	if msg, isStr := m["message"].(string); isStr {
		m["message"] = SanitizeString(msg, maxMessageLen)
	}
	if d, has := m["details"]; has {
		m["details"] = sanitizeValue(d)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return encoded
	}
	return buf.Bytes()
}
