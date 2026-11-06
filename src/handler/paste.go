package handler

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"

	"github.com/apimgr/cairnbin/src/cache"
	"github.com/apimgr/cairnbin/src/common/httputil"
	"github.com/apimgr/cairnbin/src/common/secretbox"
	"github.com/apimgr/cairnbin/src/database"
	"github.com/apimgr/cairnbin/src/metric"
	"github.com/apimgr/cairnbin/src/model"
	"github.com/go-chi/chi/v5"
)

// pasteCacheTTL is the read-cache lifetime for individual pastes. AI.md PART 9's
// TTL table has no paste-specific row, so this uses the generic "page cache"
// tier (5 minutes) for dynamic content.
const pasteCacheTTL = 5 * time.Minute

// charset for paste IDs — URL-safe alphanumeric.
const idCharset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// PasteHandler handles all paste HTTP operations.
type PasteHandler struct {
	db database.DB
	// baseURL is an optional override, e.g. "https://paste.example.com".
	baseURL string
	// baseURLFn resolves the canonical per-request base URL (PART 12: full
	// {proto}/{fqdn}/{port}/{baseurl} chain with trusted-proxy gating and the
	// configured-DOMAIN fallback). Injected by the server layer so the resolver
	// is shared instead of duplicated; nil in unit tests, where base() falls
	// back to the static baseURL / request Host.
	baseURLFn func(*http.Request) string
	// operatorTokenHash is SHA-256(server.token), cached at construction time.
	// A constant-time compare against this lets operator tokens bypass the api_tokens
	// lookup and delete any paste unconditionally (PART 11).
	operatorTokenHash [32]byte
	// cache is the optional read-through cache for individual pastes (PART 9).
	// nil in unit tests and until the server layer injects it via SetCache.
	cache cache.Cache
	// maxSizeBytes is the configured maximum paste size (server.yml
	// paste.max_size), enforced in createFromRequest. Only meaningful once
	// maxSizeSet is true; zero means the operator explicitly configured no
	// limit ("0" or a negative paste.max_size). Unit tests that never call
	// SetMaxSize fall back to the 10MiB default via maxSize().
	maxSizeBytes int64
	// maxSizeSet reports whether SetMaxSize has been called, distinguishing
	// "operator configured unlimited (0)" from "never configured".
	maxSizeSet         bool
	tokenEncryptionKey []byte
}

// defaultMaxSizeBytes mirrors config.go's Paste.MaxSizeBytes default so
// createFromRequest has a sane limit even before SetMaxSize is called.
const defaultMaxSizeBytes int64 = 10 << 20

// maxSize returns the configured maximum paste size, falling back to
// defaultMaxSizeBytes when SetMaxSize was never called. A return value of
// 0 means the operator configured an unlimited paste size.
func (h *PasteHandler) maxSize() int64 {
	if h.maxSizeSet {
		return h.maxSizeBytes
	}
	return defaultMaxSizeBytes
}

// EffectiveReadLimit returns maxSize(), substituting a very large (but
// overflow-safe) bound for the "unlimited" (0) case so io.LimitReader /
// http.MaxBytesReader call sites never misread 0 as "allow zero bytes".
func (h *PasteHandler) EffectiveReadLimit() int64 {
	n := h.maxSize()
	if n <= 0 {
		return math.MaxInt64 - 1
	}
	return n
}

// NewPasteHandler constructs a PasteHandler.
// operatorTokenHash must be sha256.Sum256([]byte(cfg.Server.Token)); pass a zero
// array when the server token is not set (all operator paths will return 401).
func NewPasteHandler(db database.DB, baseURL string, operatorTokenHash [32]byte) *PasteHandler {
	h := &PasteHandler{db: db, baseURL: baseURL, operatorTokenHash: operatorTokenHash}
	h.refreshActiveTokenGauge()
	return h
}

// SetBaseURLResolver injects the server's canonical per-request base-URL
// resolver (PART 12) so pasteURL/origin reuse the full reverse-proxy + FQDN
// chain instead of a simplified copy.
func (h *PasteHandler) SetBaseURLResolver(fn func(*http.Request) string) {
	h.baseURLFn = fn
}

// SetTokenEncryptionKey supplies the configured at-rest key for recoverable owner credentials.
func (h *PasteHandler) SetTokenEncryptionKey(key []byte) {
	h.tokenEncryptionKey = append([]byte(nil), key...)
}

// SetCache injects the server's shared cache driver (PART 9/12) so paste reads
// can be served from cache. Passing nil disables read caching; a nil cache
// field is treated as "no cache configured" everywhere it is used.
func (h *PasteHandler) SetCache(c cache.Cache) {
	h.cache = c
}

// SetMaxSize injects the server's configured maximum paste size
// (server.yml paste.max_size) so createFromRequest enforces the
// operator-configured limit instead of a hardcoded default. n <= 0 configures
// an unlimited paste size (maxSize() then returns 0).
func (h *PasteHandler) SetMaxSize(n int64) {
	if n < 0 {
		n = 0
	}
	h.maxSizeBytes = n
	h.maxSizeSet = true
}

// pasteCacheKey returns the cache key for a paste ID, following AI.md PART 9's
// "{type}:{id}" hierarchical key pattern. The "cairnbin:" prefix is applied
// automatically by the cache driver — callers pass un-prefixed keys.
func pasteCacheKey(id string) string {
	return "paste:" + id
}

// getCachedPaste returns a cached paste and true on a cache hit. Burn-limited
// pastes (BurnAfter > 0) are never served from cache: the cached Views count
// can lag the database, and burn-after-read enforcement must stay
// DB-authoritative to avoid over- or under-counting views toward the burn
// threshold. Cache misses/errors are non-fatal — callers fall through to the
// database (backend-rules.md: "Cache miss is non-fatal").
func (h *PasteHandler) getCachedPaste(id string) (*model.Paste, bool) {
	if h.cache == nil {
		return nil, false
	}
	raw, err := h.cache.Get(context.Background(), pasteCacheKey(id))
	if err != nil {
		return nil, false
	}
	var paste model.Paste
	if err := json.Unmarshal([]byte(raw), &paste); err != nil {
		return nil, false
	}
	if paste.BurnAfter > 0 {
		return nil, false
	}
	return &paste, true
}

// cachePaste stores a freshly loaded paste for subsequent reads. Burn-limited
// pastes are never cached (see getCachedPaste for why). Errors are non-fatal.
func (h *PasteHandler) cachePaste(paste *model.Paste) {
	if h.cache == nil || paste == nil || paste.BurnAfter > 0 {
		return
	}
	raw, err := json.Marshal(paste)
	if err != nil {
		return
	}
	_ = h.cache.Set(context.Background(), pasteCacheKey(paste.ID), string(raw), pasteCacheTTL)
}

// InvalidatePasteCache removes a paste from the cache after delete or expiry
// (PART 9 event-based invalidation: "Delete on update/delete"). No-op when no
// cache is configured; errors are non-fatal.
func (h *PasteHandler) InvalidatePasteCache(id string) {
	if h.cache == nil {
		return
	}
	_ = h.cache.Delete(context.Background(), pasteCacheKey(id))
}

// ─── ID & token generation ────────────────────────────────────────────────────

// generateID returns an 8-character random alphanumeric string using crypto/rand.
func generateID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i, v := range b {
		b[i] = idCharset[int(v)%len(idCharset)]
	}
	return string(b), nil
}

// tokenCharset is the base62 alphabet for owner tokens (PART 11).
const tokenCharset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// generateOwnerToken generates a spec-compliant resource-owner token.
// Format: "tok_" prefix + 32 random base62 chars.
// Returns the raw plaintext token and its SHA-256 [32]byte hash.
func generateOwnerToken() (plaintext string, tokenHash [32]byte, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", tokenHash, err
	}
	b := make([]byte, 32)
	for i, v := range raw {
		b[i] = tokenCharset[int(v)%len(tokenCharset)]
	}
	plaintext = "tok_" + string(b)
	tokenHash = sha256.Sum256([]byte(plaintext))
	return plaintext, tokenHash, nil
}

// HashToken returns the SHA-256 hex digest of a token string. It is exported so
// the black-box package test (paste_test.go) can verify hashing determinism.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// extractToken pulls a bearer/owner token from the request using all accepted
// delivery mechanisms (in priority order):
//  1. Authorization: Bearer tok_...
//  2. Authorization: tok_...   (bare, no scheme prefix)
//  3. X-Api-Token: tok_...
//  4. X-Token: tok_...
//  5. X-Delete-Token: tok_...  (legacy compat header)
//  6. ?token= query param
//  7. JSON body {"token":"tok_..."}
func extractToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		if strings.HasPrefix(auth, "Bearer ") {
			return auth[len("Bearer "):]
		}
		// Bare token — no scheme prefix.
		return auth
	}
	for _, h := range []string{"X-Api-Token", "X-Token", "X-Delete-Token"} {
		if v := r.Header.Get(h); v != "" {
			return v
		}
	}
	if v := r.URL.Query().Get("token"); v != "" {
		return v
	}
	// Web form (no-JS create) supplies the token as a urlencoded field.
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		if v := r.PostFormValue("owner_token"); v != "" {
			return v
		}
	}
	// Browser cookie fallback is restricted to web forms; API routes never
	// receive ambient authority from cookies.
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		if cookie, err := r.Cookie("cairnbin_owner_token_rU3uW5Ze"); err == nil && cookie.Value != "" {
			return cookie.Value
		}
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var body struct {
			Token string `json:"token"`
		}
		// Peek without consuming — clone the body via a bytes buffer.
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<10))
		r.Body = io.NopCloser(bytes.NewReader(raw))
		_ = json.Unmarshal(raw, &body)
		return body.Token
	}
	return ""
}

// ─── Create ───────────────────────────────────────────────────────────────────

// CreateRequest is the JSON body for paste creation.
type CreateRequest struct {
	Content  string `json:"content"`
	Title    string `json:"title"`
	Language string `json:"language"`
	// Visibility is "public" | "unlisted".
	Visibility string `json:"visibility"`
	// ExpiresIn is "1h","1d","1w","1m","3m","6m","1y","18m","2y","never", or seconds.
	ExpiresIn string `json:"expires_in"`
	// BurnAfter is 0=disabled, 1-9999.
	BurnAfter int `json:"burn_after"`
	// ContentType is the detected MIME type for non-text uploads; empty = plain text.
	ContentType string `json:"content_type,omitempty"`
	// IsLink is never client-settable — it is derived automatically from
	// Content by isSingleURLContent after parsing. A `json:"-"` tag keeps it
	// out of the decoded request body entirely.
	IsLink bool `json:"-"`
}

// ValidateLinkTarget reports whether target is an absolute http:// or https://
// URL suitable for a redirect-shortener paste (is_link=true). The target is
// never fetched server-side — this is a scheme/format check only, so there is
// no SSRF exposure.
func ValidateLinkTarget(target string) bool {
	u, err := url.Parse(target)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return u.Host != ""
}

// isSingleURLContent reports whether content, once trimmed of surrounding
// whitespace, is exactly one absolute http:// or https:// URL and nothing
// else — no internal whitespace, no additional lines, no surrounding text.
// This is the sole trigger for auto-detecting a link-shortener paste: there
// is no client-settable is_link flag. A URL embedded in larger text, or any
// other content, is stored as a normal paste; raw/download/view-raw routes
// always return the literal stored content regardless, so nothing is hidden.
func isSingleURLContent(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	if strings.ContainsAny(trimmed, " \t\n\r") {
		return false
	}
	if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
		return false
	}
	return ValidateLinkTarget(trimmed)
}

// createFromRequest parses the request body (JSON, multipart, urlencoded, or
// raw), creates the paste, and returns the result. It writes NO HTTP response;
// callers render the outcome. On failure it returns an HTTP status and error.
func (h *PasteHandler) createFromRequest(r *http.Request) (*model.CreateResponse, int, error) {
	var req CreateRequest

	ct := r.Header.Get("Content-Type")

	limit := h.EffectiveReadLimit()

	switch {
	case strings.HasPrefix(ct, "application/json"):
		raw, tooLarge := readLimited(r.Body, limit)
		if tooLarge {
			return nil, http.StatusRequestEntityTooLarge, fmt.Errorf("paste exceeds maximum size of %d bytes", limit)
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, http.StatusBadRequest, fmt.Errorf("invalid JSON")
		}

	case strings.HasPrefix(ct, "multipart/form-data"):
		if err := r.ParseMultipartForm(limit); err != nil {
			return nil, http.StatusBadRequest, fmt.Errorf("failed to parse form")
		}
		file, header, err := r.FormFile("files")
		if err == nil {
			defer func() { _ = file.Close() }()
			raw, tooLarge := readLimited(file, limit)
			if tooLarge {
				return nil, http.StatusRequestEntityTooLarge, fmt.Errorf("paste exceeds maximum size of %d bytes", limit)
			}
			// Detect the MIME type from the actual file bytes before storage.
			detectedCT := http.DetectContentType(raw[:min512(len(raw))])
			// Non-text binary data (images, executables, archives, etc.) is
			// stored as base64 so it round-trips safely through the TEXT column.
			// ContentType is set so viewers know to decode before rendering.
			if strings.HasPrefix(detectedCT, "text/") {
				req.Content = string(raw)
			} else {
				req.Content = base64.StdEncoding.EncodeToString(raw)
				req.ContentType = detectedCT
			}
			req.Title = header.Filename
			req.Language = DetectLanguage(header.Filename)
		} else {
			req.Content = r.FormValue("content")
			req.Title = r.FormValue("title")
			req.Language = r.FormValue("language")
		}
		req.Visibility = r.FormValue("visibility")
		req.ExpiresIn = r.FormValue("expires_in")
		if ba, err := strconv.Atoi(r.FormValue("burn_after")); err == nil {
			req.BurnAfter = ba
		}

	case strings.HasPrefix(ct, "application/x-www-form-urlencoded"):
		if err := r.ParseForm(); err != nil {
			return nil, http.StatusBadRequest, fmt.Errorf("failed to parse form")
		}
		req.Content = r.FormValue("content")
		req.Title = r.FormValue("title")
		req.Language = r.FormValue("language")
		req.Visibility = r.FormValue("visibility")
		req.ExpiresIn = r.FormValue("expires_in")
		if ba, err := strconv.Atoi(r.FormValue("burn_after")); err == nil {
			req.BurnAfter = ba
		}

	default:
		// Raw body (curl --data-binary)
		raw, tooLarge := readLimited(r.Body, limit)
		if tooLarge {
			return nil, http.StatusRequestEntityTooLarge, fmt.Errorf("paste exceeds maximum size of %d bytes", limit)
		}
		req.Content = string(raw)
		req.Title = r.Header.Get("X-Title")
		req.Language = r.Header.Get("X-Language")
		req.ExpiresIn = r.Header.Get("X-Expires-In")
	}

	// is_link is auto-detected from content shape alone (see
	// isSingleURLContent) — there is no client-settable is_link flag/field/
	// header on any submission path (web form, JSON, multipart, raw body).
	req.IsLink = isSingleURLContent(req.Content)

	// Links (is_link=true) store the redirect target verbatim as plain text —
	// they never go through binary detection/base64 encoding, and language/
	// syntax highlighting do not apply.
	if req.IsLink {
		req.ContentType = ""
		req.Language = ""
		req.Content = strings.TrimSpace(req.Content)
		if !ValidateLinkTarget(req.Content) {
			return nil, http.StatusBadRequest, &validationError{field: "content", rule: "absolute_url", message: "content must be an absolute http:// or https:// URL"}
		}
	} else {
		// Binary uploads that arrive with ContentType already set (multipart, JSON
		// clients) MUST carry base64 content — reject anything that will not decode,
		// otherwise the stored paste can never be rendered or downloaded correctly.
		if req.ContentType != "" && !strings.HasPrefix(req.ContentType, "text/") {
			if _, err := base64.StdEncoding.DecodeString(req.Content); err != nil {
				return nil, http.StatusBadRequest, fmt.Errorf("binary content must be base64-encoded")
			}
		}

		// Detect binary MIME type BEFORE any trimming so binary bytes are never
		// modified, then base64-encode so the content round-trips safely through
		// the TEXT DB column. Plain text pastes keep ContentType empty.
		if req.ContentType == "" && len(req.Content) > 0 {
			sample := []byte(req.Content)
			if len(sample) > 512 {
				sample = sample[:512]
			}
			detected := http.DetectContentType(sample)
			if !strings.HasPrefix(detected, "text/") {
				req.ContentType = detected
				req.Content = base64.StdEncoding.EncodeToString([]byte(req.Content))
			}
		}

		// Only trim trailing newlines from plain-text content; base64-encoded binary
		// must not be modified after encoding (ContentType is set for binary uploads).
		if req.ContentType == "" {
			req.Content = strings.TrimRight(req.Content, "\n")
		}
	}
	if strings.TrimSpace(req.Content) == "" {
		return nil, http.StatusBadRequest, &validationError{field: "content", rule: "required", message: "content is required"}
	}

	// Visibility
	vis := model.VisibilityPublic
	if req.Visibility == "unlisted" || req.Visibility == "1" {
		vis = model.VisibilityUnlisted
	}

	// BurnAfter clamp
	burn := req.BurnAfter
	if burn < 0 {
		burn = 0
	}
	if burn > 9999 {
		burn = 9999
	}

	// Expiry
	var expiresAt *time.Time
	if req.ExpiresIn != "" && req.ExpiresIn != "never" {
		if t := ParseExpiry(req.ExpiresIn); t != nil {
			expiresAt = t
		}
	}

	// Language default — links never carry a language/syntax mode.
	if req.Language == "" && !req.IsLink {
		req.Language = "text"
	}
	if req.Title == "" {
		// A link paste with no explicit title defaults to its target URL,
		// not the generic "Untitled" fallback used by regular pastes.
		if req.IsLink {
			req.Title = req.Content
		} else {
			req.Title = "Untitled"
		}
	}

	// Generate unique paste ID
	var pasteID string
	for range 10 {
		id, err := generateID()
		if err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("failed to generate ID")
		}
		existing, _ := h.db.GetPasteByID(id)
		if existing == nil {
			pasteID = id
			break
		}
	}
	if pasteID == "" {
		return nil, http.StatusInternalServerError, fmt.Errorf("could not generate unique ID")
	}

	// Resolve owner token: reuse an existing valid token if the caller provides one,
	// otherwise generate a fresh tok_+32base62 token (PART 11).
	// An invalid/unknown provided token is non-fatal — a new token is generated instead,
	// so web-UI users who paste a stale token from CLI still get a working paste.
	var plainToken string
	var tokenHash [32]byte
	var ownerID string
	newOwner := false
	ownerDB, supportsOwners := h.db.(database.OwnerDB)
	if incoming := extractOwnerToken(r); incoming != "" {
		inHash := sha256.Sum256([]byte(incoming))
		var lookupErr error
		if supportsOwners {
			var tokenRec *database.APITokenRecord
			tokenRec, lookupErr = ownerDB.GetOwnerTokenByHash(inHash)
			if lookupErr == nil {
				ownerID = tokenRec.OwnerID
			}
		} else {
			lookupErr = h.db.ValidateAPIToken(inHash, "paste")
		}
		if lookupErr == nil {
			plainToken = incoming
			tokenHash = inHash
		}
	}
	if plainToken == "" {
		var err error
		plainToken, tokenHash, err = generateOwnerToken()
		if err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("failed to generate token")
		}
		ownerID, err = generateID()
		if err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("failed to generate owner identity")
		}
		newOwner = true
	}
	if supportsOwners && ownerID == "" {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to resolve owner identity")
	}

	paste := &model.Paste{
		ID:          pasteID,
		OwnerID:     ownerID,
		Title:       req.Title,
		Content:     req.Content,
		ContentType: req.ContentType,
		Language:    req.Language,
		Visibility:  vis,
		IsLink:      req.IsLink,
		ExpiresAt:   expiresAt,
		BurnAfter:   burn,
		Views:       0,
	}

	// A new owner's primary credential is stored with an encrypted recoverable copy.
	tokenPrefix := plainToken
	if len(tokenPrefix) > 12 {
		tokenPrefix = tokenPrefix[:12]
	}
	tokenEnc := ""
	if newOwner && supportsOwners {
		if len(h.tokenEncryptionKey) != 32 {
			return nil, http.StatusInternalServerError, fmt.Errorf("owner token encryption is unavailable")
		}
		sealed, err := secretbox.Seal(h.tokenEncryptionKey, []byte(plainToken))
		if err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("failed to encrypt owner token")
		}
		tokenEnc = base64.StdEncoding.EncodeToString(sealed)
		if err := ownerDB.CreatePasteWithPrimaryToken(paste, hex.EncodeToString(tokenHash[:]), tokenPrefix, plainToken[len(plainToken)-4:], tokenEnc); err != nil {
			log.Printf("warning: create owner and paste %s: %v", pasteID, err)
			return nil, http.StatusInternalServerError, fmt.Errorf("failed to create owner identity")
		}
	} else {
		if err := h.db.CreatePaste(paste); err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("failed to create paste")
		}
	}
	metric.PastesCreatedTotal.Inc()

	if newOwner && !supportsOwners {
		if err := h.db.CreateAPIToken(hex.EncodeToString(tokenHash[:]), tokenPrefix, "paste", pasteID, expiresAt); err != nil {
			log.Printf("warning: create legacy api_token for paste %s: %v", pasteID, err)
		}
		h.refreshActiveTokenGauge()
	} else if supportsOwners {
		h.refreshActiveTokenGauge()
	}

	link := h.PasteURL(r, paste.ID)
	resp := &model.CreateResponse{
		ID:         paste.ID,
		Title:      paste.Title,
		Language:   paste.Language,
		Visibility: paste.Visibility,
		IsLink:     paste.IsLink,
		BurnAfter:  paste.BurnAfter,
		ExpiresAt:  paste.ExpiresAt,
		Views:      0,
		CreatedAt:  paste.CreatedAt,
		Link:       link,
		OwnerToken: func() string {
			if newOwner {
				return plainToken
			}
			return ""
		}(),
	}
	return resp, 0, nil
}

// CreateFromForm creates a paste from an HTML form POST and returns the result
// for server-side template rendering — the no-JS web create flow (PART 16).
func (h *PasteHandler) CreateFromForm(r *http.Request) (*model.CreateResponse, int, error) {
	return h.createFromRequest(r)
}

// CreatePaste handles paste creation for API and CLI callers (JSON, multipart,
// raw, or urlencoded) and writes the response per content negotiation.
func (h *PasteHandler) CreatePaste(w http.ResponseWriter, r *http.Request) {
	resp, status, err := h.createFromRequest(r)
	if err != nil {
		var verr *validationError
		if errors.As(err, &verr) {
			sendAPIErrorDetails(w, r, "VALIDATION_FAILED", verr.message,
				map[string]interface{}{"field": verr.field, "rule": verr.rule})
			return
		}
		sendAPIError(w, r, httpErrCode(status), err.Error())
		return
	}

	ct := r.Header.Get("Content-Type")
	accept := r.Header.Get("Accept")
	isAPI := strings.HasPrefix(r.URL.Path, "/api/")
	isJSON := strings.Contains(accept, "application/json")

	// Browser form submit without JS that reaches the API handler directly:
	// redirect to the paste view (the /create web route renders a confirmation).
	if !isAPI && !isJSON && strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
		http.Redirect(w, r, "/"+resp.ID, http.StatusSeeOther)
		return
	}

	// curl / raw / non-JSON API callers: return the URL as plain text.
	if !isAPI && !isJSON {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, "OK: paste created\n%s\n", resp.Link)
		return
	}

	if httputil.GetAPIResponseFormat(r) == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, "OK: paste created\n%s\n", resp.Link)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{"ok": true, "data": resp})
}

// ─── Get ──────────────────────────────────────────────────────────────────────

// GetPaste returns paste JSON (burns if applicable).
func (h *PasteHandler) GetPaste(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	paste, err := h.loadLivePaste(w, r, id)
	if paste == nil || err != nil {
		return
	}

	views, burned, verr := h.db.IncrementViewsAndCheckBurn(id)
	if verr == nil {
		paste.Views = views
	} else {
		paste.Views++
	}
	metric.PastesViewedTotal.Inc()

	if burned {
		h.InvalidatePasteCache(id)
		metric.PastesDeletedTotal.Inc()
	}

	// Never return delete token hash.
	paste.DeleteTokenHash = ""

	// Links redirect instead of rendering — the raw endpoint (GetRawPaste) still
	// returns the target URL as plain text, no redirect, for parity with other
	// raw endpoints.
	if paste.IsLink {
		http.Redirect(w, r, paste.Content, http.StatusFound)
		return
	}

	// Content negotiation: text format returns key=value summary (PART 14).
	if httputil.GetAPIResponseFormat(r) == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintf(w, "OK: paste retrieved\nid: %s\ntitle: %s\nlanguage: %s\nviews: %d\ncreated: %s\n",
			paste.ID, paste.Title, paste.Language, paste.Views, paste.CreatedAt.Format(time.RFC3339))
		if paste.ExpiresAt != nil {
			_, _ = fmt.Fprintf(w, "expires: %s\n", paste.ExpiresAt.Format(time.RFC3339))
		}
		_, _ = fmt.Fprintf(w, "\n%s\n", paste.Content)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "data": paste})
}

// activeContentTypes are MIME types that a browser can execute or interpret as
// markup when served inline. Per PART 11 security rules, these MUST be forced
// to Content-Disposition: attachment so browsers treat them as downloads.
var activeContentTypes = map[string]bool{
	"text/html":              true,
	"application/xhtml+xml":  true,
	"image/svg+xml":          true,
	"text/xml":               true,
	"application/xml":        true,
	"application/javascript": true,
	"text/javascript":        true,
}

// GetRawPaste returns paste content with a MIME type detected from the content
// bytes (PART 11: exact Content-Type + nosniff; active types forced to attachment).
func (h *PasteHandler) GetRawPaste(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	paste, err := h.loadLivePaste(w, r, id)
	if paste == nil || err != nil {
		return
	}

	_, burned, verr := h.db.IncrementViewsAndCheckBurn(id)
	metric.PastesViewedTotal.Inc()

	if verr == nil && burned {
		h.InvalidatePasteCache(id)
		metric.PastesDeletedTotal.Inc()
	}

	// Binary pastes are stored as base64 to survive the TEXT DB column; decode
	// them back to raw bytes before serving. Text pastes use the string directly.
	var body []byte
	if paste.ContentType != "" && !strings.HasPrefix(paste.ContentType, "text/") {
		if decoded, err := base64.StdEncoding.DecodeString(paste.Content); err == nil {
			body = decoded
		} else {
			body = []byte(paste.Content)
		}
	} else {
		body = []byte(paste.Content)
	}

	// Use the stored ContentType when available; otherwise sniff from bytes.
	detected := paste.ContentType
	if detected == "" {
		detected = http.DetectContentType(body[:min512(len(body))])
	}

	// Canonicalize the media type before the allow-list check so that
	// case, surrounding whitespace, or trailing parameters cannot smuggle an
	// active type past the check (e.g. "TEXT/HTML", "text/html ").
	// mime.ParseMediaType lowercases the base type and trims whitespace;
	// unparseable values fail closed as a forced download.
	baseType, params, err := mime.ParseMediaType(detected)
	var served string
	if err != nil {
		baseType = ""
		served = "application/octet-stream"
		w.Header().Set("Content-Disposition", "attachment")
	} else {
		served = mime.FormatMediaType(baseType, params)
		if served == "" {
			served = baseType
		}
	}

	// Active content types must be served as downloads to prevent browser execution.
	if activeContentTypes[baseType] {
		w.Header().Set("Content-Disposition", "attachment")
	}

	w.Header().Set("Content-Type", served)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(body) //nolint:errcheck
}

// min512 returns n capped at 512 for the DetectContentType sniff buffer.
func min512(n int) int {
	if n > 512 {
		return 512
	}
	return n
}

// readLimited reads r up to limit bytes and reports whether the stream had
// more data beyond that (i.e. the caller's payload exceeds the configured
// paste.max_size). It reads limit+1 bytes so an exact-limit-sized body
// is never mistaken for an oversized one.
func readLimited(r io.Reader, limit int64) (data []byte, tooLarge bool) {
	raw, _ := io.ReadAll(io.LimitReader(r, limit+1))
	if int64(len(raw)) > limit {
		return raw[:limit], true
	}
	return raw, false
}

// ─── List ─────────────────────────────────────────────────────────────────────

// ListPastes returns paginated public pastes as JSON.
func (h *PasteHandler) ListPastes(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 {
		limit = 250
	}
	if limit > 250 {
		limit = 250
	}

	// search/sort/order (TODO.md: search recents by id/name/content, sort by
	// name/date/etc.) — SearchPublicPastes falls back to the same "date desc"
	// ordering GetPublicPastes always used when these are empty, so it is a
	// drop-in replacement for the plain listing case too.
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	sortBy := strings.TrimSpace(r.URL.Query().Get("sort"))
	order := strings.TrimSpace(r.URL.Query().Get("order"))

	pastes, total, err := h.db.SearchPublicPastes(page, limit, search, sortBy, order)
	if err != nil {
		sendAPIError(w, r, "SERVER_ERROR", "failed to fetch pastes")
		return
	}

	// Content negotiation: text format returns a tab-separated list (PART 14).
	if httputil.GetAPIResponseFormat(r) == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintf(w, "OK: %d pastes found (page %d)\n", total, page)
		for _, p := range pastes {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", p.ID, p.Language, p.Title)
		}
		return
	}

	pages := (total + limit - 1) / limit
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":   true,
		"data": pastes,
		"pagination": map[string]interface{}{
			"page":  page,
			"limit": limit,
			"total": total,
			"pages": pages,
		},
	})
}

// ─── Delete ───────────────────────────────────────────────────────────────────

// DeletePaste deletes a paste using two-tier auth (PART 11):
//  1. Authorization: Bearer <token> — primary delivery
//  2. If the token matches server.token (operator) → delete unconditionally
//  3. Otherwise → verify token against api_tokens for this paste
//
// Legacy fallbacks accepted for compatibility:
//   - ?token=tok_... query param
//   - X-Delete-Token: tok_... header
//   - JSON body {"token":"tok_..."}
func (h *PasteHandler) DeletePaste(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	token := extractOwnerToken(r)
	if token == "" {
		sendAPIError(w, r, "UNAUTHORIZED", "owner token required (Authorization: Bearer tok_...)")
		return
	}

	incomingHash := sha256.Sum256([]byte(token))

	// Tier 1: operator token — allows deleting any paste.
	var zeroHash [32]byte
	if h.operatorTokenHash != zeroHash &&
		subtle.ConstantTimeCompare(incomingHash[:], h.operatorTokenHash[:]) == 1 {
		if err := h.db.DeletePaste(id); err != nil {
			sendAPIError(w, r, "NOT_FOUND", "paste not found")
			return
		}
		h.InvalidatePasteCache(id)
		metric.PastesDeletedTotal.Inc()
		if httputil.GetAPIResponseFormat(r) == "text" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = fmt.Fprintln(w, "OK: paste deleted")
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "data": map[string]string{"message": "paste deleted"}})
		return
	}

	// Tier 2: resource-owner token — must match api_tokens for this paste.
	if err := h.db.VerifyAPIToken(incomingHash, "paste", id); err != nil {
		sendAPIError(w, r, "NOT_FOUND", "paste not found or invalid token")
		return
	}
	if err := h.db.DeletePaste(id); err != nil {
		sendAPIError(w, r, "NOT_FOUND", "paste not found")
		return
	}
	h.InvalidatePasteCache(id)
	metric.PastesDeletedTotal.Inc()
	h.refreshActiveTokenGauge()

	if httputil.GetAPIResponseFormat(r) == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintln(w, "OK: paste deleted")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "data": map[string]string{"message": "paste deleted"}})
}

// extractOwnerToken enforces the PART 11 credential boundary: native API
// routes use an Authorization bearer token only; web forms may use a token
// field or the project owner cookie.
func extractOwnerToken(r *http.Request) string {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		auth := r.Header.Get("Authorization")
		if strings.HasPrefix(auth, "Bearer ") {
			return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		}
		return ""
	}
	return extractToken(r)
}

// ─── Web view helpers ─────────────────────────────────────────────────────────

// GetPasteForWeb returns the paste struct for server-side template rendering.
// Increments views and handles burn logic. Returns nil if paste is unavailable.
func (h *PasteHandler) GetPasteForWeb(id string) (*model.Paste, error) {
	paste, hit := h.getCachedPaste(id)
	if !hit {
		var err error
		paste, err = h.db.GetPasteByID(id)
		if err != nil {
			return nil, err
		}
		if paste == nil {
			return nil, nil
		}
		h.cachePaste(paste)
	}
	if paste.ExpiresAt != nil && paste.ExpiresAt.Before(time.Now()) {
		_ = h.db.DeletePaste(id)
		h.InvalidatePasteCache(id)
		return nil, nil
	}

	views, burned, verr := h.db.IncrementViewsAndCheckBurn(id)
	if verr == nil {
		paste.Views = views
	} else {
		paste.Views++
	}
	metric.PastesViewedTotal.Inc()

	if burned {
		h.InvalidatePasteCache(id)
		metric.PastesDeletedTotal.Inc()
	}

	paste.DeleteTokenHash = ""
	return paste, nil
}

// HighlightedContent returns Chroma-highlighted HTML for the paste content.
// Falls back to HTML-escaped plain text if the language is unknown or highlighting fails.
// Returns empty string for binary pastes (ContentType set, non-text) — callers render
// those via <img>, <audio>, <video>, or a download prompt instead.
// When lineNumbers is true, line numbers are rendered in their own table cell
// (styled non-selectable via CSS) so they are never included in copy/selection.
func HighlightedContent(paste *model.Paste, lineNumbers bool) template.HTML {
	if paste.ContentType != "" && !strings.HasPrefix(paste.ContentType, "text/") {
		return ""
	}

	lexer := lexers.Get(paste.Language)
	if lexer == nil {
		lexer = lexers.Fallback
	}

	style := styles.Get("github-dark")
	if style == nil {
		style = styles.Fallback
	}

	formatter := chromahtml.New(
		chromahtml.TabWidth(4),
		chromahtml.WithLineNumbers(lineNumbers),
		chromahtml.LineNumbersInTable(lineNumbers),
	)

	iterator, err := lexer.Tokenise(nil, paste.Content)
	if err != nil {
		return template.HTML(template.HTMLEscapeString(paste.Content))
	}

	var buf bytes.Buffer
	if err := formatter.Format(&buf, style, iterator); err != nil {
		return template.HTML(template.HTMLEscapeString(paste.Content))
	}

	return template.HTML(buf.String())
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// loadLivePaste retrieves a paste by ID, enforces expiry, and writes an error
// response if unavailable. Returns nil when a response has already been written.
func (h *PasteHandler) loadLivePaste(w http.ResponseWriter, r *http.Request, id string) (*model.Paste, error) {
	paste, hit := h.getCachedPaste(id)
	if !hit {
		var err error
		paste, err = h.db.GetPasteByID(id)
		if err != nil {
			sendAPIError(w, r, "SERVER_ERROR", "internal server error")
			return nil, err
		}
		if paste == nil {
			sendAPIError(w, r, "NOT_FOUND", "paste not found")
			return nil, nil
		}
		h.cachePaste(paste)
	}
	if paste.ExpiresAt != nil && paste.ExpiresAt.Before(time.Now()) {
		_ = h.db.DeletePaste(id)
		h.InvalidatePasteCache(id)
		sendAPIError(w, r, "GONE", "paste has expired")
		return nil, nil
	}
	return paste, nil
}

// refreshActiveTokenGauge recomputes the active API-token gauge from the
// database. ListAPITokens returns only non-revoked rows (PART 20).
func (h *PasteHandler) refreshActiveTokenGauge() {
	toks, err := h.db.ListAPITokens()
	if err != nil {
		return
	}
	metric.APITokensActive.Set(float64(len(toks)))
}

func (h *PasteHandler) PasteURL(r *http.Request, id string) string {
	return h.Base(r) + "/" + id
}

// Base resolves the per-request absolute base URL (no trailing slash). It
// delegates to the injected server resolver (PART 12 full chain) when present;
// otherwise it honours the static baseURL override, and finally falls back to
// the bare connection scheme+Host. X-Forwarded-* headers are never read here —
// the trusted-proxy gate is the server resolver's responsibility (PART 12).
func (h *PasteHandler) Base(r *http.Request) string {
	if h.baseURLFn != nil {
		return strings.TrimRight(h.baseURLFn(r), "/")
	}
	if h.baseURL != "" {
		return strings.TrimRight(h.baseURL, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// CreatePasteInternal is used by compatibility handlers to create pastes without
// going through HTTP request parsing.
// Returns the new paste ID and a raw delete token (for compat protocols that
// need to return it, e.g. lenpaste). The delete token is stored as a SHA-256
// hash in paste.DeleteTokenHash and is NOT part of the api_tokens system.
func (h *PasteHandler) CreatePasteInternal(
	title, content, lang string,
	vis, burnAfter int,
	expiresAt *time.Time,
) (pasteID, deleteToken string, err error) {
	if title == "" {
		title = "Untitled"
	}
	if lang == "" {
		lang = "text"
	}

	// Detect non-text binary content and store it as base64 with ContentType
	// set so viewers render it correctly (same rule as HTTP upload parsing).
	var contentType string
	if len(content) > 0 {
		sample := []byte(content)
		if len(sample) > 512 {
			sample = sample[:512]
		}
		if detected := http.DetectContentType(sample); !strings.HasPrefix(detected, "text/") {
			contentType = detected
			content = base64.StdEncoding.EncodeToString([]byte(content))
		}
	}

	for range 10 {
		id, e := generateID()
		if e != nil {
			return "", "", e
		}
		existing, _ := h.db.GetPasteByID(id)
		if existing == nil {
			pasteID = id
			break
		}
	}
	if pasteID == "" {
		return "", "", fmt.Errorf("could not generate unique paste ID")
	}

	// Generate a compat delete token: 16 random bytes hex-encoded.
	// Store its SHA-256 hash in the paste row; NOT stored in api_tokens.
	rawBytes := make([]byte, 16)
	if _, e := rand.Read(rawBytes); e != nil {
		return "", "", e
	}
	deleteToken = hex.EncodeToString(rawBytes)

	paste := &model.Paste{
		ID:              pasteID,
		Title:           title,
		Content:         content,
		ContentType:     contentType,
		Language:        lang,
		Visibility:      vis,
		ExpiresAt:       expiresAt,
		BurnAfter:       burnAfter,
		Views:           0,
		DeleteTokenHash: HashToken(deleteToken),
	}

	if e := h.db.CreatePaste(paste); e != nil {
		return "", "", e
	}
	return pasteID, deleteToken, nil
}

// httpErrCode maps HTTP status to a canonical error code string (PART 9), for
// call sites that only have a dynamic http.Status* value on hand.
func httpErrCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "BAD_REQUEST"
	case http.StatusUnauthorized:
		return "UNAUTHORIZED"
	case http.StatusForbidden:
		return "FORBIDDEN"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusMethodNotAllowed:
		return "METHOD_NOT_ALLOWED"
	case http.StatusConflict:
		return "CONFLICT"
	case http.StatusGone:
		return "GONE"
	case http.StatusRequestEntityTooLarge:
		return "PAYLOAD_TOO_LARGE"
	case http.StatusTooManyRequests:
		return "RATE_LIMITED"
	case http.StatusServiceUnavailable:
		return "MAINTENANCE"
	default:
		return "SERVER_ERROR"
	}
}

// mapAPIErrorCodeToHTTPStatus maps a canonical error code to its HTTP status (PART 9).
func mapAPIErrorCodeToHTTPStatus(code string) int {
	switch code {
	case "BAD_REQUEST", "VALIDATION_FAILED":
		return http.StatusBadRequest
	case "UNAUTHORIZED", "TOKEN_EXPIRED", "TOKEN_INVALID":
		return http.StatusUnauthorized
	case "FORBIDDEN", "ACCOUNT_LOCKED", "CSRF_FAILED":
		return http.StatusForbidden
	case "NOT_FOUND":
		return http.StatusNotFound
	case "METHOD_NOT_ALLOWED":
		return http.StatusMethodNotAllowed
	case "CONFLICT":
		return http.StatusConflict
	case "GONE":
		return http.StatusGone
	case "PAYLOAD_TOO_LARGE":
		return http.StatusRequestEntityTooLarge
	case "RATE_LIMITED":
		return http.StatusTooManyRequests
	case "MAINTENANCE":
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// sendAPIError writes a canonical error response using the error code (PART 9).
// Content negotiation (PART 16 "Text Response Format"): text/plain and .txt
// callers get "ERROR: {code}: {message}"; everyone else gets the JSON envelope.
func sendAPIError(w http.ResponseWriter, r *http.Request, code, message string) {
	sendAPIErrorDetails(w, r, code, message, nil)
}

// validationError is a request-field validation failure. It is reported as
// VALIDATION_FAILED with a structured details object naming the field and the
// rule that failed (PART 14 "Error Response").
type validationError struct {
	field   string
	rule    string
	message string
}

func (e *validationError) Error() string { return e.message }

// sendAPIErrorDetails is sendAPIError plus the optional structured `details`
// object; details is omitted from the envelope when nil or empty. Plain-text
// callers get the same one-line "ERROR: {code}: {message}" form.
func sendAPIErrorDetails(w http.ResponseWriter, r *http.Request, code, message string, details map[string]interface{}) {
	status := mapAPIErrorCodeToHTTPStatus(code)
	if httputil.GetAPIResponseFormat(r) == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, "ERROR: %s: %s\n", code, message)
		return
	}
	body := map[string]interface{}{
		"ok":      false,
		"error":   code,
		"message": message,
	}
	if len(details) > 0 {
		body["details"] = details
	}
	writeJSON(w, status, body)
}

// ─── Expiry parsing ───────────────────────────────────────────────────────────

// ParseExpiry converts an expiry string to an absolute time.
// Accepts: 1h 1d 1w 1m 3m 6m 1y 18m 2y or raw seconds as a decimal string.
func ParseExpiry(s string) *time.Time {
	var d time.Duration
	switch s {
	case "1h":
		d = time.Hour
	case "1d":
		d = 24 * time.Hour
	case "1w":
		d = 7 * 24 * time.Hour
	case "1m":
		d = 30 * 24 * time.Hour
	case "3m":
		d = 90 * 24 * time.Hour
	case "6m":
		d = 180 * 24 * time.Hour
	case "18m":
		d = 540 * 24 * time.Hour
	case "1y":
		d = 365 * 24 * time.Hour
	case "2y":
		d = 730 * 24 * time.Hour
	default:
		// Try raw seconds. Cap at 10 years: time.Duration is int64 nanoseconds,
		// so sec * 1e9 overflows past ~9.2e9 seconds and wraps negative, which
		// would mark the paste as already expired. Reject out-of-range values.
		const maxExpirySeconds = 10 * 365 * 24 * 60 * 60
		if sec, err := strconv.ParseInt(s, 10, 64); err == nil && sec > 0 && sec <= maxExpirySeconds {
			d = time.Duration(sec) * time.Second
		} else {
			return nil
		}
	}
	t := time.Now().Add(d)
	return &t
}

// ─── Language detection ───────────────────────────────────────────────────────

// DetectLanguage infers a syntax-highlighting language name from a filename extension.
func DetectLanguage(filename string) string {
	ext := strings.ToLower(filename)
	if idx := strings.LastIndex(ext, "."); idx != -1 {
		ext = ext[idx+1:]
	}

	m := map[string]string{
		"js":         "javascript",
		"ts":         "typescript",
		"jsx":        "jsx",
		"tsx":        "tsx",
		"py":         "python",
		"rb":         "ruby",
		"go":         "go",
		"rs":         "rust",
		"java":       "java",
		"c":          "c",
		"cpp":        "cpp",
		"cc":         "cpp",
		"h":          "c",
		"hpp":        "cpp",
		"cs":         "csharp",
		"php":        "php",
		"sh":         "bash",
		"bash":       "bash",
		"zsh":        "bash",
		"fish":       "bash",
		"ps1":        "powershell",
		"html":       "html",
		"htm":        "html",
		"css":        "css",
		"scss":       "scss",
		"sass":       "sass",
		"json":       "json",
		"yaml":       "yaml",
		"yml":        "yaml",
		"toml":       "toml",
		"xml":        "xml",
		"sql":        "sql",
		"md":         "markdown",
		"txt":        "text",
		"lua":        "lua",
		"r":          "r",
		"swift":      "swift",
		"kt":         "kotlin",
		"dart":       "dart",
		"ex":         "elixir",
		"exs":        "elixir",
		"erl":        "erlang",
		"hs":         "haskell",
		"clj":        "clojure",
		"scala":      "scala",
		"pl":         "perl",
		"ini":        "ini",
		"conf":       "ini",
		"env":        "bash",
		"diff":       "diff",
		"patch":      "diff",
		"dockerfile": "dockerfile",
		"makefile":   "makefile",
		"mk":         "makefile",
	}

	if lang, ok := m[ext]; ok {
		return lang
	}
	return "text"
}

// writeJSON encodes v as JSON (without HTML-escaping) and writes it to w
// with the given HTTP status code. Delegates to httputil.WriteJSON so the
// encoding behavior stays identical to the copy in src/server (AI.md PART 14
// "Response Formatting" — deduplication tracked for the server.go side).
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	httputil.WriteJSON(w, status, v)
}
