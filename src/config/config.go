package config

import (
	crand "crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/apimgr/pastebin/src/common/secretbox"
	"github.com/apimgr/pastebin/src/logging"
	"github.com/apimgr/pastebin/src/updater"
)

// Config is the top-level configuration for the pastebin server.
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Paste    PasteConfig    `yaml:"paste"`
	Web      WebConfig      `yaml:"web"`
	// FirstRun is true when Load() created server.yml because none existed.
	// Runtime-only — never persisted (startup step 20, AI.md:10631).
	FirstRun bool `yaml:"-"`
}

// ServerConfig holds listener and runtime settings.
type ServerConfig struct {
	Port    string `yaml:"port"`
	Address string `yaml:"address"`
	FQDN    string `yaml:"fqdn"`
	Mode    string `yaml:"mode"`
	// Lang is the config-file equivalent of --lang: the output language used
	// when no --lang flag is given (AI.md PART 30 detection priority 2).
	// "auto" or empty falls through to LC_ALL/LANG, then "en".
	Lang string `yaml:"lang"`
	// APIVersion is the API route prefix segment (default v1): /api/{api_version}/.
	APIVersion string `yaml:"api_version"`
	// BaseURL overrides URL generation.
	BaseURL string `yaml:"baseurl"`
	// Branding holds the site title/tagline/description (PART 12/16).
	Branding BrandingConfig `yaml:"branding"`
	// SEO holds search-engine metadata such as keywords (PART 12/16).
	SEO SEOConfig `yaml:"seo"`
	// User and Group are the system user/group to drop privileges to (PART 12).
	// Empty or "{auto}" means auto-detect; keep current privileges if unset.
	User  string `yaml:"user"`
	Group string `yaml:"group"`
	// PIDFile enables writing a PID file on start (PART 12).
	PIDFile bool `yaml:"pidfile"`
	// Daemonize detaches from the terminal on start (PART 12). Default false.
	Daemonize bool `yaml:"daemonize"`
	// Scheduler configures built-in background tasks (PART 12/18).
	Scheduler SchedulerConfig `yaml:"scheduler"`
	// Token is the operator token (server.token). Auto-generated on first run.
	// All operator-protected API endpoints require: Authorization: Bearer <token>
	Token  string            `yaml:"token"`
	Tokens OwnerTokensConfig `yaml:"tokens"`
	// DataDir is the runtime data directory. Resolved at startup by main from path.GetDataDir.
	// Used by middleware (blocklist) and tasks that need access to security databases.
	DataDir       string              `yaml:"data_dir"`
	Metrics       MetricsConfig       `yaml:"metrics"`
	GeoIP         GeoIPConfig         `yaml:"geoip"`
	Tor           TorConfig           `yaml:"tor"`
	I2P           I2PConfig           `yaml:"i2p"`
	Logging       LoggingConfig       `yaml:"logs"`
	Notifications NotificationsConfig `yaml:"notifications"`
	TLS           TLSConfig           `yaml:"tls"`
	// Backup configures backup encryption, compliance, and retention (PART 21).
	Backup BackupConfig `yaml:"backup"`
	// Update controls the self-update channel and scheduling behaviour (PART 22).
	Update UpdateConfig `yaml:"update"`
	// CLI controls how this server distributes the companion CLI binaries (PART 32).
	CLI CLIDistConfig `yaml:"cli"`
	// I18n controls language availability, defaults and the language cookie (PART 30).
	I18n I18nConfig `yaml:"i18n"`
	// Cache configures the in-process or remote cache driver (PART 9/12).
	Cache CacheConfig `yaml:"cache"`
	// Limits controls HTTP server request and body limits (PART 12).
	Limits LimitsConfig `yaml:"limits"`
	// TrustedProxies lists additional proxy IPs/CIDRs beyond the private ranges
	// that are always trusted (loopback, RFC 1918, etc.) (PART 12).
	TrustedProxies TrustedProxiesConfig `yaml:"trusted_proxies"`
	// URLDetection controls the domain-learning subsystem (PART 12).
	URLDetection URLDetectionConfig `yaml:"url_detection"`
	// Termbin configures the raw-TCP termbin/fiche compatibility listener.
	Termbin TermbinConfig `yaml:"termbin"`
	// Maintenance configures the runtime self-healing maintenance mode (PART 20).
	Maintenance MaintenanceConfig `yaml:"maintenance"`
	// Contact holds the per-role notification recipients (PART 12): admin
	// (server-internal alerts, never public), security (vulnerability reports,
	// surfaced in security.txt), and general (public /server/contact form).
	Contact ContactConfig `yaml:"contact"`
	// Pages holds operator-customizable content and settings for the static
	// /server pages (about, privacy, contact, help, terms) (PART 31).
	Pages PagesConfig `yaml:"pages"`
	// Privacy holds server-wide privacy, cookie-consent, data-handling, and
	// third-party disclosure settings driving the /server/privacy page and the
	// site-wide cookie-consent banner (PART 31).
	Privacy PrivacyConfig `yaml:"privacy"`
	// Tracking configures the server-wide analytics platform (PART 31). Disabled
	// by default; when set it drives the tracking script and third-party
	// auto-population on the privacy page.
	Tracking TrackingConfig `yaml:"tracking"`
	// Cors controls cross-origin resource sharing (PART 16). Moved from the
	// legacy web.cors / web.security.cors single-string field.
	Cors CORSConfig `yaml:"cors"`
	// Healthz controls the optional root-level /healthz alias (PART 13). Moved
	// from the legacy web.healthz location.
	Healthz HealthzConfig `yaml:"healthz"`
	// MCP controls the Model Context Protocol endpoint (PART 11 "MCP Endpoint").
	MCP MCPConfig `yaml:"mcp"`
	// RateLimit controls per-IP request throttling (PART 12 "Rate Limiting").
	RateLimit RateLimitConfig `yaml:"rate_limit"`
	// Compression controls response compression (PART 12).
	Compression CompressionConfig `yaml:"compression"`
	// Security holds secrets, IP lists, abuse detection and security data sources
	// (PART 11 server.security.*).
	Security ServerSecurityConfig `yaml:"security"`
	// Debug holds the debug-only diagnostics settings (PART 6 "Debug
	// Configuration"). They apply only while the debug flag is on.
	Debug DebugConfig `yaml:"debug"`
	// Restore bounds restore/import archive extraction (PART 21).
	Restore RestoreConfig `yaml:"restore"`
	// Compliance toggles per-standard regulatory behavior (AI.md Compliance).
	// All standards are disabled by default; enabled individually. When more
	// than one is enabled, the strictest requirement wins on overlap.
	Compliance ComplianceConfig `yaml:"compliance"`
}

// OwnerTokensConfig controls anonymous owner credentials (AI.md PART 11).
type OwnerTokensConfig struct {
	MaxLinked     int    `yaml:"max_linked"`
	CookieMaxAge  string `yaml:"cookie_max_age"`
	IdlePurgeDays int    `yaml:"idle_purge_days"`
}

// ContactConfig is the unified notification-recipient tree (PART 12). Each role
// carries an email address plus any number of named webhook transports. Empty
// role-specific values fall back to the admin role. The only knob an operator
// must set is server.contact.admin.email; everything else is optional.
type ContactConfig struct {
	// Admin receives server-internal alerts (error spikes, cert/backup failures,
	// panics). NEVER public. Universal fallback for empty role addresses.
	Admin ContactRole `yaml:"admin"`
	// Security receives vulnerability reports. Public — surfaced in security.txt's
	// Contact: line and the PGP keypair UID. Empty → falls back to admin.
	Security ContactRole `yaml:"security"`
	// Abuse receives abuse reports (spam, harassment, illegal content, DMCA /
	// takedown). Public — shown in the contact page "Abuse Reports" section when
	// set. Empty → delivery falls back to general, then admin; never
	// auto-advertises abuse@{fqdn} (an unprovisioned mailbox would bounce).
	Abuse ContactRole `yaml:"abuse"`
	// General receives /server/contact form submissions. Public — shown as the
	// footer "Contact us" address. Empty → falls back to admin.
	General ContactRole `yaml:"general"`
}

// ContactRole is a single notification recipient: an email address and a map of
// named webhook transports (telegram, discord, slack, generic, ...) (PART 12).
type ContactRole struct {
	Email    string            `yaml:"email"`
	Webhooks map[string]string `yaml:"webhooks"`
}

// PagesConfig holds operator-customizable content for the static /server pages
// (PART 31). Empty content fields fall back to the built-in default templates.
type PagesConfig struct {
	About   PageContentConfig `yaml:"about"`
	Privacy PageContentConfig `yaml:"privacy"`
	Contact ContactPageConfig `yaml:"contact"`
	Help    PageContentConfig `yaml:"help"`
	Terms   PageContentConfig `yaml:"terms"`
}

// PageContentConfig is a single static page's optional Markdown content override.
type PageContentConfig struct {
	Content string `yaml:"content"`
}

// ContactPageConfig configures the /server/contact form (PART 31). When Enabled
// is false the form is hidden and only static contact details are shown.
type ContactPageConfig struct {
	// Enabled turns the contact form on. Default true.
	Enabled bool `yaml:"enabled"`
	// Captcha selects the spam-prevention challenge: "simple" (built-in),
	// "recaptcha", or "hcaptcha". Default "simple".
	Captcha string `yaml:"captcha"`
	// SuccessMessage is shown after a successful submission.
	SuccessMessage string `yaml:"success_message"`
}

// PrivacyConfig holds server-wide privacy, cookie-consent, and data-handling
// settings (PART 31). Messaging adapts dynamically to Data.Sold: when data is
// sold the consent banner, analytics description, and data-usage content switch
// to their "may be sold" variants and a CCPA "Do Not Sell" opt-out is surfaced.
type PrivacyConfig struct {
	Data       DataPolicy       `yaml:"data"`
	Retention  RetentionPolicy  `yaml:"retention"`
	Consent    ConsentConfig    `yaml:"consent"`
	Cookies    CookieCategories `yaml:"cookies"`
	ThirdParty ThirdPartyConfig `yaml:"third_party"`
	Content    PrivacyContent   `yaml:"content"`
}

// DataPolicy controls data-handling disclosure and CCPA applicability (PART 31).
type DataPolicy struct {
	// Sold reports whether user data is sold to third parties. Default false.
	// The MIT license permits downstream operators to set this true.
	Sold bool `yaml:"sold"`
	// StoredOnServer reports data lives on this server, not third-party cloud.
	StoredOnServer bool `yaml:"stored_on_server"`
	// Sharing enumerates the conditions under which data may be shared.
	Sharing []SharingCondition `yaml:"sharing"`
}

// SharingCondition describes one circumstance under which data may be shared.
type SharingCondition struct {
	Condition string `yaml:"condition"`
	When      string `yaml:"when"`
	Data      string `yaml:"data"`
}

// RetentionPolicy describes how long data is kept and user data rights (PART 31).
type RetentionPolicy struct {
	Period            string `yaml:"period"`
	ExportAvailable   bool   `yaml:"export_available"`
	DeletionAvailable bool   `yaml:"deletion_available"`
}

// ConsentConfig configures the site-wide cookie-consent banner (PART 31).
type ConsentConfig struct {
	// ShowUntilAcknowledged keeps the banner on every frontend page until the
	// visitor accepts or declines.
	ShowUntilAcknowledged bool `yaml:"show_until_acknowledged"`
	// DefaultEnabled selects the opt-out model: non-essential cookies default on.
	DefaultEnabled bool `yaml:"default_enabled"`
	// Message is shown when Data.Sold is false.
	Message string `yaml:"message"`
	// MessageIfSold is shown when Data.Sold is true.
	MessageIfSold string            `yaml:"message_if_sold"`
	Policy        ConsentPolicyLink `yaml:"policy"`
	Buttons       ConsentButtons    `yaml:"buttons"`
	// Position places the banner: "bottom" (default) or "top".
	Position string `yaml:"position"`
	// ShowPreferences shows the granular "Manage Preferences" link.
	ShowPreferences bool `yaml:"show_preferences"`
	// PreferencesText is the label for the "Manage Preferences" link.
	PreferencesText string `yaml:"preferences_text"`
}

// ConsentPolicyLink is the privacy-policy link shown in the consent banner.
type ConsentPolicyLink struct {
	Text string `yaml:"text"`
	URL  string `yaml:"url"`
}

// ConsentButtons holds the accept/decline button labels for the banner.
type ConsentButtons struct {
	Decline string `yaml:"decline"`
	Accept  string `yaml:"accept"`
}

// CookieCategories groups the three cookie tiers shown in the preferences UI.
type CookieCategories struct {
	Essential   CookieCategory  `yaml:"essential"`
	Preferences CookieCategory  `yaml:"preferences"`
	Analytics   AnalyticsCookie `yaml:"analytics"`
}

// CookieCategory is a single cookie tier: whether it is on and its description.
type CookieCategory struct {
	Enabled     bool   `yaml:"enabled"`
	Description string `yaml:"description"`
}

// AnalyticsCookie extends CookieCategory with sold/not-sold description suffixes.
type AnalyticsCookie struct {
	CookieCategory           `yaml:",inline"`
	DescriptionSuffixNotSold string `yaml:"description_suffix_not_sold"`
	DescriptionSuffixSold    string `yaml:"description_suffix_sold"`
}

// ThirdPartyConfig lists third-party services that receive data. Analytics
// entries are auto-populated from server.tracking; operators may add more.
type ThirdPartyConfig struct {
	Services []ThirdPartyService `yaml:"services"`
}

// ThirdPartyService is a single third-party recipient shown on the privacy page.
type ThirdPartyService struct {
	Name      string `yaml:"name"`
	Purpose   string `yaml:"purpose"`
	DataSent  string `yaml:"data_sent"`
	PolicyURL string `yaml:"policy_url"`
}

// PrivacyContent holds the Markdown body blocks rendered on the privacy page.
type PrivacyContent struct {
	DataCollection string `yaml:"data_collection"`
	// DataUsage is used when Data.Sold is false.
	DataUsage string `yaml:"data_usage"`
	// DataUsageIfSold is used when Data.Sold is true.
	DataUsageIfSold string `yaml:"data_usage_if_sold"`
	DataSecurity    string `yaml:"data_security"`
}

// TrackingConfig configures the server-wide analytics platform (PART 31).
type TrackingConfig struct {
	// Type selects the analytics platform; empty or "none" disables tracking.
	Type string `yaml:"type"`
	// ID is the platform tracking/site identifier (format depends on Type).
	ID string `yaml:"id"`
	// URL is the self-hosted instance URL (required for matomo/piwik/owa/umami).
	URL string `yaml:"url"`
}

// GetConsentMessage returns the banner message matching the data-sold setting.
func (p *PrivacyConfig) GetConsentMessage() string {
	if p.Data.Sold {
		return p.Consent.MessageIfSold
	}
	return p.Consent.Message
}

// GetAnalyticsDescription returns the analytics cookie description with the
// sold/not-sold suffix appended.
func (p *PrivacyConfig) GetAnalyticsDescription() string {
	base := p.Cookies.Analytics.Description
	if p.Data.Sold {
		return strings.TrimSpace(base + " " + p.Cookies.Analytics.DescriptionSuffixSold)
	}
	return strings.TrimSpace(base + " " + p.Cookies.Analytics.DescriptionSuffixNotSold)
}

// GetDataUsageContent returns the data-usage Markdown matching the sold setting.
func (p *PrivacyConfig) GetDataUsageContent() string {
	if p.Data.Sold {
		return p.Content.DataUsageIfSold
	}
	return p.Content.DataUsage
}

// IsCCPAApplicable reports whether the CCPA "Do Not Sell" opt-out must be shown.
func (p *PrivacyConfig) IsCCPAApplicable() bool {
	return p.Data.Sold
}

// trackingTypeNames maps analytics type keys to human-friendly platform names.
var trackingTypeNames = map[string]string{
	"google":     "Google Analytics",
	"matomo":     "Matomo",
	"piwik":      "Piwik",
	"owa":        "Open Web Analytics",
	"fathom":     "Fathom Analytics",
	"plausible":  "Plausible Analytics",
	"umami":      "Umami",
	"simple":     "Simple Analytics",
	"cloudflare": "Cloudflare Web Analytics",
}

// trackingPolicyURLs maps analytics types to their public privacy-policy URLs.
var trackingPolicyURLs = map[string]string{
	"google":     "https://policies.google.com/privacy",
	"matomo":     "https://matomo.org/privacy-policy/",
	"piwik":      "https://matomo.org/privacy-policy/",
	"fathom":     "https://usefathom.com/privacy",
	"plausible":  "https://plausible.io/privacy",
	"umami":      "https://umami.is/privacy",
	"simple":     "https://simpleanalytics.com/privacy",
	"cloudflare": "https://www.cloudflare.com/privacypolicy/",
}

// Enabled reports whether an analytics platform is configured.
func (t TrackingConfig) Enabled() bool {
	return t.Type != "" && t.Type != "none"
}

// TypeName returns the human-friendly platform name for the configured type.
func (t TrackingConfig) TypeName() string {
	if n, ok := trackingTypeNames[t.Type]; ok {
		return n
	}
	return t.Type
}

// analyticsService builds the third-party service entry for the configured
// analytics platform, or false when tracking is disabled (PART 31).
func (t TrackingConfig) analyticsService() (ThirdPartyService, bool) {
	if !t.Enabled() {
		return ThirdPartyService{}, false
	}
	return ThirdPartyService{
		Name:      t.TypeName(),
		Purpose:   "Usage analytics",
		DataSent:  "Page views, browser type, country (anonymized IP)",
		PolicyURL: trackingPolicyURLs[t.Type],
	}, true
}

// EffectiveThirdParty returns the configured third-party services with the
// analytics platform auto-prepended when server.tracking is enabled and not
// already present (PART 31).
func (c *Config) EffectiveThirdParty() []ThirdPartyService {
	services := append([]ThirdPartyService(nil), c.Server.Privacy.ThirdParty.Services...)
	svc, ok := c.Server.Tracking.analyticsService()
	if !ok {
		return services
	}
	for _, s := range services {
		if s.Name == svc.Name {
			return services
		}
	}
	return append([]ThirdPartyService{svc}, services...)
}

// MaintenanceConfig configures the runtime self-healing maintenance mode (PART 20).
// On a critical error (database connection loss or file-write failure) the server
// enters maintenance mode, rejects write operations with HTTP 503, and continuously
// attempts self-healing until the issue clears.
type MaintenanceConfig struct {
	// SelfHealing controls the background retry/recovery loop.
	SelfHealing MaintenanceSelfHealingConfig `yaml:"self_healing"`
	// Cleanup controls resource reclamation attempted during disk-related healing.
	Cleanup MaintenanceCleanupConfig `yaml:"cleanup"`
	// Notify controls maintenance enter/exit email notifications.
	Notify MaintenanceNotifyConfig `yaml:"notify"`
}

// MaintenanceSelfHealingConfig controls the self-healing retry loop (PART 20).
type MaintenanceSelfHealingConfig struct {
	// Enabled gates the background retry loop. Default true.
	Enabled bool `yaml:"enabled"`
	// RetryInterval is the delay between self-healing attempts. Default "30s".
	RetryInterval string `yaml:"retry_interval"`
	// MaxAttempts caps retries; 0 means unlimited (keep trying forever). Default 0.
	MaxAttempts int `yaml:"max_attempts"`
}

// MaintenanceCleanupConfig controls resource reclamation during healing (PART 20).
type MaintenanceCleanupConfig struct {
	// DiskThreshold is the disk-usage percentage that triggers cleanup. Default 90.
	DiskThreshold int `yaml:"disk_threshold"`
	// LogRetentionDays is how many days of logs to keep during cleanup. Default 7.
	LogRetentionDays int `yaml:"log_retention_days"`
	// BackupKeepCount is how many recent backups to retain during cleanup. Default 5.
	BackupKeepCount int `yaml:"backup_keep_count"`
}

// MaintenanceNotifyConfig controls maintenance enter/exit notifications (PART 20).
type MaintenanceNotifyConfig struct {
	// OnEnter sends a notification when entering maintenance mode. Default true.
	OnEnter bool `yaml:"on_enter"`
	// OnExit sends a notification when exiting maintenance mode. Default true.
	OnExit bool `yaml:"on_exit"`
}

// TermbinConfig configures the raw-TCP termbin/fiche-protocol listener.
// Clients connect, stream content, half-close, and receive a single
// "{base}/{id}\n" URL line. Disabled by default; opt-in per operator.
type TermbinConfig struct {
	// Enabled turns the raw-TCP listener on. Default false.
	Enabled bool `yaml:"enabled"`
	// Port is the TCP port to listen on. termbin/fiche default is 9999.
	Port int `yaml:"port"`
	// MaxSize is the maximum bytes accepted per connection. Default 32768.
	MaxSize int64 `yaml:"max_size"`
	// Timeout is the read deadline for a single upload. Default "5s".
	Timeout string `yaml:"timeout"`
}

// LimitsConfig controls HTTP server timeouts and body size limits (PART 12).
type LimitsConfig struct {
	// MaxBodySize is the maximum request body in bytes. Accepts "10MB", "1MiB", or plain integer.
	MaxBodySize int64 `yaml:"max_body_size"`
	// ReadTimeout is the request read timeout, e.g. "30s".
	ReadTimeout string `yaml:"read_timeout"`
	// WriteTimeout is the response write timeout, e.g. "30s".
	WriteTimeout string `yaml:"write_timeout"`
	// IdleTimeout is the keep-alive idle timeout, e.g. "120s".
	IdleTimeout string `yaml:"idle_timeout"`
}

// TrustedProxiesConfig lists additional proxy IPs/CIDRs/DNS names beyond the
// private ranges that are always trusted (PART 12).
// Private ranges (127/8, 10/8, 172.16/12, 192.168/16, fc00::/7, etc.) are
// always trusted without config. Add public proxy IPs here only.
type TrustedProxiesConfig struct {
	Additional []string `yaml:"additional"`
}

// URLDetectionConfig controls the domain-learning subsystem (PART 12).
// When enabled, the server observes incoming Host headers, infers the base
// domain (eTLD+1) and wildcard domain, and uses them for CORS and URL building.
type URLDetectionConfig struct {
	// Learning enables domain learning. Default true.
	Learning bool `yaml:"learning"`
	// MinSamples is the number of observations of a hostname within SampleWindow
	// before it is promoted to baseDomain / wildcardDomain.
	MinSamples int `yaml:"min_samples"`
	// SampleWindow is the rolling time window for MinSamples observations.
	SampleWindow time.Duration `yaml:"sample_window"`
	// LogChanges emits a log line whenever baseDomain or wildcardDomain changes.
	LogChanges bool `yaml:"log_changes"`
	// LiveReload causes the CORS header to update immediately when a new domain is
	// learned, without restarting the server.
	LiveReload bool `yaml:"live_reload"`
}

// CacheConfig selects and configures the cache driver (PART 9/12).
// Defaults to "memory" (in-process, lost on restart).
// Valkey/Redis is recommended for production to persist counters across restarts.
type CacheConfig struct {
	// Type selects the driver: "none", "memory" (default), "valkey", "redis".
	Type string `yaml:"type"`
	// URL is the connection string. Takes precedence over individual fields.
	// Format: redis://user:pass@host:port/db  or  valkey://...
	URL      string `yaml:"url"`
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
	// TLS enables TLS for the connection.
	TLS           bool `yaml:"tls"`
	TLSSkipVerify bool `yaml:"tls_skip_verify"`
	PoolSize      int  `yaml:"pool_size"`
	MinIdle       int  `yaml:"min_idle"`
	// Timeout is the dial/read/write timeout for remote drivers.
	Timeout string `yaml:"timeout"`
	// Prefix is prepended to every key to avoid namespace collisions.
	Prefix string `yaml:"prefix"`
	// TTL is the default time-to-live. e.g. "1h", "30m".
	TTL string `yaml:"ttl"`
}

// TorConfig configures the Tor hidden service and optional outbound network.
type TorConfig struct {
	// Binary path; empty = auto-detect in PATH and common locations.
	Binary string `yaml:"binary"`

	// Outbound network settings.
	UseNetwork bool `yaml:"use_network"`

	// Performance.
	MaxCircuits      int `yaml:"max_circuits"`
	CircuitTimeout   int `yaml:"circuit_timeout"`
	BootstrapTimeout int `yaml:"bootstrap_timeout"`

	// Security.
	SafeLogging               bool `yaml:"safe_logging"`
	MaxStreamsPerCircuit      int  `yaml:"max_streams_per_circuit"`
	CloseCircuitOnStreamLimit bool `yaml:"close_circuit_on_stream_limit"`

	// Bandwidth.
	BandwidthRate       string `yaml:"bandwidth_rate"`
	BandwidthBurst      string `yaml:"bandwidth_burst"`
	MaxMonthlyBandwidth string `yaml:"max_monthly_bandwidth"`

	// Hidden service.
	NumIntroPoints int `yaml:"num_intro_points"`
	VirtualPort    int `yaml:"virtual_port"`

	// OnionAddress is the v3 .onion address (56 chars) assigned to this hidden service.
	// Set automatically on first successful Tor bootstrap; may be overridden to use a
	// pre-generated key. Used by baseURL() priority-0 check (PART 12) and Tor privacy
	// rules — clearnet contact email is NEVER shown when Host matches this address.
	OnionAddress string `yaml:"onion_address"`

	// ContactEmail is the Tor-specific operator contact address.
	// Shown on Tor responses instead of any clearnet contact email (PART 12/31).
	// When empty, no contact email is disclosed on Tor responses.
	ContactEmail string `yaml:"contact_email"`
}

// I2PConfig configures the optional, opt-in I2P eepsite (PART 31.2).
// Disabled by default — unlike Tor, no provider is contacted and no port
// is allocated unless Enabled is explicitly set true.
type I2PConfig struct {
	// Enabled opts into the I2P eepsite; default false (unlike Tor, which
	// auto-enables whenever the tor binary is found).
	Enabled bool `yaml:"enabled"`

	// Binary is the i2pd binary path; empty = auto-detect. When found, the
	// app spawns/manages a dedicated i2pd process (Model A). When absent,
	// the app falls back to an external SAM bridge (Model B).
	Binary string `yaml:"binary"`

	// SAMAddress is the SAMv3 bridge address used only for Model B.
	SAMAddress string `yaml:"sam_address"`

	// VirtualPort is the port the eepsite listens on (what users connect to).
	VirtualPort int `yaml:"virtual_port"`

	// Tunnel settings (privacy vs latency trade-off).
	InboundLength    int `yaml:"inbound_length"`
	OutboundLength   int `yaml:"outbound_length"`
	InboundQuantity  int `yaml:"inbound_quantity"`
	OutboundQuantity int `yaml:"outbound_quantity"`

	// SignatureType is the SAM/destination signature type (7 = EdDSA-SHA512-Ed25519).
	SignatureType int `yaml:"signature_type"`

	// BootstrapTimeout waits for the destination + tunnels to become ready.
	// Duration string (e.g. "5m"), parsed at startup.
	BootstrapTimeout string `yaml:"bootstrap_timeout"`

	// B32Address is the .b32.i2p address derived from the persisted
	// destination key. Set automatically on first successful I2P bootstrap
	// (PART 31.2, mirroring tor.onion_address's PART 12 behavior) — enables
	// priority-0 FQDN/request detection for .b32.i2p Host headers exactly
	// like tor.onion_address does for .onion.
	B32Address string `yaml:"b32_address"`
}

// GeoIPConfig configures GeoIP detection and country blocking.
type GeoIPConfig struct {
	Enabled bool `yaml:"enabled"`
	// Dir is the path to the MMDB database directory.
	Dir string `yaml:"dir"`
	// DenyCountries blocks the listed ISO 3166-1 alpha-2 codes and allows
	// all others. AllowCountries allows ONLY the listed codes and blocks all
	// others. When both are set, allow_countries wins. When both are empty
	// (the default), no country blocking happens.
	DenyCountries  []string `yaml:"deny_countries"`
	AllowCountries []string `yaml:"allow_countries"`
	// Presets are named, operator-authored country lists for reuse
	// (AI.md PART 19, "Country Blocking Presets"). A preset is only ever a
	// name plus the codes the operator entered; it is never fetched,
	// inferred, or auto-applied, and the enforced behavior always comes from
	// DenyCountries/AllowCountries. Ships empty.
	Presets   map[string][]string  `yaml:"presets"`
	Databases GeoIPDatabasesConfig `yaml:"databases"`
}

// GeoIPDatabasesConfig controls which MMDB files to download and use.
//
// PART 19 lists exactly three: asn, country, and city. There is no separate
// WHOIS database — the geo-whois-asn-country package exposes only country_code.
type GeoIPDatabasesConfig struct {
	ASN     bool `yaml:"asn"`
	Country bool `yaml:"country"`
	City    bool `yaml:"city"`
}

// MetricsAuthConfig is the `server.metrics.auth` block (AI.md PART 20).
//
// "Every metrics route requires a bearer token. There is no unauthenticated
// default." Each service has its own token so rotating one never breaks the
// others, and an empty token disables that service with a 403.
type MetricsAuthConfig struct {
	// AllowUnauthenticated skips token checks for ALL metrics services. It is
	// a firewalled-internal-networks escape hatch; enabling it on a publicly
	// reachable server is a deployment bug.
	AllowUnauthenticated bool `yaml:"allow_unauthenticated"`
	// Tokens holds the per-service bearer tokens.
	Tokens MetricsTokensConfig `yaml:"tokens"`
}

// MetricsTokensConfig holds the per-service bearer tokens for the metrics
// routes. An empty token disables that service (403, empty body).
type MetricsTokensConfig struct {
	Prometheus string `yaml:"prometheus"`
	Grafana    string `yaml:"grafana"`
	Loki       string `yaml:"loki"`
}

// MetricsRootConfig is the `server.metrics.root` block (AI.md PART 20). It
// gates the bare `/metrics` root alias, which Prometheus scrapers default to.
// The alias serves the same handler and is never a redirect.
type MetricsRootConfig struct {
	// Enabled mounts the bare `/metrics` alias. Defaults to true.
	Enabled bool `yaml:"enabled"`
}

// MetricsLokiConfig bounds how much recent log the loki metrics service
// serves (AI.md PART 20).
type MetricsLokiConfig struct {
	// MaxEntries caps how many log entries a scrape returns.
	MaxEntries int `yaml:"max_entries"`
	// MaxAge drops entries older than this Go duration string (e.g. "1h").
	MaxAge string `yaml:"max_age"`
}

// MetricsConfig configures the /metrics endpoint.
type MetricsConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Endpoint string `yaml:"endpoint"`
	// IncludeSystem exposes system_* gauges (CPU, memory, disk) when true.
	IncludeSystem bool `yaml:"include_system"`
	// IncludeRuntime exposes pastebin_go_* runtime gauges when true.
	IncludeRuntime bool   `yaml:"include_runtime"`
	Token          string `yaml:"token"`
	// Auth carries the per-service tokens and the unauthenticated escape
	// hatch. Token is retained as the prometheus-service token for
	// compatibility with existing configs.
	Auth MetricsAuthConfig `yaml:"auth"`
	// Root gates the bare `/metrics` alias.
	Root MetricsRootConfig `yaml:"root"`
	// Loki bounds how much recent log the loki service serves.
	Loki MetricsLokiConfig `yaml:"loki"`
	// DurationBuckets overrides the http_request_duration_seconds histogram buckets.
	DurationBuckets []float64 `yaml:"duration_buckets"`
	// SizeBuckets overrides the http_request_size_bytes / http_response_size_bytes buckets.
	SizeBuckets []float64 `yaml:"size_buckets"`
}

// LoggingConfig is the `server.logs` block (AI.md "Logging"): the global log
// level plus one sub-block per server-owned log file. Every file has its own
// rotate/keep policy; formats are per-file (access: apache/nginx/json/custom,
// server/error/debug: text/json, app: logfmt/json, auth: syslog/json,
// security: fail2ban/syslog/cef/json/text, audit: json only).
type LoggingConfig struct {
	Level    string          `yaml:"level"`
	Access   AccessLogConfig `yaml:"access"`
	Server   LogFileConfig   `yaml:"server"`
	Error    LogFileConfig   `yaml:"error"`
	App      LogFileConfig   `yaml:"app"`
	Auth     LogFileConfig   `yaml:"auth"`
	Audit    AuditConfig     `yaml:"audit"`
	Security LogFileConfig   `yaml:"security"`
	Debug    DebugLogConfig  `yaml:"debug"`
}

// AccessLogConfig configures access.log (server.logs.access). LogHealthChecks
// controls whether successful (2xx) health-check requests are written to
// access.log; failures are always logged regardless of this setting, and all
// health-check requests are logged when debug mode is enabled (AI.md
// "Health-Check Log Suppression").
type AccessLogConfig struct {
	Filename        string `yaml:"filename"`
	Format          string `yaml:"format"`
	Custom          string `yaml:"custom"`
	Rotate          string `yaml:"rotate"`
	Keep            string `yaml:"keep"`
	LogHealthChecks bool   `yaml:"log_health_checks"`
}

// LogFileConfig configures one server-owned log file (server.logs.*).
// Custom is only honored by files whose format may be "custom" (access,
// security); other files ignore it.
type LogFileConfig struct {
	Filename string `yaml:"filename"`
	Format   string `yaml:"format"`
	Custom   string `yaml:"custom"`
	Rotate   string `yaml:"rotate"`
	Keep     string `yaml:"keep"`
}

// DebugLogConfig configures debug.log (server.logs.debug). Lines are written
// only when Enabled is true AND the server runs with debug mode active.
type DebugLogConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Filename string `yaml:"filename"`
	Format   string `yaml:"format"`
	Custom   string `yaml:"custom"`
	Rotate   string `yaml:"rotate"`
	Keep     string `yaml:"keep"`
}

// AuditConfig controls the JSON Lines audit log (AI.md `server.logs.audit`).
// The audit log records configuration, security, backup, and server-lifecycle
// events as one JSON object per line.
type AuditConfig struct {
	Enabled          bool                 `yaml:"enabled"`
	Filename         string               `yaml:"filename"`
	Format           string               `yaml:"format"`
	Rotate           string               `yaml:"rotate"`
	Keep             string               `yaml:"keep"`
	Compress         bool                 `yaml:"compress"`
	IncludeUserAgent bool                 `yaml:"include_user_agent"`
	MaskEmails       bool                 `yaml:"mask_emails"`
	TokenUsage       bool                 `yaml:"token_usage"`
	Events           AuditEventCategories `yaml:"events"`
}

// AuditEventCategories toggles which categories of events are recorded.
type AuditEventCategories struct {
	Configuration bool `yaml:"configuration"`
	Security      bool `yaml:"security"`
	Backup        bool `yaml:"backup"`
	Server        bool `yaml:"server"`
}

// DatabaseConfig selects and configures the storage backend.
type DatabaseConfig struct {
	// Type selects the storage backend: "sqlite" (+ "sqlite2"/"sqlite3" aliases)
	// or "libsql" (+ "turso" alias) for remote Turso/libsql (AI.md PART 5/10).
	Type string `yaml:"driver"`
	// Path is the database connection string: a SQLite file path, or a
	// libsql:// URL for remote Turso/libsql (AI.md PART 5/10).
	Path string `yaml:"url"`
	// Token is the libsql/Turso auth token, used when the URL does not already
	// carry an authToken query parameter (AI.md PART 3 "libSQL Driver").
	// Ignored by the sqlite driver.
	Token string `yaml:"token"`
	// Pool holds the connection pool settings applied to every database handle
	// (AI.md PART 10 "Pool Configuration").
	Pool DatabasePoolConfig `yaml:"pool"`
}

// DatabasePoolConfig configures the sql.DB connection pool (AI.md PART 10).
// SQLite runs a single writer plus readers, so the defaults are the
// "Development" row of the pool sizing table; libsql/remote deployments raise
// them in server.yml.
type DatabasePoolConfig struct {
	// MaxOpen is the maximum number of open connections (default 5).
	MaxOpen int `yaml:"max_open"`
	// MaxIdle is the maximum number of idle connections (default 2).
	MaxIdle int `yaml:"max_idle"`
	// MaxLifetime is the maximum lifetime of a connection (default 5m).
	MaxLifetime time.Duration `yaml:"max_lifetime"`
	// MaxIdleTime is how long a connection may sit idle before it is closed
	// (default 1m).
	MaxIdleTime time.Duration `yaml:"max_idle_time"`
}

// PasteConfig controls paste-specific behaviour.
type PasteConfig struct {
	// MaxSize is the max paste size as a human-readable value ("10mb",
	// "100kb", "1gb", "1tb", or a bare number defaulting to megabytes, e.g.
	// "5" == "5mb"; default "10mb") so operators never have to hand-convert a
	// size to raw bytes. Zero or negative means unlimited. Parsed via
	// ParseSize into MaxSizeBytes below by Validate() — read MaxSizeBytes for
	// the resolved byte count, never MaxSize directly.
	MaxSize string `yaml:"max_size"`
	// MaxSizeBytes is MaxSize resolved to a byte count; zero means unlimited.
	// Computed at load/validation time, never serialized to server.yml.
	MaxSizeBytes int64 `yaml:"-"`
	// DefaultExpiry is "never" or an expiry code.
	DefaultExpiry string `yaml:"default_expiry"`
	// DefaultLanguage is the fallback syntax language (default "text").
	DefaultLanguage string `yaml:"default_language"`
	// MaxBurnAfter caps burn_after (default 9999).
	MaxBurnAfter int `yaml:"max_burn_after"`
	// AllowUnlisted allows unlisted pastes (default true).
	AllowUnlisted bool `yaml:"allow_unlisted"`
}

// RateLimitEndpoint configures the per-IP request quota for one endpoint class (PART 12).
type RateLimitEndpoint struct {
	// Requests is the maximum number of requests allowed per IP within Window seconds.
	Requests int `yaml:"requests"`
	// Window is the sliding window in seconds.
	Window int `yaml:"window"`
}

// RateLimitConfig controls request throttling (PART 12).
// Endpoint classes: Read (GET/HEAD), Write (POST/PUT/PATCH/DELETE), Health (status endpoints).
// GlobalBurst is the absolute ceiling across all classes combined.
type RateLimitConfig struct {
	Enabled     bool              `yaml:"enabled"`
	Read        RateLimitEndpoint `yaml:"read"`
	Write       RateLimitEndpoint `yaml:"write"`
	Health      RateLimitEndpoint `yaml:"health"`
	GlobalBurst int               `yaml:"global_burst"`
}

// WebConfig holds web-UI settings.
type WebConfig struct {
	SiteTitle string `yaml:"site_title"`
	// Theme is the default site theme: "dark", "light", or "auto".
	Theme    string         `yaml:"theme"`
	Robots   RobotsConfig   `yaml:"robots"`
	Security SecurityConfig `yaml:"security"`
	HSTS     HSTSConfig     `yaml:"hsts"`
	CSP      CSPConfig      `yaml:"csp"`
	// Headers controls which advanced security headers are emitted.
	Headers HeadersConfig `yaml:"headers"`
	// CSRF controls double-submit cookie CSRF protection (PART 16).
	CSRF CSRFConfig `yaml:"csrf"`
	// Footer controls operator footer branding (PART 16).
	Footer FooterConfig `yaml:"footer"`
	// Announcements controls the server-rendered site banner (PART 16 → Site
	// Banner, PART 22 → Announcements).
	Announcements AnnouncementsConfig `yaml:"announcements"`
}

// AnnouncementsConfig holds operator-configured site-wide announcement
// messages (server.yml web.announcements, AI.md 26078-26121) rendered as the
// Site Banner (AI.md 22517-22605). Disabled or an empty Messages list means
// no banner is ever rendered.
type AnnouncementsConfig struct {
	Enabled  bool                  `yaml:"enabled"`
	Messages []AnnouncementMessage `yaml:"messages"`
}

// AnnouncementMessage is a single operator announcement (AI.md 26094-26109).
// Type must be one of warning, info, error, success; Start/End are ISO 8601
// UTC timestamps bounding the active window. ID is stable across edits and is
// the key stored in the visitor's dismissed_announcements cookie — changing
// the ID reshows the banner for everyone.
type AnnouncementMessage struct {
	ID          string `yaml:"id"`
	Type        string `yaml:"type"`
	Title       string `yaml:"title"`
	Message     string `yaml:"message"`
	Start       string `yaml:"start"`
	End         string `yaml:"end"`
	Dismissible bool   `yaml:"dismissible"`
}

// FooterConfig holds operator footer branding shown above the default
// application footer (PART 16 Footer Customization).
type FooterConfig struct {
	// CustomHTML is operator-supplied branding HTML rendered above the default
	// footer. It is sanitized before rendering — scripts, event handlers, and
	// dangerous elements are stripped. An empty value uses the default branding;
	// a single space (" ") disables branding and shows only the default footer.
	CustomHTML string `yaml:"custom_html"`
}

// RequestFloodConfig tunes request-flood detection.
type RequestFloodConfig struct {
	Multiplier    int    `yaml:"multiplier"`
	BlockDuration string `yaml:"block_duration"`
}

// AbuseDetectionConfig configures automated abuse detection (PART 11).
type AbuseDetectionConfig struct {
	// RequestFlood triggers when one IP sends more than Multiplier times the
	// read rate limit within a minute.
	RequestFlood RequestFloodConfig `yaml:"request_flood"`
	// AutoBlockIP blocks a flooding IP for RequestFlood.BlockDuration.
	AutoBlockIP bool `yaml:"auto_block_ip"`
	// AutoAlert emails the operator when an IP is auto-blocked.
	AutoAlert bool `yaml:"auto_alert"`
}

// CompressionConfig controls response compression (PART 12).
type CompressionConfig struct {
	Enabled bool `yaml:"enabled"`
	// Level is the compression level, 1-9.
	Level int `yaml:"level"`
	// Types lists the MIME types that are compressed.
	Types []string `yaml:"types"`
}

// MCPConfig controls the Model Context Protocol endpoint. It reuses the API's
// Bearer tokens; there is no MCP-specific token or scope.
type MCPConfig struct {
	// Enabled serves /api/{api_version}/server/mcp (plus /api/mcp and /mcp).
	// Default true; when false every MCP route answers 404.
	Enabled bool `yaml:"enabled"`
}

// HealthzConfig controls the optional /healthz root alias (PART 13).
// The canonical route is always /server/healthz; this alias is opt-in only.
type HealthzConfig struct {
	// Root.Enabled mounts /healthz → /server/healthz when true.
	// Default: false. Only enable for tooling that requires a root-level probe.
	Root struct {
		Enabled bool `yaml:"enabled"`
	} `yaml:"root"`
}

// ComplianceConfig toggles the 11 regulatory standards documented in AI.md's
// Compliance section (server.compliance.*). All default to false; operators
// enable only the standards that apply to their deployment. Consent state is
// stored client-side (cookies) — there is no server-side consent table.
type ComplianceConfig struct {
	// Enabled is the compliance-mode master switch (server.compliance.enabled,
	// AI.md 28945-28989): when true, backups are blocked unless an encryption
	// password is set, independent of which per-standard flags below are on.
	Enabled  bool `yaml:"enabled"`
	GDPR     bool `yaml:"gdpr"`
	CCPA     bool `yaml:"ccpa"`
	HIPAA    bool `yaml:"hipaa"`
	SOC2     bool `yaml:"soc2"`
	PCIDSS   bool `yaml:"pci_dss"`
	ISO27001 bool `yaml:"iso27001"`
	FedRAMP  bool `yaml:"fedramp"`
	LGPD     bool `yaml:"lgpd"`
	PIPEDA   bool `yaml:"pipeda"`
	APPI     bool `yaml:"appi"`
	PDPA     bool `yaml:"pdpa"`
}

// CORSConfig controls cross-origin resource sharing (PART 16 → CORS).
// Lives at server.cors (moved from the legacy web.cors / web.security.cors
// single-string field).
type CORSConfig struct {
	// AllowedOrigins lists allowed origins. ["*"] allows all (credentials NOT
	// allowed); [""] disables CORS headers entirely (same-origin only).
	AllowedOrigins []string `yaml:"allowed_origins"`
	// AllowCredentials sets Access-Control-Allow-Credentials. Only honored
	// when AllowedOrigins is not a bare "*" wildcard.
	AllowCredentials bool `yaml:"allow_credentials"`
	// MaxAge is the Access-Control-Max-Age preflight cache duration in seconds.
	MaxAge int `yaml:"max_age"`
}

// HeadersConfig controls which advanced security headers the server emits (PART 11).
type HeadersConfig struct {
	// SecFetchValidation rejects cross-site state-changing requests when
	// Sec-Fetch-Site: cross-site is present and no Bearer token is provided.
	// Absence of the header is treated as pass-through for legacy-browser compat.
	SecFetchValidation bool `yaml:"sec_fetch_validation"`
}

// CSRFConfig controls CSRF double-submit cookie protection (PART 16).
// CSRF only applies to cookie-authenticated browser forms; Bearer/API-token
// requests and public endpoints are always exempt.
type CSRFConfig struct {
	Enabled bool `yaml:"enabled"`
	// TokenLength is the CSRF token size in bytes. Default: 32.
	TokenLength int    `yaml:"token_length"`
	CookieName  string `yaml:"cookie_name"`
	HeaderName  string `yaml:"header_name"`
	// Secure controls the Secure cookie flag: "auto" | "true" | "false".
	// "auto" sets Secure when the request was received over HTTPS.
	Secure string `yaml:"secure"`
	// ExemptPaths lists paths that bypass CSRF validation.
	// Glob patterns are supported (e.g., /api/v1/webhooks/*).
	ExemptPaths []string `yaml:"exempt_paths"`
}

// CSRFBuiltinExemptPaths are always exempt from CSRF validation, in addition to
// server.csrf.exempt_paths. They are the tokenless, cookie-free creation
// endpoints the compat layer must serve to unmodified third-party clients
// (curl, CI scripts, pastebin.com / lenpaste / microbin / stikked / hastebin /
// dpaste / 0x0.st / sprunge / ix.io tooling): every /api/* route (API routes
// never accept cookie auth, so there is no ambient authority to forge),
// hastebin's POST /documents, microbin's POST /upload, the Bearer-only MCP
// endpoint at /mcp, and the curl-upload
// family's POST /. Browser forms post to /pastes and keep full CSRF validation.
var CSRFBuiltinExemptPaths = []string{"/api/*", "/documents", "/upload", "/mcp", "/"}

// NotificationsConfig holds email notification settings.
type NotificationsConfig struct {
	Email EmailConfig `yaml:"email"`
}

// EmailConfig holds SMTP and sender settings for outbound email.
type EmailConfig struct {
	Enabled bool       `yaml:"enabled"`
	SMTP    SMTPConfig `yaml:"smtp"`
	From    EmailFrom  `yaml:"from"`
	ReplyTo string     `yaml:"reply_to"`
	// TemplateDir is a custom override dir; empty = use embedded defaults.
	TemplateDir string            `yaml:"template_dir"`
	Events      EmailEventsConfig `yaml:"events"`
}

// EmailEventsConfig controls which operator events trigger an email (AI.md:26587-26598).
// All fields default to false except the high-signal events that are true by default.
type EmailEventsConfig struct {
	Startup          bool `yaml:"startup"`
	Shutdown         bool `yaml:"shutdown"`
	BackupComplete   bool `yaml:"backup_complete"`
	BackupFailed     bool `yaml:"backup_failed"`
	SSLExpiring      bool `yaml:"ssl_expiring"`
	SSLRenewed       bool `yaml:"ssl_renewed"`
	SSLRenewalFailed bool `yaml:"ssl_renewal_failed"`
	SecurityAlert    bool `yaml:"security_alert"`
	SchedulerError   bool `yaml:"scheduler_error"`
	UpdateAvailable  bool `yaml:"update_available"`
	UpdateInstalled  bool `yaml:"update_installed"`
}

// SMTPConfig holds connection settings for the outbound SMTP server.
type SMTPConfig struct {
	// Host is the SMTP server hostname or IP. Empty = auto-detect on first run.
	Host string `yaml:"host"`
	// Port is the SMTP server port (default 587).
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	// TLS controls connection security: "auto", "starttls", "tls", "none"
	TLS string `yaml:"tls"`
}

// EmailFrom holds sender identity fields.
type EmailFrom struct {
	// Name defaults to the site title when empty.
	Name string `yaml:"name"`
	// Email defaults to no-reply@{fqdn} when empty.
	Email string `yaml:"email"`
}

// RobotsConfig sets robots.txt allow/deny lists.
type RobotsConfig struct {
	Allow []string `yaml:"allow"`
	Deny  []string `yaml:"deny"`
}

// SecurityConfig holds security.txt metadata and server-wide encryption keys.
// The security contact recipient is NOT stored here — it is the canonical
// server.contact.security.email (PART 12 → "Canonical Contact Keys Only").
type SecurityConfig struct {
	// ReportURL is the primary security.txt Contact line — the repository's
	// GitHub private vulnerability reporting URL, listed FIRST per RFC 9116
	// preference order (PART 11). Empty → derived from the repo owner/name as
	// https://github.com/{org}/{name}/security/advisories/new.
	ReportURL string `yaml:"report_url"`
	// Expires is the security.txt Expires field. Empty → auto-calculated one
	// year from the time the file is served (RFC 9116 requires this field).
	Expires string `yaml:"expires"`
	// PreferredLanguages is the security.txt Preferred-Languages field: a
	// comma-separated list of RFC 5646 language tags. Empty → "en".
	PreferredLanguages string `yaml:"preferred_languages"`
	// ContactEmail is the operator recipient for coordinated-disclosure security
	// reports (PART 11 → Coordinated Disclosure Pipeline). Empty → falls back to
	// the canonical server.contact.security.email.
	ContactEmail string `yaml:"contact_email"`
	// Keyservers is the list of PGP keyserver submission endpoints the project's
	// public key is published to on generate/rotate (PART 11).
	Keyservers []string `yaml:"keyservers"`
	// PublishPGPKey enables serving /.well-known/pgp-key.asc, the Encryption line
	// in security.txt, and keyserver publishing. Parsed via config.ParseBool.
	PublishPGPKey string `yaml:"publish_pgp_key"`
}

// DebugConfig is the server.debug block (AI.md PART 6). Every setting is gated
// by the debug flag (--debug, DEBUG env, MODE=debug), never by mode alone.
type DebugConfig struct {
	// Pprof mounts /debug/pprof and /debug/vars.
	Pprof bool `yaml:"pprof"`
	// LogQueries is reserved for SQL statement logging to debug.log.
	LogQueries bool `yaml:"log_queries"`
	// LogCache logs cache get/set/delete operations to debug.log.
	LogCache bool `yaml:"log_cache"`
	// LogBodies logs request and response bodies, truncated to MaxBodyLogSize.
	LogBodies bool `yaml:"log_bodies"`
	// MaxBodyLogSize caps each logged body (e.g. "10KB").
	MaxBodyLogSize string `yaml:"max_body_log_size"`
	// BlockProfileRate is the runtime block profiling rate (0 = disabled).
	BlockProfileRate int `yaml:"block_profile_rate"`
	// MutexProfileFraction is the runtime mutex profiling fraction (0 = disabled).
	MutexProfileFraction int `yaml:"mutex_profile_fraction"`
	// RuntimeEndpoints mounts /debug/{config,routes,cache,db,scheduler,memory,goroutines}.
	RuntimeEndpoints bool `yaml:"runtime_endpoints"`
}

// MaxBodyLogBytes returns MaxBodyLogSize in bytes, defaulting to 10KB.
func (d DebugConfig) MaxBodyLogBytes() int {
	n, err := ParseSize(d.MaxBodyLogSize, 10*1024)
	if err != nil || n <= 0 {
		return 10 * 1024
	}
	return int(n)
}

// ServerSecurityConfig is the server.security block (AI.md PART 11): at-rest
// encryption key, IP allow/block lists, abuse detection and the downloaded
// security data sources. The security.txt fields stay in web.security.
type ServerSecurityConfig struct {
	// EncryptionKey is the 32-byte AES-256-GCM key (hex-encoded) used for at-rest
	// encryption of sensitive server data (DNS credentials, security reports, etc.).
	// Auto-generated on first run; stored in server.yml; included in every backup.
	EncryptionKey string `yaml:"encryption_key"`
	// Allowlist is a list of IP addresses or CIDR ranges that bypass blocklist,
	// rate limiting, and geoip checks. Auth checks are never bypassed.
	// Single IPs are automatically expanded to /32 (IPv4) or /128 (IPv6).
	Allowlist []AllowlistEntry `yaml:"allowlist"`
	// BlockedIPs are permanent blocks (IP or CIDR): config-file entries only,
	// released manually by removing the entry (PART 11 "IP Block Management").
	BlockedIPs []string `yaml:"blocked_ips"`
	// AbuseDetection configures automatic detection and temporary IP blocking.
	AbuseDetection AbuseDetectionConfig `yaml:"abuse_detection"`
	// Blocklists configures the IP/domain blocklist sources downloaded daily by
	// the scheduler into {data_dir}/security/blocklists/ (PART 18/19).
	Blocklists BlocklistsConfig `yaml:"blocklists"`
	// CVE configures the CVE database source downloaded daily by the scheduler
	// into {data_dir}/security/cve/ (PART 18/19).
	CVE CVEConfig `yaml:"cve"`
	// Trivy configures the Aqua Trivy vulnerability database downloaded daily by
	// the scheduler into {data_dir}/security/trivy/ (PART 18/19).
	Trivy TrivyConfig `yaml:"trivy"`
}

// AllowlistEntry is one server.security.allowlist item (AI.md PART 11): an IP
// or CIDR plus a human-readable label. A bare scalar in server.yml is accepted
// as an entry with no description.
type AllowlistEntry struct {
	CIDR        string `yaml:"cidr" json:"cidr"`
	Description string `yaml:"description" json:"description"`
}

// UnmarshalYAML accepts either the `{cidr, description}` mapping or a bare
// string (older configs).
func (e *AllowlistEntry) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		e.CIDR = strings.TrimSpace(n.Value)
		return nil
	}
	type plain AllowlistEntry
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	p.CIDR = strings.TrimSpace(p.CIDR)
	*e = AllowlistEntry(p)
	return nil
}

// AllowlistCIDRs returns the non-empty allowlist IP/CIDR strings.
func (c ServerSecurityConfig) AllowlistCIDRs() []string {
	out := make([]string, 0, len(c.Allowlist))
	for _, e := range c.Allowlist {
		if e.CIDR != "" {
			out = append(out, e.CIDR)
		}
	}
	return out
}

// BlocklistSource is a single downloadable blocklist: File is the destination
// filename within the blocklists directory; URL is the download source.
type BlocklistSource struct {
	File string `yaml:"file"`
	URL  string `yaml:"url"`
}

// BlocklistsConfig holds the configurable blocklist download sources.
type BlocklistsConfig struct {
	Enabled bool              `yaml:"enabled"`
	Sources []BlocklistSource `yaml:"sources"`
}

// CVEConfig holds the configurable CVE database download settings.
type CVEConfig struct {
	Enabled bool `yaml:"enabled"`
	// File is the destination filename within the cve directory.
	File string `yaml:"file"`
	// Source is the download URL for the CVE feed.
	Source string `yaml:"source"`
	// FilterByCPE restricts the feed to CVEs matching CPE strings derived from
	// the project's dependency manifest. NVD's CPE dictionary does not map
	// cleanly onto Go module paths and no derivation is defined, so guessing
	// wrong would silently skip a real CVE. Defaults to false (full feed).
	FilterByCPE bool `yaml:"filter_by_cpe"`
}

// TrivyConfig holds the Aqua Trivy vulnerability database download settings.
// trivy-db is an OCI artifact rather than a plain HTTP-downloadable file, so
// Source is an OCI repository reference and the tag is the DB schema version.
type TrivyConfig struct {
	Enabled bool `yaml:"enabled"`
	// Source is the OCI reference of the Trivy database artifact.
	Source string `yaml:"source"`
}

// TLSConfig holds SSL/TLS and Let's Encrypt settings (PART 12/15).
// The yaml key is `tls` per PART 15: "TLS config: server.tls.* keys in config".
type TLSConfig struct {
	Enabled bool `yaml:"enabled"`
	// Cert and Key are optional manual certificate override paths.
	// Leave empty for auto-detection (certbot dirs, app-managed, self-signed).
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
	// MinVersion is the minimum negotiated TLS version: TLS1.2 or TLS1.3.
	MinVersion string `yaml:"min_version"`
	// LetsEncrypt holds ACME settings, nested under ssl.letsencrypt per spec.
	LetsEncrypt LetsEncryptConfig `yaml:"letsencrypt"`
	// DNSProvider selects the dns-01 provider name (e.g. cloudflare, rfc2136).
	DNSProvider string `yaml:"dns_provider"`
	// DNSCredentialsEncrypted stores AES-256-GCM encrypted JSON of provider credentials.
	// Encrypted using server.security.encryption_key. Never store plaintext credentials here.
	DNSCredentialsEncrypted string `yaml:"dns_credentials_encrypted"`
}

// LetsEncryptConfig holds ACME settings nested under server.tls.letsencrypt (PART 12/15).
type LetsEncryptConfig struct {
	Enabled bool   `yaml:"enabled"`
	Email   string `yaml:"email"`
	// Challenge selects the ACME challenge type: http-01, tls-alpn-01, dns-01.
	Challenge string `yaml:"challenge"`
	// Staging uses the LE staging CA for testing.
	Staging bool `yaml:"staging"`
}

// BrandingConfig holds site branding shown in the UI and metadata (PART 12/16).
// Tagline, Description, Features, and Links source the /server/about page content
// (PART 16 page-content sourcing); each has a real default drawn from IDEA.md so
// the About page is never blank or generic.
type BrandingConfig struct {
	Title       string   `yaml:"title"`
	Tagline     string   `yaml:"tagline"`
	Description string   `yaml:"description"`
	Favicon     string   `yaml:"favicon"`
	Logo        string   `yaml:"logo"`
	Features    []string `yaml:"features"`
	// ImageRefreshInterval controls how often a remote logo/favicon/OG image
	// URL is re-fetched (PART 16 Image Scaling: "configurable, default:
	// daily"). Accepts a Go duration string (e.g. "24h", "12h"); empty or
	// invalid falls back to the 24h default.
	ImageRefreshInterval string         `yaml:"image_refresh_interval"`
	Links                []BrandingLink `yaml:"links"`
}

// DefaultImageRefreshInterval is the PART 16 Image Scaling default cadence for
// re-fetching a remote branding image (logo/favicon/OG image) URL.
const DefaultImageRefreshInterval = 24 * time.Hour

// EffectiveImageRefreshInterval parses the configured refresh interval,
// falling back to DefaultImageRefreshInterval when empty or invalid.
func (b BrandingConfig) EffectiveImageRefreshInterval() time.Duration {
	if b.ImageRefreshInterval == "" {
		return DefaultImageRefreshInterval
	}
	d, err := time.ParseDuration(b.ImageRefreshInterval)
	if err != nil || d <= 0 {
		return DefaultImageRefreshInterval
	}
	return d
}

// BrandingLink is a single labeled hyperlink shown on the /server/about page.
type BrandingLink struct {
	Label string `yaml:"label"`
	URL   string `yaml:"url"`
}

// defaultBrandingTagline, defaultBrandingDescription, defaultBrandingFeatures, and
// defaultBrandingLinks are the real /server/about defaults sourced from IDEA.md
// (PART 16). They are used both to seed a fresh config and as the runtime fallback
// when an operator leaves a branding field empty.
const (
	defaultBrandingTagline     = "A fast, public paste sharing service — no account required."
	defaultBrandingDescription = "Pastebin is a fast, public paste sharing service. Share code snippets, configuration files, logs, and any text content instantly — no account required. It is a drop-in replacement for pastebin.com, microbin, and lenpaste, so existing scripts work without changes."
)

// defaultBrandingFeatures is the real feature list drawn from the IDEA.md in-scope
// items (PART 16 About page sourcing).
var defaultBrandingFeatures = []string{
	"Syntax highlighting for 20+ languages",
	"Configurable expiry (1 hour to 2 years, or never)",
	"Burn-after-N-views for ephemeral content",
	"Public and unlisted paste visibility",
	"Delete token — pastes are yours to remove",
	"Full REST API with JSON support",
	"Compatible with pastebin.com, microbin, and lenpaste CLIs",
	"No accounts, no tracking, no ads",
}

// defaultBrandingLinks is the real link list drawn from IDEA.md (PART 16).
var defaultBrandingLinks = []BrandingLink{
	{Label: "Source", URL: "https://github.com/apimgr/pastebin"},
	{Label: "apimgr", URL: "https://github.com/apimgr"},
}

// EffectiveTitle returns the configured branding title or the default project name.
func (b BrandingConfig) EffectiveTitle() string {
	if v := strings.TrimSpace(b.Title); v != "" {
		return v
	}
	return "Pastebin"
}

// EffectiveTagline returns the configured tagline or the IDEA.md default.
func (b BrandingConfig) EffectiveTagline() string {
	if v := strings.TrimSpace(b.Tagline); v != "" {
		return v
	}
	return defaultBrandingTagline
}

// EffectiveDescription returns the configured description or the IDEA.md default.
func (b BrandingConfig) EffectiveDescription() string {
	if v := strings.TrimSpace(b.Description); v != "" {
		return v
	}
	return defaultBrandingDescription
}

// EffectiveFeatures returns the configured feature list or the IDEA.md default.
func (b BrandingConfig) EffectiveFeatures() []string {
	if len(b.Features) > 0 {
		return b.Features
	}
	return defaultBrandingFeatures
}

// EffectiveLinks returns the configured link list or the IDEA.md default.
func (b BrandingConfig) EffectiveLinks() []BrandingLink {
	if len(b.Links) > 0 {
		return b.Links
	}
	return defaultBrandingLinks
}

// SEOConfig holds search-engine metadata (PART 12/16).
type SEOConfig struct {
	Keywords      []string              `yaml:"keywords"`
	Author        string                `yaml:"author"`
	OGImage       string                `yaml:"og_image"`
	TwitterHandle string                `yaml:"twitter_handle"`
	Verification  SEOVerificationConfig `yaml:"verification"`
}

// SEOVerificationConfig holds search-engine and social-platform site-verification
// codes rendered as <meta> tags on the homepage (PART 16 "Site Verification").
// Each provider field is optional; a tag is rendered only when its value is
// non-empty and passes the provider's format/length validation.
type SEOVerificationConfig struct {
	Google    string                     `yaml:"google"`
	Bing      string                     `yaml:"bing"`
	Yandex    string                     `yaml:"yandex"`
	Baidu     string                     `yaml:"baidu"`
	Pinterest string                     `yaml:"pinterest"`
	Facebook  string                     `yaml:"facebook"`
	Custom    []SEOCustomVerificationTag `yaml:"custom"`
}

// SEOCustomVerificationTag is an operator-supplied <meta> verification tag not
// covered by a named provider. Exactly one of Name/Property must be set; both
// map directly to the corresponding HTML attribute on the rendered <meta> tag.
type SEOCustomVerificationTag struct {
	Name     string `yaml:"name,omitempty"`
	Property string `yaml:"property,omitempty"`
	Content  string `yaml:"content"`
}

// RestoreConfig is the `server.restore` block (PART 21): hard limits applied
// to every restore/import archive so a crafted backup cannot exhaust disk or
// memory (zip-bomb, file-count and oversized-file protection).
type RestoreConfig struct {
	// MaxArchiveSize is the largest archive accepted (default "1G").
	MaxArchiveSize string `yaml:"max_archive_size"`
	// MaxExpandedSize is the largest total expanded size (default "4G").
	MaxExpandedSize string `yaml:"max_expanded_size"`
	// MaxFiles is the largest number of files in an archive (default 10000).
	MaxFiles int `yaml:"max_files"`
	// MaxFileSize is the largest single extracted file (default "512M").
	MaxFileSize string `yaml:"max_file_size"`
	// MaxRatio is the largest expanded-to-compressed ratio (default 100).
	MaxRatio int `yaml:"max_ratio"`
}

// BackupConfig holds backup, encryption, compliance, and retention settings (PART 21).
type BackupConfig struct {
	Encryption BackupEncryptionConfig `yaml:"encryption"`
	Compliance BackupComplianceConfig `yaml:"compliance"`
	Retention  BackupRetentionConfig  `yaml:"retention"`
}

// BackupEncryptionConfig controls AES-256-GCM + Argon2id backup encryption (PART 21).
type BackupEncryptionConfig struct {
	// Enabled turns on AES-256-GCM encryption for all backup archives.
	// Password is prompted interactively; never stored in config.
	Enabled bool `yaml:"enabled"`
}

// BackupComplianceConfig enables compliance metadata in backup manifests (PART 21).
type BackupComplianceConfig struct {
	// Enabled includes compliance metadata (retention policy, legal hold) in manifests.
	Enabled bool `yaml:"enabled"`
}

// BackupRetentionConfig controls how many backup archives are kept (PART 21).
type BackupRetentionConfig struct {
	// MaxBackups is the total maximum number of backup files to retain.
	MaxBackups  int `yaml:"max_backups"`
	KeepWeekly  int `yaml:"keep_weekly"`
	KeepMonthly int `yaml:"keep_monthly"`
	KeepYearly  int `yaml:"keep_yearly"`
	// MaxTotalSize is a hard size cap on retained backups: a percent of the
	// backup volume ("10%") or an absolute size ("50G", "500MB"); falsey
	// values ("0", "off", ...) disable the cap. Overrides the count limits.
	MaxTotalSize string `yaml:"max_total_size"`
}

// UpdateConfig controls self-update channel and scheduled check behaviour (PART 22).
type UpdateConfig struct {
	// Branch selects the release channel: "stable" (default), "beta", or "devel".
	Branch string `yaml:"branch"`
	// AutoInstall causes the daily update_check task to install eligible updates
	// automatically. Default false — the task notifies only.
	AutoInstall bool `yaml:"auto_install"`
	// DeferDays delays eligibility of a release: a release is only considered once
	// it has been public for this many days (0 = adopt immediately).
	DeferDays int `yaml:"defer_days"`
	// CLIMinVersion is the minimum CLI version the server accepts; returned
	// verbatim as cli_min_version by /api/autodiscover. Empty = no minimum.
	CLIMinVersion string `yaml:"cli_min_version"`
}

// SchedulerConfig configures the built-in task scheduler (PART 12/18).
// Tasks maps a task name to its per-task settings; absent entries use code defaults.
type SchedulerConfig struct {
	// Timezone for cron expressions (default: America/New_York, override with TZ).
	Timezone string `yaml:"timezone"`
	// CatchUpWindow re-runs missed tasks on startup if due within this duration
	// (default: 1h). Accepts Go duration strings ("1h", "30m").
	CatchUpWindow string                   `yaml:"catch_up_window"`
	Tasks         map[string]SchedulerTask `yaml:"tasks"`
}

// SchedulerTask holds the settings for a single scheduled task (PART 12/18).
type SchedulerTask struct {
	Enabled     bool   `yaml:"enabled"`
	Schedule    string `yaml:"schedule"`
	RetryOnFail bool   `yaml:"retry_on_fail"`
	RetryDelay  string `yaml:"retry_delay"`
	// MaxAge and MaxSize apply to log_rotation.
	MaxAge  string `yaml:"max_age"`
	MaxSize string `yaml:"max_size"`
	// Retention applies to the backup task (max backups to keep).
	Retention int `yaml:"retention"`
	// RenewBefore applies to ssl_renewal (e.g. "7d").
	RenewBefore string `yaml:"renew_before"`
}

// HSTSConfig controls Strict-Transport-Security emission.
type HSTSConfig struct {
	Enabled bool `yaml:"enabled"`
	// MaxAgeSeconds is the HSTS max-age (default 63072000, 2 years).
	MaxAgeSeconds int64 `yaml:"max_age_seconds"`
	// IncludeSubdomains sets the includeSubDomains directive (default true).
	IncludeSubdomains bool `yaml:"include_subdomains"`
	// Preload sets the preload directive (default true).
	Preload bool `yaml:"preload"`
}

// CSPConfig controls Content-Security-Policy emission.
type CSPConfig struct {
	Enabled bool `yaml:"enabled"`
	// Mode is "enforce" or "report-only".
	Mode              string `yaml:"mode"`
	ScriptSrcExtra    string `yaml:"script_src_extra"`
	StyleSrcExtra     string `yaml:"style_src_extra"`
	ImgSrcExtra       string `yaml:"img_src_extra"`
	FontSrcExtra      string `yaml:"font_src_extra"`
	ConnectSrcExtra   string `yaml:"connect_src_extra"`
	FrameSrcExtra     string `yaml:"frame_src_extra"`
	FormActionExtra   string `yaml:"form_action_extra"`
	ScriptSrcOverride string `yaml:"script_src_override"`
	// frame-ancestors value for the embeddable /emb/{id} endpoint only.
	// Default "*" (any site may iframe embeds); operator can restrict to
	// specific origins, e.g. "'self' https://example.com". Empty = "*".
	EmbedFrameAncestors string `yaml:"embed_frame_ancestors"`
}

// DefaultConfig returns a config with sensible defaults.
// Server.Port is intentionally empty so that Load + ResolvePort can apply
// the "random 64xxx on first run" rule described in PART 5.
func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Debug: DebugConfig{
				Pprof:                true,
				LogQueries:           true,
				LogCache:             true,
				MaxBodyLogSize:       "10KB",
				BlockProfileRate:     1,
				MutexProfileFraction: 1,
				RuntimeEndpoints:     true,
			},
			Security: ServerSecurityConfig{
				// auto-generated on first run
				EncryptionKey: "",
				AbuseDetection: AbuseDetectionConfig{
					RequestFlood: RequestFloodConfig{Multiplier: 10, BlockDuration: "1h"},
					AutoBlockIP:  true,
					AutoAlert:    true,
				},
				Blocklists: BlocklistsConfig{
					Enabled: true,
					Sources: []BlocklistSource{
						{
							File: "firehol_level1.txt",
							URL:  "https://raw.githubusercontent.com/firehol/blocklist-ipsets/master/firehol_level1.netset",
						},
						{
							File: "spamhaus_drop.txt",
							URL:  "https://www.spamhaus.org/drop/drop.txt",
						},
					},
				},
				CVE: CVEConfig{
					Enabled:     false,
					File:        "nvd.json",
					Source:      "https://services.nvd.nist.gov/rest/json/cves/2.0",
					FilterByCPE: false,
				},
				Trivy: TrivyConfig{
					Enabled: false,
					Source:  "ghcr.io/aquasecurity/trivy-db:2",
				},
			},
			Port:       "",
			Address:    "0.0.0.0",
			FQDN:       "",
			Mode:       "production",
			APIVersion: "v1",
			Tokens:     OwnerTokensConfig{MaxLinked: 25, CookieMaxAge: "1y"},
			Branding: BrandingConfig{
				Title:       "Pastebin",
				Tagline:     defaultBrandingTagline,
				Description: defaultBrandingDescription,
				Features:    defaultBrandingFeatures,
				Links:       defaultBrandingLinks,
			},
			SEO: SEOConfig{
				Keywords: []string{},
			},
			PIDFile:   true,
			Daemonize: false,
			TLS: TLSConfig{
				MinVersion: "TLS1.2",
				LetsEncrypt: LetsEncryptConfig{
					Challenge: "http-01",
				},
			},
			Scheduler: SchedulerConfig{
				Tasks: map[string]SchedulerTask{},
			},
			Metrics: MetricsConfig{
				Enabled:        true,
				Endpoint:       "/metrics",
				IncludeSystem:  true,
				IncludeRuntime: true,
				// Token is the prometheus-service token, kept for compatibility
				// with configs that predate auth.tokens.
				Token: "",
				// PART 20: no unauthenticated default, so the escape hatch is
				// off and every service token starts empty. An empty token
				// disables that service (403), so out of the box /metrics is
				// mounted but serves nothing until an operator sets a token.
				Auth: MetricsAuthConfig{
					AllowUnauthenticated: false,
					Tokens: MetricsTokensConfig{
						Prometheus: "",
						Grafana:    "",
						Loki:       "",
					},
				},
				// Prometheus scrapers default to /metrics, so the root alias is
				// on by default.
				Root: MetricsRootConfig{Enabled: true},
				Loki: MetricsLokiConfig{
					MaxEntries: 1000,
					MaxAge:     "1h",
				},
				DurationBuckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
				SizeBuckets:     []float64{100, 1000, 10000, 100000, 1000000, 10000000},
			},
			Cors: CORSConfig{
				AllowedOrigins:   []string{"*"},
				AllowCredentials: true,
				MaxAge:           86400,
			},
			Healthz: HealthzConfig{},
			MCP:     MCPConfig{Enabled: true},
			RateLimit: RateLimitConfig{
				Enabled:     true,
				Read:        RateLimitEndpoint{Requests: 120, Window: 60},
				Write:       RateLimitEndpoint{Requests: 10, Window: 60},
				Health:      RateLimitEndpoint{Requests: 120, Window: 60},
				GlobalBurst: 240,
			},
			Compression: CompressionConfig{
				Enabled: true,
				Level:   5,
				Types:   []string{"text/html", "text/css", "text/javascript", "application/json", "application/xml", "text/plain"},
			},
			Restore: RestoreConfig{
				MaxArchiveSize:  "1G",
				MaxExpandedSize: "4G",
				MaxFiles:        10000,
				MaxFileSize:     "512M",
				MaxRatio:        100,
			},
			Compliance: ComplianceConfig{},
			GeoIP: GeoIPConfig{
				Enabled: true,
				// resolved at startup to {data_dir}/security/geoip
				Dir:            "",
				DenyCountries:  []string{},
				AllowCountries: []string{},
				// Operator-authored only; never bundled or auto-applied.
				Presets: map[string][]string{},
				Databases: GeoIPDatabasesConfig{
					ASN:     true,
					Country: true,
					City:    true,
				},
			},
			Tor: TorConfig{
				Binary:                    "",
				UseNetwork:                false,
				MaxCircuits:               32,
				CircuitTimeout:            60,
				BootstrapTimeout:          180,
				SafeLogging:               true,
				MaxStreamsPerCircuit:      100,
				CloseCircuitOnStreamLimit: true,
				BandwidthRate:             "1 MB",
				BandwidthBurst:            "2 MB",
				MaxMonthlyBandwidth:       "100 GB",
				NumIntroPoints:            3,
				VirtualPort:               80,
			},
			I2P: I2PConfig{
				Enabled:          false,
				Binary:           "",
				SAMAddress:       "127.0.0.1:7656",
				VirtualPort:      80,
				InboundLength:    3,
				OutboundLength:   3,
				InboundQuantity:  5,
				OutboundQuantity: 5,
				SignatureType:    7,
				BootstrapTimeout: "5m",
			},
			Logging: LoggingConfig{
				Level: "info",
				Access: AccessLogConfig{
					Filename:        "access.log",
					Format:          "apache",
					Custom:          "",
					Rotate:          "monthly",
					Keep:            "none",
					LogHealthChecks: false,
				},
				Server: LogFileConfig{
					Filename: "server.log",
					Format:   "text",
					Rotate:   "weekly,50MB",
					Keep:     "none",
				},
				Error: LogFileConfig{
					Filename: "error.log",
					Format:   "text",
					Rotate:   "weekly,50MB",
					Keep:     "none",
				},
				App: LogFileConfig{
					Filename: "app.log",
					Format:   "logfmt",
					Rotate:   "weekly,50MB",
					Keep:     "none",
				},
				Auth: LogFileConfig{
					Filename: "auth.log",
					Format:   "syslog",
					Rotate:   "weekly,50MB",
					Keep:     "none",
				},
				Security: LogFileConfig{
					Filename: "security.log",
					Format:   "fail2ban",
					Custom:   "",
					Rotate:   "weekly,50MB",
					Keep:     "none",
				},
				Debug: DebugLogConfig{
					Enabled:  true,
					Filename: "debug.log",
					Format:   "text",
					Custom:   "",
					Rotate:   "weekly,50MB",
					Keep:     "none",
				},
				Audit: AuditConfig{
					Enabled:          true,
					Filename:         "audit.log",
					Format:           "json",
					Rotate:           "daily",
					Keep:             "none",
					Compress:         false,
					IncludeUserAgent: true,
					MaskEmails:       true,
					TokenUsage:       false,
					Events: AuditEventCategories{
						Configuration: true,
						Security:      true,
						Backup:        true,
						Server:        true,
					},
				},
			},
			Notifications: NotificationsConfig{
				Email: EmailConfig{
					Enabled: false,
					SMTP: SMTPConfig{
						Host: "",
						Port: 587,
						TLS:  "auto",
					},
					// Per-event defaults from AI.md:26591-26597: high-signal events on, low-noise off.
					Events: EmailEventsConfig{
						Startup:          false,
						Shutdown:         false,
						BackupComplete:   false,
						BackupFailed:     true,
						SSLExpiring:      true,
						SSLRenewed:       false,
						SSLRenewalFailed: true,
						SecurityAlert:    true,
						SchedulerError:   true,
						UpdateAvailable:  false,
						UpdateInstalled:  true,
					},
				},
			},
			I18n: I18nConfig{
				Enabled:            true,
				DefaultLanguage:    "en",
				AvailableLanguages: []string{"en", "es", "zh", "fr", "ar", "de", "ja"},
				FallbackLanguage:   "en",
				CookieName:         "lang",
				CookieMaxAge:       "365d",
			},
			Update: UpdateConfig{
				Branch:      "stable",
				AutoInstall: false,
				DeferDays:   0,
			},
			Backup: BackupConfig{
				Encryption: BackupEncryptionConfig{Enabled: false},
				Compliance: BackupComplianceConfig{Enabled: false},
				Retention: BackupRetentionConfig{
					MaxBackups:   1,
					KeepWeekly:   0,
					KeepMonthly:  0,
					KeepYearly:   0,
					MaxTotalSize: "10%",
				},
			},
			Cache: CacheConfig{
				Type:     "memory",
				Host:     "localhost",
				Port:     6379,
				PoolSize: 10,
				MinIdle:  2,
				Timeout:  "5s",
				Prefix:   "pastebin:",
				TTL:      "1h",
			},
			Limits: LimitsConfig{
				// 10 MiB
				MaxBodySize:  10 << 20,
				ReadTimeout:  "30s",
				WriteTimeout: "30s",
				IdleTimeout:  "120s",
			},
			TrustedProxies: TrustedProxiesConfig{
				Additional: []string{},
			},
			URLDetection: URLDetectionConfig{
				Learning:     true,
				MinSamples:   3,
				SampleWindow: 5 * time.Minute,
				LogChanges:   true,
				LiveReload:   true,
			},
			Termbin: TermbinConfig{
				Enabled: true,
				Port:    9999,
				MaxSize: 32768,
				Timeout: "5s",
			},
			Maintenance: MaintenanceConfig{
				SelfHealing: MaintenanceSelfHealingConfig{
					Enabled:       true,
					RetryInterval: "30s",
					MaxAttempts:   0,
				},
				Cleanup: MaintenanceCleanupConfig{
					DiskThreshold:    90,
					LogRetentionDays: 7,
					BackupKeepCount:  5,
				},
				Notify: MaintenanceNotifyConfig{
					OnEnter: true,
					OnExit:  true,
				},
			},
			Contact: ContactConfig{
				Admin:    ContactRole{Email: "admin@{fqdn}", Webhooks: map[string]string{}},
				Security: ContactRole{Email: "security@{fqdn}", Webhooks: map[string]string{}},
				Abuse:    ContactRole{Email: "", Webhooks: map[string]string{}},
				General:  ContactRole{Email: "", Webhooks: map[string]string{}},
			},
			Pages: PagesConfig{
				Contact: ContactPageConfig{
					Enabled:        true,
					Captcha:        "simple",
					SuccessMessage: "Thank you for your message. We'll respond soon.",
				},
			},
			Privacy: PrivacyConfig{
				Data: DataPolicy{
					Sold:           false,
					StoredOnServer: true,
					Sharing: []SharingCondition{
						{
							Condition: "analytics",
							When:      "Tracking configured (server.tracking.type set) AND user consents",
							Data:      "Anonymized: page views, browser type, country",
						},
						{
							Condition: "email",
							When:      "SMTP configured for sending emails",
							Data:      "Email address, message content",
						},
						{
							Condition: "user_initiated",
							When:      "User explicitly shares content (social buttons, exports)",
							Data:      "Whatever user chooses to share",
						},
					},
				},
				Retention: RetentionPolicy{
					Period:            "Account data is retained while your account is active. Upon account deletion, all personal data is permanently deleted within 30 days. Anonymized analytics data may be retained for up to 12 months.",
					ExportAvailable:   true,
					DeletionAvailable: true,
				},
				Consent: ConsentConfig{
					ShowUntilAcknowledged: true,
					DefaultEnabled:        true,
					Message:               "In accordance with the EU GDPR law this message is being displayed. We use cookies for essential site functionality and, with your consent, for preferences and analytics. Your data is stored on our servers and is never sold.",
					MessageIfSold:         "In accordance with the EU GDPR law this message is being displayed. We use cookies for essential site functionality and, with your consent, for preferences and analytics. Your data may be shared with or sold to third parties as described in our Privacy Policy.",
					Policy: ConsentPolicyLink{
						Text: "Privacy Policy",
						URL:  "/server/privacy",
					},
					Buttons: ConsentButtons{
						Decline: "Decline",
						Accept:  "I Agree",
					},
					Position:        "bottom",
					ShowPreferences: true,
					PreferencesText: "Manage Preferences",
				},
				Cookies: CookieCategories{
					Essential: CookieCategory{
						Enabled:     true,
						Description: "Required for the site to function. Includes security tokens (CSRF) and site preferences. These cookies are strictly necessary and cannot be disabled.",
					},
					Preferences: CookieCategory{
						Enabled:     true,
						Description: "Remember your settings such as theme (dark/light), language, and UI preferences. Disabling will reset to defaults on each visit.",
					},
					Analytics: AnalyticsCookie{
						CookieCategory: CookieCategory{
							Enabled:     true,
							Description: "Help us understand how visitors use our site to improve the experience.",
						},
						DescriptionSuffixNotSold: "Analytics data is anonymized and never sold.",
						DescriptionSuffixSold:    "Analytics data may be shared with third parties.",
					},
				},
				ThirdParty: ThirdPartyConfig{
					Services: []ThirdPartyService{},
				},
				Content: PrivacyContent{
					DataCollection:  "**We collect only what is necessary to provide our service:**\n\n**Usage Information (with consent):**\n- Pages visited and features used\n- Browser type and device information\n- Approximate location (country/region from IP, not precise)\n\n**Technical Information:**\n- IP address (for security and abuse prevention)\n- API token identifiers (hashed, never stored in plain text)\n\n**We do NOT collect:**\n- Payment information (unless explicitly required by the service)\n- Precise location data\n- Data from other websites or apps\n",
					DataUsage:       "**Your data is used solely to:**\n\n- **Provide the service:** Authentication, core functionality\n- **Improve the experience:** Performance optimization, bug fixes, feature improvements\n- **Ensure security:** Prevent abuse, detect fraud, protect your account\n- **Communicate:** Service updates, security alerts, and (with consent) product news\n\n**Your data is NEVER:**\n- Sold to third parties\n- Used for targeted advertising\n- Shared without your explicit consent (except as required by law)\n",
					DataUsageIfSold: "**Your data may be used to:**\n\n- **Provide the service:** Authentication, core functionality\n- **Improve the experience:** Performance optimization, bug fixes, feature improvements\n- **Ensure security:** Prevent abuse, detect fraud, protect your account\n- **Communicate:** Service updates, security alerts, and (with consent) product news\n- **Third-party sharing:** Your data may be shared with or sold to third parties for analytics, advertising, or other purposes as described below\n\n**Your rights:**\n- You can opt out of data sales via your account privacy settings\n- You can request deletion of your data at any time\n- See \"Your Rights\" section below for details\n",
					DataSecurity:    "**How we protect your data:**\n\n- All data is stored on our servers (not third-party cloud services unless specified)\n- API tokens are stored as SHA-256 hashes (never in plain text)\n- All connections are encrypted (HTTPS/TLS)\n- Regular security audits and updates\n- Access controls and audit logging for operator actions\n",
				},
			},
			Tracking: TrackingConfig{
				Type: "",
				ID:   "",
				URL:  "",
			},
		},
		Database: DatabaseConfig{
			Type: "sqlite",
			// resolved at startup relative to data dir
			Path: "",
			Pool: DatabasePoolConfig{
				MaxOpen:     5,
				MaxIdle:     2,
				MaxLifetime: 5 * time.Minute,
				MaxIdleTime: time.Minute,
			},
		},
		Paste: PasteConfig{
			// 10 MiB
			MaxSize:         "10mb",
			MaxSizeBytes:    10 << 20,
			DefaultExpiry:   "never",
			DefaultLanguage: "text",
			MaxBurnAfter:    9999,
			AllowUnlisted:   true,
		},
		Web: WebConfig{
			SiteTitle: "Pastebin",
			Theme:     "dark",
			Robots: RobotsConfig{
				Allow: []string{"/"},
				Deny:  []string{},
			},
			HSTS: HSTSConfig{
				Enabled: true,
				// 2 years (preload-list eligible)
				MaxAgeSeconds:     63072000,
				IncludeSubdomains: true,
				Preload:           true,
			},
			CSP: CSPConfig{
				Enabled:             true,
				Mode:                "enforce",
				EmbedFrameAncestors: "*",
			},
			Headers: HeadersConfig{
				SecFetchValidation: true,
			},
			CSRF: CSRFConfig{
				Enabled:     true,
				TokenLength: 32,
				CookieName:  "csrf_token",
				HeaderName:  "X-CSRF-Token",
				Secure:      "auto",
				ExemptPaths: []string{},
			},
			Announcements: AnnouncementsConfig{
				Enabled:  true,
				Messages: []AnnouncementMessage{},
			},
		},
	}
}

// Load reads config from path, creating it with defaults if absent.
// On first run (or when encryption_key is empty) an AES-256-GCM key is
// auto-generated and persisted to server.yml immediately.
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()
	cfg.loadEnv()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if genErr := cfg.ensureEncryptionKey(); genErr != nil {
				log.Printf("config: warning: could not generate encryption key: %v", genErr)
			}
			if genErr := cfg.ensureServerToken(); genErr != nil {
				log.Printf("config: warning: could not generate server token: %v", genErr)
			} else {
				log.Printf("config: generated server.token — copy it from %s", path)
			}
			// Write canonical defaults to disk. Never bake runtime env-var overrides
			// (MODE, DEBUG, etc.) into the persisted config — env vars are runtime-only.
			// Copy only the generated secrets so they survive the next startup.
			saveCfg := DefaultConfig()
			saveCfg.Server.FQDN = cfg.ResolveFQDN()
			saveCfg.Server.Security.EncryptionKey = cfg.Server.Security.EncryptionKey
			saveCfg.Server.Token = cfg.Server.Token
			if saveErr := Save(path, saveCfg); saveErr != nil {
				log.Printf("config: warning: could not persist generated secrets to %s: %v", path, saveErr)
			}
			cfg.FirstRun = true
			return cfg, nil
		}
		return cfg, err
	}

	// A YAML type/unknown-key error (*yaml.TypeError) still fills every valid
	// field, so keep going: validate, generate any missing secret, and hand the
	// caller a usable config plus the error to warn about. Returning early would
	// leave the encryption key and operator token empty and break token
	// creation over a single typo (PART 5: never fail on invalid config —
	// warn and use the default). A real syntax error is still returned as-is.
	var decodeErr error
	data = migrateLegacyKeys(data)
	if err := decodeConfig(data, cfg); err != nil {
		var typeErr *yaml.TypeError
		if !errors.As(err, &typeErr) {
			return cfg, err
		}
		decodeErr = err
	}

	// Env always wins over file.
	cfg.loadEnv()

	// Validate and sanitize — replace invalid values with defaults, never crash.
	Validate(cfg)

	// Show the operator exactly what a configured footer.custom_html will
	// render as, once it has survived Validate() (PART 16 Sanitization Preview).
	LogFooterSanitizationPreview(cfg.Web.Footer.CustomHTML)

	// Generate and persist encryption key if missing (upgrade path for older configs).
	needSave := false
	if cfg.Server.Security.EncryptionKey == "" {
		if genErr := cfg.ensureEncryptionKey(); genErr != nil {
			log.Printf("config: warning: could not generate encryption key: %v", genErr)
		} else {
			needSave = true
		}
	}

	// Generate and persist server.token if missing (upgrade path for older configs).
	if cfg.Server.Token == "" {
		if genErr := cfg.ensureServerToken(); genErr != nil {
			log.Printf("config: warning: could not generate server token: %v", genErr)
		} else {
			needSave = true
			log.Printf("config: generated server.token — copy it from %s", path)
		}
	}

	// Generate and persist a per-webhook signing secret for every configured
	// webhook URL that lacks one (PART 17).
	if gen, genErr := cfg.ensureWebhookSecrets(); genErr != nil {
		log.Printf("config: warning: could not generate webhook secret: %v", genErr)
	} else if gen {
		needSave = true
	}

	// Never rewrite a file that held invalid entries: Save would silently drop
	// them, so the operator could not see what to fix.
	if needSave && decodeErr == nil {
		if saveErr := Save(path, cfg); saveErr != nil {
			log.Printf("config: warning: could not persist generated secrets to %s: %v", path, saveErr)
		}
	}

	return cfg, decodeErr
}

// ensureEncryptionKey generates a 32-byte AES-256-GCM key and stores it
// hex-encoded in Server.Security.EncryptionKey. Idempotent — no-ops if already set.
func (c *Config) ensureEncryptionKey() error {
	if c.Server.Security.EncryptionKey != "" {
		return nil
	}
	var key [32]byte
	if _, err := crand.Read(key[:]); err != nil {
		return err
	}
	c.Server.Security.EncryptionKey = fmt.Sprintf("%x", key)
	return nil
}

// tokenCharset is the base62 alphabet used for operator and resource-owner tokens (PART 11).
const tokenCharset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// ensureServerToken generates a "tok_" + 32 base62 char operator token and
// stores it in Server.Token. Idempotent — no-ops if already set.
func (c *Config) ensureServerToken() error {
	if c.Server.Token != "" {
		return nil
	}
	raw := make([]byte, 32)
	if _, err := crand.Read(raw); err != nil {
		return err
	}
	b := make([]byte, 32)
	for i, v := range raw {
		b[i] = tokenCharset[int(v)%len(tokenCharset)]
	}
	c.Server.Token = "tok_" + string(b)
	return nil
}

// EncryptionKey returns the decoded 32-byte AES-256-GCM key, or an error if
// the key is absent or malformed.
func (c *Config) EncryptionKey() ([]byte, error) {
	s := c.Server.Security.EncryptionKey
	if s == "" {
		return nil, fmt.Errorf("encryption_key not configured")
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("encryption_key is not valid hex: %w", err)
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("encryption_key must be 32 bytes (got %d)", len(b))
	}
	return b, nil
}

// EncryptDNSCredentials JSON-marshals creds, seals it with AES-256-GCM using
// EncryptionKey(), and returns the result base64-encoded for storage in
// server.tls.dns_credentials_encrypted (PART 15: DNS-01 provider credentials
// are never stored in plaintext).
func (c *Config) EncryptDNSCredentials(creds map[string]string) (string, error) {
	key, err := c.EncryptionKey()
	if err != nil {
		return "", fmt.Errorf("dns credentials: %w", err)
	}
	plain, err := json.Marshal(creds)
	if err != nil {
		return "", fmt.Errorf("dns credentials: marshal: %w", err)
	}
	sealed, err := secretbox.Seal(key, plain)
	if err != nil {
		return "", fmt.Errorf("dns credentials: seal: %w", err)
	}
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// DecryptDNSCredentials decodes and opens
// server.tls.dns_credentials_encrypted, returning the provider credential
// map. Following the config package's warn-and-default pattern, an unset
// DNSCredentialsEncrypted is not an error — it returns an empty, non-nil
// map so callers can range over it safely.
func (c *Config) DecryptDNSCredentials() (map[string]string, error) {
	s := c.Server.TLS.DNSCredentialsEncrypted
	if s == "" {
		return map[string]string{}, nil
	}
	key, err := c.EncryptionKey()
	if err != nil {
		return nil, fmt.Errorf("dns credentials: %w", err)
	}
	sealed, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("dns credentials: decode: %w", err)
	}
	plain, err := secretbox.Open(key, sealed)
	if err != nil {
		return nil, fmt.Errorf("dns credentials: open: %w", err)
	}
	var creds map[string]string
	if err := json.Unmarshal(plain, &creds); err != nil {
		return nil, fmt.Errorf("dns credentials: unmarshal: %w", err)
	}
	if creds == nil {
		creds = map[string]string{}
	}
	return creds, nil
}

// projectName is the AI.md {project_name} placeholder (PART 12 dynamic
// project TLD, e.g. "app.pastebin"). The config package cannot import
// package main (which owns the appName constant) without an import cycle,
// so the literal is duplicated here.
const projectName = "pastebin"

// devModeForValidation reports whether host validation should apply
// development-mode relaxations (PART 12). It reflects config.Server.Mode as
// resolved at the time loadEnv() runs (config file default, then MODE env
// var); the final --mode CLI flag override happens later in main and is out
// of reach of the config package.
func (c *Config) devModeForValidation() bool {
	return strings.EqualFold(strings.TrimSpace(c.Server.Mode), "development")
}

// fqdn returns the server FQDN used to expand {fqdn} tokens in contact
// addresses. It follows the AI.md {fqdn} resolution order (PART 12): the
// configured/DOMAIN value first, then os.Hostname(), then $HOSTNAME, then the
// first public IP, and only "localhost" as the last resort. Reverse-proxy
// header detection (priority 1) is request-scoped and handled at the HTTP layer,
// so it is out of scope here. The $HOSTNAME environment variable MUST pass
// host validation for the current mode; an invalid value is skipped silently
// and detection continues to the next source (PART 12).
func (c *Config) fqdn() string {
	if f := strings.TrimSpace(c.Server.FQDN); f != "" && !strings.EqualFold(f, "localhost") {
		return f
	}
	devMode := c.devModeForValidation()
	if h, err := os.Hostname(); err == nil {
		if h = strings.TrimSpace(h); h != "" && !strings.EqualFold(h, "localhost") {
			// A bare container/host name (e.g. Docker's auto-assigned
			// short hostname "pastebin") is not a usable public FQDN —
			// validate it the same way the $HOSTNAME env var below is
			// validated, and fall through to the next source on failure.
			if IsValidHost(h, devMode, projectName) {
				return h
			}
		}
	}
	if h := strings.TrimSpace(os.Getenv("HOSTNAME")); h != "" && !strings.EqualFold(h, "localhost") {
		if IsValidHost(h, devMode, projectName) {
			return h
		}
	}
	if ip := PublicIP(); ip != "" {
		return ip
	}
	return "localhost"
}

// publicIPMu guards the cached public IP refreshed by the public_ip_refresh
// scheduler task (startup step 16, AI.md:10593).
var publicIPMu sync.RWMutex

// cachedPublicIP holds the last detected public IP; refreshed at startup and
// every 12h by the public_ip_refresh task.
var cachedPublicIP string

// publicIPCached is true once RefreshPublicIP has run at least once.
var publicIPCached bool

// PublicIP returns the cached first public interface address, detecting it on
// first use when the public_ip_refresh task has not populated the cache yet.
func PublicIP() string {
	publicIPMu.RLock()
	if publicIPCached {
		ip := cachedPublicIP
		publicIPMu.RUnlock()
		return ip
	}
	publicIPMu.RUnlock()
	return RefreshPublicIP()
}

// RefreshPublicIP re-scans local interfaces for the first public address and
// updates the cache. Called at startup and every 12h by the hardcoded
// public_ip_refresh scheduler task (AI.md:10593).
func RefreshPublicIP() string {
	ip := firstPublicIP()
	publicIPMu.Lock()
	cachedPublicIP = ip
	publicIPCached = true
	publicIPMu.Unlock()
	return ip
}

// ResolveFQDN exposes the configuration-scoped {fqdn} resolution chain (PART 12
// priorities 2–7: configured/DOMAIN value → os.Hostname() → $HOSTNAME → first
// public IP → "localhost") to the HTTP layer. The request-scoped reverse-proxy
// header step (priority 1) is applied by the caller before falling back here.
func (c *Config) ResolveFQDN() string {
	return c.fqdn()
}

// firstPublicIP returns the first globally routable unicast address bound to a
// local interface, preferring IPv6 over IPv4, or "" when none is found. Private,
// loopback, and link-local addresses are excluded (AI.md PART 12 {fqdn}
// resolution priorities 5 and 6).
func firstPublicIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	var v4 string
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP
		if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
			continue
		}
		if ip.To4() == nil {
			return ip.String()
		}
		if v4 == "" {
			v4 = ip.String()
		}
	}
	return v4
}

// expandFQDN substitutes the {fqdn} placeholder in a contact value with the
// resolved server FQDN.
func (c *Config) expandFQDN(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), "{fqdn}", c.fqdn())
}

// AdminEmail returns the resolved admin recipient — the universal fallback for
// every other role (PART 12). Falls back to admin@{fqdn} when unset.
func (c *Config) AdminEmail() string {
	if e := c.expandFQDN(c.Server.Contact.Admin.Email); e != "" {
		return e
	}
	return "admin@" + c.fqdn()
}

// SecurityEmail returns the resolved security-report recipient (PART 12).
// Empty → falls back to the admin address. Public: security.txt Contact line.
func (c *Config) SecurityEmail() string {
	if e := c.expandFQDN(c.Server.Contact.Security.Email); e != "" {
		return e
	}
	return c.AdminEmail()
}

// SecurityReportEmail returns the operator recipient for coordinated-disclosure
// security reports (PART 11). Prefers web.security.contact_email, then falls
// back to the canonical security.txt contact (SecurityEmail).
func (c *Config) SecurityReportEmail() string {
	if e := c.expandFQDN(strings.TrimSpace(c.Web.Security.ContactEmail)); e != "" {
		return e
	}
	return c.SecurityEmail()
}

// PublishPGPKeyEnabled reports whether the project PGP key is published
// (pgp-key.asc, security.txt Encryption line, keyserver submission) (PART 11).
func (c *Config) PublishPGPKeyEnabled() bool {
	return IsTruthy(c.Web.Security.PublishPGPKey)
}

// SecurityReportURL returns the primary security.txt Contact line — the repo's
// GitHub private vulnerability reporting URL, listed first per RFC 9116 (PART
// 11). Falls back to the canonical apimgr/pastebin advisories endpoint.
func (c *Config) SecurityReportURL() string {
	if u := strings.TrimSpace(c.Web.Security.ReportURL); u != "" {
		return u
	}
	return "https://github.com/apimgr/pastebin/security/advisories/new"
}

// SecurityPreferredLanguages returns the security.txt Preferred-Languages value
// (RFC 5646 tags, comma-separated). Empty config → "en" (PART 11).
func (c *Config) SecurityPreferredLanguages() string {
	if l := strings.TrimSpace(c.Web.Security.PreferredLanguages); l != "" {
		return l
	}
	return "en"
}

// GeneralEmail returns the resolved /server/contact recipient (PART 12).
// Empty → falls back to the admin address. Public: footer "Contact us".
func (c *Config) GeneralEmail() string {
	if e := c.expandFQDN(c.Server.Contact.General.Email); e != "" {
		return e
	}
	return c.AdminEmail()
}

// GeneralEmailPublic returns the /server/contact address ONLY when the operator
// explicitly set server.contact.general.email; otherwise it returns "" (PART 12).
// Unlike GeneralEmail it never falls back to the admin address, because the admin
// email is server-internal and MUST NEVER be exposed publicly (AI.md Privacy &
// Public Exposure: admin.email NEVER public). The contact form still delivers to
// the admin fallback via GeneralEmail; only the displayed "Contact us" address is
// suppressed when general is unset.
func (c *Config) GeneralEmailPublic() string {
	return c.expandFQDN(c.Server.Contact.General.Email)
}

// AbuseEmail returns the resolved abuse-report delivery recipient (PART 12):
// abuse.email if set → general.email if set → admin.email. Used for routing
// incoming abuse reports; never auto-advertises abuse@{fqdn}.
func (c *Config) AbuseEmail() string {
	if e := c.expandFQDN(c.Server.Contact.Abuse.Email); e != "" {
		return e
	}
	return c.GeneralEmail()
}

// AbuseEmailPublic returns the address shown in the contact page "Abuse Reports"
// section (PART 12): abuse.email if explicitly set → else the public general
// address (general.email if set, otherwise ""). It NEVER falls back to the admin
// address, which is server-internal and must not be exposed publicly.
func (c *Config) AbuseEmailPublic() string {
	if e := c.expandFQDN(c.Server.Contact.Abuse.Email); e != "" {
		return e
	}
	return c.GeneralEmailPublic()
}

// ContactWebhook returns the webhook URL for a role+transport, falling back
// through the role's documented chain to the admin transport when empty (PART
// 12). abuse → general → admin; security/general → admin.
func (c *Config) ContactWebhook(role, transport string) string {
	pick := func(r ContactRole) string {
		if r.Webhooks == nil {
			return ""
		}
		return strings.TrimSpace(r.Webhooks[transport])
	}
	var v string
	switch role {
	case "security":
		v = pick(c.Server.Contact.Security)
	case "abuse":
		if v = pick(c.Server.Contact.Abuse); v == "" {
			v = pick(c.Server.Contact.General)
		}
	case "general":
		v = pick(c.Server.Contact.General)
	default:
		v = pick(c.Server.Contact.Admin)
	}
	if v != "" {
		return v
	}
	return pick(c.Server.Contact.Admin)
}

// webhookSecretSuffix is appended to a transport name to form the config key
// that stores its per-webhook signing secret (PART 17): webhooks.<name>_secret.
const webhookSecretSuffix = "_secret"

// WebhookTarget is a resolved webhook destination for dispatch: the transport
// adapter name, the destination URL, and the per-webhook signing secret.
type WebhookTarget struct {
	Transport string
	URL       string
	Secret    string
}

// contactRole returns the ContactRole for a role name
// (admin/security/abuse/general), defaulting to admin for any unknown name.
func (c *Config) contactRole(role string) ContactRole {
	switch role {
	case "security":
		return c.Server.Contact.Security
	case "abuse":
		return c.Server.Contact.Abuse
	case "general":
		return c.Server.Contact.General
	default:
		return c.Server.Contact.Admin
	}
}

// WebhookSecret returns the per-webhook signing secret for a role+transport,
// paired with whichever role actually supplies the URL (role-specific value if
// set, else the admin fallback) (PART 17).
func (c *Config) WebhookSecret(role, transport string) string {
	r := c.contactRole(role)
	if r.Webhooks != nil && strings.TrimSpace(r.Webhooks[transport]) != "" {
		return strings.TrimSpace(r.Webhooks[transport+webhookSecretSuffix])
	}
	if c.Server.Contact.Admin.Webhooks != nil {
		return strings.TrimSpace(c.Server.Contact.Admin.Webhooks[transport+webhookSecretSuffix])
	}
	return ""
}

// WebhookTargets returns every configured webhook destination for a role,
// applying the admin-role fallback and pairing each URL with its signing
// secret. `_secret` companion keys are not treated as transports (PART 17).
func (c *Config) WebhookTargets(role string) []WebhookTarget {
	seen := map[string]bool{}
	var out []WebhookTarget
	add := func(r ContactRole) {
		for name := range r.Webhooks {
			if strings.HasSuffix(name, webhookSecretSuffix) {
				continue
			}
			if seen[name] {
				continue
			}
			u := c.ContactWebhook(role, name)
			if strings.TrimSpace(u) == "" {
				continue
			}
			seen[name] = true
			out = append(out, WebhookTarget{Transport: name, URL: u, Secret: c.WebhookSecret(role, name)})
		}
	}
	add(c.contactRole(role))
	// Abuse resolves abuse → general → admin, so enumerate general's transports
	// too before the shared admin fallback (PART 12).
	if role == "abuse" {
		add(c.Server.Contact.General)
	}
	add(c.Server.Contact.Admin)
	return out
}

// ensureWebhookSecrets generates a random 32-byte hex signing secret for every
// configured webhook URL that lacks one, storing it in the same webhooks map as
// `<name>_secret`. Reports whether any secret was generated (PART 17).
func (c *Config) ensureWebhookSecrets() (bool, error) {
	generated := false
	roles := []*ContactRole{&c.Server.Contact.Admin, &c.Server.Contact.Security, &c.Server.Contact.Abuse, &c.Server.Contact.General}
	for _, r := range roles {
		if r.Webhooks == nil {
			continue
		}
		for name, url := range r.Webhooks {
			if strings.HasSuffix(name, webhookSecretSuffix) {
				continue
			}
			if strings.TrimSpace(url) == "" {
				continue
			}
			key := name + webhookSecretSuffix
			if strings.TrimSpace(r.Webhooks[key]) != "" {
				continue
			}
			var b [32]byte
			if _, err := crand.Read(b[:]); err != nil {
				return generated, err
			}
			r.Webhooks[key] = fmt.Sprintf("%x", b)
			generated = true
		}
	}
	return generated, nil
}

func (c *Config) loadEnv() {
	// MODE is a runtime variable — always overrides config file (AI.md PART 5).
	// Never baked into the persisted config; only applies at runtime. Processed
	// first so devModeForValidation() reflects it for the DOMAIN check below.
	if v := os.Getenv("MODE"); v != "" {
		c.Server.Mode = v
	}
	// PART 8: the canonical env override is {PROJECT_NAME}_PORT (PASTEBIN_PORT);
	// PORT is the generic docker alias. PASTEBIN_PORT wins when both are set.
	if v := os.Getenv("PORT"); v != "" {
		c.Server.Port = v
	}
	if v := os.Getenv("PASTEBIN_PORT"); v != "" {
		c.Server.Port = v
	}
	// LISTEN is the AI.md-canonical name (PART 5); ADDRESS is an accepted alias.
	if v := os.Getenv("ADDRESS"); v != "" {
		c.Server.Address = v
	}
	if v := os.Getenv("LISTEN"); v != "" {
		c.Server.Address = v
	}
	if v := os.Getenv("BASE_URL"); v != "" {
		c.Server.BaseURL = v
	}
	// DOMAIN is the highest-priority FQDN override (AI.md 7548, 7561). It MUST
	// pass host validation for the current mode; an invalid value is skipped
	// silently and detection continues to the next source (PART 12).
	if v := strings.TrimSpace(os.Getenv("DOMAIN")); v != "" {
		if IsValidHost(v, c.devModeForValidation(), projectName) {
			c.Server.FQDN = v
		} else {
			log.Printf("config: DOMAIN=%q failed host validation for mode %q; ignoring", v, c.Server.Mode)
		}
	}
	// DATABASE_DRIVER selects the storage backend (AI.md 7550).
	if v := os.Getenv("DATABASE_DRIVER"); v != "" {
		c.Database.Type = v
	}
	// DATABASE_URL / DB_PATH set the connection string (AI.md 7551); DB_PATH is an alias.
	if v := os.Getenv("DB_PATH"); v != "" {
		c.Database.Path = v
	}
	if v := os.Getenv("DATABASE_URL"); v != "" {
		c.Database.Path = v
	}
	// CACHE_URL is the container-canonical cache override (AI.md 31725), e.g.
	// "valkey://host:6379" or "redis://host:6379". The cache driver dispatches on
	// Type, so derive Type from the URL scheme; a URL alone would otherwise stay on
	// the default in-memory driver and silently ignore the remote endpoint.
	if v := os.Getenv("CACHE_URL"); v != "" {
		c.Server.Cache.URL = v
		switch {
		case strings.HasPrefix(v, "valkey://"), strings.HasPrefix(v, "valkeys://"):
			c.Server.Cache.Type = "valkey"
		case strings.HasPrefix(v, "redis://"), strings.HasPrefix(v, "rediss://"):
			c.Server.Cache.Type = "redis"
		}
	}
	// APPLICATION_NAME is the AI.md-canonical application title (AI.md 7579); SITE_TITLE is an alias.
	if v := os.Getenv("SITE_TITLE"); v != "" {
		c.Web.SiteTitle = v
	}
	if v := os.Getenv("APPLICATION_NAME"); v != "" {
		c.Server.Branding.Title = v
	}
	// APPLICATION_TAGLINE sets the application description/tagline (AI.md 7580).
	if v := os.Getenv("APPLICATION_TAGLINE"); v != "" {
		c.Server.Branding.Tagline = v
	}
	if v := os.Getenv("THEME"); v != "" {
		c.Web.Theme = v
	}
	// MAX_SIZE accepts a human-readable size ("100kb", "20mb", "1gb", "1tb", or
	// a bare number defaulting to megabytes); zero or negative means
	// unlimited. MAX_SIZE_BYTES is kept as a deprecated alias for raw-byte
	// overrides.
	if v := os.Getenv("MAX_SIZE"); v != "" {
		if n, err := ParseSize(v, c.Paste.MaxSizeBytes); err == nil {
			c.Paste.MaxSize = v
			c.Paste.MaxSizeBytes = n
		} else {
			log.Printf("[config] WARNING: invalid MAX_SIZE %q, keeping %s", v, c.Paste.MaxSize)
		}
	} else if v := os.Getenv("MAX_SIZE_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			if n < 0 {
				n = 0
			}
			c.Paste.MaxSize = fmt.Sprintf("%d", n)
			c.Paste.MaxSizeBytes = n
		}
	}
	if v := os.Getenv("SMTP_HOST"); v != "" {
		c.Server.Notifications.Email.SMTP.Host = v
	}
	if v := os.Getenv("SMTP_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Server.Notifications.Email.SMTP.Port = n
		}
	}
	if v := os.Getenv("SMTP_USERNAME"); v != "" {
		c.Server.Notifications.Email.SMTP.Username = v
	}
	if v := os.Getenv("SMTP_PASSWORD"); v != "" {
		c.Server.Notifications.Email.SMTP.Password = v
	}
	if v := os.Getenv("SMTP_TLS"); v != "" {
		c.Server.Notifications.Email.SMTP.TLS = v
	}
	if v := os.Getenv("SMTP_FROM_NAME"); v != "" {
		c.Server.Notifications.Email.From.Name = v
	}
	if v := os.Getenv("SMTP_FROM_EMAIL"); v != "" {
		c.Server.Notifications.Email.From.Email = v
	}
	if v := os.Getenv("TERMBIN_ENABLED"); v != "" {
		if b, err := ParseBool(v, c.Server.Termbin.Enabled); err == nil {
			c.Server.Termbin.Enabled = b
		}
	}
	if v := os.Getenv("I2P_ENABLED"); v != "" {
		if b, err := ParseBool(v, c.Server.I2P.Enabled); err == nil {
			c.Server.I2P.Enabled = b
		}
	}
	if v := os.Getenv("TERMBIN_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Server.Termbin.Port = n
		}
	}
	if v := os.Getenv("TERMBIN_MAX_SIZE"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			c.Server.Termbin.MaxSize = n
		}
	}
	if v := os.Getenv("TERMBIN_TIMEOUT"); v != "" {
		c.Server.Termbin.Timeout = v
	}
}

// validateLogFile normalizes one server.logs.* block in place: blank fields
// take the default, unknown formats and unparseable rotate/keep policies warn
// and default. formats lists the values valid for this file type.
func validateLogFile(name string, c *LogFileConfig, def LogFileConfig, formats ...string) {
	if strings.TrimSpace(c.Filename) == "" {
		c.Filename = def.Filename
	}
	c.Format = strings.ToLower(strings.TrimSpace(c.Format))
	if c.Format == "" {
		c.Format = def.Format
	}
	valid := false
	for _, f := range formats {
		if c.Format == f {
			valid = true
			break
		}
	}
	if !valid {
		log.Printf("[config] WARNING: invalid %s.format %q, using default %q",
			name, c.Format, def.Format)
		c.Format = def.Format
	}
	if strings.TrimSpace(c.Rotate) == "" {
		c.Rotate = def.Rotate
	}
	if _, err := logging.ParseRotate(c.Rotate); err != nil {
		log.Printf("[config] WARNING: invalid %s.rotate %q, using default %q",
			name, c.Rotate, def.Rotate)
		c.Rotate = def.Rotate
	}
	if strings.TrimSpace(c.Keep) == "" {
		c.Keep = def.Keep
	}
	if _, err := logging.ParseKeep(c.Keep); err != nil {
		log.Printf("[config] WARNING: invalid %s.keep %q, using default %q",
			name, c.Keep, def.Keep)
		c.Keep = def.Keep
	}
}

// Validate checks all config values, replaces invalid ones with defaults, and
// logs a warning for each replacement. It never returns an error; the server
// must always start with sane defaults even if the config file is malformed (PART 12).
func Validate(cfg *Config) {
	d := DefaultConfig()
	if cfg.Server.Tokens.MaxLinked <= 0 {
		log.Printf("[config] WARNING: invalid server.tokens.max_linked %d, using default %d", cfg.Server.Tokens.MaxLinked, d.Server.Tokens.MaxLinked)
		cfg.Server.Tokens.MaxLinked = d.Server.Tokens.MaxLinked
	}
	if cfg.Server.Tokens.IdlePurgeDays < 0 {
		log.Printf("[config] WARNING: invalid server.tokens.idle_purge_days %d, using default 0", cfg.Server.Tokens.IdlePurgeDays)
		cfg.Server.Tokens.IdlePurgeDays = 0
	}
	if age, ok := ownerCookieMaxAge(cfg.Server.Tokens.CookieMaxAge); !ok || age <= 0 {
		log.Printf("[config] WARNING: invalid server.tokens.cookie_max_age %q, using default %s", cfg.Server.Tokens.CookieMaxAge, d.Server.Tokens.CookieMaxAge)
		cfg.Server.Tokens.CookieMaxAge = d.Server.Tokens.CookieMaxAge
	}

	validateRestoreLimits(&cfg.Server.Restore)
	validateDebugConfig(&cfg.Server.Debug)
	validateUpdate(&cfg.Server.Update)
	validateI18n(&cfg.Server.I18n)
	validateBackupRetention(&cfg.Server.Backup.Retention)
	if cfg.Server.Compression.Level < 1 || cfg.Server.Compression.Level > 9 {
		log.Printf("[config] WARNING: invalid server.compression.level %d, using default 5", cfg.Server.Compression.Level)
		cfg.Server.Compression.Level = 5
	}
	if len(cfg.Server.Compression.Types) == 0 {
		cfg.Server.Compression.Types = d.Server.Compression.Types
	}

	ad := &cfg.Server.Security.AbuseDetection.RequestFlood
	if ad.Multiplier < 1 {
		log.Printf("[config] WARNING: invalid abuse_detection.request_flood.multiplier %d, using default 10", ad.Multiplier)
		ad.Multiplier = 10
	}
	if dur, err := time.ParseDuration(ad.BlockDuration); err != nil || dur <= 0 {
		log.Printf("[config] WARNING: invalid abuse_detection.request_flood.block_duration %q, using default 1h", ad.BlockDuration)
		ad.BlockDuration = "1h"
	}

	// Database driver aliases normalize to their canonical form (AI.md 7722):
	// sqlite/sqlite2/sqlite3 -> sqlite; libsql/turso -> libsql.
	switch strings.ToLower(strings.TrimSpace(cfg.Database.Type)) {
	case "", "sqlite", "sqlite2", "sqlite3":
		cfg.Database.Type = "sqlite"
	case "libsql", "turso":
		cfg.Database.Type = "libsql"
	default:
		log.Printf("[config] WARNING: invalid database.driver %q, using default %q",
			cfg.Database.Type, d.Database.Type)
		cfg.Database.Type = d.Database.Type
	}

	// Database pool: a non-positive value is invalid, so warn and substitute the
	// default rather than handing sql.DB an unbounded pool (AI.md PART 10).
	if cfg.Database.Pool.MaxOpen <= 0 {
		if cfg.Database.Pool.MaxOpen != 0 {
			log.Printf("[config] WARNING: invalid database.pool.max_open %d, using default %d",
				cfg.Database.Pool.MaxOpen, d.Database.Pool.MaxOpen)
		}
		cfg.Database.Pool.MaxOpen = d.Database.Pool.MaxOpen
	}
	if cfg.Database.Pool.MaxIdle <= 0 {
		if cfg.Database.Pool.MaxIdle != 0 {
			log.Printf("[config] WARNING: invalid database.pool.max_idle %d, using default %d",
				cfg.Database.Pool.MaxIdle, d.Database.Pool.MaxIdle)
		}
		cfg.Database.Pool.MaxIdle = d.Database.Pool.MaxIdle
	}
	if cfg.Database.Pool.MaxIdle > cfg.Database.Pool.MaxOpen {
		cfg.Database.Pool.MaxIdle = cfg.Database.Pool.MaxOpen
	}
	if cfg.Database.Pool.MaxLifetime <= 0 {
		if cfg.Database.Pool.MaxLifetime != 0 {
			log.Printf("[config] WARNING: invalid database.pool.max_lifetime %s, using default %s",
				cfg.Database.Pool.MaxLifetime, d.Database.Pool.MaxLifetime)
		}
		cfg.Database.Pool.MaxLifetime = d.Database.Pool.MaxLifetime
	}
	if cfg.Database.Pool.MaxIdleTime <= 0 {
		if cfg.Database.Pool.MaxIdleTime != 0 {
			log.Printf("[config] WARNING: invalid database.pool.max_idle_time %s, using default %s",
				cfg.Database.Pool.MaxIdleTime, d.Database.Pool.MaxIdleTime)
		}
		cfg.Database.Pool.MaxIdleTime = d.Database.Pool.MaxIdleTime
	}

	// Limits: timeouts must be parseable and positive.
	for _, pair := range []struct {
		name *string
		def  string
	}{
		{&cfg.Server.Limits.ReadTimeout, d.Server.Limits.ReadTimeout},
		{&cfg.Server.Limits.WriteTimeout, d.Server.Limits.WriteTimeout},
		{&cfg.Server.Limits.IdleTimeout, d.Server.Limits.IdleTimeout},
	} {
		if dur, err := time.ParseDuration(*pair.name); err != nil || dur <= 0 {
			log.Printf("[config] WARNING: invalid timeout %q, using default %s", *pair.name, pair.def)
			*pair.name = pair.def
		}
	}

	// Limits: max body size must be positive.
	if cfg.Server.Limits.MaxBodySize <= 0 {
		log.Printf("[config] WARNING: invalid max_body_size %d, using default %d",
			cfg.Server.Limits.MaxBodySize, d.Server.Limits.MaxBodySize)
		cfg.Server.Limits.MaxBodySize = d.Server.Limits.MaxBodySize
	}

	// Termbin: when enabled, port/size/timeout must be sane.
	if cfg.Server.Termbin.Enabled {
		if cfg.Server.Termbin.Port <= 0 || cfg.Server.Termbin.Port > 65535 {
			log.Printf("[config] WARNING: invalid termbin.port %d, using default %d",
				cfg.Server.Termbin.Port, d.Server.Termbin.Port)
			cfg.Server.Termbin.Port = d.Server.Termbin.Port
		}
		if cfg.Server.Termbin.MaxSize <= 0 {
			log.Printf("[config] WARNING: invalid termbin.max_size %d, using default %d",
				cfg.Server.Termbin.MaxSize, d.Server.Termbin.MaxSize)
			cfg.Server.Termbin.MaxSize = d.Server.Termbin.MaxSize
		}
		if dur, err := time.ParseDuration(cfg.Server.Termbin.Timeout); err != nil || dur <= 0 {
			log.Printf("[config] WARNING: invalid termbin.timeout %q, using default %s",
				cfg.Server.Termbin.Timeout, d.Server.Termbin.Timeout)
			cfg.Server.Termbin.Timeout = d.Server.Termbin.Timeout
		}
	}

	// Contact page captcha must be one of the supported challenge types.
	switch cfg.Server.Pages.Contact.Captcha {
	case "simple", "recaptcha", "hcaptcha":
	default:
		log.Printf("[config] WARNING: invalid pages.contact.captcha %q, using default %q",
			cfg.Server.Pages.Contact.Captcha, d.Server.Pages.Contact.Captcha)
		cfg.Server.Pages.Contact.Captcha = d.Server.Pages.Contact.Captcha
	}
	if cfg.Server.Pages.Contact.SuccessMessage == "" {
		cfg.Server.Pages.Contact.SuccessMessage = d.Server.Pages.Contact.SuccessMessage
	}

	// Web theme must be one of the valid values.
	switch cfg.Web.Theme {
	case "dark", "light", "auto":
	default:
		log.Printf("[config] WARNING: invalid web.theme %q, using default \"dark\"", cfg.Web.Theme)
		cfg.Web.Theme = "dark"
	}

	// Announcement type must be one of the valid values (AI.md 22527); an
	// entry with a blank/missing ID cannot be dismissed or matched against the
	// dismissed_announcements cookie, so it is dropped with a warning rather
	// than rendered forever.
	validAnnouncements := cfg.Web.Announcements.Messages[:0]
	for _, m := range cfg.Web.Announcements.Messages {
		if strings.TrimSpace(m.ID) == "" {
			log.Printf("[config] WARNING: web.announcements.messages entry with empty id skipped")
			continue
		}
		switch m.Type {
		case "info", "warning", "error", "success":
		default:
			log.Printf("[config] WARNING: invalid web.announcements.messages[%q].type %q, using default \"info\"", m.ID, m.Type)
			m.Type = "info"
		}
		validAnnouncements = append(validAnnouncements, m)
	}
	cfg.Web.Announcements.Messages = validAnnouncements

	// Rate limit counts must be non-negative (zero means "disabled for this class").
	if cfg.Server.RateLimit.Read.Requests < 0 {
		log.Printf("[config] WARNING: rate_limit.read.requests < 0, using default %d",
			d.Server.RateLimit.Read.Requests)
		cfg.Server.RateLimit.Read.Requests = d.Server.RateLimit.Read.Requests
	}
	if cfg.Server.RateLimit.Write.Requests < 0 {
		log.Printf("[config] WARNING: rate_limit.write.requests < 0, using default %d",
			d.Server.RateLimit.Write.Requests)
		cfg.Server.RateLimit.Write.Requests = d.Server.RateLimit.Write.Requests
	}
	if cfg.Server.RateLimit.Health.Requests < 0 {
		log.Printf("[config] WARNING: rate_limit.health.requests < 0, using default %d",
			d.Server.RateLimit.Health.Requests)
		cfg.Server.RateLimit.Health.Requests = d.Server.RateLimit.Health.Requests
	}
	if cfg.Server.RateLimit.GlobalBurst < 0 {
		log.Printf("[config] WARNING: rate_limit.global_burst < 0, using default %d",
			d.Server.RateLimit.GlobalBurst)
		cfg.Server.RateLimit.GlobalBurst = d.Server.RateLimit.GlobalBurst
	}

	// Paste size must be a valid human-readable size ("10mb", "1gb", ...); zero
	// or negative is allowed and means unlimited.
	if n, err := ParseSize(cfg.Paste.MaxSize, d.Paste.MaxSizeBytes); err != nil {
		log.Printf("[config] WARNING: invalid paste.max_size %q, using default %s",
			cfg.Paste.MaxSize, d.Paste.MaxSize)
		cfg.Paste.MaxSize = d.Paste.MaxSize
		cfg.Paste.MaxSizeBytes = d.Paste.MaxSizeBytes
	} else {
		cfg.Paste.MaxSizeBytes = n
	}

	// Cache TTL and timeout must be parseable.
	for _, pair := range []struct {
		name *string
		def  string
	}{
		{&cfg.Server.Cache.Timeout, d.Server.Cache.Timeout},
		{&cfg.Server.Cache.TTL, d.Server.Cache.TTL},
	} {
		if dur, err := time.ParseDuration(*pair.name); err != nil || dur <= 0 {
			log.Printf("[config] WARNING: invalid cache duration %q, using default %s",
				*pair.name, pair.def)
			*pair.name = pair.def
		}
	}

	// Logging level must be a known value.
	switch cfg.Server.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		log.Printf("[config] WARNING: invalid logging.level %q, using default \"info\"",
			cfg.Server.Logging.Level)
		cfg.Server.Logging.Level = "info"
	}

	// Per-file log settings: blank filename/format/rotate/keep fall back to
	// the defaults; unknown formats warn and default (never fail startup).
	accessAsFile := LogFileConfig{
		Filename: cfg.Server.Logging.Access.Filename,
		Format:   cfg.Server.Logging.Access.Format,
		Custom:   cfg.Server.Logging.Access.Custom,
		Rotate:   cfg.Server.Logging.Access.Rotate,
		Keep:     cfg.Server.Logging.Access.Keep,
	}
	validateLogFile("logs.access", &accessAsFile, LogFileConfig{
		Filename: d.Server.Logging.Access.Filename,
		Format:   d.Server.Logging.Access.Format,
		Custom:   d.Server.Logging.Access.Custom,
		Rotate:   d.Server.Logging.Access.Rotate,
		Keep:     d.Server.Logging.Access.Keep,
	}, "apache", "nginx", "json", "custom")
	cfg.Server.Logging.Access.Filename = accessAsFile.Filename
	cfg.Server.Logging.Access.Format = accessAsFile.Format
	cfg.Server.Logging.Access.Custom = accessAsFile.Custom
	cfg.Server.Logging.Access.Rotate = accessAsFile.Rotate
	cfg.Server.Logging.Access.Keep = accessAsFile.Keep
	validateLogFile("logs.server", &cfg.Server.Logging.Server, d.Server.Logging.Server,
		"text", "json")
	validateLogFile("logs.error", &cfg.Server.Logging.Error, d.Server.Logging.Error,
		"text", "json")
	validateLogFile("logs.app", &cfg.Server.Logging.App, d.Server.Logging.App,
		"logfmt", "json")
	validateLogFile("logs.auth", &cfg.Server.Logging.Auth, d.Server.Logging.Auth,
		"syslog", "json")
	validateLogFile("logs.security", &cfg.Server.Logging.Security, d.Server.Logging.Security,
		"fail2ban", "syslog", "cef", "json", "text")
	debugAsFile := LogFileConfig{
		Filename: cfg.Server.Logging.Debug.Filename,
		Format:   cfg.Server.Logging.Debug.Format,
		Custom:   cfg.Server.Logging.Debug.Custom,
		Rotate:   cfg.Server.Logging.Debug.Rotate,
		Keep:     cfg.Server.Logging.Debug.Keep,
	}
	validateLogFile("logs.debug", &debugAsFile, LogFileConfig{
		Filename: d.Server.Logging.Debug.Filename,
		Format:   d.Server.Logging.Debug.Format,
		Rotate:   d.Server.Logging.Debug.Rotate,
		Keep:     d.Server.Logging.Debug.Keep,
	}, "text", "json")
	cfg.Server.Logging.Debug.Filename = debugAsFile.Filename
	cfg.Server.Logging.Debug.Format = debugAsFile.Format
	cfg.Server.Logging.Debug.Rotate = debugAsFile.Rotate
	cfg.Server.Logging.Debug.Keep = debugAsFile.Keep

	// Consent banner position must be top or bottom.
	switch cfg.Server.Privacy.Consent.Position {
	case "top", "bottom":
	default:
		log.Printf("[config] WARNING: invalid privacy.consent.position %q, using default %q",
			cfg.Server.Privacy.Consent.Position, d.Server.Privacy.Consent.Position)
		cfg.Server.Privacy.Consent.Position = d.Server.Privacy.Consent.Position
	}
	// Essential cookies are strictly necessary and cannot be disabled.
	cfg.Server.Privacy.Cookies.Essential.Enabled = true
	// Fall back to default text for any blank consent-banner field.
	for _, pair := range []struct {
		val *string
		def string
	}{
		{&cfg.Server.Privacy.Consent.Message, d.Server.Privacy.Consent.Message},
		{&cfg.Server.Privacy.Consent.MessageIfSold, d.Server.Privacy.Consent.MessageIfSold},
		{&cfg.Server.Privacy.Consent.Policy.Text, d.Server.Privacy.Consent.Policy.Text},
		{&cfg.Server.Privacy.Consent.Policy.URL, d.Server.Privacy.Consent.Policy.URL},
		{&cfg.Server.Privacy.Consent.Buttons.Decline, d.Server.Privacy.Consent.Buttons.Decline},
		{&cfg.Server.Privacy.Consent.Buttons.Accept, d.Server.Privacy.Consent.Buttons.Accept},
		{&cfg.Server.Privacy.Consent.PreferencesText, d.Server.Privacy.Consent.PreferencesText},
	} {
		if strings.TrimSpace(*pair.val) == "" {
			*pair.val = pair.def
		}
	}

	// Analytics configuration must be valid; on error warn and disable tracking
	// (warn-and-default: never fail startup, PART 5).
	if err := ValidateTracking(&cfg.Server.Tracking); err != nil {
		log.Printf("[config] WARNING: invalid server.tracking (%v), disabling analytics", err)
		cfg.Server.Tracking = TrackingConfig{}
	}

	// Footer custom HTML must survive sanitization; content that sanitizes
	// down to nothing is a configuration error, so warn and fall back to the
	// default (empty = built-in branding) rather than silently rendering
	// nothing (PART 16 Custom HTML Validation, warn-and-default per PART 5).
	if _, err := ValidateFooterHTML(cfg.Web.Footer.CustomHTML); err != nil {
		log.Printf("[config] WARNING: invalid web.footer.custom_html (%v), using default branding", err)
		cfg.Web.Footer.CustomHTML = d.Web.Footer.CustomHTML
	}
}

func ownerCookieMaxAge(value string) (time.Duration, bool) {
	if strings.HasSuffix(value, "y") || strings.HasSuffix(value, "d") {
		unit := value[len(value)-1]
		n, err := strconv.Atoi(value[:len(value)-1])
		if err != nil || n <= 0 || n > 36500 {
			return 0, false
		}
		days := int64(n)
		if unit == 'y' {
			days *= 365
		}
		return time.Duration(days) * 24 * time.Hour, true
	}
	d, err := time.ParseDuration(value)
	return d, err == nil
}

// ValidateTracking validates the analytics tracking configuration (PART 31).
// An empty or "none" type is valid (disabled). Returns a descriptive error for
// any malformed ID/URL so the caller can warn-and-disable.
func ValidateTracking(cfg *TrackingConfig) error {
	if cfg.Type == "" || cfg.Type == "none" {
		return nil
	}
	switch cfg.Type {
	case "google":
		if !regexp.MustCompile(`^(UA-\d+-\d+|G-[A-Z0-9]+)$`).MatchString(cfg.ID) {
			return errors.New("invalid Google Analytics ID format")
		}
	case "matomo", "piwik":
		if _, err := strconv.Atoi(cfg.ID); err != nil {
			return errors.New("matomo/piwik ID must be an integer")
		}
		if cfg.URL == "" {
			return errors.New("matomo/piwik requires URL")
		}
	case "owa":
		if cfg.ID == "" {
			return errors.New("OWA requires site ID")
		}
		if cfg.URL == "" {
			return errors.New("OWA requires URL")
		}
	case "fathom":
		if cfg.ID == "" {
			return errors.New("fathom requires site ID")
		}
	case "plausible":
		if cfg.ID == "" {
			return errors.New("plausible requires domain")
		}
	case "umami":
		if _, err := uuid.Parse(cfg.ID); err != nil {
			return errors.New("umami ID must be a valid UUID")
		}
		if cfg.URL == "" {
			return errors.New("umami requires URL")
		}
	case "simple":
	case "cloudflare":
		if cfg.ID == "" {
			return errors.New("cloudflare requires beacon token")
		}
	default:
		return fmt.Errorf("unknown tracking type: %s", cfg.Type)
	}
	// Any provided self-hosted URL must be a parseable absolute URL.
	if cfg.URL != "" {
		u, err := url.Parse(cfg.URL)
		if err != nil {
			return fmt.Errorf("invalid tracking URL: %w", err)
		}
		if u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("invalid tracking URL: %q is not absolute", cfg.URL)
		}
	}
	return nil
}

// Save writes config to path atomically. The config file holds the AES-256-GCM
// encryption key and operator token, so a crash mid-write must never leave a
// truncated or corrupt file: the data is written to a temp file in the same
// directory and renamed into place (rename is atomic on the same filesystem).
func Save(path string, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".server.yml.*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp config: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o640); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("syncing temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("renaming temp config: %w", err)
	}
	return nil
}

// SetUpdateBranch loads the config at path, sets server.update.branch to the
// given value, and saves the file back. Creates the file and parent dirs if absent.
func SetUpdateBranch(path, branch string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	cfg, err := Load(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("loading config: %w", err)
	}
	if cfg == nil {
		cfg = DefaultConfig()
	}
	cfg.Server.Update.Branch = branch
	return Save(path, cfg)
}

// SetOnionAddress loads the config at path, sets server.tor.onion_address to
// the given value, and saves the file back (PART 12: tor.onion_address is
// "Set automatically on first successful Tor bootstrap"). Creates the file
// and parent dirs if absent.
func SetOnionAddress(path, addr string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	cfg, err := Load(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("loading config: %w", err)
	}
	if cfg == nil {
		cfg = DefaultConfig()
	}
	cfg.Server.Tor.OnionAddress = addr
	return Save(path, cfg)
}

// SetB32Address loads the config at path, sets server.i2p.b32_address to
// the given value, and saves the file back (PART 31.2, mirroring
// SetOnionAddress's PART 12 behavior). Creates the file and parent dirs if
// absent.
func SetB32Address(path, addr string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	cfg, err := Load(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("loading config: %w", err)
	}
	if cfg == nil {
		cfg = DefaultConfig()
	}
	cfg.Server.I2P.B32Address = addr
	return Save(path, cfg)
}

// ResolvePort finalises cfg.Server.Port according to the PART 5 / PART 8 rules.
// Precedence (highest first):
//
//   - Explicit --port flag / $PASTEBIN_PORT / $PORT env / config file value → use as-is
//   - No port configured (empty), container environment → "80" (docker default)
//   - No port configured (empty), non-container → pick a random unused port in
//     64000-64999, persist it to cfgPath so subsequent restarts use the same port
//
// The caller must apply CLI flag overrides to cfg BEFORE calling this function
// so that an explicit --port value takes precedence over the container default
// and the persisted value. cfgPath is the path used to save the selected random
// port. In a container an explicit port always wins; 80 is only the fallback
// default when nothing else set it.
func ResolvePort(cfgPath string, cfg *Config, inContainer bool) error {
	// An explicit value (flag, env, or config file) always wins, in or out of
	// a container.
	if cfg.Server.Port != "" {
		return nil
	}
	if inContainer {
		// Container default when nothing else configured the port.
		cfg.Server.Port = "80"
		return nil
	}

	// First run: no port configured — pick a random unused port in 64000-64999.
	port, err := randomUnusedPort(64000, 64999)
	if err != nil {
		return fmt.Errorf("port allocator: %w", err)
	}
	cfg.Server.Port = strconv.Itoa(port)

	// Persist only the port. Read the canonical config file, update Server.Port,
	// and save it back. Never save cfg directly: cfg carries runtime env-var
	// overrides (MODE, etc.) that must not be baked into the persisted config
	// (AI.md PART 5 — runtime variables are never persisted).
	if saveErr := persistPortOnly(cfgPath, cfg.Server.Port); saveErr != nil {
		// Non-fatal: log at the call site; the server can still start.
		return fmt.Errorf("port allocator: could not persist port to %s: %w", cfgPath, saveErr)
	}
	return nil
}

// persistPortOnly reads the config file at path, sets Server.Port to port, and
// saves the result. Using a clean file-round-trip ensures runtime env-var
// overrides are never written to disk (AI.md PART 5).
func persistPortOnly(path string, port string) error {
	base := DefaultConfig()
	if data, err := os.ReadFile(path); err == nil {
		// Abort rather than clobber: if the existing file is malformed, saving
		// defaults over it would silently destroy the operator's config.
		if err := decodeConfig(data, base); err != nil {
			return fmt.Errorf("parse existing config %s: %w", path, err)
		}
	}
	base.Server.Port = port
	return Save(path, base)
}

func decodeConfig(data []byte, cfg *Config) error {
	data = normalizeBoolDocument(data, cfg)
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	return decoder.Decode(cfg)
}

// SplitPorts parses a port specification into an optional plain-HTTP port and an
// optional HTTPS port following the PART 15 dual-port rule ("First = HTTP,
// Second = HTTPS"). A single value (e.g. "8080") returns (value, "") and the
// caller applies the existing single-port TLS logic. A comma-separated pair
// (e.g. "80,443") returns the first field as the plain-HTTP port and the second
// as the HTTPS port. Surrounding whitespace on each field is trimmed.
func SplitPorts(spec string) (httpPort, httpsPort string) {
	parts := strings.SplitN(strings.TrimSpace(spec), ",", 2)
	first := strings.TrimSpace(parts[0])
	if len(parts) == 2 {
		return first, strings.TrimSpace(parts[1])
	}
	return first, ""
}

// randomUnusedPort picks a random port in [lo, hi] that can be bound.
func randomUnusedPort(lo, hi int) (int, error) {
	ports := rand.Perm(hi - lo + 1)
	for _, offset := range ports {
		port := lo + offset
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			continue
		}
		ln.Close()
		return port, nil
	}
	return 0, fmt.Errorf("no unused port found in %d-%d", lo, hi)
}

// ConfigManager watches a config file and hot-reloads eligible settings.
// Restart-required settings are logged as warnings but not applied.
type ConfigManager struct {
	configPath  string
	current     *Config
	lastModTime time.Time
	mu          sync.RWMutex
}

// NewConfigManager constructs a ConfigManager for configPath with the already-loaded cfg as its initial state.
func NewConfigManager(configPath string, cfg *Config) *ConfigManager {
	var modTime time.Time
	if info, err := os.Stat(configPath); err == nil {
		modTime = info.ModTime()
	}
	return &ConfigManager{
		configPath:  configPath,
		current:     cfg,
		lastModTime: modTime,
	}
}

// Get returns the current active config. Safe for concurrent use.
func (m *ConfigManager) Get() *Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current
}

// Start launches the background polling goroutine; it runs until stop is closed.
// Optional onChange callbacks are called after each successful hot-reload with the new config.
func (m *ConfigManager) Start(stop <-chan struct{}, onChange ...func(*Config)) {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if next := m.checkFileChanges(); next != nil {
					for _, fn := range onChange {
						fn(next)
					}
				}
			case <-stop:
				return
			}
		}
	}()
}

// checkFileChanges stat-checks the config file and reloads if the modtime changed.
// Returns the new config on a successful reload, nil otherwise.
func (m *ConfigManager) checkFileChanges() *Config {
	info, err := os.Stat(m.configPath)
	if err != nil {
		return nil
	}
	if !info.ModTime().After(m.lastModTime) {
		return nil
	}

	next, err := Load(m.configPath)
	if err != nil {
		log.Printf("[config] reload error: %v", err)
		return nil
	}

	m.mu.Lock()
	prev := m.current
	m.applyHotSettings(prev, next)
	m.current = next
	m.lastModTime = info.ModTime()
	m.mu.Unlock()

	log.Printf("[config] reloaded from %s", m.configPath)
	return next
}

// applyHotSettings logs warnings for restart-required changes and applies
// hot-reloadable settings from next into next (they are always applied on swap).
// The separate prev reference allows detecting which restart-required keys changed.
func (m *ConfigManager) applyHotSettings(prev, next *Config) {
	if prev.Server.Port != next.Server.Port {
		log.Printf("[config] WARNING: server.port changed from %s to %s — restart required",
			prev.Server.Port, next.Server.Port)
	}
	if prev.Server.Address != next.Server.Address {
		log.Printf("[config] WARNING: server.address changed (%s → %s) — restart required",
			prev.Server.Address, next.Server.Address)
	}
	if prev.Database.Type != next.Database.Type || prev.Database.Path != next.Database.Path {
		log.Printf("[config] WARNING: database settings changed — restart required")
	}
	if prev.Server.Tor.Binary != next.Server.Tor.Binary ||
		prev.Server.Tor.VirtualPort != next.Server.Tor.VirtualPort {
		log.Printf("[config] WARNING: tor settings changed — restart required")
	}

	// Hot-reloadable settings are applied by replacing the entire current pointer
	// on the outer swap. Log what changed for operator visibility.
	if prev.Server.Logging.Level != next.Server.Logging.Level {
		log.Printf("[config] hot-reload: logging.level %s → %s",
			prev.Server.Logging.Level, next.Server.Logging.Level)
	}
	if prev.Server.RateLimit.Enabled != next.Server.RateLimit.Enabled ||
		prev.Server.RateLimit.Read != next.Server.RateLimit.Read ||
		prev.Server.RateLimit.Write != next.Server.RateLimit.Write ||
		prev.Server.RateLimit.Health != next.Server.RateLimit.Health ||
		prev.Server.RateLimit.GlobalBurst != next.Server.RateLimit.GlobalBurst {
		log.Printf("[config] hot-reload: rate_limit settings updated")
	}
	if !slices.Equal(prev.Server.Cors.AllowedOrigins, next.Server.Cors.AllowedOrigins) ||
		prev.Server.Cors.AllowCredentials != next.Server.Cors.AllowCredentials ||
		prev.Server.Cors.MaxAge != next.Server.Cors.MaxAge {
		log.Printf("[config] hot-reload: cors policy updated")
	}
	if prev.Web.SiteTitle != next.Web.SiteTitle || prev.Web.Theme != next.Web.Theme {
		log.Printf("[config] hot-reload: branding settings updated")
	}
}

// StartupScheme returns "https" when the server is configured to serve TLS and
// "http" otherwise. Without a live request the protocol comes from config:
// server.tls.enabled, Let's Encrypt, or port 443 all imply HTTPS (PART 12/15).
func (c *Config) StartupScheme() string {
	if c.Server.TLS.Enabled || c.Server.TLS.LetsEncrypt.Enabled {
		return "https"
	}
	for _, p := range strings.Split(c.Server.Port, ",") {
		if strings.TrimSpace(p) == "443" {
			return "https"
		}
	}
	return "http"
}

// StartupBaseURL builds the canonical base URL when no HTTP request is
// available (startup banner, background-task email links, the raw-TCP termbin
// reply). It mirrors the per-request baseURL(r) logic for the static config
// path: server.base_url wins, otherwise {proto}://{fqdn}[:{port}] with the
// standard :80/:443 stripped (PART 12).
func (c *Config) StartupBaseURL() string {
	if u := strings.TrimRight(c.Server.BaseURL, "/"); u != "" {
		return u
	}
	proto := c.StartupScheme()
	fqdn := c.ResolveFQDN()
	if fqdn == "" {
		fqdn = "localhost"
	}
	port := c.Server.Port
	if strings.Contains(port, ",") {
		port = ""
	}
	if (proto == "http" && port == "80") || (proto == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		return proto + "://" + fqdn + ":" + port
	}
	return proto + "://" + fqdn
}

// legacyKeyMoves lists top-level config keys that older releases wrote outside
// `server:`; each is read from server.<key> now (AI.md location).
var legacyKeyMoves = []string{"rate_limit"}

// migrateLegacyKeys rewrites an older server.yml so keys that moved are read
// from their new location, never overwriting a value already set there. A
// document that does not parse as a YAML mapping is returned unchanged so the
// normal decode reports the syntax error.
func migrateLegacyKeys(data []byte) []byte {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil || doc == nil {
		return data
	}
	changed := migrateWebSecurityKeys(doc)
	for _, key := range legacyKeyMoves {
		val, ok := doc[key]
		if !ok {
			continue
		}
		server, _ := doc["server"].(map[string]any)
		if server == nil {
			server = map[string]any{}
		}
		if _, exists := server[key]; !exists {
			server[key] = val
		}
		doc["server"] = server
		delete(doc, key)
		changed = true
	}
	if !changed {
		return data
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return data
	}
	return out
}

// legacyWebSecurityMoves lists the web.security keys that AI.md PART 11 places
// under server.security.
var legacyWebSecurityMoves = []string{
	"encryption_key", "allowlist", "blocked_ips", "abuse_detection", "blocklists", "cve", "trivy",
}

// migrateWebSecurityKeys moves legacy web.security.<key> values to
// server.security.<key> in doc without overwriting a value already present at
// the new location. It reports whether doc changed.
func migrateWebSecurityKeys(doc map[string]any) bool {
	web, _ := doc["web"].(map[string]any)
	old, _ := web["security"].(map[string]any)
	if old == nil {
		return false
	}
	changed := false
	for _, key := range legacyWebSecurityMoves {
		val, ok := old[key]
		if !ok {
			continue
		}
		server, _ := doc["server"].(map[string]any)
		if server == nil {
			server = map[string]any{}
		}
		sec, _ := server["security"].(map[string]any)
		if sec == nil {
			sec = map[string]any{}
		}
		if _, exists := sec[key]; !exists {
			sec[key] = val
		}
		server["security"] = sec
		doc["server"] = server
		delete(old, key)
		changed = true
	}
	return changed
}

// validateDebugConfig warns and substitutes the PART 6 default for an invalid
// server.debug value; startup never fails on a bad debug setting.
func validateDebugConfig(d *DebugConfig) {
	if d.BlockProfileRate < 0 {
		log.Printf("[config] WARNING: invalid server.debug.block_profile_rate %d, using default 1", d.BlockProfileRate)
		d.BlockProfileRate = 1
	}
	if d.MutexProfileFraction < 0 {
		log.Printf("[config] WARNING: invalid server.debug.mutex_profile_fraction %d, using default 1", d.MutexProfileFraction)
		d.MutexProfileFraction = 1
	}
	if n, err := ParseSize(d.MaxBodyLogSize, -1); err != nil || n <= 0 {
		if d.MaxBodyLogSize != "" {
			log.Printf("[config] WARNING: invalid server.debug.max_body_log_size %q, using default 10KB", d.MaxBodyLogSize)
		}
		d.MaxBodyLogSize = "10KB"
	}
}

// validateRestoreLimits warns and substitutes the PART 21 default for any
// server.restore value that is unparsable or below its minimum; startup never
// fails on a bad limit.
func validateRestoreLimits(r *RestoreConfig) {
	def := RestoreConfig{MaxArchiveSize: "1G", MaxExpandedSize: "4G", MaxFiles: 10000, MaxFileSize: "512M", MaxRatio: 100}
	fix := func(name string, v *string, d string) {
		if n, err := ParseSize(*v, -1); err != nil || n <= 0 {
			log.Printf("[config] WARNING: invalid server.restore.%s %q, using default %s", name, *v, d)
			*v = d
		}
	}
	fix("max_archive_size", &r.MaxArchiveSize, def.MaxArchiveSize)
	fix("max_expanded_size", &r.MaxExpandedSize, def.MaxExpandedSize)
	fix("max_file_size", &r.MaxFileSize, def.MaxFileSize)
	if r.MaxFiles < 1 {
		log.Printf("[config] WARNING: invalid server.restore.max_files %d, using default %d", r.MaxFiles, def.MaxFiles)
		r.MaxFiles = def.MaxFiles
	}
	if r.MaxRatio < 1 {
		log.Printf("[config] WARNING: invalid server.restore.max_ratio %d, using default %d", r.MaxRatio, def.MaxRatio)
		r.MaxRatio = def.MaxRatio
	}
}

// validateBackupRetention applies the PART 21 retention rules: an invalid
// value warns and falls back to the default (max_backups 1, others 0); values
// above the recommended thresholds are accepted with a warning.
func validateBackupRetention(r *BackupRetentionConfig) {
	if r.MaxBackups < 1 {
		log.Printf("[config] WARNING: max_backups: %d invalid, using default 1", r.MaxBackups)
		r.MaxBackups = 1
	}
	for _, f := range []struct {
		name string
		v    *int
	}{{"keep_weekly", &r.KeepWeekly}, {"keep_monthly", &r.KeepMonthly}, {"keep_yearly", &r.KeepYearly}} {
		if *f.v < 0 {
			log.Printf("[config] WARNING: %s: %d invalid, using default 0", f.name, *f.v)
			*f.v = 0
		}
	}
	if r.MaxBackups > 7 {
		log.Printf("[config] WARNING: max_backups: %d exceeds recommended 7 (%d days of daily backups)", r.MaxBackups, r.MaxBackups)
	}
	if r.KeepWeekly > 8 {
		log.Printf("[config] WARNING: keep_weekly: %d exceeds recommended 8", r.KeepWeekly)
	}
	if r.KeepMonthly > 12 {
		log.Printf("[config] WARNING: keep_monthly: %d exceeds recommended 12", r.KeepMonthly)
	}
	if r.KeepYearly > 2 {
		log.Printf("[config] WARNING: keep_yearly: %d exceeds recommended 2", r.KeepYearly)
	}
}

// CLIDistConfig is the server.cli block (PART 32 CLI Auto-Update).
type CLIDistConfig struct {
	BinaryDownload CLIBinaryDownloadConfig `yaml:"binary_download"`
}

// CLIBinaryDownloadConfig gates GET /cli/binaries/{name}. The default is a
// public download so new users can install the CLI before they hold a token.
type CLIBinaryDownloadConfig struct {
	RequireAuth bool `yaml:"require_auth"`
}

// I18nConfig is the server.i18n block (PART 30 Configuration).
type I18nConfig struct {
	Enabled            bool     `yaml:"enabled"`
	DefaultLanguage    string   `yaml:"default_language"`
	AvailableLanguages []string `yaml:"available_languages"`
	FallbackLanguage   string   `yaml:"fallback_language"`
	CookieName         string   `yaml:"cookie_name"`
	CookieMaxAge       string   `yaml:"cookie_max_age"`
}

// i18nSupported lists the languages embedded in every binary (PART 30).
var i18nSupported = []string{"en", "es", "zh", "fr", "ar", "de", "ja"}

// validateI18n keeps server.i18n usable: unknown codes are dropped, an empty
// list means all languages, the default and fallback must be available, and
// the cookie name/age must be sane. Invalid values warn and take the default.
func validateI18n(c *I18nConfig) {
	known := map[string]bool{}
	for _, l := range i18nSupported {
		known[l] = true
	}
	var avail []string
	seen := map[string]bool{}
	for _, l := range c.AvailableLanguages {
		l = strings.ToLower(strings.TrimSpace(l))
		if !known[l] {
			log.Printf("[config] WARNING: unsupported server.i18n.available_languages entry %q ignored", l)
			continue
		}
		if !seen[l] {
			seen[l] = true
			avail = append(avail, l)
		}
	}
	if len(avail) == 0 {
		avail = append([]string(nil), i18nSupported...)
		seen = map[string]bool{}
		for _, l := range avail {
			seen[l] = true
		}
	}
	c.AvailableLanguages = avail
	c.DefaultLanguage = strings.ToLower(strings.TrimSpace(c.DefaultLanguage))
	if !seen[c.DefaultLanguage] {
		log.Printf("[config] WARNING: server.i18n.default_language %q not available, using en", c.DefaultLanguage)
		c.DefaultLanguage = "en"
		if !seen["en"] {
			c.DefaultLanguage = avail[0]
		}
	}
	c.FallbackLanguage = strings.ToLower(strings.TrimSpace(c.FallbackLanguage))
	if !known[c.FallbackLanguage] {
		c.FallbackLanguage = "en"
	}
	c.CookieName = strings.TrimSpace(c.CookieName)
	if c.CookieName == "" || strings.ContainsAny(c.CookieName, " \t;=,\"") {
		c.CookieName = "lang"
	}
	if age, ok := ownerCookieMaxAge(strings.TrimSpace(c.CookieMaxAge)); !ok || age <= 0 {
		log.Printf("[config] WARNING: invalid server.i18n.cookie_max_age %q, using default 365d", c.CookieMaxAge)
		c.CookieMaxAge = "365d"
	}
}

// I18nCookieSeconds returns server.i18n.cookie_max_age in seconds (default one year).
func (c I18nConfig) I18nCookieSeconds() int {
	if age, ok := ownerCookieMaxAge(strings.TrimSpace(c.CookieMaxAge)); ok && age > 0 {
		return int(age / time.Second)
	}
	return 365 * 24 * 60 * 60
}

// validateUpdate canonicalizes server.update.branch (devel aliases accepted)
// and bounds defer_days to 0-365; invalid values warn and take the default.
func validateUpdate(u *UpdateConfig) {
	if canon, ok := updater.NormalizeBranch(u.Branch); ok {
		u.Branch = canon
	} else {
		log.Printf("[config] WARNING: invalid server.update.branch %q, using default stable", u.Branch)
		u.Branch = "stable"
	}
	if u.DeferDays < 0 || u.DeferDays > 365 {
		log.Printf("[config] WARNING: invalid server.update.defer_days %d (0-365), using default 0", u.DeferDays)
		u.DeferDays = 0
	}
}
