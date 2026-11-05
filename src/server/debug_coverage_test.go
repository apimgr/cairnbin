package server

// Tests for debug.go — isSensitiveKey, redactMap, and all handleDebug* handlers.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/config"
)

// ─── isSensitiveKey ───────────────────────────────────────────────────────────

func TestIsSensitiveKey(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"password", true},
		{"PASSWORD", true},
		{"smtp_pass", true},
		{"api_key", true},
		{"apikey", true},
		{"privatekey", true},
		{"private_key", true},
		{"encryption_key", true},
		{"encryptionkey", true},
		{"secret", true},
		{"token", true},
		{"webhook_url", true},
		{"dsn", true},
		{"credential", true},
		{"host", false},
		{"port", false},
		{"address", false},
		{"mode", false},
		{"title", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			got := isSensitiveKey(tc.key)
			if got != tc.want {
				t.Errorf("isSensitiveKey(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

// ─── redactMap ────────────────────────────────────────────────────────────────

func TestRedactMap_RedactsSensitiveKeys(t *testing.T) {
	input := map[string]any{
		"host":     "localhost",
		"password": "supersecret",
		"token":    "abc123",
	}
	result := redactMap(input).(map[string]any)
	if result["host"] != "localhost" {
		t.Errorf("non-sensitive key changed: host = %v", result["host"])
	}
	if result["password"] != redactValue {
		t.Errorf("password not redacted: got %v", result["password"])
	}
	if result["token"] != redactValue {
		t.Errorf("token not redacted: got %v", result["token"])
	}
}

func TestRedactMap_EmptyValueNotRedacted(t *testing.T) {
	input := map[string]any{
		"password": "",
	}
	result := redactMap(input).(map[string]any)
	if result["password"] != "" {
		t.Errorf("empty password should not be redacted, got %v", result["password"])
	}
}

func TestRedactMap_NestedMap(t *testing.T) {
	input := map[string]any{
		"smtp": map[string]any{
			"host": "mail.example.com",
			"pass": "secret123",
		},
	}
	result := redactMap(input).(map[string]any)
	smtp := result["smtp"].(map[string]any)
	if smtp["host"] != "mail.example.com" {
		t.Errorf("nested host changed: %v", smtp["host"])
	}
	if smtp["pass"] != redactValue {
		t.Errorf("nested pass not redacted: %v", smtp["pass"])
	}
}

func TestRedactMap_SliceOfMaps(t *testing.T) {
	input := map[string]any{
		"servers": []any{
			map[string]any{"token": "abc", "host": "h1"},
		},
	}
	result := redactMap(input).(map[string]any)
	servers := result["servers"].([]any)
	srv := servers[0].(map[string]any)
	if srv["token"] != redactValue {
		t.Errorf("token in slice not redacted: %v", srv["token"])
	}
	if srv["host"] != "h1" {
		t.Errorf("host in slice changed: %v", srv["host"])
	}
}

func TestRedactMap_NonMapScalar(t *testing.T) {
	got := redactMap("just a string")
	if got != "just a string" {
		t.Errorf("scalar should be returned unchanged, got %v", got)
	}
}

// ─── handleDebugConfig ────────────────────────────────────────────────────────

func TestHandleDebugConfig(t *testing.T) {
	s := &Server{cfg: config.DefaultConfig()}
	r := httptest.NewRequest(http.MethodGet, "/debug/config", nil)
	w := httptest.NewRecorder()
	s.handleDebugConfig(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if body["ok"] != true {
		t.Errorf("ok = %v, want true", body["ok"])
	}
	if body["data"] == nil {
		t.Error("data field missing")
	}
}

// ─── handleDebugCache ─────────────────────────────────────────────────────────

func TestHandleDebugCache_NilStore(t *testing.T) {
	s := &Server{cfg: config.DefaultConfig(), cacheStore: nil}
	r := httptest.NewRequest(http.MethodGet, "/debug/cache", nil)
	w := httptest.NewRecorder()
	s.handleDebugCache(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	data := body["data"].(map[string]any)
	if data["status"] != "disabled" {
		t.Errorf("status = %v, want disabled", data["status"])
	}
}

// ─── handleDebugDB ────────────────────────────────────────────────────────────

func TestHandleDebugDB_PingOK(t *testing.T) {
	db := &stubDB{pingErr: nil, pastesCount: 5}
	s := newServerWithDB(config.DefaultConfig(), db)
	r := httptest.NewRequest(http.MethodGet, "/debug/db", nil)
	w := httptest.NewRecorder()
	s.handleDebugDB(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	data := body["data"].(map[string]any)
	if data["status"] != "ok" {
		t.Errorf("status = %v, want ok", data["status"])
	}
	if count, ok := data["paste_count"].(float64); !ok || count != 5 {
		t.Errorf("paste_count = %v, want 5", data["paste_count"])
	}
}

func TestHandleDebugDB_PingError(t *testing.T) {
	db := &stubDB{pingErr: errDebugStub}
	s := newServerWithDB(config.DefaultConfig(), db)
	r := httptest.NewRequest(http.MethodGet, "/debug/db", nil)
	w := httptest.NewRecorder()
	s.handleDebugDB(w, r)

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	data := body["data"].(map[string]any)
	if data["status"] != "error" {
		t.Errorf("status = %v, want error", data["status"])
	}
}

func TestHandleDebugDB_CountError(t *testing.T) {
	db := &stubDBWithCountErr{stubDB: stubDB{pingErr: nil}, countErr: errDebugStub}
	s := &Server{cfg: config.DefaultConfig(), db: db, startTime: time.Now()}
	r := httptest.NewRequest(http.MethodGet, "/debug/db", nil)
	w := httptest.NewRecorder()
	s.handleDebugDB(w, r)

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	data := body["data"].(map[string]any)
	if count, ok := data["paste_count"].(float64); !ok || count != -1 {
		t.Errorf("paste_count = %v, want -1 on CountPastes error", data["paste_count"])
	}
}

// ─── handleDebugMemory ────────────────────────────────────────────────────────

func TestHandleDebugMemory(t *testing.T) {
	s := &Server{cfg: config.DefaultConfig()}
	r := httptest.NewRequest(http.MethodGet, "/debug/memory", nil)
	w := httptest.NewRecorder()
	s.handleDebugMemory(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	data := body["data"].(map[string]any)
	if _, ok := data["alloc_mb"]; !ok {
		t.Error("alloc_mb field missing")
	}
	if _, ok := data["goroutines"]; !ok {
		t.Error("goroutines field missing")
	}
}

// ─── handleDebugGoroutines ────────────────────────────────────────────────────

func TestHandleDebugGoroutines(t *testing.T) {
	s := &Server{cfg: config.DefaultConfig()}
	r := httptest.NewRequest(http.MethodGet, "/debug/goroutines", nil)
	w := httptest.NewRecorder()
	s.handleDebugGoroutines(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "goroutine") {
		t.Errorf("goroutine dump missing 'goroutine' keyword")
	}
}

// ─── handleDebugScheduler ─────────────────────────────────────────────────────

func TestHandleDebugScheduler_NoScheduler(t *testing.T) {
	s := &Server{cfg: config.DefaultConfig(), schedulerAPI: nil}
	r := httptest.NewRequest(http.MethodGet, "/debug/scheduler", nil)
	w := httptest.NewRecorder()
	s.handleDebugScheduler(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// errDebugStub is a local sentinel error for debug tests.
var errDebugStub = debugStubError("debug test error")

type debugStubError string

func (e debugStubError) Error() string { return string(e) }

// stubDBWithCountErr wraps stubDB and returns an error from CountPastes.
type stubDBWithCountErr struct {
	stubDB
	countErr error
}

func (d *stubDBWithCountErr) CountPastes() (int64, error) { return 0, d.countErr }
