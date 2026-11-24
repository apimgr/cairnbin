package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// newTestHandler returns a handler whose dispatch target echoes the request it
// received, so tests can assert on method, path, query, body and headers.
func newTestHandler() *Handler {
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/missing") {
			w.WriteHeader(http.StatusNotFound)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"method": r.Method,
			"path":   r.URL.EscapedPath(),
			"query":  r.URL.RawQuery,
			"body":   string(body),
			"auth":   r.Header.Get("Authorization"),
			"cookie": r.Header.Get("Cookie"),
		})
	})
	return New(Config{
		APIVersion:    "v1",
		ServerName:    "cairnbin",
		ServerVersion: "1.2.3",
		Dispatch:      echo,
		Authenticated: func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer test-token" },
		OriginAllowed: func(r *http.Request) bool { return r.Header.Get("Origin") == "http://example.com" },
	})
}

func rpc(t *testing.T, h *Handler, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var payload map[string]any
	if json.Unmarshal([]byte(body), &payload) == nil {
		meta, _ := payload["_meta"].(map[string]any)
		if meta == nil {
			meta = map[string]any{}
			payload["_meta"] = meta
		}
		meta["io.modelcontextprotocol/protocolVersion"] = supportedVersions[0]
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = string(encoded)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	var request map[string]any
	if json.Unmarshal([]byte(body), &request) == nil {
		method, _ := request["method"].(string)
		req.Header.Set("MCP-Protocol-Version", supportedVersions[0])
		req.Header.Set("Mcp-Method", method)
		if method == "tools/call" {
			params, _ := request["params"].(map[string]any)
			name, _ := params["name"].(string)
			req.Header.Set("Mcp-Name", name)
		}
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return m
}

func TestMCPIsAlwaysEnabled(t *testing.T) {
	rec := rpc(t, newTestHandler(), `{"jsonrpc":"2.0","id":1,"method":"ping"}`, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 when Enabled callback returns false", rec.Code)
	}
}

func TestGetAndDeleteAre405(t *testing.T) {
	h := newTestHandler()
	for _, m := range []string{http.MethodGet, http.MethodDelete} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(m, "/mcp", nil))
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" {
			t.Errorf("%s: status=%d allow=%q, want 405/POST", m, rec.Code, rec.Header().Get("Allow"))
		}
	}
}

func TestInitializeRejectsLegacyHandshake(t *testing.T) {
	h := newTestHandler()
	got := decode(t, rpc(t, h, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`, nil))
	if rec := got["error"].(map[string]any); int(rec["code"].(float64)) != codeInvalidRequest {
		t.Errorf("initialize error = %v, want UnsupportedProtocolVersion", rec)
	}
	if rec := rpc(t, h, `{"jsonrpc":"2.0","id":2,"method":"ping"}`, map[string]string{"MCP-Protocol-Version": "1999-01-01"}); rec.Code != http.StatusBadRequest {
		t.Errorf("unsupported protocol status=%d, want 400", rec.Code)
	}
}

func TestNotificationGets202(t *testing.T) {
	rec := rpc(t, newTestHandler(), `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil)
	if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
		t.Errorf("status=%d body=%q, want 202 empty", rec.Code, rec.Body.String())
	}
}

func TestUnknownMethodAndParseError(t *testing.T) {
	h := newTestHandler()
	got := decode(t, rpc(t, h, `{"jsonrpc":"2.0","id":1,"method":"nope"}`, nil))
	if e := got["error"].(map[string]any); int(e["code"].(float64)) != codeMethodNotFound {
		t.Errorf("error = %v, want method not found", e)
	}
	if rec := rpc(t, h, `{not json`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("parse error status = %d, want 400", rec.Code)
	}
}

func TestToolsListGeneratedFromRoutes(t *testing.T) {
	got := decode(t, rpc(t, newTestHandler(), `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil))
	tools := got["result"].(map[string]any)["tools"].([]any)
	names := map[string]map[string]any{}
	for _, raw := range tools {
		tl := raw.(map[string]any)
		names[tl["name"].(string)] = tl
	}
	for _, want := range []string{"postApiV1Pastes", "getApiV1Pastes", "getApiV1PastesId", "deleteApiV1PastesId", "getApiV1ServerHealthz"} {
		if names[want] == nil {
			t.Errorf("tool %q missing", want)
		}
	}
	// Never exposed: compatibility shims, docs endpoints, token management.
	for name := range names {
		for _, banned := range []string{"Preferences", "Swagger", "Graphql", "Mcp", "ApiNew", "Documents"} {
			if strings.Contains(name, banned) {
				t.Errorf("tool %q must not be exposed", name)
			}
		}
	}
	schema := names["getApiV1PastesId"]["inputSchema"].(map[string]any)
	if _, ok := schema["properties"].(map[string]any)["id"]; !ok {
		t.Errorf("getApiV1PastesId schema lacks id: %v", schema)
	}
	listSchema := names["getApiV1Pastes"]["inputSchema"].(map[string]any)
	if _, ok := listSchema["properties"].(map[string]any)["scope"]; !ok {
		t.Errorf("getApiV1Pastes schema lacks public/owned scope: %v", listSchema)
	}
	if annotations := names["getApiV1PastesId"]["annotations"].(map[string]any); annotations["readOnlyHint"] != true {
		t.Errorf("GET tool annotations = %v, want readOnlyHint", annotations)
	}
	if annotations := names["postApiV1Pastes"]["annotations"].(map[string]any); annotations["readOnlyHint"] != false {
		t.Errorf("POST tool annotations = %v, want readOnlyHint false", annotations)
	}
}

func TestToolResultCredentialFieldsAreRemovedRecursively(t *testing.T) {
	got := string(removeCredentialFields([]byte(`{"owner_token":"secret","nested":{"linked_token":"secret2","safe":"value"},"items":[{"api_key":"secret3"}]}`)))
	for _, secret := range []string{"owner_token", "linked_token", "api_key", "secret"} {
		if strings.Contains(got, secret) {
			t.Errorf("credential %q survived in %s", secret, got)
		}
	}
	if !strings.Contains(got, `"safe":"value"`) {
		t.Errorf("non-credential response data was lost: %s", got)
	}
}

func TestToolCallDispatchesInProcessWithAuthOnly(t *testing.T) {
	h := newTestHandler()
	rec := rpc(t, h, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"getApiV1PastesId","arguments":{"id":"abc/../x"}}}`,
		map[string]string{"Authorization": "Bearer test-token", "Cookie": "owner=secret"})
	res := decode(t, rec)["result"].(map[string]any)
	if res["isError"] != false {
		t.Fatalf("isError = %v: %v", res["isError"], res)
	}
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	var echoed map[string]string
	if err := json.Unmarshal([]byte(text), &echoed); err != nil {
		t.Fatalf("tool text not JSON: %v (%s)", err, text)
	}
	if echoed["method"] != "GET" {
		t.Errorf("method = %s, want GET", echoed["method"])
	}
	if echoed["path"] != "/api/v1/pastes/abc%2F..%2Fx" {
		t.Errorf("path argument was not escaped: %s", echoed["path"])
	}
	if echoed["auth"] != "Bearer test-token" {
		t.Errorf("Authorization not forwarded: %q", echoed["auth"])
	}
	if echoed["cookie"] != "" {
		t.Errorf("cookies must never be forwarded, got %q", echoed["cookie"])
	}
}

func TestListToolPassesOwnedScopeAndBearerToken(t *testing.T) {
	rec := rpc(t, newTestHandler(), `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"getApiV1Pastes","arguments":{"scope":"owned"}}}`,
		map[string]string{"Authorization": "Bearer test-token"})
	result := decode(t, rec)["result"].(map[string]any)
	content := result["content"].([]any)[0].(map[string]any)
	var echoed map[string]string
	if err := json.Unmarshal([]byte(content["text"].(string)), &echoed); err != nil {
		t.Fatalf("tool response is not JSON: %v", err)
	}
	if echoed["query"] != "scope=owned" {
		t.Errorf("query = %q, want scope=owned", echoed["query"])
	}
	if echoed["auth"] != "Bearer test-token" {
		t.Errorf("Authorization = %q, want Bearer token", echoed["auth"])
	}
}

func TestBearerAndRequiredHeaders(t *testing.T) {
	h := newTestHandler()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}`
	request := func(headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := request(map[string]string{"MCP-Protocol-Version": "2026-07-28", "Mcp-Method": "tools/list"}); rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("unauthenticated status=%d challenge=%q, want 401 Bearer", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
	if rec := request(map[string]string{"Authorization": "Bearer test-token", "Mcp-Method": "tools/list"}); rec.Code != http.StatusBadRequest {
		t.Errorf("missing version header status=%d, want 400", rec.Code)
	}
	if rec := request(map[string]string{"Authorization": "Bearer test-token", "MCP-Protocol-Version": "2026-07-28"}); rec.Code != http.StatusBadRequest {
		t.Errorf("missing method header status=%d, want 400", rec.Code)
	}
}

func TestMCPNameBase64Encoding(t *testing.T) {
	if got, ok := decodeMCPName("=?base64?5pel5pys6Kqe?="); !ok || got != "日本語" {
		t.Errorf("decodeMCPName() = %q, %v", got, ok)
	}
	for _, bad := range []string{"", "=?base64?not-base64?=", "token\nvalue"} {
		if _, ok := decodeMCPName(bad); ok {
			t.Errorf("decodeMCPName(%q) unexpectedly accepted", bad)
		}
	}
}

func TestToolCallBodyAndErrors(t *testing.T) {
	h := newTestHandler()
	got := decode(t, rpc(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"postApiV1Pastes","arguments":{"content":"hello"}}}`, nil))
	text := got["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	var echoed map[string]string
	if err := json.Unmarshal([]byte(text), &echoed); err != nil {
		t.Fatalf("tool text not JSON: %v", err)
	}
	if echoed["method"] != "POST" || !strings.Contains(echoed["body"], `"content":"hello"`) {
		t.Errorf("POST body not built from arguments: %v", echoed)
	}

	got = decode(t, rpc(t, h, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"getApiV1PastesId","arguments":{"id":"missing"}}}`, nil))
	if got["result"].(map[string]any)["isError"] != true {
		t.Errorf("a 404 from the API must map to isError: %v", got)
	}

	got = decode(t, rpc(t, h, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"noSuchTool"}}`, nil))
	if got["error"] == nil {
		t.Errorf("unknown tool must be a JSON-RPC error: %v", got)
	}
}

func TestOriginAndHeaderChecks(t *testing.T) {
	h := newTestHandler()
	ping := `{"jsonrpc":"2.0","id":1,"method":"ping"}`

	if rec := rpc(t, h, ping, map[string]string{"Origin": "http://evil.example"}); rec.Code != http.StatusForbidden {
		t.Errorf("bad Origin status = %d, want 403", rec.Code)
	}
	if rec := rpc(t, h, ping, map[string]string{"Origin": "http://example.com"}); rec.Code != http.StatusOK {
		t.Errorf("good Origin status = %d, want 200", rec.Code)
	}
	rec := rpc(t, h, ping, map[string]string{"MCP-Protocol-Version": "1999-01-01"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad protocol version status = %d, want 400", rec.Code)
	}
	data := decode(t, rec)["error"].(map[string]any)["data"].(map[string]any)
	if len(data["supported"].([]any)) != len(supportedVersions) {
		t.Errorf("error must list supported versions, got %v", data)
	}
	if rec := rpc(t, h, ping, map[string]string{"Mcp-Method": "tools/list"}); rec.Code != http.StatusBadRequest {
		t.Errorf("Mcp-Method mismatch status = %d, want 400", rec.Code)
	}
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"getApiV1Pastes"}}`
	if rec := rpc(t, h, call, map[string]string{"Mcp-Name": "other"}); rec.Code != http.StatusBadRequest {
		t.Errorf("Mcp-Name mismatch status = %d, want 400", rec.Code)
	}
}

func TestFromContextMarksDispatchedRequests(t *testing.T) {
	var seen bool
	h := New(Config{
		APIVersion: "v1",
		Dispatch: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = FromContext(r.Context())
		}),
	})
	rpc(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"getApiV1Pastes"}}`, nil)
	if !seen {
		t.Error("dispatched request not marked as MCP-sourced")
	}
	if FromContext(httptest.NewRequest(http.MethodGet, "/", nil).Context()) {
		t.Error("ordinary request wrongly marked as MCP-sourced")
	}
}

func TestOperatorOnlyToolsHiddenFromOthers(t *testing.T) {
	h := newTestHandler()
	h.tools = append(h.tools, tool{name: "adminThing", method: "GET", path: "/api/v1/admin", operatorOnly: true})

	list := func() map[string]bool {
		got := decode(t, rpc(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil))
		out := map[string]bool{}
		for _, raw := range got["result"].(map[string]any)["tools"].([]any) {
			out[raw.(map[string]any)["name"].(string)] = true
		}
		return out
	}
	if list()["adminThing"] {
		t.Error("operator-only tool listed for a non-operator")
	}
	call := decode(t, rpc(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"adminThing"}}`, nil))
	if call["error"] == nil {
		t.Error("operator-only tool callable by a non-operator")
	}

	h.cfg.IsOperator = func(*http.Request) bool { return true }
	if !list()["adminThing"] {
		t.Error("operator-only tool missing for the operator")
	}
}

// TestDispatchRoutesFreshThroughChi guards against the outer request's chi
// route context leaking into the dispatched request: with the context kept,
// chi resumes the /mcp match and never reaches the tool's own route.
func TestDispatchRoutesFreshThroughChi(t *testing.T) {
	r := chi.NewRouter()
	h := New(Config{APIVersion: "v1", Dispatch: r})
	r.Handle("/mcp", h)
	r.Get("/api/v1/pastes/{id}", func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write([]byte("paste:" + chi.URLParam(req, "id")))
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"},"params":{"name":"getApiV1PastesId","arguments":{"id":"abc123"}}}`))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "getApiV1PastesId")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	res := decode(t, rec)["result"].(map[string]any)
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if text != "paste:abc123" || res["isError"] != false {
		t.Errorf("tool result = %q isError=%v, want paste:abc123", text, res["isError"])
	}
}
