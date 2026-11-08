package config

import (
	"reflect"
	"testing"

	"github.com/apimgr/cairnbin/src/common/i18n"
)

func TestValidateI18n(t *testing.T) {
	c := I18nConfig{DefaultLanguage: "xx", AvailableLanguages: []string{"DE", "de", "bogus", "fr"}, FallbackLanguage: "zz", CookieName: "bad name", CookieMaxAge: "nope"}
	validateI18n(&c)
	if !reflect.DeepEqual(c.AvailableLanguages, i18n.SupportedCodes()) {
		t.Errorf("undersized available list = %v, want all supported languages %v", c.AvailableLanguages, i18n.SupportedCodes())
	}
	if c.DefaultLanguage != "en" {
		t.Errorf("default = %q, want en", c.DefaultLanguage)
	}
	if c.FallbackLanguage != "en" || c.CookieName != "lang" || c.CookieMaxAge != "365d" || c.I18nCookieSeconds() != 365*24*3600 {
		t.Errorf("sanitized = %+v", c)
	}
	empty := I18nConfig{}
	validateI18n(&empty)
	if !reflect.DeepEqual(empty.AvailableLanguages, i18n.SupportedCodes()) || empty.DefaultLanguage != "en" {
		t.Errorf("empty config = %+v", empty)
	}
}

func TestValidateI18nAllowsRequiredMinimum(t *testing.T) {
	required := i18n.RequiredCodes()
	c := I18nConfig{DefaultLanguage: "it", AvailableLanguages: required, FallbackLanguage: "it"}
	validateI18n(&c)
	if !reflect.DeepEqual(c.AvailableLanguages, required) {
		t.Errorf("available = %v, want required minimum %v", c.AvailableLanguages, required)
	}
	if c.DefaultLanguage != "it" || c.FallbackLanguage != "it" {
		t.Errorf("language defaults = %q/%q, want it/it", c.DefaultLanguage, c.FallbackLanguage)
	}
}
