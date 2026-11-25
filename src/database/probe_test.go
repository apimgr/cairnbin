package database

import (
	"context"
	"testing"
)

func TestCheckConnectionProbesReadAndWritePaths(t *testing.T) {
	db, err := NewDatabase("sqlite", t.TempDir()+"/probe.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	probe, ok := db.(ConnectionProbe)
	if !ok {
		t.Fatal("database does not expose its connection probe")
	}
	if err := probe.CheckConnection(context.Background()); err != nil {
		t.Fatalf("CheckConnection: %v", err)
	}
}
