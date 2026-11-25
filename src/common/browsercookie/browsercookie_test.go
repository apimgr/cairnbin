package browsercookie

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOwnerTokenCookiesPrefersCurrentName(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: LegacyOwnerTokenName, Value: "legacy"})
	request.AddCookie(&http.Cookie{Name: OwnerTokenName, Value: "current"})

	cookies := OwnerTokenCookies(request)
	if len(cookies) != 2 || cookies[0].Name != OwnerTokenName || cookies[0].Value != "current" || cookies[1].Name != LegacyOwnerTokenName || cookies[1].Value != "legacy" {
		t.Fatalf("owner-token cookie order = %#v", cookies)
	}
	if value := OwnerTokenValue(request); value != "current" {
		t.Fatalf("OwnerTokenValue = %q, want current", value)
	}
}
