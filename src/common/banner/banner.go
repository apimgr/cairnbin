package banner

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/apimgr/pastebin/src/common/i18n"
	"github.com/apimgr/pastebin/src/common/terminal"
)

// BannerConfig holds configuration for the startup banner
type BannerConfig struct {
	AppName string
	Version string
	// production/development
	AppMode string
	Debug   bool
	// URLs is the legacy list of extra public URLs; kept for API compat.
	// New call sites should prefer PublicURL/ListenURL/TorURL.
	URLs []string
	// Lang selects the locale for translatable labels; empty means "en".
	Lang string
	// PublicURL is the canonical {proto}://{fqdn}[:{port}] website URL.
	PublicURL string
	// ListenURL is the raw {proto}://{address}[:{port}] the server binds.
	ListenURL string
	// TorURL is the hidden-service URL (http://{onion}); empty when Tor is off.
	TorURL string
	// FirstRun is true when server.yml was generated this startup (micro shows [SETUP]).
	FirstRun bool
	// StartedAt is the startup timestamp; zero value means time.Now().
	StartedAt time.Time
}

// bannerURL pairs a URL with its display icon and label (AI.md:19640-19652).
type bannerURL struct {
	Icon  string
	Label string
	URL   string
}

// PrintStartupBanner prints a responsive startup banner based on terminal size
// (AI.md:19630-20172). NO_COLOR or TERM=dumb selects the plain variant; sizes:
// ≥80 full box + ASCII art, 60-79 compact, 40-59 minimal, <40 micro.
func PrintStartupBanner(cfg BannerConfig) {
	if !emojiEnabled() {
		printStartupBannerPlain(cfg)
		return
	}
	size := terminal.GetTerminalSize()

	switch {
	case size.Mode >= terminal.SizeModeStandard:
		printStartupBannerFull(cfg, size)
	case size.Mode >= terminal.SizeModeCompact:
		printStartupBannerCompact(cfg)
	case size.Mode >= terminal.SizeModeMinimal:
		printStartupBannerMinimal(cfg)
	default:
		printStartupBannerMicro(cfg)
	}
}

// colorEnabled returns true if ANSI color output is allowed
func colorEnabled() bool {
	return os.Getenv("NO_COLOR") == ""
}

// emojiEnabled returns true when icons and box drawing are allowed.
// NO_COLOR or TERM=dumb forces the plain-text banner (AI.md:20119-20135).
func emojiEnabled() bool {
	return os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
}

// colorize wraps text in an ANSI color code if color is enabled
func colorize(text, ansiCode string) string {
	if !colorEnabled() {
		return text
	}
	return "\033[" + ansiCode + "m" + text + "\033[0m"
}

// modeIcon returns the mode indicator icon: 🔧 development, 🔒 otherwise (AI.md:19845-19850).
func modeIcon(appMode string) string {
	if appMode == "development" || appMode == "debug" {
		return "🔧"
	}
	return "🔒"
}

// modeSuffix returns " [debugging]" only when debug was explicitly enabled (AI.md:19852).
func modeSuffix(debug bool) string {
	if debug {
		return " [debugging]"
	}
	return ""
}

// modeLabel returns the localized short mode label used by compact/plain variants.
func modeLabel(lang string) string {
	if lang == "" {
		lang = "en"
	}
	return i18n.Translate(lang, "cli.running_in_mode_label")
}

// urlIconLabel classifies a URL for banner display (AI.md:19640-19652):
// 🧅 Tor, 🔗 I2P, 🔐 HTTPS, 🌍 IPv6, 🌐 HTTP.
func urlIconLabel(url string) (string, string) {
	host := extractHostPort(url)
	switch {
	case strings.Contains(host, ".onion"):
		return "🧅", "Tor:  "
	case strings.Contains(host, ".i2p"):
		return "🔗", "I2P:  "
	case strings.HasPrefix(host, "["):
		return "🌍", "IPv6: "
	case strings.HasPrefix(url, "https://"):
		return "🔐", "HTTPS:"
	default:
		return "🌐", "HTTP: "
	}
}

// extractHostPort strips the scheme and path from a URL, returning host[:port].
func extractHostPort(url string) string {
	s := url
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return s
}

// extractPort returns the ":port" suffix of a URL, or "" when none is present.
func extractPort(url string) string {
	host := extractHostPort(url)
	// IPv6 literal: port follows the closing bracket.
	if i := strings.LastIndex(host, "]"); i >= 0 {
		host = host[i+1:]
	}
	if i := strings.LastIndex(host, ":"); i >= 0 {
		return host[i:]
	}
	return ""
}

// startupDatetime formats the startup timestamp in the required user-facing
// format "%B %-d, %Y at %H:%M:%S %Z" → "December 4, 2025 at 13:05:13 EST" (AI.md:19944).
func startupDatetime(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.Format("January 2, 2006 at 15:04:05 MST")
}

// bannerURLs builds the ordered list of overlay/extra rows — Tor, I2P, SMTP,
// and any legacy extras — always shown when present (AI.md:19950-20067
// example ordering). The canonical public HTTP(S) URL is not listed here: it
// is shown once, via the "Listening on" row (see listeningURL), never
// duplicated as a separate row.
func bannerURLs(cfg BannerConfig) []bannerURL {
	var out []bannerURL
	add := func(url string) {
		if url == "" {
			return
		}
		icon, label := urlIconLabel(url)
		out = append(out, bannerURL{Icon: icon, Label: label, URL: url})
	}
	add(cfg.TorURL)
	for _, u := range cfg.URLs {
		add(u)
	}
	return out
}

// listeningURL returns the value for the "Listening on" banner row. Per
// AI.md step 20: "the banner's 'Listening on' line ALWAYS shows the resolved
// {fqdn}, never the raw bind {address}" — this applies in every mode,
// including development and first-run; only the step-21 log line may show
// the raw {address}.
func listeningURL(cfg BannerConfig) string {
	if cfg.PublicURL != "" {
		return cfg.PublicURL
	}
	return cfg.ListenURL
}

// displayWidth returns the printed cell width of s, counting emojis as 2 cells.
func displayWidth(s string) int {
	return runewidth.StringWidth(s)
}

// printStartupBannerFull prints the full box-drawing banner with ASCII art
// header for terminals ≥80 cols (AI.md:19950-20067).
func printStartupBannerFull(cfg BannerConfig, size terminal.TerminalSize) {
	width := size.Cols
	if width > 80 {
		width = 80
	}
	if width < 40 {
		width = 40
	}

	if art := asciiArt(cfg.AppName, width); art != "" {
		fmt.Print(colorize(art, "1;36"))
	}

	line := strings.Repeat("─", width-2)
	row := func(text string) {
		pad := width - 2 - displayWidth(text)
		if pad < 0 {
			pad = 0
		}
		fmt.Println(colorize("│", "36") + text + strings.Repeat(" ", pad) + colorize("│", "36"))
	}
	sep := func() { fmt.Println(colorize("├"+line+"┤", "36")) }

	fmt.Println(colorize("╭"+line+"╮", "36"))
	row(fmt.Sprintf("  🚀 %s · 📦 %s", cfg.AppName, cfg.Version))
	sep()
	row(fmt.Sprintf("  %s %s: %s%s", modeIcon(cfg.AppMode), modeLabel(cfg.Lang), cfg.AppMode, modeSuffix(cfg.Debug)))

	if urls := bannerURLs(cfg); len(urls) > 0 {
		sep()
		for _, u := range urls {
			row(fmt.Sprintf("  %s %s %s", u.Icon, u.Label, u.URL))
		}
	}

	sep()
	if url := listeningURL(cfg); url != "" {
		row(fmt.Sprintf("  📡 Listening on %s", url))
	}
	row(fmt.Sprintf("  ✅ Server started on %s", startupDatetime(cfg.StartedAt)))
	fmt.Println(colorize("╰"+line+"╯", "36"))
}

// printStartupBannerCompact prints the compact banner for 60-79 cols:
// icons + text, no box drawing or ASCII art (AI.md:20077-20093).
func printStartupBannerCompact(cfg BannerConfig) {
	fmt.Printf("🚀 %s v%s\n", colorize(cfg.AppName, "1;36"), cfg.Version)
	fmt.Printf("%s %s: %s%s\n", modeIcon(cfg.AppMode), modeLabel(cfg.Lang), colorize(cfg.AppMode, "33"), modeSuffix(cfg.Debug))
	for _, u := range bannerURLs(cfg) {
		fmt.Printf("%s %s\n", u.Icon, colorize(u.URL, "32"))
	}
	if url := listeningURL(cfg); url != "" {
		fmt.Printf("📡 Listening: %s\n", colorize(url, "32"))
	}
	fmt.Printf("✅ Started: %s\n", startupDatetime(cfg.StartedAt))
}

// printStartupBannerMinimal prints the abbreviated banner for 40-59 cols:
// no icons — name/version, mode, then host:port per URL (AI.md:20095-20107).
func printStartupBannerMinimal(cfg BannerConfig) {
	fmt.Printf("%s %s\n", colorize(cfg.AppName, "1;36"), cfg.Version)
	fmt.Println(colorize(cfg.AppMode, "33"))
	if url := listeningURL(cfg); url != "" {
		fmt.Println(colorize(extractHostPort(url), "32"))
	}
	for _, u := range bannerURLs(cfg) {
		fmt.Println(colorize(extractHostPort(u.URL), "32"))
	}
}

// printStartupBannerMicro prints the single-line banner for <40 cols:
// "{name} :{port}", plus " [SETUP]" on first run (AI.md:20109-20117).
func printStartupBannerMicro(cfg BannerConfig) {
	port := extractPort(listeningURL(cfg))
	setup := ""
	if cfg.FirstRun {
		setup = " [SETUP]"
	}
	if port == "" {
		fmt.Printf("%s%s\n", cfg.AppName, setup)
		return
	}
	fmt.Printf("%s %s%s\n", cfg.AppName, port, setup)
}

// printStartupBannerPlain prints the raw-text banner for NO_COLOR / TERM=dumb:
// no emojis, no box drawing, no colors (AI.md:20119-20135).
func printStartupBannerPlain(cfg BannerConfig) {
	fmt.Printf("%s v%s\n", cfg.AppName, cfg.Version)
	fmt.Printf("%s: %s%s\n", modeLabel(cfg.Lang), cfg.AppMode, modeSuffix(cfg.Debug))
	for _, u := range bannerURLs(cfg) {
		fmt.Printf("URL: %s\n", u.URL)
	}
	if url := listeningURL(cfg); url != "" {
		fmt.Printf("Listening: %s\n", url)
	}
	fmt.Printf("Started: %s\n", startupDatetime(cfg.StartedAt))
}
