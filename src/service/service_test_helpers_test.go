package service

import (
	"os"
	"path/filepath"
	"testing"
)

func setupFakeRunit(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	svPath := filepath.Join(binDir, "sv")
	if err := os.WriteFile(svPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake sv command: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
