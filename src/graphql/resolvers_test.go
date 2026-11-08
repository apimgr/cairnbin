package graphql

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/model"
)

// mockDB implements the DB interface for testing without a real database.
type mockDB struct {
	paste     *model.Paste
	pasteErr  error
	pastes    []model.PasteListItem
	total     int
	pastesErr error
	lastPage  int
	lastLimit int
}

func (m *mockDB) GetPasteByID(id string) (*model.Paste, error) {
	return m.paste, m.pasteErr
}

func (m *mockDB) GetPublicPastes(page, limit int) ([]model.PasteListItem, int, error) {
	m.lastPage = page
	m.lastLimit = limit
	return m.pastes, m.total, m.pastesErr
}

func (m *mockDB) SearchPublicPastes(page, limit int, search, sortBy, order string, pasteType ...string) ([]model.PasteListItem, int, error) {
	m.lastPage = page
	m.lastLimit = limit
	return m.pastes, m.total, m.pastesErr
}

// samplePaste returns a non-nil Paste for use in tests.
func samplePaste() *model.Paste {
	now := time.Now()
	return &model.Paste{
		ID:              "abc12345",
		Title:           "Test Paste",
		Content:         "hello world",
		Language:        "go",
		Visibility:      model.VisibilityPublic,
		BurnAfter:       0,
		DeleteTokenHash: "deadbeef",
		Views:           3,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// sampleItems returns a small list of PasteListItems.
func sampleItems() []model.PasteListItem {
	now := time.Now()
	return []model.PasteListItem{
		{ID: "aaa", Title: "First", Language: "go", Views: 1, CreatedAt: now},
		{ID: "bbb", Title: "Second", Language: "py", Views: 2, CreatedAt: now},
	}
}

// TestResolve_Schema verifies that a __schema introspection query returns the
// expected top-level shape (queryType name, types list present).
func TestResolve_Schema(t *testing.T) {
	r := NewResolver(&mockDB{})
	data, errs := r.Resolve("{ __schema { queryType { name } types { name } } }", nil)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	m, ok := data.(map[string]interface{})
	if !ok {
		t.Fatalf("data is not map: %T", data)
	}
	schema, ok := m["__schema"].(map[string]interface{})
	if !ok {
		t.Fatal("__schema key missing or wrong type")
	}
	qt, ok := schema["queryType"].(map[string]interface{})
	if !ok {
		t.Fatal("queryType missing")
	}
	if qt["name"] != "Query" {
		t.Errorf("queryType.name = %q, want %q", qt["name"], "Query")
	}
	types, ok := schema["types"].([]map[string]interface{})
	if !ok || len(types) == 0 {
		t.Error("types list missing or empty")
	}
}

// TestResolve_Paste_InlineArg checks resolving a paste using an inline id argument.
func TestResolve_Paste_InlineArg(t *testing.T) {
	db := &mockDB{paste: samplePaste()}
	r := NewResolver(db)
	data, errs := r.Resolve(`{ paste(id: "abc12345") { id title } }`, nil)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	m, ok := data.(map[string]interface{})
	if !ok {
		t.Fatalf("data is not map: %T", data)
	}
	paste, ok := m["paste"].(*model.Paste)
	if !ok {
		t.Fatalf("paste key wrong type: %T", m["paste"])
	}
	if paste.ID != "abc12345" {
		t.Errorf("paste.ID = %q, want %q", paste.ID, "abc12345")
	}
	// DeleteTokenHash must be cleared defensively
	if paste.DeleteTokenHash != "" {
		t.Errorf("DeleteTokenHash not cleared: %q", paste.DeleteTokenHash)
	}
}

// TestResolve_Paste_VarsMap checks resolving a paste with id provided via variables map.
func TestResolve_Paste_VarsMap(t *testing.T) {
	db := &mockDB{paste: samplePaste()}
	r := NewResolver(db)
	vars := map[string]interface{}{"id": "abc12345"}
	data, errs := r.Resolve(`{ paste(id: $id) { id } }`, vars)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	m, ok := data.(map[string]interface{})
	if !ok {
		t.Fatalf("data is not map: %T", data)
	}
	if _, ok := m["paste"]; !ok {
		t.Error("paste key missing in response")
	}
}

// TestResolve_Paste_NoID checks that a paste( query with no id argument returns an error.
// The query must contain "paste(" to trigger the paste branch; without an id the resolver
// returns "id is required".
func TestResolve_Paste_NoID(t *testing.T) {
	r := NewResolver(&mockDB{})
	// paste( triggers the paste branch; no id inline, no vars → empty id → error
	data, errs := r.Resolve(`{ paste(  ) { id } }`, nil)
	if len(errs) == 0 {
		t.Fatal("expected error for missing id, got none")
	}
	if data != nil {
		t.Errorf("expected nil data, got %v", data)
	}
	if !strings.Contains(errs[0]["message"].(string), "id is required") {
		t.Errorf("unexpected error message: %v", errs[0]["message"])
	}
}

// TestResolve_Paste_NotFound checks that a DB miss returns an error.
func TestResolve_Paste_NotFound(t *testing.T) {
	db := &mockDB{paste: nil, pasteErr: errors.New("not found")}
	r := NewResolver(db)
	data, errs := r.Resolve(`{ paste(id: "missing") { id } }`, nil)
	if len(errs) == 0 {
		t.Fatal("expected error for missing paste, got none")
	}
	if data != nil {
		t.Errorf("expected nil data, got %v", data)
	}
}

// TestResolve_Pastes_Default checks the default pastes list resolution.
func TestResolve_Pastes_Default(t *testing.T) {
	items := sampleItems()
	db := &mockDB{pastes: items, total: 2}
	r := NewResolver(db)
	data, errs := r.Resolve(`{ pastes { total page limit pastes { id } } }`, nil)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	m, ok := data.(map[string]interface{})
	if !ok {
		t.Fatalf("data not map: %T", data)
	}
	inner, ok := m["pastes"].(map[string]interface{})
	if !ok {
		t.Fatalf("pastes key wrong type: %T", m["pastes"])
	}
	if inner["total"] != 2 {
		t.Errorf("total = %v, want 2", inner["total"])
	}
	if inner["page"] != 1 {
		t.Errorf("page = %v, want 1", inner["page"])
	}
	if inner["limit"] != 20 {
		t.Errorf("limit = %v, want 20", inner["limit"])
	}
}

// TestResolve_Pastes_LimitClamping verifies limits of 0 and 101 are clamped to 20,
// and page < 1 is clamped to 1.
func TestResolve_Pastes_LimitClamping(t *testing.T) {
	cases := []struct {
		name      string
		vars      map[string]interface{}
		wantPage  int
		wantLimit int
	}{
		{"limit_zero_clamped", map[string]interface{}{"limit": float64(0)}, 1, 20},
		{"limit_101_clamped", map[string]interface{}{"limit": float64(101)}, 1, 20},
		{"limit_100_ok", map[string]interface{}{"limit": float64(100)}, 1, 100},
		{"page_zero_clamped", map[string]interface{}{"page": float64(0)}, 1, 20},
		{"page_neg_clamped", map[string]interface{}{"page": float64(-5)}, 1, 20},
		{"page_and_limit_valid", map[string]interface{}{"page": float64(3), "limit": float64(10)}, 3, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := &mockDB{pastes: nil, total: 0}
			r := NewResolver(db)
			_, errs := r.Resolve(`{ pastes { total } }`, tc.vars)
			if len(errs) != 0 {
				t.Fatalf("unexpected errors: %v", errs)
			}
			if db.lastPage != tc.wantPage {
				t.Errorf("page sent to DB = %d, want %d", db.lastPage, tc.wantPage)
			}
			if db.lastLimit != tc.wantLimit {
				t.Errorf("limit sent to DB = %d, want %d", db.lastLimit, tc.wantLimit)
			}
		})
	}
}

// TestResolve_Pastes_DBError checks that a DB error propagates as a GraphQL error.
func TestResolve_Pastes_DBError(t *testing.T) {
	db := &mockDB{pastesErr: errors.New("connection refused")}
	r := NewResolver(db)
	data, errs := r.Resolve(`{ pastes { total } }`, nil)
	if len(errs) == 0 {
		t.Fatal("expected error from DB failure, got none")
	}
	if data != nil {
		t.Errorf("expected nil data on DB error, got %v", data)
	}
	if !strings.Contains(errs[0]["message"].(string), "database error") {
		t.Errorf("unexpected error message: %v", errs[0]["message"])
	}
}

// TestExtractInlineArg covers double-quoted, single-quoted, and bare value forms,
// plus the no-match case.
func TestExtractInlineArg(t *testing.T) {
	cases := []struct {
		name string
		q    string
		arg  string
		want string
	}{
		{"double_quoted", `paste(id: "abc123") { id }`, "id", "abc123"},
		{"single_quoted", `paste(id: 'abc123') { id }`, "id", "abc123"},
		{"bare_value", `paste(id: abc123) { id }`, "id", "abc123"},
		{"no_match", `paste { id }`, "id", ""},
		{"bare_value_with_comma", `paste(id: abc123, other: x) { id }`, "id", "abc123"},
		{"bare_value_closes_paren", `paste(id: abc123)`, "id", "abc123"},
		{"double_quoted_with_spaces", `paste(id: "hello world") { id }`, "id", "hello world"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractInlineArg(tc.q, tc.arg)
			if got != tc.want {
				t.Errorf("extractInlineArg(%q, %q) = %q, want %q", tc.q, tc.arg, got, tc.want)
			}
		})
	}
}

// TestIntVar covers float64 value, int value, missing key, and nil vars map.
func TestIntVar(t *testing.T) {
	cases := []struct {
		name string
		vars map[string]interface{}
		key  string
		def  int
		want int
	}{
		{"float64_value", map[string]interface{}{"n": float64(42)}, "n", 0, 42},
		{"int_value", map[string]interface{}{"n": 7}, "n", 0, 7},
		{"missing_key_returns_default", map[string]interface{}{}, "n", 99, 99},
		{"nil_vars_returns_default", nil, "n", 5, 5},
		{"wrong_type_returns_default", map[string]interface{}{"n": "foo"}, "n", 3, 3},
		{"zero_float64", map[string]interface{}{"n": float64(0)}, "n", 10, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := intVar(tc.vars, tc.key, tc.def)
			if got != tc.want {
				t.Errorf("intVar(%v, %q, %d) = %d, want %d", tc.vars, tc.key, tc.def, got, tc.want)
			}
		})
	}
}

// TestHandler_POST_ValidQuery checks that a valid POST JSON query returns 200 with data.
func TestHandler_POST_ValidQuery(t *testing.T) {
	db := &mockDB{pastes: sampleItems(), total: 2}
	h := New(db, "Test")

	body := `{"query":"{ pastes { total page limit } }"}`
	req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if _, ok := resp["data"]; !ok {
		t.Error("response missing 'data' key")
	}
}

// TestHandler_POST_InvalidJSON checks that invalid JSON in the body returns 400.
func TestHandler_POST_InvalidJSON(t *testing.T) {
	h := New(&mockDB{}, "Test")

	req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{bad json`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	errs, ok := resp["errors"]
	if !ok {
		t.Fatal("missing 'errors' key in 400 response")
	}
	errList, ok := errs.([]interface{})
	if !ok || len(errList) == 0 {
		t.Fatal("errors list is empty or wrong type")
	}
}

// TestHandler_GET_WithQuery checks that GET with ?query= executes and returns JSON.
func TestHandler_GET_WithQuery(t *testing.T) {
	db := &mockDB{pastes: nil, total: 0}
	h := New(db, "Test")

	req := httptest.NewRequest(http.MethodGet, "/graphql?query=%7B+pastes+%7B+total+%7D+%7D", nil)
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json prefix", ct)
	}
}

// TestHandler_GET_NoQuery checks that GET without ?query= returns the GraphiQL HTML UI.
func TestHandler_GET_NoQuery(t *testing.T) {
	h := New(&mockDB{}, "TestSite")

	req := httptest.NewRequest(http.MethodGet, "/graphql", nil)
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html prefix", ct)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "<!DOCTYPE html>") {
		t.Error("response body does not contain <!DOCTYPE html>")
	}
	if !strings.Contains(body, "TestSite") {
		t.Error("response body does not contain site title")
	}
}

// TestHandler_PUT_MethodNotAllowed checks that PUT returns 405.
func TestHandler_PUT_MethodNotAllowed(t *testing.T) {
	h := New(&mockDB{}, "Test")

	req := httptest.NewRequest(http.MethodPut, "/graphql", nil)
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
	allow := rr.Header().Get("Allow")
	if !strings.Contains(allow, "GET") || !strings.Contains(allow, "POST") {
		t.Errorf("Allow header = %q, want it to contain GET and POST", allow)
	}
}

// TestHandler_POST_PasteByID checks the paste query via POST with variables.
func TestHandler_POST_PasteByID(t *testing.T) {
	db := &mockDB{paste: samplePaste()}
	h := New(db, "Test")

	payload := map[string]interface{}{
		"query":     `{ paste(id: "abc12345") { id title } }`,
		"variables": map[string]interface{}{},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if _, hasErrors := resp["errors"]; hasErrors {
		t.Errorf("unexpected errors in response: %v", resp["errors"])
	}
}

// TestHandler_POST_PasteNotFound checks error response when paste is not in DB.
func TestHandler_POST_PasteNotFound(t *testing.T) {
	db := &mockDB{paste: nil, pasteErr: errors.New("not found")}
	h := New(db, "Test")

	payload := `{"query":"{ paste(id: \"missing\") { id } }"}`
	req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (GraphQL errors go in body, not HTTP status)", rr.Code, http.StatusOK)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if _, hasErrors := resp["errors"]; !hasErrors {
		t.Error("expected errors key in response for not-found paste")
	}
}
