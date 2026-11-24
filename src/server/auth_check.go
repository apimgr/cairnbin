package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// handleAuthCheck reports the kind of a valid Bearer credential without
// updating token activity or revealing whether any particular token exists.
func (s *Server) handleAuthCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeAPIError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "errors.method_not_allowed")
		return
	}
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, prefix) || strings.TrimSpace(strings.TrimPrefix(auth, prefix)) == "" {
		writeAPIError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "errors.unauthorized")
		return
	}
	raw := strings.TrimSpace(strings.TrimPrefix(auth, prefix))
	hash := sha256.Sum256([]byte(raw))
	var emptyHash [32]byte
	if subtle.ConstantTimeCompare(hash[:], s.operatorTokenHash[:]) == 1 && s.operatorTokenHash != emptyHash {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]string{"kind": "operator"}})
		return
	}
	if db, ok := s.ownerDB(); ok {
		if _, err := db.GetOwnerTokenByHashReadOnly(hash); err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]string{"kind": "owner"}})
			return
		}
	}
	writeAPIError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "errors.unauthorized")
}

// validBearer reports whether a request carries the operator token or a live
// owner token. It is shared by MCP's protocol handshake and the auth-check API.
func (s *Server) validBearer(r *http.Request) bool {
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, prefix) || strings.TrimSpace(strings.TrimPrefix(auth, prefix)) == "" {
		return false
	}
	hash := sha256.Sum256([]byte(strings.TrimSpace(strings.TrimPrefix(auth, prefix))))
	var emptyHash [32]byte
	if s.operatorTokenHash != emptyHash && subtle.ConstantTimeCompare(hash[:], s.operatorTokenHash[:]) == 1 {
		return true
	}
	db, ok := s.ownerDB()
	if !ok {
		return false
	}
	_, err := db.GetOwnerTokenByHashReadOnly(hash)
	return err == nil
}

func (s *Server) rateLimitKey(r *http.Request, ip, class string) string {
	if class == "strict" && !strings.HasPrefix(r.URL.Path, "/api/") {
		if owner, _, ok := s.requireOwnerForPage(r); ok {
			return "owner:" + owner.OwnerID + ":" + class
		}
	}
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, prefix) {
		raw := strings.TrimSpace(strings.TrimPrefix(auth, prefix))
		if raw != "" && s.validBearer(r) {
			hash := sha256.Sum256([]byte(raw))
			var zero [32]byte
			if subtle.ConstantTimeCompare(hash[:], s.operatorTokenHash[:]) == 1 && s.operatorTokenHash != zero {
				return "key:" + raw[:min(len(raw), 12)] + ":" + class
			}
			if db, ok := s.ownerDB(); ok {
				if token, err := db.GetOwnerTokenByHashReadOnly(hash); err == nil {
					if class == "strict" {
						return "owner:" + token.OwnerID + ":" + class
					}
					prefix := token.TokenPrefix
					if prefix == "" {
						prefix = raw[:min(len(raw), 12)]
					}
					return "key:" + prefix + ":" + class
				}
			}
		}
	}
	return "ip:" + ip + ":" + class
}
