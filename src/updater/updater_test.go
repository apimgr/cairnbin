package updater_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/updater"
)

// ─── CheckForUpdate ───────────────────────────────────────────────────────────

func TestCheckForUpdate_StableNewerVersion(t *testing.T) {
	release := updater.Release{
		TagName: "v2.0.0",
		Assets:  []updater.Asset{{Name: "cairnbin-linux-amd64", BrowserDownloadURL: "http://example.com/bin"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(release)
	}))
	defer srv.Close()

	// Use stable branch (calls /releases/latest).
	rel, err := updater.CheckForUpdateURL(context.Background(), "v1.0.0", "stable", 0, srv.URL+"/releases/latest")
	if err != nil {
		t.Fatalf("CheckForUpdate error: %v", err)
	}
	if rel == nil {
		t.Fatal("expected a release, got nil")
	}
	if rel.TagName != "v2.0.0" {
		t.Errorf("expected tag v2.0.0, got %s", rel.TagName)
	}
}

func TestCheckForUpdate_AlreadyUpToDate(t *testing.T) {
	release := updater.Release{TagName: "v1.0.0"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(release)
	}))
	defer srv.Close()

	rel, err := updater.CheckForUpdateURL(context.Background(), "v1.0.0", "stable", 0, srv.URL+"/releases/latest")
	if err != nil {
		t.Fatalf("CheckForUpdate error: %v", err)
	}
	if rel != nil {
		t.Errorf("expected nil release (already up to date), got %+v", rel)
	}
}

func TestCheckForUpdate_404NoUpdate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	rel, err := updater.CheckForUpdateURL(context.Background(), "v1.0.0", "stable", 0, srv.URL+"/releases/latest")
	if err != nil {
		t.Fatalf("unexpected error on 404: %v", err)
	}
	if rel != nil {
		t.Errorf("expected nil release on 404, got %+v", rel)
	}
}

func TestCheckForUpdate_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := updater.CheckForUpdateURL(context.Background(), "v1.0.0", "stable", 0, srv.URL+"/releases/latest")
	if err == nil {
		t.Error("expected error for 500 response, got nil")
	}
}

func TestCheckForUpdate_BetaBranch(t *testing.T) {
	releases := []updater.Release{
		// matches beta
		{TagName: "v2.0.0-beta", Prerelease: false},
		{TagName: "v1.5.0", Prerelease: false},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(releases)
	}))
	defer srv.Close()

	rel, err := updater.CheckForUpdateURL(context.Background(), "v1.0.0", "beta", 0, srv.URL+"/releases")
	if err != nil {
		t.Fatalf("CheckForUpdate beta error: %v", err)
	}
	if rel == nil {
		t.Fatal("expected a beta release, got nil")
	}
	if rel.TagName != "v2.0.0-beta" {
		t.Errorf("expected v2.0.0-beta, got %s", rel.TagName)
	}
}

func TestCheckForUpdate_DailyBranch(t *testing.T) {
	releases := []updater.Release{
		// devel rolling tag
		{TagName: "devel", Prerelease: false, PublishedAt: time.Unix(1, 0)},
		{TagName: "v1.5.0", Prerelease: false},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(releases)
	}))
	defer srv.Close()

	rel, err := updater.CheckForUpdateURL(context.Background(), "v1.0.0", "devel", 0, srv.URL+"/releases")
	if err != nil {
		t.Fatalf("CheckForUpdate devel error: %v", err)
	}
	if rel == nil {
		t.Fatal("expected a devel release, got nil")
	}
}

func TestCheckForUpdate_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	_, err := updater.CheckForUpdateURL(context.Background(), "v1.0.0", "stable", 0, srv.URL+"/releases/latest")
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}
