package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestCSSPolicy enforces PART 16: no !important outside print styles and
// logical properties instead of physical left/right ones (RTL support).
func TestCSSPolicy(t *testing.T) {
	files, err := filepath.Glob("static/css/*.tmpl")
	if err != nil || len(files) == 0 {
		t.Fatalf("no css templates found: %v", err)
	}
	physical := regexp.MustCompile(`^\s*(margin|padding|border)-(left|right)\s*:|^\s*(left|right)\s*:|text-align:\s*(left|right)`)
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		inPrint := false
		for i, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, "@media print") {
				inPrint = true
			}
			if strings.Contains(line, "!important") && !inPrint && !strings.Contains(line, "/*") {
				t.Errorf("%s:%d: !important outside print styles", f, i+1)
			}
			if physical.MatchString(line) && !strings.Contains(line, "left: 50%") {
				t.Errorf("%s:%d: physical property, use a logical one: %s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
}
