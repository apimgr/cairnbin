package httputil

import (
	"context"
	"net/http"
	"strings"
)

// projectName is the CLI binary prefix used in User-Agent detection.
const projectName = "cairnbin"

// ctxKeyTxtExtension is a private context key set by the txt-extension middleware.
type ctxKeyTxtExtension struct{}

// WithTxtExtension returns a new request with the txt-extension flag set.
// Called by the server's txtExtensionMiddleware when stripping ".txt" from a path.
func WithTxtExtension(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxKeyTxtExtension{}, true))
}

// hasTxtExtension reports whether the request had a ".txt" extension that was stripped.
func hasTxtExtension(r *http.Request) bool {
	v, _ := r.Context().Value(ctxKeyTxtExtension{}).(bool)
	return v
}

// IsOurCliClient detects our own client binary (cairnbin-cli).
// Our client is INTERACTIVE (TUI/GUI) — receives JSON and renders itself.
func IsOurCliClient(r *http.Request) bool {
	ua := r.Header.Get("User-Agent")
	return strings.HasPrefix(ua, projectName+"-cli/")
}

// IsTextBrowser detects text-mode browsers (lynx, w3m, links, etc.).
// Text browsers are INTERACTIVE but do NOT support JavaScript.
// They receive no-JS HTML (server-rendered, standard form POST).
func IsTextBrowser(r *http.Request) bool {
	ua := strings.ToLower(r.Header.Get("User-Agent"))

	textBrowsers := []string{
		// Lynx — classic text browser
		"lynx/",
		// w3m — text browser with table support
		"w3m/",
		// Links — text browser (note: space after)
		"links ",
		// Links alternative format
		"links/",
		// ELinks — enhanced links
		"elinks/",
		// Browsh — modern text browser
		"browsh/",
		// Carbonyl — Chromium in terminal
		"carbonyl/",
		// NetSurf — lightweight browser (limited JS)
		"netsurf",
	}
	for _, browser := range textBrowsers {
		if strings.Contains(ua, browser) {
			return true
		}
	}
	return false
}

// IsHttpTool detects HTTP tools (curl, wget, httpie, etc.).
// HTTP tools are NON-INTERACTIVE — they just dump output.
func IsHttpTool(r *http.Request) bool {
	ua := strings.ToLower(r.Header.Get("User-Agent"))

	httpTools := []string{
		// Command-line tools.
		"curl/", "wget/", "httpie/", "xh/", "hurl/", "libcurl/",
		// Python clients.
		"python-requests/", "python-urllib/", "python-httpx/", "aiohttp/", "urllib3/",
		// Go and Rust clients.
		"go-http-client/", "go-resty/", "fasthttp", "reqwest/", "ureq/", "hyper/",
		// JavaScript runtimes and libraries.
		"axios/", "node-fetch/", "undici", "superagent/", "deno/", "bun/",
		// JVM, PHP, Perl, Ruby, Dart, and PowerShell clients.
		"okhttp/", "apache-httpclient/", "java/", "guzzlehttp/", "libwww-perl/", "lwp-trivial/", "faraday", "dart/", "windowspowershell/",
		// API clients and load tools.
		"postmanruntime/", "insomnia/", "bruno-runtime/", "k6/",
	}
	for _, tool := range httpTools {
		if strings.Contains(ua, tool) {
			return true
		}
	}

	// No User-Agent = likely HTTP tool (non-interactive).
	if ua == "" {
		return true
	}

	return false
}

// IsNonInteractiveClient reports whether the client needs pre-formatted text.
// Only HTTP tools are non-interactive.
// Our client and text browsers are INTERACTIVE (handle their own rendering).
func IsNonInteractiveClient(r *http.Request) bool {
	// Our client is INTERACTIVE — receives JSON.
	if IsOurCliClient(r) {
		return false
	}

	// Text browsers are INTERACTIVE — receive no-JS HTML, render it themselves.
	if IsTextBrowser(r) {
		return false
	}

	// HTTP tools are NON-INTERACTIVE — need pre-formatted text.
	if IsHttpTool(r) {
		return true
	}

	return false
}

// GetAPIResponseFormat determines the response format for /api/** routes.
// API routes default to JSON; plain text is returned for .txt extension,
// Accept: text/plain header, or non-interactive clients (curl, wget, etc.).
func GetAPIResponseFormat(r *http.Request) string {
	// .txt extension always wins regardless of other signals.
	// Also checks the context flag set by the txt-extension middleware, which
	// strips ".txt" from r.URL.Path before routing so chi can match routes.
	if strings.HasSuffix(r.URL.Path, ".txt") || hasTxtExtension(r) {
		return "text"
	}

	accept := r.Header.Get("Accept")

	if strings.Contains(accept, "application/json") {
		return "json"
	}
	if strings.Contains(accept, "text/plain") {
		return "text"
	}

	// Non-interactive clients (curl, wget, empty UA) get plain text.
	if IsNonInteractiveClient(r) {
		return "text"
	}

	// Default: JSON.
	return "json"
}
