package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFlashRoundTrip verifies a flash key set on a redirect is rendered once
// (translated) and then cleared, and that unknown keys never render.
func TestFlashRoundTrip(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/server/preferences", nil)
	setFlash(rec, req, "preferences_saved")
	setFlash(rec, req, "not_allowed")
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != flashCookieName || cookies[0].Value != "preferences_saved" {
		t.Fatalf("setFlash cookies = %+v, want exactly the preferences_saved flash cookie", cookies)
	}

	next := httptest.NewRequest(http.MethodGet, "/server/preferences", nil)
	next.AddCookie(cookies[0])
	out := httptest.NewRecorder()
	fv := takeFlash(out, next, "en")
	if fv == nil || fv.Kind != "success" || fv.Message == "" {
		t.Fatalf("takeFlash = %+v, want success flash with message", fv)
	}
	cleared := out.Result().Cookies()
	if len(cleared) != 1 || cleared[0].MaxAge >= 0 {
		t.Errorf("takeFlash must expire the flash cookie, got %+v", cleared)
	}

	forged := httptest.NewRequest(http.MethodGet, "/", nil)
	forged.AddCookie(&http.Cookie{Name: flashCookieName, Value: "<script>"})
	if fv := takeFlash(httptest.NewRecorder(), forged, "en"); fv != nil {
		t.Errorf("forged flash key rendered: %+v", fv)
	}
	if fv := takeFlash(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "en"); fv != nil {
		t.Errorf("no cookie must yield no flash, got %+v", fv)
	}
}

// TestRefererIsPreferencesPage covers the path-suffix check that limits the
// save confirmation to the preferences page.
func TestRefererIsPreferencesPage(t *testing.T) {
	cases := map[string]bool{
		"":                                       false,
		"https://x.test/server/preferences":      true,
		"https://x.test/base/server/preferences": true,
		"https://x.test/server/preferences/":     true,
		"https://x.test/create":                  false,
	}
	for ref, want := range cases {
		r := httptest.NewRequest(http.MethodPost, "/server/preferences", nil)
		if ref != "" {
			r.Header.Set("Referer", ref)
		}
		if got := refererIsPreferencesPage(r); got != want {
			t.Errorf("refererIsPreferencesPage(%q) = %v, want %v", ref, got, want)
		}
	}
}
