package compat

// Tests for Handler — pastebin.com, microbin, and lenpaste compat routes.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/database"
	"github.com/apimgr/cairnbin/src/handler"
	"github.com/go-chi/chi/v5"
)

// ─── Test helpers ─────────────────────────────────────────────────────────────

func newCompatTestDB(t *testing.T) database.DB {
	t.Helper()
	base := filepath.Join(os.TempDir(), "apimgr")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "compat-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	db, err := database.NewDatabase("sqlite", filepath.Join(dir, "compat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newCompatHandler(t *testing.T) (*Handler, database.DB) {
	t.Helper()
	db := newCompatTestDB(t)
	ph := handler.NewPasteHandler(db, "", [32]byte{})
	ph.SetTokenEncryptionKey([]byte(strings.Repeat("x", 32)))
	return New(ph, db, "test-version"), db
}

// withID injects a chi URL parameter named "id" into the request context.
func withID(r *http.Request, id string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// seedPaste creates a paste via the internal helper and returns its ID and raw
// delete token.
func seedPaste(t *testing.T, ph *handler.PasteHandler, content string) (id, token string) {
	t.Helper()
	var err error
	id, token, err = ph.CreatePasteInternal("test", content, "text", 0, 0, nil)
	if err != nil {
		t.Fatalf("seedPaste: %v", err)
	}
	return id, token
}

// ─── parsePastebinExpiry ──────────────────────────────────────────────────────

func TestParsePastebinExpiry(t *testing.T) {
	cases := []struct {
		code     string
		wantNil  bool
		minDelta time.Duration
		maxDelta time.Duration
	}{
		{"10M", false, 9*time.Minute + 59*time.Second, 10*time.Minute + 1*time.Second},
		{"1H", false, time.Hour - time.Minute, time.Hour + time.Minute},
		{"1D", false, 24*time.Hour - time.Minute, 24*time.Hour + time.Minute},
		{"1W", false, 7*24*time.Hour - time.Minute, 7*24*time.Hour + time.Minute},
		{"2W", false, 14*24*time.Hour - time.Minute, 14*24*time.Hour + time.Minute},
		{"1M", false, 30*24*time.Hour - time.Minute, 30*24*time.Hour + time.Minute},
		{"6M", false, 180*24*time.Hour - time.Minute, 180*24*time.Hour + time.Minute},
		{"1Y", false, 365*24*time.Hour - time.Minute, 365*24*time.Hour + time.Minute},
		{"N", true, 0, 0},
		{"", true, 0, 0},
		{"unknown", true, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			now := time.Now()
			got := parsePastebinExpiry(tc.code)

			if tc.wantNil {
				if got != nil {
					t.Errorf("parsePastebinExpiry(%q): got %v, want nil", tc.code, got)
				}
				return
			}

			if got == nil {
				t.Fatalf("parsePastebinExpiry(%q): got nil, want non-nil", tc.code)
			}

			delta := got.Sub(now)
			if delta < tc.minDelta {
				t.Errorf("parsePastebinExpiry(%q): delta %v < min %v", tc.code, delta, tc.minDelta)
			}
			if delta > tc.maxDelta {
				t.Errorf("parsePastebinExpiry(%q): delta %v > max %v", tc.code, delta, tc.maxDelta)
			}
		})
	}
}

// ─── xmlEscape ────────────────────────────────────────────────────────────────

func TestXMLEscape(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"&", "&amp;"},
		{"<", "&lt;"},
		{">", "&gt;"},
		{`"`, "&quot;"},
		{"'", "&#39;"},
		{"hello", "hello"},
		{"<script>&", "&lt;script&gt;&amp;"},
		{"a<b>c&d\"e'f", "a&lt;b&gt;c&amp;d&quot;e&#39;f"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got := xmlEscape(tc.input)
			if got != tc.want {
				t.Errorf("xmlEscape(%q): got %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// ─── PastebinPost: create ─────────────────────────────────────────────────────

func TestPastebinPost_CreatePaste(t *testing.T) {
	ch, _ := newCompatHandler(t)

	form := url.Values{
		"api_option":     {"paste"},
		"api_paste_code": {"hello world"},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/api_post.php",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	ch.PastebinPost(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200\nbody: %s", rr.Code, rr.Body.String())
	}
	body := strings.TrimSpace(rr.Body.String())
	if body == "" {
		t.Fatal("expected a URL in body, got empty string")
	}
	// Response must look like a URL or at least contain a non-empty string.
	if strings.Contains(body, "Bad API") {
		t.Errorf("unexpected error in body: %s", body)
	}
}

// ─── PastebinPost: empty content ──────────────────────────────────────────────

func TestPastebinPost_EmptyContent(t *testing.T) {
	ch, _ := newCompatHandler(t)

	form := url.Values{
		"api_option":     {"paste"},
		"api_paste_code": {""},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/api_post.php",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	ch.PastebinPost(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rr.Code)
	}
}

// ─── PastebinPost: list ───────────────────────────────────────────────────────

func TestPastebinPost_List(t *testing.T) {
	ch, _ := newCompatHandler(t)

	form := url.Values{"api_option": {"list"}}
	req := httptest.NewRequest(http.MethodPost, "/api/api_post.php",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	ch.PastebinPost(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200\nbody: %s", rr.Code, rr.Body.String())
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/xml") {
		t.Errorf("Content-Type: got %q, want text/xml", ct)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "<pastes>") {
		t.Errorf("body missing <pastes> tag: %s", body)
	}
}

// ─── PastebinPost: delete — no ID ─────────────────────────────────────────────

func TestPastebinPost_Delete_NoID(t *testing.T) {
	ch, _ := newCompatHandler(t)

	form := url.Values{
		"api_option":   {"delete"},
		"api_user_key": {"sometoken"},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/api_post.php",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	ch.PastebinPost(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rr.Code)
	}
}

// ─── PastebinPost: delete — no token ─────────────────────────────────────────

func TestPastebinPost_Delete_NoToken(t *testing.T) {
	ch, _ := newCompatHandler(t)

	form := url.Values{
		"api_option":    {"delete"},
		"api_paste_key": {"somepasteid"},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/api_post.php",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	ch.PastebinPost(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status: got %d, want 403", rr.Code)
	}
}

// ─── PastebinPost: userdetails ────────────────────────────────────────────────

func TestPastebinPost_UserDetails(t *testing.T) {
	ch, _ := newCompatHandler(t)

	form := url.Values{"api_option": {"userdetails"}}
	req := httptest.NewRequest(http.MethodPost, "/api/api_post.php",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	ch.PastebinPost(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200\nbody: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "<user>") {
		t.Errorf("body missing <user> tag: %s", body)
	}
}

// ─── PastebinRaw ──────────────────────────────────────────────────────────────

func TestPastebinRaw_NoID(t *testing.T) {
	ch, _ := newCompatHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/api_raw.php", nil)

	rr := httptest.NewRecorder()
	ch.PastebinRaw(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rr.Code)
	}
}

func TestPastebinRaw_NotFound(t *testing.T) {
	ch, _ := newCompatHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/api_raw.php?i=doesnotexist", nil)

	rr := httptest.NewRecorder()
	ch.PastebinRaw(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", rr.Code)
	}
}

func TestPastebinRaw_Found(t *testing.T) {
	ch, db := newCompatHandler(t)
	_ = db

	id, _ := seedPaste(t, ch.ph, "raw compat content")

	req := httptest.NewRequest(http.MethodGet, "/api/api_raw.php?i="+id, nil)

	rr := httptest.NewRecorder()
	ch.PastebinRaw(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200\nbody: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if body != "raw compat content" {
		t.Errorf("body: got %q, want %q", body, "raw compat content")
	}
}

// ─── PastebinLogin ────────────────────────────────────────────────────────────

func TestPastebinLogin(t *testing.T) {
	ch, _ := newCompatHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/api_login.php", nil)

	rr := httptest.NewRecorder()
	ch.PastebinLogin(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}
	body := strings.TrimSpace(rr.Body.String())
	if body != "ANONYMOUS" {
		t.Errorf("body: got %q, want %q", body, "ANONYMOUS")
	}
}

// ─── LenCreate ────────────────────────────────────────────────────────────────

func TestLenCreate_Success(t *testing.T) {
	ch, _ := newCompatHandler(t)

	form := url.Values{
		"title":  {"my title"},
		"body":   {"len paste content"},
		"syntax": {"go"},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/new",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	ch.LenCreate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200\nbody: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["id"] == "" {
		t.Error("id field missing in LenCreate response")
	}
	if resp["deleteToken"] == "" {
		t.Error("deleteToken field missing in LenCreate response")
	}
}

func TestLenCreate_EmptyBody(t *testing.T) {
	ch, _ := newCompatHandler(t)

	form := url.Values{"body": {""}}
	req := httptest.NewRequest(http.MethodPost, "/api/new",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	ch.LenCreate(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rr.Code)
	}
}

// ─── LenGet ───────────────────────────────────────────────────────────────────

func TestLenGet_NotFound(t *testing.T) {
	ch, _ := newCompatHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/get?id=notexist", nil)

	rr := httptest.NewRecorder()
	ch.LenGet(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", rr.Code)
	}
}

func TestLenGet_NoID(t *testing.T) {
	ch, _ := newCompatHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/get", nil)

	rr := httptest.NewRecorder()
	ch.LenGet(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rr.Code)
	}
}

func TestLenGet_Found(t *testing.T) {
	ch, _ := newCompatHandler(t)
	id, _ := seedPaste(t, ch.ph, "lenpaste body")

	req := httptest.NewRequest(http.MethodGet, "/api/get?id="+id, nil)

	rr := httptest.NewRecorder()
	ch.LenGet(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200\nbody: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["body"] != "lenpaste body" {
		t.Errorf("body field: got %v, want %q", resp["body"], "lenpaste body")
	}
}

// ─── LenList ──────────────────────────────────────────────────────────────────

func TestLenList(t *testing.T) {
	ch, _ := newCompatHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/list?page=1&pageSize=10", nil)

	rr := httptest.NewRecorder()
	ch.LenList(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200\nbody: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := resp["pastes"]; !ok {
		t.Error("pastes field missing in LenList response")
	}
}

// ─── LenRemove ────────────────────────────────────────────────────────────────

func TestLenRemove_Success(t *testing.T) {
	ch, _ := newCompatHandler(t)
	id, token := seedPaste(t, ch.ph, "to be len-removed")

	req := httptest.NewRequest(http.MethodDelete,
		"/api/remove?id="+id+"&deleteToken="+token, nil)

	rr := httptest.NewRecorder()
	ch.LenRemove(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200\nbody: %s", rr.Code, rr.Body.String())
	}
}

func TestLenRemove_MissingParams(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"no id", "/api/remove?deleteToken=tok"},
		{"no token", "/api/remove?id=someid"},
		{"neither", "/api/remove"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch, _ := newCompatHandler(t)
			req := httptest.NewRequest(http.MethodDelete, tc.url, nil)
			rr := httptest.NewRecorder()
			ch.LenRemove(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status: got %d, want 400", rr.Code)
			}
		})
	}
}

// ─── LenServerInfo ────────────────────────────────────────────────────────────

func TestLenServerInfo(t *testing.T) {
	ch, _ := newCompatHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/getServerInfo", nil)

	rr := httptest.NewRecorder()
	ch.LenServerInfo(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200\nbody: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["version"] != "test-version" {
		t.Errorf("version: got %v, want %q", resp["version"], "test-version")
	}
}

// ─── Microbin compatibility ───────────────────────────────────────────────────

func TestMicrobinCreate_Success(t *testing.T) {
	ch, _ := newCompatHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pasta", strings.NewReader(`{"content":"microbin paste content","title":"microbin title"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rr := httptest.NewRecorder()

	ch.MicrobinCreate(rr, req)

	if rr.Code != http.StatusCreated {
		t.Errorf("MicrobinCreate: got status %d, want 201\nbody: %s",
			rr.Code, rr.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["id"] == "" || got["url"] == "" || got["deleteToken"] == "" {
		t.Fatalf("missing compat response fields: %#v", got)
	}
	if got["ok"] != "" {
		t.Fatalf("compat response unexpectedly uses native envelope: %#v", got)
	}
}

func TestMicrobinList_Empty(t *testing.T) {
	ch, _ := newCompatHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pasta", nil)
	rr := httptest.NewRecorder()

	ch.MicrobinList(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("MicrobinList: got status %d, want 200\nbody: %s", rr.Code, rr.Body.String())
	}
}

func TestMicrobinDelete_UsesCompatibilityToken(t *testing.T) {
	ch, _ := newCompatHandler(t)

	// Seed a paste so the handler has something to delete.
	id, token := seedPaste(t, ch.ph, "to delete via microbin")

	req := httptest.NewRequest(http.MethodDelete,
		"/api/v1/pasta/"+id+"?deleteToken="+token, nil)
	req = withID(req, id)
	rr := httptest.NewRecorder()

	ch.MicrobinDelete(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("MicrobinDelete: got status %d, want 200\nbody: %s",
			rr.Code, rr.Body.String())
	}
	if paste, err := ch.db.GetPasteByID(id); err != nil || paste != nil {
		t.Fatalf("compat delete left paste in storage: paste=%v err=%v", paste, err)
	}
}

func TestMicrobinGet_Delegates(t *testing.T) {
	ch, _ := newCompatHandler(t)

	id, _ := seedPaste(t, ch.ph, "microbin get test")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pasta/"+id, nil)
	req = withID(req, id)
	rr := httptest.NewRecorder()

	ch.MicrobinGet(rr, req)

	if rr.Code != http.StatusOK || rr.Body.String() != "microbin get test" {
		t.Errorf("MicrobinGet: got status %d, want 200\nbody: %s", rr.Code, rr.Body.String())
	}
}

func TestMicrobinGetReturnsBinaryBytes(t *testing.T) {
	ch, _ := newCompatHandler(t)
	want := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F'}
	id, _, err := ch.ph.CreatePasteInternal("microbin.jpg", string(want), "", 0, 0, nil)
	if err != nil {
		t.Fatalf("CreatePasteInternal: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/pasta/"+id, nil)
	req = withID(req, id)
	rec := httptest.NewRecorder()
	ch.MicrobinGet(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("MicrobinGet status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Errorf("MicrobinGet returned %v, want raw binary bytes %v", rec.Body.Bytes(), want)
	}
}
