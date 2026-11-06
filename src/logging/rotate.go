// Rotation and retention policy parsing plus the rotating file writer used by
// every log file the server owns (AI.md "Logging" > Rotation Options /
// Retention Options). Policies come from server.logs.{type}.rotate and .keep.
package logging

import (
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RotatePolicy is a parsed server.logs.*.rotate value. A zero policy (empty
// Interval, zero MaxBytes) means "never rotate".
type RotatePolicy struct {
	// Interval is one of "", "daily", "weekly", "monthly", "yearly".
	Interval string
	// MaxBytes rotates the file when it would exceed this size (0 = no limit).
	MaxBytes int64
}

// KeepPolicy is a parsed server.logs.*.keep value.
type KeepPolicy struct {
	// Mode is one of "none", "count", "age", "forever".
	Mode string
	// Count is the number of rotated files to retain (Mode "count").
	Count int
	// Age is the maximum age of rotated files (Mode "age").
	Age time.Duration
}

// ParseRotate parses a rotation policy string: "never", "daily", "weekly",
// "monthly", "yearly", "NMB", "NGB", or a comma-combined time+size form such
// as "weekly,50MB" (whichever triggers first wins).
func ParseRotate(s string) (RotatePolicy, error) {
	var p RotatePolicy
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "never" {
		return p, nil
	}
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		switch tok {
		case "daily", "weekly", "monthly", "yearly":
			if p.Interval != "" {
				return RotatePolicy{}, fmt.Errorf("rotate %q: multiple time intervals", s)
			}
			p.Interval = tok
		case "never", "":
			return RotatePolicy{}, fmt.Errorf("rotate %q: %q cannot be combined", s, tok)
		default:
			n, unit, err := parseSize(tok)
			if err != nil {
				return RotatePolicy{}, fmt.Errorf("rotate %q: %w", s, err)
			}
			if p.MaxBytes != 0 {
				return RotatePolicy{}, fmt.Errorf("rotate %q: multiple size limits", s)
			}
			p.MaxBytes = n * unit
		}
	}
	return p, nil
}

// parseSize parses "50MB" or "1GB" into a count and a unit multiplier.
func parseSize(tok string) (int64, int64, error) {
	var unit int64
	var num string
	switch {
	case strings.HasSuffix(tok, "mb"):
		unit = 1 << 20
		num = strings.TrimSuffix(tok, "mb")
	case strings.HasSuffix(tok, "gb"):
		unit = 1 << 30
		num = strings.TrimSuffix(tok, "gb")
	default:
		return 0, 0, fmt.Errorf("unknown rotate token %q", tok)
	}
	n, err := strconv.ParseInt(strings.TrimSpace(num), 10, 64)
	if err != nil || n <= 0 {
		return 0, 0, fmt.Errorf("invalid size %q", tok)
	}
	return n, unit, nil
}

// ParseKeep parses a retention policy string: "none" (or empty), "N" (count),
// "Nd"/"Nw"/"Nm" (age in days/weeks/months), "forever", or the optional
// "weekly:N"/"monthly:N" count forms (AI.md "Log Rotation" > Rules).
func ParseKeep(s string) (KeepPolicy, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "", "none":
		return KeepPolicy{Mode: "none"}, nil
	case "forever":
		return KeepPolicy{Mode: "forever"}, nil
	}
	// Optional "weekly:N" / "monthly:N" / "daily:N" forms — count-based.
	if i := strings.IndexByte(s, ':'); i > 0 {
		n, err := strconv.Atoi(s[i+1:])
		if err != nil || n < 0 {
			return KeepPolicy{}, fmt.Errorf("keep %q: invalid count", s)
		}
		switch s[:i] {
		case "daily", "weekly", "monthly", "yearly":
			return KeepPolicy{Mode: "count", Count: n}, nil
		}
		return KeepPolicy{}, fmt.Errorf("keep %q: unknown prefix", s)
	}
	// Age forms: Nd / Nw / Nm.
	if len(s) > 1 {
		var mult time.Duration
		switch s[len(s)-1] {
		case 'd':
			mult = 24 * time.Hour
		case 'w':
			mult = 7 * 24 * time.Hour
		case 'm':
			mult = 30 * 24 * time.Hour
		}
		if mult != 0 {
			n, err := strconv.Atoi(s[:len(s)-1])
			if err != nil || n <= 0 {
				return KeepPolicy{}, fmt.Errorf("keep %q: invalid age", s)
			}
			return KeepPolicy{Mode: "age", Age: time.Duration(n) * mult}, nil
		}
	}
	// Plain count form: N.
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return KeepPolicy{}, fmt.Errorf("keep %q: not a valid retention policy", s)
	}
	return KeepPolicy{Mode: "count", Count: n}, nil
}

// periodKey returns a string identifying the rotation period t falls into for
// the given interval. Two times share a period iff their keys are equal.
func periodKey(interval string, t time.Time) string {
	switch interval {
	case "daily":
		return t.Format("2006-01-02")
	case "weekly":
		y, w := t.ISOWeek()
		return fmt.Sprintf("%04d-W%02d", y, w)
	case "monthly":
		return t.Format("2006-01")
	case "yearly":
		return t.Format("2006")
	}
	return ""
}

// fileWriter is a thread-safe rotating append writer for one log file. It owns
// the open file handle and applies the rotate/keep policy on every write and
// on scheduled RotateCheck calls.
type fileWriter struct {
	mu       sync.Mutex
	path     string
	perm     os.FileMode
	rotate   RotatePolicy
	keep     KeepPolicy
	compress bool
	// holdOpen keeps the file descriptor open between writes. Writers whose
	// files are also written by other packages (audit.log) set this false so
	// rotation renames never orphan another writer's handle.
	holdOpen bool
	file     *os.File
	size     int64
	// periodStart anchors time-based rotation: the mtime the file had when it
	// was opened (existing file) or the time of the first write (new file).
	periodStart time.Time
}

// newFileWriter builds a writer for dir/filename with the given policies.
func newFileWriter(dir, filename string, perm os.FileMode, rotate RotatePolicy, keep KeepPolicy, compress, holdOpen bool) *fileWriter {
	return &fileWriter{
		path:     filepath.Join(dir, filename),
		perm:     perm,
		rotate:   rotate,
		keep:     keep,
		compress: compress,
		holdOpen: holdOpen,
	}
}

// open ensures the file handle is ready and the size/period state is loaded.
// Callers must hold w.mu.
func (w *fileWriter) open() error {
	if w.file != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(w.path), 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, w.perm)
	if err != nil {
		return err
	}
	w.file = f
	w.size = 0
	w.periodStart = time.Now()
	if st, err := f.Stat(); err == nil {
		w.size = st.Size()
		if w.size > 0 {
			w.periodStart = st.ModTime()
		}
	}
	return nil
}

// closeLocked closes the handle if open. Callers must hold w.mu.
func (w *fileWriter) closeLocked() {
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			log.Printf("log rotation: close failed: %v", err)
		}
		w.file = nil
	}
}

// reopen closes the current handle and reopens the log path from scratch. This
// is the SIGUSR1 primitive (PART 8): after an external tool (logrotate) has
// renamed the live file, reopening recreates it so subsequent writes land in a
// fresh file rather than the moved inode.
func (w *fileWriter) reopen() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closeLocked()
	if !w.holdOpen {
		// Non-held writers open lazily on the next write; nothing to do now.
		return nil
	}
	return w.open()
}

// needsRotate reports whether the file must rotate before writing n more bytes.
// Callers must hold w.mu with the file open.
func (w *fileWriter) needsRotate(n int64, now time.Time) bool {
	if w.size == 0 {
		return false
	}
	if w.rotate.MaxBytes > 0 && w.size+n > w.rotate.MaxBytes {
		return true
	}
	if w.rotate.Interval != "" &&
		periodKey(w.rotate.Interval, w.periodStart) != periodKey(w.rotate.Interval, now) {
		return true
	}
	return false
}

// doRotate renames the current file to a timestamped name, applies the keep
// policy, and leaves the writer ready to reopen a fresh file. Callers must
// hold w.mu.
func (w *fileWriter) doRotate(now time.Time) error {
	w.closeLocked()
	rotated := w.path + "." + now.Format("2006-01-02")
	if _, err := os.Stat(rotated); err == nil {
		rotated = w.path + "." + now.Format("2006-01-02_150405")
	}
	if err := os.Rename(w.path, rotated); err != nil {
		return err
	}
	w.size = 0
	w.periodStart = now

	// keep none: delete the rotated file immediately (spec default).
	if w.keep.Mode == "none" {
		return os.Remove(rotated)
	}
	if w.compress {
		if err := gzipAndRemove(rotated, w.perm); err != nil {
			return err
		}
	}
	return w.prune(now)
}

// prune applies count/age retention to previously rotated files. Callers must
// hold w.mu.
func (w *fileWriter) prune(now time.Time) error {
	switch w.keep.Mode {
	case "forever":
		return nil
	case "none":
		// Rotated files are removed at rotation time; sweep any leftovers.
	}
	entries, err := filepath.Glob(w.path + ".*")
	if err != nil {
		return err
	}
	type rotatedFile struct {
		path string
		mod  time.Time
	}
	var files []rotatedFile
	for _, p := range entries {
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			continue
		}
		files = append(files, rotatedFile{path: p, mod: st.ModTime()})
	}
	// Newest first so count-based retention keeps the most recent files.
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })

	var firstErr error
	for i, f := range files {
		var remove bool
		switch w.keep.Mode {
		case "none":
			remove = true
		case "count":
			remove = i >= w.keep.Count
		case "age":
			remove = now.Sub(f.mod) > w.keep.Age
		}
		if remove {
			if err := os.Remove(f.path); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// writeLine appends one already-formatted line (no trailing newline required)
// to the file, rotating first when the policy demands it. Errors are returned
// so callers can decide whether to surface them; writes never panic.
func (w *fileWriter) writeLine(line string) error {
	if !strings.HasSuffix(line, "\n") {
		line += "\n"
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.open(); err != nil {
		return err
	}
	now := time.Now()
	if w.needsRotate(int64(len(line)), now) {
		if err := w.doRotate(now); err != nil {
			return err
		}
		if err := w.open(); err != nil {
			return err
		}
	}
	n, err := w.file.WriteString(line)
	w.size += int64(n)
	if !w.holdOpen {
		w.closeLocked()
	}
	return err
}

// rotateCheck performs a scheduled time-based rotation check plus retention
// pruning. Unlike writeLine it also rotates files this process has not written
// to recently (e.g. a quiet debug.log crossing a week boundary).
func (w *fileWriter) rotateCheck() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	st, err := os.Stat(w.path)
	if err != nil {
		// No live file — still prune leftover rotated files.
		if os.IsNotExist(err) {
			return w.prune(now)
		}
		return err
	}
	if st.Size() > 0 {
		start := w.periodStart
		if w.file == nil {
			start = st.ModTime()
		}
		sizeHit := w.rotate.MaxBytes > 0 && st.Size() >= w.rotate.MaxBytes
		timeHit := w.rotate.Interval != "" &&
			periodKey(w.rotate.Interval, start) != periodKey(w.rotate.Interval, now)
		if sizeHit || timeHit {
			if err := w.doRotate(now); err != nil {
				return err
			}
			return nil
		}
	}
	return w.prune(now)
}

// close releases the file handle.
func (w *fileWriter) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closeLocked()
}

// gzipAndRemove compresses src to src+".gz" with the given permissions and
// removes the original on success.
func gzipAndRemove(src string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		if err := in.Close(); err != nil {
			log.Printf("log rotation: close source failed: %v", err)
		}
	}()
	dst := src + ".gz"
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(out)
	if _, err := io.Copy(gz, in); err != nil {
		_ = gz.Close()
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := gz.Close(); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return os.Remove(src)
}

// EnforceMinimumAge returns the keep policy to apply given a compliance
// minimum retention of days (PART 11: audit log retention under a compliance
// standard). Policies that already retain at least that long ("forever", or an
// age >= days) are returned unchanged; anything shorter, including "none" and
// plain counts whose age cannot be known, becomes "<days>d". days <= 0 leaves
// keep unchanged.
func EnforceMinimumAge(keep string, days int) string {
	if days <= 0 {
		return keep
	}
	p, err := ParseKeep(keep)
	if err == nil {
		switch p.Mode {
		case "forever":
			return keep
		case "age":
			if p.Age >= time.Duration(days)*24*time.Hour {
				return keep
			}
		}
	}
	return fmt.Sprintf("%dd", days)
}
