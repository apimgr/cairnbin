package i18n

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestIsSupported covers all 7 supported language codes, unsupported codes,
// and normalization (mixed case, whitespace).
func TestIsSupported(t *testing.T) {
	cases := []struct {
		name string
		lang string
		want bool
	}{
		{"en_supported", "en", true},
		{"fr_supported", "fr", true},
		{"de_supported", "de", true},
		{"es_supported", "es", true},
		{"ar_supported", "ar", true},
		{"ja_supported", "ja", true},
		{"zh_supported", "zh", true},
		{"uppercase_EN", "EN", true},
		{"mixed_case_Fr", "Fr", true},
		{"leading_space", " en", true},
		{"trailing_space", "en ", true},
		{"both_spaces", " en ", true},
		{"unsupported_ru", "ru", false},
		{"unsupported_it", "it", false},
		{"unsupported_empty", "", false},
		{"unsupported_xx", "xx", false},
		{"unsupported_en_US", "en-US", false},
		{"unsupported_en_dash_us", "en_US", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsSupported(tc.lang)
			if got != tc.want {
				t.Errorf("IsSupported(%q) = %v, want %v", tc.lang, got, tc.want)
			}
		})
	}
}

// TestTranslate covers known key in en, unknown key fallback, unsupported lang fallback,
// and that a missing key in a foreign lang falls back to en before returning the key itself.
func TestTranslate(t *testing.T) {
	cases := []struct {
		name string
		lang string
		key  string
		want string
	}{
		{"en_known_key", "en", "common.save", "Save"},
		{"fr_known_key", "fr", "common.save", "Enregistrer"},
		{"owner_tokens_en", "en", "preferences.owner_tokens", "Owner tokens"},
		{"owner_tokens_es", "es", "preferences.owner_tokens", "Tokens de propietario"},
		{"owner_tokens_fr", "fr", "preferences.owner_tokens", "Jetons du propriétaire"},
		{"owner_tokens_de", "de", "preferences.owner_tokens", "Eigentümer-Tokens"},
		{"owner_tokens_ja", "ja", "preferences.owner_tokens", "所有者トークン"},
		{"owner_tokens_zh", "zh", "preferences.owner_tokens", "所有者令牌"},
		{"owner_tokens_ar", "ar", "preferences.owner_tokens", "رموز المالك"},
		{"en_nav_home", "en", "nav.home", "Home"},
		{"unsupported_lang_falls_back_to_en", "xx", "common.save", "Save"},
		{"unknown_key_returns_key", "en", "no.such.key", "no.such.key"},
		{"unknown_key_unsupported_lang_returns_key", "ru", "no.such.key", "no.such.key"},
		{"uppercase_lang_normalized", "EN", "common.save", "Save"},
		{"space_in_lang_normalized", " en ", "common.save", "Save"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Translate(tc.lang, tc.key)
			if got != tc.want {
				t.Errorf("Translate(%q, %q) = %q, want %q", tc.lang, tc.key, got, tc.want)
			}
		})
	}
}

// TestTranslateFormat covers placeholder substitution, multiple placeholders,
// and odd/missing placeholder args (no panic, no substitution).
func TestTranslateFormat(t *testing.T) {
	cases := []struct {
		name string
		lang string
		key  string
		args []interface{}
		want string
	}{
		{
			"single_placeholder",
			"en", "auth.min_length",
			[]interface{}{"min", "8"},
			"At least 8 characters",
		},
		{
			"two_placeholders",
			"en", "common.page_x_of_y",
			[]interface{}{"current", "3", "total", "10"},
			"Page 3 of 10",
		},
		{
			"no_placeholders",
			"en", "common.save",
			nil,
			"Save",
		},
		{
			"missing_placeholder_arg_left_in_string",
			"en", "auth.min_length",
			nil,
			"At least {min} characters",
		},
		{
			"odd_args_last_pair_ignored",
			"en", "common.page_x_of_y",
			[]interface{}{"current", "1", "total"},
			"Page 1 of {total}",
		},
		{
			"unsupported_lang_falls_back_to_en",
			"xx", "common.save",
			nil,
			"Save",
		},
		{
			"swagger_title_placeholder",
			"en", "swagger.title",
			[]interface{}{"app_name", "MyApp"},
			"API Documentation - MyApp",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TranslateFormat(tc.lang, tc.key, Args(tc.args...))
			if got != tc.want {
				t.Errorf("TranslateFormat(%q, %q, %v) = %q, want %q", tc.lang, tc.key, tc.args, got, tc.want)
			}
		})
	}
}

// TestTranslatePlural covers count=0, count=1, count=2 for languages with
// different plural rules: en (1→one, else→other), fr (≤1→one, else→other),
// zh/ja (always→other), ar (zero/one/two/few/many/other).
func TestTranslatePlural(t *testing.T) {
	cases := []struct {
		name  string
		lang  string
		key   string
		count int
		want  string
	}{
		// English: standard one/other; count=0 uses "other" (no "zero" form in pluralForm),
		// so the code returns the "other" value "0 items", not the "zero" key "No items".
		{"en_zero", "en", "plurals.items", 0, "0 items"},
		{"en_one", "en", "plurals.items", 1, "1 item"},
		{"en_two", "en", "plurals.items", 2, "2 items"},
		{"en_ten", "en", "plurals.items", 10, "10 items"},

		// French: 0 and 1 both use "one"
		{"fr_zero", "fr", "plurals.items", 0, "0 élément"},
		{"fr_one", "fr", "plurals.items", 1, "1 élément"},
		{"fr_two", "fr", "plurals.items", 2, "2 éléments"},

		// Chinese: all counts use "other"
		{"zh_zero", "zh", "plurals.items", 0, "0 个项目"},
		{"zh_one", "zh", "plurals.items", 1, "1 个项目"},
		{"zh_two", "zh", "plurals.items", 2, "2 个项目"},

		// Japanese: all counts use "other"
		{"ja_zero", "ja", "plurals.items", 0, "0件"},
		{"ja_one", "ja", "plurals.items", 1, "1件"},
		{"ja_two", "ja", "plurals.items", 2, "2件"},

		// Arabic: full six-form CLDR plural rules; the locale has zero/one/two/few/many/other.
		{"ar_zero", "ar", "plurals.items", 0, "لا توجد عناصر"},
		{"ar_one", "ar", "plurals.items", 1, "عنصر واحد"},
		{"ar_two", "ar", "plurals.items", 2, "عنصران"},
		{"ar_few", "ar", "plurals.items", 3, "3 عناصر"},
		{"ar_many", "ar", "plurals.items", 11, "11 عنصرًا"},

		// Count substitution works for "days"
		{"en_one_day", "en", "plurals.days", 1, "1 day"},
		{"en_many_days", "en", "plurals.days", 5, "5 days"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TranslatePlural(tc.lang, tc.key, tc.count)
			if got != tc.want {
				t.Errorf("TranslatePlural(%q, %q, %d) = %q, want %q", tc.lang, tc.key, tc.count, got, tc.want)
			}
		})
	}
}

// TestLangFromRequest covers the full priority chain:
// ?lang= query param (supported and unsupported), lang cookie,
// Accept-Language header with quality values and subtags, and "en" fallback.
func TestLangFromRequest(t *testing.T) {
	cases := []struct {
		name       string
		url        string
		cookie     string
		acceptLang string
		want       string
	}{
		// Query parameter wins over everything else
		{"query_param_supported", "/?lang=fr", "", "", "fr"},
		{"query_param_unsupported_falls_back_to_en", "/?lang=ru", "", "", "en"},
		{"query_param_uppercase", "/?lang=FR", "", "", "fr"},

		// Cookie wins when no query param
		{"cookie_supported", "/", "fr", "", "fr"},
		{"cookie_unsupported_falls_back_to_en", "/", "ru", "", "en"},

		// Accept-Language header
		{"accept_lang_simple", "/", "", "de", "de"},
		{"accept_lang_en_US_base_match", "/", "", "en-US,en;q=0.9", "en"},
		{"accept_lang_es_with_quality", "/", "", "en-US,en;q=0.9,es;q=0.8", "en"},
		{"accept_lang_first_preferred_match", "/", "", "fr;q=0.9,de;q=0.8", "fr"},
		{"accept_lang_only_subtag", "/", "", "zh-CN;q=1.0", "zh"},
		{"accept_lang_unsupported_falls_back_to_en", "/", "", "ru;q=1.0,uk;q=0.9", "en"},

		// Fallback to "en"
		{"no_hints_returns_en", "/", "", "", "en"},

		// Query param beats cookie
		{"query_beats_cookie", "/?lang=de", "fr", "", "de"},

		// Cookie beats Accept-Language
		{"cookie_beats_accept_lang", "/", "ja", "fr", "ja"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			if tc.cookie != "" {
				req.AddCookie(&http.Cookie{Name: "lang", Value: tc.cookie})
			}
			if tc.acceptLang != "" {
				req.Header.Set("Accept-Language", tc.acceptLang)
			}
			got := LangFromRequest(req)
			if got != tc.want {
				t.Errorf("LangFromRequest (url=%q cookie=%q accept=%q) = %q, want %q",
					tc.url, tc.cookie, tc.acceptLang, got, tc.want)
			}
		})
	}
}

// TestGetLanguage covers the CLI flag path, LC_ALL env, LANG env, and "en" fallback.
// It also exercises validateLang indirectly for supported and unsupported values.
func TestGetLanguage(t *testing.T) {
	cases := []struct {
		name       string
		flagLang   string
		configLang string
		lcAll      string
		langEnv    string
		want       string
	}{
		// Explicit flag takes highest priority
		{"flag_supported", "fr", "", "", "", "fr"},
		{"flag_unsupported_falls_to_en", "ru", "", "", "", "en"},
		{"flag_uppercase_normalized", "FR", "", "", "", "fr"},
		{"flag_space_normalized", " fr ", "", "", "", "fr"},
		{"flag_beats_config", "fr", "de", "", "", "fr"},

		// Config file is priority 2, above the environment
		{"config_used", "", "de", "es_ES.UTF-8", "", "de"},
		{"config_auto_ignored", "", "auto", "es_ES.UTF-8", "", "es"},
		{"config_unsupported_falls_to_en", "", "ru", "", "", "en"},

		// LC_ALL overrides LANG
		{"lc_all_wins", "", "", "de_DE.UTF-8", "es_ES.UTF-8", "de"},
		{"lc_all_unsupported_falls_to_en", "", "", "ru_RU.UTF-8", "", "en"},
		{"lc_all_bare", "", "", "ja", "", "ja"},

		// LANG used when LC_ALL absent
		{"lang_env", "", "", "", "ar_SA.UTF-8", "ar"},
		{"lang_env_bare", "", "", "", "zh", "zh"},
		{"lang_env_unsupported", "", "", "", "it_IT.UTF-8", "en"},

		// Nothing set → "en"
		{"all_empty", "", "", "", "", "en"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LC_ALL", tc.lcAll)
			t.Setenv("LANG", tc.langEnv)
			got := GetLanguage(tc.flagLang, tc.configLang)
			if got != tc.want {
				t.Errorf("GetLanguage(%q, %q) [LC_ALL=%q LANG=%q] = %q, want %q",
					tc.flagLang, tc.configLang, tc.lcAll, tc.langEnv, got, tc.want)
			}
		})
	}
}

// TestPluralForm exercises the pluralForm helper indirectly through TranslatePlural
// using the "days" key (which has no "zero" entry, so zero triggers "other" fallback
// on all languages — this is the documented fallback path).
func TestPluralFormFallback(t *testing.T) {
	cases := []struct {
		name  string
		lang  string
		count int
		want  string
	}{
		// "days" has no zero key for en/zh/ja; pluralForm returns "other" for 0 in those langs.
		// French is special: pluralForm("fr", 0) returns "one" (French treats 0 as singular),
		// so days.one = "{count} jour" → "0 jour".
		// Arabic: pluralForm("ar", 0) returns "zero" and ar.json has "days.zero" — would need it;
		// we test ar with count=1 (one form) which maps to "days.one" in ar.json.
		{"en_zero_days_fallback", "en", 0, "0 days"},
		{"fr_zero_days_fallback", "fr", 0, "0 jour"},
		{"zh_zero_days_fallback", "zh", 0, "0 天"},
		{"ja_zero_days_fallback", "ja", 0, "0日"},
		{"ar_one_day", "ar", 1, "يوم واحد"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TranslatePlural(tc.lang, "plurals.days", tc.count)
			if got != tc.want {
				t.Errorf("TranslatePlural(%q, plurals.days, %d) = %q, want %q",
					tc.lang, tc.count, got, tc.want)
			}
		})
	}
}

// TestLocalesFS verifies the embedded FS is accessible and non-empty.
func TestLocalesFS(t *testing.T) {
	fs := LocaleFS()
	data, err := fs.ReadFile("locales/en.json")
	if err != nil {
		t.Fatalf("LocaleFS ReadFile locales/en.json: %v", err)
	}
	if len(data) == 0 {
		t.Error("LocaleFS: en.json is empty")
	}
}

// TestJSBundle verifies the runtime JS string bundle is valid JSON, contains
// the expected keys with the js. prefix stripped, localizes per language, and
// falls back to English for unsupported locales.
func TestJSBundle(t *testing.T) {
	en := JSBundle("en")
	if !strings.Contains(en, `"update_now"`) || strings.Contains(en, "js.") {
		t.Errorf("JSBundle(en) missing key or kept prefix: %s", en)
	}

	var m map[string]string
	if err := json.Unmarshal([]byte(en), &m); err != nil {
		t.Fatalf("JSBundle(en) is not valid JSON: %v", err)
	}
	if m["copy"] != "Copy" {
		t.Errorf("JSBundle(en)[copy] = %q; want Copy", m["copy"])
	}

	de := JSBundle("de")
	var dm map[string]string
	if err := json.Unmarshal([]byte(de), &dm); err != nil {
		t.Fatalf("JSBundle(de) is not valid JSON: %v", err)
	}
	if dm["update_now"] != "Jetzt aktualisieren" {
		t.Errorf("JSBundle(de)[update_now] = %q; want German value", dm["update_now"])
	}

	// Unsupported locale falls back to English.
	if JSBundle("xx") != en {
		t.Error("JSBundle(unsupported) did not fall back to English")
	}
}

// TestToString_FloatValue covers the default (json.Marshal) branch of the
// private toString helper, which is triggered by any non-string, non-int value.
func TestToString_FloatValue(t *testing.T) {
	got := toString(3.14)
	if got != "3.14" {
		t.Errorf("toString(3.14) = %q, want %q", got, "3.14")
	}
}

// flattenKeys recursively flattens a nested map into a sorted slice of dot-separated keys.
func flattenKeys(m map[string]interface{}, prefix string) []string {
	var keys []string
	for k, v := range m {
		full := k
		if prefix != "" {
			full = prefix + "." + k
		}
		if nested, ok := v.(map[string]interface{}); ok {
			keys = append(keys, flattenKeys(nested, full)...)
		} else {
			keys = append(keys, full)
		}
	}
	sort.Strings(keys)
	return keys
}

// TestKeyParity asserts that every locale file has exactly the same set of keys as en.json.
// This is a build-time check required by PART 30: all 7 locale files MUST have identical keys.
func TestKeyParity(t *testing.T) {
	fs := LocaleFS()

	loadKeys := func(lang string) []string {
		data, err := fs.ReadFile("locales/" + lang + ".json")
		if err != nil {
			t.Fatalf("TestKeyParity: cannot read %s.json: %v", lang, err)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("TestKeyParity: cannot parse %s.json: %v", lang, err)
		}
		return flattenKeys(m, "")
	}

	enKeys := loadKeys("en")
	enSet := make(map[string]struct{}, len(enKeys))
	for _, k := range enKeys {
		enSet[k] = struct{}{}
	}

	for _, lang := range []string{"ar", "de", "es", "fr", "ja", "zh"} {
		t.Run(lang, func(t *testing.T) {
			otherKeys := loadKeys(lang)
			otherSet := make(map[string]struct{}, len(otherKeys))
			for _, k := range otherKeys {
				otherSet[k] = struct{}{}
			}

			var missing, extra []string
			for k := range enSet {
				if _, ok := otherSet[k]; !ok {
					missing = append(missing, k)
				}
			}
			for k := range otherSet {
				if _, ok := enSet[k]; !ok {
					extra = append(extra, k)
				}
			}
			sort.Strings(missing)
			sort.Strings(extra)

			if len(missing) > 0 {
				t.Errorf("%s.json is missing %d keys present in en.json: %v", lang, len(missing), missing)
			}
			if len(extra) > 0 {
				t.Errorf("%s.json has %d extra keys not in en.json: %v", lang, len(extra), extra)
			}
		})
	}
}

// TestTranslatePlural_FallbackPath covers the "try other as fallback" branch:
// when the primary plural form key (e.g. "nonexistent.one") is not found,
// the code falls back to "nonexistent.other". Neither key exists so the key
// itself is returned — but all three fallback stmts are executed.
func TestTranslatePlural_FallbackPath(t *testing.T) {
	got := TranslatePlural("en", "nonexistent.key", 1)
	// "nonexistent.key.one" does not exist → falls back to "nonexistent.key.other"
	// which also does not exist → returns the key as-is (no panic).
	if got == "" {
		t.Error("TranslatePlural with nonexistent key returned empty string")
	}
}

// ─── Direction ────────────────────────────────────────────────────────────────

// TestDirection covers the RTL branch ("ar"), the LTR branch (all other
// supported languages), the unsupported-language fallback to "ltr", and
// normalization (uppercase, whitespace).
func TestDirection(t *testing.T) {
	cases := []struct {
		name string
		lang string
		want string
	}{
		{"ar_is_rtl", "ar", "rtl"},
		{"en_is_ltr", "en", "ltr"},
		{"fr_is_ltr", "fr", "ltr"},
		{"de_is_ltr", "de", "ltr"},
		{"es_is_ltr", "es", "ltr"},
		{"zh_is_ltr", "zh", "ltr"},
		{"ja_is_ltr", "ja", "ltr"},
		// Unsupported language falls back to "en" which is ltr.
		{"unsupported_falls_back_ltr", "ru", "ltr"},
		{"empty_falls_back_ltr", "", "ltr"},
		// Normalization: uppercase and whitespace.
		{"ar_uppercase_normalized", "AR", "rtl"},
		{"ar_space_normalized", " ar ", "rtl"},
		{"en_uppercase_normalized", "EN", "ltr"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Direction(tc.lang)
			if got != tc.want {
				t.Errorf("Direction(%q) = %q; want %q", tc.lang, got, tc.want)
			}
		})
	}
}

// TestNoFormatVerbsInLocales enforces PART 30: translations use named {token}
// placeholders only; printf verbs and positional {0} placeholders are forbidden.
func TestNoFormatVerbsInLocales(t *testing.T) {
	verb := regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z]|\{[0-9]+\}`)
	for lang, table := range translations {
		for key, val := range table {
			if verb.MatchString(val) {
				t.Errorf("%s: key %q has a format verb or positional placeholder: %q", lang, key, val)
			}
		}
	}
}

func TestArgsAndLocaleLang(t *testing.T) {
	got := Args("a", 1, "b", "x", "dangling")
	if len(got) != 2 || got["a"] != "1" || got["b"] != "x" {
		t.Errorf("Args = %v", got)
	}
	for in, want := range map[string]string{"es_ES.UTF-8": "es", "fr.UTF-8": "fr", "de@euro": "de", "ja": "ja"} {
		if localeLang(in) != want {
			t.Errorf("localeLang(%q) = %q, want %q", in, localeLang(in), want)
		}
	}
}

// resetI18nSettings restores the spec defaults after a test that calls Configure.
func resetI18nSettings(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		Configure(Settings{Enabled: true, DefaultLanguage: "en", FallbackLanguage: "en"})
	})
}

func TestConfigure_AvailableDefaultAndDisabled(t *testing.T) {
	resetI18nSettings(t)
	Configure(Settings{Enabled: true, DefaultLanguage: "de", FallbackLanguage: "en", AvailableLanguages: []string{"de", "fr", "xx"}})

	if IsAvailable("es") || !IsAvailable("fr") || !IsAvailable("DE") {
		t.Error("available set not honored")
	}
	if got := Languages(); len(got) != 2 || got[0].Code != "de" || got[1].Code != "fr" || got[0].NativeName == "" {
		t.Errorf("Languages() = %+v", got)
	}

	req := httptest.NewRequest(http.MethodGet, "/?lang=es", nil)
	if got := LangFromRequest(req); got != "de" {
		t.Errorf("unavailable ?lang= = %q, want default de", got)
	}
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Language", "ja,fr;q=0.8")
	if got := LangFromRequest(req); got != "fr" {
		t.Errorf("Accept-Language = %q, want fr", got)
	}
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	if got := LangFromRequest(req); got != "de" {
		t.Errorf("no preference = %q, want default de", got)
	}

	Configure(Settings{Enabled: false, DefaultLanguage: "fr", AvailableLanguages: nil})
	req = httptest.NewRequest(http.MethodGet, "/?lang=es", nil)
	if got := LangFromRequest(req); got != "fr" {
		t.Errorf("disabled i18n = %q, want default fr", got)
	}
}

func TestConfigure_CookieSettings(t *testing.T) {
	resetI18nSettings(t)
	if CookieName() != "lang" || CookieMaxAge() != 365*24*60*60 {
		t.Fatalf("defaults wrong: %q %d", CookieName(), CookieMaxAge())
	}
	Configure(Settings{Enabled: true, CookieName: "locale", CookieMaxAge: 60})
	if CookieName() != "locale" || CookieMaxAge() != 60 {
		t.Errorf("cookie settings not applied: %q %d", CookieName(), CookieMaxAge())
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "locale", Value: "ja"})
	if got := LangFromRequest(req); got != "ja" {
		t.Errorf("custom cookie = %q, want ja", got)
	}
}

// TestKeyConsistency is the PART 30 build-time validation beyond key parity:
// no empty values, matching {token} sets, and every plural category the
// language requires is present wherever English defines plurals.
func TestKeyConsistency(t *testing.T) {
	token := regexp.MustCompile(`\{[a-z_0-9]+\}`)
	tokens := func(s string) string {
		found := token.FindAllString(s, -1)
		sort.Strings(found)
		return strings.Join(found, ",")
	}
	required := map[string][]string{
		"en": {"one", "other"}, "es": {"one", "other"}, "de": {"one", "other"}, "fr": {"one", "other"},
		"zh": {"other"}, "ja": {"other"}, "ar": {"zero", "one", "two", "few", "many", "other"},
	}
	for lang, table := range translations {
		for key, val := range table {
			if strings.TrimSpace(val) == "" {
				t.Errorf("%s: key %q is empty", lang, key)
			}
			// Plural forms legitimately differ per language ("one" may omit {count}).
			isPlural := false
			for _, cat := range []string{"zero", "one", "two", "few", "many", "other"} {
				if strings.HasSuffix(key, "."+cat) && strings.HasPrefix(key, "plurals.") {
					isPlural = true
				}
			}
			if lang != "en" && !isPlural {
				if en, ok := translations["en"][key]; ok && tokens(en) != tokens(val) {
					t.Errorf("%s: key %q placeholders %q differ from en %q", lang, key, tokens(val), tokens(en))
				}
			}
		}
		for key := range translations["en"] {
			if !strings.HasSuffix(key, ".other") {
				continue
			}
			base := strings.TrimSuffix(key, ".other")
			for _, form := range required[lang] {
				if _, ok := table[base+"."+form]; !ok {
					t.Errorf("%s: plural %q missing category %q", lang, base, form)
				}
			}
		}
	}
}
