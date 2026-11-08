package handler_test

// Additional coverage tests: ListPastes, GetPasteForWeb, HighlightedContent,
// extra CreatePaste paths (multipart, raw body, burn_after, unlisted, expiry).

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/handler"
	"github.com/apimgr/cairnbin/src/model"
)

// ─── ListPastes ───────────────────────────────────────────────────────────────

func TestListPastes_Empty(t *testing.T) {
	h, _ := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pastes", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	rr := httptest.NewRecorder()
	h.ListPastes(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["ok"] != true {
		t.Errorf("ok: got %v", resp["ok"])
	}
}

func TestListPastes_WithPastes(t *testing.T) {
	h, _ := newTestHandler(t)

	for i := range 3 {
		body := fmt.Sprintf(`{"content":"paste number %d"}`, i)
		createViaAPI(t, h, body)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pastes?page=1&limit=10", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	rr := httptest.NewRecorder()
	h.ListPastes(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200\nbody: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	// PART 14 canonical list shape: data is the array itself, pagination is a
	// top-level sibling (never nested under data), with a "pages" field.
	if _, ok := resp["data"].([]interface{}); !ok {
		t.Fatal("data field must be a JSON array")
	}
	pagination, ok := resp["pagination"].(map[string]interface{})
	if !ok {
		t.Fatal("pagination field missing")
	}
	total := int(pagination["total"].(float64))
	if total < 1 {
		t.Errorf("total: got %d, want >= 1", total)
	}
	if _, ok := pagination["pages"]; !ok {
		t.Error("pagination missing pages field")
	}
}

func TestListPastes_DefaultPagination(t *testing.T) {
	h, _ := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pastes?page=0&limit=0", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	rr := httptest.NewRecorder()
	h.ListPastes(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}
}

func TestListPastes_LimitExceedsMax(t *testing.T) {
	h, _ := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pastes?limit=999", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	rr := httptest.NewRecorder()
	h.ListPastes(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}
}

// TestListPastes_Search verifies the ?search= query param filters by
// ID, title, and content.
func TestListPastes_Search(t *testing.T) {
	h, _ := newTestHandler(t)

	createViaAPI(t, h, `{"title":"Zebra notes","content":"unique-alpha-content"}`)
	createViaAPI(t, h, `{"title":"Apple pie","content":"unique-beta-content"}`)
	createViaAPI(t, h, `{"title":"Mango salad","content":"search-target-here"}`)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pastes?search=Mango", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	rr := httptest.NewRecorder()
	h.ListPastes(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200\nbody: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	data, ok := resp["data"].([]interface{})
	if !ok {
		t.Fatal("data field must be a JSON array")
	}
	if len(data) != 1 {
		t.Fatalf("got %d results, want 1", len(data))
	}
	item := data[0].(map[string]interface{})
	if item["title"] != "Mango salad" {
		t.Errorf("title: got %v, want Mango salad", item["title"])
	}
}

// TestListPastes_SortByName verifies the ?sort=name&order=asc query params
// order results alphabetically.
func TestListPastes_SortByName(t *testing.T) {
	h, _ := newTestHandler(t)

	createViaAPI(t, h, `{"title":"Zebra notes","content":"content-z"}`)
	createViaAPI(t, h, `{"title":"Apple pie","content":"content-a"}`)
	createViaAPI(t, h, `{"title":"Mango salad","content":"content-m"}`)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pastes?sort=name&order=asc", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	rr := httptest.NewRecorder()
	h.ListPastes(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200\nbody: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	data, ok := resp["data"].([]interface{})
	if !ok {
		t.Fatal("data field must be a JSON array")
	}
	if len(data) != 3 {
		t.Fatalf("got %d results, want 3", len(data))
	}
	want := []string{"Apple pie", "Mango salad", "Zebra notes"}
	for i, w := range want {
		item := data[i].(map[string]interface{})
		if item["title"] != w {
			t.Errorf("data[%d].title: got %v, want %q", i, item["title"], w)
		}
	}
}

// ─── GetPasteForWeb ───────────────────────────────────────────────────────────

func TestGetPasteForWeb_NotFound(t *testing.T) {
	h, _ := newTestHandler(t)

	p, err := h.GetPasteForWeb("notexist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p != nil {
		t.Errorf("expected nil paste for not found, got %+v", p)
	}
}

func TestGetPasteForWeb_Found(t *testing.T) {
	h, _ := newTestHandler(t)

	m := createViaAPI(t, h, `{"content":"web view content"}`)
	data := m["data"].(map[string]interface{})
	id := data["id"].(string)

	p, err := h.GetPasteForWeb(id)
	if err != nil {
		t.Fatalf("GetPasteForWeb: %v", err)
	}
	if p == nil {
		t.Fatal("expected paste, got nil")
	}
	if p.Content != "web view content" {
		t.Errorf("content: got %q, want %q", p.Content, "web view content")
	}
	if p.DeleteTokenHash != "" {
		t.Error("DeleteTokenHash should be cleared")
	}
}

func TestGetPasteForWeb_BurnAfter(t *testing.T) {
	h, _ := newTestHandler(t)

	m := createViaAPI(t, h, `{"content":"burn me","burn_after":1}`)
	data := m["data"].(map[string]interface{})
	id := data["id"].(string)

	p1, err := h.GetPasteForWeb(id)
	if err != nil {
		t.Fatalf("first GetPasteForWeb: %v", err)
	}
	if p1 == nil {
		t.Fatal("first call: expected paste, got nil")
	}

	p2, err := h.GetPasteForWeb(id)
	if err != nil {
		t.Fatalf("second GetPasteForWeb: %v", err)
	}
	if p2 != nil {
		t.Errorf("second call: expected paste deleted after burn_after=1, got %+v", p2)
	}
}

// ─── HighlightedContent ───────────────────────────────────────────────────────

func TestHighlightedContent(t *testing.T) {
	cases := []struct {
		name    string
		lang    string
		content string
	}{
		{"go_code", "go", "package main"},
		{"plain_text", "text", "hello world"},
		{"unknown_lang", "unknownlang99", "some content"},
		{"empty_content", "go", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			paste := &model.Paste{
				Language: tc.lang,
				Content:  tc.content,
			}
			result := handler.HighlightedContent(paste, true)
			// Result must always be valid (non-panicking); empty content may yield empty.
			_ = result
			resultNoLN := handler.HighlightedContent(paste, false)
			_ = resultNoLN
		})
	}
}

// ─── CreatePaste extra branches ───────────────────────────────────────────────

func TestCreatePaste_RawBody(t *testing.T) {
	h, _ := newTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pastes",
		strings.NewReader("raw paste content from curl"))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-Title", "My Raw Paste")
	req.Header.Set("X-Language", "text")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	rr := httptest.NewRecorder()
	h.CreatePaste(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201\nbody: %s", rr.Code, rr.Body.String())
	}
}

func TestCreatePaste_PlainTextResponse(t *testing.T) {
	h, _ := newTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/paste",
		strings.NewReader("hello plain"))
	req.Header.Set("Content-Type", "text/plain")
	// No Accept or /api/ prefix → plain text response.

	rr := httptest.NewRecorder()
	h.CreatePaste(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201\nbody: %s", rr.Code, rr.Body.String())
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type: got %q, want text/plain", ct)
	}
}

func TestCreatePaste_BurnAfter(t *testing.T) {
	h, _ := newTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pastes",
		strings.NewReader(`{"content":"burn after 3 reads","burn_after":3}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	rr := httptest.NewRecorder()
	h.CreatePaste(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201", rr.Code)
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	data := resp["data"].(map[string]interface{})
	ba := int(data["burn_after"].(float64))
	if ba != 3 {
		t.Errorf("burn_after: got %d, want 3", ba)
	}
}

func TestCreatePaste_Unlisted(t *testing.T) {
	h, _ := newTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pastes",
		strings.NewReader(`{"content":"secret paste","visibility":"unlisted"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	rr := httptest.NewRecorder()
	h.CreatePaste(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201", rr.Code)
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	data := resp["data"].(map[string]interface{})
	vis := int(data["visibility"].(float64))
	if vis != model.VisibilityUnlisted {
		t.Errorf("visibility: got %d, want %d (unlisted)", vis, model.VisibilityUnlisted)
	}
}

func TestCreatePaste_WithExpiry(t *testing.T) {
	h, _ := newTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pastes",
		strings.NewReader(`{"content":"expires soon","expires_in":"1h"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	rr := httptest.NewRecorder()
	h.CreatePaste(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201", rr.Code)
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	data := resp["data"].(map[string]interface{})
	if data["expires_at"] == nil {
		t.Error("expires_at should be set")
	}
}

func TestCreatePaste_MultipartForm_Content(t *testing.T) {
	h, _ := newTestHandler(t)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("content", "multipart paste content")
	_ = w.WriteField("title", "Multipart Title")
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pastes", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	rr := httptest.NewRecorder()
	h.CreatePaste(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201\nbody: %s", rr.Code, rr.Body.String())
	}
}

func TestCreatePaste_MultipartForm_FileUpload(t *testing.T) {
	h, _ := newTestHandler(t)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("files", "main.go")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	_, _ = io.WriteString(fw, "package main\n\nfunc main() {}")
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pastes", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	rr := httptest.NewRecorder()
	h.CreatePaste(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201\nbody: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	data := resp["data"].(map[string]interface{})
	if data["language"] != "go" {
		t.Errorf("language: got %v, want go (detected from main.go)", data["language"])
	}
}

func TestCreatePaste_FormWithBurnAfter(t *testing.T) {
	h, _ := newTestHandler(t)

	form := url.Values{
		"content":    {"burn after 2 from form"},
		"burn_after": {"2"},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pastes",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	rr := httptest.NewRecorder()
	h.CreatePaste(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201\nbody: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	data := resp["data"].(map[string]interface{})
	ba := int(data["burn_after"].(float64))
	if ba != 2 {
		t.Errorf("burn_after: got %d, want 2", ba)
	}
}

// ─── GetPaste burn_after via HTTP ─────────────────────────────────────────────

func TestGetPaste_BurnAfter(t *testing.T) {
	h, _ := newTestHandler(t)

	m := createViaAPI(t, h, `{"content":"burn on read","burn_after":1}`)
	data := m["data"].(map[string]interface{})
	id := data["id"].(string)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pastes/"+id, nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req = withID(req, id)

	rr := httptest.NewRecorder()
	h.GetPaste(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("first read: status %d, want 200", rr.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/pastes/"+id, nil)
	req2.Header.Set("Accept", "application/json")
	req2.Header.Set("User-Agent", "Mozilla/5.0")
	req2 = withID(req2, id)

	rr2 := httptest.NewRecorder()
	h.GetPaste(rr2, req2)

	if rr2.Code != http.StatusNotFound {
		t.Errorf("second read (after burn): status %d, want 404", rr2.Code)
	}
}

// ─── GetRawPaste burn_after ───────────────────────────────────────────────────

func TestGetRawPaste_BurnAfter(t *testing.T) {
	h, _ := newTestHandler(t)

	m := createViaAPI(t, h, `{"content":"raw burn content","burn_after":1}`)
	data := m["data"].(map[string]interface{})
	id := data["id"].(string)

	req := httptest.NewRequest(http.MethodGet, "/"+id+"/raw", nil)
	req = withID(req, id)

	rr := httptest.NewRecorder()
	h.GetRawPaste(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/"+id+"/raw", nil)
	req2 = withID(req2, id)

	rr2 := httptest.NewRecorder()
	h.GetRawPaste(rr2, req2)

	if rr2.Code != http.StatusNotFound {
		t.Errorf("after burn: status %d, want 404", rr2.Code)
	}
}

// ─── CreatePaste HTTPS link ───────────────────────────────────────────────────

// TestCreatePaste_HTTPSLink verifies that when a trusted base-URL resolver
// returns an https URL, the created paste link uses https (PART 12).
// X-Forwarded-Proto without a resolver must NOT be trusted (proxy-spoofing guard).
func TestCreatePaste_HTTPSLink(t *testing.T) {
	h, _ := newTestHandler(t)
	h.SetBaseURLResolver(func(*http.Request) string { return "https://paste.example.com" })

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pastes",
		strings.NewReader(`{"content":"https test"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Host = "paste.example.com"

	rr := httptest.NewRecorder()
	h.CreatePaste(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201", rr.Code)
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	data := resp["data"].(map[string]interface{})
	link := data["link"].(string)
	if !strings.HasPrefix(link, "https://") {
		t.Errorf("link: got %q, want https:// prefix", link)
	}
}

// ─── CreateFromForm (no-JS web create flow, PART 16) ────────────────────────────

// TestCreateFromForm_Success verifies that a urlencoded form POST produces a
// CreateResponse with the populated fields the confirmation template renders.
func TestCreateFromForm_Success(t *testing.T) {
	h, _ := newTestHandler(t)

	form := url.Values{
		"content":  {"hello no-js world"},
		"title":    {"my paste"},
		"language": {"text"},
	}
	req := httptest.NewRequest(http.MethodPost, "/create",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, status, err := h.CreateFromForm(req)
	if err != nil {
		t.Fatalf("CreateFromForm: status=%d err=%v", status, err)
	}
	if status != 0 {
		t.Errorf("status: got %d, want 0 on success", status)
	}
	if resp == nil {
		t.Fatal("resp is nil")
	}
	if resp.ID == "" {
		t.Error("ID is empty")
	}
	if resp.Title != "my paste" {
		t.Errorf("Title: got %q, want %q", resp.Title, "my paste")
	}
	if resp.Link == "" {
		t.Error("Link is empty")
	}
	if resp.OwnerToken == "" {
		t.Error("OwnerToken is empty")
	}
}

// TestCreateFromForm_EmptyContent verifies that an empty form submission returns
// a non-zero HTTP status and an error rather than creating an empty paste.
func TestCreateFromForm_EmptyContent(t *testing.T) {
	h, _ := newTestHandler(t)

	form := url.Values{"content": {""}}
	req := httptest.NewRequest(http.MethodPost, "/create",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, status, err := h.CreateFromForm(req)
	if err == nil {
		t.Fatal("expected error for empty content, got nil")
	}
	if status == 0 {
		t.Error("expected non-zero status for empty content")
	}
	if resp != nil {
		t.Errorf("expected nil resp on error, got %+v", resp)
	}
}

// ─── CreatePaste — binary content handling ───────────────────────────────────

// TestCreatePaste_JSONBinaryDetectedAndEncoded verifies that raw non-text
// content arriving via JSON is MIME-detected, base64-encoded, and stored with
// its ContentType so viewers can decode and render it.
func TestCreatePaste_JSONBinaryDetectedAndEncoded(t *testing.T) {
	h, db := newTestHandler(t)

	raw := "%PDF-1.4 fake pdf body"
	body, _ := json.Marshal(map[string]string{"content": raw})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pastes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	rr := httptest.NewRecorder()
	h.CreatePaste(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201\nbody: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	id := resp["data"].(map[string]interface{})["id"].(string)

	paste, err := db.GetPasteByID(id)
	if err != nil || paste == nil {
		t.Fatalf("GetPasteByID(%q): %v", id, err)
	}
	if paste.ContentType != "application/pdf" {
		t.Errorf("ContentType: got %q, want application/pdf", paste.ContentType)
	}
	decoded, err := base64.StdEncoding.DecodeString(paste.Content)
	if err != nil {
		t.Fatalf("stored content is not base64: %v", err)
	}
	if string(decoded) != raw {
		t.Errorf("decoded content: got %q, want %q", decoded, raw)
	}
}

// TestCreatePaste_BinaryContentTypeNonBase64Rejected verifies that a client
// claiming a binary content_type but sending non-base64 content gets a 400.
func TestCreatePaste_BinaryContentTypeNonBase64Rejected(t *testing.T) {
	h, _ := newTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pastes",
		strings.NewReader(`{"content":"@@ not base64 @@","content_type":"image/png"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	rr := httptest.NewRecorder()
	h.CreatePaste(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400\nbody: %s", rr.Code, rr.Body.String())
	}
}

// TestCreatePaste_MultipartBinaryFileStoredBase64 verifies that a binary file
// uploaded via multipart form is stored base64-encoded with its detected MIME
// type and round-trips byte-for-byte.
func TestCreatePaste_MultipartBinaryFileStoredBase64(t *testing.T) {
	h, db := newTestHandler(t)

	png := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x01, 0x02, 0x03}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("files", "img.png")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	_, _ = fw.Write(png)
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pastes", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	rr := httptest.NewRecorder()
	h.CreatePaste(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201\nbody: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	id := resp["data"].(map[string]interface{})["id"].(string)

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
	if !bytes.Equal(decoded, png) {
		t.Errorf("decoded content does not match uploaded bytes")
	}
}
