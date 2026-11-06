package server

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/apimgr/cairnbin/src/common/i18n"
)

// flashCookieName is the short-lived cookie carrying a one-shot flash message
// key across the POST -> redirect -> GET cycle (PART 16 Toast Notifications,
// "No-JS fallback").
const flashCookieName = "flash"

// flashMessages is the closed allow-list of flash keys the server will render.
// The cookie only ever carries a key from this set, never message text, so a
// forged cookie cannot inject content.
var flashMessages = map[string]struct {
	// i18nKey is the translation key rendered for the message.
	i18nKey string
	// kind is the visual/ARIA kind: success, info, warning or error.
	kind string
}{
	"preferences_saved": {i18nKey: "preferences.saved", kind: "success"},
}

// flashView is the template-facing flash message.
type flashView struct {
	// Kind is the banner kind (success, info, warning, error).
	Kind string
	// Message is the already-translated message text.
	Message string
}

// setFlash stores a flash key for the next page render. Keys outside
// flashMessages are ignored.
func setFlash(w http.ResponseWriter, r *http.Request, key string) {
	if _, ok := flashMessages[key]; !ok {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    key,
		Path:     "/",
		MaxAge:   60,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
}

// takeFlash returns the pending flash message (if any) for this request and
// clears the cookie so it renders exactly once. It must run before the
// response headers are written.
func takeFlash(w http.ResponseWriter, r *http.Request, lang string) *flashView {
	c, err := r.Cookie(flashCookieName)
	if err != nil {
		return nil
	}
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
	msg, ok := flashMessages[c.Value]
	if !ok {
		return nil
	}
	return &flashView{Kind: msg.kind, Message: i18n.Translate(lang, msg.i18nKey)}
}

// refererIsPreferencesPage reports whether the request came from the
// preferences page, the only place a save confirmation flash is wanted (the
// header theme toggle on other pages stays silent).
func refererIsPreferencesPage(r *http.Request) bool {
	ref := r.Referer()
	if ref == "" {
		return false
	}
	u, err := url.Parse(ref)
	if err != nil {
		return false
	}
	return strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/server/preferences")
}
