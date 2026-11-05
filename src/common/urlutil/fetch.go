// Package urlutil provides SSRF-safe remote resource fetching (PART 16 "Remote
// URL Fetching") used for operator-configured branding images (logo, favicon,
// OG image) that reference a remote https:// URL instead of a local file path.
package urlutil

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// projectName is the hardcoded internal project name used in the User-Agent
// header regardless of binary rename (PART 7/8).
const projectName = "cairnbin"

// FetchRemoteImageConfig controls remote image fetch validation and limits.
type FetchRemoteImageConfig struct {
	// MaxSize is the maximum allowed response body size in bytes.
	MaxSize int64
	// Timeout bounds the entire request/response cycle.
	Timeout time.Duration
	// AllowedTypes is the Content-Type prefix allowlist.
	AllowedTypes []string
	// AllowedSchemes is the URL scheme allowlist (https only by default).
	AllowedSchemes []string
}

// DefaultFetchRemoteImageConfig returns the PART 16 defaults: 10MB max size,
// 30s timeout, common raster image types, https-only.
func DefaultFetchRemoteImageConfig() FetchRemoteImageConfig {
	return FetchRemoteImageConfig{
		MaxSize: 10 * 1024 * 1024,
		Timeout: 30 * time.Second,
		AllowedTypes: []string{
			"image/png", "image/jpeg", "image/gif", "image/webp", "image/x-icon",
		},
		AllowedSchemes: []string{"https"},
	}
}

// ValidateRemoteURL rejects URLs that are not safe to fetch server-side:
// disallowed scheme, private/loopback/link-local resolved IPs, localhost, and
// internal-looking hostnames (.local/.internal). Called both before the
// initial request and on every redirect hop.
func ValidateRemoteURL(rawURL string, cfg FetchRemoteImageConfig) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	schemeAllowed := false
	for _, s := range cfg.AllowedSchemes {
		if strings.EqualFold(u.Scheme, s) {
			schemeAllowed = true
			break
		}
	}
	if !schemeAllowed {
		return fmt.Errorf("scheme not allowed: %s (allowed: %v)", u.Scheme, cfg.AllowedSchemes)
	}

	if err := validateNotPrivateIP(u.Hostname()); err != nil {
		return err
	}

	hostname := strings.ToLower(u.Hostname())
	if hostname == "localhost" || hostname == "127.0.0.1" || hostname == "::1" {
		return fmt.Errorf("localhost URLs not allowed")
	}
	if strings.HasSuffix(hostname, ".local") || strings.HasSuffix(hostname, ".internal") {
		return fmt.Errorf("internal hostnames not allowed")
	}

	return nil
}

// validateNotPrivateIP resolves hostname and rejects it if any resolved
// address is private, loopback, or link-local (RFC 1918/4193/3927 etc.),
// preventing DNS-rebinding SSRF against internal services.
func validateNotPrivateIP(hostname string) error {
	if hostname == "" {
		return fmt.Errorf("missing host")
	}
	ips, err := net.LookupIP(hostname)
	if err != nil {
		return fmt.Errorf("DNS lookup failed: %w", err)
	}
	for _, ip := range ips {
		if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("private/local IP not allowed: %s resolves to %s", hostname, ip)
		}
	}
	return nil
}

// FetchRemoteImage downloads rawURL under the SSRF, size, type, redirect, and
// timeout constraints in cfg. It returns the raw body bytes and the response
// Content-Type. Callers must treat any error as "fetch failed" and fall back
// to the embedded default image per PART 16 — never leave the field blank.
func FetchRemoteImage(ctx context.Context, rawURL string, cfg FetchRemoteImageConfig) ([]byte, string, error) {
	if err := ValidateRemoteURL(rawURL, cfg); err != nil {
		return nil, "", fmt.Errorf("URL validation failed: %w", err)
	}

	client := &http.Client{
		Timeout: cfg.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if err := ValidateRemoteURL(req.URL.String(), cfg); err != nil {
				return fmt.Errorf("redirect blocked: %w", err)
			}
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", fmt.Sprintf("%s-branding/1.0", projectName))
	req.Header.Set("Accept", strings.Join(cfg.AllowedTypes, ", "))

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("fetching URL: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	typeAllowed := false
	for _, t := range cfg.AllowedTypes {
		if strings.HasPrefix(contentType, t) {
			typeAllowed = true
			break
		}
	}
	if !typeAllowed {
		return nil, "", fmt.Errorf("content type not allowed: %s", contentType)
	}

	limitedReader := io.LimitReader(resp.Body, cfg.MaxSize+1)
	data, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, "", fmt.Errorf("reading response: %w", err)
	}
	if int64(len(data)) > cfg.MaxSize {
		return nil, "", fmt.Errorf("file too large (max: %d bytes)", cfg.MaxSize)
	}

	return data, contentType, nil
}
