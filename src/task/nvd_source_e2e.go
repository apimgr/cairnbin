//go:build e2e

package task

import "net/url"

// validNVDSource permits the private HTTP integration double used by the E2E
// stack while keeping every other configured source HTTPS-only.
func validNVDSource(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	if parsed.Scheme == "https" && parsed.Host != "" {
		return true
	}
	return parsed.Scheme == "http" && parsed.Hostname() == "cairnbin-e2e-runner" && parsed.Port() == "64980" && parsed.Path == "/cve"
}
