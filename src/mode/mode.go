package mode

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/apimgr/pastebin/src/config"
)

// Mode represents the application execution mode
type Mode string

const (
	// Production mode - optimized for performance and security
	Production Mode = "production"
	// Development mode - optimized for debugging and development
	Development Mode = "development"
	// Debug mode - explicit opt-in only; development behavior with the debug
	// flag defaulting to on and minimal output sanitization (PART 6).
	Debug Mode = "debug"
)

var (
	blockProfileRate     = 1
	mutexProfileFraction = 1
	// currentMode stores the active application mode
	currentMode Mode = Production
	// debugEnabled tracks whether --debug / DEBUG=true was set (PART 6).
	debugEnabled bool
	// mu protects concurrent access to currentMode and debugEnabled
	mu sync.RWMutex
)

// Get returns the current application mode
func Get() Mode {
	mu.RLock()
	defer mu.RUnlock()
	return currentMode
}

// Set sets the application mode
// Valid values: "production", "prod", "development", "dev", "devel", "debug"
// "debug" selects debug mode, which defaults the debug flag to on; an explicit
// --debug flag or DEBUG env var applied after this call still wins.
func Set(mode string) error {
	parsed, err := ParseMode(mode)
	if err != nil {
		return err
	}

	mu.Lock()
	currentMode = parsed
	mu.Unlock()

	if strings.EqualFold(strings.TrimSpace(mode), "debug") {
		SetDebugEnabled(true)
	}
	return nil
}

// SetAppMode is the spec-required name for setting the application mode (PART 6, AI.md line 9009).
// Invalid mode values are silently ignored; use Set() when error handling is needed.
func SetAppMode(m string) {
	_ = Set(m)
}

// ParseMode parses a mode string into a Mode constant
// Accepts: "dev", "devel", "development", "prod", "production" (case-insensitive)
func ParseMode(s string) (Mode, error) {
	normalized := strings.ToLower(strings.TrimSpace(s))

	switch normalized {
	case "development", "dev", "devel":
		return Development, nil
	case "debug":
		// Debug mode: explicit opt-in only; defaults the debug flag on (an
		// explicit --debug flag or DEBUG env var applied after this wins).
		return Debug, nil
	case "production", "prod":
		return Production, nil
	default:
		return "", fmt.Errorf("invalid mode: %q (expected: production, prod, development, dev, devel, debug, or production)", s)
	}
}

// IsDevelopment returns true if the current mode applies development
// behavior: Development, and Debug (which is "as Development Mode" per PART 6).
func IsDevelopment() bool {
	m := Get()
	return m == Development || m == Debug
}

// IsDebugMode returns true only when the mode itself is "debug" (MODE=debug).
func IsDebugMode() bool {
	return Get() == Debug
}

// IsProduction returns true if the current mode is Production
func IsProduction() bool {
	return Get() == Production
}

// Initialize sets the mode based on priority order:
// 1. CLI flag (passed as parameter)
// 2. MODE environment variable
// 3. Default: production
func Initialize(cliMode string) error {
	// Priority 1: CLI flag
	if cliMode != "" {
		return Set(cliMode)
	}

	// Priority 2: Environment variable
	if envMode := os.Getenv("MODE"); envMode != "" {
		return Set(envMode)
	}

	// Priority 3: Default (already set to Production)
	return nil
}

// FromEnv reads the MODE and DEBUG environment variables and applies them
// (PART 6, AI.md line 9068). Priority: CLI flag overrides already applied
// before this call take precedence. Setting the MODE env var to "debug"
// selects debug mode with the debug flag defaulting on, but an explicitly set
// DEBUG env var (truthy or falsy) always wins over that default, so a falsy
// DEBUG still runs debug mode with the debug flag off. The --debug CLI flag (applied
// after this) wins over both.
func FromEnv() {
	if envMode := os.Getenv("MODE"); envMode != "" {
		SetAppMode(envMode)
	}
	// LookupEnv distinguishes "explicitly set" from "unset": an unset DEBUG
	// leaves the MODE=debug alias result alone; a set DEBUG overrides it.
	if v, set := os.LookupEnv("DEBUG"); set {
		SetDebugEnabled(config.IsTruthy(v))
	}
}

// ApplyModeAndDebug resolves and applies the full mode + debug precedence
// chain in one call (PART 6, AI.md line 9068). Mode resolution order: the
// --mode CLI flag, then the MODE environment variable, then the
// "production" default. Debug resolution order: the --debug CLI flag, then
// the DEBUG environment variable, then the debug-on alias triggered by a
// "debug" mode value coming from either the CLI flag or the environment,
// then the false default. An explicitly set DEBUG environment value beats
// the debug-on alias regardless of which source produced the "debug" mode
// value; the --debug CLI flag beats everything, including an explicit
// falsy DEBUG.
func ApplyModeAndDebug(cliMode string, cliDebug bool) {
	_, debugEnvSet := os.LookupEnv("DEBUG")

	if cliMode != "" {
		if err := Set(cliMode); err != nil {
			FromEnv()
		}
	} else {
		FromEnv()
	}

	// A "--mode debug" CLI flag re-triggers the debug-on alias unconditionally
	// through Set(); re-apply an explicitly set DEBUG value so it still wins.
	if debugEnvSet {
		SetDebugEnabled(config.IsTruthy(os.Getenv("DEBUG")))
	}

	if cliDebug {
		SetDebugEnabled(true)
	}
}

// SetDebug enables or disables debug mode (--debug flag / DEBUG env var).
// Debug enables pprof, /debug/* endpoints, and full error detail regardless of mode.
func SetDebug(enabled bool) {
	mu.Lock()
	defer mu.Unlock()
	debugEnabled = enabled
	updateProfilingSettings(enabled)
}

// updateProfilingSettings enables block and mutex profiling when debug is on
// and disables them when it is off, so /debug/pprof/block and /mutex report
// data only while debugging (PART 6).
func updateProfilingSettings(enabled bool) {
	if enabled {
		runtime.SetBlockProfileRate(blockProfileRate)
		runtime.SetMutexProfileFraction(mutexProfileFraction)
		return
	}
	runtime.SetBlockProfileRate(0)
	runtime.SetMutexProfileFraction(0)
}

// SetProfileRates sets the block profiling rate and mutex profiling fraction
// used while debug is on (server.debug.block_profile_rate and
// server.debug.mutex_profile_fraction); 0 disables that profile.
func SetProfileRates(block, mutex int) {
	mu.Lock()
	defer mu.Unlock()
	blockProfileRate = block
	mutexProfileFraction = mutex
	updateProfilingSettings(debugEnabled)
}

// SetDebugEnabled is the spec-required alias for SetDebug (PART 6, AI.md line 9020).
func SetDebugEnabled(enabled bool) {
	SetDebug(enabled)
}

// IsDebug returns true when debug mode is active (--debug was passed or DEBUG env truthy).
func IsDebug() bool {
	mu.RLock()
	defer mu.RUnlock()
	return debugEnabled
}

// IsDebugEnabled is the spec-required alias for IsDebug (PART 6, AI.md line 9053).
func IsDebugEnabled() bool {
	return IsDebug()
}

// GetErrorDetail returns error details based on the current mode
// In development mode: returns full error details with stack traces
// In production mode: returns generic error message without internal details
func GetErrorDetail(err error) string {
	if err == nil {
		return ""
	}

	if IsDevelopment() {
		// Development mode: return full error details
		return err.Error()
	}

	// Production mode: return generic error message
	return "An internal error occurred. Please contact support if the problem persists."
}

// ShouldShowDebugEndpoints returns true when debug endpoints (/debug/pprof/*,
// /debug/vars) should be registered. Enabled only when --debug / DEBUG=true is
// set — NOT simply because the mode is development (PART 6).
func ShouldShowDebugEndpoints() bool {
	return IsDebug()
}

// CacheHeaders represents HTTP cache control headers
type CacheHeaders struct {
	CacheControl string
	Pragma       string
	Expires      string
}

// GetCacheHeaders returns appropriate cache headers based on the current mode
// Development mode: no-cache headers to prevent caching
// Production mode: aggressive caching headers for static files
func GetCacheHeaders() CacheHeaders {
	if IsDevelopment() {
		// Development mode: disable caching
		return CacheHeaders{
			CacheControl: "no-cache, no-store, must-revalidate",
			Pragma:       "no-cache",
			Expires:      "0",
		}
	}

	// Production mode: enable caching (1 year for static assets)
	return CacheHeaders{
		CacheControl: "public, max-age=31536000, immutable",
		Pragma:       "",
		Expires:      "",
	}
}

// GetLogLevel returns the recommended log level for the current mode
func GetLogLevel() string {
	if IsDevelopment() {
		return "debug"
	}
	return "info"
}

// ShouldCacheTemplates returns true if templates should be cached
func ShouldCacheTemplates() bool {
	return IsProduction()
}

// ShouldEnableAutoReload returns true if auto-reload should be enabled
func ShouldEnableAutoReload() bool {
	return IsDevelopment()
}

// ShouldEnableProfiling returns true if pprof profiling endpoints should be enabled.
// Requires --debug flag — not just development mode (PART 6).
func ShouldEnableProfiling() bool {
	return IsDebug()
}

// GetPanicRecoveryMode returns the panic recovery behavior for the current mode
// Returns "verbose" for development, "graceful" for production
func GetPanicRecoveryMode() string {
	if IsDevelopment() {
		return "verbose"
	}
	return "graceful"
}

// String returns the string representation of the Mode
func (m Mode) String() string {
	return string(m)
}

// Validate returns an error if the mode is not valid
func (m Mode) Validate() error {
	switch m {
	case Production, Development, Debug:
		return nil
	default:
		return errors.New("invalid mode")
	}
}
