package database_test

import (
	"path/filepath"
	"testing"

	"github.com/apimgr/cairnbin/src/database"
)

func TestAppSecretBackupRoundTrip(t *testing.T) {
	source, err := database.NewDatabase("sqlite", filepath.Join(t.TempDir(), "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	if _, err := source.EnsureAppSecret("installation_secret"); err != nil {
		t.Fatal(err)
	}
	exporter := source.(interface {
		ExportAppSecrets() ([]database.AppSecretRecord, error)
	})
	records, err := exporter.ExportAppSecrets()
	if err != nil || len(records) != 1 {
		t.Fatalf("ExportAppSecrets() = %d rows, %v; want one row", len(records), err)
	}
	target, err := database.NewDatabase("sqlite", filepath.Join(t.TempDir(), "target.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = target.Close() }()
	importer := target.(interface {
		ImportAppSecrets([]database.AppSecretRecord) error
	})
	if err := importer.ImportAppSecrets(records); err != nil {
		t.Fatal(err)
	}
	got, err := target.EnsureAppSecret("installation_secret")
	if err != nil {
		t.Fatal(err)
	}
	want, err := source.EnsureAppSecret("installation_secret")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("restored app secret differs from source")
	}
}
