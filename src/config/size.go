package config

import (
	"fmt"
	"strconv"
	"strings"
)

// sizeUnits lists recognised size suffixes (case-insensitive) with their byte
// multiplier, longest/most-specific first so "mb" is matched before a bare
// "b" suffix would otherwise swallow it.
var sizeUnits = []struct {
	suffix     string
	multiplier int64
}{
	{"tb", 1 << 40},
	{"gb", 1 << 30},
	{"mb", 1 << 20},
	{"kb", 1 << 10},
	{"t", 1 << 40},
	{"g", 1 << 30},
	{"m", 1 << 20},
	{"k", 1 << 10},
	{"b", 1},
}

// ParseSize parses a human-readable size string ("100kb", "20mb", "1gb",
// "1tb") into a byte count, so operators never have to hand-convert a size to
// raw bytes in server.yml. A bare number with no unit suffix ("5") defaults
// to megabytes ("5mb"). Zero or a negative value means unlimited and returns
// 0. An empty string returns defaultVal; a non-empty, unparsable value
// returns an error (PART 5: empty/unset uses the default, an actually
// invalid value is an error, never a silent default).
func ParseSize(s string, defaultVal int64) (int64, error) {
	trimmed := strings.TrimSpace(strings.ToLower(s))
	if trimmed == "" {
		return defaultVal, nil
	}
	for _, u := range sizeUnits {
		if !strings.HasSuffix(trimmed, u.suffix) {
			continue
		}
		numPart := strings.TrimSpace(strings.TrimSuffix(trimmed, u.suffix))
		if numPart == "" {
			continue
		}
		n, err := strconv.ParseFloat(numPart, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid size value: %q", s)
		}
		if n <= 0 {
			// Zero or negative means unlimited.
			return 0, nil
		}
		return int64(n * float64(u.multiplier)), nil
	}
	// No recognised unit suffix: treat the bare number as megabytes.
	n, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size value: %q", s)
	}
	if n <= 0 {
		// Zero or negative means unlimited.
		return 0, nil
	}
	return int64(n * float64(1<<20)), nil
}

// FormatSize renders a positive byte count as a human-readable size string
// ("10 MB", "100 KB", "1 GB", "512 B") using the largest unit that divides
// the count evenly, so UI/help text never shows a raw byte count the user
// has to convert themselves. Callers must check for the zero-or-negative
// "unlimited" case themselves (via an i18n string) before calling this —
// FormatSize has no user-facing text of its own to keep it locale-agnostic.
func FormatSize(bytes int64) string {
	switch {
	case bytes >= 1<<40 && bytes%(1<<40) == 0:
		return fmt.Sprintf("%d TB", bytes/(1<<40))
	case bytes >= 1<<30 && bytes%(1<<30) == 0:
		return fmt.Sprintf("%d GB", bytes/(1<<30))
	case bytes >= 1<<20 && bytes%(1<<20) == 0:
		return fmt.Sprintf("%d MB", bytes/(1<<20))
	case bytes >= 1<<10 && bytes%(1<<10) == 0:
		return fmt.Sprintf("%d KB", bytes/(1<<10))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
