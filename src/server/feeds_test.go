package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/config"
)

func TestFeedRoutesServeValidEmptyFeedsAndValidators(t *testing.T) {
	s := New(&stubDB{}, config.DefaultConfig(), nil, "1.0.0", "abc", "now", t.TempDir(), t.TempDir())
	for _, path := range []string{"/feed.xml", "/rss.xml"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Host = "feed.example.test"
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "xml") || !strings.Contains(w.Header().Get("Cache-Control"), "max-age=300") || w.Header().Get("ETag") == "" || w.Header().Get("Last-Modified") == "" {
			t.Fatalf("%s headers/status incomplete: %d %#v", path, w.Code, w.Header())
		}
		if !strings.Contains(w.Body.String(), "feed.example.test") {
			t.Errorf("%s URLs are not absolute: %s", path, w.Body.String())
		}
		etag := w.Header().Get("ETag")
		conditional := httptest.NewRequest("GET", path, nil)
		conditional.Host = "feed.example.test"
		conditional.Header.Set("If-None-Match", etag)
		cw := httptest.NewRecorder()
		s.router.ServeHTTP(cw, conditional)
		if cw.Code != 304 || cw.Body.Len() != 0 {
			t.Errorf("%s conditional response=%d body=%q", path, cw.Code, cw.Body.String())
		}
		head := httptest.NewRequest("HEAD", path, nil)
		head.Host = "feed.example.test"
		hw := httptest.NewRecorder()
		s.router.ServeHTTP(hw, head)
		if hw.Code != 200 || hw.Body.Len() != 0 {
			t.Errorf("%s HEAD response=%d body=%q", path, hw.Code, hw.Body.String())
		}
	}
}

func TestFeedTextAndRobotsPolicy(t *testing.T) {
	if got := feedPlain(" <b>alpha &amp; beta</b>\x00 "); got != "alpha & beta" {
		t.Fatalf("feedPlain = %q", got)
	}
	if !robotsAllows([]string{"/"}, []string{"/api", "/server/admin"}, "/server/about") {
		t.Fatal("public route denied unexpectedly")
	}
	if robotsAllows([]string{"/"}, []string{"/server"}, "/server/contact") {
		t.Fatal("robots deny rule ignored")
	}
}

func TestPublicFeedWithoutDatabaseIsEmptyAndValid(t *testing.T) {
	s := &Server{cfg: config.DefaultConfig()}
	entries, updated, title := s.publicFeedItems(httptest.NewRequest("GET", "/feed.xml", nil))
	if len(entries) != 0 || updated.IsZero() || title == "" {
		t.Fatalf("empty feed = entries:%d updated:%v title:%q", len(entries), updated, title)
	}
}
