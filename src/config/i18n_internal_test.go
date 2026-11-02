package config

import "testing"

func TestValidateI18n(t *testing.T) {
	c := I18nConfig{DefaultLanguage: "xx", AvailableLanguages: []string{"DE", "de", "bogus", "fr"}, FallbackLanguage: "zz", CookieName: "bad name", CookieMaxAge: "nope"}
	validateI18n(&c)
	if len(c.AvailableLanguages) != 2 || c.AvailableLanguages[0] != "de" || c.AvailableLanguages[1] != "fr" {
		t.Errorf("available = %v", c.AvailableLanguages)
	}
	if c.DefaultLanguage != "avail-first" && c.DefaultLanguage != "de" {
		t.Errorf("default = %q", c.DefaultLanguage)
	}
	if c.FallbackLanguage != "en" || c.CookieName != "lang" || c.CookieMaxAge != "365d" || c.I18nCookieSeconds() != 365*24*3600 {
		t.Errorf("sanitized = %+v", c)
	}
	empty := I18nConfig{}
	validateI18n(&empty)
	if len(empty.AvailableLanguages) != 7 || empty.DefaultLanguage != "en" {
		t.Errorf("empty config = %+v", empty)
	}
}
