package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/common/browsercookie"
	"github.com/apimgr/cairnbin/src/common/secretbox"
	"github.com/apimgr/cairnbin/src/config"
	"github.com/apimgr/cairnbin/src/database"
)

func ownerTokenTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	base := filepath.Join(os.TempDir(), "apimgr")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "cairnbin-owner-tokens-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	db, err := database.NewDatabase("sqlite", filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := config.DefaultConfig()
	cfg.Server.Security.EncryptionKey = strings.Repeat("1", 64)
	primary := "own_test-primary-token-value-000000000000"
	hash := sha256.Sum256([]byte(primary))
	key, err := cfg.EncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := secretbox.Seal(key, []byte(primary))
	if err != nil {
		t.Fatal(err)
	}
	ownerDB := db.(database.OwnerDB)
	if err := ownerDB.CreateOwnerWithPrimaryToken("owner-test", fmt.Sprintf("%x", hash[:]), primary[:12], primary[len(primary)-4:], base64.StdEncoding.EncodeToString(sealed)); err != nil {
		t.Fatal(err)
	}
	s := newMinimalServer(cfg)
	s.db = db
	return s, primary
}

func TestLinkedTokenAPIUsesOpaqueIDAndReturnsCanonicalShape(t *testing.T) {
	s, primary := ownerTokenTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/server/preferences/tokens", strings.NewReader(`{"name":"automation","expires_on":""}`))
	req.Header.Set("Authorization", "Bearer "+primary)
	rec := httptest.NewRecorder()
	s.handleLinkedTokenCreate(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	id, _ := envelope.Data["id"].(string)
	raw, _ := envelope.Data["linked_token"].(string)
	if len(id) != 36 || id[8] != '-' || id[14] != '4' || (id[19] != '8' && id[19] != '9' && id[19] != 'a' && id[19] != 'b') {
		t.Errorf("public token ID=%q, want UUID v4", id)
	}
	if !strings.HasPrefix(raw, "own_") || envelope.Data["token_masked"] == "" || envelope.Data["kind"] != "linked" {
		t.Errorf("linked token response has wrong fields: %#v", envelope.Data)
	}
	for _, key := range []string{"created_at", "last_used_at", "expires_at", "status"} {
		if _, ok := envelope.Data[key]; !ok {
			t.Errorf("linked token response omitted %q", key)
		}
	}
	listed := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/server/preferences/tokens", nil)
	listReq.Header.Set("Authorization", "Bearer "+primary)
	s.handleOwnerTokensAPI(listed, listReq)
	if strings.Contains(listed.Body.String(), raw) || !strings.Contains(listed.Body.String(), id) {
		t.Errorf("token list leaked the raw credential or omitted its ID: %s", listed.Body.String())
	}
}

func TestLinkedTokenAPIRejectsDateInExpiresField(t *testing.T) {
	s, primary := ownerTokenTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/server/preferences/tokens", strings.NewReader(`{"name":"automation","expires":"2030-01-01"}`))
	req.Header.Set("Authorization", "Bearer "+primary)
	rec := httptest.NewRecorder()
	s.handleLinkedTokenCreate(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expires date status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
}

func TestOwnerTokenRevealUsesOneUseViewCookie(t *testing.T) {
	s, primary := ownerTokenTestServer(t)
	post := httptest.NewRequest(http.MethodPost, "/server/preferences/token/reveal", nil)
	post.AddCookie(&http.Cookie{Name: ownerCookieName, Value: primary})
	rec := httptest.NewRecorder()
	s.handleOwnerTokenRevealWeb(rec, post)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/server/preferences/tokens#primary" {
		t.Fatalf("reveal response status=%d location=%q, want 303 to primary", rec.Code, rec.Header().Get("Location"))
	}
	var viewCookie *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == tokenViewCookieName {
			viewCookie = cookie
		}
	}
	if viewCookie == nil || viewCookie.Value != "primary" || viewCookie.Path != "/server/preferences/tokens" || viewCookie.MaxAge != 60 || !viewCookie.HttpOnly || !viewCookie.Secure || viewCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("view cookie=%#v, want secure one-use primary cookie", viewCookie)
	}

	get := httptest.NewRequest(http.MethodGet, "/server/preferences/tokens", nil)
	get.AddCookie(&http.Cookie{Name: ownerCookieName, Value: primary})
	get.AddCookie(viewCookie)
	if cookie, err := get.Cookie(tokenViewCookieName); err != nil || cookie.Value != "primary" {
		t.Fatalf("GET token view cookie=%#v err=%v", cookie, err)
	}
	owner, ownerDB, ownerOK := s.requireOwnerForPage(get)
	if !ownerOK {
		t.Fatal("test owner cookie did not resolve")
	}
	record, err := ownerDB.GetOwnerTokenByID(owner.OwnerID, owner.ID)
	if err != nil || record.Kind != "primary" || record.TokenEnc == "" {
		t.Fatalf("primary token record=%#v err=%v", record, err)
	}
	decrypted, err := s.decryptOwnerToken(record.TokenEnc)
	if err != nil || decrypted != primary {
		t.Fatalf("primary token decrypt=%q err=%v", decrypted, err)
	}
	viewData := map[string]interface{}{}
	page := httptest.NewRecorder()
	s.consumeTokenView(page, get, viewData)
	if viewData["RevealedOwnerToken"] != primary || page.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("consumed token view data=%#v cache=%q", viewData, page.Header().Get("Cache-Control"))
	}
	cleared := false
	for _, cookie := range page.Result().Cookies() {
		if cookie.Name == tokenViewCookieName && cookie.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("token view cookie was not cleared after consumption")
	}

	reloadData := map[string]interface{}{}
	reload := httptest.NewRecorder()
	reloadRequest := httptest.NewRequest(http.MethodGet, "/server/preferences/tokens", nil)
	reloadRequest.AddCookie(&http.Cookie{Name: ownerCookieName, Value: primary})
	s.consumeTokenView(reload, reloadRequest, reloadData)
	if _, ok := reloadData["RevealedOwnerToken"]; ok {
		t.Fatal("token page reload disclosed the primary token without a view cookie")
	}
}

func TestOwnerCookieMiddlewareMigratesLegacyPrimaryCookie(t *testing.T) {
	s, primary := ownerTokenTestServer(t)
	request := httptest.NewRequest(http.MethodGet, "/server/preferences/tokens", nil)
	request.AddCookie(&http.Cookie{Name: browsercookie.LegacyOwnerTokenName, Value: primary})
	recorder := httptest.NewRecorder()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	s.ownerCookieMiddleware(next).ServeHTTP(recorder, request)
	var migrated, clearedLegacy bool
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == browsercookie.OwnerTokenName && cookie.Value == primary && cookie.MaxAge > 0 && cookie.HttpOnly && cookie.Secure {
			migrated = true
		}
		if cookie.Name == browsercookie.LegacyOwnerTokenName && cookie.MaxAge < 0 {
			clearedLegacy = true
		}
	}
	if !migrated || !clearedLegacy {
		t.Fatalf("legacy cookie migration did not set current and clear legacy cookie: %#v", recorder.Result().Cookies())
	}
}

func TestOwnerCookieMigrationExpiresAfterConfiguredMaxAge(t *testing.T) {
	s, _ := ownerTokenTestServer(t)
	s.startTime = time.Now().Add(-s.ownerCookieMaxAge())
	request := httptest.NewRequest(http.MethodGet, "/server/preferences/tokens", nil)
	request.AddCookie(&http.Cookie{Name: browsercookie.LegacyOwnerTokenName, Value: "legacy"})
	if cookies := s.ownerTokenCookies(request); len(cookies) != 0 {
		t.Fatalf("legacy cookie remained accepted after the migration window: %#v", cookies)
	}
	recorder := httptest.NewRecorder()
	s.ownerCookieMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie(browsercookie.LegacyOwnerTokenName); err == nil {
			t.Error("expired legacy cookie reached the handler")
		}
	})).ServeHTTP(recorder, request)
	cleared := false
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == browsercookie.LegacyOwnerTokenName && cookie.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("expired legacy cookie was not cleared")
	}
}

func TestExpiryDateUsesFollowingUTCmidnight(t *testing.T) {
	date := time.Now().UTC().AddDate(0, 0, 3).Format("2006-01-02")
	got, err := expiryFromChoice(date)
	if err != nil {
		t.Fatal(err)
	}
	day, err := time.Parse("2006-01-02", date)
	if err != nil {
		t.Fatal(err)
	}
	want := day.AddDate(0, 0, 1)
	if got == nil || !got.Equal(want) || got.Location() != time.UTC {
		t.Errorf("expiry=%v, want %v UTC", got, want)
	}
}
