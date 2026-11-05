// Package mcp implements the Model Context Protocol endpoint (AI.md PART 11,
// "MCP Endpoint"): a Streamable HTTP, JSON-RPC 2.0 front door onto the existing
// REST API. It adds no authentication of its own. Every tool call is
// dispatched in-process into the server's router with the caller's
// Authorization header, so validation, rate limits, audit and ownership checks
// are exactly those of the matching API endpoint.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/apimgr/cairnbin/src/swagger"
)

// maxRequestBytes bounds one JSON-RPC request body.
const maxRequestBytes = 10 << 20

// supportedVersions lists the MCP protocol revisions this server negotiates,
// newest first.
var supportedVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// JSON-RPC 2.0 error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeHeaderMismatch = -32001
)

type ctxKey struct{}

// FromContext reports whether the request was dispatched by the MCP endpoint,
// so audit entries can record `source: mcp`.
func FromContext(ctx context.Context) bool {
	v, _ := ctx.Value(ctxKey{}).(bool)
	return v
}

// Config wires the endpoint to the rest of the server.
type Config struct {
	// APIVersion is the {api_version} the tools are generated for.
	APIVersion string
	// ServerName and ServerVersion populate serverInfo.
	ServerName    string
	ServerVersion string
	// Dispatch is the server's full router; tool calls run through it.
	Dispatch http.Handler
	// Enabled reports server.mcp.enabled, read per request so a config reload
	// takes effect without restart.
	Enabled func() bool
	// OriginAllowed validates a present Origin header (DNS-rebinding guard).
	OriginAllowed func(r *http.Request) bool
	// IsOperator reports whether the request carries the operator token.
	IsOperator func(r *http.Request) bool
}

// Handler serves the MCP endpoint.
type Handler struct {
	cfg   Config
	tools []tool
}

// tool is one MCP tool generated from an API route annotation.
type tool struct {
	name         string
	description  string
	method       string
	path         string
	params       []swagger.Param
	bodyProps    map[string]interface{}
	bodyRequired []string
	operatorOnly bool
}

// New builds the handler and its tool list from the swagger route annotations.
func New(cfg Config) *Handler {
	h := &Handler{cfg: cfg}
	h.tools = buildTools(cfg.APIVersion)
	return h
}

// excluded reports routes that are never exposed as tools: documentation
// endpoints, compatibility shims, and token management (which lives only on
// /server/preferences, PART 11).
func excluded(r swagger.Route, apiBase string) bool {
	if r.Tag == "compatibility" || !strings.HasPrefix(r.Path, apiBase+"/") {
		return true
	}
	rest := strings.TrimPrefix(r.Path, apiBase)
	switch {
	case strings.HasPrefix(rest, "/server/swagger"),
		strings.HasPrefix(rest, "/server/graphql"),
		strings.HasPrefix(rest, "/server/mcp"),
		strings.HasPrefix(rest, "/server/preferences"):
		return true
	}
	return false
}

// toolName is the OpenAPI operationId: method + path segments in camelCase.
func toolName(method, path string) string {
	out := strings.ToLower(method)
	for _, p := range strings.Split(strings.Trim(path, "/"), "/") {
		p = strings.Trim(p, "{}")
		if p == "" {
			continue
		}
		out += strings.ToUpper(p[:1]) + p[1:]
	}
	return out
}

func buildTools(apiVersion string) []tool {
	apiBase := "/api/" + apiVersion
	var tools []tool
	for _, r := range swagger.Routes(apiVersion) {
		if excluded(r, apiBase) {
			continue
		}
		t := tool{
			name:        toolName(r.Method, r.Path),
			description: strings.TrimSpace(r.Summary + ". " + r.Description),
			method:      r.Method,
			path:        r.Path,
			params:      r.Params,
		}
		if r.Body != nil {
			if props, ok := r.Body.Schema["properties"].(map[string]interface{}); ok {
				t.bodyProps = props
			}
			if req, ok := r.Body.Schema["required"].([]string); ok {
				t.bodyRequired = req
			}
		}
		tools = append(tools, t)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].name < tools[j].name })
	return tools
}

// inputSchema builds the JSON Schema for a tool's arguments.
func (t tool) inputSchema() map[string]interface{} {
	props := map[string]interface{}{}
	required := []string{}
	for _, p := range t.params {
		if p.In != "path" && p.In != "query" {
			continue
		}
		s := map[string]interface{}{}
		for k, v := range p.Schema {
			s[k] = v
		}
		if p.Description != "" {
			s["description"] = p.Description
		}
		props[p.Name] = s
		if p.Required {
			required = append(required, p.Name)
		}
	}
	for k, v := range t.bodyProps {
		props[k] = v
	}
	required = append(required, t.bodyRequired...)
	schema := map[string]interface{}{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// rpcRequest is one JSON-RPC 2.0 message.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (h *Handler) enabled() bool { return h.cfg.Enabled == nil || h.cfg.Enabled() }

// ServeHTTP handles POST (JSON-RPC); GET and DELETE are 405 because the server
// keeps no sessions and opens no standalone stream.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.enabled() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{
			"ok": false, "error": "METHOD_NOT_ALLOWED", "message": "Method not allowed",
		})
		return
	}
	if r.Header.Get("Origin") != "" && h.cfg.OriginAllowed != nil && !h.cfg.OriginAllowed(r) {
		writeJSON(w, http.StatusForbidden, map[string]interface{}{
			"ok": false, "error": "FORBIDDEN", "message": "Permission denied",
		})
		return
	}
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !versionSupported(v) {
		writeRPCError(w, http.StatusBadRequest, nil, codeInvalidRequest, "Unsupported protocol version",
			map[string]interface{}{"supported": supportedVersions, "requested": v})
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		writeRPCError(w, http.StatusBadRequest, nil, codeParseError, "could not read request body", nil)
		return
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var batch []rpcRequest
		if err := json.Unmarshal(trimmed, &batch); err != nil {
			writeRPCError(w, http.StatusBadRequest, nil, codeParseError, "parse error", nil)
			return
		}
		var out []rpcResponse
		for _, req := range batch {
			if resp, ok := h.handle(r, req); ok {
				out = append(out, resp)
			}
		}
		if len(out) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}

	var req rpcRequest
	if err := json.Unmarshal(trimmed, &req); err != nil {
		writeRPCError(w, http.StatusBadRequest, nil, codeParseError, "parse error", nil)
		return
	}
	if hm := r.Header.Get("Mcp-Method"); hm != "" && hm != req.Method {
		writeRPCError(w, http.StatusBadRequest, req.ID, codeHeaderMismatch, "HeaderMismatch: Mcp-Method does not match the request body", nil)
		return
	}
	if hn := r.Header.Get("Mcp-Name"); hn != "" && req.Method == "tools/call" {
		var p struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.Name != hn {
			writeRPCError(w, http.StatusBadRequest, req.ID, codeHeaderMismatch, "HeaderMismatch: Mcp-Name does not match the tool name", nil)
			return
		}
	}
	resp, ok := h.handle(r, req)
	if !ok {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func versionSupported(v string) bool {
	for _, s := range supportedVersions {
		if s == v {
			return true
		}
	}
	return false
}

// handle runs one JSON-RPC message. The bool is false for notifications, which
// get no response.
func (h *Handler) handle(r *http.Request, req rpcRequest) (rpcResponse, bool) {
	isNotification := len(req.ID) == 0
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	if req.JSONRPC != "2.0" {
		resp.Error = &rpcError{Code: codeInvalidRequest, Message: "jsonrpc must be \"2.0\""}
		return resp, !isNotification
	}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := supportedVersions[0]
		if versionSupported(p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		resp.Result = map[string]interface{}{
			"protocolVersion": version,
			"capabilities":    map[string]interface{}{"tools": map[string]interface{}{"listChanged": false}},
			"serverInfo":      map[string]interface{}{"name": h.cfg.ServerName, "version": h.cfg.ServerVersion},
		}
	case "ping":
		resp.Result = map[string]interface{}{}
	case "tools/list":
		resp.Result = map[string]interface{}{"tools": h.listTools(r)}
	case "tools/call":
		result, rerr := h.callTool(r, req.Params)
		if rerr != nil {
			resp.Error = rerr
		} else {
			resp.Result = result
		}
	default:
		if strings.HasPrefix(req.Method, "notifications/") || isNotification {
			return resp, false
		}
		resp.Error = &rpcError{Code: codeMethodNotFound, Message: "method not found"}
	}
	return resp, !isNotification
}

// visible reports whether the caller may see/use a tool.
func (h *Handler) visible(r *http.Request, t tool) bool {
	return !t.operatorOnly || (h.cfg.IsOperator != nil && h.cfg.IsOperator(r))
}

func (h *Handler) listTools(r *http.Request) []map[string]interface{} {
	out := []map[string]interface{}{}
	for _, t := range h.tools {
		if !h.visible(r, t) {
			continue
		}
		out = append(out, map[string]interface{}{
			"name":        t.name,
			"description": t.description,
			"inputSchema": t.inputSchema(),
		})
	}
	return out
}

func (h *Handler) callTool(r *http.Request, raw json.RawMessage) (interface{}, *rpcError) {
	var p struct {
		Name      string                 `json:"name"`
		Arguments map[string]interface{} `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.Name == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call requires a tool name"}
	}
	var t *tool
	for i := range h.tools {
		if h.tools[i].name == p.Name && h.visible(r, h.tools[i]) {
			t = &h.tools[i]
			break
		}
	}
	if t == nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "unknown tool"}
	}
	status, respBody := h.dispatch(r, *t, p.Arguments)
	return map[string]interface{}{
		"content": []map[string]interface{}{{"type": "text", "text": string(respBody)}},
		"isError": status >= 400,
	}, nil
}

// dispatch maps tool arguments onto an API request and runs it through the
// server's router in-process with the caller's Authorization header.
func (h *Handler) dispatch(orig *http.Request, t tool, args map[string]interface{}) (int, []byte) {
	path := t.path
	query := url.Values{}
	used := map[string]bool{}
	for _, p := range t.params {
		v, ok := args[p.Name]
		if !ok {
			continue
		}
		used[p.Name] = true
		s := stringify(v)
		switch p.In {
		case "path":
			path = strings.ReplaceAll(path, "{"+p.Name+"}", url.PathEscape(s))
		case "query":
			query.Set(p.Name, s)
		}
	}

	var payload io.Reader
	if t.method == http.MethodPost || t.method == http.MethodPut || t.method == http.MethodPatch {
		body := map[string]interface{}{}
		for k, v := range args {
			if !used[k] {
				body[k] = v
			}
		}
		b, _ := json.Marshal(body)
		payload = bytes.NewReader(b)
	}

	target := path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req := httptest.NewRequest(t.method, target, payload)
	req.Host = orig.Host
	req.RemoteAddr = orig.RemoteAddr
	req.TLS = orig.TLS
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Only the Authorization header carries identity; cookies are never
	// forwarded (no ambient authority). Proxy headers keep client-IP and URL
	// resolution identical to a direct API call.
	for _, name := range []string{"Authorization", "X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host", "X-Forwarded-Port", "X-Real-Ip", "User-Agent"} {
		if v := orig.Header.Get(name); v != "" {
			req.Header.Set(name, v)
		}
	}
	// The outer request carries chi's route context for the /mcp match; keeping
	// it would make the router resume that match instead of routing the tool's
	// own path from scratch. A typed-nil context forces a fresh routing pass.
	ctx := context.WithValue(orig.Context(), chi.RouteCtxKey, (*chi.Context)(nil))
	req = req.WithContext(context.WithValue(ctx, ctxKey{}, true))

	rec := httptest.NewRecorder()
	h.cfg.Dispatch.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func stringify(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeRPCError(w http.ResponseWriter, status int, id json.RawMessage, code int, msg string, data interface{}) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	writeJSON(w, status, rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg, Data: data}})
}
