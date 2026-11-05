package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ─── matchesBranch ────────────────────────────────────────────────────────────

func TestMatchesBranch(t *testing.T) {
	cases := []struct {
		branch string
		tag    string
		pre    bool
		want   bool
	}{
		// stable branch (default) — non-prerelease only
		{"stable", "v1.0.0", false, true},
		{"stable", "v1.0.0", true, false},
		{"", "v1.0.0", false, true},
		{"", "v1.0.0-beta", true, false},
		// beta branch — tag ends in "-beta"
		{"beta", "v1.0.0-beta", true, true},
		{"beta", "v1.0.0", true, false},
		{"beta", "v1.0.0-rc1", true, false},
		// devel branch — the single rolling "devel" tag
		{"devel", "devel", true, true},
		// a timestamp-shaped tag is not the devel tag
		{"devel", "20250115120000", true, false},
		{"devel", "v1.0.20250115", true, false},
		// beta channel must not accept the rolling devel tag
		{"beta", "devel", true, false},
		// stable branch must not accept the rolling devel tag
		{"stable", "devel", true, false},
		// unknown branch behaves like stable
		{"unknown", "v2.0.0", false, true},
		{"unknown", "v2.0.0", true, false},
		// channels are cumulative — a stable release matches every channel
		{"beta", "v1.0.0", false, true},
		{"devel", "v1.0.0", false, true},
		// a beta prerelease also matches the (less stable) devel channel
		{"devel", "v1.0.0-beta", true, true},
	}
	for _, tc := range cases {
		r := Release{TagName: tc.tag, Prerelease: tc.pre}
		got := matchesBranch(r, tc.branch)
		if got != tc.want {
			t.Errorf("matchesBranch(%q, tag=%q, pre=%v) = %v, want %v",
				tc.branch, tc.tag, tc.pre, got, tc.want)
		}
	}
}

// ─── fetchExpectedChecksum ────────────────────────────────────────────────────

func TestFetchExpectedChecksum_HashAndFilenameFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("abc123def456abc1  cairnbin-linux-amd64\n"))
	}))
	defer srv.Close()

	client := &http.Client{}
	rel := &Release{Assets: []Asset{{Name: "sha256.txt", BrowserDownloadURL: srv.URL}}}
	hash, err := fetchExpectedChecksum(context.Background(), client, rel, "cairnbin-linux-amd64")
	if err != nil {
		t.Fatalf("fetchExpectedChecksum error: %v", err)
	}
	if hash != "abc123def456abc1" {
		t.Errorf("got %q; want abc123def456abc1", hash)
	}
}

func TestFetchExpectedChecksum_NoMatch_Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("abc  other-binary\n"))
	}))
	defer srv.Close()

	client := &http.Client{}
	rel := &Release{Assets: []Asset{{Name: "sha256.txt", BrowserDownloadURL: srv.URL}}}
	_, err := fetchExpectedChecksum(context.Background(), client, rel, "cairnbin-linux-amd64")
	if err == nil {
		t.Fatal("expected error when no matching entry is found, got nil")
	}
}

func TestFetchExpectedChecksum_NoChecksumsAsset_Errors(t *testing.T) {
	client := &http.Client{}
	rel := &Release{Assets: []Asset{{Name: "cairnbin-linux-amd64", BrowserDownloadURL: "http://example.com/bin"}}}
	_, err := fetchExpectedChecksum(context.Background(), client, rel, "cairnbin-linux-amd64")
	if err == nil {
		t.Fatal("expected error when release has no sha256.txt asset, got nil")
	}
}

// ─── binaryAssetName ─────────────────────────────────────────────────────────

func TestBinaryAssetName(t *testing.T) {
	name := binaryAssetName()
	if name == "" {
		t.Error("binaryAssetName returned empty string")
	}
	// Must start with the project name.
	if len(name) < len(projectName) || name[:len(projectName)] != projectName {
		t.Errorf("binaryAssetName %q does not start with %q", name, projectName)
	}
}

// ─── replaceBinary ────────────────────────────────────────────────────────────

func TestReplaceBinary_ReplacesFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("replaceBinary on Windows uses a different codepath")
	}
	tmp := t.TempDir()
	current := filepath.Join(tmp, "current")
	newBin := filepath.Join(tmp, "new")

	if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
		t.Fatalf("write current: %v", err)
	}
	if err := os.WriteFile(newBin, []byte("new"), 0o755); err != nil {
		t.Fatalf("write new: %v", err)
	}

	if err := replaceBinary(current, newBin); err != nil {
		t.Fatalf("replaceBinary error: %v", err)
	}

	got, err := os.ReadFile(current)
	if err != nil {
		t.Fatalf("ReadFile after replace: %v", err)
	}
	if string(got) != "new" {
		t.Errorf("after replace, content = %q; want 'new'", got)
	}
	// The temp file should have been renamed away (no longer exists at original path).
	if _, err := os.Stat(newBin); err == nil {
		t.Error("temp binary should not exist after successful replace")
	}
}

func TestReplaceBinary_MissingCurrent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows path")
	}
	tmp := t.TempDir()
	newBin := filepath.Join(tmp, "new")
	if err := os.WriteFile(newBin, []byte("new"), 0o755); err != nil {
		t.Fatalf("write new: %v", err)
	}
	err := replaceBinary(filepath.Join(tmp, "nonexistent"), newBin)
	if err == nil {
		t.Error("expected error when current binary does not exist")
	}
}

// ─── DoUpdate ────────────────────────────────────────────────────────────────

func TestDoUpdate_NoBinaryForPlatform(t *testing.T) {
	rel := Release{
		TagName: "v9.9.9",
		Assets: []Asset{
			{Name: "other-binary-other-arch", BrowserDownloadURL: "http://example.com/other"},
		},
	}
	err := DoUpdate(context.Background(), &rel)
	if err == nil {
		t.Error("expected error when no binary matches current platform")
	}
}

func TestDoUpdate_DownloadsAsset(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("replaceBinary Windows path differs")
	}

	assetName := binaryAssetName()
	served := []byte("fake binary v2")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(served)
	}))
	defer srv.Close()

	rel := Release{
		TagName: "v9.9.9",
		Assets: []Asset{
			{Name: assetName, BrowserDownloadURL: srv.URL + "/bin"},
		},
	}

	// DoUpdate calls os.Executable() to find the binary to replace.
	// In test context it will attempt to replace the test binary itself, which
	// may or may not succeed depending on permissions; we only assert the error
	// is NOT about a missing/undownloaded asset.
	err := DoUpdate(context.Background(), &rel)
	if err != nil {
		if strings.Contains(err.Error(), "no binary") {
			t.Errorf("should have found asset %q: %v", assetName, err)
		}
		if strings.Contains(err.Error(), "downloading update") {
			t.Errorf("download should have succeeded: %v", err)
		}
	}
}

func TestDoUpdate_InvalidDownloadURL(t *testing.T) {
	assetName := binaryAssetName()
	rel := Release{
		TagName: "v9.9.9",
		Assets: []Asset{
			{Name: assetName, BrowserDownloadURL: "http://127.0.0.1:1/nonexistent"},
		},
	}
	err := DoUpdate(context.Background(), &rel)
	if err == nil {
		t.Error("expected error when download URL is unreachable")
	}
}

// ─── CheckForUpdate (URL dispatch) ───────────────────────────────────────────

func TestCheckForUpdate_Stable_HitsLatestEndpoint(t *testing.T) {
	latestCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/releases/latest" {
			latestCalled = true
		}
		json.NewEncoder(w).Encode(Release{TagName: "v2.0.0"})
	}))
	defer srv.Close()

	_, _ = CheckForUpdateURL(context.Background(), "v1.0.0", "stable", 0, srv.URL+"/releases/latest")
	if !latestCalled {
		t.Error("stable branch should hit /releases/latest endpoint")
	}
}

func TestCheckForUpdateURL_BetaAlreadyUpToDate(t *testing.T) {
	releases := []Release{
		{TagName: "v1.0.0-beta", Prerelease: false},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(releases)
	}))
	defer srv.Close()

	// Current version matches the only beta release → should return nil, nil.
	rel, err := CheckForUpdateURL(context.Background(), "v1.0.0-beta", "beta", 0, srv.URL+"/releases")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel != nil {
		t.Errorf("expected nil (already up to date), got %+v", rel)
	}
}

func TestCheckForUpdateURL_NonStableInvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not a json array"))
	}))
	defer srv.Close()

	_, err := CheckForUpdateURL(context.Background(), "v1.0.0", "beta", 0, srv.URL+"/releases")
	if err == nil {
		t.Error("expected error for invalid JSON release list, got nil")
	}
}

func TestDoUpdate_WithChecksumAsset(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("replaceBinary Windows path differs")
	}

	assetName := binaryAssetName()
	fakeContent := []byte("hello from fake binary")

	// We'll compute the expected sha256.
	h := sha256.New()
	h.Write(fakeContent)
	expectedHash := hex.EncodeToString(h.Sum(nil))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "sha256.txt") {
			w.Write([]byte(expectedHash + "  " + assetName + "\n"))
		} else {
			w.Write(fakeContent)
		}
	}))
	defer srv.Close()

	rel := &Release{
		TagName: "v9.9.9",
		Assets: []Asset{
			{Name: assetName, BrowserDownloadURL: srv.URL + "/bin"},
			{Name: "sha256.txt", BrowserDownloadURL: srv.URL + "/sha256.txt"},
		},
	}

	// DoUpdate may fail at replaceBinary (test binary permissions), but must NOT
	// fail at the checksum verification step.
	err := DoUpdate(context.Background(), rel)
	if err != nil {
		if strings.Contains(err.Error(), "checksum mismatch") {
			t.Errorf("checksum should match: %v", err)
		}
	}
}

func TestDoUpdate_ChecksumMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("replaceBinary Windows path differs")
	}

	assetName := binaryAssetName()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "sha256.txt") {
			// Return a wrong hash for the asset.
			w.Write([]byte("0000000000000000000000000000000000000000000000000000000000000000  " + assetName + "\n"))
		} else {
			w.Write([]byte("some binary content"))
		}
	}))
	defer srv.Close()

	rel := &Release{
		TagName: "v9.9.9",
		Assets: []Asset{
			{Name: assetName, BrowserDownloadURL: srv.URL + "/bin"},
			{Name: "sha256.txt", BrowserDownloadURL: srv.URL + "/sha256.txt"},
		},
	}

	err := DoUpdate(context.Background(), rel)
	if err == nil {
		t.Error("expected checksum mismatch error, got nil")
	} else if !strings.Contains(err.Error(), "checksum") {
		t.Errorf("expected checksum error, got: %v", err)
	}
}

// TestDoUpdate_NoChecksumRefuses verifies the fail-closed policy: a release
// that ships the binary but no sha256.txt asset must be refused rather
// than installed unverified.
func TestDoUpdate_NoChecksumRefuses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("replaceBinary Windows path differs")
	}

	assetName := binaryAssetName()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("unverified binary content"))
	}))
	defer srv.Close()

	rel := &Release{
		TagName: "v9.9.9",
		Assets: []Asset{
			{Name: assetName, BrowserDownloadURL: srv.URL + "/bin"},
		},
	}

	err := DoUpdate(context.Background(), rel)
	if err == nil {
		t.Fatal("expected refusal when no checksum is published, got nil")
	}
	if !strings.Contains(err.Error(), "sha256.txt") {
		t.Errorf("expected unverified-update refusal, got: %v", err)
	}
}

// ─── isSameVersion / isDevelTag ──────────────────────────────────────────────

// TestIsSameVersion verifies the tag comparison ignores the "v" prefix that
// release.yml omits from the stamped Version but includes in tag_name.
func TestIsSameVersion(t *testing.T) {
	cases := []struct {
		tag     string
		current string
		want    bool
	}{
		// stamped Version has no "v", tag_name does — must still match
		{"v1.0.0", "1.0.0", true},
		{"1.0.0", "v1.0.0", true},
		{"v1.0.0", "v1.0.0", true},
		{"1.0.0", "1.0.0", true},
		// genuine version differences must not match
		{"v1.0.1", "1.0.0", false},
		{"v2.0.0", "1.0.0", false},
		// a rolling tag never equals a version string
		{"devel", "1.0.0", false},
	}
	for _, tc := range cases {
		if got := isSameVersion(tc.tag, tc.current); got != tc.want {
			t.Errorf("isSameVersion(%q, %q) = %v, want %v", tc.tag, tc.current, got, tc.want)
		}
	}
}

// TestIsDailyTag verifies the rolling devel tag is recognised exactly.
func TestIsDailyTag(t *testing.T) {
	cases := map[string]bool{
		"devel":          true,
		"v1.0.0":         false,
		"20250115120000": false,
		"":               false,
	}
	for tag, want := range cases {
		if got := isDevelTag(tag); got != want {
			t.Errorf("isDevelTag(%q) = %v, want %v", tag, got, want)
		}
	}
}

// ─── CheckForUpdateURL version comparison ────────────────────────────────────

// TestCheckForUpdateURL_StablePrefixMismatchIsUpToDate is the regression test for
// the perpetual "update available" report: a binary stamped "1.0.0" against a
// release tagged "v1.0.0" is up to date, not an update.
func TestCheckForUpdateURL_StablePrefixMismatchIsUpToDate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, Release{TagName: "v1.0.0"})
	}))
	defer srv.Close()

	rel, err := CheckForUpdateURL(context.Background(), "1.0.0", "stable", 0, srv.URL+"/releases/latest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel != nil {
		t.Errorf("expected nil (up to date) for v-prefixed tag vs unprefixed version, got %+v", rel)
	}
}

// TestCheckForUpdateURL_StableGenuineUpgrade verifies a real version bump is
// still reported once the prefix normalization is applied.
func TestCheckForUpdateURL_StableGenuineUpgrade(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, Release{TagName: "v1.1.0"})
	}))
	defer srv.Close()

	rel, err := CheckForUpdateURL(context.Background(), "1.0.0", "stable", 0, srv.URL+"/releases/latest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel == nil {
		t.Fatal("expected an update for 1.0.0 -> v1.1.0")
	}
	if rel.TagName != "v1.1.0" {
		t.Errorf("TagName = %q, want v1.1.0", rel.TagName)
	}
}

// TestCheckForUpdateURL_DailyRollingTagUsesBuildEpoch verifies the rolling
// "devel" tag is resolved by publish time against the binary's build epoch,
// since the tag name itself never changes.
func TestCheckForUpdateURL_DailyRollingTagUsesBuildEpoch(t *testing.T) {
	newer := time.Unix(2_000_000_000, 0).UTC()
	releases := []Release{{TagName: "devel", Prerelease: true, PublishedAt: newer}}

	cases := []struct {
		name       string
		buildEpoch int64
		wantUpdate bool
	}{
		// binary built before the release was published — newer nightly exists
		{"build predates release", 1_000_000_000, true},
		// binary built after the nightly was published — nothing to do
		{"build postdates release", 3_000_000_000, false},
		// exactly at the publish instant is not "newer"
		{"build equals publish", newer.Unix(), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, releases)
			}))
			defer srv.Close()

			rel, err := CheckForUpdateURL(context.Background(), "1.0.0", "devel", tc.buildEpoch, srv.URL+"/releases")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotUpdate := rel != nil; gotUpdate != tc.wantUpdate {
				t.Errorf("update reported = %v, want %v (rel=%+v)", gotUpdate, tc.wantUpdate, rel)
			}
		})
	}
}

// TestCheckForUpdateURL_DailySkipsBetaAndStableTags verifies the devel channel
// does not stop at an unversioned older release: a real version ahead of the
// running one is still offered.
func TestCheckForUpdateURL_DailySkipsBetaAndStableTags(t *testing.T) {
	releases := []Release{{TagName: "v1.1.0"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, releases)
	}))
	defer srv.Close()

	rel, err := CheckForUpdateURL(context.Background(), "1.0.0", "devel", 0, srv.URL+"/releases")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel == nil || rel.TagName != "v1.1.0" {
		t.Errorf("expected the v1.1.0 upgrade, got %+v", rel)
	}
}

// ─── restartArgs ─────────────────────────────────────────────────────────────

// TestRestartArgs_StripsUpdateSelector is the regression test for the infinite
// update loop: re-execing with the original argv would replay "--update yes"
// forever and never start the server.
func TestRestartArgs_StripsUpdateSelector(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "space separated update yes",
			in:   []string{"cairnbin", "--update", "yes"},
			want: []string{"cairnbin"},
		},
		{
			name: "inline update value",
			in:   []string{"cairnbin", "--update=yes"},
			want: []string{"cairnbin"},
		},
		{
			name: "single dash update",
			in:   []string{"cairnbin", "-update", "yes"},
			want: []string{"cairnbin"},
		},
		{
			name: "update branch consumes the channel",
			in:   []string{"cairnbin", "--update", "branch", "devel"},
			want: []string{"cairnbin"},
		},
		{
			name: "bare positional action",
			in:   []string{"cairnbin", "yes"},
			want: []string{"cairnbin"},
		},
		{
			name: "bare positional check",
			in:   []string{"cairnbin", "check"},
			want: []string{"cairnbin"},
		},
		{
			name: "bare branch with channel",
			in:   []string{"cairnbin", "branch", "beta"},
			want: []string{"cairnbin"},
		},
		{
			name: "other flags are preserved",
			in:   []string{"cairnbin", "--update", "yes", "--port", "8080", "--debug"},
			want: []string{"cairnbin", "--port", "8080", "--debug"},
		},
		{
			name: "flags before the update selector survive",
			in:   []string{"cairnbin", "--config", "/etc/cairnbin", "--update", "yes"},
			want: []string{"cairnbin", "--config", "/etc/cairnbin"},
		},
		{
			name: "value that looks like an action is not stripped",
			in:   []string{"cairnbin", "--update", "yes", "--baseurl", "help"},
			want: []string{"cairnbin", "--baseurl", "help"},
		},
		{
			// Regression: the flag package accepts the single-dash inline form,
			// which must not survive the strip or the restarted process re-runs
			// the update it just installed, looping forever.
			name: "single dash inline update value",
			in:   []string{"cairnbin", "-update=yes"},
			want: []string{"cairnbin"},
		},
		{
			// Regression: --update followed by another flag must not swallow
			// that flag as its action, orphaning the flag's value as a stray
			// positional and dropping the flag entirely.
			name: "update followed by another flag keeps the flag",
			in:   []string{"cairnbin", "--update", "--port", "8080"},
			want: []string{"cairnbin", "--port", "8080"},
		},
		{
			// Regression: "--maintenance update" is a documented alias for
			// "--update yes". Left in argv it restarts the new binary back into
			// the update flow, which exits without starting the server.
			name: "maintenance update alias is stripped",
			in:   []string{"cairnbin", "--maintenance", "update"},
			want: []string{"cairnbin"},
		},
		{
			name: "maintenance update alias is stripped with other flags",
			in:   []string{"cairnbin", "--port", "8090", "--maintenance", "update"},
			want: []string{"cairnbin", "--port", "8090"},
		},
		{
			// A different maintenance operation is not an update selector and
			// must survive the restart untouched.
			name: "other maintenance operation is preserved",
			in:   []string{"cairnbin", "--maintenance", "backup"},
			want: []string{"cairnbin", "--maintenance", "backup"},
		},
		{
			name: "update branch followed by a flag keeps the flag",
			in:   []string{"cairnbin", "--update", "branch", "--port", "8080"},
			want: []string{"cairnbin", "--port", "8080"},
		},
		{
			name: "no update selector is unchanged",
			in:   []string{"cairnbin", "--port", "8080"},
			want: []string{"cairnbin", "--port", "8080"},
		},
		{
			name: "trailing update flag with no value",
			in:   []string{"cairnbin", "--update"},
			want: []string{"cairnbin"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := restartArgs(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("restartArgs(%q) = %q, want %q", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("restartArgs(%q) = %q, want %q", tc.in, got, tc.want)
				}
			}
			// The result must never still carry an update selector.
			for _, a := range got {
				if a == "--update" || a == "-update" || strings.HasPrefix(a, "--update=") {
					t.Fatalf("restartArgs(%q) still carries update selector %q: %q", tc.in, a, got)
				}
			}
		})
	}
}

// ─── NormalizeBranch / deferred checks (PART 22) ─────────────────────────────

func TestNormalizeBranch(t *testing.T) {
	cases := map[string]string{
		"": "stable", "stable": "stable", "beta": "beta", "devel": "devel",
		"daily": "devel", "rolling": "devel", "dev": "devel", "development": "devel", "DEVEL": "devel",
	}
	for in, want := range cases {
		got, ok := NormalizeBranch(in)
		if !ok || got != want {
			t.Errorf("NormalizeBranch(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if _, ok := NormalizeBranch("nightly"); ok {
		t.Error("unknown channel must not normalize")
	}
}

// TestCheckDeferred_PicksNewestEligible mirrors the spec example: running
// v1.2.2, v1.2.3 is 40 days old, v1.2.4 is 5 days old, defer_days 30 → v1.2.3.
func TestCheckDeferred_PicksNewestEligible(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	releases := []Release{
		{TagName: "v1.2.4", PublishedAt: now.Add(-5 * 24 * time.Hour)},
		{TagName: "v1.2.3", PublishedAt: now.Add(-40 * 24 * time.Hour)},
		{TagName: "v1.2.2", PublishedAt: now.Add(-60 * 24 * time.Hour)},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(releases)
	}))
	defer srv.Close()

	rel, err := checkDeferredURL(context.Background(), "v1.2.2", "stable", 30, srv.URL, now)
	if err != nil || rel == nil || rel.TagName != "v1.2.3" {
		t.Fatalf("got %+v, %v; want v1.2.3", rel, err)
	}
	// Nothing eligible and newer: running v1.2.3 with only v1.2.4 (too new).
	rel, err = checkDeferredURL(context.Background(), "v1.2.3", "stable", 30, srv.URL, now)
	if err != nil || rel != nil {
		t.Fatalf("got %+v, %v; want nil", rel, err)
	}
}

func TestFetchExpectedChecksum_UsesSha256Txt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("abc123  cairnbin-linux-amd64\n"))
	}))
	defer srv.Close()
	rel := &Release{Assets: []Asset{{Name: "sha256.txt", BrowserDownloadURL: srv.URL}}}
	got, err := fetchExpectedChecksum(context.Background(), srv.Client(), rel, "cairnbin-linux-amd64")
	if err != nil || got != "abc123" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := fetchExpectedChecksum(context.Background(), srv.Client(), &Release{}, "x"); err == nil {
		t.Error("release without sha256.txt must be refused")
	}
}
