package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/apimgr/pastebin/src/config"
	"github.com/apimgr/pastebin/src/mode"
)

func TestSanitizeBodyForLog(t *testing.T) {
	if got := sanitizeBodyForLog([]byte("a\nb")); got != `"a\nb"` {
		t.Errorf("control chars must be quoted, got %s", got)
	}
	if got := sanitizeBodyForLog([]byte{0xff, 0xfe}); !strings.HasPrefix(got, "[binary") {
		t.Errorf("binary body must be a marker, got %s", got)
	}
}

func TestBodyCaptureTruncatesAndRespectsGate(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.Debug.LogBodies = true
	cfg.Server.Debug.MaxBodyLogSize = "4B"
	s := newMinimalServer(cfg)

	mode.SetDebug(false)
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("hello"))
	ww := middleware.NewWrapResponseWriter(httptest.NewRecorder(), r.ProtoMajor)
	if bc := s.newBodyCapture(ww, r); bc.on {
		t.Fatal("capture must be off when debug is off")
	}

	mode.SetDebug(true)
	defer mode.SetDebug(false)
	r = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("hello world"))
	ww = middleware.NewWrapResponseWriter(httptest.NewRecorder(), r.ProtoMajor)
	bc := s.newBodyCapture(ww, r)
	if !bc.on {
		t.Fatal("capture must be on in debug with log_bodies")
	}
	if _, err := io.ReadAll(r.Body); err != nil {
		t.Fatal(err)
	}
	_, _ = ww.Write([]byte("response body"))
	if got := bc.req.String(); got != "hell" {
		t.Errorf("request capture = %q, want hell", got)
	}
	if got := bc.resp.String(); got != "resp" {
		t.Errorf("response capture = %q, want resp", got)
	}
}
