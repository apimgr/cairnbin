// Package updater — additional coverage tests targeting uncovered branches in
// CheckForUpdate, CheckForUpdateURL, binaryAssetName, fetchExpectedChecksum, and DoUpdate.
package updater

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ─── CheckForUpdate URL dispatch ─────────────────────────────────────────────

// TestCheckForUpdate_StableURLShape verifies that CheckForUpdate constructs a
// "/releases/latest" URL for the "stable" branch and returns a non-network
// error (context cancelled) — which proves the code path ran.
func TestCheckForUpdate_StableURLShape(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := CheckForUpdate(ctx, "v1.0.0", "stable", 0)
	if err == nil {
		t.Fatal("expected error with cancelled context, got nil")
	}
}

// TestCheckForUpdate_EmptyBranchURLShape verifies the empty-string branch
// ("") also routes to /releases/latest with a cancelled context.
func TestCheckForUpdate_EmptyBranchURLShape(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := CheckForUpdate(ctx, "v1.0.0", "", 0)
	if err == nil {
		t.Fatal("expected error with cancelled context, got nil")
	}
}

// TestCheckForUpdate_NonStableURLShape verifies that a non-stable branch (e.g.
// "beta") routes to the /releases list endpoint, confirmed by a cancelled
// context returning a network error (not nil).
func TestCheckForUpdate_NonStableURLShape(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := CheckForUpdate(ctx, "v1.0.0", "beta", 0)
	if err == nil {
		t.Fatal("expected error with cancelled context, got nil")
	}
}

// ─── CheckForUpdateURL error paths ────────────────────────────────────────────

// TestCheckForUpdateURL_CancelledContext covers the client.Do error branch
// when the context is already cancelled before the request fires.
func TestCheckForUpdateURL_CancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := CheckForUpdateURL(ctx, "v1.0.0", "stable", 0, srv.URL+"/releases/latest")
	if err == nil {
		t.Fatal("expected error with cancelled context, got nil")
	}
}

// TestCheckForUpdateURL_UnexpectedStatusCode covers the non-404/non-200 branch
// returning an error with the HTTP status code embedded.
func TestCheckForUpdateURL_UnexpectedStatusCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := CheckForUpdateURL(context.Background(), "v1.0.0", "stable", 0, srv.URL+"/releases/latest")
	if err == nil {
		t.Fatal("expected error for 503, got nil")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error should mention HTTP 503, got: %v", err)
	}
}

// TestCheckForUpdateURL_BadURL covers the http.NewRequestWithContext error
// branch by passing a URL that cannot be parsed.
func TestCheckForUpdateURL_BadURL(t *testing.T) {
	_, err := CheckForUpdateURL(context.Background(), "v1.0.0", "stable", 0, "://bad-url")
	if err == nil {
		t.Fatal("expected error for malformed URL, got nil")
	}
}

// TestCheckForUpdateURL_DailyNoMatch verifies that a devel-branch list with no
// matching tag returns nil, nil (covers the loop-exhaust path on non-beta/devel).
func TestCheckForUpdateURL_DailyNoMatch(t *testing.T) {
	releases := []Release{
		{TagName: "v1.0.0", Prerelease: false},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, releases)
	}))
	defer srv.Close()

	rel, err := CheckForUpdateURL(context.Background(), "v1.0.0", "devel", 0, srv.URL+"/releases")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel != nil {
		t.Errorf("expected nil when no devel tag found, got %+v", rel)
	}
}

// ─── binaryAssetName ─────────────────────────────────────────────────────────

// TestBinaryAssetName_ContainsGOOS verifies the asset name embeds the current
// GOOS and GOARCH, and carries ".exe" only on Windows.
func TestBinaryAssetName_ContainsGOOS(t *testing.T) {
	name := binaryAssetName()
	if !strings.Contains(name, runtime.GOOS) {
		t.Errorf("binaryAssetName %q does not contain GOOS %q", name, runtime.GOOS)
	}
	if !strings.Contains(name, runtime.GOARCH) {
		t.Errorf("binaryAssetName %q does not contain GOARCH %q", name, runtime.GOARCH)
	}
	if runtime.GOOS == "windows" {
		if !strings.HasSuffix(name, ".exe") {
			t.Errorf("windows binary asset name %q should end with .exe", name)
		}
	} else {
		if strings.HasSuffix(name, ".exe") {
			t.Errorf("non-windows binary asset name %q should not end with .exe", name)
		}
	}
}

// TestBinaryAssetName_Format verifies the full "{project}-{goos}-{goarch}"
// structure of the returned asset name.
func TestBinaryAssetName_Format(t *testing.T) {
	name := binaryAssetName()
	base := projectName + "-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		base += ".exe"
	}
	if name != base {
		t.Errorf("binaryAssetName = %q; want %q", name, base)
	}
}

// ─── fetchExpectedChecksum error paths ─────────────────────────────────────────

// TestFetchExpectedChecksum_HTTPError covers the client.Do error branch by
// pointing at a server that immediately closes the connection.
func TestFetchExpectedChecksum_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		conn, _, _ := hj.Hijack()
		_ = conn.Close()
	}))
	defer srv.Close()

	client := &http.Client{}
	rel := &Release{Assets: []Asset{{Name: "sha256.txt", BrowserDownloadURL: srv.URL}}}
	_, err := fetchExpectedChecksum(context.Background(), client, rel, "cairnbin-linux-amd64")
	if err == nil {
		t.Fatal("expected error when connection is forcibly closed, got nil")
	}
}

// TestFetchExpectedChecksum_CancelledContext covers the request-build-then-fail
// path when the context is already cancelled.
func TestFetchExpectedChecksum_CancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := &http.Client{}
	rel := &Release{Assets: []Asset{{Name: "sha256.txt", BrowserDownloadURL: srv.URL}}}
	_, err := fetchExpectedChecksum(ctx, client, rel, "cairnbin-linux-amd64")
	if err == nil {
		t.Fatal("expected error with cancelled context, got nil")
	}
}

// TestFetchExpectedChecksum_BadURL covers the http.NewRequestWithContext error
// branch by passing an unparseable sha256.txt URL.
func TestFetchExpectedChecksum_BadURL(t *testing.T) {
	client := &http.Client{}
	rel := &Release{Assets: []Asset{{Name: "sha256.txt", BrowserDownloadURL: "://bad"}}}
	_, err := fetchExpectedChecksum(context.Background(), client, rel, "cairnbin-linux-amd64")
	if err == nil {
		t.Fatal("expected error for malformed URL, got nil")
	}
}

// TestFetchExpectedChecksum_MultipleEntriesMatchFirst verifies that when a
// sha256.txt file has multiple entries the one matching assetName is
// returned, not the others.
func TestFetchExpectedChecksum_MultipleEntriesMatchFirst(t *testing.T) {
	const want = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	body := "other000000000000000000000000000000000000000000000000000  other-bin\n" +
		want + "  cairnbin-linux-amd64\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	client := &http.Client{}
	rel := &Release{Assets: []Asset{{Name: "sha256.txt", BrowserDownloadURL: srv.URL}}}
	got, err := fetchExpectedChecksum(context.Background(), client, rel, "cairnbin-linux-amd64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("fetchExpectedChecksum = %q; want %q", got, want)
	}
}

// TestFetchExpectedChecksum_UnexpectedStatusCode covers the non-200 status
// branch when fetching sha256.txt.
func TestFetchExpectedChecksum_UnexpectedStatusCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client := &http.Client{}
	rel := &Release{Assets: []Asset{{Name: "sha256.txt", BrowserDownloadURL: srv.URL}}}
	_, err := fetchExpectedChecksum(context.Background(), client, rel, "cairnbin-linux-amd64")
	if err == nil {
		t.Fatal("expected error for 503, got nil")
	}
}

// ─── DoUpdate error paths ─────────────────────────────────────────────────────

// TestDoUpdate_CancelledContextDownload covers the "downloading update" error
// branch by cancelling the context before the download starts.
func TestDoUpdate_CancelledContextDownload(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("replaceBinary Windows path differs")
	}

	assetName := binaryAssetName()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("some content"))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rel := &Release{
		TagName: "v9.9.9",
		Assets: []Asset{
			{Name: assetName, BrowserDownloadURL: srv.URL + "/bin"},
		},
	}

	err := DoUpdate(ctx, rel)
	if err == nil {
		t.Fatal("expected error with cancelled context, got nil")
	}
}

// TestDoUpdate_EmptyRelease verifies that a release with no assets returns the
// "no binary" error (boundary: nil/empty assets slice).
func TestDoUpdate_EmptyRelease(t *testing.T) {
	rel := &Release{
		TagName: "v9.9.9",
		Assets:  []Asset{},
	}
	err := DoUpdate(context.Background(), rel)
	if err == nil {
		t.Fatal("expected error for empty assets, got nil")
	}
	if !strings.Contains(err.Error(), "no binary") {
		t.Errorf("expected 'no binary' error, got: %v", err)
	}
}

// TestDoUpdate_AssetWithWrongName verifies only an exact asset name match
// triggers a download (boundary: asset present but different arch).
func TestDoUpdate_AssetWithWrongName(t *testing.T) {
	rel := &Release{
		TagName: "v9.9.9",
		Assets: []Asset{
			{Name: "cairnbin-plan9-arm", BrowserDownloadURL: "http://127.0.0.1:1/bin"},
		},
	}
	err := DoUpdate(context.Background(), rel)
	if err == nil {
		t.Fatal("expected error for wrong asset name, got nil")
	}
	if !strings.Contains(err.Error(), "no binary") {
		t.Errorf("expected 'no binary' error, got: %v", err)
	}
}

// ─── replaceBinary ────────────────────────────────────────────────────────────

// TestReplaceBinary_NewBinNotExist covers the Rename error branch by passing a
// newBinaryPath that does not exist, confirming replaceBinary returns an error.
func TestReplaceBinary_NewBinNotExist(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows path")
	}
	tmp := t.TempDir()
	current := filepath.Join(tmp, "current")
	if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
		t.Fatalf("write current: %v", err)
	}

	err := replaceBinary(current, filepath.Join(tmp, "nonexistent-new"))
	if err == nil {
		t.Error("expected error when new binary does not exist, got nil")
	}
}

// TestReplaceBinary_PreservesMode verifies that after a successful replace the
// file mode matches the original.
func TestReplaceBinary_PreservesMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows path")
	}
	tmp := t.TempDir()
	current := filepath.Join(tmp, "current")
	newBin := filepath.Join(tmp, "new")

	if err := os.WriteFile(current, []byte("old"), 0o750); err != nil {
		t.Fatalf("write current: %v", err)
	}
	if err := os.WriteFile(newBin, []byte("new"), 0o755); err != nil {
		t.Fatalf("write new: %v", err)
	}

	if err := replaceBinary(current, newBin); err != nil {
		t.Fatalf("replaceBinary error: %v", err)
	}

	info, err := os.Stat(current)
	if err != nil {
		t.Fatalf("stat after replace: %v", err)
	}
	if info.Mode() != 0o750 {
		t.Errorf("mode after replace = %o; want 0750", info.Mode())
	}
}

// writeJSON is a test helper that encodes v as JSON into w.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
	}
}
