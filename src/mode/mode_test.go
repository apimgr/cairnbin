package mode_test

// Tests for the mode package: ParseMode, Set/Get, IsDevelopment/IsProduction,
// Initialize (CLI flag > env > default), SetDebug/IsDebug, GetErrorDetail,
// GetCacheHeaders, GetLogLevel, and the boolean helpers.
//
// currentMode and debugEnabled are package-level globals, so every sub-test
// that mutates state must restore it via t.Cleanup to prevent cross-test
// pollution when the test binary runs sub-tests sequentially or in parallel.

import (
	"errors"
	"runtime"
	"testing"

	"github.com/apimgr/cairnbin/src/mode"
)

// resetMode restores the global mode to Production and debug to false after
// each sub-test that mutates shared state.
func resetMode(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		_ = mode.Set("production")
		mode.SetDebug(false)
	})
}

// ─── ParseMode ────────────────────────────────────────────────────────────────

// TestParseMode verifies all accepted aliases and error cases.
func TestParseMode(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    mode.Mode
		wantErr bool
	}{
		{"dev alias", "dev", mode.Development, false},
		{"development full", "development", mode.Development, false},
		{"prod alias", "prod", mode.Production, false},
		{"production full", "production", mode.Production, false},
		{"uppercase PROD", "PROD", mode.Production, false},
		{"uppercase DEV", "DEV", mode.Development, false},
		{"mixed case Dev", "Dev", mode.Development, false},
		{"leading space", " dev", mode.Development, false},
		{"trailing space", "dev ", mode.Development, false},
		{"surrounding spaces", " Dev ", mode.Development, false},
		{"debug is its own mode", "debug", mode.Debug, false},
		{"uppercase DEBUG", "DEBUG", mode.Debug, false},
		{"empty string", "", mode.Mode(""), true},
		{"invalid value", "staging", mode.Mode(""), true},
		{"whitespace only", "   ", mode.Mode(""), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mode.ParseMode(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("ParseMode(%q): expected error, got nil (result %q)", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseMode(%q): unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ParseMode(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// ─── Set ──────────────────────────────────────────────────────────────────────

// TestSet verifies that valid modes are accepted and invalid ones are rejected
// without mutating the global state on failure.
func TestSet(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"set production", "production", false},
		{"set prod alias", "prod", false},
		{"set development", "development", false},
		{"set dev alias", "dev", false},
		{"invalid mode", "canary", true},
		{"empty string", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetMode(t)
			err := mode.Set(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("Set(%q): expected error, got nil", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("Set(%q): unexpected error: %v", tc.input, err)
			}
		})
	}
}

// ─── Get ──────────────────────────────────────────────────────────────────────

// TestGet verifies that Get returns exactly what Set stored.
func TestGet(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  mode.Mode
	}{
		{"after set dev", "dev", mode.Development},
		{"after set production", "production", mode.Production},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetMode(t)
			if err := mode.Set(tc.input); err != nil {
				t.Fatalf("Set(%q): %v", tc.input, err)
			}
			if got := mode.Get(); got != tc.want {
				t.Errorf("Get() = %q, want %q", got, tc.want)
			}
		})
	}
}

// ─── IsDevelopment / IsProduction ─────────────────────────────────────────────

// TestIsDevelopment confirms the predicate tracks the active mode.
func TestIsDevelopment(t *testing.T) {
	cases := []struct {
		name string
		set  string
		want bool
	}{
		{"dev mode → true", "dev", true},
		{"debug mode → true (as development)", "debug", true},
		{"production mode → false", "production", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetMode(t)
			if err := mode.Set(tc.set); err != nil {
				t.Fatalf("Set: %v", err)
			}
			if got := mode.IsDevelopment(); got != tc.want {
				t.Errorf("IsDevelopment() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestIsProduction confirms the predicate tracks the active mode.
func TestIsProduction(t *testing.T) {
	cases := []struct {
		name string
		set  string
		want bool
	}{
		{"production mode → true", "production", true},
		{"dev mode → false", "dev", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetMode(t)
			if err := mode.Set(tc.set); err != nil {
				t.Fatalf("Set: %v", err)
			}
			if got := mode.IsProduction(); got != tc.want {
				t.Errorf("IsProduction() = %v, want %v", got, tc.want)
			}
		})
	}
}

// ─── Initialize ───────────────────────────────────────────────────────────────

// TestInitialize covers the three-level priority: CLI flag > MODE env > default production.
func TestInitialize(t *testing.T) {
	cases := []struct {
		name    string
		cliFlag string
		envVar  string
		want    mode.Mode
		wantErr bool
	}{
		// CLI flag wins regardless of env
		{"cli dev beats env prod", "dev", "production", mode.Development, false},
		{"cli prod beats env dev", "prod", "development", mode.Production, false},
		{"cli dev, no env", "dev", "", mode.Development, false},
		// Env var used when CLI is empty
		{"no cli, env dev", "", "dev", mode.Development, false},
		{"no cli, env prod", "", "production", mode.Production, false},
		// Default production when both are empty
		{"no cli, no env → default production", "", "", mode.Production, false},
		// Invalid CLI flag returns error
		{"invalid cli flag", "nightly", "", mode.Mode(""), true},
		// Invalid env var returns error
		{"invalid env var", "", "nightly", mode.Mode(""), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetMode(t)
			t.Setenv("MODE", tc.envVar)
			err := mode.Initialize(tc.cliFlag)
			if tc.wantErr {
				if err == nil {
					t.Errorf("Initialize(%q): expected error, got nil", tc.cliFlag)
				}
				return
			}
			if err != nil {
				t.Fatalf("Initialize(%q): unexpected error: %v", tc.cliFlag, err)
			}
			if got := mode.Get(); got != tc.want {
				t.Errorf("Get() = %q after Initialize, want %q", got, tc.want)
			}
		})
	}
}

// TestApplyModeAndDebug verifies the combined mode+debug precedence chain
// (PART 6, AI.md line 9068), including the case where a "debug" mode value
// (from either the CLI flag or the MODE env var) must not override an
// explicitly set DEBUG environment variable.
func TestApplyModeAndDebug(t *testing.T) {
	cases := []struct {
		name      string
		cliMode   string
		cliDebug  bool
		envMode   string
		envDebug  string
		setDebug  bool
		wantMode  mode.Mode
		wantDebug bool
	}{
		{"defaults", "", false, "", "", false, mode.Production, false},
		{"cli mode only", "development", false, "", "", false, mode.Development, false},
		{"cli mode debug defaults debug on", "debug", false, "", "", false, mode.Debug, true},
		{"cli mode debug beaten by explicit DEBUG=false", "debug", false, "", "false", true, mode.Debug, false},
		{"env MODE=debug beaten by explicit DEBUG=false", "", false, "debug", "false", true, mode.Debug, false},
		{"env MODE=debug defaults debug on when DEBUG unset", "", false, "debug", "", false, mode.Debug, true},
		{"cli --debug beats explicit DEBUG=false", "debug", true, "", "false", true, mode.Debug, true},
		{"env DEBUG=true with production mode", "production", false, "", "true", true, mode.Production, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetMode(t)
			t.Setenv("MODE", tc.envMode)
			if tc.setDebug {
				t.Setenv("DEBUG", tc.envDebug)
			}
			mode.ApplyModeAndDebug(tc.cliMode, tc.cliDebug)
			if got := mode.Get(); got != tc.wantMode {
				t.Errorf("Get() = %q, want %q", got, tc.wantMode)
			}
			if got := mode.IsDebugEnabled(); got != tc.wantDebug {
				t.Errorf("IsDebugEnabled() = %v, want %v", got, tc.wantDebug)
			}
		})
	}
}

// ─── SetDebug / IsDebug ───────────────────────────────────────────────────────

// TestSetDebugIsDebug verifies round-trip semantics for the debug flag.
func TestSetDebugIsDebug(t *testing.T) {
	cases := []struct {
		name  string
		input bool
	}{
		{"enable debug", true},
		{"disable debug", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetMode(t)
			mode.SetDebug(tc.input)
			if got := mode.IsDebug(); got != tc.input {
				t.Errorf("IsDebug() = %v after SetDebug(%v), want %v", got, tc.input, tc.input)
			}
		})
	}
}

// TestIsDebugDefaultsFalse confirms that debug is off at package initialisation.
func TestIsDebugDefaultsFalse(t *testing.T) {
	resetMode(t)
	if mode.IsDebug() {
		t.Error("IsDebug() = true before any SetDebug call, want false")
	}
}

// ─── GetErrorDetail ───────────────────────────────────────────────────────────

// TestGetErrorDetail covers nil, development, and production behaviour.
func TestGetErrorDetail(t *testing.T) {
	sentinel := errors.New("database connection refused")

	cases := []struct {
		name    string
		setMode string
		err     error
		// true → expect err.Error() verbatim; false → generic message
		wantFull bool
	}{
		{"nil error returns empty", "production", nil, false},
		{"development returns full detail", "dev", sentinel, true},
		{"production returns generic message", "production", sentinel, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetMode(t)
			if err := mode.Set(tc.setMode); err != nil {
				t.Fatalf("Set: %v", err)
			}
			got := mode.GetErrorDetail(tc.err)
			if tc.err == nil {
				if got != "" {
					t.Errorf("GetErrorDetail(nil) = %q, want empty string", got)
				}
				return
			}
			if tc.wantFull {
				if got != tc.err.Error() {
					t.Errorf("GetErrorDetail in dev: got %q, want %q", got, tc.err.Error())
				}
			} else {
				if got == tc.err.Error() {
					t.Errorf("GetErrorDetail in prod: leaked internal error %q", got)
				}
				if got == "" {
					t.Error("GetErrorDetail in prod: returned empty string, want generic message")
				}
			}
		})
	}
}

// ─── GetCacheHeaders ──────────────────────────────────────────────────────────

// TestGetCacheHeaders verifies no-cache for dev and public/immutable for prod.
func TestGetCacheHeaders(t *testing.T) {
	cases := []struct {
		name             string
		setMode          string
		wantCacheControl string
		// true → must NOT contain "public"
		wantNoCache bool
	}{
		{"development → no-cache", "dev", "no-cache, no-store, must-revalidate", true},
		{"production → public immutable", "production", "public, max-age=31536000, immutable", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetMode(t)
			if err := mode.Set(tc.setMode); err != nil {
				t.Fatalf("Set: %v", err)
			}
			h := mode.GetCacheHeaders()
			if h.CacheControl != tc.wantCacheControl {
				t.Errorf("CacheControl = %q, want %q", h.CacheControl, tc.wantCacheControl)
			}
			if tc.wantNoCache {
				if h.Pragma != "no-cache" {
					t.Errorf("Pragma = %q, want %q", h.Pragma, "no-cache")
				}
				if h.Expires != "0" {
					t.Errorf("Expires = %q, want %q", h.Expires, "0")
				}
			} else {
				if h.Pragma != "" {
					t.Errorf("Pragma = %q in production, want empty", h.Pragma)
				}
				if h.Expires != "" {
					t.Errorf("Expires = %q in production, want empty", h.Expires)
				}
			}
		})
	}
}

// ─── GetLogLevel ──────────────────────────────────────────────────────────────

// TestGetLogLevel verifies "debug" in dev and "info" in prod.
func TestGetLogLevel(t *testing.T) {
	cases := []struct {
		name    string
		setMode string
		want    string
	}{
		{"development → debug", "dev", "debug"},
		{"production → info", "production", "info"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetMode(t)
			if err := mode.Set(tc.setMode); err != nil {
				t.Fatalf("Set: %v", err)
			}
			if got := mode.GetLogLevel(); got != tc.want {
				t.Errorf("GetLogLevel() = %q, want %q", got, tc.want)
			}
		})
	}
}

// ─── Boolean helpers ──────────────────────────────────────────────────────────

// TestShouldCacheTemplates verifies caching is enabled only in production.
func TestShouldCacheTemplates(t *testing.T) {
	cases := []struct {
		name    string
		setMode string
		want    bool
	}{
		{"production → true", "production", true},
		{"development → false", "dev", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetMode(t)
			if err := mode.Set(tc.setMode); err != nil {
				t.Fatalf("Set: %v", err)
			}
			if got := mode.ShouldCacheTemplates(); got != tc.want {
				t.Errorf("ShouldCacheTemplates() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestShouldEnableAutoReload verifies auto-reload is enabled only in development.
func TestShouldEnableAutoReload(t *testing.T) {
	cases := []struct {
		name    string
		setMode string
		want    bool
	}{
		{"development → true", "dev", true},
		{"production → false", "production", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetMode(t)
			if err := mode.Set(tc.setMode); err != nil {
				t.Fatalf("Set: %v", err)
			}
			if got := mode.ShouldEnableAutoReload(); got != tc.want {
				t.Errorf("ShouldEnableAutoReload() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestShouldEnableProfiling verifies profiling is controlled by the debug flag,
// NOT by the mode. Profiling must be off in dev without --debug, and on in prod
// with --debug.
func TestShouldEnableProfiling(t *testing.T) {
	cases := []struct {
		name     string
		setMode  string
		setDebug bool
		want     bool
	}{
		{"dev without debug → false", "dev", false, false},
		{"dev with debug → true", "dev", true, true},
		{"prod without debug → false", "production", false, false},
		{"prod with debug → true", "production", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetMode(t)
			if err := mode.Set(tc.setMode); err != nil {
				t.Fatalf("Set: %v", err)
			}
			mode.SetDebug(tc.setDebug)
			if got := mode.ShouldEnableProfiling(); got != tc.want {
				t.Errorf("ShouldEnableProfiling() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestGetPanicRecoveryMode verifies "verbose" in dev and "graceful" in prod.
func TestGetPanicRecoveryMode(t *testing.T) {
	cases := []struct {
		name    string
		setMode string
		want    string
	}{
		{"development → verbose", "dev", "verbose"},
		{"production → graceful", "production", "graceful"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetMode(t)
			if err := mode.Set(tc.setMode); err != nil {
				t.Fatalf("Set: %v", err)
			}
			if got := mode.GetPanicRecoveryMode(); got != tc.want {
				t.Errorf("GetPanicRecoveryMode() = %q, want %q", got, tc.want)
			}
		})
	}
}

// ─── Mode.String / Mode.Validate ─────────────────────────────────────────────

// TestModeString verifies the string representation of each Mode constant.
func TestModeString(t *testing.T) {
	cases := []struct {
		name string
		m    mode.Mode
		want string
	}{
		{"production", mode.Production, "production"},
		{"development", mode.Development, "development"},
		{"empty", mode.Mode(""), ""},
		{"custom", mode.Mode("custom"), "custom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.String(); got != tc.want {
				t.Errorf("Mode(%q).String() = %q, want %q", string(tc.m), got, tc.want)
			}
		})
	}
}

// TestModeValidate verifies that Production, Development and Debug are valid.
func TestModeValidate(t *testing.T) {
	cases := []struct {
		name    string
		m       mode.Mode
		wantErr bool
	}{
		{"production valid", mode.Production, false},
		{"development valid", mode.Development, false},
		{"debug valid", mode.Debug, false},
		{"empty invalid", mode.Mode(""), true},
		{"arbitrary string invalid", mode.Mode("staging"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.m.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("Mode(%q).Validate() = nil, want error", string(tc.m))
			}
			if !tc.wantErr && err != nil {
				t.Errorf("Mode(%q).Validate() = %v, want nil", string(tc.m), err)
			}
		})
	}
}

// ─── ShouldShowDebugEndpoints ─────────────────────────────────────────────────

// TestShouldShowDebugEndpoints verifies the function follows the debug flag,
// not the mode — matching the PART 6 specification.
func TestShouldShowDebugEndpoints(t *testing.T) {
	cases := []struct {
		name     string
		setMode  string
		setDebug bool
		want     bool
	}{
		{"dev no debug → false", "dev", false, false},
		{"dev with debug → true", "dev", true, true},
		{"prod no debug → false", "production", false, false},
		{"prod with debug → true", "production", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetMode(t)
			if err := mode.Set(tc.setMode); err != nil {
				t.Fatalf("Set: %v", err)
			}
			mode.SetDebug(tc.setDebug)
			if got := mode.ShouldShowDebugEndpoints(); got != tc.want {
				t.Errorf("ShouldShowDebugEndpoints() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestIsDebugMode verifies IsDebugMode is true only for MODE=debug.
func TestIsDebugMode(t *testing.T) {
	for set, want := range map[string]bool{"debug": true, "dev": false, "production": false} {
		resetMode(t)
		if err := mode.Set(set); err != nil {
			t.Fatalf("Set(%q): %v", set, err)
		}
		if got := mode.IsDebugMode(); got != want {
			t.Errorf("IsDebugMode() after Set(%q) = %v, want %v", set, got, want)
		}
	}
}

// TestSetDebugTogglesMutexProfiling verifies the debug flag turns mutex
// profiling on and off (PART 6, updateAppModeProfilingSettings).
func TestSetDebugTogglesMutexProfiling(t *testing.T) {
	resetMode(t)

	mode.SetDebug(true)
	if got := runtime.SetMutexProfileFraction(-1); got != 1 {
		t.Errorf("mutex profile fraction with debug on = %d, want 1", got)
	}

	mode.SetDebug(false)
	if got := runtime.SetMutexProfileFraction(-1); got != 0 {
		t.Errorf("mutex profile fraction with debug off = %d, want 0", got)
	}
}
