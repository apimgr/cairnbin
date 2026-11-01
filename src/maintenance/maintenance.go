// Package maintenance implements the --maintenance CLI subcommands:
// backup, restore, mode, setup, and update (alias for --update yes).
package maintenance

import (
	"archive/tar"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/apimgr/pastebin/src/audit"
	"github.com/apimgr/pastebin/src/common/secretbox"
	"github.com/apimgr/pastebin/src/pgp"
)

// ErrComplianceBackupBlocked is returned by Backup when compliance mode
// (server.compliance.enabled) is on and no encryption password was supplied
// (AI.md 28945-28989: "Backups will NOT run unless encryption password is
// set"). Callers can match it with errors.Is to distinguish a compliance
// refusal from any other backup failure.
var ErrComplianceBackupBlocked = errors.New("compliance mode requires an encryption password; backup refused")

const (
	orgName     = "apimgr"
	projectName = "pastebin"
)

// Manifest is embedded in every backup archive as manifest.json.
type Manifest struct {
	Version          string   `json:"version"`
	CreatedAt        string   `json:"created_at"`
	AppVersion       string   `json:"app_version"`
	CreatedBy        string   `json:"created_by"`
	Contents         []string `json:"contents"`
	Encrypted        bool     `json:"encrypted"`
	EncryptionMethod string   `json:"encryption_method,omitempty"`
	Checksum         string   `json:"checksum"`
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
	addDir := func(dir, prefix string) {
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(dir, path)
			addFile(path, filepath.Join(prefix, rel))
			return nil
		})
	}

	addFile(filepath.Join(opts.ConfigDir, "server.yml"), "server.yml")
	addFile(filepath.Join(opts.DataDir, "db", "server.db"), "server.db")
	addFile(filepath.Join(opts.DataDir, "db", "users.db"), "users.db")
	addDir(filepath.Join(opts.ConfigDir, "template"), "template")
	addDir(filepath.Join(opts.ConfigDir, "theme"), "theme")
	// Security keypair state (PART, AI.md 14206-14209): public key, encrypted
	// private key, parked rotated key, and per-keyserver publish state. The
	// private key's KDF input is installation_secret, which lives in server.db
	// (already archived), so the backup is self-sufficient to restore.
	addDir(filepath.Join(opts.ConfigDir, "security"), "security")

	// Compute content checksum: SHA-256 of all data file bytes in archive order,
	// excluding the manifest. This avoids the circular-dependency that would arise
	// from hashing an archive that itself contains the checksum.
	contentHash := sha256.New()
	for _, e := range entries {
		b, readErr := os.ReadFile(e.src)
		if readErr == nil {
			contentHash.Write(b)
		}
	}

	// Build and add manifest (with checksum already set — single archive pass).
	hostname, _ := os.Hostname()
	manifest := Manifest{
		Version:    "1.0.0",
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
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

	tw.Close()
	gz.Close()

	archiveBytes := archiveBuf.Bytes()

	var finalBytes []byte
	if opts.Password != "" {
		finalBytes, err = encrypt(archiveBytes, opts.Password)
		if err != nil {
			return fmt.Errorf("encrypting backup: %w", err)
		}
	} else {
		finalBytes = archiveBytes
	}

	if err := os.WriteFile(destPath, finalBytes, 0o600); err != nil {
		return fmt.Errorf("writing backup file: %w", err)
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
			manifestGzip.Close()
			return fmt.Errorf("reading backup manifest: %w", readErr)
		}
		if hdr.Name == "manifest.json" {
			if err := json.NewDecoder(manifestTar).Decode(&manifest); err != nil {
				manifestGzip.Close()
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
	defer gr.Close()

	tr := tar.NewReader(gr)
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
		if err := extractEntry(tr, hdr, configDir, dataDir); err != nil {
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
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
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
	fmt.Println("the server with the PASTEBIN_RESET_ADMIN=1 environment variable.")
	fmt.Printf("Config directory: %s\n", configDir)
	return nil
}

// PrintHelp prints the --maintenance subcommand help.
func PrintHelp(binaryName string) {
	fmt.Printf(`Maintenance operations: %s --maintenance <command>

Commands:
  backup [filename]   Create a backup of config and database files
  backup test         Dry-run verify the security keypair decrypts
  restore <file>      Restore from a backup file
  update              Update the binary to the latest release (alias: --update yes)
  mode <mode>         Set the server mode (production|development)
  setup               Reset admin credentials / initial setup
  pgp <action>        Manage the project security keypair (see below)
  token <action>      Manage API tokens (see below)
  data <action> <prefix>  Data-subject export/delete (see below)
  compliance report   Print the compliance status summary
  --help              Show this help

PGP keypair actions (--maintenance pgp <action>):
  generate            Create the keypair and publish to keyservers
  rotate              Roll to a new keypair, cross-signed by the old key
  publish             Re-publish the public key to configured keyservers
  export public [path]   Write the public key to path (or stdout)
  export private <path>  Export the private key (operator auth, 1/hour)
  import <file>       Import a private key (operator auth, identity checked)
  delete              Delete the keypair and disable PGP publishing

Token actions (--maintenance token <action> [prefix]):
  list                List all API tokens (prefix, resource, expiry)
  revoke <prefix>     Revoke the token matching prefix

Data actions (--maintenance data <action> <prefix>) — requires root or the
operator token; prefix identifies the owner token (see token list):
  export <prefix>     Print the token and paste record as JSON (GDPR/CCPA export)
  delete <prefix>     Delete the paste and revoke the token (right to erasure)

Examples:
  %s --maintenance backup
  %s --maintenance backup mybackup.tar.gz
  %s --maintenance backup test
  %s --maintenance restore pastebin_backup_2025-01-01_120000.tar.gz
  %s --maintenance mode development
  %s --maintenance update
  %s --maintenance pgp generate
  %s --maintenance pgp export public /tmp/pgp.pub.asc
  %s --maintenance token list
  %s --maintenance token revoke tok_abc123
  %s --maintenance data export tok_abc123
  %s --maintenance data delete tok_abc123
  %s --maintenance compliance report
`, binaryName, binaryName, binaryName, binaryName, binaryName, binaryName, binaryName, binaryName, binaryName, binaryName, binaryName, binaryName, binaryName, binaryName)
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
	checkPath := strings.TrimSuffix(path, ".tmp")
	if strings.HasSuffix(checkPath, ".enc") {
		if password == "" {
			return fmt.Errorf("verify: encrypted backup requires password")
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
	defer gr.Close()

	tr := tar.NewReader(gr)

	// Check 4 + 5 + 6: walk entries in a single pass.
	// Accumulate SHA-256 of all non-manifest content bytes to verify against
	// the checksum field embedded in the manifest (same algorithm as Backup).
	contentHash := sha256.New()
	var manifestFound bool
	var manifestChecksum string
	tmpDir, err := os.MkdirTemp("", "pastebin-verify-*")
	if err != nil {
		return fmt.Errorf("verify: temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

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
			var m Manifest
			if jsonErr := json.NewDecoder(tr).Decode(&m); jsonErr != nil {
				return fmt.Errorf("verify: invalid manifest: %w", jsonErr)
			}
			manifestChecksum = m.Checksum
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
			f.Close()
			return fmt.Errorf("verify: read entry %s: %w", hdr.Name, err)
		}
		f.Close()

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
	defer f.Close()

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
	defer f.Close()

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
	// Map archive names to destination paths.
	var dest string
	switch hdr.Name {
	case "server.yml":
		dest = filepath.Join(configDir, "server.yml")
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
	defer f.Close()

	// O_CREATE mode only applies to new files — tighten pre-existing ones too.
	if err := f.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", dest, err)
	}

	// 256 MiB cap per entry
	lr := io.LimitReader(tr, 256<<20)
	_, err = io.Copy(f, lr)
	return err
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

// encrypt encrypts data using AES-256-GCM with a key derived via Argon2id.
// Output format: [16-byte salt][12-byte nonce][ciphertext+tag].
func encrypt(plaintext []byte, password string) ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	key := argon2Key(password, salt)

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

	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)

	out := make([]byte, 0, 16+len(nonce)+len(ciphertext))
	out = append(out, salt...)
	out = append(out, nonce...)
	out = append(out, ciphertext...)
	return out, nil
}

// decrypt reverses encrypt.
func decrypt(data []byte, password string) ([]byte, error) {
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
