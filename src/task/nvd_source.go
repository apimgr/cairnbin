//go:build !e2e

package task

import "net/url"

// validNVDSource only allows encrypted NVD API sources in production.
func validNVDSource(parsed *url.URL) bool {
	return parsed != nil && parsed.Scheme == "https" && parsed.Host != ""
}
