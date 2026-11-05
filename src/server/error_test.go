package server

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/config"
)

func newErrorTestServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{
		cfg:       &config.Config{Web: config.WebConfig{SiteTitle: "Cairnbin", Theme: "dark"}},
		version:   "test",
		buildDate: "2026-01-01",
	}
	tmpl, err := s.buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	s.templates = tmpl
	return s
}

func TestErrorCodeForStatus(t *testing.T) {
	cases := map[int]string{
		http.StatusBadRequest:          "BAD_REQUEST",
		http.StatusUnauthorized:        "UNAUTHORIZED",
		http.StatusForbidden:           "FORBIDDEN",
		http.StatusNotFound:            "NOT_FOUND",
		http.StatusMethodNotAllowed:    "METHOD_NOT_ALLOWED",
		http.StatusBadGateway:          "BAD_GATEWAY",
		http.StatusServiceUnavailable:  "MAINTENANCE",
		http.StatusInternalServerError: "SERVER_ERROR",
		http.StatusTeapot:              "SERVER_ERROR",
	}
	for status, want := range cases {
		if got := errorCodeForStatus(status); got != want {
			t.Errorf("status %d: got %q, want %q", status, got, want)
		}
	}
}

func TestRenderErrorPageJSON(t *testing.T) {
	s := newErrorTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	req.Header.Set("Accept", "application/json")

	s.renderErrorPage(rec, req, http.StatusNotFound, "gone")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content-type: got %q", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{`"ok": false`, `"error": "NOT_FOUND"`, `"message": "gone"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q: %s", want, body)
		}
	}
}

func TestRenderErrorPageHTML(t *testing.T) {
	s := newErrorTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	req.Header.Set("Accept", "text/html")

	s.renderErrorPage(rec, req, http.StatusNotFound, "no such paste")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("content-type: got %q", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{"404", "Not Found", "no such paste", `class="theme-dark"`, "Cairnbin"} {
		if !strings.Contains(body, want) {
			t.Errorf("html body missing %q", want)
		}
	}
}

func TestRenderErrorPageDefaultMessage(t *testing.T) {
	s := newErrorTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Accept", "application/json")

	s.renderErrorPage(rec, req, http.StatusInternalServerError, "")

	if !strings.Contains(rec.Body.String(), "Internal Server Error") {
		t.Errorf("expected default status text in body: %s", rec.Body.String())
	}
}

func TestRenderErrorPageNoTemplatesFallback(t *testing.T) {
	s := &Server{cfg: &config.Config{Web: config.WebConfig{SiteTitle: "P", Theme: "dark"}}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Accept", "text/html")

	s.renderErrorPage(rec, req, http.StatusNotFound, "boom")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("fallback body missing message: %s", rec.Body.String())
	}
}

func TestRecovererRendersThemed500(t *testing.T) {
	s := newErrorTestServer(t)
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	h := s.recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/html")

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "500") {
		t.Errorf("expected themed 500 page")
	}
}
