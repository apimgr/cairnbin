package domainutil

import (
	"net"
	"net/mail"
	"net/url"
	"strings"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

// NormalizeHost extracts and canonicalizes a URL host, email domain, or host
// value. IP literals are rejected because the IP blocklists cover them.
func NormalizeHost(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	if address, err := mail.ParseAddress(value); err == nil && strings.Contains(address.Address, "@") {
		value = address.Address[strings.LastIndex(address.Address, "@")+1:]
	} else if strings.Contains(value, "@") && !strings.Contains(value, "://") {
		value = value[strings.LastIndex(value, "@")+1:]
	}
	if parsed, err := url.Parse(value); err == nil && (strings.Contains(value, "://") || strings.HasPrefix(value, "//")) {
		value = parsed.Hostname()
	} else if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.TrimSuffix(strings.Trim(strings.TrimSpace(value), "[]"), ".")
	if value == "" || net.ParseIP(value) != nil || strings.ContainsAny(value, "/?#@") {
		return "", false
	}
	ascii, err := idna.Lookup.ToASCII(value)
	if err != nil {
		return "", false
	}
	ascii = strings.ToLower(strings.TrimSuffix(ascii, "."))
	if ascii == "" || len(ascii) > 253 {
		return "", false
	}
	for _, label := range strings.Split(ascii, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for _, r := range label {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
				return "", false
			}
		}
	}
	return ascii, true
}

// NormalizeEntry canonicalizes a blocklist domain or wildcard. Wildcards at
// public-suffix boundaries are invalid and must be counted as skipped.
func NormalizeEntry(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if strings.Contains(value, "://") || strings.ContainsAny(value, "/?#") {
		return "", false
	}
	wildcard := strings.HasPrefix(value, "*.")
	if wildcard {
		value = strings.TrimPrefix(value, "*.")
	}
	host, ok := NormalizeHost(value)
	if !ok {
		return "", false
	}
	if wildcard {
		suffix, _ := publicsuffix.PublicSuffix(host)
		if suffix == host {
			return "", false
		}
		return "*." + host, true
	}
	return host, true
}

// Matches reports whether host matches an exact or subdomain wildcard entry.
func Matches(entry, host string) bool {
	if strings.HasPrefix(entry, "*.") {
		suffix := strings.TrimPrefix(entry, "*")
		return strings.HasSuffix(host, suffix) && host != strings.TrimPrefix(suffix, ".")
	}
	return host == entry
}
