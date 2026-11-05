// Package updater implements self-update logic against GitHub Releases.
package updater

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	orgName     = "apimgr"
	projectName = "cairnbin"
	apiBase     = "https://api.github.com/repos/" + orgName + "/" + projectName + "/releases"
)

// Release represents a single GitHub release.
type Release struct {
	TagName     string    `json:"tag_name"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []Asset   `json:"assets"`
}

// Asset is a downloadable file attached to a release.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// devel is the canonical name of the rolling nightly channel (PART 22).
const devel = "devel"

// NormalizeBranch maps a user-supplied channel name to its canonical form
// (stable, beta, devel). "daily", "rolling", "dev" and "development" are
// accepted aliases for devel; an empty value means stable. The second result
// is false for an unknown channel.
func NormalizeBranch(branch string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(branch)) {
	case "", "stable":
		return "stable", true
	case "beta":
		return "beta", true
	case devel, "daily", "rolling", "dev", "development":
		return devel, true
	}
	return "", false
}

// CheckForUpdate queries GitHub Releases for a newer version on the given
// branch ("stable", "beta", or "devel").  buildEpoch is the caller's embedded
// BuildEpoch and is only consulted for the rolling "devel" tag, whose tag name
// never changes while its contents are rebuilt nightly.  Returns nil, nil when
// already up to date or when no release is found.
func CheckForUpdate(ctx context.Context, currentVersion, branch string, buildEpoch int64) (*Release, error) {
	if canon, ok := NormalizeBranch(branch); ok {
		branch = canon
	}
	var apiURL string
	switch branch {
	case "stable", "":
		apiURL = configuredAPIBase() + "/latest"
	default:
		apiURL = configuredAPIBase()
	}
	return CheckForUpdateURL(ctx, currentVersion, branch, buildEpoch, apiURL)
}

// CheckForUpdateURL is the testable core of CheckForUpdate.  It queries the
// given apiURL (so tests can inject an httptest server) and otherwise behaves
// identically to CheckForUpdate.
func CheckForUpdateURL(ctx context.Context, currentVersion, branch string, buildEpoch int64, apiURL string) (*Release, error) {
	if canon, ok := NormalizeBranch(branch); ok {
		branch = canon
	}
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// no updates available
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API: HTTP %d", resp.StatusCode)
	}

	// 4 MiB cap
	lr := io.LimitReader(resp.Body, 4<<20)

	if branch == "stable" || branch == "" {
		var rel Release
		if err := json.NewDecoder(lr).Decode(&rel); err != nil {
			return nil, fmt.Errorf("decode release: %w", err)
		}
		if isSameVersion(rel.TagName, currentVersion) {
			return nil, nil
		}
		return &rel, nil
	}

	var releases []Release
	if err := json.NewDecoder(lr).Decode(&releases); err != nil {
		return nil, fmt.Errorf("decode releases: %w", err)
	}
	for _, r := range releases {
		if !matchesBranch(r, branch) {
			continue
		}
		// The devel channel is a single rolling tag: its name never changes
		// while the release is rebuilt nightly, so a newer nightly exists only
		// when the release was published after this binary was built.
		if isDevelTag(r.TagName) {
			if r.PublishedAt.Unix() > buildEpoch {
				return &r, nil
			}
			continue
		}
		if !isSameVersion(r.TagName, currentVersion) {
			return &r, nil
		}
	}
	return nil, nil
}

// isSameVersion reports whether the release tag names the version this binary
// already carries.  release.yml stamps Version from release.txt without the "v"
// prefix while publishing tag_name as "v1.0.0", so the tag is compared with and
// without that prefix rather than byte-for-byte.
func isSameVersion(tag, currentVersion string) bool {
	return tag == currentVersion || strings.TrimPrefix(tag, "v") == strings.TrimPrefix(currentVersion, "v")
}

// isDevelTag reports whether tag is the rolling devel tag, which the devel
// workflow deletes and recreates every night.
func isDevelTag(tag string) bool {
	return tag == devel
}

// flagTakesValue reports whether the named flag consumes the following word
// from argv.  A word that is a flag's value is never a positional subcommand,
// even when it happens to read "check", "yes" or "help" — "--baseurl help" is
// a legitimate invocation and its value must survive the restart.
func flagTakesValue(arg string) bool {
	name := strings.TrimLeft(arg, "-")
	if strings.Contains(name, "=") {
		// "--flag=value" carries its value inline, so nothing follows it.
		return false
	}
	switch name {
	case "port", "address", "mode", "config", "data", "log",
		"cache", "backup", "pid", "baseurl", "color", "lang",
		"shell", "service", "maintenance", "update":
		return true
	}
	return false
}

// restartArgs returns the command line to re-exec after an update install.
// The update selectors must be stripped: re-execing the original argv would
// replay "--update yes" against the freshly installed binary, re-running the
// update forever and never starting the server.  Every other flag (--config,
// --port, --debug, …) is preserved so the server restarts as it was configured.
func restartArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		// "--update=yes" carries its action inline. The flag package accepts the
		// single-dash form too, so match on the name with any leading dashes
		// trimmed — otherwise "-update=yes" survives the strip and the restarted
		// process re-runs the update it just installed, looping forever.
		if name := strings.TrimLeft(arg, "-"); strings.HasPrefix(name, "update=") {
			continue
		}
		// "--maintenance update" is a documented alias for "--update yes"
		// (src/main.go) that reaches the same install path, so it is an update
		// selector too. Left in place it restarts the new binary straight back
		// into the update flow, which finds the version it just installed and
		// exits without ever starting the server.
		if strings.TrimLeft(arg, "-") == "maintenance" &&
			i+1 < len(args) && args[i+1] == "update" {
			i++
			continue
		}
		// "--update yes" consumes the following word as its action. Only consume
		// it when it actually looks like an action: a following flag belongs to
		// the server invocation and must be preserved for flagTakesValue.
		if arg == "--update" || arg == "-update" {
			if i+1 < len(args) && args[i+1] != "" && !strings.HasPrefix(args[i+1], "-") {
				action := args[i+1]
				i++
				// "--update branch devel" consumes the channel as a second word.
				if action == "branch" && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					i++
				}
			}
			continue
		}
		// A bare "branch <channel>" positional names the channel too.
		if arg == "branch" {
			i++
			continue
		}
		// Preserve a flag and its value before considering the value for
		// stripping, so "--baseurl help" keeps both words.
		if strings.HasPrefix(arg, "-") {
			out = append(out, arg)
			if flagTakesValue(arg) && i+1 < len(args) {
				i++
				out = append(out, args[i])
			}
			continue
		}
		// A bare positional such as "cairnbin yes" also selects an action.
		if arg == "check" || arg == "yes" || arg == "help" {
			continue
		}
		out = append(out, arg)
	}
	return out
}

// DoUpdate downloads the release binary, verifies it, and replaces the
// running binary. The caller must restart the process afterwards.
func DoUpdate(ctx context.Context, release *Release) error {
	assetName := binaryAssetName()
	var downloadURL string
	for _, a := range release.Assets {
		if a.Name == assetName {
			downloadURL = a.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		return fmt.Errorf("no binary for %s/%s in release %s", runtime.GOOS, runtime.GOARCH, release.TagName)
	}

	// Download to a temp file inside the same directory as the binary so
	// os.Rename stays on the same filesystem (atomic on Unix).
	currentPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving executable: %w", err)
	}
	currentPath, err = filepath.EvalSymlinks(currentPath)
	if err != nil {
		return fmt.Errorf("resolving symlinks: %w", err)
	}

	tmpDir := filepath.Dir(currentPath)
	tmpFile, err := os.CreateTemp(tmpDir, projectName+"-update-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	// best-effort cleanup on error
	defer os.Remove(tmpPath)

	client := &http.Client{Timeout: 10 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		tmpFile.Close()
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		tmpFile.Close()
		return fmt.Errorf("downloading update: %w", err)
	}
	defer resp.Body.Close()

	// 256 MiB cap
	lr := io.LimitReader(resp.Body, 256<<20)
	if _, err := io.Copy(tmpFile, lr); err != nil {
		tmpFile.Close()
		return fmt.Errorf("writing download: %w", err)
	}
	tmpFile.Close()

	// Verify SHA256 checksum against the release's sha256.txt (MANDATORY)
	expectedHash, err := fetchExpectedChecksum(ctx, client, release, assetName)
	if err != nil {
		return fmt.Errorf("failed to fetch checksum: %w", err)
	}
	if err := verifyChecksum(tmpPath, expectedHash); err != nil {
		return err
	}

	// Set executable bit (Unix).
	if runtime.GOOS != "windows" {
		if err := os.Chmod(tmpPath, 0o755); err != nil {
			return fmt.Errorf("chmod: %w", err)
		}
	}

	return replaceBinary(currentPath, tmpPath)
}

// fetchExpectedChecksum downloads the release's sha256.txt asset and
// returns the SHA256 hash recorded for assetName. Every published release
// MUST ship a sha256.txt — refusing an unverified binary prevents
// installing a tampered or MITM-substituted download (fail closed).
func fetchExpectedChecksum(ctx context.Context, client *http.Client, release *Release, assetName string) (string, error) {
	var checksumsURL string
	for _, a := range release.Assets {
		if a.Name == "sha256.txt" {
			checksumsURL = a.BrowserDownloadURL
			break
		}
	}
	if checksumsURL == "" {
		return "", fmt.Errorf("release has no sha256.txt asset; refusing to install unverified update")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checksumsURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("checksum download failed: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", err
	}
	// Each line is "{sha256}  {filename}" (sha256sum output format).
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == assetName {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("no checksum entry for %s in sha256.txt; refusing to install unverified update", assetName)
}

// verifyChecksum computes the SHA256 of the file at path and compares it
// (constant-time) to expectedHash (hex-encoded).
func verifyChecksum(path, expectedHash string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}

	actualHash := hex.EncodeToString(h.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(actualHash), []byte(expectedHash)) != 1 {
		return fmt.Errorf("checksum mismatch: downloaded binary failed verification")
	}
	return nil
}

// binaryAssetName returns the expected GitHub release asset name for the
// current platform (e.g. "cairnbin-linux-amd64").
func binaryAssetName() string {
	name := projectName + "-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// matchesBranch implements cumulative channels: each channel also accepts
// every release from all more-stable channels.
func matchesBranch(r Release, branch string) bool {
	// stable releases match every channel
	if !r.Prerelease {
		return true
	}
	isBeta := strings.HasSuffix(r.TagName, "-beta")
	// The devel channel is a single rolling release: tag "devel", rebuilt nightly.
	isDevel := isDevelTag(r.TagName)
	switch branch {
	case "beta":
		return isBeta
	case devel:
		return isBeta || isDevel
	default:
		return false
	}
}


// CheckForUpdateDeferred is the scheduled-task variant of CheckForUpdate
// (PART 22 "Defer Semantics"): it selects the newest release on the channel
// that is newer than the running version AND at least deferDays old
// (published_at, UTC). The devel channel ignores deferDays because its rolling
// release is recreated nightly. Manual --update check/yes never use this.
func CheckForUpdateDeferred(ctx context.Context, currentVersion, branch string, buildEpoch int64, deferDays int) (*Release, error) {
	if deferDays <= 0 {
		return CheckForUpdate(ctx, currentVersion, branch, buildEpoch)
	}
	canon, ok := NormalizeBranch(branch)
	if !ok {
		canon = "stable"
	}
	if canon == devel {
		return CheckForUpdate(ctx, currentVersion, canon, buildEpoch)
	}
	return checkDeferredURL(ctx, currentVersion, canon, deferDays, configuredAPIBase(), time.Now())
}

// checkDeferredURL lists releases from apiURL (newest first) and returns the
// newest eligible one newer than currentVersion. Scanning stops at the running
// version, since everything after it is older.
func checkDeferredURL(ctx context.Context, currentVersion, branch string, deferDays int, apiURL string, now time.Time) (*Release, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API: HTTP %d", resp.StatusCode)
	}
	var releases []Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&releases); err != nil {
		return nil, fmt.Errorf("decode releases: %w", err)
	}
	minAge := time.Duration(deferDays) * 24 * time.Hour
	for _, r := range releases {
		if isSameVersion(r.TagName, currentVersion) {
			return nil, nil
		}
		if !matchesBranch(r, branch) || isDevelTag(r.TagName) {
			continue
		}
		if r.PublishedAt.IsZero() || now.Sub(r.PublishedAt) < minAge {
			continue
		}
		return &r, nil
	}
	return nil, nil
}
