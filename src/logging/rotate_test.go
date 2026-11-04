// Tests for rotation/retention policy parsing and the rotating file writer:
// ParseRotate, ParseKeep, periodKey, size-triggered rotation, keep-none
// deletion, count/age retention, and gzip compression of rotated files.
package logging

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ─── ParseRotate ─────────────────────────────────────────────────────────────

func TestParseRotate(t *testing.T) {
	tests := []struct {
		in      string
		want    RotatePolicy
		wantErr bool
	}{
		{"never", RotatePolicy{}, false},
		{"", RotatePolicy{}, false},
		{"daily", RotatePolicy{Interval: "daily"}, false},
		{"weekly", RotatePolicy{Interval: "weekly"}, false},
		{"monthly", RotatePolicy{Interval: "monthly"}, false},
		{"yearly", RotatePolicy{Interval: "yearly"}, false},
		{"50MB", RotatePolicy{MaxBytes: 50 << 20}, false},
		{"1GB", RotatePolicy{MaxBytes: 1 << 30}, false},
		{"weekly,50MB", RotatePolicy{Interval: "weekly", MaxBytes: 50 << 20}, false},
		{"50mb,daily", RotatePolicy{Interval: "daily", MaxBytes: 50 << 20}, false},
		{" Weekly , 50MB ", RotatePolicy{Interval: "weekly", MaxBytes: 50 << 20}, false},
		{"daily,weekly", RotatePolicy{}, true},
		{"50MB,1GB", RotatePolicy{}, true},
		{"never,50MB", RotatePolicy{}, true},
		{"bogus", RotatePolicy{}, true},
		{"-5MB", RotatePolicy{}, true},
		{"0MB", RotatePolicy{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseRotate(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseRotate(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ParseRotate(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

// ─── ParseKeep ───────────────────────────────────────────────────────────────

func TestParseKeep(t *testing.T) {
	tests := []struct {
		in      string
		want    KeepPolicy
		wantErr bool
	}{
		{"none", KeepPolicy{Mode: "none"}, false},
		{"", KeepPolicy{Mode: "none"}, false},
		{"forever", KeepPolicy{Mode: "forever"}, false},
		{"5", KeepPolicy{Mode: "count", Count: 5}, false},
		{"0", KeepPolicy{Mode: "count", Count: 0}, false},
		{"30d", KeepPolicy{Mode: "age", Age: 30 * 24 * time.Hour}, false},
		{"4w", KeepPolicy{Mode: "age", Age: 4 * 7 * 24 * time.Hour}, false},
		{"6m", KeepPolicy{Mode: "age", Age: 6 * 30 * 24 * time.Hour}, false},
		{"weekly:4", KeepPolicy{Mode: "count", Count: 4}, false},
		{"monthly:12", KeepPolicy{Mode: "count", Count: 12}, false},
		{"FOREVER", KeepPolicy{Mode: "forever"}, false},
		{"bogus", KeepPolicy{}, true},
		{"-3", KeepPolicy{}, true},
		{"0d", KeepPolicy{}, true},
		{"hourly:2", KeepPolicy{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseKeep(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseKeep(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ParseKeep(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

// ─── periodKey ───────────────────────────────────────────────────────────────

func TestPeriodKey(t *testing.T) {
	ts := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		interval string
		want     string
	}{
		{"daily", "2026-07-10"},
		{"weekly", "2026-W28"},
		{"monthly", "2026-07"},
		{"yearly", "2026"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := periodKey(tt.interval, ts); got != tt.want {
			t.Errorf("periodKey(%q) = %q, want %q", tt.interval, got, tt.want)
		}
	}
	// Same day, different hours share a daily period.
	if periodKey("daily", ts) != periodKey("daily", ts.Add(5*time.Hour)) {
		t.Error("same day must share a daily period key")
	}
	// Next day differs.
	if periodKey("daily", ts) == periodKey("daily", ts.Add(24*time.Hour)) {
		t.Error("next day must have a different daily period key")
	}
}

// ─── fileWriter — size rotation ──────────────────────────────────────────────

// rotatedFiles returns the rotated siblings of path (path.* entries).
func rotatedFiles(t *testing.T, path string) []string {
	t.Helper()
	entries, err := filepath.Glob(path + ".*")
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestFileWriter_SizeRotation_KeepCount(t *testing.T) {
	dir := t.TempDir()
	w := newFileWriter(dir, "app.log", 0o640,
		RotatePolicy{MaxBytes: 70}, KeepPolicy{Mode: "count", Count: 5}, false, true)
	defer w.close()

	// Each line is 33 bytes; the third write pushes past 70 and rotates first.
	line := strings.Repeat("x", 32)
	for i := 0; i < 3; i++ {
		if err := w.writeLine(line); err != nil {
			t.Fatalf("writeLine %d: %v", i, err)
		}
	}
	rotated := rotatedFiles(t, filepath.Join(dir, "app.log"))
	if len(rotated) != 1 {
		t.Fatalf("rotated files = %v, want exactly 1", rotated)
	}
	// Rotated name is app.log.YYYY-MM-DD.
	wantSuffix := "app.log." + time.Now().Format("2006-01-02")
	if filepath.Base(rotated[0]) != wantSuffix {
		t.Errorf("rotated name = %s, want %s", filepath.Base(rotated[0]), wantSuffix)
	}
	// Live file contains only the last line.
	data, err := os.ReadFile(filepath.Join(dir, "app.log"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != line+"\n" {
		t.Errorf("live file = %q, want single line", data)
	}
}

func TestFileWriter_SizeRotation_KeepNoneDeletes(t *testing.T) {
	dir := t.TempDir()
	w := newFileWriter(dir, "app.log", 0o640,
		RotatePolicy{MaxBytes: 16}, KeepPolicy{Mode: "none"}, false, true)
	defer w.close()

	for i := 0; i < 4; i++ {
		if err := w.writeLine(strings.Repeat("y", 15)); err != nil {
			t.Fatalf("writeLine %d: %v", i, err)
		}
	}
	if rotated := rotatedFiles(t, filepath.Join(dir, "app.log")); len(rotated) != 0 {
		t.Errorf("keep=none must delete rotated files immediately, found %v", rotated)
	}
	if _, err := os.Stat(filepath.Join(dir, "app.log")); err != nil {
		t.Errorf("live file must still exist: %v", err)
	}
}

func TestFileWriter_NeverRotates(t *testing.T) {
	dir := t.TempDir()
	w := newFileWriter(dir, "big.log", 0o640, RotatePolicy{}, KeepPolicy{Mode: "none"}, false, true)
	defer w.close()

	for i := 0; i < 10; i++ {
		if err := w.writeLine(strings.Repeat("z", 100)); err != nil {
			t.Fatal(err)
		}
	}
	if rotated := rotatedFiles(t, filepath.Join(dir, "big.log")); len(rotated) != 0 {
		t.Errorf("rotate=never must not rotate, found %v", rotated)
	}
}

// ─── fileWriter — retention pruning ──────────────────────────────────────────

func TestFileWriter_PruneCount(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "app.log")
	// Create 4 rotated files with staggered mtimes (oldest first).
	for i, name := range []string{"2026-01-01", "2026-01-02", "2026-01-03", "2026-01-04"} {
		p := base + "." + name
		if err := os.WriteFile(p, []byte("old\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		mt := time.Now().Add(time.Duration(i-10) * time.Hour)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	w := newFileWriter(dir, "app.log", 0o640, RotatePolicy{}, KeepPolicy{Mode: "count", Count: 2}, false, true)
	if err := w.prune(time.Now()); err != nil {
		t.Fatalf("prune: %v", err)
	}
	rotated := rotatedFiles(t, base)
	if len(rotated) != 2 {
		t.Fatalf("kept %d rotated files, want 2: %v", len(rotated), rotated)
	}
	// Newest two (01-03, 01-04 by mtime) must survive.
	for _, p := range rotated {
		b := filepath.Base(p)
		if b != "app.log.2026-01-03" && b != "app.log.2026-01-04" {
			t.Errorf("unexpected survivor %s", b)
		}
	}
}

func TestFileWriter_PruneAge(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "app.log")
	oldP := base + ".2026-01-01"
	newP := base + ".2026-07-01"
	for p, age := range map[string]time.Duration{oldP: 40 * 24 * time.Hour, newP: time.Hour} {
		if err := os.WriteFile(p, []byte("x\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		mt := time.Now().Add(-age)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	w := newFileWriter(dir, "app.log", 0o640, RotatePolicy{}, KeepPolicy{Mode: "age", Age: 30 * 24 * time.Hour}, false, true)
	if err := w.prune(time.Now()); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if _, err := os.Stat(oldP); !os.IsNotExist(err) {
		t.Error("40-day-old rotated file should have been pruned")
	}
	if _, err := os.Stat(newP); err != nil {
		t.Errorf("recent rotated file should survive: %v", err)
	}
}

func TestFileWriter_PruneForever(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "app.log")
	p := base + ".2020-01-01"
	if err := os.WriteFile(p, []byte("x\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2000 * 24 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	w := newFileWriter(dir, "app.log", 0o640, RotatePolicy{}, KeepPolicy{Mode: "forever"}, false, true)
	if err := w.prune(time.Now()); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("keep=forever must never delete: %v", err)
	}
}

// ─── fileWriter — compression ────────────────────────────────────────────────

func TestFileWriter_CompressRotated(t *testing.T) {
	dir := t.TempDir()
	w := newFileWriter(dir, "audit.log", 0o640,
		RotatePolicy{MaxBytes: 16}, KeepPolicy{Mode: "count", Count: 5}, true, false)
	defer w.close()

	content := strings.Repeat("a", 15)
	if err := w.writeLine(content); err != nil {
		t.Fatal(err)
	}
	// Second write exceeds 16 bytes → rotate + gzip.
	if err := w.writeLine("next"); err != nil {
		t.Fatal(err)
	}
	gzs, err := filepath.Glob(filepath.Join(dir, "audit.log.*.gz"))
	if err != nil || len(gzs) != 1 {
		t.Fatalf("gz files = %v (err %v), want exactly 1", gzs, err)
	}
	// Un-gzipped rotated original must be gone.
	plain := strings.TrimSuffix(gzs[0], ".gz")
	if _, err := os.Stat(plain); !os.IsNotExist(err) {
		t.Error("uncompressed rotated file should have been removed")
	}
	// Round-trip the gz content.
	f, err := os.Open(gzs[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gr.Close()
	got, err := io.ReadAll(gr)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content+"\n" {
		t.Errorf("decompressed = %q, want %q", got, content+"\n")
	}
}

// ─── fileWriter — scheduled rotateCheck ──────────────────────────────────────

func TestFileWriter_RotateCheck_SizeHit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quiet.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("q", 100)), 0o640); err != nil {
		t.Fatal(err)
	}
	w := newFileWriter(dir, "quiet.log", 0o640,
		RotatePolicy{MaxBytes: 50}, KeepPolicy{Mode: "count", Count: 3}, false, true)
	if err := w.rotateCheck(); err != nil {
		t.Fatalf("rotateCheck: %v", err)
	}
	if rotated := rotatedFiles(t, path); len(rotated) != 1 {
		t.Errorf("rotateCheck should rotate oversized file, got %v", rotated)
	}
}

func TestFileWriter_RotateCheck_PeriodBoundary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quiet.log")
	if err := os.WriteFile(path, []byte("old content\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	// mtime two days ago → previous daily period.
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	w := newFileWriter(dir, "quiet.log", 0o640,
		RotatePolicy{Interval: "daily"}, KeepPolicy{Mode: "count", Count: 3}, false, true)
	if err := w.rotateCheck(); err != nil {
		t.Fatalf("rotateCheck: %v", err)
	}
	if rotated := rotatedFiles(t, path); len(rotated) != 1 {
		t.Errorf("rotateCheck should rotate across a period boundary, got %v", rotated)
	}
}

func TestFileWriter_RotateCheck_NoFile(t *testing.T) {
	dir := t.TempDir()
	w := newFileWriter(dir, "missing.log", 0o640,
		RotatePolicy{Interval: "daily"}, KeepPolicy{Mode: "none"}, false, true)
	if err := w.rotateCheck(); err != nil {
		t.Errorf("rotateCheck on missing file should be a no-op, got %v", err)
	}
}

func TestEnforceMinimumAge(t *testing.T) {
	cases := []struct {
		keep string
		days int
		want string
	}{
		{"none", 0, "none"},
		{"none", 365, "365d"},
		{"30d", 365, "365d"},
		{"400d", 365, "400d"},
		{"forever", 2190, "forever"},
		{"5", 365, "365d"},
		{"bogus", 365, "365d"},
	}
	for _, c := range cases {
		if got := EnforceMinimumAge(c.keep, c.days); got != c.want {
			t.Errorf("EnforceMinimumAge(%q,%d)=%q want %q", c.keep, c.days, got, c.want)
		}
	}
}
