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
	"github.com/apimgr/cairnbin/src/common/secretbox"
	"github.com/apimgr/cairnbin/src/database"
	"github.com/go-chi/chi/v5"
)

const ownerCookieName = "cairnbin_owner_token_rU3uW5Ze"
const tokenViewCookieName = "cairnbin_token_view"

func (s *Server) setTokenViewCookie(w http.ResponseWriter, target string) {
	http.SetCookie(w, &http.Cookie{Name: tokenViewCookieName, Value: target, Path: "/server/preferences/tokens", MaxAge: 60, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}

func clearTokenViewCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: tokenViewCookieName, Value: "", Path: "/server/preferences/tokens", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}

func tokenViewRedirect(w http.ResponseWriter, r *http.Request, target string) {
	anchor := "#primary"
	if target != "primary" {
		anchor = "#token-" + target
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/server/preferences/tokens"+anchor, http.StatusSeeOther)
}

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
			writeAPIError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "errors.unauthorized")
		} else {
			http.Redirect(w, r, "/server/preferences/tokens", http.StatusSeeOther)
		}
		return nil, db, false
	}
	if rec.Kind != "primary" {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeAPIError(w, r, http.StatusForbidden, "FORBIDDEN", "errors.forbidden")
		} else {
			s.renderErrorPage(w, r, http.StatusForbidden, "Primary token required")
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
		writeAPIError(w, r, http.StatusInternalServerError, "SERVER_ERROR", "errors.server_error")
		return
	}
	primaryToken := map[string]any{}
	linked := make([]map[string]any, 0, len(tokens))
	for _, t := range tokens {
		if t.Kind == "primary" {
			primaryToken = tokenSummary(t)
		} else {
			linked = append(linked, tokenSummary(t))
		}
	}
	max := s.liveCfg().Server.Tokens.MaxLinked
	if max <= 0 {
		max = 25
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"primary": primaryToken, "linked": linked, "max_linked": max}})
}

func tokenSummary(t *database.APITokenRecord) map[string]any {
	status := "active"
	if t.RevokedAt != nil {
		status = "revoked"
	} else if t.ExpiresAt != nil && !t.ExpiresAt.After(time.Now()) {
		status = "expired"
	}
	masked := "own_...????"
	if len(t.TokenSuffix) == 4 {
		masked = "own_..." + t.TokenSuffix
	}
	return map[string]any{"id": t.PublicID, "kind": t.Kind, "name": t.Name, "token_masked": masked, "created_at": t.CreatedAt.UTC().Format(time.RFC3339), "last_used_at": utcTimeValue(t.LastUsedAt), "expires_at": utcTimeValue(t.ExpiresAt), "status": status}
}

func utcTimeValue(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339)
}

func (s *Server) revealOwnerPrimary(w http.ResponseWriter, r *http.Request) (string, bool) {
	primary, _, ok := s.requirePrimaryOwner(w, r)
	if !ok {
		return "", false
	}
	key, err := s.liveCfg().EncryptionKey()
	if err != nil {
		s.renderErrorPage(w, r, http.StatusInternalServerError, "Token unavailable")
		return "", false
	}
	sealed, err := base64.StdEncoding.DecodeString(primary.TokenEnc)
	if err != nil {
		s.renderErrorPage(w, r, http.StatusInternalServerError, "Token unavailable")
		return "", false
	}
	plain, err := s.openAtRest(key, sealed)
	if err != nil {
		s.renderErrorPage(w, r, http.StatusInternalServerError, "Token unavailable")
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
	_, _, ok := s.requirePrimaryOwner(w, r)
	if !ok {
		return
	}
	s.setTokenViewCookie(w, "primary")
	tokenViewRedirect(w, r, "primary")
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
	token := "own_" + string(raw)
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
		s.renderErrorPage(w, r, http.StatusInternalServerError, "Token rotation failed")
		return "", nil, false
	}
	key, err := s.liveCfg().EncryptionKey()
	if err != nil {
		s.renderErrorPage(w, r, http.StatusInternalServerError, "Token rotation failed")
		return "", nil, false
	}
	sealed, err := secretbox.Seal(key, []byte(token))
	if err != nil {
		s.renderErrorPage(w, r, http.StatusInternalServerError, "Token rotation failed")
		return "", nil, false
	}
	err = db.RotateOwnerToken(primary.OwnerID, primary.ID, hex.EncodeToString(sum[:]), prefix, token[len(token)-4:], base64.StdEncoding.EncodeToString(sealed))
	if err != nil {
		s.renderErrorPage(w, r, http.StatusInternalServerError, "Token rotation failed")
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
	token, _, ok := s.rotateOwnerPrimary(w, r)
	if !ok {
		return
	}
	s.setOwnerCookie(w, r, token)
	s.setTokenViewCookie(w, "primary")
	tokenViewRedirect(w, r, "primary")
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
		s.renderErrorPage(w, r, http.StatusServiceUnavailable, "Token restore unavailable")
		return
	}
	value := strings.TrimSpace(r.FormValue("owner_token"))
	if value == "" {
		if s.ownerRestoreFailureLimited(w, r) {
			return
		}
		s.renderErrorPage(w, r, http.StatusBadRequest, "Invalid primary token")
		return
	}
	rec, err := db.GetOwnerTokenByHash(sha256.Sum256([]byte(value)))
	if err != nil || rec.Kind != "primary" {
		if s.ownerRestoreFailureLimited(w, r) {
			return
		}
		s.renderErrorPage(w, r, http.StatusUnauthorized, "Invalid primary token")
		return
	}
	s.setOwnerCookie(w, r, value)
	w.Header().Set("Cache-Control", "no-store")
	s.tokenAuditOwner(r, rec.OwnerID, "token.restored", "primary", rec.TokenPrefix)
	http.Redirect(w, r, "/server/preferences/tokens", http.StatusSeeOther)
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
	allowed, retry := s.restoreFailLimiter.allowWithRetry("ip:" + ip + ":auth_failures")
	if allowed {
		return false
	}
	seconds := int(retry.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	s.renderErrorPage(w, r, http.StatusTooManyRequests, "Too many invalid restore attempts")
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

func (s *Server) ownerTokenByID(db database.OwnerDB, ownerID, publicID string) (*database.APITokenRecord, error) {
	return db.GetOwnerTokenByPublicID(ownerID, publicID)
}

func (s *Server) linkedTokenCreateFields(r *http.Request) (string, string, error) {
	name, expiry := "", ""
	if strings.HasPrefix(r.URL.Path, "/api/") {
		var body struct {
			Name      string `json:"name"`
			Expires   string `json:"expires"`
			ExpiresOn string `json:"expires_on"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&body); err != nil {
			return "", "", err
		}
		name, expiry = body.Name, body.Expires
		if body.Expires != "" && body.ExpiresOn != "" {
			return "", "", fmt.Errorf("expires and expires_on cannot both be set")
		}
		if body.Expires != "" && body.Expires != "never" && body.Expires != "7d" && body.Expires != "30d" && body.Expires != "90d" && body.Expires != "1y" {
			return "", "", fmt.Errorf("expires must be never, 7d, 30d, 90d, or 1y")
		}
		if body.ExpiresOn != "" {
			expiry = body.ExpiresOn
		}
	} else {
		name, expiry = r.FormValue("name"), r.FormValue("expires")
		if date := r.FormValue("expires_on"); date != "" {
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
		if day.Before(now.Truncate(24 * time.Hour)) {
			return nil, fmt.Errorf("expiry date must be today or later in UTC")
		}
		t = day.AddDate(0, 0, 1)
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
	record, err := db.GetOwnerTokenByID(primary.OwnerID, id)
	if err != nil {
		s.tokenActionError(w, r, http.StatusInternalServerError, "token storage error")
		return
	}
	s.tokenAudit(r, "token.created", "linked", prefix)
	result := tokenSummary(record)
	result["linked_token"] = token
	s.tokenActionSuccess(w, r, result)
}

func (s *Server) handleLinkedTokenReveal(w http.ResponseWriter, r *http.Request) {
	primary, db, ok := s.requirePrimaryOwner(w, r)
	if !ok {
		return
	}
	token, err := s.ownerTokenByID(db, primary.OwnerID, chi.URLParam(r, "id"))
	if err != nil || token.Kind != "linked" || token.RevokedAt != nil {
		s.tokenActionError(w, r, 404, "token not found")
		return
	}
	s.tokenAudit(r, "token.revealed", "linked", token.TokenPrefix)
	result := tokenSummary(token)
	if strings.HasPrefix(r.URL.Path, "/api/") {
		plain, err := s.decryptOwnerToken(token.TokenEnc)
		if err != nil {
			s.tokenActionError(w, r, 500, "token unavailable")
			return
		}
		result["linked_token"] = plain
	} else {
		s.setTokenViewCookie(w, token.PublicID)
		tokenViewRedirect(w, r, token.PublicID)
		return
	}
	s.tokenActionSuccess(w, r, result)
}

func (s *Server) handleLinkedTokenRotate(w http.ResponseWriter, r *http.Request) {
	primary, db, ok := s.requirePrimaryOwner(w, r)
	if !ok {
		return
	}
	old, err := s.ownerTokenByID(db, primary.OwnerID, chi.URLParam(r, "id"))
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
	if err := db.RotateOwnerToken(primary.OwnerID, old.ID, hex.EncodeToString(sum[:]), prefix, token[len(token)-4:], base64.StdEncoding.EncodeToString(sealed)); err != nil {
		s.tokenActionError(w, r, 500, "token rotation failed")
		return
	}
	s.tokenAuditOwner(r, primary.OwnerID, "token.rotated", "linked", prefix)
	w.Header().Set("Cache-Control", "no-store")
	old.TokenPrefix, old.TokenSuffix = prefix, token[len(token)-4:]
	result := tokenSummary(old)
	result["linked_token"] = token
	s.tokenActionSuccess(w, r, result)
}

func (s *Server) handleLinkedTokenUpdate(w http.ResponseWriter, r *http.Request) {
	primary, db, ok := s.requirePrimaryOwner(w, r)
	if !ok {
		return
	}
	name, choice, err := s.linkedTokenCreateFields(r)
	if err != nil {
		s.tokenActionError(w, r, 400, err.Error())
		return
	}
	old, err := s.ownerTokenByID(db, primary.OwnerID, chi.URLParam(r, "id"))
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
	tokens, err := db.ListOwnerTokens(primary.OwnerID)
	if err != nil {
		s.tokenActionError(w, r, 500, "token storage error")
		return
	}
	for _, token := range tokens {
		if token.ID != old.ID && token.Kind == "linked" && token.Name == name {
			s.tokenActionError(w, r, http.StatusConflict, "token name already exists")
			return
		}
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
	if err := db.UpdateOwnerToken(primary.OwnerID, old.ID, name, expiry, max); err != nil {
		s.tokenActionError(w, r, 404, "token not found")
		return
	}
	old.Name, old.ExpiresAt = name, expiry
	s.tokenActionSuccess(w, r, tokenSummary(old))
}

func (s *Server) handleLinkedTokenRevoke(w http.ResponseWriter, r *http.Request) {
	primary, db, ok := s.requirePrimaryOwner(w, r)
	if !ok {
		return
	}
	token, err := s.ownerTokenByID(db, primary.OwnerID, chi.URLParam(r, "id"))
	if err != nil || token.Kind != "linked" {
		s.tokenActionError(w, r, 404, "token not found")
		return
	}
	if err := db.RevokeOwnerToken(primary.OwnerID, token.ID); err != nil {
		s.tokenActionError(w, r, 404, "token not found")
		return
	}
	s.tokenAudit(r, "token.revoked", "linked", token.TokenPrefix)
	// Token revocation purges the browser's cache/cookies/storage (PART 11).
	if csd := s.liveCfg().Web.Headers.ClearSiteData; csd.OnTokenRevocation {
		w.Header().Set("Clear-Site-Data", clearSiteDataValue(csd))
	}
	s.tokenActionSuccess(w, r, map[string]any{"id": token.PublicID, "status": "revoked"})
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
		code := errorCodeForStatus(status)
		writeAPIError(w, r, status, code, apiErrorTranslationKey(code, message))
		return
	}
	s.renderErrorPage(w, r, status, message)
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
		if _, yes := values["linked_token"].(string); yes {
			id, _ := values["id"].(string)
			s.setTokenViewCookie(w, id)
			tokenViewRedirect(w, r, id)
			return
		}
	}
	http.Redirect(w, r, "/server/preferences/tokens", http.StatusSeeOther)
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
