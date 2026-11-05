package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/apimgr/cairnbin/src/audit"
	"github.com/apimgr/cairnbin/src/common/i18n"
	"github.com/apimgr/cairnbin/src/common/secretbox"
	"github.com/apimgr/cairnbin/src/database"
	"github.com/go-chi/chi/v5"
)

const ownerCookieName = "cairnbin_owner_token_rU3uW5Ze"

func (s *Server) ownerDB() (database.OwnerDB, bool) {
	db, ok := s.db.(database.OwnerDB)
	return db, ok
}

// ownerCredential resolves a primary or linked token. API handlers accept a
// bearer token only; browser management actions use the HttpOnly owner cookie.
func (s *Server) ownerCredential(r *http.Request) (*database.APITokenRecord, error) {
	db, ok := s.ownerDB()
	if !ok {
		return nil, fmt.Errorf("owner token storage unavailable")
	}
	var raw string
	if strings.HasPrefix(r.URL.Path, "/api/") {
		auth := r.Header.Get("Authorization")
		if strings.HasPrefix(auth, "Bearer ") {
			raw = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		}
	} else if c, err := r.Cookie(ownerCookieName); err == nil {
		raw = c.Value
	}
	if raw == "" {
		return nil, fmt.Errorf("unauthorized")
	}
	rec, err := db.GetOwnerTokenByHash(sha256.Sum256([]byte(raw)))
	if err != nil {
		return nil, fmt.Errorf("unauthorized")
	}
	return rec, nil
}

func (s *Server) requireOwnerForPage(r *http.Request) (*database.APITokenRecord, database.OwnerDB, bool) {
	db, ok := s.ownerDB()
	if !ok {
		return nil, nil, false
	}
	c, err := r.Cookie(ownerCookieName)
	if err != nil || c.Value == "" {
		return nil, db, false
	}
	rec, err := db.GetOwnerTokenByHash(sha256.Sum256([]byte(c.Value)))
	if err != nil || rec.Kind != "primary" {
		return nil, db, false
	}
	return rec, db, true
}

func (s *Server) requirePrimaryOwner(w http.ResponseWriter, r *http.Request) (*database.APITokenRecord, database.OwnerDB, bool) {
	rec, err := s.ownerCredential(r)
	db, ok := s.ownerDB()
	if err != nil || !ok {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "UNAUTHORIZED"})
		} else {
			http.Redirect(w, r, "/server/preferences", http.StatusSeeOther)
		}
		return nil, db, false
	}
	if rec.Kind != "primary" {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "FORBIDDEN", "message": "primary token required"})
		} else {
			http.Error(w, "Primary token required", http.StatusForbidden)
		}
		return nil, db, false
	}
	return rec, db, true
}

func (s *Server) handleOwnerTokensAPI(w http.ResponseWriter, r *http.Request) {
	primary, db, ok := s.requirePrimaryOwner(w, r)
	if !ok {
		return
	}
	tokens, err := db.ListOwnerTokens(primary.OwnerID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "SERVER_ERROR"})
		return
	}
	items := make([]map[string]any, 0, len(tokens))
	for _, t := range tokens {
		items = append(items, tokenSummary(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"tokens": items}})
}

func tokenSummary(t *database.APITokenRecord) map[string]any {
	status := "active"
	if t.RevokedAt != nil {
		status = "revoked"
	} else if t.ExpiresAt != nil && !t.ExpiresAt.After(time.Now()) {
		status = "expired"
	}
	masked := "tok_••••••••••••••••••••••••????"
	if len(t.TokenSuffix) == 4 {
		masked = "tok_••••••••••••••••••••••••" + t.TokenSuffix
	}
	return map[string]any{"id": t.ID, "kind": t.Kind, "name": t.Name, "token": masked, "token_prefix": t.TokenPrefix, "created_at": t.CreatedAt, "last_used_at": t.LastUsedAt, "expires_at": t.ExpiresAt, "status": status}
}

func (s *Server) revealOwnerPrimary(w http.ResponseWriter, r *http.Request) (string, bool) {
	primary, _, ok := s.requirePrimaryOwner(w, r)
	if !ok {
		return "", false
	}
	key, err := s.liveCfg().EncryptionKey()
	if err != nil {
		http.Error(w, "Token unavailable", http.StatusInternalServerError)
		return "", false
	}
	sealed, err := base64.StdEncoding.DecodeString(primary.TokenEnc)
	if err != nil {
		http.Error(w, "Token unavailable", http.StatusInternalServerError)
		return "", false
	}
	plain, err := s.openAtRest(key, sealed)
	if err != nil {
		http.Error(w, "Token unavailable", http.StatusInternalServerError)
		return "", false
	}
	w.Header().Set("Cache-Control", "no-store")
	return string(plain), true
}

func (s *Server) handleOwnerTokenRevealAPI(w http.ResponseWriter, r *http.Request) {
	token, ok := s.revealOwnerPrimary(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]string{"owner_token": token}})
	s.tokenAudit(r, "token.revealed", "primary", "")
}

func (s *Server) handleOwnerTokenRevealWeb(w http.ResponseWriter, r *http.Request) {
	token, ok := s.revealOwnerPrimary(w, r)
	if !ok {
		return
	}
	data := s.preferencesPageData(r, s.themeFromRequest(r), i18n.LangFromRequest(r))
	data["RevealedOwnerToken"] = token
	s.renderTemplate(w, r, "preferences.html", data)
	s.tokenAudit(r, "token.revealed", "primary", "")
}

func createOwnerCredential() (string, [32]byte, string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", [32]byte{}, "", err
	}
	for i, b := range raw {
		raw[i] = alphabet[int(b)%len(alphabet)]
	}
	token := "tok_" + string(raw)
	sum := sha256.Sum256([]byte(token))
	prefix := token[:12]
	return token, sum, prefix, nil
}

func (s *Server) rotateOwnerPrimary(w http.ResponseWriter, r *http.Request) (string, *database.APITokenRecord, bool) {
	primary, db, ok := s.requirePrimaryOwner(w, r)
	if !ok {
		return "", nil, false
	}
	token, sum, prefix, err := createOwnerCredential()
	if err != nil {
		http.Error(w, "Token rotation failed", http.StatusInternalServerError)
		return "", nil, false
	}
	key, err := s.liveCfg().EncryptionKey()
	if err != nil {
		http.Error(w, "Token rotation failed", http.StatusInternalServerError)
		return "", nil, false
	}
	sealed, err := secretbox.Seal(key, []byte(token))
	if err != nil {
		http.Error(w, "Token rotation failed", http.StatusInternalServerError)
		return "", nil, false
	}
	err = db.RotateOwnerToken(primary.OwnerID, primary.ID, hex.EncodeToString(sum[:]), prefix, token[len(token)-4:], base64.StdEncoding.EncodeToString(sealed))
	if err != nil {
		http.Error(w, "Token rotation failed", http.StatusInternalServerError)
		return "", nil, false
	}
	s.tokenAuditOwner(r, primary.OwnerID, "token.rotated", "primary", prefix)
	w.Header().Set("Cache-Control", "no-store")
	return token, primary, true
}

func (s *Server) handleOwnerTokenRotateAPI(w http.ResponseWriter, r *http.Request) {
	token, _, ok := s.rotateOwnerPrimary(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]string{"owner_token": token}})
}

func (s *Server) handleOwnerTokenRotateWeb(w http.ResponseWriter, r *http.Request) {
	token, primary, ok := s.rotateOwnerPrimary(w, r)
	if !ok {
		return
	}
	s.setOwnerCookie(w, r, token)
	data := s.preferencesPageData(r, s.themeFromRequest(r), i18n.LangFromRequest(r))
	data["RevealedOwnerToken"] = token
	data["HasOwnerPrimary"] = true
	if db, ok := s.ownerDB(); ok {
		if tokens, err := db.ListOwnerTokens(primary.OwnerID); err == nil {
			items := make([]map[string]any, 0, len(tokens))
			for _, item := range tokens {
				items = append(items, tokenSummary(item))
			}
			data["OwnerTokens"] = items
		}
	}
	s.renderTemplate(w, r, "preferences.html", data)
}

func (s *Server) setOwnerCookie(w http.ResponseWriter, r *http.Request, token string) {
	maxAge := 365 * 24 * 60 * 60
	if age := s.liveCfg().Server.Tokens.CookieMaxAge; age != "" {
		if d, err := parseOwnerCookieAge(age); err == nil {
			maxAge = int(d.Seconds())
		}
	}
	http.SetCookie(w, &http.Cookie{Name: ownerCookieName, Value: token, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}

func parseOwnerCookieAge(value string) (time.Duration, error) {
	if strings.HasSuffix(value, "y") {
		n, err := time.ParseDuration(strings.TrimSuffix(value, "y") + "h")
		if err != nil {
			return 0, err
		}
		return n * 24 * 365, nil
	}
	if strings.HasSuffix(value, "d") {
		n, err := time.ParseDuration(strings.TrimSuffix(value, "d") + "h")
		if err != nil {
			return 0, err
		}
		return n * 24, nil
	}
	return time.ParseDuration(value)
}

func (s *Server) handleOwnerTokenRestore(w http.ResponseWriter, r *http.Request) {
	db, ok := s.ownerDB()
	if !ok {
		http.Error(w, "Token restore unavailable", http.StatusServiceUnavailable)
		return
	}
	value := strings.TrimSpace(r.FormValue("owner_token"))
	if value == "" {
		if s.ownerRestoreFailureLimited(w, r) {
			return
		}
		http.Error(w, "Invalid primary token", http.StatusBadRequest)
		return
	}
	rec, err := db.GetOwnerTokenByHash(sha256.Sum256([]byte(value)))
	if err != nil || rec.Kind != "primary" {
		if s.ownerRestoreFailureLimited(w, r) {
			return
		}
		http.Error(w, "Invalid primary token", http.StatusUnauthorized)
		return
	}
	s.setOwnerCookie(w, r, value)
	w.Header().Set("Cache-Control", "no-store")
	s.tokenAuditOwner(r, rec.OwnerID, "token.restored", "primary", rec.TokenPrefix)
	http.Redirect(w, r, "/server/preferences", http.StatusSeeOther)
}

// ownerRestoreFailureLimited counts only failed restore attempts, separately
// from the strict limiter that also bounds successful credential operations.
func (s *Server) ownerRestoreFailureLimited(w http.ResponseWriter, r *http.Request) bool {
	if s.restoreFailLimiter == nil {
		return false
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	if s.restoreFailLimiter.allow(ip) {
		return false
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(s.restoreFailLimiter.window.Seconds())))
	http.Error(w, "Too many invalid restore attempts", http.StatusTooManyRequests)
	return true
}

func (s *Server) tokenAudit(r *http.Request, event, kind, prefix string) {
	rec, err := s.ownerCredential(r)
	ownerID := ""
	if err == nil {
		ownerID = rec.OwnerID
		if prefix == "" {
			prefix = rec.TokenPrefix
		}
	}
	s.tokenAuditOwner(r, ownerID, event, kind, prefix)
}

func (s *Server) tokenAuditOwner(r *http.Request, ownerID, event, kind, prefix string) {
	details := map[string]any{"kind": kind}
	if prefix != "" {
		details["token_prefix"] = prefix
	}
	s.auditLog(r, audit.Entry{Event: event, Category: "security", Severity: audit.SeverityInfo, Result: audit.ResultSuccess, Target: &audit.Target{Type: "owner", ID: ownerID}, Details: details})
}

func (s *Server) ownerTokenByID(db database.OwnerDB, ownerID string, id int64) (*database.APITokenRecord, error) {
	return db.GetOwnerTokenByID(ownerID, id)
}

func (s *Server) linkedTokenCreateFields(r *http.Request) (string, string, error) {
	name, expiry := "", ""
	if strings.HasPrefix(r.URL.Path, "/api/") {
		var body struct {
			Name        string `json:"name"`
			Expires     string `json:"expires"`
			ExpiresDate string `json:"expires_date"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&body); err != nil {
			return "", "", err
		}
		name, expiry = body.Name, body.Expires
		if body.ExpiresDate != "" {
			expiry = body.ExpiresDate
		}
	} else {
		name, expiry = r.FormValue("name"), r.FormValue("expires")
		if date := r.FormValue("expires_date"); date != "" {
			expiry = date
		}
	}
	name = strings.TrimSpace(name)
	return name, expiry, nil
}

func expiryFromChoice(choice string) (*time.Time, error) {
	now := time.Now().UTC()
	var t time.Time
	switch choice {
	case "", "never":
		return nil, nil
	case "7d":
		t = now.Add(7 * 24 * time.Hour)
	case "30d":
		t = now.Add(30 * 24 * time.Hour)
	case "90d":
		t = now.Add(90 * 24 * time.Hour)
	case "1y":
		t = now.AddDate(1, 0, 0)
	default:
		day, err := time.Parse("2006-01-02", choice)
		if err != nil {
			return nil, fmt.Errorf("invalid expiry")
		}
		t = day.Add(24*time.Hour - time.Second)
	}
	if !t.After(now) {
		return nil, fmt.Errorf("expiry must be in the future")
	}
	return &t, nil
}

func (s *Server) handleLinkedTokenCreate(w http.ResponseWriter, r *http.Request) {
	primary, db, ok := s.requirePrimaryOwner(w, r)
	if !ok {
		return
	}
	name, choice, err := s.linkedTokenCreateFields(r)
	if err != nil {
		s.tokenActionError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if n := utf8.RuneCountInString(name); n < 1 || n > 64 {
		s.tokenActionError(w, r, http.StatusBadRequest, "name must be 1 to 64 characters")
		return
	}
	if choice == "" {
		choice = "never"
	}
	expiry, err := expiryFromChoice(choice)
	if err != nil {
		s.tokenActionError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	tokens, err := db.ListOwnerTokens(primary.OwnerID)
	if err != nil {
		s.tokenActionError(w, r, 500, "token storage error")
		return
	}
	active := 0
	for _, token := range tokens {
		if token.Kind != "linked" {
			continue
		}
		if token.Name == name {
			s.tokenActionError(w, r, http.StatusConflict, "token name already exists")
			return
		}
		if token.RevokedAt == nil && (token.ExpiresAt == nil || token.ExpiresAt.After(time.Now())) {
			active++
		}
	}
	max := s.liveCfg().Server.Tokens.MaxLinked
	if max <= 0 {
		max = 25
	}
	if active >= max {
		s.tokenActionError(w, r, http.StatusConflict, "linked token limit reached")
		return
	}
	token, sum, prefix, err := createOwnerCredential()
	if err != nil {
		s.tokenActionError(w, r, 500, "token creation failed")
		return
	}
	key, err := s.liveCfg().EncryptionKey()
	if err != nil {
		s.tokenActionError(w, r, 500, "token creation failed")
		return
	}
	sealed, err := secretbox.Seal(key, []byte(token))
	if err != nil {
		s.tokenActionError(w, r, 500, "token creation failed")
		return
	}
	id, err := db.CreateLinkedToken(primary.OwnerID, hex.EncodeToString(sum[:]), prefix, token[len(token)-4:], name, base64.StdEncoding.EncodeToString(sealed), expiry, max)
	if err != nil {
		s.tokenActionError(w, r, http.StatusConflict, "token name already exists")
		return
	}
	s.tokenAudit(r, "token.created", "linked", prefix)
	s.tokenActionSuccess(w, r, map[string]any{"id": id, "name": name, "token": token})
}

func (s *Server) handleLinkedTokenReveal(w http.ResponseWriter, r *http.Request) {
	primary, db, ok := s.requirePrimaryOwner(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		s.tokenActionError(w, r, 404, "token not found")
		return
	}
	token, err := s.ownerTokenByID(db, primary.OwnerID, id)
	if err != nil || token.Kind != "linked" || token.RevokedAt != nil {
		s.tokenActionError(w, r, 404, "token not found")
		return
	}
	plain, err := s.decryptOwnerToken(token.TokenEnc)
	if err != nil {
		s.tokenActionError(w, r, 500, "token unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.tokenAudit(r, "token.revealed", "linked", token.TokenPrefix)
	s.tokenActionSuccess(w, r, map[string]any{"id": id, "token": plain})
}

func (s *Server) handleLinkedTokenRotate(w http.ResponseWriter, r *http.Request) {
	primary, db, ok := s.requirePrimaryOwner(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		s.tokenActionError(w, r, 404, "token not found")
		return
	}
	old, err := s.ownerTokenByID(db, primary.OwnerID, id)
	if err != nil || old.Kind != "linked" || old.RevokedAt != nil {
		s.tokenActionError(w, r, 404, "token not found")
		return
	}
	token, sum, prefix, err := createOwnerCredential()
	if err != nil {
		s.tokenActionError(w, r, 500, "token rotation failed")
		return
	}
	key, err := s.liveCfg().EncryptionKey()
	if err != nil {
		s.tokenActionError(w, r, 500, "token rotation failed")
		return
	}
	sealed, err := secretbox.Seal(key, []byte(token))
	if err != nil {
		s.tokenActionError(w, r, 500, "token rotation failed")
		return
	}
	if err := db.RotateOwnerToken(primary.OwnerID, id, hex.EncodeToString(sum[:]), prefix, token[len(token)-4:], base64.StdEncoding.EncodeToString(sealed)); err != nil {
		s.tokenActionError(w, r, 500, "token rotation failed")
		return
	}
	s.tokenAuditOwner(r, primary.OwnerID, "token.rotated", "linked", prefix)
	w.Header().Set("Cache-Control", "no-store")
	s.tokenActionSuccess(w, r, map[string]any{"id": id, "token": token})
}

func (s *Server) handleLinkedTokenUpdate(w http.ResponseWriter, r *http.Request) {
	primary, db, ok := s.requirePrimaryOwner(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		s.tokenActionError(w, r, 404, "token not found")
		return
	}
	name, choice, err := s.linkedTokenCreateFields(r)
	if err != nil {
		s.tokenActionError(w, r, 400, err.Error())
		return
	}
	old, err := s.ownerTokenByID(db, primary.OwnerID, id)
	if err != nil || old.Kind != "linked" {
		s.tokenActionError(w, r, 404, "token not found")
		return
	}
	if name == "" {
		name = old.Name
	}
	if n := utf8.RuneCountInString(name); n < 1 || n > 64 {
		s.tokenActionError(w, r, 400, "name must be 1 to 64 characters")
		return
	}
	expiry := old.ExpiresAt
	if choice != "" {
		expiry, err = expiryFromChoice(choice)
		if err != nil {
			s.tokenActionError(w, r, 400, err.Error())
			return
		}
	}
	max := s.liveCfg().Server.Tokens.MaxLinked
	if max <= 0 {
		max = 25
	}
	if err := db.UpdateOwnerToken(primary.OwnerID, id, name, expiry, max); err != nil {
		s.tokenActionError(w, r, 404, "token not found")
		return
	}
	s.tokenActionSuccess(w, r, map[string]any{"id": id, "name": name, "expires_at": expiry})
}

func (s *Server) handleLinkedTokenRevoke(w http.ResponseWriter, r *http.Request) {
	primary, db, ok := s.requirePrimaryOwner(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		s.tokenActionError(w, r, 404, "token not found")
		return
	}
	token, err := s.ownerTokenByID(db, primary.OwnerID, id)
	if err != nil || token.Kind != "linked" {
		s.tokenActionError(w, r, 404, "token not found")
		return
	}
	if err := db.RevokeOwnerToken(primary.OwnerID, id); err != nil {
		s.tokenActionError(w, r, 404, "token not found")
		return
	}
	s.tokenAudit(r, "token.revoked", "linked", token.TokenPrefix)
	s.tokenActionSuccess(w, r, map[string]any{"id": id, "revoked": true})
}

func (s *Server) decryptOwnerToken(encoded string) (string, error) {
	key, err := s.liveCfg().EncryptionKey()
	if err != nil {
		return "", err
	}
	sealed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	plain, err := s.openAtRest(key, sealed)
	return string(plain), err
}

func (s *Server) tokenActionError(w http.ResponseWriter, r *http.Request, status int, message string) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeJSON(w, status, map[string]any{"ok": false, "error": http.StatusText(status), "message": message})
		return
	}
	http.Error(w, message, status)
}

func (s *Server) tokenActionSuccess(w http.ResponseWriter, r *http.Request, data any) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		status := http.StatusOK
		if strings.HasSuffix(r.URL.Path, "/server/preferences/tokens") {
			status = http.StatusCreated
		}
		writeJSON(w, status, map[string]any{"ok": true, "data": data})
		return
	}
	if values, ok := data.(map[string]any); ok {
		if token, yes := values["token"].(string); yes {
			w.Header().Set("Cache-Control", "no-store")
			pageData := s.preferencesPageData(r, s.themeFromRequest(r), i18n.LangFromRequest(r))
			pageData["RevealedLinkedToken"] = token
			s.renderTemplate(w, r, "preferences.html", pageData)
			return
		}
	}
	http.Redirect(w, r, "/server/preferences", http.StatusSeeOther)
}

// ownerCookieMiddleware slides valid primary cookies and clears invalid or
// revoked values. API routes deliberately ignore cookie credentials.
func (s *Server) ownerCookieMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			if c, err := r.Cookie(ownerCookieName); err == nil && c.Value != "" {
				if db, ok := s.ownerDB(); ok {
					rec, lookupErr := db.GetOwnerTokenByHash(sha256.Sum256([]byte(c.Value)))
					if lookupErr == nil && rec.Kind == "primary" {
						s.setOwnerCookie(w, r, c.Value)
					} else {
						http.SetCookie(w, &http.Cookie{Name: ownerCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
					}
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
