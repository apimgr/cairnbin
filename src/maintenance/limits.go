package maintenance

import (
	"fmt"
	"os"

	"github.com/apimgr/pastebin/src/config"
)

// RestoreLimits bounds a restore/import archive (PART 21, `server.restore`).
// A zero field means "no limit" for that dimension.
type RestoreLimits struct {
	// MaxArchiveSize is the largest accepted archive file, in bytes.
	MaxArchiveSize int64
	// MaxExpandedSize is the largest total of all extracted file bytes.
	MaxExpandedSize int64
	// MaxFiles is the largest number of entries in the archive.
	MaxFiles int
	// MaxFileSize is the largest single extracted file, in bytes.
	MaxFileSize int64
	// MaxRatio is the largest expanded-to-archive size ratio (100 = 100:1).
	MaxRatio int
}

// DefaultRestoreLimits returns the PART 21 defaults: 1G archive, 4G expanded,
// 10000 files, 512M per file, 100:1 ratio.
func DefaultRestoreLimits() RestoreLimits {
	return RestoreLimitsFromConfig(config.DefaultConfig().Server.Restore)
}

// RestoreLimitsFromConfig converts the validated `server.restore` block into
// byte counts. Unparsable sizes fall back to the defaults.
func RestoreLimitsFromConfig(c config.RestoreConfig) RestoreLimits {
	size := func(v string, def int64) int64 {
		n, err := config.ParseSize(v, def)
		if err != nil || n <= 0 {
			return def
		}
		return n
	}
	files, ratio := c.MaxFiles, c.MaxRatio
	if files < 1 {
		files = 10000
	}
	if ratio < 1 {
		ratio = 100
	}
	return RestoreLimits{
		MaxArchiveSize:  size(c.MaxArchiveSize, 1<<30),
		MaxExpandedSize: size(c.MaxExpandedSize, 4<<30),
		MaxFiles:        files,
		MaxFileSize:     size(c.MaxFileSize, 512<<20),
		MaxRatio:        ratio,
	}
}

// checkArchiveSize rejects an archive file larger than MaxArchiveSize before
// any of it is read into memory.
func (l RestoreLimits) checkArchiveSize(path string) error {
	if l.MaxArchiveSize <= 0 {
		return nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat backup: %w", err)
	}
	if st.Size() > l.MaxArchiveSize {
		return fmt.Errorf("backup is %d bytes, above server.restore.max_archive_size (%d)", st.Size(), l.MaxArchiveSize)
	}
	return nil
}

// extractBudget tracks what has been extracted so far and aborts the restore
// as soon as any limit is crossed.
type extractBudget struct {
	limits     RestoreLimits
	compressed int64
	files      int
	expanded   int64
}

// admit is called for every archive entry before it is extracted.
func (b *extractBudget) admit(size int64) error {
	b.files++
	if b.limits.MaxFiles > 0 && b.files > b.limits.MaxFiles {
		return fmt.Errorf("archive has more than %d files (server.restore.max_files)", b.limits.MaxFiles)
	}
	if b.limits.MaxFileSize > 0 && size > b.limits.MaxFileSize {
		return fmt.Errorf("entry is %d bytes, above server.restore.max_file_size (%d)", size, b.limits.MaxFileSize)
	}
	return nil
}

// account is called with the real number of bytes written for an entry.
func (b *extractBudget) account(n int64) error {
	b.expanded += n
	if b.limits.MaxExpandedSize > 0 && b.expanded > b.limits.MaxExpandedSize {
		return fmt.Errorf("expanded size exceeds server.restore.max_expanded_size (%d)", b.limits.MaxExpandedSize)
	}
	if b.limits.MaxRatio > 0 && b.compressed > 0 && b.expanded/b.compressed > int64(b.limits.MaxRatio) {
		return fmt.Errorf("expansion ratio exceeds server.restore.max_ratio (%d:1)", b.limits.MaxRatio)
	}
	return nil
}
