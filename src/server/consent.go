package server

import (
	"encoding/json"
	"html/template"

	"github.com/apimgr/cairnbin/src/common/i18n"
	"github.com/apimgr/cairnbin/src/config"
)

// consentClientConfig is the client-side consent configuration serialized into
// an inert <script type="application/json" id="pb-consent-data"> block (never
// an executable inline <script>, per project rules). It is sourced entirely
// from server.privacy and drives the app.js banner enhancement and granular
// preferences UI (PART 16). The banner itself is server-rendered; consent state
// lives in the server-readable cookie_consent cookie, never localStorage.
type consentClientConfig struct {
	// ShowUntilAcknowledged keeps the banner visible until the visitor responds.
	ShowUntilAcknowledged bool `json:"showUntilAcknowledged"`
	// Position places the banner: "bottom" (default) or "top".
	Position string `json:"position"`
	// Message is the consent banner text (sold/not-sold variant already chosen).
	Message string `json:"message"`
	// PolicyText/PolicyURL render the inline privacy-policy link.
	PolicyText string `json:"policyText"`
	PolicyURL  string `json:"policyUrl"`
	// AcceptText/DeclineText label the banner buttons.
	AcceptText  string `json:"acceptText"`
	DeclineText string `json:"declineText"`
	// ShowPreferences surfaces the granular "Manage Preferences" control.
	ShowPreferences bool `json:"showPreferences"`
	// PreferencesText labels the "Manage Preferences" control.
	PreferencesText string `json:"preferencesText"`
	// DataSold drives the CCPA "Do Not Sell" opt-out affordance client-side.
	DataSold bool `json:"dataSold"`
	// DefaultPreferences/DefaultAnalytics seed the opt-out model defaults.
	DefaultPreferences bool `json:"defaultPreferences"`
	DefaultAnalytics   bool `json:"defaultAnalytics"`
	// AnalyticsConfigured reports whether an analytics platform is configured;
	// the analytics toggle is only meaningful when true.
	AnalyticsConfigured bool `json:"analyticsConfigured"`
	// Descriptions holds the per-category cookie descriptions for the modal.
	Descriptions consentDescriptions `json:"descriptions"`
}

// consentDescriptions holds the human-readable cookie-category descriptions.
type consentDescriptions struct {
	Essential   string `json:"essential"`
	Preferences string `json:"preferences"`
	Analytics   string `json:"analytics"`
}

// buildConsentClientConfig assembles the client consent config from the privacy
// and tracking settings (PART 31).
func buildConsentClientConfig(cfg *config.Config, languages ...string) consentClientConfig {
	p := &cfg.Server.Privacy
	c := p.Consent
	lang := "en"
	if len(languages) > 0 && languages[0] != "" {
		lang = languages[0]
	}
	defaults := config.DefaultConfig().Server.Privacy
	message := p.GetConsentMessage()
	defaultMessage := defaults.Consent.Message
	messageKey := "privacy.consent_banner"
	if p.Data.Sold {
		defaultMessage = defaults.Consent.MessageIfSold
		messageKey = "privacy.consent_banner_sold"
	}
	message = localizedDefault(lang, message, defaultMessage, messageKey)
	policyText := localizedDefault(lang, c.Policy.Text, defaults.Consent.Policy.Text, "privacy.consent_policy_link")
	acceptText := localizedDefault(lang, c.Buttons.Accept, defaults.Consent.Buttons.Accept, "privacy.consent_accept")
	declineText := localizedDefault(lang, c.Buttons.Decline, defaults.Consent.Buttons.Decline, "privacy.consent_decline")
	preferencesText := localizedDefault(lang, c.PreferencesText, defaults.Consent.PreferencesText, "privacy.consent_manage")
	essentialDescription := localizedDefault(lang, p.Cookies.Essential.Description, defaults.Cookies.Essential.Description, "privacy.cookie_essential_description")
	preferencesDescription := localizedDefault(lang, p.Cookies.Preferences.Description, defaults.Cookies.Preferences.Description, "privacy.cookie_preferences_description")
	analyticsDescription := p.GetAnalyticsDescription()
	defaultAnalytics := defaults.Cookies.Analytics.Description + " " + defaults.Cookies.Analytics.DescriptionSuffixNotSold
	analyticsKey := "privacy.cookie_analytics_description"
	if p.Data.Sold {
		defaultAnalytics = defaults.Cookies.Analytics.Description + " " + defaults.Cookies.Analytics.DescriptionSuffixSold
		analyticsKey = "privacy.cookie_analytics_description_sold"
	}
	analyticsDescription = localizedDefault(lang, analyticsDescription, defaultAnalytics, analyticsKey)
	return consentClientConfig{
		ShowUntilAcknowledged: c.ShowUntilAcknowledged,
		Position:              consentPosition(c.Position),
		Message:               message,
		PolicyText:            policyText,
		PolicyURL:             c.Policy.URL,
		AcceptText:            acceptText,
		DeclineText:           declineText,
		ShowPreferences:       c.ShowPreferences,
		PreferencesText:       preferencesText,
		DataSold:              p.Data.Sold,
		DefaultPreferences:    c.DefaultEnabled && p.Cookies.Preferences.Enabled,
		DefaultAnalytics:      c.DefaultEnabled && p.Cookies.Analytics.Enabled,
		AnalyticsConfigured:   cfg.Server.Tracking.Enabled(),
		Descriptions: consentDescriptions{
			Essential:   essentialDescription,
			Preferences: preferencesDescription,
			Analytics:   analyticsDescription,
		},
	}
}

// consentPosition normalizes the banner position to "bottom" or "top".
func consentPosition(pos string) string {
	if pos == "top" {
		return "top"
	}
	return "bottom"
}

// renderConsentConfig serializes the consent client config as a JSON literal for
// injection into a <script> assignment (PART 31). Marshaling failure yields an
// empty object rather than breaking the page.
func renderConsentConfig(cfg *config.Config, languages ...string) template.JS {
	b, err := json.Marshal(buildConsentClientConfig(cfg, languages...))
	if err != nil {
		return template.JS("{}")
	}
	return template.JS(b)
}

func localizedDefault(lang, value, defaultValue, key string) string {
	if value == "" || value == defaultValue {
		return i18n.Translate(lang, key)
	}
	return value
}

func localizedPrivacyText(lang, field, value string) string {
	defaults := config.DefaultConfig().Server.Privacy
	var defaultValue, key string
	switch field {
	case "data_collection":
		defaultValue, key = defaults.Content.DataCollection, "privacy.default_data_collection"
	case "data_usage":
		defaultValue, key = defaults.Content.DataUsage, "privacy.default_data_usage"
	case "data_usage_if_sold":
		defaultValue, key = defaults.Content.DataUsageIfSold, "privacy.default_data_usage_sold"
	case "data_security":
		defaultValue, key = defaults.Content.DataSecurity, "privacy.default_data_security"
	case "essential_cookies":
		defaultValue, key = defaults.Cookies.Essential.Description, "privacy.cookie_essential_description"
	case "preference_cookies":
		defaultValue, key = defaults.Cookies.Preferences.Description, "privacy.cookie_preferences_description"
	case "analytics_cookies":
		defaultValue = defaults.Cookies.Analytics.Description + " " + defaults.Cookies.Analytics.DescriptionSuffixNotSold
		key = "privacy.cookie_analytics_description"
	case "analytics_cookies_sold":
		defaultValue = defaults.Cookies.Analytics.Description + " " + defaults.Cookies.Analytics.DescriptionSuffixSold
		key = "privacy.cookie_analytics_description_sold"
	}
	if key == "" {
		return value
	}
	return localizedDefault(lang, value, defaultValue, key)
}
