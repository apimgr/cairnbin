package theme_test

import (
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/common/theme"
)

func TestGetThemePalette(t *testing.T) {
	cases := []struct {
		name           string
		input          string
		wantBackground string
		anyOf          []string
	}{
		{"dark", "dark", "#282a36", nil},
		{"light", "light", "#ffffff", nil},
		{"empty defaults to dark", "", "#282a36", nil},
		{"unknown defaults to dark", "unknown", "#282a36", nil},
		{"auto returns dark or light", "auto", "", []string{"#282a36", "#ffffff"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := theme.GetThemePalette(tc.input)
			if len(tc.anyOf) > 0 {
				for _, v := range tc.anyOf {
					if got.Background == v {
						return
					}
				}
				t.Errorf("GetThemePalette(%q).Background = %q, want one of %v", tc.input, got.Background, tc.anyOf)
				return
			}
			if got.Background != tc.wantBackground {
				t.Errorf("GetThemePalette(%q).Background = %q, want %q", tc.input, got.Background, tc.wantBackground)
			}
		})
	}
}

func TestThemePalette_ToCSSVariables(t *testing.T) {
	p := theme.ThemePaletteDark
	got := p.ToCSSVariables()

	expectedVars := []string{
		"--color-background",
		"--color-foreground",
		"--color-primary",
		"--color-secondary",
		"--color-accent",
		"--color-success",
		"--color-warning",
		"--color-error",
		"--color-info",
		"--color-surface",
		"--color-surface-alt",
		"--color-border",
		"--color-muted",
	}
	for _, v := range expectedVars {
		if !strings.Contains(got, v) {
			t.Errorf("ToCSSVariables() missing %q", v)
		}
	}

	for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasSuffix(trimmed, ";") {
			t.Errorf("ToCSSVariables() line does not end with ';': %q", line)
		}
	}
}

func TestThemePalette_ToRootCSS(t *testing.T) {
	p := theme.ThemePaletteDark
	got := p.ToRootCSS()

	if !strings.HasPrefix(got, ":root {") {
		t.Errorf("ToRootCSS() does not start with ':root {', got prefix: %q", got[:min(len(got), 20)])
	}
	if !strings.HasSuffix(strings.TrimRight(got, "\n"), "}") {
		t.Errorf("ToRootCSS() does not end with '}', got: %q", got[max(0, len(got)-20):])
	}

	cssVars := p.ToCSSVariables()
	if !strings.Contains(got, cssVars) {
		t.Errorf("ToRootCSS() does not contain ToCSSVariables() output")
	}
}

func TestThemePaletteDark_Fields(t *testing.T) {
	p := theme.ThemePaletteDark
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"Background", p.Background, "#282a36"},
		{"Foreground", p.Foreground, "#f8f8f2"},
		{"Primary", p.Primary, "#bd93f9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("ThemePaletteDark.%s = %q, want %q", tc.name, tc.got, tc.want)
			}
		})
	}
}

func TestThemePaletteLight_Fields(t *testing.T) {
	p := theme.ThemePaletteLight
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"Background", p.Background, "#ffffff"},
		{"Foreground", p.Foreground, "#1f2328"},
		{"Primary", p.Primary, "#0969da"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("ThemePaletteLight.%s = %q, want %q", tc.name, tc.got, tc.want)
			}
		})
	}
}

func TestIsSystemDarkTheme_ReturnsBool(t *testing.T) {
	_ = theme.IsSystemDarkTheme()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
