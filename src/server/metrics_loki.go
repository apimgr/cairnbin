package server

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/apimgr/cairnbin/src/logging"
	"github.com/apimgr/cairnbin/src/metric"
)

// lokiTailBytes is how much of the tail of each log file is read per scrape.
// Sized to comfortably hold loki.max_entries short lines while keeping the
// read bounded: a scrape never reads a whole rotated-oversized log file.
const lokiTailBytes = 512 * 1024

// lokiStreamFiles are the server log files the loki service exposes. Part 20
// calls for log telemetry alongside the Prometheus metrics; audit.log is
// deliberately excluded because it already has its own download route and
// carries the fullest operational detail.
var lokiStreamFiles = []string{
	"access.log",
	"server.log",
	"error.log",
	"app.log",
	"auth.log",
	"security.log",
}

// recentLogEntries is the LogProvider behind the loki metrics service. It tails
// the recent portion of each server log file and returns the lines as Loki
// stream entries labelled by file name.
//
// This is intentionally a file tail rather than a hook into the logging
// Manager: the manager holds open descriptors and formats to its configured
// per-file format, and re-deriving that here would duplicate the renderers.
// Tailing reads whatever the configured format actually produced, so JSON
// files are decoded into real labels and text files keep their raw line.
func (s *Server) recentLogEntries() []metric.LokiEntry {
	if s.logDir == "" {
		return nil
	}
	var out []metric.LokiEntry
	for _, name := range lokiStreamFiles {
		out = append(out, tailLogFile(filepath.Join(s.logDir, name), name)...)
	}
	return out
}

// tailLogFile reads the last lokiTailBytes of path and returns its complete
// lines as Loki entries. A missing or unreadable file yields no entries rather
// than an error: an operator may have disabled a log file, and a scrape must
// not fail because of it.
func tailLogFile(path, name string) []metric.LokiEntry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return nil
	}
	// Seek to the tail window, then drop the first (probably partial) line.
	offset := info.Size() - lokiTailBytes
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return nil
		}
	}

	var entries []metric.LokiEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), lokiTailBytes)
	first := offset > 0
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if first {
			// Everything before this point was cut mid-line by the seek.
			first = false
			continue
		}
		if line == "" {
			continue
		}
		entries = append(entries, lokiEntry(name, line))
	}
	return entries
}

// lokiEntry converts one log line into a Loki stream entry. JSON log formats
// are decoded so time, level, and any extra fields become real Loki labels and
// the stored message is not duplicated into the line; every other format is
// carried through verbatim with only the timestamp recovered.
func lokiEntry(file, line string) metric.LokiEntry {
	entry := metric.LokiEntry{
		Labels: map[string]string{"file": file},
		Line:   logging.Sanitize(line),
	}

	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		// Not JSON: try the leading RFC3339 timestamp the text formats emit.
		entry.Time = parseLokiTime(line)
		return entry
	}

	for k, v := range obj {
		s, ok := v.(string)
		if !ok {
			continue
		}
		switch k {
		case "time":
			// Promoted to the stream timestamp rather than left as a label.
			if ts, err := time.Parse(time.RFC3339, s); err == nil {
				entry.Time = ts
			}
		case "msg", "message":
			entry.Line = logging.Sanitize(s)
		default:
			// Every remaining JSON field becomes a label, which is what makes
			// the loki service useful for querying by level, actor, or task.
			entry.Labels[k] = logging.Sanitize(s)
		}
	}
	if entry.Time.IsZero() {
		entry.Time = parseLokiTime(line)
	}
	return entry
}

// lokiTimeLayouts are the timestamp prefixes the non-JSON log formats write,
// in the order they are attempted.
var lokiTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006/01/02 15:04:05",
}

// parseLokiTime recovers a timestamp from a log line that does not carry a
// JSON time field. It returns the zero time when none of the known layouts
// match, which the loki service substitutes with scrape time.
func parseLokiTime(line string) time.Time {
	if len(line) < 20 {
		return time.Time{}
	}
	prefix := line[:min(len(line), 35)]
	for _, layout := range lokiTimeLayouts {
		if ts, err := time.Parse(layout, prefix); err == nil {
			return ts
		}
	}
	return time.Time{}
}
