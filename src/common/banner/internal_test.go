package banner

// Internal tests for unexported banner functions.
// These run in the same package and can call unexported helpers directly.

import (
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/common/terminal"
)

// TestPrintStartupBannerCompact verifies the compact banner path runs without panic.
func TestPrintStartupBannerCompact(t *testing.T) {
	cfg := BannerConfig{
		AppName: "cairnbin",
		Version: "1.0.0",
		AppMode: "production",
		Debug:   false,
		URLs:    []string{"http://localhost:8080"},
	}
	printStartupBannerCompact(cfg)

	// With debug enabled
	cfg.Debug = true
	printStartupBannerCompact(cfg)
}

// TestPrintStartupBannerMinimal verifies the minimal banner path runs without panic.
func TestPrintStartupBannerMinimal(t *testing.T) {
	cfg := BannerConfig{
		AppName: "cairnbin",
		Version: "1.0.0",
		AppMode: "production",
		URLs:    []string{"http://localhost:8080", "http://127.0.0.1:8080"},
	}
	printStartupBannerMinimal(cfg)
}

// TestPrintStartupBannerMicro verifies the micro banner path runs without panic.
func TestPrintStartupBannerMicro(t *testing.T) {
	cfg := BannerConfig{
		AppName: "cairnbin",
		Version: "1.0.0",
	}
	printStartupBannerMicro(cfg)
}

// TestPrintStartupBannerFull_WithURLs verifies the full banner with URLs runs without panic.
func TestPrintStartupBannerFull_WithURLs(t *testing.T) {
	cfg := BannerConfig{
		AppName: "cairnbin",
		Version: "1.0.0",
		AppMode: "development",
		Debug:   true,
		URLs:    []string{"http://localhost:8080", "http://127.0.0.1:8080"},
	}
	size := terminal.TerminalSize{Cols: 100, Rows: 30, Mode: terminal.SizeModeStandard}
	printStartupBannerFull(cfg, size)
}

// TestPrintStartupBannerFull_NarrowWidth verifies the full banner at width=40 (padding clamped to 0).
func TestPrintStartupBannerFull_NarrowWidth(t *testing.T) {
	cfg := BannerConfig{
		AppName: "cairnbin",
		Version: "1.0.0",
		AppMode: "production",
		URLs:    []string{},
	}
	size := terminal.TerminalSize{Cols: 40, Rows: 24, Mode: terminal.SizeModeStandard}
	printStartupBannerFull(cfg, size)
}

// TestPrintStartupBannerFull_NoColor_URLArrow verifies NO_COLOR arrow rendering.
func TestPrintStartupBannerFull_NoColor_URLArrow(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cfg := BannerConfig{
		AppName: "cairnbin",
		Version: "1.0.0",
		AppMode: "production",
		URLs:    []string{"http://localhost:8080"},
	}
	size := terminal.TerminalSize{Cols: 80, Rows: 24, Mode: terminal.SizeModeStandard}
	printStartupBannerFull(cfg, size)
}

// TestExtractHostPort verifies scheme and path stripping.
func TestExtractHostPort(t *testing.T) {
	cases := []struct{ in, want string }{
		{"http://localhost:8080", "localhost:8080"},
		{"https://example.com", "example.com"},
		{"http://example.com/path/x", "example.com"},
		{"http://[::1]:8080", "[::1]:8080"},
		{"example.com:9000", "example.com:9000"},
	}
	for _, tc := range cases {
		if got := extractHostPort(tc.in); got != tc.want {
			t.Errorf("extractHostPort(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestExtractPort verifies the ":port" suffix extraction, including IPv6 hosts.
func TestExtractPort(t *testing.T) {
	cases := []struct{ in, want string }{
		{"http://localhost:8080", ":8080"},
		{"https://example.com", ""},
		{"http://[::1]:8080", ":8080"},
		{"http://[::1]", ""},
	}
	for _, tc := range cases {
		if got := extractPort(tc.in); got != tc.want {
			t.Errorf("extractPort(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestStartupDatetime verifies the user-facing datetime format (AI.md:19944).
func TestStartupDatetime(t *testing.T) {
	ts := time.Date(2025, time.December, 4, 13, 5, 13, 0, time.UTC)
	got := startupDatetime(ts)
	want := "December 4, 2025 at 13:05:13 UTC"
	if got != want {
		t.Errorf("startupDatetime() = %q, want %q", got, want)
	}
	if startupDatetime(time.Time{}) == "" {
		t.Error("startupDatetime(zero) returned empty string")
	}
}

// TestURLIconLabel verifies URL classification (AI.md:19640-19652).
func TestURLIconLabel(t *testing.T) {
	cases := []struct {
		url  string
		icon string
	}{
		{"http://abcdefabcdef.onion", "🧅"},
		{"http://example.i2p", "🔗"},
		{"https://example.com", "🔐"},
		{"http://[2001:db8::1]:8080", "🌍"},
		{"http://example.com:8080", "🌐"},
	}
	for _, tc := range cases {
		icon, _ := urlIconLabel(tc.url)
		if icon != tc.icon {
			t.Errorf("urlIconLabel(%q) icon = %q, want %q", tc.url, icon, tc.icon)
		}
	}
}

// TestModeIconAndSuffix verifies mode icon selection and [debugging] suffix (AI.md:19845-19852).
func TestModeIconAndSuffix(t *testing.T) {
	if got := modeIcon("development"); got != "🔧" {
		t.Errorf("modeIcon(development) = %q, want 🔧", got)
	}
	if got := modeIcon("production"); got != "🔒" {
		t.Errorf("modeIcon(production) = %q, want 🔒", got)
	}
	if got := modeSuffix(true); got != " [debugging]" {
		t.Errorf("modeSuffix(true) = %q, want \" [debugging]\"", got)
	}
	if got := modeSuffix(false); got != "" {
		t.Errorf("modeSuffix(false) = %q, want \"\"", got)
	}
}

// TestPrintStartupBannerMicro_FirstRun verifies the [SETUP] micro variant (AI.md:20115-20117).
func TestPrintStartupBannerMicro_FirstRun(t *testing.T) {
	cfg := BannerConfig{
		AppName:   "cairnbin",
		Version:   "1.0.0",
		ListenURL: "http://0.0.0.0:8080",
		FirstRun:  true,
	}
	printStartupBannerMicro(cfg)
}

// TestPrintStartupBannerPlain verifies the NO_COLOR/TERM=dumb variant runs without panic.
func TestPrintStartupBannerPlain(t *testing.T) {
	cfg := BannerConfig{
		AppName:   "cairnbin",
		Version:   "1.0.0",
		AppMode:   "production",
		Debug:     true,
		PublicURL: "https://example.com",
		ListenURL: "https://0.0.0.0:443",
	}
	printStartupBannerPlain(cfg)
}

// TestEmojiEnabled verifies NO_COLOR and TERM=dumb both force the plain banner.
func TestEmojiEnabled(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	if !emojiEnabled() {
		t.Error("emojiEnabled() = false with color terminal, want true")
	}
	t.Setenv("NO_COLOR", "1")
	if emojiEnabled() {
		t.Error("emojiEnabled() = true with NO_COLOR set, want false")
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	if emojiEnabled() {
		t.Error("emojiEnabled() = true with TERM=dumb, want false")
	}
}
