package compat

// Tests for the stikked, hastebin/haste-server, dpaste, and curl-upload
// (sprunge/0x0/ix.io) compatibility handlers.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// ─── stikked ────────────────────────────────────────────────────────────────

func TestStikkedCreate_Success(t *testing.T) {
	h, _ := newCompatHandler(t)

	form := url.Values{"text": {"hello stikked"}, "title": {"t"}, "lang": {"go"}, "expire": {"60"}, "private": {"1"}}
	req := httptest.NewRequest(http.MethodPost, "/api/create", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.StikkedCreate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "/view/") {
		t.Fatalf("body = %q, want a /view/ URL", rr.Body.String())
	}
}

func TestStikkedCreate_EmptyText(t *testing.T) {
	h, _ := newCompatHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/create", strings.NewReader("text="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.StikkedCreate(rr, req)

	if !strings.HasPrefix(rr.Body.String(), "Error:") {
		t.Fatalf("body = %q, want an Error: prefix", rr.Body.String())
	}
}

func TestStikkedJSON_FoundAndMissing(t *testing.T) {
	h, _ := newCompatHandler(t)
	id, _ := seedPaste(t, h.ph, "stikked json body")

	req := withID(httptest.NewRequest(http.MethodGet, "/api/paste/"+id, nil), id)
	rr := httptest.NewRecorder()
	h.StikkedJSON(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if got["raw"] != "stikked json body" || got["pid"] != id {
		t.Fatalf("unexpected payload: %v", got)
	}

	req = withID(httptest.NewRequest(http.MethodGet, "/api/paste/nope", nil), "nope")
	rr = httptest.NewRecorder()
	h.StikkedJSON(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d, want 404", rr.Code)
	}
}

// ─── hastebin / haste-server ──────────────────────────────────────────────────

func TestHastebinCreateAndGet(t *testing.T) {
	h, _ := newCompatHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/documents", strings.NewReader("raw haste content"))
	rr := httptest.NewRecorder()
	h.HastebinCreate(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("create status = %d, want 200", rr.Code)
	}
	var created map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("invalid create json: %v", err)
	}
	key := created["key"]
	if key == "" {
		t.Fatal("create returned empty key")
	}

	req = withID(httptest.NewRequest(http.MethodGet, "/documents/"+key, nil), key)
	rr = httptest.NewRecorder()
	h.HastebinGet(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200", rr.Code)
	}
	var fetched map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &fetched); err != nil {
		t.Fatalf("invalid get json: %v", err)
	}
	if fetched["data"] != "raw haste content" || fetched["key"] != key {
		t.Fatalf("unexpected get payload: %v", fetched)
	}
}

func TestHastebinCreate_Empty(t *testing.T) {
	h, _ := newCompatHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/documents", strings.NewReader("   "))
	rr := httptest.NewRecorder()
	h.HastebinCreate(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestHastebinGet_Missing(t *testing.T) {
	h, _ := newCompatHandler(t)
	req := withID(httptest.NewRequest(http.MethodGet, "/documents/nope", nil), "nope")
	rr := httptest.NewRecorder()
	h.HastebinGet(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

// ─── dpaste ───────────────────────────────────────────────────────────────────

func dpastePost(t *testing.T, h *Handler, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.DpasteCreate(rr, req)
	return rr
}

func TestDpasteCreate_Formats(t *testing.T) {
	h, _ := newCompatHandler(t)

	// default → quoted URL.
	rr := dpastePost(t, h, url.Values{"content": {"x"}, "expires": {"7"}})
	if rr.Code != http.StatusOK || !strings.HasPrefix(rr.Body.String(), `"`) {
		t.Fatalf("default format body = %q (status %d)", rr.Body.String(), rr.Code)
	}

	// url → bare URL.
	rr = dpastePost(t, h, url.Values{"content": {"x"}, "format": {"url"}})
	if strings.HasPrefix(rr.Body.String(), `"`) || !strings.Contains(rr.Body.String(), "http") {
		t.Fatalf("url format body = %q", rr.Body.String())
	}

	// json → object with url/content/lexer.
	rr = dpastePost(t, h, url.Values{"content": {"y"}, "lexer": {"go"}, "format": {"json"}})
	var got map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if got["content"] != "y" || got["lexer"] != "go" || got["url"] == "" {
		t.Fatalf("unexpected json payload: %v", got)
	}
}

func TestDpasteCreate_Empty(t *testing.T) {
	h, _ := newCompatHandler(t)
	rr := dpastePost(t, h, url.Values{"content": {""}})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// ─── curl-upload family ───────────────────────────────────────────────────────

func TestRootUpload_Sprunge(t *testing.T) {
	h, _ := newCompatHandler(t)
	form := url.Values{"sprunge": {"sprunge body"}}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.RootUpload(rr, req)

	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "/raw/") {
		t.Fatalf("sprunge body = %q (status %d), want a /raw/ URL", rr.Body.String(), rr.Code)
	}
}

func TestRootUpload_IxIo(t *testing.T) {
	h, _ := newCompatHandler(t)
	form := url.Values{"f:1": {"ixio body"}}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.RootUpload(rr, req)

	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "/raw/") {
		t.Fatalf("ix.io body = %q (status %d)", rr.Body.String(), rr.Code)
	}
}

func TestRootUpload_ZeroXMultipartFile(t *testing.T) {
	h, _ := newCompatHandler(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "paste.txt")
	if err != nil {
		t.Fatal(err)
	}
	fw.Write([]byte("0x0 file content"))
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	h.RootUpload(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("0x0 status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "/raw/") {
		t.Fatalf("0x0 body = %q, want a /raw/ URL", rr.Body.String())
	}
	if rr.Header().Get("X-Token") == "" {
		t.Fatal("0x0 response missing X-Token header")
	}
}

func TestRootUpload_ExceedsConfiguredMaxSize(t *testing.T) {
	h, _ := newCompatHandler(t)
	h.ph.SetMaxSize(8)

	form := url.Values{"sprunge": {"this body is way over eight bytes"}}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.RootUpload(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rr.Code)
	}
}

// ─── curl-upload family — subdomain-forced dispatch ────────────────────────────
//
// On tb./termbin./ix./sprunge./0x0./st. subdomains, the server's compatModeGate
// threads the resolved target into the request context via WithTarget; when no
// distinguishing form field is present, RootUpload must treat the whole body as
// that target's content instead of falling through to the native create handler.

func TestRootUpload_SubdomainForced_Termbin(t *testing.T) {
	h, _ := newCompatHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("termbin subdomain body"))
	req = WithTarget(req, "termbin")
	rr := httptest.NewRecorder()
	h.RootUpload(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "/raw/") {
		t.Fatalf("body = %q, termbin subdomain must return a bare id URL, not /raw/", rr.Body.String())
	}
}

// curl sends --data-binary bodies as application/x-www-form-urlencoded, which
// ParseForm would consume; the raw body must still reach the target.
func TestRootUpload_SubdomainForced_Termbin_URLEncodedRawBody(t *testing.T) {
	h, _ := newCompatHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("hello from curl --data-binary"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = WithTarget(req, "termbin")
	rr := httptest.NewRecorder()
	h.RootUpload(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %q, want 200", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "://") {
		t.Fatalf("body = %q, want a paste URL", rr.Body.String())
	}
}

func TestRootUpload_SubdomainForced_IxIo(t *testing.T) {
	h, _ := newCompatHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("ix.io subdomain body"))
	req = WithTarget(req, "ixio")
	rr := httptest.NewRecorder()
	h.RootUpload(rr, req)

	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "/raw/") {
		t.Fatalf("body = %q (status %d), want a /raw/ URL", rr.Body.String(), rr.Code)
	}
}

func TestRootUpload_SubdomainForced_Sprunge(t *testing.T) {
	h, _ := newCompatHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("sprunge subdomain body"))
	req = WithTarget(req, "sprunge")
	rr := httptest.NewRecorder()
	h.RootUpload(rr, req)

	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "/raw/") {
		t.Fatalf("body = %q (status %d), want a /raw/ URL", rr.Body.String(), rr.Code)
	}
}

func TestRootUpload_SubdomainForced_ZeroX0(t *testing.T) {
	h, _ := newCompatHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("0x0 subdomain body"))
	req = WithTarget(req, "zerox0")
	rr := httptest.NewRecorder()
	h.RootUpload(rr, req)

	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "/raw/") {
		t.Fatalf("body = %q (status %d), want a /raw/ URL", rr.Body.String(), rr.Code)
	}
}

func TestRootUpload_SubdomainForced_ExceedsConfiguredMaxSize(t *testing.T) {
	h, _ := newCompatHandler(t)
	h.ph.SetMaxSize(8)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("this body is way over eight bytes"))
	req = WithTarget(req, "sprunge")
	rr := httptest.NewRecorder()
	h.RootUpload(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rr.Code)
	}
}

func TestRootUpload_SubdomainForced_EmptyBodyFallsThroughToCreate(t *testing.T) {
	h, _ := newCompatHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req = WithTarget(req, "termbin")
	rr := httptest.NewRecorder()
	h.RootUpload(rr, req)

	// termbinHTTPRespond rejects empty content with 400 rather than falling
	// through — the target dispatch always wins once a subdomain is set.
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (empty termbin subdomain body)", rr.Code)
	}
}

// ─── termbinHTTPRespond (tb./termbin. subdomain HTTP alias) ───────────────────

func TestTermbinHTTPRespond_Success(t *testing.T) {
	h, _ := newCompatHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rr := httptest.NewRecorder()
	h.termbinHTTPRespond(rr, req, "termbin http content")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := strings.TrimSpace(rr.Body.String())
	if strings.Contains(body, "/raw/") {
		t.Fatalf("body = %q, want a bare id URL (no /raw/)", body)
	}
	if !strings.HasPrefix(body, "http") {
		t.Fatalf("body = %q, want a URL", body)
	}
}

func TestTermbinHTTPRespond_EmptyContent(t *testing.T) {
	h, _ := newCompatHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rr := httptest.NewRecorder()
	h.termbinHTTPRespond(rr, req, "   \n")

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// ─── compat create endpoints honor paste.max_size ──────────────────────────────
//
// createFromRequest (the native create path) enforces server.yml's
// paste.max_size via SetMaxSize/maxSize(); these compat routes must
// enforce the exact same configured limit rather than falling back to Go's
// stdlib ParseForm default, so a small (or large) operator-configured limit
// applies uniformly across every third-party-compatible API surface.

func TestPastebinPost_ExceedsConfiguredMaxSize(t *testing.T) {
	ch, _ := newCompatHandler(t)
	ch.ph.SetMaxSize(8)

	form := url.Values{
		"api_option":     {"paste"},
		"api_paste_code": {"this content is definitely over eight bytes"},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/api_post.php",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	ch.PastebinPost(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413\nbody: %s", rr.Code, rr.Body.String())
	}
}

func TestLenCreate_ExceedsConfiguredMaxSize(t *testing.T) {
	h, _ := newCompatHandler(t)
	h.ph.SetMaxSize(8)

	form := url.Values{"body": {"this content is definitely over eight bytes"}}
	req := httptest.NewRequest(http.MethodPost, "/api/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.LenCreate(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413\nbody: %s", rr.Code, rr.Body.String())
	}
}

func TestStikkedCreate_ExceedsConfiguredMaxSize(t *testing.T) {
	h, _ := newCompatHandler(t)
	h.ph.SetMaxSize(8)

	form := url.Values{"text": {"this content is definitely over eight bytes"}}
	req := httptest.NewRequest(http.MethodPost, "/api/create", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.StikkedCreate(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413\nbody: %s", rr.Code, rr.Body.String())
	}
}

func TestDpasteCreate_ExceedsConfiguredMaxSize(t *testing.T) {
	h, _ := newCompatHandler(t)
	h.ph.SetMaxSize(8)

	form := url.Values{"content": {"this content is definitely over eight bytes"}}
	req := httptest.NewRequest(http.MethodPost, "/api/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.DpasteCreate(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413\nbody: %s", rr.Code, rr.Body.String())
	}
}

// ─── termbin / fiche (raw TCP) ────────────────────────────────────────────────

// termbinRoundTrip drives TermbinServe over an in-memory socket pair: it writes
// content, half-closes, and returns the server's response line.
func termbinRoundTrip(t *testing.T, h *Handler, content string, maxSize int64) string {
	t.Helper()
	srvConn, cliConn := net.Pipe()
	// net.Pipe has no half-close, so the server's read ends on the deadline; a
	// short timeout keeps the test fast.
	go h.TermbinServe(srvConn, "http://paste.example", maxSize, 250*time.Millisecond)

	_ = cliConn.SetDeadline(time.Now().Add(3 * time.Second))
	// Write in a goroutine: when the server reads less than the full input
	// (over-limit case) the remaining write blocks until the server closes, so
	// the write error here is expected and ignored.
	go func() {
		_, _ = cliConn.Write([]byte(content))
	}()
	resp, _ := io.ReadAll(cliConn)
	cliConn.Close()
	return string(resp)
}

func TestTermbinServe_Success(t *testing.T) {
	h, _ := newCompatHandler(t)
	resp := termbinRoundTrip(t, h, "termbin content\n", 32768)
	if !strings.HasPrefix(resp, "http://paste.example/") {
		t.Fatalf("response = %q, want a base URL line", resp)
	}
	if !strings.HasSuffix(resp, "\n") {
		t.Fatalf("response = %q, want trailing newline", resp)
	}
}

func TestTermbinServe_Empty(t *testing.T) {
	h, _ := newCompatHandler(t)
	resp := termbinRoundTrip(t, h, "   \n", 32768)
	if !strings.HasPrefix(resp, "Error:") {
		t.Fatalf("response = %q, want an Error: line", resp)
	}
}

func TestTermbinServe_TooLarge(t *testing.T) {
	h, _ := newCompatHandler(t)
	resp := termbinRoundTrip(t, h, strings.Repeat("x", 64), 16)
	if !strings.Contains(resp, "too large") {
		t.Fatalf("response = %q, want a too-large error", resp)
	}
}

func TestRootUpload_FallthroughToCreate(t *testing.T) {
	h, _ := newCompatHandler(t)

	// No curl-upload field present → delegate to the native create handler.
	form := url.Values{"content": {"native create"}}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.RootUpload(rr, req)

	// The native create handler responds (redirect or success), never a 404.
	if rr.Code == http.StatusNotFound {
		t.Fatalf("fallthrough status = %d, native create did not run", rr.Code)
	}
}

// ─── CreatePasteInternal — binary content ────────────────────────────────────

// TestCreatePasteInternal_BinaryStoredBase64 verifies the compat/termbin
// creation path detects binary content, stores it base64-encoded, and sets
// ContentType so viewers render it correctly.
func TestCreatePasteInternal_BinaryStoredBase64(t *testing.T) {
	h, db := newCompatHandler(t)

	raw := string([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x01})
	id, _, err := h.ph.CreatePasteInternal("img", raw, "text", 0, 0, nil)
	if err != nil {
		t.Fatalf("CreatePasteInternal: %v", err)
	}

	paste, err := db.GetPasteByID(id)
	if err != nil || paste == nil {
		t.Fatalf("GetPasteByID(%q): %v", id, err)
	}
	if paste.ContentType != "image/png" {
		t.Errorf("ContentType: got %q, want image/png", paste.ContentType)
	}
	decoded, err := base64.StdEncoding.DecodeString(paste.Content)
	if err != nil {
		t.Fatalf("stored content is not base64: %v", err)
	}
	if string(decoded) != raw {
		t.Errorf("decoded content does not match original bytes")
	}
}

// TestCreatePasteInternal_TextKeepsEmptyContentType verifies plain text is
// stored unmodified with an empty ContentType.
func TestCreatePasteInternal_TextKeepsEmptyContentType(t *testing.T) {
	h, db := newCompatHandler(t)

	id, _, err := h.ph.CreatePasteInternal("t", "plain text content", "text", 0, 0, nil)
	if err != nil {
		t.Fatalf("CreatePasteInternal: %v", err)
	}

	paste, err := db.GetPasteByID(id)
	if err != nil || paste == nil {
		t.Fatalf("GetPasteByID(%q): %v", id, err)
	}
	if paste.ContentType != "" {
		t.Errorf("ContentType: got %q, want empty", paste.ContentType)
	}
	if paste.Content != "plain text content" {
		t.Errorf("Content: got %q, want unmodified text", paste.Content)
	}
}
