// Package buildinfo exposes the running build's identity as a single
// cache-busting stamp. Every static asset URL, every static/HTML cache header,
// and the version-change purge cookie derive from this one value so a stale
// browser cache can never survive an update (AI.md PART 9, "Asset
// Version-Busting (REQUIRED)" and "Version-Change Purge").
package buildinfo

import (
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
)

const (
	// defaultVersion stands in for an un-stamped local build (no -ldflags).
	defaultVersion = "devel"
	// defaultCommit stands in for a build with no resolvable commit id.
	defaultCommit = "0000000"
	// shortCommitLen is the conventional git short-SHA length.
	shortCommitLen = 7
)

// stampUnsafe matches every character that is not safe to carry unescaped in a
// `?v=` query value or an ETag, so a malformed -ldflags value can never emit a
// broken URL or header.
var stampUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// current holds the resolved stamp string. atomic.Value keeps reads lock-free
// on the hot path — AssetStamp is called on every request.
var current atomic.Value

// Set records the running build's version and commit id. Called once during
// server construction, before any request is served.
func Set(version, commit string) {
	current.Store(stampFor(version, commit))
}

// AssetStamp returns `{project_version}-{short_commit}` — the value appended to
// every static asset URL as `?v=`, compared against the request's `?v=` to
// decide `immutable` vs `no-cache`, and stored in the `pastebin_build` cookie
// (AI.md PART 9, line 13321).
func AssetStamp() string {
	if s, ok := current.Load().(string); ok && s != "" {
		return s
	}
	return stampFor("", "")
}

// AssetURL joins a base prefix and a static asset path and appends the running
// build stamp as `?v=`, so every asset URL changes on every release (AI.md PART
// 9, line 13285). Shared by the server's `asset` template helper and the
// hand-rendered Swagger/GraphiQL shells so all three stamp identically.
func AssetURL(prefix, path string) string {
	if path == "" {
		return prefix
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return prefix + path + sep + "v=" + url.QueryEscape(AssetStamp())
}

// stampFor builds the stamp from raw build metadata, substituting defaults for
// empty or placeholder values so the stamp is always a usable token.
func stampFor(version, commit string) string {
	v := sanitize(version, defaultVersion)
	c := sanitize(commit, defaultCommit)
	if len(c) > shortCommitLen {
		c = c[:shortCommitLen]
	}
	return v + "-" + c
}

// sanitize strips unsafe characters and falls back when the result is empty or
// the known "unknown" placeholder the build vars default to.
func sanitize(value, fallback string) string {
	v := stampUnsafe.ReplaceAllString(strings.TrimSpace(value), "")
	if v == "" || strings.EqualFold(v, "unknown") || strings.EqualFold(v, "N/A") {
		return fallback
	}
	return v
}
