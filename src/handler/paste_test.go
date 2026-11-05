package handler_test

// Tests for PasteHandler HTTP operations.
// All tests use net/http/httptest and a real SQLite database so that the
// tests exercise the full handler→database path, not just mocks.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/cache"
	"github.com/apimgr/cairnbin/src/database"
	"github.com/apimgr/cairnbin/src/handler"
	"github.com/go-chi/chi/v5"
)

// ─── Test helpers ──────────────────────────────────────────────────────────────

func newTestDB(t *testing.T) database.DB {
	t.Helper()
	base := filepath.Join(os.TempDir(), "apimgr")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "cairnbin-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	db, err := database.NewDatabase("sqlite", filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func newTestHandler(t *testing.T) (*handler.PasteHandler, database.DB) {
	t.Helper()
	db := newTestDB(t)
	h := handler.NewPasteHandler(db, "", [32]byte{})
	h.SetTokenEncryptionKey(bytes.Repeat([]byte{1}, 32))
	return h, db
}

// withID injects a chi URL param "id" into the request context so that
// chi.URLParam(r, "id") works without a full router.
func withID(r *http.Request, id string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// createViaAPI posts JSON to the handler and returns the parsed response body.
func createViaAPI(t *testing.T, h *handler.PasteHandler, body string) map[string]interface{} {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pastes",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	rr := httptest.NewRecorder()
	h.CreatePaste(rr, req)

	var m map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&m); err != nil {
		t.Fatalf("decode response: %v\nbody: %s", err, rr.Body.String())
	}
	return m
}

// ─── CreatePaste ──────────────────────────────────────────────────────────────

// TestCreatePaste_JSON verifies JSON create returns 201 with ok:true and an id.
func TestCreatePaste_JSON(t *testing.T) {
	h, _ := newTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pastes",
		strings.NewReader(`{"content":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	rr := httptest.NewRecorder()
	h.CreatePaste(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want %d", rr.Code, http.StatusCreated)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["ok"] != true {
		t.Errorf("ok: got %v, want true", resp["ok"])
	}
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatal("data field missing or wrong type")
	}
	id, ok := data["id"].(string)
	if !ok || id == "" {
		t.Error("id missing or empty in response data")
	}
}

// TestCreatePaste_EmptyContent expects 400 when content is empty.
func TestCreatePaste_EmptyContent(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"empty string", `{"content":""}`},
		{"missing field", `{}`},
		{"whitespace only", `{"content":"   "}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newTestHandler(t)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/pastes",
				strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json")
			req.Header.Set("User-Agent", "Mozilla/5.0")

			rr := httptest.NewRecorder()
			h.CreatePaste(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Errorf("status: got %d, want 400", rr.Code)
			}
		})
	}
}

// TestCreatePaste_Form verifies form submit redirects to /{id} with 303.
func TestCreatePaste_Form(t *testing.T) {
	h, _ := newTestHandler(t)

	form := url.Values{"content": {"hello from form"}}
	req := httptest.NewRequest(http.MethodPost, "/paste",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Do NOT set Accept: application/json — browser form path.

	rr := httptest.NewRecorder()
	h.CreatePaste(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status: got %d, want 303", rr.Code)
	}
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, "/") {
		t.Errorf("Location %q should start with /", loc)
	}
	// Location must be /{id} — 8-char alphanumeric id after the slash.
	if len(loc) < 2 {
		t.Errorf("Location %q too short", loc)
	}
}

// TestCreatePaste_InvalidOwnerTokenIsIgnored verifies that a stale/unknown
// owner token submitted at create time (e.g. a localStorage copy left over
// after the database was wiped) is silently discarded rather than trusted:
// the create still succeeds and a brand-new, different, valid token is
// generated and returned instead of the bogus one supplied.
func TestCreatePaste_InvalidOwnerTokenIsIgnored(t *testing.T) {
	h, _ := newTestHandler(t)

	bogusToken := "tok_stalefromwipeddbstalefromwipeddbst"

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pastes",
		strings.NewReader(`{"content":"stale token create"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Authorization", "Bearer "+bogusToken)

	rr := httptest.NewRecorder()
	h.CreatePaste(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201\nbody: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatal("data field missing or wrong type")
	}
	id, _ := data["id"].(string)
	if id == "" {
		t.Fatal("id missing or empty in response data")
	}
	newToken, ok := data["owner_token"].(string)
	if !ok || newToken == "" {
		t.Fatal("owner_token missing or empty in response data")
	}
	if newToken == bogusToken {
		t.Fatal("owner_token: server echoed back the invalid submitted token instead of minting a fresh one")
	}

	// Round-trip proof: the freshly-minted token must actually work for delete;
	// the bogus one must not.
	badReq := httptest.NewRequest(http.MethodDelete, "/api/v1/pastes/"+id, nil)
	badReq.Header.Set("Authorization", "Bearer "+bogusToken)
	badReq = withID(badReq, id)
	badRR := httptest.NewRecorder()
	h.DeletePaste(badRR, badReq)
	if badRR.Code != http.StatusNotFound {
		t.Fatalf("delete with bogus token: got status %d, want 404", badRR.Code)
	}

	goodReq := httptest.NewRequest(http.MethodDelete, "/api/v1/pastes/"+id, nil)
	goodReq.Header.Set("Authorization", "Bearer "+newToken)
	goodReq = withID(goodReq, id)
	goodRR := httptest.NewRecorder()
	h.DeletePaste(goodRR, goodReq)
	if goodRR.Code != http.StatusOK {
		t.Fatalf("delete with new token: got status %d, want 200\nbody: %s", goodRR.Code, goodRR.Body.String())
	}
}

// ─── GetPaste ─────────────────────────────────────────────────────────────────

// TestGetPaste creates a paste via API, then retrieves it by ID.
func TestGetPaste(t *testing.T) {
	h, _ := newTestHandler(t)

	m := createViaAPI(t, h, `{"content":"test content"}`)
	data := m["data"].(map[string]interface{})
	id := data["id"].(string)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pastes/"+id, nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req = withID(req, id)

	rr := httptest.NewRecorder()
	h.GetPaste(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200\nbody: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["ok"] != true {
		t.Errorf("ok: got %v", resp["ok"])
	}
}

// TestGetPaste_NotFound expects 404 for an unknown ID.
func TestGetPaste_NotFound(t *testing.T) {
	h, _ := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pastes/badid00x", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req = withID(req, "badid00x")

	rr := httptest.NewRecorder()
	h.GetPaste(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", rr.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["ok"] != false {
		t.Errorf("ok: got %v, want false", resp["ok"])
	}
}

// TestGetRawPaste creates a paste and verifies the raw endpoint returns plain text.
func TestGetRawPaste(t *testing.T) {
	h, _ := newTestHandler(t)

	m := createViaAPI(t, h, `{"content":"raw hello"}`)
	data := m["data"].(map[string]interface{})
	id := data["id"].(string)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pastes/"+id+"/raw", nil)
	req = withID(req, id)

	rr := httptest.NewRecorder()
	h.GetRawPaste(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type: got %q, want text/plain", ct)
	}
	body := rr.Body.String()
	if body != "raw hello" {
		t.Errorf("body: got %q, want %q", body, "raw hello")
	}
}

// TestGetRawPaste_ActiveContentForcesDownload verifies that a paste whose
// content_type would render as active content in a browser is served with
// Content-Disposition: attachment, even when the stored media type is
// disguised with trailing whitespace or non-canonical casing (which the
// allow-list check must not let bypass the download-forcing).
func TestGetRawPaste_ActiveContentForcesDownload(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
	}{
		{"canonical", "text/html"},
		{"trailing space", "text/html "},
		{"with params", "text/html; charset=utf-8"},
		{"svg", "image/svg+xml"},
		{"xhtml", "application/xhtml+xml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newTestHandler(t)

			body := `{"content":"PGh0bWw+PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0PjwvaHRtbD4=","content_type":"` + tc.contentType + `"}`
			m := createViaAPI(t, h, body)
			data, ok := m["data"].(map[string]interface{})
			if !ok {
				t.Fatalf("data field missing: %v", m)
			}
			id := data["id"].(string)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/pastes/"+id+"/raw", nil)
			req = withID(req, id)
			rr := httptest.NewRecorder()
			h.GetRawPaste(rr, req)

			if cd := rr.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
				t.Errorf("Content-Disposition: got %q, want attachment (active content must not render inline)", cd)
			}
		})
	}
}

// ─── is_link (URL shortening, auto-detected from content) ─────────────────────

// TestCreateLink_JSON verifies a paste whose entire content is a single
// http(s) URL is auto-detected as a link (is_link=true) with language
// cleared, with no is_link field sent in the request.
func TestCreateLink_JSON(t *testing.T) {
	h, _ := newTestHandler(t)

	m := createViaAPI(t, h, `{"content":"https://example.com/target"}`)
	data, ok := m["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("data field missing or wrong type: %v", m)
	}
	if data["is_link"] != true {
		t.Errorf("is_link: got %v, want true", data["is_link"])
	}
	if lang, _ := data["language"].(string); lang != "" {
		t.Errorf("language: got %q, want empty for a link", lang)
	}
}

// TestCreateLink_TitleDefaultsToURL verifies a link paste with no explicit
// title gets the target URL as its title, not the generic "Untitled"
// fallback used by regular pastes.
func TestCreateLink_TitleDefaultsToURL(t *testing.T) {
	h, _ := newTestHandler(t)

	target := "https://example.com/target"
	m := createViaAPI(t, h, fmt.Sprintf(`{"content":%q}`, target))
	data, ok := m["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("data field missing or wrong type: %v", m)
	}
	if title, _ := data["title"].(string); title != target {
		t.Errorf("title: got %q, want %q", title, target)
	}
}

// TestCreateLink_ExplicitTitleKept verifies an explicitly supplied title on a
// link paste is not overridden by the URL-default behavior.
func TestCreateLink_ExplicitTitleKept(t *testing.T) {
	h, _ := newTestHandler(t)

	target := "https://example.com/target"
	m := createViaAPI(t, h, fmt.Sprintf(`{"content":%q,"title":"My Link"}`, target))
	data, ok := m["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("data field missing or wrong type: %v", m)
	}
	if title, _ := data["title"].(string); title != "My Link" {
		t.Errorf("title: got %q, want %q", title, "My Link")
	}
}

// TestCreate_NonSingleURLContentIsNotLink verifies content that is not
// exactly one absolute http(s) URL is stored as a normal text paste
// (is_link=false) rather than being rejected — there is no explicit is_link
// flag to reject against anymore, so anything not matching the auto-detect
// shape simply falls through to plain-text storage.
func TestCreate_NonSingleURLContentIsNotLink(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"javascript scheme", `javascript:alert(1)`},
		{"ftp scheme", `ftp://example.com/file`},
		{"relative path", `/just/a/path`},
		{"plain text", `not a url at all`},
		{"url with surrounding text", `see https://example.com/target for details`},
		{"url with trailing newline", "https://example.com/target\nextra"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newTestHandler(t)
			m := createViaAPI(t, h, fmt.Sprintf(`{"content":%q}`, tc.content))
			data, ok := m["data"].(map[string]interface{})
			if !ok {
				t.Fatalf("data field missing or wrong type: %v", m)
			}
			if data["is_link"] != false {
				t.Errorf("is_link: got %v, want false", data["is_link"])
			}
		})
	}
}

// TestCreate_EmptyContentRejected verifies empty content is still rejected
// with 400, regardless of link auto-detection.
func TestCreate_EmptyContentRejected(t *testing.T) {
	h, _ := newTestHandler(t)
	body, _ := json.Marshal(map[string]interface{}{"content": ""})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pastes",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	rr := httptest.NewRecorder()
	h.CreatePaste(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400\nbody: %s", rr.Code, rr.Body.String())
	}
}

// TestGetLink_Redirects verifies GET /api/{v}/pastes/{id} issues a 302 to the
// target URL for an auto-detected link paste, instead of returning JSON.
func TestGetLink_Redirects(t *testing.T) {
	h, _ := newTestHandler(t)

	target := "https://example.com/redirect-target"
	m := createViaAPI(t, h, fmt.Sprintf(`{"content":%q}`, target))
	data := m["data"].(map[string]interface{})
	id := data["id"].(string)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pastes/"+id, nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req = withID(req, id)

	rr := httptest.NewRecorder()
	h.GetPaste(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("status: got %d, want 302\nbody: %s", rr.Code, rr.Body.String())
	}
	if loc := rr.Header().Get("Location"); loc != target {
		t.Errorf("Location: got %q, want %q", loc, target)
	}
}

// TestGetRawLink_NoRedirect verifies the raw endpoint returns the target URL
// as plain text with no redirect, for parity with other raw endpoints.
func TestGetRawLink_NoRedirect(t *testing.T) {
	h, _ := newTestHandler(t)

	target := "https://example.com/raw-target"
	m := createViaAPI(t, h, fmt.Sprintf(`{"content":%q}`, target))
	data := m["data"].(map[string]interface{})
	id := data["id"].(string)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pastes/"+id+"/raw", nil)
	req = withID(req, id)

	rr := httptest.NewRecorder()
	h.GetRawPaste(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (no redirect)\nbody: %s", rr.Code, rr.Body.String())
	}
	if body := rr.Body.String(); body != target {
		t.Errorf("body: got %q, want %q", body, target)
	}
}

// ─── DeletePaste ──────────────────────────────────────────────────────────────

// TestDeletePaste creates a paste, extracts its delete token, and deletes it.
func TestDeletePaste(t *testing.T) {
	h, _ := newTestHandler(t)

	m := createViaAPI(t, h, `{"content":"to be deleted"}`)
	data := m["data"].(map[string]interface{})
	id := data["id"].(string)
	token := data["owner_token"].(string)

	req := httptest.NewRequest(http.MethodDelete,
		fmt.Sprintf("/api/v1/pastes/%s", id), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req = withID(req, id)

	rr := httptest.NewRecorder()
	h.DeletePaste(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200\nbody: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["ok"] != true {
		t.Errorf("ok: got %v, want true", resp["ok"])
	}
}

// TestDeletePaste_WrongToken expects 404 when the token is incorrect.
func TestDeletePaste_WrongToken(t *testing.T) {
	h, _ := newTestHandler(t)

	m := createViaAPI(t, h, `{"content":"delete me wrong"}`)
	data := m["data"].(map[string]interface{})
	id := data["id"].(string)

	req := httptest.NewRequest(http.MethodDelete,
		fmt.Sprintf("/api/v1/pastes/%s", id), nil)
	req.Header.Set("Authorization", "Bearer tok_wrongtokenwrongtokenwrongtokenwx")
	req = withID(req, id)

	rr := httptest.NewRecorder()
	h.DeletePaste(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", rr.Code)
	}
}

// TestDeletePaste_NoToken expects 400 when no token is supplied.
func TestDeletePaste_NoToken(t *testing.T) {
	h, _ := newTestHandler(t)

	m := createViaAPI(t, h, `{"content":"delete me no token"}`)
	data := m["data"].(map[string]interface{})
	id := data["id"].(string)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/pastes/"+id, nil)
	req = withID(req, id)

	rr := httptest.NewRecorder()
	h.DeletePaste(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", rr.Code)
	}
}

// ─── Cache wiring (PART 9) ─────────────────────────────────────────────────────

// pasteCacheTestKey mirrors the un-prefixed "paste:{id}" key format documented
// on the unexported pasteCacheKey helper in paste.go.
func pasteCacheTestKey(id string) string {
	return "paste:" + id
}

// TestPasteCache_HitAvoidsDB verifies that once a paste is cached, a read
// still succeeds even after the underlying DB row is removed directly —
// proving the read was served from cache, not the database.
func TestPasteCache_HitAvoidsDB(t *testing.T) {
	h, db := newTestHandler(t)
	c, err := cache.New(cache.DefaultConfig())
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	h.SetCache(c)

	m := createViaAPI(t, h, `{"content":"cache hit test"}`)
	data := m["data"].(map[string]interface{})
	id := data["id"].(string)

	// First read populates the cache from the DB.
	if _, err := h.GetPasteForWeb(id); err != nil {
		t.Fatalf("first GetPasteForWeb: %v", err)
	}

	// Remove the DB row directly, bypassing the handler/cache entirely.
	if err := db.DeletePaste(id); err != nil {
		t.Fatalf("db.DeletePaste: %v", err)
	}

	// A second read must still succeed — it can only come from cache now.
	paste, err := h.GetPasteForWeb(id)
	if err != nil {
		t.Fatalf("second GetPasteForWeb should hit cache, got error: %v", err)
	}
	if paste.Content != "cache hit test" {
		t.Errorf("content: got %q, want %q", paste.Content, "cache hit test")
	}
}

// TestPasteCache_PopulatesOnDBRead verifies a DB-backed read populates the
// cache entry for subsequent reads.
func TestPasteCache_PopulatesOnDBRead(t *testing.T) {
	h, _ := newTestHandler(t)
	c, err := cache.New(cache.DefaultConfig())
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	h.SetCache(c)

	m := createViaAPI(t, h, `{"content":"populate test"}`)
	data := m["data"].(map[string]interface{})
	id := data["id"].(string)

	if _, err := h.GetPasteForWeb(id); err != nil {
		t.Fatalf("GetPasteForWeb: %v", err)
	}

	raw, err := c.Get(context.Background(), pasteCacheTestKey(id))
	if err != nil {
		t.Fatalf("expected cache entry after DB read, got error: %v", err)
	}
	if !strings.Contains(raw, "populate test") {
		t.Errorf("cached value missing content: %q", raw)
	}
}

// TestPasteCache_InvalidatedOnDelete verifies DeletePaste removes the cache
// entry alongside the DB row.
func TestPasteCache_InvalidatedOnDelete(t *testing.T) {
	h, _ := newTestHandler(t)
	c, err := cache.New(cache.DefaultConfig())
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	h.SetCache(c)

	m := createViaAPI(t, h, `{"content":"invalidate on delete"}`)
	data := m["data"].(map[string]interface{})
	id := data["id"].(string)
	token := data["owner_token"].(string)

	// Populate the cache.
	if _, err := h.GetPasteForWeb(id); err != nil {
		t.Fatalf("GetPasteForWeb: %v", err)
	}
	if _, err := c.Get(context.Background(), pasteCacheTestKey(id)); err != nil {
		t.Fatalf("expected cache entry before delete, got error: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/pastes/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req = withID(req, id)
	rr := httptest.NewRecorder()
	h.DeletePaste(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("DeletePaste status: got %d, want 200\nbody: %s", rr.Code, rr.Body.String())
	}

	if _, err := c.Get(context.Background(), pasteCacheTestKey(id)); err == nil {
		t.Error("expected cache miss after DeletePaste, got a hit")
	}
}

// TestPasteCache_BurnAfterNeverCached verifies a burn-after-read paste is
// never written to the cache, keeping burn-threshold enforcement DB-authoritative.
func TestPasteCache_BurnAfterNeverCached(t *testing.T) {
	h, _ := newTestHandler(t)
	c, err := cache.New(cache.DefaultConfig())
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	h.SetCache(c)

	m := createViaAPI(t, h, `{"content":"burn me","burn_after":5}`)
	data := m["data"].(map[string]interface{})
	id := data["id"].(string)

	if _, err := h.GetPasteForWeb(id); err != nil {
		t.Fatalf("GetPasteForWeb: %v", err)
	}

	if _, err := c.Get(context.Background(), pasteCacheTestKey(id)); err == nil {
		t.Error("burn-limited paste must never be cached, but got a cache hit")
	}
}

// ─── ParseExpiry unit tests ────────────────────────────────────────────────────

// TestParseExpiry exercises the ParseExpiry helper directly.
func TestParseExpiry(t *testing.T) {
	cases := []struct {
		input   string
		wantNil bool
		// When not nil, the result must be within this duration of now.
		withinHi time.Duration
	}{
		{"1h", false, 2 * time.Hour},
		{"1d", false, 25 * time.Hour},
		{"1w", false, 8 * 24 * time.Hour},
		{"1m", false, 31 * 24 * time.Hour},
		{"3m", false, 91 * 24 * time.Hour},
		{"6m", false, 181 * 24 * time.Hour},
		{"18m", false, 541 * 24 * time.Hour},
		{"1y", false, 366 * 24 * time.Hour},
		{"2y", false, 731 * 24 * time.Hour},
		{"never", true, 0},
		// raw seconds = 1 hour
		{"3600", false, 2 * time.Hour},
		{"bad", true, 0},
		// non-positive seconds → nil
		{"0", true, 0},
		{"", true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got := handler.ParseExpiry(tc.input)
			if tc.wantNil {
				if got != nil {
					t.Errorf("ParseExpiry(%q): got %v, want nil", tc.input, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("ParseExpiry(%q): got nil, want non-nil", tc.input)
			}
			now := time.Now()
			if got.Before(now) {
				t.Errorf("ParseExpiry(%q): result %v is in the past", tc.input, got)
			}
			if got.After(now.Add(tc.withinHi)) {
				t.Errorf("ParseExpiry(%q): result %v is too far in the future (> %v)", tc.input, got, tc.withinHi)
			}
		})
	}
}

// ─── DetectLanguage unit tests ────────────────────────────────────────────────

// TestDetectLanguage verifies well-known filename→language mappings and the fallback.
func TestDetectLanguage(t *testing.T) {
	cases := []struct {
		filename string
		want     string
	}{
		{"main.go", "go"},
		{"index.js", "javascript"},
		{"app.ts", "typescript"},
		{"script.py", "python"},
		{"style.css", "css"},
		{"README.md", "markdown"},
		{"data.json", "json"},
		{"config.yml", "yaml"},
		{"config.yaml", "yaml"},
		// no dot → whole lowercase name is the key
		{"Dockerfile", "dockerfile"},
		{"unknown.xyz", "text"},
		{"noextension", "text"},
	}
	for _, tc := range cases {
		t.Run(tc.filename, func(t *testing.T) {
			got := handler.DetectLanguage(tc.filename)
			if got != tc.want {
				t.Errorf("DetectLanguage(%q): got %q, want %q", tc.filename, got, tc.want)
			}
		})
	}
}

// ─── ValidateLinkTarget unit tests ─────────────────────────────────────────────

// TestValidateLinkTarget verifies only absolute http/https URLs with a host
// are accepted as link-paste redirect targets.
func TestValidateLinkTarget(t *testing.T) {
	cases := []struct {
		target string
		want   bool
	}{
		{"https://example.com/path", true},
		{"http://example.com", true},
		{"https://example.com:8443/path?q=1#frag", true},
		{"javascript:alert(1)", false},
		{"ftp://example.com/file", false},
		{"/relative/path", false},
		{"example.com", false},
		{"", false},
		{"   ", false},
		{"file:///etc/passwd", false},
	}
	for _, tc := range cases {
		t.Run(tc.target, func(t *testing.T) {
			got := handler.ValidateLinkTarget(tc.target)
			if got != tc.want {
				t.Errorf("ValidateLinkTarget(%q): got %v, want %v", tc.target, got, tc.want)
			}
		})
	}
}

// ─── HashToken unit tests ─────────────────────────────────────────────────────

// TestHashToken verifies determinism and that distinct inputs produce distinct hashes.
func TestHashToken(t *testing.T) {
	// Same input → same output (determinism).
	h1 := handler.HashToken("mytoken")
	h2 := handler.HashToken("mytoken")
	if h1 != h2 {
		t.Errorf("HashToken is not deterministic: %q != %q", h1, h2)
	}

	// Different inputs → different hashes.
	h3 := handler.HashToken("other")
	if h1 == h3 {
		t.Error("HashToken: different inputs produced the same hash")
	}

	// Verify against the expected SHA-256 of "mytoken".
	sum := sha256.Sum256([]byte("mytoken"))
	expected := hex.EncodeToString(sum[:])
	if h1 != expected {
		t.Errorf("HashToken: got %q, want %q", h1, expected)
	}

	// Empty string does not panic and produces a stable hash.
	he := handler.HashToken("")
	if he == "" {
		t.Error("HashToken of empty string should not be empty")
	}
	// silence unused import warning if any
	_ = bytes.NewBufferString(he)
}

// TestCreatePasteEmptyContentIsValidationFailed verifies an empty create is
// reported as VALIDATION_FAILED naming the content field (PART 14).
func TestCreatePasteEmptyContentIsValidationFailed(t *testing.T) {
	h, _ := newTestHandler(t)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pastes", strings.NewReader(`{"content":""}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	h.CreatePaste(rec, r)
	var body map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusBadRequest || body["error"] != "VALIDATION_FAILED" {
		t.Fatalf("status=%d body=%v, want 400 VALIDATION_FAILED", rec.Code, body)
	}
	if d, _ := body["details"].(map[string]interface{}); d["field"] != "content" || d["rule"] != "required" {
		t.Errorf("details = %v", body["details"])
	}
}
