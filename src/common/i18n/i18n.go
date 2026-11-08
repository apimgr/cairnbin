package i18n

import (
	"embed"
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
)

// localeFS embeds all locale JSON files at compile time
//
//go:embed locales/*.json
var localeFS embed.FS

// LanguageMetadata is the canonical supported-language registry entry.
type LanguageMetadata struct {
	Code             string
	NativeName       string
	Direction        string
	PluralCategories []string
	Required         bool
}

var languageRegistry = map[string]LanguageMetadata{
	"ar": {Code: "ar", NativeName: "العربية", Direction: "rtl", PluralCategories: []string{"zero", "one", "two", "few", "many", "other"}, Required: true},
	"de": {Code: "de", NativeName: "Deutsch", Direction: "ltr", PluralCategories: []string{"one", "other"}, Required: true},
	"en": {Code: "en", NativeName: "English", Direction: "ltr", PluralCategories: []string{"one", "other"}, Required: true},
	"es": {Code: "es", NativeName: "Español", Direction: "ltr", PluralCategories: []string{"one", "other"}, Required: true},
	"fr": {Code: "fr", NativeName: "Français", Direction: "ltr", PluralCategories: []string{"one", "other"}, Required: true},
	"it": {Code: "it", NativeName: "Italiano", Direction: "ltr", PluralCategories: []string{"one", "many", "other"}, Required: true},
	"ja": {Code: "ja", NativeName: "日本語", Direction: "ltr", PluralCategories: []string{"other"}},
	"zh": {Code: "zh", NativeName: "中文", Direction: "ltr", PluralCategories: []string{"other"}},
}

// SupportedCodes returns the canonical language registry in stable code order.
func SupportedCodes() []string {
	codes := make([]string, 0, len(languageRegistry))
	for code := range languageRegistry {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

// RequiredCodes returns registry languages mandated by the supported-language minimum.
func RequiredCodes() []string {
	codes := make([]string, 0, len(languageRegistry))
	for code, info := range languageRegistry {
		if info.Required {
			codes = append(codes, code)
		}
	}
	sort.Strings(codes)
	return codes
}

// LanguageInfo returns metadata for a supported language.
func LanguageInfo(code string) (LanguageMetadata, bool) {
	info, ok := languageRegistry[strings.ToLower(strings.TrimSpace(code))]
	if !ok {
		return LanguageMetadata{}, false
	}
	info.PluralCategories = append([]string(nil), info.PluralCategories...)
	return info, true
}

// Runtime settings from server.i18n (PART 30 Configuration). The zero state is
// the spec default: enabled, English default and fallback, every embedded
// language available, cookie "lang" for one year.
var (
	settingsMu  sync.RWMutex
	cfgEnabled  = true
	cfgDefault  = "en"
	cfgFallback = "en"
	cfgAvail    map[string]bool
	cfgCookie   = "lang"
	cfgMaxAge   = 365 * 24 * 60 * 60
)

// Settings carries the server.i18n values applied by Configure.
type Settings struct {
	Enabled            bool
	DefaultLanguage    string
	FallbackLanguage   string
	AvailableLanguages []string
	CookieName         string
	CookieMaxAge       int
}

// Configure applies server.i18n settings. Unsupported codes are ignored and an
// empty or fully-invalid available list means every embedded language.
func Configure(st Settings) {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	cfgEnabled = st.Enabled
	cfgDefault, cfgFallback = "en", "en"
	if d := strings.ToLower(strings.TrimSpace(st.DefaultLanguage)); IsSupported(d) {
		cfgDefault = d
	}
	if f := strings.ToLower(strings.TrimSpace(st.FallbackLanguage)); IsSupported(f) {
		cfgFallback = f
	}
	cfgAvail = nil
	for _, l := range st.AvailableLanguages {
		l = strings.ToLower(strings.TrimSpace(l))
		if IsSupported(l) {
			if cfgAvail == nil {
				cfgAvail = map[string]bool{}
			}
			cfgAvail[l] = true
		}
	}
	cfgCookie = "lang"
	if c := strings.TrimSpace(st.CookieName); c != "" {
		cfgCookie = c
	}
	cfgMaxAge = 365 * 24 * 60 * 60
	if st.CookieMaxAge > 0 {
		cfgMaxAge = st.CookieMaxAge
	}
}

// CookieName returns the language cookie name (default "lang").
func CookieName() string {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return cfgCookie
}

// CookieMaxAge returns the language cookie lifetime in seconds.
func CookieMaxAge() int {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return cfgMaxAge
}

// DefaultLanguage returns the language used when no preference is detected.
func DefaultLanguage() string {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return cfgDefault
}

// IsAvailable reports whether lang is supported AND offered by server.i18n.
func IsAvailable(lang string) bool {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if !IsSupported(lang) {
		return false
	}
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return cfgAvail == nil || cfgAvail[lang]
}

// Language describes one selectable language for the UI selector.
type Language = LanguageMetadata

// Languages lists available languages in stable registry order.
func Languages() []Language {
	codes := SupportedCodes()
	out := make([]Language, 0, len(codes))
	for _, code := range codes {
		if IsAvailable(code) {
			info, _ := LanguageInfo(code)
			out = append(out, info)
		}
	}
	return out
}

// translations holds flattened key→value maps per language
var translations = map[string]map[string]string{}

func init() {
	for lang := range languageRegistry {
		data, err := localeFS.ReadFile("locales/" + lang + ".json")
		if err != nil {
			continue
		}
		var raw map[string]interface{}
		if err := json.Unmarshal(data, &raw); err != nil {
			continue
		}
		flat := map[string]string{}
		flattenJSON("", raw, flat)
		translations[lang] = flat
	}
}

// flattenJSON converts a nested JSON object into dot-separated flat keys.
func flattenJSON(prefix string, obj map[string]interface{}, out map[string]string) {
	for k, v := range obj {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch val := v.(type) {
		case string:
			out[key] = val
		case map[string]interface{}:
			flattenJSON(key, val, out)
		}
	}
}

// JSBundle returns a JSON object string containing every "js.*" translation
// key for the given language, with the "js." prefix stripped. It is embedded
// into the page so progressive-enhancement scripts can localize the few strings
// they generate at runtime (update banner, copy feedback, loading verbs).
// Falls back to English for any missing key.
func JSBundle(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if !IsSupported(lang) {
		lang = "en"
	}
	out := map[string]string{}
	const prefix = "js."
	for k, v := range translations["en"] {
		if strings.HasPrefix(k, prefix) {
			out[k[len(prefix):]] = v
		}
	}
	for k, v := range translations[lang] {
		if strings.HasPrefix(k, prefix) {
			out[k[len(prefix):]] = v
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// IsSupported returns true if lang is a supported language code.
func IsSupported(lang string) bool {
	_, ok := languageRegistry[strings.ToLower(strings.TrimSpace(lang))]
	return ok
}

// Direction returns the text direction ("ltr" or "rtl") for the given language.
// Reads meta.direction from the locale file; falls back to "ltr".
func Direction(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if !IsSupported(lang) {
		lang = "en"
	}
	if info, ok := LanguageInfo(lang); ok {
		return info.Direction
	}
	return "ltr"
}

// Translate returns the translated string for the given key.
// If lang is unsupported, falls back to "en". If key is missing, falls back to "en" then returns key.
func Translate(lang, key string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if !IsSupported(lang) {
		lang = "en"
	}
	if val, ok := translations[lang][key]; ok {
		return val
	}
	// fallback language (server.i18n.fallback_language, English by default)
	settingsMu.RLock()
	fb := cfgFallback
	settingsMu.RUnlock()
	if val, ok := translations[fb][key]; ok {
		return val
	}
	if val, ok := translations["en"][key]; ok {
		return val
	}
	return key
}

// TranslateFormat replaces named {token} placeholders in the translated string
// with the supplied values. Interpolation is LITERAL string replacement - the
// translation is never used as a fmt format string, so a stray '%' can never
// corrupt output. Tokens without a supplied value stay visible as-is and extra
// args are ignored (PART 30 "Template Usage").
func TranslateFormat(lang, key string, args map[string]string) string {
	s := Translate(lang, key)
	for k, v := range args {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return s
}

// Args builds the token map for TranslateFormat from alternating name/value
// pairs, e.g. Args("name", user, "count", n). A trailing unpaired name is
// dropped.
func Args(kv ...interface{}) map[string]string {
	out := make(map[string]string, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		out[toString(kv[i])] = toString(kv[i+1])
	}
	return out
}

// TranslatePlural returns a pluralized translation for a count value.
// Looks up key.one, key.other, key.zero, etc. based on count.
func TranslatePlural(lang, key string, count int) string {
	form := pluralForm(lang, count)
	k := key + "." + form
	if val := Translate(lang, k); val != k {
		return strings.ReplaceAll(val, "{count}", toString(count))
	}
	// try "other" as fallback
	k = key + ".other"
	val := Translate(lang, k)
	return strings.ReplaceAll(val, "{count}", toString(count))
}

// pluralForm returns the CLDR plural category for a count in a given language.
func pluralForm(lang string, count int) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	switch lang {
	case "it":
		if count == 1 {
			return "one"
		}
		if count > 0 && count%1_000_000 == 0 {
			return "many"
		}
		return "other"
	case "ar":
		// Arabic: six CLDR plural forms
		if count == 0 {
			return "zero"
		}
		if count == 1 {
			return "one"
		}
		if count == 2 {
			return "two"
		}
		mod100 := count % 100
		mod10 := count % 10
		if mod10 >= 3 && mod10 <= 10 {
			return "few"
		}
		if mod100 >= 11 && mod100 <= 99 {
			return "many"
		}
		if mod10 == 0 || mod100 == 100 {
			return "zero"
		}
		return "other"
	case "fr":
		// French: 0 and 1 are "one"
		if count <= 1 {
			return "one"
		}
		return "other"
	case "zh", "ja":
		// No plural forms
		return "other"
	default:
		// English, Spanish, German, etc.
		if count == 1 {
			return "one"
		}
		return "other"
	}
}

// LangFromRequest extracts the preferred language from an HTTP request.
// Priority: ?lang= param → lang cookie → Accept-Language header → default.
// Unsupported or unavailable values silently fall back to the default; with
// server.i18n.enabled=false the default language is always used.
func LangFromRequest(r *http.Request) string {
	settingsMu.RLock()
	enabled, def, cookie := cfgEnabled, cfgDefault, cfgCookie
	settingsMu.RUnlock()
	if !enabled {
		return def
	}
	// 1. Query parameter
	if lang := r.URL.Query().Get("lang"); lang != "" {
		if IsAvailable(lang) {
			return strings.ToLower(strings.TrimSpace(lang))
		}
		return def
	}
	// 2. Cookie
	if c, err := r.Cookie(cookie); err == nil && c.Value != "" {
		if IsAvailable(c.Value) {
			return strings.ToLower(strings.TrimSpace(c.Value))
		}
		return def
	}
	// 3. Accept-Language header
	if accept := r.Header.Get("Accept-Language"); accept != "" {
		if lang := parseBestMatch(accept); lang != "" {
			return lang
		}
	}
	return def
}

// parseBestMatch parses an Accept-Language header and returns the best supported match.
func parseBestMatch(header string) string {
	// Accept-Language: en-US,en;q=0.9,es;q=0.8
	parts := strings.Split(header, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		// strip quality value
		if idx := strings.Index(part, ";"); idx != -1 {
			part = part[:idx]
		}
		lang := strings.ToLower(strings.TrimSpace(part))
		// Try full tag first (e.g. "en-us"), then base (e.g. "en")
		if IsAvailable(lang) {
			return lang
		}
		if idx := strings.Index(lang, "-"); idx != -1 {
			base := lang[:idx]
			if IsAvailable(base) {
				return base
			}
		}
	}
	return ""
}

// GetLanguage determines the output language from CLI flag, config, and env vars.
// Never errors; silently falls back to "en" for unsupported languages.
//
// configLang is the language recorded in the config file (server.yml / cli.yml);
// pass an empty string when no config has been loaded. The literal "auto" is
// treated as unset so system-locale detection still applies (AI.md PART 30).
func GetLanguage(flagLang, configLang string) string {
	// 1. Explicit --lang flag
	if flagLang != "" {
		return validateLang(flagLang)
	}
	// 2. Config file
	if configLang != "" && configLang != "auto" {
		return validateLang(configLang)
	}
	// 3. LC_ALL env var
	if lang := os.Getenv("LC_ALL"); lang != "" {
		return validateLang(localeLang(lang))
	}
	// 3b. LANG env var
	if lang := os.Getenv("LANG"); lang != "" {
		return validateLang(localeLang(lang))
	}
	// 4. Default
	return "en"
}

// localeLang extracts the language code from a system locale such as
// "es_ES.UTF-8", "fr.UTF-8" or "de@euro".
func localeLang(locale string) string {
	if i := strings.IndexAny(locale, "_.@-"); i != -1 {
		return locale[:i]
	}
	return locale
}

// validateLang returns lang if supported, otherwise "en".
func validateLang(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if IsSupported(lang) {
		return lang
	}
	return "en"
}

// toString converts an interface{} to string.
func toString(v interface{}) string {
	switch s := v.(type) {
	case string:
		return s
	case int:
		return fmt_int(s)
	case error:
		return s.Error()
	default:
		b, _ := json.Marshal(v)
		return strings.Trim(string(b), `"`)
	}
}

// fmt_int converts int to string without importing fmt
func fmt_int(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	digits := make([]byte, 0, 20)
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}

// LocaleFS returns the embedded locale filesystem for serving via HTTP.
func LocaleFS() embed.FS {
	return localeFS
}
