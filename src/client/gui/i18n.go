//go:build gui && !freebsd && !netbsd && !openbsd

package gui

import "github.com/apimgr/cairnbin/src/common/i18n"

// uiLang is the resolved GUI display language, cached once at startup.
// PART 30 requires locale files be embedded and used in ALL binaries,
// including the GUI screens of cairnbin-cli.
var uiLang = "en"

// setUILang resolves and caches the GUI display language from the client's
// configured locale, falling back to "en" for any unsupported value (never
// errors or crashes on a bad --lang/config value).
func setUILang(lang string) {
	if i18n.IsSupported(lang) {
		uiLang = lang
		return
	}
	uiLang = "en"
}

// t translates a "cli.gui.*" key using the resolved uiLang.
func t(key string) string {
	return i18n.Translate(uiLang, "cli.gui."+key)
}

// tf translates a "cli.gui.*" key, substituting {variable} placeholders
// from the given (name, value) pairs.
func tf(key string, args ...interface{}) string {
	return i18n.TranslateFormat(uiLang, "cli.gui."+key, i18n.Args(args...))
}
