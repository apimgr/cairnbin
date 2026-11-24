package config

import (
	"strings"
	"testing"
)

func TestValidateFooterHTML(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantErr   bool
		wantEmpty bool
	}{
		{"empty passes through", "", false, true},
		{"space sentinel passes through", " ", false, false},
		{"safe html passes through unchanged", "<p>Powered by <strong>MyCompany</strong></p>", false, false},
		{"script only is fully stripped and rejected", "<script>alert('xss')</script>", true, false},
		{"event handler is stripped while safe image remains", `<img src="https://example.com/x.png" onerror="alert('xss')">`, false, false},
		{"iframe only is fully stripped and rejected", `<iframe src="https://evil.com"></iframe>`, true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateFooterHTML(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateFooterHTML(%q): expected error, got nil (sanitized=%q)", tc.input, got)
				}
				if got != "" {
					t.Fatalf("ValidateFooterHTML(%q): expected empty result on error, got %q", tc.input, got)
				}
				return
			}
			if strings.Contains(got, "onerror") {
				t.Fatalf("ValidateFooterHTML(%q): event handler survived sanitization: %q", tc.input, got)
			}
			if err != nil {
				t.Fatalf("ValidateFooterHTML(%q): unexpected error: %v", tc.input, err)
			}
			if tc.wantEmpty && got != "" {
				t.Fatalf("ValidateFooterHTML(%q): expected empty result, got %q", tc.input, got)
			}
		})
	}
}

func TestValidateFooterHTMLPartiallyStrippedNotRejected(t *testing.T) {
	// Content with a mix of safe and unsafe elements sanitizes to non-empty
	// output and must not be rejected — only fully-stripped content errors.
	got, err := ValidateFooterHTML(`<p>Built with</p><script>alert(1)</script>`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == "" {
		t.Fatalf("expected non-empty sanitized output")
	}
}

func TestFooterSanitizerAllowsHTTPSAndRelativeLinksButRejectsDataURLs(t *testing.T) {
	input := `<p><a href="https://example.com">external</a> <a href="/server/help">internal</a> <img src="data:image/svg+xml,<svg onload=alert(1)>" alt="unsafe"></p>`
	got := SanitizeFooterHTML(input)
	if !strings.Contains(got, `href="https://example.com"`) || !strings.Contains(got, `href="/server/help"`) {
		t.Fatalf("safe links were not retained: %s", got)
	}
	if strings.Contains(strings.ToLower(got), "data:") || strings.Contains(strings.ToLower(got), "onload") {
		t.Fatalf("unsafe data URL or event handler survived: %s", got)
	}
}
