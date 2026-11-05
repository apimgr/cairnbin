package banner

import (
	"strings"
	"testing"
)

func TestColorEnabled(t *testing.T) {
	cases := []struct {
		name   string
		envVal string
		setEnv bool
		want   bool
	}{
		{"no_color_empty", "", true, true},
		{"no_color_one", "1", true, false},
		{"no_color_any", "any", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setEnv {
				t.Setenv("NO_COLOR", tc.envVal)
			}
			got := colorEnabled()
			if got != tc.want {
				t.Errorf("colorEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestColorize_WithColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	result := colorize("hello", "32")
	if !strings.Contains(result, "hello") {
		t.Errorf("colorize result %q does not contain %q", result, "hello")
	}
	if !strings.Contains(result, "\033[") {
		t.Errorf("colorize result %q does not contain ANSI escape sequence", result)
	}
}

func TestColorize_NoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	got := colorize("text", "31")
	if got != "text" {
		t.Errorf("colorize() = %q, want %q", got, "text")
	}
}

func TestAsciiArt_Empty(t *testing.T) {
	got := asciiArt("", 80)
	if got != "" {
		t.Errorf("asciiArt(\"\", 80) = %q, want %q", got, "")
	}
}

func TestAsciiArt_TooNarrow(t *testing.T) {
	got := asciiArt("hi", 10)
	if got != "" {
		t.Errorf("asciiArt(\"hi\", 10) = %q, want %q", got, "")
	}
}

func TestAsciiArt_Normal(t *testing.T) {
	got := asciiArt("hi", 80)
	if got == "" {
		t.Error("asciiArt(\"hi\", 80) returned empty string, want non-empty")
	}
	if !strings.Contains(got, "#") {
		t.Errorf("asciiArt(\"hi\", 80) = %q, want result containing \"#\"", got)
	}
}

func TestAsciiArt_TooLong(t *testing.T) {
	got := asciiArt("averylongnamethatexceedsmaxwidth", 20)
	if got != "" {
		t.Errorf("asciiArt(\"averylongnamethatexceedsmaxwidth\", 20) = %q, want %q", got, "")
	}
}

func TestPrintStartupBanner_DoesNotPanic(t *testing.T) {
	cfg := BannerConfig{
		AppName: "cairnbin",
		Version: "1.0.0",
		AppMode: "production",
		Debug:   false,
		URLs:    []string{"http://localhost:64001"},
	}
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("PrintStartupBanner panicked: %v", r)
		}
	}()
	PrintStartupBanner(cfg)
}
