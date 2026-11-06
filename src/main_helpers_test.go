package main

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestNormalizeArgs verifies short-flag expansion and single-dash long-flag
// conversion performed before flag parsing.
func TestNormalizeArgs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "short help and version expand",
			in:   []string{"-h", "-v"},
			want: []string{"--help", "--version"},
		},
		{
			name: "single dash long flag passes through unchanged (spec: only -h/-v are short flags)",
			in:   []string{"-config", "/etc"},
			want: []string{"-config", "/etc"},
		},
		{
			name: "double dash flag is untouched",
			in:   []string{"--port", "8080"},
			want: []string{"--port", "8080"},
		},
		{
			name: "short single-char unknown flag is untouched",
			in:   []string{"-x"},
			want: []string{"-x"},
		},
		{
			name: "positional argument is untouched",
			in:   []string{"cairnbin-id", "list"},
			want: []string{"cairnbin-id", "list"},
		},
		{
			name: "empty input",
			in:   []string{},
			want: []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeArgs(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("normalizeArgs(%v) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("normalizeArgs(%v)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestApplyColor verifies the --color flag mutates NO_COLOR per spec PART 8.
func TestApplyColor(t *testing.T) {
	t.Run("no sets NO_COLOR", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		_ = os.Unsetenv("NO_COLOR")
		applyColor("no")
		if os.Getenv("NO_COLOR") != "1" {
			t.Fatalf("applyColor(no): NO_COLOR = %q, want 1", os.Getenv("NO_COLOR"))
		}
	})

	t.Run("never alias sets NO_COLOR", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		_ = os.Unsetenv("NO_COLOR")
		applyColor("never")
		if os.Getenv("NO_COLOR") != "1" {
			t.Fatalf("applyColor(never): NO_COLOR = %q, want 1", os.Getenv("NO_COLOR"))
		}
	})

	t.Run("yes unsets NO_COLOR", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		applyColor("yes")
		if _, ok := os.LookupEnv("NO_COLOR"); ok {
			t.Fatalf("applyColor(yes): NO_COLOR should be unset")
		}
	})

	t.Run("always alias unsets NO_COLOR", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		applyColor("always")
		if _, ok := os.LookupEnv("NO_COLOR"); ok {
			t.Fatalf("applyColor(always): NO_COLOR should be unset")
		}
	})

	t.Run("auto leaves NO_COLOR set", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		applyColor("auto")
		if os.Getenv("NO_COLOR") != "1" {
			t.Fatalf("applyColor(auto): NO_COLOR = %q, want unchanged 1", os.Getenv("NO_COLOR"))
		}
	})

	t.Run("empty leaves NO_COLOR unset", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		_ = os.Unsetenv("NO_COLOR")
		applyColor("")
		if _, ok := os.LookupEnv("NO_COLOR"); ok {
			t.Fatalf("applyColor(empty): NO_COLOR should remain unset")
		}
	})
}

// TestPrintHelp verifies the help output contains the binary name and the key
// flag sections documented in the spec.
func TestPrintHelp(t *testing.T) {
	var buf strings.Builder
	printHelp(&buf, "cairnbin")
	got := buf.String()

	wantSubstrings := []string{
		"cairnbin",
		"Usage:",
		"--help",
		"--version",
		"--port",
		"--daemon",
		"--color",
	}
	for _, s := range wantSubstrings {
		if !strings.Contains(got, s) {
			t.Errorf("printHelp output missing %q", s)
		}
	}
}

// TestLogSchedErr verifies the helper tolerates a nil error and logs a non-nil
// error without panicking.
func TestLogSchedErr(t *testing.T) {
	logSchedErr(nil)
	logSchedErr(context.DeadlineExceeded)
}
