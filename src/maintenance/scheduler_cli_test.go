package maintenance_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/database"
	"github.com/apimgr/cairnbin/src/maintenance"
	"gopkg.in/yaml.v3"
)

func TestSetSchedulerTaskEnabledWritesMaintenanceDropIn(t *testing.T) {
	configDir := t.TempDir()
	path := filepath.Join(configDir, "yaml.d", "maintenance.yml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("server:\n  scheduler:\n    timezone: UTC\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.SetSchedulerTaskEnabled(configDir, "backup_hourly", true); err != nil {
		t.Fatalf("SetSchedulerTaskEnabled: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("unmarshal updated drop-in: %v", err)
	}
	server := document["server"].(map[string]any)
	scheduler := server["scheduler"].(map[string]any)
	tasks := scheduler["tasks"].(map[string]any)
	backup := tasks["backup_hourly"].(map[string]any)
	if backup["enabled"] != true || scheduler["timezone"] != "UTC" {
		t.Fatalf("updated drop-in lost or misstated values: %#v", document)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("maintenance drop-in mode = %04o, want 0600", info.Mode().Perm())
	}
}

func TestSetSchedulerTaskEnabledRejectsUnsafeID(t *testing.T) {
	if err := maintenance.SetSchedulerTaskEnabled(t.TempDir(), "../server", false); err == nil {
		t.Fatal("unsafe scheduler task id was accepted")
	}
}

func TestSetSchedulerTaskEnabledPreservesInvalidConfig(t *testing.T) {
	configDir := t.TempDir()
	path := filepath.Join(configDir, "yaml.d", "maintenance.yml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("server: disabled\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.SetSchedulerTaskEnabled(configDir, "backup_hourly", true); err == nil {
		t.Fatal("scalar server config was silently replaced with a mapping")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("invalid config changed on failure: got %q, want %q", got, original)
	}
}

func TestPrintSchedulerTasksSortsAndShowsFailures(t *testing.T) {
	var output strings.Builder
	maintenance.PrintSchedulerTasks(&output, []database.TaskState{
		{TaskID: "zeta", LastStatus: "pending"},
		{TaskID: "alpha", LastStatus: "failed", FailCount: 2},
	})
	got := output.String()
	if strings.Index(got, "alpha") > strings.Index(got, "zeta") || !strings.Contains(got, "failed") || !strings.Contains(got, "2") {
		t.Fatalf("scheduler list is unsorted or omits failure state: %s", got)
	}
}
