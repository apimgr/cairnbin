// Package maintenance implements the --maintenance CLI subcommands:
// backup, restore, mode, setup, and update (alias for --update yes).
package maintenance

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"

	"github.com/apimgr/cairnbin/src/audit"
	"github.com/apimgr/cairnbin/src/common/i18n"
	"github.com/apimgr/cairnbin/src/common/secretbox"
	"github.com/apimgr/cairnbin/src/paths"
	"github.com/apimgr/cairnbin/src/pgp"
)

// ErrComplianceBackupBlocked is returned by Backup when compliance mode
// (server.compliance.enabled) is on and no encryption password was supplied
// (AI.md 28945-28989: "Backups will NOT run unless encryption password is
// set"). Callers can match it with errors.Is to distinguish a compliance
// refusal from any other backup failure.
var ErrComplianceBackupBlocked = errors.New("compliance mode requires an encryption password; backup refused")
var ErrBaseMissing = errors.New("ERR_BASE_MISSING")
var ErrBaseCorrupt = errors.New("ERR_BASE_CORRUPT")

const (
	orgName     = "apimgr"
	projectName = "cairnbin"
)

var outputLanguage = "en"

// SetOutputLanguage selects the locale for maintenance command output.
func SetOutputLanguage(lang string) {
	outputLanguage = i18n.GetLanguage(lang, "")
}

func maintenanceText(key string, args ...interface{}) string {
	if len(args) == 0 {
		return i18n.Translate(outputLanguage, "cli."+key)
	}
	return i18n.TranslateFormat(outputLanguage, "cli."+key, i18n.Args(args...))
}

// Manifest is embedded in every backup archive as manifest.json.
type Manifest struct {
	Version          string         `json:"version"`
	CreatedAt        string         `json:"created_at"`
	Type             string         `json:"type"`
	SnapshotAt       string         `json:"snapshot_at"`
	SHA256           string         `json:"sha256"`
	Files            []ManifestFile `json:"files,omitempty"`
	BaseBackup       string         `json:"base_backup,omitempty"`
	BaseSHA256       string         `json:"base_sha256,omitempty"`
	Deleted          []string       `json:"deleted,omitempty"`
	AppVersion       string         `json:"app_version"`
	CreatedBy        string         `json:"created_by"`
	Contents         []string       `json:"contents"`
	Encrypted        bool           `json:"encrypted"`
	EncryptionMethod string         `json:"encryption_method,omitempty"`
	Checksum         string         `json:"checksum"`
}

// ManifestFile describes one source file captured in a full or incremental
// archive.
type ManifestFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	MTime  int64  `json:"mtime"`
	SHA256 string `json:"sha256"`
}

// EncryptedBackupHeader is the cleartext, authenticated metadata prefix on an
// encrypted archive. It contains no password or derived key material.
type EncryptedBackupHeader struct {
	Salt       []byte
	MemoryKiB  uint32
	Iterations uint32
	Threads    uint32
	Nonce      []byte
	KeyID      string
	BaseBackup string
	AAD        []byte
	Offset     int
}

// BackupOptions controls what gets included in the backup.
type BackupOptions struct {
	ConfigDir  string
	DataDir    string
	BackupDir  string
	AppVersion string
	// empty = no encryption
	Password string
	// empty = auto-generate
	Filename string
	// BaseBackup records the base archive name in the authenticated header for
	// an incremental backup.
	BaseBackup string
	// BaseManifest and BaseSHA256 identify the full archive used by an
	// incremental backup.
	BaseManifest *Manifest
	BaseSHA256   string
	SnapshotAt   time.Time
	// IncludeSSL adds {config_dir}/ssl/ to the archive (--include-ssl).
	IncludeSSL bool
	// IncludeData adds {data_dir}/ (except db/ and the backup dir) to the
	// archive (--include-data).
	IncludeData bool
	// ComplianceEnabled mirrors server.compliance.enabled (AI.md 28945-28989).
	// When true, Backup refuses to run unless Password is set.
	ComplianceEnabled bool
	// Audit records the backup.compliance_blocked event when a backup is
	// refused under compliance mode; nil = no audit logging.
	Audit *audit.Writer
}

// Backup creates a tar.gz (optionally AES-256-GCM encrypted) backup of the
// config and database files and writes it to BackupDir.
func Backup(opts BackupOptions) error {
	// Compliance-mode enforcement (AI.md 28945-28989): encryption is optional
	// unless compliance mode is enabled, in which case an unencrypted backup
	// must be blocked outright — never silently created.
	if opts.ComplianceEnabled && opts.Password == "" {
		if opts.Audit != nil {
			opts.Audit.Log(audit.Entry{
				Event:    "backup.compliance_blocked",
				Severity: audit.SeverityWarn,
				Result:   audit.ResultFailure,
				Target:   &audit.Target{Type: "backup_dir", ID: opts.BackupDir},
				Reason:   ErrComplianceBackupBlocked.Error(),
			})
		}
		return fmt.Errorf("%w (set backup.encryption_password / backup.encryption.enabled in server.yml)", ErrComplianceBackupBlocked)
	}

	if err := os.MkdirAll(opts.BackupDir, 0o700); err != nil {
		return fmt.Errorf("creating backup dir: %w", err)
	}

	ts := time.Now().UTC().Format("2006-01-02_150405")
	ext := ".tar.gz"
	if opts.Password != "" {
		ext = ".tar.gz.enc"
	}
	filename := opts.Filename
	if filename == "" {
		filename = fmt.Sprintf("%s_backup_%s%s", projectName, ts, ext)
	}
	if filename != filepath.Base(filename) || strings.Contains(filename, "..") {
		return fmt.Errorf("invalid backup filename %q: must not contain path components", filename)
	}
	destPath := filepath.Join(opts.BackupDir, filename)
	if _, err := os.Lstat(destPath); err == nil {
		return fmt.Errorf("backup destination already exists: %s", filename)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking backup destination: %w", err)
	}

	// Collect files to archive.
	type entry struct {
		src string
		// path inside the archive
		name string
	}
	var entries []entry
	var contents []string

	addFile := func(src, name string) {
		if _, err := os.Stat(src); err == nil {
			entries = append(entries, entry{src, name})
			contents = append(contents, name)
		}
	}
	addDir := func(dir, prefix string) error {
		return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				if os.IsNotExist(err) && path == dir {
					return nil
				}
				return err
			}
			if info.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			addFile(path, filepath.Join(prefix, rel))
			return nil
		})
	}

	addFile(filepath.Join(opts.ConfigDir, "server.yml"), "server.yml")
	addFile(filepath.Join(opts.ConfigDir, "secrets.yml"), "secrets.yml")
	if err := addDir(filepath.Join(opts.ConfigDir, "yaml.d"), "yaml.d"); err != nil {
		return fmt.Errorf("collect drop-in config backup files: %w", err)
	}
	if err := addDir(filepath.Join(opts.ConfigDir, "html"), "html"); err != nil {
		return fmt.Errorf("collect operator content backup files: %w", err)
	}
	addFile(filepath.Join(opts.DataDir, "db", "server.db"), "server.db")
	addFile(filepath.Join(opts.DataDir, "db", "users.db"), "users.db")
	for _, item := range []struct{ dir, prefix string }{
		{filepath.Join(opts.ConfigDir, "template"), "template"},
		{filepath.Join(opts.ConfigDir, "theme"), "theme"},
		{filepath.Join(opts.ConfigDir, "security"), "security"},
	} {
		if err := addDir(item.dir, item.prefix); err != nil {
			return fmt.Errorf("collect backup files from %s: %w", item.dir, err)
		}
	}
	// Security keypair state (PART, AI.md 14206-14209): public key, encrypted
	// private key, parked rotated key, and per-keyserver publish state. The
	// private key's KDF input is installation_secret, which lives in server.db
	// (already archived), so the backup is self-sufficient to restore.
	if opts.IncludeSSL {
		if err := addDir(filepath.Join(opts.ConfigDir, "ssl"), "ssl"); err != nil {
			return fmt.Errorf("collect SSL backup files: %w", err)
		}
	}
	if opts.IncludeData {
		if err := addDataDir(addFile, opts.DataDir, opts.BackupDir); err != nil {
			return fmt.Errorf("collect backup data: %w", err)
		}
	}

	if opts.BaseBackup != "" && (opts.BaseManifest == nil || opts.BaseSHA256 == "") {
		return fmt.Errorf("incremental backup requires base manifest and archive checksum")
	}
	baseFiles := make(map[string]ManifestFile)
	if opts.BaseManifest != nil {
		for _, file := range opts.BaseManifest.Files {
			baseFiles[filepath.ToSlash(file.Path)] = file
		}
	}
	currentFiles := make([]ManifestFile, 0, len(entries))
	changedFiles := make([]ManifestFile, 0, len(entries))
	changedEntries := make([]entry, 0, len(entries))
	currentPaths := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		data, readErr := os.ReadFile(e.src)
		if readErr != nil {
			return fmt.Errorf("reading %s for backup manifest: %w", e.name, readErr)
		}
		info, statErr := os.Stat(e.src)
		if statErr != nil {
			return fmt.Errorf("stat %s for backup manifest: %w", e.name, statErr)
		}
		path := filepath.ToSlash(e.name)
		fileHash := sha256.Sum256(data)
		record := ManifestFile{Path: path, Size: int64(len(data)), MTime: info.ModTime().Unix(), SHA256: hex.EncodeToString(fileHash[:])}
		currentFiles = append(currentFiles, record)
		currentPaths[path] = struct{}{}
		base, exists := baseFiles[path]
		if opts.BaseManifest == nil || !exists || base.Size != record.Size || base.MTime != record.MTime || base.SHA256 != record.SHA256 {
			changedEntries = append(changedEntries, e)
			changedFiles = append(changedFiles, record)
		}
	}
	deleted := make([]string, 0)
	for path := range baseFiles {
		if _, exists := currentPaths[path]; !exists {
			deleted = append(deleted, path)
		}
	}
	sort.Strings(deleted)
	if opts.BaseManifest != nil {
		entries = changedEntries
		currentFiles = changedFiles
		contents = contents[:0]
		for _, e := range entries {
			contents = append(contents, filepath.ToSlash(e.name))
		}
	}

	// Compute content checksum: SHA-256 of all data file bytes in archive order,
	// excluding the manifest. This avoids the circular-dependency that would arise
	// from hashing an archive that itself contains the checksum.
	contentHash := sha256.New()
	for _, e := range entries {
		b, readErr := os.ReadFile(e.src)
		if readErr != nil {
			return fmt.Errorf("reading %s for backup checksum: %w", e.name, readErr)
		}
		_, _ = contentHash.Write(b)
	}

	// Build and add the manifest (with checksums set before the archive pass).
	hostname, _ := os.Hostname()
	snapshotAt := opts.SnapshotAt.UTC()
	if snapshotAt.IsZero() {
		snapshotAt = time.Now().UTC()
	}
	backupType := "full"
	if opts.BaseManifest != nil {
		backupType = "incremental"
	}
	manifest := Manifest{
		Version:    "1.0.0",
		CreatedAt:  snapshotAt.Format(time.RFC3339),
		Type:       backupType,
		SnapshotAt: snapshotAt.Format(time.RFC3339),
		SHA256:     "sha256:" + hex.EncodeToString(contentHash.Sum(nil)),
		Files:      currentFiles,
		BaseBackup: opts.BaseBackup,
		BaseSHA256: opts.BaseSHA256,
		Deleted:    deleted,
		AppVersion: opts.AppVersion,
		CreatedBy:  orgName + "/" + projectName + "@" + hostname,
		Contents:   contents,
		Encrypted:  opts.Password != "",
		Checksum:   "sha256:" + hex.EncodeToString(contentHash.Sum(nil)),
	}
	if opts.Password != "" {
		manifest.EncryptionMethod = "AES-256-GCM"
	}

	// Build the archive into memory so we can optionally encrypt without
	// writing a plaintext file to disk.
	archiveBuf := &memBuf{}

	gz := gzip.NewWriter(archiveBuf)
	tw := tar.NewWriter(gz)

	for _, e := range entries {
		if err := addToTar(tw, e.src, e.name); err != nil {
			return fmt.Errorf("archiving %s: %w", e.name, err)
		}
	}

	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	hdr := &tar.Header{
		Name:    "manifest.json",
		Size:    int64(len(manifestJSON)),
		Mode:    0o600,
		ModTime: time.Now().UTC(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write manifest header: %w", err)
	}
	if _, err := tw.Write(manifestJSON); err != nil {
		return fmt.Errorf("write manifest body: %w", err)
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("close backup tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("close backup gzip: %w", err)
	}

	archiveBytes := archiveBuf.Bytes()

	var finalBytes []byte
	if opts.Password != "" {
		finalBytes, err = encryptWithBase(archiveBytes, opts.Password, opts.BaseBackup)
		if err != nil {
			return fmt.Errorf("encrypting backup: %w", err)
		}
	} else {
		finalBytes = archiveBytes
	}

	// Stage beside the destination so the old backup remains intact until the
	// replacement is fully written and verified.
	tmpPath := destPath + ".tmp"
	tmp, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("creating staged backup: %w", err)
	}
	stageOwned := true
	defer func() {
		if stageOwned {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(finalBytes); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing staged backup: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("syncing staged backup: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing staged backup: %w", err)
	}
	if err := VerifyBackup(tmpPath, opts.Password); err != nil {
		return fmt.Errorf("verifying staged backup: %w", err)
	}
	// A hard link installs the verified archive atomically without replacing an
	// existing destination, including when another backup races this one.
	if err := os.Link(tmpPath, destPath); err != nil {
		return fmt.Errorf("installing verified backup: %w", err)
	}
	stageOwned = false
	if err := os.Remove(tmpPath); err != nil {
		return fmt.Errorf("removing staged backup after install: %w", err)
	}

	fmt.Printf("Backup written: %s\n", destPath)
	return nil
}

// Restore extracts a backup archive into the config and data directories.
// Per PART 21, the backup is fully verified before any data is written;
// restore only proceeds when every verification check passes.
func Restore(archivePath, configDir, dataDir, password string) error {
	return RestoreWithVersion(archivePath, configDir, dataDir, password, "")
}

// RestoreWithVersion warns when the backup was produced by a different app
// version, then restores it after all integrity checks pass.
func RestoreWithVersion(archivePath, configDir, dataDir, password, runningVersion string) error {
	return RestoreWithLimits(archivePath, configDir, dataDir, password, runningVersion, DefaultRestoreLimits())
}

// RestoreWithLimits is RestoreWithVersion with explicit `server.restore`
// limits: the archive size is checked before it is read, and extraction aborts
// the moment the file count, per-file size, expanded size, or expansion ratio
// limit is crossed.
func RestoreWithLimits(archivePath, configDir, dataDir, password, runningVersion string, limits RestoreLimits) error {
	if err := limits.checkArchiveSize(archivePath); err != nil {
		return fmt.Errorf("backup rejected: %w", err)
	}
	// Verify the backup before touching any live config or data (PART 21:
	// "Only proceed with restore if ALL verification checks pass").
	if err := VerifyBackup(archivePath, password); err != nil {
		return fmt.Errorf("backup verification failed: %w", err)
	}

	data, err := os.ReadFile(archivePath)
	if err != nil {
		return fmt.Errorf("reading backup: %w", err)
	}

	// Detect encryption by file extension.
	if strings.HasSuffix(archivePath, ".enc") {
		if password == "" {
			return fmt.Errorf("backup is encrypted — a password is required to restore it")
		}
		data, err = decrypt(data, password)
		if err != nil {
			return fmt.Errorf("decrypting backup: %w", err)
		}
	}

	manifestGzip, err := gzip.NewReader(strings.NewReader(string(data)))
	if err != nil {
		return fmt.Errorf("opening gzip: %w", err)
	}
	manifestTar := tar.NewReader(manifestGzip)
	var manifest Manifest
	for {
		hdr, readErr := manifestTar.Next()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = manifestGzip.Close()
			return fmt.Errorf("reading backup manifest: %w", readErr)
		}
		if hdr.Name == "manifest.json" {
			if err := json.NewDecoder(manifestTar).Decode(&manifest); err != nil {
				_ = manifestGzip.Close()
				return fmt.Errorf("reading backup manifest: %w", err)
			}
			break
		}
	}
	if err := manifestGzip.Close(); err != nil {
		return fmt.Errorf("closing backup manifest: %w", err)
	}
	if runningVersion != "" && manifest.AppVersion != "" && !sameAppVersion(runningVersion, manifest.AppVersion) {
		fmt.Fprintf(os.Stderr, "Warning: backup app version %s differs from running version %s; restore may require schema updates\n", manifest.AppVersion, runningVersion)
	}

	gr, err := gzip.NewReader(strings.NewReader(string(data)))
	if err != nil {
		return fmt.Errorf("opening gzip: %w", err)
	}
	defer func() { _ = gr.Close() }()

	tr := tar.NewReader(gr)
	budget := &extractBudget{limits: limits, compressed: int64(len(data))}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading archive: %w", err)
		}
		if hdr.Name == "manifest.json" {
			continue
		}
		if err := budget.admit(hdr.Size); err != nil {
			return fmt.Errorf("backup rejected: %w", err)
		}
		if err := extractEntryBudget(tr, hdr, configDir, dataDir, budget); err != nil {
			return fmt.Errorf("extracting %s: %w", hdr.Name, err)
		}
	}

	fmt.Printf("Restore complete from: %s\n", archivePath)
	return nil
}

func sameAppVersion(a, b string) bool {
	return strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v")
}

// SetYAMLField updates a top-level YAML key in the given config file.
// If the file does not exist it is created with the key set to value.
// Intended for programmatic updates (e.g. persisting update_branch from the CLI).
func SetYAMLField(cfgPath, key, value string) error {
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("reading config: %w", err)
		}
		// File does not exist — create it with just the requested key.
		content := fmt.Sprintf("%s: %s\n", key, value)
		if writeErr := atomicWriteFile(cfgPath, []byte(content), 0o600); writeErr != nil {
			return fmt.Errorf("writing config: %w", writeErr)
		}
		return nil
	}
	updated := strings.Join(replaceYAMLField(strings.Split(string(data), "\n"), key, value), "\n")
	if err := atomicWriteFile(cfgPath, []byte(updated), 0o600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	return nil
}

// SetMode updates the server.mode value in server.yml.
func SetMode(configDir, mode string) error {
	cfgPath := filepath.Join(configDir, "server.yml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	updated := strings.Join(replaceYAMLField(strings.Split(string(data), "\n"), "mode", mode), "\n")
	if err := atomicWriteFile(cfgPath, []byte(updated), 0o600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	fmt.Printf("Mode set to: %s\n", mode)
	return nil
}

// atomicWriteFile writes data to path atomically: it writes to a temp file in
// the same directory, fsyncs it, and renames it into place so a crash mid-write
// can never leave a truncated server.yml (which holds the encryption key and
// operator token).
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".server.yml.*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp config: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("syncing temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("renaming temp config: %w", err)
	}
	return nil
}

// replaceYAMLField does a best-effort in-place replacement of a top-level
// YAML key's value.  Only handles simple scalar values.
func replaceYAMLField(lines []string, key, value string) []string {
	prefix := key + ":"
	out := make([]string, len(lines))
	for i, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, prefix) {
			indent := strings.Index(l, strings.TrimLeft(l, " \t"))
			if indent < 0 {
				indent = 0
			}
			out[i] = strings.Repeat(" ", indent) + key + ": " + value
		} else {
			out[i] = l
		}
	}
	return out
}

// Setup prints guidance for resetting admin credentials.
func Setup(configDir string) error {
	fmt.Println("To reset admin credentials, edit the database directly or restart")
	fmt.Println("the server with the CAIRNBIN_RESET_ADMIN=1 environment variable.")
	fmt.Printf("Config directory: %s\n", configDir)
	return nil
}

// PrintHelp prints the --maintenance subcommand help.
func PrintHelp(binaryName string, languages ...string) {
	lang := "en"
	if len(languages) > 0 {
		lang = i18n.GetLanguage(languages[0], "")
	}
	fmt.Println(i18n.TranslateFormat(lang, "cli.maintenance_help", i18n.Args(
		"project_name", binaryName,
		"backup_dir", paths.GetBackupDir(projectName),
	)))
}

// VerifyBackup performs the post-creation checks required by PART 21:
//  1. File exists
//  2. Size > 0
//  3. Decrypt (if encrypted, password must work)
//  4. Manifest present and parseable
//  5. All archive entries extractable to temp dir
//  6. Any *.db entries have a valid SQLite magic header
//
// Returns nil when all checks pass. The caller must delete the file and abort
// retention on any error.
func VerifyBackup(path, password string) error {
	// Check 1 + 2: file exists and non-empty.
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("verify: file not found: %w", err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("verify: file is empty: %s", path)
	}

	// Read the raw bytes.
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("verify: cannot read: %w", err)
	}

	// Check 3: decrypt if encrypted (.enc or .enc.tmp for in-flight temp files).
	checkPath := path
	for strings.HasSuffix(checkPath, ".tmp") {
		checkPath = strings.TrimSuffix(checkPath, ".tmp")
	}
	var encryptedHeader *EncryptedBackupHeader
	if strings.HasSuffix(checkPath, ".enc") {
		if password == "" {
			return fmt.Errorf("verify: encrypted backup requires password")
		}
		if bytes.HasPrefix(data, []byte(backupHeaderMagic)) {
			header, headerErr := ParseEncryptedBackupHeader(data)
			if headerErr != nil {
				return fmt.Errorf("verify: invalid encrypted header: %w", headerErr)
			}
			encryptedHeader = &header
		}
		data, err = decrypt(data, password)
		if err != nil {
			return fmt.Errorf("verify: decrypt failed: %w", err)
		}
	}

	// Open the gzip+tar archive.
	gr, err := gzip.NewReader(strings.NewReader(string(data)))
	if err != nil {
		return fmt.Errorf("verify: not a valid gzip archive: %w", err)
	}
	defer func() { _ = gr.Close() }()

	tr := tar.NewReader(gr)

	// Check 4 + 5 + 6: walk entries in a single pass.
	// Accumulate SHA-256 of all non-manifest content bytes to verify against
	// the checksum field embedded in the manifest (same algorithm as Backup).
	contentHash := sha256.New()
	var manifestFound bool
	var manifestChecksum string
	var manifest Manifest
	tmpDir, err := orgTempDir()
	if err != nil {
		return fmt.Errorf("verify: temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("verify: reading archive entry: %w", err)
		}

		if hdr.Name == "manifest.json" {
			manifestFound = true
			// Check 4: parse the manifest to retrieve the expected checksum.
			if jsonErr := json.NewDecoder(tr).Decode(&manifest); jsonErr != nil {
				return fmt.Errorf("verify: invalid manifest: %w", jsonErr)
			}
			manifestChecksum = manifest.Checksum
			continue
		}

		// Check 5: extract entry to temp dir while feeding bytes to the content hasher.
		dest := filepath.Join(tmpDir, filepath.Base(hdr.Name))
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return fmt.Errorf("verify: extract %s: %w", hdr.Name, err)
		}
		// 256 MiB cap per entry
		lr := io.LimitReader(tr, 256<<20)
		tee := io.TeeReader(lr, contentHash)
		if _, err := io.Copy(f, tee); err != nil {
			_ = f.Close()
			return fmt.Errorf("verify: read entry %s: %w", hdr.Name, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("verify: close extracted entry %s: %w", hdr.Name, err)
		}

		// Check 6: SQLite magic header for .db files.
		if strings.HasSuffix(hdr.Name, ".db") {
			if err := verifySQLiteMagic(dest); err != nil {
				return fmt.Errorf("verify: db integrity %s: %w", hdr.Name, err)
			}
		}
	}

	if !manifestFound {
		return fmt.Errorf("verify: manifest.json missing from archive")
	}

	// Validate SHA-256 of content files against the manifest checksum field.
	// Both Backup() and VerifyBackup() hash non-manifest entry bytes only,
	// which avoids the circular-dependency of hashing an archive that contains
	// the checksum itself.
	if manifestChecksum != "" {
		computedHex := "sha256:" + hex.EncodeToString(contentHash.Sum(nil))
		if manifestChecksum != computedHex {
			return fmt.Errorf("verify: checksum mismatch: expected %s, got %s", manifestChecksum, computedHex)
		}
	}
	if manifest.Type == "incremental" {
		if manifest.BaseBackup == "" || manifest.BaseSHA256 == "" {
			return fmt.Errorf("verify: incremental backup is missing base metadata")
		}
		if encryptedHeader != nil && encryptedHeader.BaseBackup != manifest.BaseBackup {
			return fmt.Errorf("verify: encrypted header base filename does not match manifest")
		}
		basePath := filepath.Join(filepath.Dir(path), manifest.BaseBackup)
		if _, err := os.Lstat(basePath); err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("%w: %s", ErrBaseMissing, manifest.BaseBackup)
			}
			return fmt.Errorf("verify: check incremental base: %w", err)
		}
		baseSHA, err := FileSHA256(basePath)
		if err != nil || baseSHA != manifest.BaseSHA256 {
			return fmt.Errorf("%w: %s", ErrBaseCorrupt, manifest.BaseBackup)
		}
		if err := VerifyBackup(basePath, password); err != nil {
			return fmt.Errorf("%w: %v", ErrBaseCorrupt, err)
		}
	}

	return nil
}

// TestSecurityKeypair performs a dry-run restore of the coordinated-disclosure
// keypair (AI.md 14213): it reads the encrypted project private key from
// {configDir}/security/pgp.priv.asc.enc, unwraps it with the
// installation_secret-derived key, and confirms it decrypts to a valid armored
// key carrying a user identity. It never writes the decrypted key anywhere.
//
// Returns nil when the private key decrypts successfully. A missing key file is
// reported as an error so `backup test` fails loudly when publish is enabled but
// no keypair exists.
func TestSecurityKeypair(configDir string, installSecret []byte) error {
	privPath := filepath.Join(configDir, "security", "pgp.priv.asc.enc")
	sealed, err := os.ReadFile(privPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("test: no security keypair found at %s", privPath)
		}
		return fmt.Errorf("test: read private key: %w", err)
	}

	wrapKey, err := pgp.WrapKey(installSecret)
	if err != nil {
		return fmt.Errorf("test: %w", err)
	}
	armored, err := secretbox.Open(wrapKey, sealed)
	if err != nil {
		return fmt.Errorf("test: decrypt private key failed: %w", err)
	}

	identity, err := pgp.IdentityOf(string(armored))
	if err != nil {
		return fmt.Errorf("test: decrypted private key is invalid: %w", err)
	}
	fmt.Printf("Security keypair OK: private key decrypts successfully (identity: %s)\n", identity)
	return nil
}

// verifySQLiteMagic checks that a file begins with the SQLite3 magic header.
func verifySQLiteMagic(path string) error {
	const sqliteMagic = "SQLite format 3\x00"
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, len(sqliteMagic))
	if _, err := io.ReadFull(f, buf); err != nil {
		return fmt.Errorf("cannot read header: %w", err)
	}
	if string(buf) != sqliteMagic {
		return fmt.Errorf("not a valid SQLite3 file")
	}
	return nil
}

// ── internal helpers ─────────────────────────────────────────────────────────

type memBuf struct {
	b []byte
}

func (m *memBuf) Write(p []byte) (int, error) {
	m.b = append(m.b, p...)
	return len(p), nil
}

func (m *memBuf) Bytes() []byte { return m.b }

func addToTar(tw *tar.Writer, srcPath, archiveName string) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return err
	}
	hdr := &tar.Header{
		Name:    archiveName,
		Size:    info.Size(),
		Mode:    int64(info.Mode()),
		ModTime: info.ModTime(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

func extractEntry(tr *tar.Reader, hdr *tar.Header, configDir, dataDir string) error {
	return extractEntryBudget(tr, hdr, configDir, dataDir, &extractBudget{limits: DefaultRestoreLimits()})
}

func extractEntryBudget(tr *tar.Reader, hdr *tar.Header, configDir, dataDir string, budget *extractBudget) error {
	// Map archive names to destination paths.
	var dest string
	switch hdr.Name {
	case "server.yml":
		dest = filepath.Join(configDir, "server.yml")
	case "secrets.yml":
		dest = filepath.Join(configDir, "secrets.yml")
	case "server.db":
		dest = filepath.Join(dataDir, "db", "server.db")
	case "users.db":
		dest = filepath.Join(dataDir, "db", "users.db")
	default:
		// Sub-directory entries (template/, theme/, security/) carry a relative
		// path from a crafted archive. Guard every branch against path traversal
		// so a malicious backup cannot escape its destination base directory.
		var base, prefix string
		switch {
		case strings.HasPrefix(hdr.Name, "template/"):
			base, prefix = filepath.Join(configDir, "template"), "template/"
		case strings.HasPrefix(hdr.Name, "theme/"):
			base, prefix = filepath.Join(configDir, "theme"), "theme/"
		case strings.HasPrefix(hdr.Name, "security/"):
			base, prefix = filepath.Join(configDir, "security"), "security/"
		case strings.HasPrefix(hdr.Name, "html/"):
			base, prefix = filepath.Join(configDir, "html"), "html/"
		case strings.HasPrefix(hdr.Name, "ssl/"):
			base, prefix = filepath.Join(configDir, "ssl"), "ssl/"
		case strings.HasPrefix(hdr.Name, "data/"):
			base, prefix = dataDir, "data/"
		default:
			// unknown entry — skip
			return nil
		}
		rel := strings.TrimPrefix(hdr.Name, prefix)
		candidate := filepath.Join(base, rel)
		// Reject any entry that escapes the base directory (absolute paths,
		// "..", or symlink-style prefixes all resolve outside base).
		if candidate != base && !strings.HasPrefix(candidate, base+string(os.PathSeparator)) {
			return nil
		}
		dest = candidate
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}

	// The containment check above is purely lexical, so a symlink planted
	// anywhere along the path defeats it: MkdirAll and OpenFile both follow
	// pre-existing symlinks, letting a crafted archive write outside the
	// destination base. Re-verify against the real filesystem now that the
	// directories exist, and refuse to extract through any symlink.
	if err := rejectSymlinkedPath(dest); err != nil {
		return err
	}

	// PART 21: restored files always get mode 0600 — never trust the archive
	// header mode, which a crafted backup could set world-writable.
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	// O_CREATE mode only applies to new files — tighten pre-existing ones too.
	if err := f.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", dest, err)
	}

	// Read at most one byte past the per-file limit so an entry whose header
	// understates its size is still caught; the written total feeds the
	// expanded-size and ratio checks.
	var reader io.Reader = tr
	if budget.limits.MaxFileSize > 0 {
		reader = io.LimitReader(tr, budget.limits.MaxFileSize+1)
	}
	n, err := io.Copy(f, reader)
	if err != nil {
		return err
	}
	if budget.limits.MaxFileSize > 0 && n > budget.limits.MaxFileSize {
		return fmt.Errorf("entry exceeds server.restore.max_file_size (%d)", budget.limits.MaxFileSize)
	}
	return budget.account(n)
}

// rejectSymlinkedPath walks every component of path from the filesystem root
// down and fails if any component is a symbolic link. It uses Lstat rather
// than O_NOFOLLOW because that flag is not portable across Linux, BSD, macOS,
// and Windows. A component that does not exist yet is not an error — the
// caller has already created the parent directories.
func rejectSymlinkedPath(path string) error {
	cur := filepath.Clean(path)
	for {
		info, err := os.Lstat(cur)
		if err != nil {
			// A component that does not exist cannot be a symlink, but its
			// parents still can be — keep walking up to the root.
			if !os.IsNotExist(err) {
				return err
			}
		} else if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to restore through symlink: %s", cur)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return nil
		}
		cur = parent
	}
}

const (
	backupHeaderMagic = "BKUP1"
	backupMemoryKiB   = uint32(64 * 1024)
	backupIterations  = uint32(1)
	backupThreads     = uint32(4)
	backupHeaderFixed = 5 + 16 + 4 + 4 + 4 + 12 + 8 + 2
)

func encryptWithBase(plaintext []byte, password, baseBackup string) ([]byte, error) {
	if baseBackup != "" && (filepath.Base(baseBackup) != baseBackup || strings.ContainsAny(baseBackup, "/\\") || strings.Contains(baseBackup, "..") || !utf8.ValidString(baseBackup)) {
		return nil, fmt.Errorf("invalid incremental base filename %q", baseBackup)
	}
	if len(baseBackup) > 255 {
		return nil, fmt.Errorf("incremental base filename is too long")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	key := argon2.IDKey([]byte(password), salt, backupIterations, backupMemoryKiB, uint8(backupThreads), 32)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}

	header := make([]byte, 0, backupHeaderFixed+len(baseBackup))
	header = append(header, backupHeaderMagic...)
	header = append(header, salt...)
	var params [12]byte
	binary.BigEndian.PutUint32(params[0:4], backupMemoryKiB)
	binary.BigEndian.PutUint32(params[4:8], backupIterations)
	binary.BigEndian.PutUint32(params[8:12], backupThreads)
	header = append(header, params[:]...)
	header = append(header, nonce...)
	keyMAC := hmac.New(sha256.New, key)
	_, _ = keyMAC.Write([]byte("key-id"))
	header = append(header, []byte(hex.EncodeToString(keyMAC.Sum(nil)[:4]))...)
	var nameLen [2]byte
	binary.BigEndian.PutUint16(nameLen[:], uint16(len(baseBackup)))
	header = append(header, nameLen[:]...)
	header = append(header, baseBackup...)
	ciphertext := gcm.Seal(nil, nonce, plaintext, header)
	out := append(header, ciphertext...)
	return out, nil
}

// ParseEncryptedBackupHeader parses and bounds-checks an encrypted archive's
// cleartext header. The raw header is retained as AAD for decryption.
func ParseEncryptedBackupHeader(data []byte) (EncryptedBackupHeader, error) {
	if len(data) < backupHeaderFixed || string(data[:5]) != backupHeaderMagic {
		return EncryptedBackupHeader{}, fmt.Errorf("invalid or truncated BKUP1 header")
	}
	mem := binary.BigEndian.Uint32(data[21:25])
	iterations := binary.BigEndian.Uint32(data[25:29])
	threads := binary.BigEndian.Uint32(data[29:33])
	legacy := threads < 1 || threads > 8
	fixed := backupHeaderFixed
	nameOffset := 53
	nonceOffset, keyIDOffset := 33, 45
	if legacy {
		threads = uint32(data[29])
		fixed = 5 + 16 + 4 + 4 + 1 + 12 + 8 + 2
		nameOffset = 50
		nonceOffset, keyIDOffset = 30, 42
	}
	if mem < 8*1024 || mem > 256*1024 || iterations < 1 || iterations > 10 || threads < 1 || threads > 8 {
		return EncryptedBackupHeader{}, fmt.Errorf("invalid BKUP1 Argon2id parameters")
	}
	if len(data) < fixed {
		return EncryptedBackupHeader{}, fmt.Errorf("invalid or truncated BKUP1 header")
	}
	baseLen := int(binary.BigEndian.Uint16(data[nameOffset : nameOffset+2]))
	end := fixed + baseLen
	if end > len(data) || baseLen > 255 {
		return EncryptedBackupHeader{}, fmt.Errorf("invalid BKUP1 base filename length")
	}
	baseBackup := string(data[fixed:end])
	if baseBackup != "" && (filepath.Base(baseBackup) != baseBackup || strings.ContainsAny(baseBackup, "/\\") || strings.Contains(baseBackup, "..") || !utf8.ValidString(baseBackup)) {
		return EncryptedBackupHeader{}, fmt.Errorf("invalid BKUP1 base filename")
	}
	keyID := string(data[keyIDOffset : keyIDOffset+8])
	if _, err := hex.DecodeString(keyID); err != nil || strings.ToLower(keyID) != keyID {
		return EncryptedBackupHeader{}, fmt.Errorf("invalid BKUP1 key id")
	}
	return EncryptedBackupHeader{
		Salt:       append([]byte(nil), data[5:21]...),
		MemoryKiB:  mem,
		Iterations: iterations,
		Threads:    threads,
		Nonce:      append([]byte(nil), data[nonceOffset:nonceOffset+12]...),
		KeyID:      keyID,
		BaseBackup: baseBackup,
		AAD:        append([]byte(nil), data[:end]...),
		Offset:     end,
	}, nil
}

// BackupKeyIDForPassword returns the key id for a password derived using the
// archive header's own salt and Argon2id parameters.
func BackupKeyIDForPassword(password string, header EncryptedBackupHeader) string {
	key := argon2.IDKey([]byte(password), header.Salt, header.Iterations, header.MemoryKiB, uint8(header.Threads), 32)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("key-id"))
	return hex.EncodeToString(mac.Sum(nil)[:4])
}

// ReadEncryptedBackupHeader reads just the cleartext metadata prefix.
func ReadEncryptedBackupHeader(path string) (header EncryptedBackupHeader, retErr error) {
	f, err := os.Open(path)
	if err != nil {
		return EncryptedBackupHeader{}, err
	}
	defer func() {
		if err := f.Close(); retErr == nil && err != nil {
			retErr = fmt.Errorf("closing encrypted backup header: %w", err)
		}
	}()
	magic := make([]byte, len(backupHeaderMagic))
	if _, err := io.ReadFull(f, magic); err != nil || string(magic) != backupHeaderMagic {
		return EncryptedBackupHeader{}, fmt.Errorf("invalid or truncated BKUP1 header")
	}
	data := make([]byte, 33)
	copy(data, magic)
	if _, err := io.ReadFull(f, data[len(magic):]); err != nil {
		return EncryptedBackupHeader{}, fmt.Errorf("invalid or truncated BKUP1 header")
	}
	threads := binary.BigEndian.Uint32(data[29:33])
	baseOffset, fixed := 53, backupHeaderFixed
	if threads < 1 || threads > 8 {
		baseOffset, fixed = 50, 5+16+4+4+1+12+8+2
	}
	data = append(data, make([]byte, fixed-len(data))...)
	if _, err := io.ReadFull(f, data[33:]); err != nil {
		return EncryptedBackupHeader{}, fmt.Errorf("invalid or truncated BKUP1 header")
	}
	baseLen := int(binary.BigEndian.Uint16(data[baseOffset : baseOffset+2]))
	if baseLen > 255 {
		return ParseEncryptedBackupHeader(data[:fixed])
	}
	if baseLen > 0 {
		data = append(data, make([]byte, baseLen)...)
		if _, err := io.ReadFull(f, data[fixed:]); err != nil {
			return EncryptedBackupHeader{}, fmt.Errorf("invalid or truncated BKUP1 header")
		}
	}
	return ParseEncryptedBackupHeader(data)
}

// FileSHA256 returns the SHA-256 digest of an archive file, including its
// encrypted header when present.
func FileSHA256(path string) (digest string, retErr error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() {
		if err := f.Close(); retErr == nil && err != nil {
			retErr = fmt.Errorf("closing backup archive: %w", err)
		}
	}()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// ReadBackupManifest reads the manifest from a verified or unverified backup.
// Call VerifyBackup before using this metadata for restore or retention.
func ReadBackupManifest(path, password string) (manifestResult *Manifest, retErr error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(path, ".enc") {
		data, err = decrypt(data, password)
		if err != nil {
			return nil, fmt.Errorf("decrypting backup manifest: %w", err)
		}
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := gz.Close(); retErr == nil && err != nil {
			retErr = fmt.Errorf("closing backup manifest: %w", err)
		}
	}()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if hdr.Name == "manifest.json" {
			var manifest Manifest
			if err := json.NewDecoder(tr).Decode(&manifest); err != nil {
				return nil, err
			}
			return &manifest, nil
		}
	}
	return nil, fmt.Errorf("backup manifest missing")
}

// decrypt reverses encrypt and continues to accept archives written before
// BKUP1 was introduced.
func decrypt(data []byte, password string) ([]byte, error) {
	if bytes.HasPrefix(data, []byte(backupHeaderMagic)) {
		header, err := ParseEncryptedBackupHeader(data)
		if err != nil {
			return nil, err
		}
		key := argon2.IDKey([]byte(password), header.Salt, header.Iterations, header.MemoryKiB, uint8(header.Threads), 32)
		if !hmac.Equal([]byte(header.KeyID), []byte(BackupKeyIDForPassword(password, header))) {
			return nil, fmt.Errorf("backup password does not match header key id")
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		return gcm.Open(nil, header.Nonce, data[header.Offset:], header.AAD)
	}
	if len(data) < 16+12 {
		return nil, fmt.Errorf("backup too short to be valid")
	}
	salt := data[:16]
	key := argon2Key(password, salt)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	if len(data) < 16+nonceSize {
		return nil, fmt.Errorf("backup truncated")
	}
	nonce := data[16 : 16+nonceSize]
	ciphertext := data[16+nonceSize:]

	return gcm.Open(nil, nonce, ciphertext, nil)
}

// argon2Key derives a 32-byte AES key from a password and salt using Argon2id.
func argon2Key(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, 1, 64*1024, 4, 32)
}

// addDataDir walks dataDir for --include-data, skipping db/ (server.db and
// users.db are always archived on their own) and the backup directory so a
// backup never contains earlier backups.
func addDataDir(addFile func(src, name string), dataDir, backupDir string) error {
	skipDB := filepath.Join(dataDir, "db")
	return filepath.Walk(dataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if path == skipDB || (backupDir != "" && path == filepath.Clean(backupDir)) {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(dataDir, path)
		if relErr != nil {
			return relErr
		}
		addFile(path, filepath.Join("data", rel))
		return nil
	})
}

// orgTempDir creates a scratch directory under {tmp}/{org}/{name}-XXXXXX so
// every temporary path is identifiable and never lands in the bare temp root
// (PART 28 Temporary Directory Structure).
func orgTempDir() (string, error) {
	base := filepath.Join(os.TempDir(), orgName)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	return os.MkdirTemp(base, projectName+"-")
}
