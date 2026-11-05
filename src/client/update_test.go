package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadAndApplyUpdateVerifiesReplacesAndReexecutes(t *testing.T) {
	newBinary := []byte("updated cairnbin cli")
	sum := sha256.Sum256(newBinary)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(newBinary)
	}))
	defer server.Close()

	executable := filepath.Join(t.TempDir(), "cairnbin-cli")
	if err := os.WriteFile(executable, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	reexecuted := false
	c := &client{}
	err := c.downloadAndApplyUpdateAt(server.URL, fmt.Sprintf("%x", sum), executable, func(path string) error {
		reexecuted = true
		if path != executable {
			t.Errorf("re-exec path = %q, want %q", path, executable)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("downloadAndApplyUpdateAt: %v", err)
	}
	if !reexecuted {
		t.Fatal("updated binary was not re-executed")
	}
	got, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(newBinary) {
		t.Fatalf("installed binary = %q, want %q", got, newBinary)
	}
}

func TestDownloadAndApplyUpdateRejectsBadChecksumBeforeReplacement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("unverified binary"))
	}))
	defer server.Close()

	executable := filepath.Join(t.TempDir(), "cairnbin-cli")
	if err := os.WriteFile(executable, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	reexecuted := false
	c := &client{}
	err := c.downloadAndApplyUpdateAt(server.URL, "invalid-checksum", executable, func(string) error {
		reexecuted = true
		return nil
	})
	if err == nil {
		t.Fatal("downloadAndApplyUpdateAt succeeded with a bad checksum")
	}
	if reexecuted {
		t.Fatal("bad-checksum binary was re-executed")
	}
	got, readErr := os.ReadFile(executable)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "old binary" {
		t.Fatalf("binary changed after checksum failure: %q", got)
	}
}
