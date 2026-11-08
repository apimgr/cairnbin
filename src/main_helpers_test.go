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
	printHelp(&buf, "cairnbin", "en")
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

func TestPrintHelpUsesRequestedLanguage(t *testing.T) {
	var buf strings.Builder
	printHelp(&buf, "cairnbin", "es")
	if got := buf.String(); !strings.Contains(got, "Uso:") || !strings.Contains(got, "Mostrar ayuda") {
		t.Fatalf("Spanish help output is not localized: %q", got)
	}
}

func TestCLIServiceMessagesUseRequestedLanguage(t *testing.T) {
	if got, want := cliMessage("es", "service_started"), "Servicio iniciado."; got != want {
		t.Errorf("Spanish service message = %q, want %q", got, want)
	}
	got := cliMessage("es", "service_error", "project_name", "cairnbin", "action", "start", "error", "failed")
	for _, want := range []string{"cairnbin", "servicio", "start", "failed"} {
		if !strings.Contains(got, want) {
			t.Errorf("localized service error %q missing %q", got, want)
		}
	}
}

func TestServiceFailureUsesLocalizedGenericText(t *testing.T) {
	var stderr strings.Builder
	writeServiceFailure(&stderr, "es", "cairnbin", "start", os.ErrPermission)
	if !strings.Contains(stderr.String(), "falló la operación del servicio") || strings.Contains(stderr.String(), "permission denied") {
		t.Fatalf("localized service failure output = %q", stderr.String())
	}
}

func TestMaintenanceFailureUsesLocalizedGenericText(t *testing.T) {
	var stderr strings.Builder
	writeMaintenanceFailure(&stderr, "es", "cairnbin", "restore", os.ErrPermission)
	if !strings.Contains(stderr.String(), "falló la operación de mantenimiento restore") || strings.Contains(stderr.String(), "permission denied") {
		t.Fatalf("localized maintenance failure output = %q", stderr.String())
	}
	if got := cliMessage("es", "token_status_expired"); got != "Caducado" {
		t.Fatalf("Spanish expired-token status = %q", got)
	}
}

func TestCommandFailureUsesLocalizedGenericText(t *testing.T) {
	var stderr strings.Builder
	writeCommandFailure(&stderr, "es", "cairnbin", os.ErrPermission)
	if !strings.Contains(stderr.String(), "falló el comando") || strings.Contains(stderr.String(), "permission denied") {
		t.Fatalf("localized command failure output = %q", stderr.String())
	}
}

func TestCLIStatusMessagesUseRequestedLanguage(t *testing.T) {
	if got := cliMessage("es", "status_healthy", "project_name", "cairnbin", "port", "80"); !strings.Contains(got, "saludable") || !strings.Contains(got, "80") {
		t.Errorf("Spanish healthy status = %q", got)
	}
	if got := cliMessage("es", "status_unhealthy", "project_name", "cairnbin", "error", "offline"); !strings.Contains(got, "no saludable") || !strings.Contains(got, "offline") {
		t.Errorf("Spanish unhealthy status = %q", got)
	}
}

func TestCLIUpdateMessagesUseRequestedLanguage(t *testing.T) {
	if got := cliMessage("es", "update_help", "binary", "cairnbin"); !strings.Contains(got, "Gestión de actualizaciones") || !strings.Contains(got, "Ejemplos") {
		t.Errorf("Spanish update help = %q", got)
	}
	if got := cliMessage("es", "update_available", "old", "1.0.0", "new", "1.1.0"); !strings.Contains(got, "Actualización disponible") || !strings.Contains(got, "1.1.0") {
		t.Errorf("Spanish update status = %q", got)
	}
}

func TestCLIMaintenanceUsageMessagesUseRequestedLanguage(t *testing.T) {
	got := cliMessage("es", "maintenance_usage_restore", "binary", "cairnbin")
	if !strings.Contains(got, "Uso:") || !strings.Contains(got, "cairnbin") || !strings.Contains(got, "restore") {
		t.Fatalf("Spanish restore usage = %q", got)
	}
	got = cliMessage("es", "maintenance_argument_required", "binary", "cairnbin", "command", "data", "argument", "acción")
	if !strings.Contains(got, "requiere") || !strings.Contains(got, "acción") {
		t.Fatalf("Spanish required-argument message = %q", got)
	}
}

// TestLogSchedErr verifies the helper tolerates a nil error and logs a non-nil
// error without panicking.
func TestLogSchedErr(t *testing.T) {
	logSchedErr(nil)
	logSchedErr(context.DeadlineExceeded)
}
