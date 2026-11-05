package server

import (
	"encoding/json"
	"html/template"

	"github.com/apimgr/pastebin/src/config"
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
func buildConsentClientConfig(cfg *config.Config) consentClientConfig {
	p := &cfg.Server.Privacy
	c := p.Consent
	return consentClientConfig{
		ShowUntilAcknowledged: c.ShowUntilAcknowledged,
		Position:              consentPosition(c.Position),
		Message:               p.GetConsentMessage(),
		PolicyText:            c.Policy.Text,
		PolicyURL:             c.Policy.URL,
		AcceptText:            c.Buttons.Accept,
		DeclineText:           c.Buttons.Decline,
		ShowPreferences:       c.ShowPreferences,
		PreferencesText:       c.PreferencesText,
		DataSold:              p.Data.Sold,
		DefaultPreferences:    c.DefaultEnabled && p.Cookies.Preferences.Enabled,
		DefaultAnalytics:      c.DefaultEnabled && p.Cookies.Analytics.Enabled,
		AnalyticsConfigured:   cfg.Server.Tracking.Enabled(),
		Descriptions: consentDescriptions{
			Essential:   p.Cookies.Essential.Description,
			Preferences: p.Cookies.Preferences.Description,
			Analytics:   p.GetAnalyticsDescription(),
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
func renderConsentConfig(cfg *config.Config) template.JS {
	b, err := json.Marshal(buildConsentClientConfig(cfg))
	if err != nil {
		return template.JS("{}")
	}
	return template.JS(b)
}
