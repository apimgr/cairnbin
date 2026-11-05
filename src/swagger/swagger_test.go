package swagger_test

// Tests for the swagger package: handler construction, ServeSpec JSON shape,
// ServeUI HTML output, resolveBase logic, and operationID generation via
// the spec JSON. All tests use net/http/httptest — no external services.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/swagger"
)

// ─── New ─────────────────────────────────────────────────────────────────────

// TestNew verifies that New returns a non-nil handler for every combination of
// filled and empty arguments.
func TestNew(t *testing.T) {
	cases := []struct {
		name    string
		title   string
		version string
		baseURL string
	}{
		{"all fields", "Cairnbin API", "1.2.3", "https://example.com"},
		{"empty baseURL", "Cairnbin", "0.1.0", ""},
		{"empty title", "", "1.0.0", "https://example.com"},
		{"all empty", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := swagger.New(tc.title, tc.version, tc.baseURL, "")
			if h == nil {
				t.Fatal("New returned nil")
			}
		})
	}
}

// ─── ServeSpec ────────────────────────────────────────────────────────────────

// TestServeSpec_StatusAndContentType checks the HTTP status code and
// Content-Type header returned by ServeSpec.
func TestServeSpec_StatusAndContentType(t *testing.T) {
	h := swagger.New("Test API", "1.0.0", "https://api.example.com", "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/swagger", nil)
	rec := httptest.NewRecorder()

	h.ServeSpec(rec, req)

	resp := rec.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want prefix %q", ct, "application/json")
	}
}

// TestServeSpec_ValidJSON confirms the response body is valid JSON.
func TestServeSpec_ValidJSON(t *testing.T) {
	h := swagger.New("Test API", "1.0.0", "https://api.example.com", "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/swagger", nil)
	rec := httptest.NewRecorder()

	h.ServeSpec(rec, req)

	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	var spec map[string]interface{}
	if err := json.Unmarshal(body, &spec); err != nil {
		t.Fatalf("body is not valid JSON: %v\nbody: %s", err, body)
	}
}

// TestServeSpec_RequiredTopLevelKeys confirms "openapi" and "paths" are present
// and that "openapi" equals "3.0.3".
func TestServeSpec_RequiredTopLevelKeys(t *testing.T) {
	h := swagger.New("Test API", "2.0.0", "https://api.example.com", "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/swagger", nil)
	rec := httptest.NewRecorder()

	h.ServeSpec(rec, req)

	var spec map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if _, ok := spec["openapi"]; !ok {
		t.Error("spec missing top-level key: openapi")
	}
	if spec["openapi"] != "3.0.3" {
		t.Errorf("openapi version: got %v, want 3.0.3", spec["openapi"])
	}
	if _, ok := spec["paths"]; !ok {
		t.Error("spec missing top-level key: paths")
	}
}

// TestServeSpec_InfoBlock verifies info.title and info.version reflect what
// was passed to New.
func TestServeSpec_InfoBlock(t *testing.T) {
	cases := []struct {
		name    string
		title   string
		version string
	}{
		{"normal values", "My Paste Service", "3.1.4"},
		{"empty title", "", "0.0.1"},
		{"empty version", "Pasta", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := swagger.New(tc.title, tc.version, "https://example.com", "")
			req := httptest.NewRequest(http.MethodGet, "/api/v1/server/swagger", nil)
			rec := httptest.NewRecorder()
			h.ServeSpec(rec, req)

			var spec map[string]interface{}
			if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			info, ok := spec["info"].(map[string]interface{})
			if !ok {
				t.Fatal("info block missing or wrong type")
			}
			if info["title"] != tc.title {
				t.Errorf("info.title: got %v, want %q", info["title"], tc.title)
			}
			if info["version"] != tc.version {
				t.Errorf("info.version: got %v, want %q", info["version"], tc.version)
			}
		})
	}
}

// TestServeSpec_BaseURLInServers verifies that a configured baseURL appears in
// the servers array of the generated spec (exercises resolveBase with explicit URL).
func TestServeSpec_BaseURLInServers(t *testing.T) {
	const base = "https://paste.example.com"
	h := swagger.New("API", "1.0.0", base, "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/swagger", nil)
	rec := httptest.NewRecorder()
	h.ServeSpec(rec, req)

	var spec map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	servers, ok := spec["servers"].([]interface{})
	if !ok || len(servers) == 0 {
		t.Fatal("servers array missing or empty")
	}
	first, ok := servers[0].(map[string]interface{})
	if !ok {
		t.Fatal("first server entry is not an object")
	}
	if first["url"] != base {
		t.Errorf("servers[0].url: got %v, want %q", first["url"], base)
	}
}

// TestServeSpec_BaseURLFromRequest verifies that when no baseURL is configured,
// resolveBase derives the base URL from the incoming request host.
func TestServeSpec_BaseURLFromRequest(t *testing.T) {
	h := swagger.New("API", "1.0.0", "", "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/swagger", nil)
	req.Host = "dynamic.host.example.com"
	rec := httptest.NewRecorder()
	h.ServeSpec(rec, req)

	var spec map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	servers, ok := spec["servers"].([]interface{})
	if !ok || len(servers) == 0 {
		t.Fatal("servers array missing or empty")
	}
	first := servers[0].(map[string]interface{})
	want := "http://dynamic.host.example.com"
	if first["url"] != want {
		t.Errorf("servers[0].url: got %v, want %q", first["url"], want)
	}
}

// TestServeSpec_ForwardedHeadersIgnoredWithoutResolver verifies that
// X-Forwarded-Proto and X-Forwarded-Host are NOT honoured when no trusted
// resolver is registered (PART 12 proxy-spoofing guard). The bare connection
// info (r.TLS + r.Host) is used instead.
func TestServeSpec_ForwardedHeadersIgnoredWithoutResolver(t *testing.T) {
	h := swagger.New("API", "1.0.0", "", "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/swagger", nil)
	req.Host = "internal.host"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "public.example.com")
	rec := httptest.NewRecorder()
	h.ServeSpec(rec, req)

	var spec map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	servers := spec["servers"].([]interface{})
	first := servers[0].(map[string]interface{})
	// Without a trusted resolver the handler uses the bare connection: http + r.Host.
	want := "http://internal.host"
	if first["url"] != want {
		t.Errorf("servers[0].url: got %v, want %q", first["url"], want)
	}
}

// TestServeSpec_BaseURLFromResolver verifies that a registered base-URL resolver
// is called and its result is used as the server URL in the spec (PART 12).
func TestServeSpec_BaseURLFromResolver(t *testing.T) {
	h := swagger.New("API", "1.0.0", "", "")
	h.SetBaseURLResolver(func(_ *http.Request) string {
		return "https://public.example.com"
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/swagger", nil)
	req.Host = "internal.host"
	rec := httptest.NewRecorder()
	h.ServeSpec(rec, req)

	var spec map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	servers := spec["servers"].([]interface{})
	first := servers[0].(map[string]interface{})
	want := "https://public.example.com"
	if first["url"] != want {
		t.Errorf("servers[0].url: got %v, want %q", first["url"], want)
	}
}

// TestServeSpec_PathsNonEmpty confirms that at least one path is registered,
// proving buildSpec exercised the Routes() list.
func TestServeSpec_PathsNonEmpty(t *testing.T) {
	h := swagger.New("API", "1.0.0", "https://example.com", "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/swagger", nil)
	rec := httptest.NewRecorder()
	h.ServeSpec(rec, req)

	var spec map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	paths, ok := spec["paths"].(map[string]interface{})
	if !ok {
		t.Fatal("paths is not an object")
	}
	if len(paths) == 0 {
		t.Error("paths object is empty — no routes registered")
	}
}

// TestServeSpec_OperationIDPresent verifies that operationId is present on at
// least one operation. This exercises operationID indirectly through buildSpec.
func TestServeSpec_OperationIDPresent(t *testing.T) {
	h := swagger.New("API", "1.0.0", "https://example.com", "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/swagger", nil)
	rec := httptest.NewRecorder()
	h.ServeSpec(rec, req)

	var spec map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	paths := spec["paths"].(map[string]interface{})

	found := false
	for _, item := range paths {
		pathObj, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		for _, op := range pathObj {
			opObj, ok := op.(map[string]interface{})
			if !ok {
				continue
			}
			if id, ok := opObj["operationId"]; ok && id != "" {
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Error("no operationId found in any path operation")
	}
}

// TestServeSpec_KnownOperationID checks that the GET /api/v1/pastes route
// generates the expected operationId string.
func TestServeSpec_KnownOperationID(t *testing.T) {
	h := swagger.New("API", "1.0.0", "https://example.com", "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/swagger", nil)
	rec := httptest.NewRecorder()
	h.ServeSpec(rec, req)

	var spec map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	paths := spec["paths"].(map[string]interface{})
	pastesPath, ok := paths["/api/v1/pastes"].(map[string]interface{})
	if !ok {
		t.Fatal("path /api/v1/pastes not found in spec")
	}
	getOp, ok := pastesPath["get"].(map[string]interface{})
	if !ok {
		t.Fatal("GET /api/v1/pastes not found in spec")
	}
	// operationID("GET", "/api/v1/pastes") → "getApiV1Pastes"
	if getOp["operationId"] != "getApiV1Pastes" {
		t.Errorf("operationId: got %v, want %q", getOp["operationId"], "getApiV1Pastes")
	}
}

// TestServeSpec_ParameterStructure checks that route parameters are serialised
// with the required fields (name, in, required, schema).
func TestServeSpec_ParameterStructure(t *testing.T) {
	h := swagger.New("API", "1.0.0", "https://example.com", "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/swagger", nil)
	rec := httptest.NewRecorder()
	h.ServeSpec(rec, req)

	var spec map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	paths := spec["paths"].(map[string]interface{})
	idPath, ok := paths["/api/v1/pastes/{id}"].(map[string]interface{})
	if !ok {
		t.Fatal("path /api/v1/pastes/{id} not found in spec")
	}
	getOp := idPath["get"].(map[string]interface{})
	params, ok := getOp["parameters"].([]interface{})
	if !ok || len(params) == 0 {
		t.Fatal("parameters missing on GET /api/v1/pastes/{id}")
	}
	p := params[0].(map[string]interface{})
	for _, key := range []string{"name", "in", "required", "schema"} {
		if _, ok := p[key]; !ok {
			t.Errorf("parameter missing field %q", key)
		}
	}
}

// TestServeSpec_CacheControlHeader confirms Cache-Control: no-cache is set.
func TestServeSpec_CacheControlHeader(t *testing.T) {
	h := swagger.New("API", "1.0.0", "https://example.com", "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/swagger", nil)
	rec := httptest.NewRecorder()
	h.ServeSpec(rec, req)

	cc := rec.Header().Get("Cache-Control")
	if cc != "no-cache" {
		t.Errorf("Cache-Control: got %q, want %q", cc, "no-cache")
	}
}

// ─── ServeUI ──────────────────────────────────────────────────────────────────

// TestServeUI_StatusAndContentType verifies the HTTP status and Content-Type.
func TestServeUI_StatusAndContentType(t *testing.T) {
	h := swagger.New("Cairnbin", "1.0.0", "https://example.com", "")
	req := httptest.NewRequest(http.MethodGet, "/server/swagger", nil)
	rec := httptest.NewRecorder()

	h.ServeUI(rec, req)

	resp := rec.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type: got %q, want prefix %q", ct, "text/html")
	}
}

// TestServeUI_BodyContainsSwagger confirms the HTML body contains "swagger"
// (case-insensitive), proving the UI page was rendered.
func TestServeUI_BodyContainsSwagger(t *testing.T) {
	h := swagger.New("Cairnbin", "1.0.0", "https://example.com", "")
	req := httptest.NewRequest(http.MethodGet, "/server/swagger", nil)
	rec := httptest.NewRecorder()

	h.ServeUI(rec, req)

	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(strings.ToLower(string(body)), "swagger") {
		t.Error("HTML body does not contain 'swagger'")
	}
}

// TestServeUI_BodyContainsTitle confirms the configured title appears in the
// HTML body.
func TestServeUI_BodyContainsTitle(t *testing.T) {
	const title = "SpecialPasteAPITitle"
	h := swagger.New(title, "1.0.0", "https://example.com", "")
	req := httptest.NewRequest(http.MethodGet, "/server/swagger", nil)
	rec := httptest.NewRecorder()
	h.ServeUI(rec, req)

	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), title) {
		t.Errorf("HTML body does not contain title %q", title)
	}
}

// TestServeUI_BodyContainsSpecURL verifies that the rendered UI embeds the
// spec URL (base + "/api/v1/server/swagger").
func TestServeUI_BodyContainsSpecURL(t *testing.T) {
	const base = "https://paste.example.com"
	h := swagger.New("API", "1.0.0", base, "")
	req := httptest.NewRequest(http.MethodGet, "/server/swagger", nil)
	rec := httptest.NewRecorder()
	h.ServeUI(rec, req)

	body, _ := io.ReadAll(rec.Body)
	want := base + "/api/v1/server/swagger"
	if !strings.Contains(string(body), want) {
		t.Errorf("HTML body does not contain spec URL %q", want)
	}
}

// TestServeUI_IsValidHTML confirms the body starts with <!DOCTYPE html> and
// contains </html>, which are the outermost document markers.
func TestServeUI_IsValidHTML(t *testing.T) {
	h := swagger.New("API", "1.0.0", "https://example.com", "")
	req := httptest.NewRequest(http.MethodGet, "/server/swagger", nil)
	rec := httptest.NewRecorder()
	h.ServeUI(rec, req)

	body := rec.Body.String()
	if !strings.HasPrefix(strings.TrimSpace(body), "<!DOCTYPE html>") {
		t.Error("body does not start with <!DOCTYPE html>")
	}
	if !strings.Contains(body, "</html>") {
		t.Error("body does not contain closing </html>")
	}
}

// TestServeUI_CacheControlHeader confirms the UI shell is an HTML response and
// therefore never cached: no-store plus a build-stamp ETag (AI.md PART 9, 13287).
func TestServeUI_CacheControlHeader(t *testing.T) {
	h := swagger.New("API", "1.0.0", "https://example.com", "")
	req := httptest.NewRequest(http.MethodGet, "/server/swagger", nil)
	rec := httptest.NewRecorder()
	h.ServeUI(rec, req)

	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control: got %q, want %q", cc, "no-store")
	}
	if etag := rec.Header().Get("ETag"); etag == "" {
		t.Error("ETag: got empty, want the build stamp")
	}
}

// ─── Routes via mux ───────────────────────────────────────────────────────────

// TestHandlerViaMux confirms that both routes work correctly when served
// through a standard http.ServeMux, which is the normal runtime setup.
func TestHandlerViaMux(t *testing.T) {
	h := swagger.New("Mux API", "9.9.9", "https://mux.example.com", "")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/server/swagger", h.ServeSpec)
	mux.HandleFunc("/server/swagger", h.ServeUI)

	cases := []struct {
		name         string
		path         string
		wantStatus   int
		wantCTPrefix string
		wantBodyStr  string
	}{
		{
			name:         "spec endpoint",
			path:         "/api/v1/server/swagger",
			wantStatus:   http.StatusOK,
			wantCTPrefix: "application/json",
			wantBodyStr:  "openapi",
		},
		{
			name:         "UI endpoint",
			path:         "/server/swagger",
			wantStatus:   http.StatusOK,
			wantCTPrefix: "text/html",
			wantBodyStr:  "swagger",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status: got %d, want %d", rec.Code, tc.wantStatus)
			}
			ct := rec.Header().Get("Content-Type")
			if !strings.HasPrefix(ct, tc.wantCTPrefix) {
				t.Errorf("Content-Type: got %q, want prefix %q", ct, tc.wantCTPrefix)
			}
			if !strings.Contains(strings.ToLower(rec.Body.String()), tc.wantBodyStr) {
				t.Errorf("body does not contain %q", tc.wantBodyStr)
			}
		})
	}
}
