package httputil

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func req(ua string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if ua != "" {
		r.Header.Set("User-Agent", ua)
	}
	return r
}

func TestIsOurCliClient(t *testing.T) {
	cases := []struct {
		ua   string
		want bool
	}{
		{"cairnbin-cli/1.0.0", true},
		{"cairnbin-cli/0.0.1", true},
		{"curl/7.68.0", false},
		{"Mozilla/5.0", false},
		{"lynx/2.9.0", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsOurCliClient(req(tc.ua)); got != tc.want {
			t.Errorf("IsOurCliClient(%q) = %v, want %v", tc.ua, got, tc.want)
		}
	}
}

func TestIsTextBrowser(t *testing.T) {
	cases := []struct {
		ua   string
		want bool
	}{
		{"Lynx/2.9.0dev.10 libwww-FM/2.14", true},
		{"w3m/0.5.3", true},
		// "links " with space
		{"Links (2.28; Linux)", true},
		// "links/"
		{"Links/2.28 (Linux)", true},
		{"ELinks/0.13.2 (Linux)", true},
		{"Browsh/1.6.4", true},
		{"Carbonyl/0.0.3", true},
		{"NetSurf/3.10", true},
		{"Mozilla/5.0 (X11; Linux)", false},
		{"curl/7.68.0", false},
		{"cairnbin-cli/1.0", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsTextBrowser(req(tc.ua)); got != tc.want {
			t.Errorf("IsTextBrowser(%q) = %v, want %v", tc.ua, got, tc.want)
		}
	}
}

func TestIsHttpTool(t *testing.T) {
	cases := []struct {
		ua   string
		want bool
	}{
		{"curl/7.68.0", true},
		{"Wget/1.21", true},
		{"HTTPie/3.2.0", true},
		{"libcurl/7.68.0 OpenSSL/1.1.1f", true},
		{"python-requests/2.25.1", true},
		{"Go-http-client/1.1", true},
		{"axios/0.21.1", true},
		{"node-fetch/2.6.1", true},
		// no UA = non-interactive
		{"", true},
		{"Mozilla/5.0", false},
		{"cairnbin-cli/1.0", false},
		{"Lynx/2.9.0", false},
	}
	for _, tc := range cases {
		if got := IsHttpTool(req(tc.ua)); got != tc.want {
			t.Errorf("IsHttpTool(%q) = %v, want %v", tc.ua, got, tc.want)
		}
	}
}

func TestGetAPIResponseFormat(t *testing.T) {
	cases := []struct {
		ua   string
		path string
		want string
	}{
		// .txt extension always → text
		{"Mozilla/5.0", "/api/v1/pastes.txt", "text"},
		{"curl/7.68.0", "/api/v1/pastes.txt", "text"},
		// HTTP tools → text
		{"curl/7.68.0", "/api/v1/pastes", "text"},
		{"Wget/1.21", "/api/v1/pastes", "text"},
		{"", "/api/v1/pastes", "text"},
		// Our client → JSON (INTERACTIVE)
		{"cairnbin-cli/1.0", "/api/v1/pastes", "json"},
		// Text browser → JSON (INTERACTIVE)
		{"Lynx/2.9.0", "/api/v1/pastes", "json"},
		// Regular browser → JSON
		{"Mozilla/5.0", "/api/v1/pastes", "json"},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		if tc.ua != "" {
			r.Header.Set("User-Agent", tc.ua)
		}
		got := GetAPIResponseFormat(r)
		if got != tc.want {
			t.Errorf("GetAPIResponseFormat(ua=%q, path=%q) = %q, want %q", tc.ua, tc.path, got, tc.want)
		}
	}
}

func TestIsNonInteractiveClient(t *testing.T) {
	cases := []struct {
		ua   string
		want bool
	}{
		{"curl/7.68.0", true},
		{"Wget/1.21", true},
		{"", true},
		// our client is INTERACTIVE
		{"cairnbin-cli/1.0", false},
		// text browser is INTERACTIVE
		{"Lynx/2.9.0dev", false},
		// text browser is INTERACTIVE
		{"w3m/0.5.3", false},
		// full browser is INTERACTIVE
		{"Mozilla/5.0", false},
	}
	for _, tc := range cases {
		if got := IsNonInteractiveClient(req(tc.ua)); got != tc.want {
			t.Errorf("IsNonInteractiveClient(%q) = %v, want %v", tc.ua, got, tc.want)
		}
	}
}

// TestWithTxtExtension_SetsContextFlag verifies that WithTxtExtension marks the
// request context so that GetAPIResponseFormat returns "text" even when the URL
// has no .txt suffix (covers the hasTxtExtension(r) true-return path).
func TestWithTxtExtension_SetsContextFlag(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/pastes", nil)
	r.Header.Set("User-Agent", "Mozilla/5.0")
	r2 := WithTxtExtension(r)
	got := GetAPIResponseFormat(r2)
	if got != "text" {
		t.Errorf("GetAPIResponseFormat with TxtExtension context = %q, want %q", got, "text")
	}
}

// TestGetAPIResponseFormat_AcceptJSON verifies explicit JSON negotiation takes
// precedence over client detection, while API requests without an explicit
// format continue to use the client type.
func TestGetAPIResponseFormat_AcceptJSON(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/pastes", nil)
	r.Header.Set("Accept", "application/json")
	r.Header.Set("User-Agent", "curl/7.68.0")
	got := GetAPIResponseFormat(r)
	if got != "json" {
		t.Errorf("Accept: application/json (curl): got %q, want %q", got, "json")
	}

	r2 := httptest.NewRequest(http.MethodGet, "/api/v1/pastes", nil)
	r2.Header.Set("Accept", "application/json")
	r2.Header.Set("User-Agent", "Mozilla/5.0")
	got2 := GetAPIResponseFormat(r2)
	if got2 != "json" {
		t.Errorf("Accept: application/json (browser): got %q, want %q", got2, "json")
	}
}

// TestGetAPIResponseFormat_AcceptTextPlain covers the Accept: text/plain branch
// which forces plain text even for interactive browsers.
func TestGetAPIResponseFormat_AcceptTextPlain(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/pastes", nil)
	r.Header.Set("Accept", "text/plain")
	r.Header.Set("User-Agent", "Mozilla/5.0")
	got := GetAPIResponseFormat(r)
	if got != "text" {
		t.Errorf("Accept: text/plain: got %q, want %q", got, "text")
	}
}
