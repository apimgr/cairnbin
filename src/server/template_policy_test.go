package server

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// inlineHandler matches an inline event-handler attribute (onclick=, onsubmit=,
// ...). The CSP ships script-src 'self' only, so these never run, and PART 16
// forbids them: handlers are bound in static/js/app.js via addEventListener.
var inlineHandler = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)

// TestTemplatesHaveNoInlineScriptOrStyle enforces the PART 16 frontend rules on
// every template: no inline event handlers, no style="" attributes, no inline
// <style> blocks, and no inline executable <script> (data blocks such as
// application/json and <script src> are allowed).
func TestTemplatesHaveNoInlineScriptOrStyle(t *testing.T) {
	scriptTag := regexp.MustCompile(`(?is)<script\b([^>]*)>`)
	err := filepath.WalkDir("template", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".tmpl") {
			return err
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		body := string(data)
		if loc := inlineHandler.FindStringIndex(body); loc != nil {
			t.Errorf("%s: inline event handler attribute near %q", path, body[loc[0]:min(loc[1]+30, len(body))])
		}
		if strings.Contains(strings.ToLower(body), ` style="`) {
			t.Errorf("%s: inline style attribute", path)
		}
		if strings.Contains(strings.ToLower(body), "<style") {
			t.Errorf("%s: inline <style> block", path)
		}
		for _, m := range scriptTag.FindAllStringSubmatch(body, -1) {
			attrs := strings.ToLower(m[1])
			if !strings.Contains(attrs, "src=") && !strings.Contains(attrs, "application/json") {
				t.Errorf("%s: inline executable <script>", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk templates: %v", err)
	}
}

// TestJSHasNoBlockingDialogs forbids alert(), confirm() and prompt() in the
// single frontend script (PART 16: use native <dialog> and toasts).
func TestJSHasNoBlockingDialogs(t *testing.T) {
	data, err := os.ReadFile("static/js/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	blocking := regexp.MustCompile(`(^|[^.\w])(alert|confirm|prompt)\(`)
	for i, line := range strings.Split(string(data), "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "//") || strings.HasPrefix(trim, "*") {
			continue
		}
		if blocking.MatchString(line) {
			t.Errorf("app.js:%d: blocking dialog call: %s", i+1, trim)
		}
	}
}

func TestFrontendValidationAndToastContracts(t *testing.T) {
	data, err := os.ReadFile("static/js/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	script := string(data)
	for _, required := range []string{
		"form input:not([type=\"hidden\"]):not([type=\"submit\"]):not([type=\"button\"]):not([type=\"reset\"]), form select, form textarea",
		"el.value = el.value.trim()",
		"el.setCustomValidity(hasEdgeWhitespace ? t('password_whitespace') : '')",
		"form.requestSubmit(event.submitter || undefined)",
		"function showToast(message, type, requestedDuration)",
		"requestedDuration === undefined ? TOAST_DURATIONS[toastType] : Number(requestedDuration)",
		"const TOAST_DURATIONS = { success: 3000, info: 3000, warning: 5000, error: 0 }",
		"if (duration > 0) armToastTimer(toast, duration)",
		"if (toast.__toastDuration > 0) armToastTimer(toast, toast.__toastDuration)",
		"function requestClosePreferences()",
		"t('unsaved_changes')",
		"dialog.__requestClose = requestClosePreferences",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("app.js is missing frontend contract %q", required)
		}
	}
}

// TestTemplatesAccessibilityBasics enforces the static half of PART 30
// "Testing Requirements": skip links that target real anchors, landmark
// elements, alt text on every image, and a programmatic label for every
// visible form control.
func TestTemplatesAccessibilityBasics(t *testing.T) {
	read := func(p string) string {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		return string(data)
	}
	header := read("template/partial/public/header.tmpl")
	nav := read("template/partial/public/nav.tmpl")
	layout := read("template/layout/public.tmpl")
	footer := read("template/partial/public/footer.tmpl")

	if !strings.Contains(header, `href="#main-content"`) || !strings.Contains(layout, `id="main-content"`) {
		t.Error("skip link to #main-content or its <main id=main-content> target is missing")
	}
	if !strings.Contains(header, `href="#navigation"`) || !strings.Contains(nav, `id="navigation"`) {
		t.Error("skip link to #navigation or its id=navigation target is missing")
	}
	if strings.Index(layout, `template "header"`) > strings.Index(layout, `<main`) {
		t.Error("header (skip links) must precede <main> so the skip links are the first focusable elements")
	}
	for name, body := range map[string]string{"<main": layout, "<nav": nav, "<footer": footer} {
		if !strings.Contains(body, name) {
			t.Errorf("landmark element %s missing", name)
		}
	}

	img := regexp.MustCompile(`(?is)<img\b[^>]*>`)
	control := regexp.MustCompile(`(?is)<(input|select|textarea)\b[^>]*>`)
	typ := regexp.MustCompile(`(?i)\btype="(hidden|submit|button|checkbox|radio|image)"`)
	err := filepath.WalkDir("template", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".tmpl") {
			return err
		}
		body := read(path)
		for _, m := range img.FindAllString(body, -1) {
			if !strings.Contains(strings.ToLower(m), "alt=") {
				t.Errorf("%s: <img> without alt: %s", path, m)
			}
		}
		for _, loc := range control.FindAllStringIndex(body, -1) {
			m := body[loc[0]:loc[1]]
			low := strings.ToLower(m)
			if typ.MatchString(m) && !strings.Contains(low, `type="checkbox"`) && !strings.Contains(low, `type="radio"`) {
				continue
			}
			// A control wrapped in <label>...</label> is implicitly labelled.
			before := strings.ToLower(body[:loc[0]])
			if i := strings.LastIndex(before, "<label"); i != -1 && !strings.Contains(before[i:], "</label>") {
				continue
			}
			if !strings.Contains(low, "aria-label") && !strings.Contains(low, "aria-labelledby") && !strings.Contains(low, ` id=`) {
				t.Errorf("%s: form control without id/aria-label: %s", path, m)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk templates: %v", err)
	}
}

// TestCSSFollowsMobileFirstPolicy enforces the PART 16 stylesheet rules on the
// three embedded CSS templates: mobile-first (no max-width media queries),
// only the spec breakpoints, !important only inside print styles, and the
// CSS Variable Reference custom properties defined once in common.css.
func TestCSSFollowsMobileFirstPolicy(t *testing.T) {
	files := []string{"static/css/common.css.tmpl", "static/css/components.css.tmpl", "static/css/public.css.tmpl"}
	maxWidth := regexp.MustCompile(`@media[^{]*\(max-width`)
	minWidth := regexp.MustCompile(`@media[^{]*\(min-width:\s*(\d+)px\)`)
	allowed := map[string]bool{"768": true, "1024": true, "1280": true}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		body := string(data)
		if maxWidth.MatchString(body) {
			t.Errorf("%s: desktop-first max-width media query", f)
		}
		for _, m := range minWidth.FindAllStringSubmatch(body, -1) {
			if !allowed[m[1]] {
				t.Errorf("%s: non-spec breakpoint min-width: %spx", f, m[1])
			}
		}
		// !important is allowed only inside the @media print block, which is
		// the last block that opens with "@media print" in common.css.
		stripped := body
		if i := strings.Index(stripped, "@media print"); i != -1 {
			if j := strings.Index(stripped[i:], "\n}\n"); j != -1 {
				stripped = stripped[:i] + stripped[i+j:]
			}
		}
		if strings.Contains(stripped, "!important") {
			t.Errorf("%s: !important outside print styles", f)
		}
	}

	common, err := os.ReadFile("static/css/common.css.tmpl")
	if err != nil {
		t.Fatalf("read common.css.tmpl: %v", err)
	}
	for _, v := range []string{
		"--color-info-bg", "--color-secondary", "--color-accent:", "--color-on-error",
		"--z-modal", "--z-toast", "--focus-ring", "--focus-ring-offset",
		"--radius-base", "--shadow-xl", "--transition-spring", "--font-serif",
		"--font-weight-bold", "--line-height-relaxed", "--space-24",
	} {
		if !strings.Contains(string(common), v) {
			t.Errorf("common.css.tmpl: CSS Variable Reference property %s missing", v)
		}
	}
}
