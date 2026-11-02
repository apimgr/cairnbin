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
