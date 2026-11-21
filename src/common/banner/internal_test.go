package banner

// Internal tests for unexported banner functions.
// These run in the same package and can call unexported helpers directly.

import (
	"bytes"
	"strings"
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
	printStartupBannerCompact(&bytes.Buffer{}, cfg, 60)

	// With debug enabled
	cfg.Debug = true
	printStartupBannerCompact(&bytes.Buffer{}, cfg, 60)
}

// TestPrintStartupBannerMinimal verifies the minimal banner path runs without panic.
func TestPrintStartupBannerMinimal(t *testing.T) {
	cfg := BannerConfig{
		AppName: "cairnbin",
		Version: "1.0.0",
		AppMode: "production",
		URLs:    []string{"http://localhost:8080", "http://127.0.0.1:8080"},
	}
	printStartupBannerMinimal(&bytes.Buffer{}, cfg, 59)
}

// TestPrintStartupBannerMicro verifies the micro banner path runs without panic.
func TestPrintStartupBannerMicro(t *testing.T) {
	cfg := BannerConfig{
		AppName: "cairnbin",
		Version: "1.0.0",
	}
	printStartupBannerMicro(&bytes.Buffer{}, cfg, 39)
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
	printStartupBannerFull(&bytes.Buffer{}, cfg, size)
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
	printStartupBannerFull(&bytes.Buffer{}, cfg, size)
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
	printStartupBannerFull(&bytes.Buffer{}, cfg, size)
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
	t.Setenv("TZ", "")
	ts := time.Date(2025, time.December, 4, 13, 5, 13, 0, time.UTC)
	got := startupDatetime(ts)
	want := "December 04, 2025 at 08:05:13 EST"
	if got != want {
		t.Errorf("startupDatetime() = %q, want %q", got, want)
	}
	t.Setenv("TZ", "UTC")
	if got, want := startupDatetime(ts), "December 04, 2025 at 13:05:13 UTC"; got != want {
		t.Errorf("startupDatetime() with TZ=UTC = %q, want %q", got, want)
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
	if got := modeIcon("debug"); got != "🐛" {
		t.Errorf("modeIcon(debug) = %q, want 🐛", got)
	}
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
	printStartupBannerMicro(&bytes.Buffer{}, cfg, 39)
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
	printStartupBannerPlain(&bytes.Buffer{}, cfg, 80)
}

func TestBannerVariantsStayWithinTerminalWidth(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cfg := BannerConfig{
		AppName:   strings.Repeat("cairnbin-", 12),
		Version:   "development-build-with-a-long-version-string",
		AppMode:   "development",
		PublicURL: "https://" + strings.Repeat("long-subdomain.", 8) + "example.test",
		TorURL:    "http://6yx4fqeve67l47gzb3rfxxr2vauv6qepwkefm2hk73xukg3zupndf5qd.onion",
		StartedAt: time.Date(2026, time.November, 20, 19, 36, 9, 0, time.UTC),
	}
	variants := []struct {
		name   string
		width  int
		render func(*bytes.Buffer)
	}{
		{"full", 80, func(w *bytes.Buffer) {
			printStartupBannerFull(w, cfg, terminal.TerminalSize{Cols: 80, Rows: 24, Mode: terminal.SizeModeStandard})
		}},
		{"compact", 60, func(w *bytes.Buffer) { printStartupBannerCompact(w, cfg, 60) }},
		{"minimal", 59, func(w *bytes.Buffer) { printStartupBannerMinimal(w, cfg, 59) }},
		{"micro", 39, func(w *bytes.Buffer) { printStartupBannerMicro(w, cfg, 39) }},
		{"plain", 80, func(w *bytes.Buffer) { printStartupBannerPlain(w, cfg, 80) }},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			var output bytes.Buffer
			variant.render(&output)
			lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
			if variant.name == "micro" && len(lines) != 1 {
				t.Fatalf("micro banner has %d lines, want one", len(lines))
			}
			for _, line := range lines {
				if got := displayWidth(line); got > variant.width {
					t.Errorf("banner line width = %d, exceeds %d columns: %q", got, variant.width, line)
				}
			}
		})
	}
}

func TestPrintStartupBannerFullWrapsLongOverlay(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var output bytes.Buffer
	cfg := BannerConfig{
		AppName:   "cairnbin",
		Version:   "devel",
		AppMode:   "development",
		TorURL:    "http://6yx4fqeve67l47gzb3rfxxr2vauv6qepwkefm2hk73xukg3zupndf5qd.onion",
		PublicURL: "http://pste.us",
		StartedAt: time.Date(2026, time.November, 20, 19, 36, 9, 0, time.UTC),
	}
	printStartupBannerFull(&output, cfg, terminal.TerminalSize{Cols: 80, Rows: 24, Mode: terminal.SizeModeStandard})
	for _, line := range strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n") {
		if displayWidth(line) > 80 {
			t.Errorf("banner line width = %d, exceeds terminal width 80: %q", displayWidth(line), line)
		}
	}
	if !strings.Contains(output.String(), "Running in mode: development") {
		t.Error("banner omits the required mode label")
	}
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
