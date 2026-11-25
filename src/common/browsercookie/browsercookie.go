package browsercookie

import "net/http"

const (
	OwnerTokenName       = "cairnbin_owner_token_karmassz"
	LegacyOwnerTokenName = "cairnbin_owner_token_rU3uW5Ze"
)

func OwnerTokenCookies(r *http.Request) []*http.Cookie {
	if r == nil {
		return nil
	}
	cookies := make([]*http.Cookie, 0, 2)
	for _, name := range []string{OwnerTokenName, LegacyOwnerTokenName} {
		cookie, err := r.Cookie(name)
		if err == nil && cookie.Value != "" {
			cookies = append(cookies, cookie)
		}
	}
	return cookies
}

func OwnerTokenValue(r *http.Request) string {
	cookies := OwnerTokenCookies(r)
	if len(cookies) == 0 {
		return ""
	}
	return cookies[0].Value
}
