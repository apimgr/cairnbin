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
