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
