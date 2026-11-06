package tor

// In-process vanity .onion address search (AI.md PART 31.1, "Vanity Onion
// Address Search"). The server brute-forces v3 hidden-service keypairs itself
// — the same approach mkp224o takes — so the feature has no external tool
// dependency. Candidates are written to {data_dir}/tor/vanity/{address}/ in
// Tor's own on-disk formats and are only swapped into the live site directory
// by an explicit apply.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha3"
	"crypto/sha512"
	"encoding/base32"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// VanityMaxPrefixLen is the longest prefix this in-process search accepts.
	// Beyond six characters a CPU search is impractical and the operator is
	// pointed at an external GPU tool instead.
	VanityMaxPrefixLen = 6
	// vanityCharset is the onion-address base32 alphabet, the only characters
	// that can ever appear in a v3 .onion address.
	vanityCharset = "abcdefghijklmnopqrstuvwxyz234567"
	// vanitySampleSize is how many attempts a worker makes between flushes of
	// its local counter into the shared atomic total, keeping the progress
	// figure approximate but nearly free.
	vanitySampleSize = 256
	// vanityStateIdle means no search is running and no candidate is waiting.
	vanityStateIdle = "idle"
	// vanityStateRunning means a search is currently running.
	vanityStateRunning = "running"
	// vanityStateFound means at least one candidate is waiting on disk.
	vanityStateFound = "found"
)

// nativePublicKeyHeader is the fixed 32-byte header Tor prepends to a native
// on-disk ed25519 hidden-service public-key file.
var nativePublicKeyHeader = []byte("== ed25519v1-public: type0 ==\x00\x00\x00")

// vanityBase32 is the unpadded base32 encoding used by v3 onion addresses.
var vanityBase32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// VanityStatus reports the progress of the vanity search plus any candidates
// waiting on disk, as surfaced by "tor status" and /server/tor/status.
type VanityStatus struct {
	State          string   `json:"state"`
	Prefix         string   `json:"prefix,omitempty"`
	Workers        int      `json:"workers,omitempty"`
	Attempts       uint64   `json:"attempts,omitempty"`
	Rate           float64  `json:"rate,omitempty"`
	ElapsedSeconds float64  `json:"elapsed_seconds,omitempty"`
	Candidates     []string `json:"candidates,omitempty"`
}

// vanitySearcher owns the state of the single allowed in-process search.
type vanitySearcher struct {
	mu       sync.Mutex
	running  bool
	prefix   string
	workers  int
	started  time.Time
	stopped  time.Time
	cancel   context.CancelFunc
	done     chan struct{}
	attempts atomic.Uint64
}

// OnionAddressFromPublicKey derives the v3 .onion address for an ed25519
// public key per rend-spec-v3: base32(pubkey ‖ checksum ‖ version) where
// checksum is the first two bytes of SHA3-256(".onion checksum" ‖ pubkey ‖
// version) and version is 0x03.
func OnionAddressFromPublicKey(pub ed25519.PublicKey) string {
	const version = byte(0x03)
	checksumInput := make([]byte, 0, len(".onion checksum")+ed25519.PublicKeySize+1)
	checksumInput = append(checksumInput, ".onion checksum"...)
	checksumInput = append(checksumInput, pub...)
	checksumInput = append(checksumInput, version)
	sum := sha3.Sum256(checksumInput)
	blob := make([]byte, 0, ed25519.PublicKeySize+3)
	blob = append(blob, pub...)
	blob = append(blob, sum[0], sum[1])
	blob = append(blob, version)
	return strings.ToLower(vanityBase32.EncodeToString(blob)) + ".onion"
}

// expandedSecretKey converts an ed25519 seed into the 64-byte expanded
// private key Tor stores on disk (clamped SHA-512 of the seed).
func expandedSecretKey(seed []byte) []byte {
	h := sha512.Sum512(seed)
	h[0] &= 248
	h[31] &= 127
	h[31] |= 64
	out := make([]byte, legacyKeyBlobLen)
	copy(out, h[:])
	return out
}

// ValidateVanityPrefix checks a requested prefix against the onion base32
// alphabet and the in-process length ceiling, returning a clear error before
// any worker is started.
func ValidateVanityPrefix(prefix string) error {
	if prefix == "" {
		return fmt.Errorf("prefix is required")
	}
	for _, r := range prefix {
		if !strings.ContainsRune(vanityCharset, r) {
			return fmt.Errorf("prefix must use only onion base32 characters (a-z, 2-7)")
		}
	}
	if len(prefix) > VanityMaxPrefixLen {
		return fmt.Errorf("prefix longer than %d characters is impractical to search on CPU — generate it with an external GPU tool such as mkp224o and install the result with \"tor import-keys <path>\"", VanityMaxPrefixLen)
	}
	return nil
}

// NormalizeVanityWorkers resolves the requested worker count: zero or less
// means the default of logical CPUs minus one (at least one), and anything
// above the number of logical CPUs is rejected.
func NormalizeVanityWorkers(requested int) (int, error) {
	maxWorkers := runtime.NumCPU()
	if maxWorkers < 1 {
		maxWorkers = 1
	}
	if requested <= 0 {
		def := maxWorkers - 1
		if def < 1 {
			def = 1
		}
		return def, nil
	}
	if requested > maxWorkers {
		return 0, fmt.Errorf("workers must be between 1 and %d", maxWorkers)
	}
	return requested, nil
}

// vanityDir returns the candidate storage directory for this manager.
func (m *Manager) vanityDir() string {
	return filepath.Join(m.cfg.DataDir, "tor", "vanity")
}

// VanityStart validates the request and launches the background search.
// Returns an error if a search is already running, per the spec's
// one-search-at-a-time rule.
func (m *Manager) VanityStart(prefix string, workers int) (VanityStatus, error) {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if err := ValidateVanityPrefix(prefix); err != nil {
		return VanityStatus{}, err
	}
	resolved, err := NormalizeVanityWorkers(workers)
	if err != nil {
		return VanityStatus{}, err
	}
	dir := m.vanityDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return VanityStatus{}, fmt.Errorf("create vanity dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return VanityStatus{}, fmt.Errorf("chmod vanity dir: %w", err)
	}
	if err := m.vanity.start(prefix, resolved, dir); err != nil {
		return VanityStatus{}, err
	}
	return m.VanityStatus(), nil
}

// VanityStop cancels a running search. Returns false when nothing was
// running; candidates already written to disk are always kept.
func (m *Manager) VanityStop() bool {
	return m.vanity.stop()
}

// VanityStatus reports search progress plus the candidates waiting on disk.
func (m *Manager) VanityStatus() VanityStatus {
	st := m.vanity.status()
	st.Candidates = ListVanityCandidates(m.cfg.DataDir)
	if st.State != vanityStateRunning {
		if len(st.Candidates) > 0 {
			st.State = vanityStateFound
		} else {
			st.State = vanityStateIdle
		}
	}
	return st
}

// RunningVanityPrefix returns the prefix of the currently running search, or
// an empty string when no search is running.
func (m *Manager) RunningVanityPrefix() string {
	st := m.vanity.status()
	if st.State != vanityStateRunning {
		return ""
	}
	return st.Prefix
}

// start launches the worker pool for a new search.
func (v *vanitySearcher) start(prefix string, workers int, dir string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.running {
		return fmt.Errorf("a vanity search is already running for prefix %q", v.prefix)
	}
	ctx, cancel := context.WithCancel(context.Background())
	v.running = true
	v.prefix = prefix
	v.workers = workers
	v.started = time.Now()
	v.stopped = time.Time{}
	v.cancel = cancel
	v.done = make(chan struct{})
	v.attempts.Store(0)
	go v.run(ctx, cancel, prefix, workers, dir, v.done)
	return nil
}

// run supervises the worker pool and records completion state.
func (v *vanitySearcher) run(ctx context.Context, cancel context.CancelFunc, prefix string, workers int, dir string, done chan struct{}) {
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v.worker(ctx, prefix, dir, cancel)
		}()
	}
	wg.Wait()
	cancel()
	v.mu.Lock()
	v.running = false
	v.stopped = time.Now()
	v.cancel = nil
	v.mu.Unlock()
	close(done)
}

// worker brute-forces keypairs until the context is cancelled or a match is
// found. On a match it stores the candidate and cancels the whole search.
func (v *vanitySearcher) worker(ctx context.Context, prefix, dir string, cancel context.CancelFunc) {
	seed := make([]byte, ed25519.SeedSize)
	local := uint64(0)
	for {
		select {
		case <-ctx.Done():
			v.attempts.Add(local)
			return
		default:
		}
		for i := 0; i < vanitySampleSize; i++ {
			if _, err := rand.Read(seed); err != nil {
				v.attempts.Add(local)
				log.Printf("Tor: vanity search: entropy unavailable: %v", err)
				cancel()
				return
			}
			priv := ed25519.NewKeyFromSeed(seed)
			pub, ok := priv.Public().(ed25519.PublicKey)
			if !ok {
				v.attempts.Add(local)
				cancel()
				return
			}
			local++
			address := OnionAddressFromPublicKey(pub)
			if !strings.HasPrefix(address, prefix) {
				continue
			}
			v.attempts.Add(local)
			if err := writeVanityCandidate(dir, address, pub, expandedSecretKey(seed)); err != nil {
				log.Printf("Tor: vanity search: could not store candidate: %v", err)
			} else {
				log.Printf("Tor: vanity search found %s", address)
			}
			cancel()
			return
		}
		v.attempts.Add(local)
		local = 0
	}
}

// stop cancels a running search and waits for its workers to exit.
func (v *vanitySearcher) stop() bool {
	v.mu.Lock()
	if !v.running || v.cancel == nil {
		v.mu.Unlock()
		return false
	}
	cancel := v.cancel
	done := v.done
	v.mu.Unlock()
	cancel()
	if done != nil {
		<-done
	}
	return true
}

// status snapshots the current or most recent search progress.
func (v *vanitySearcher) status() VanityStatus {
	v.mu.Lock()
	defer v.mu.Unlock()
	st := VanityStatus{State: vanityStateIdle}
	if v.started.IsZero() {
		return st
	}
	end := v.stopped
	if v.running {
		st.State = vanityStateRunning
		end = time.Now()
	}
	elapsed := end.Sub(v.started).Seconds()
	if elapsed < 0 {
		elapsed = 0
	}
	st.Prefix = v.prefix
	st.Workers = v.workers
	st.Attempts = v.attempts.Load()
	st.ElapsedSeconds = elapsed
	if elapsed > 0 {
		st.Rate = float64(st.Attempts) / elapsed
	}
	return st
}

// writeVanityCandidate stores a found keypair under {dir}/{address}/ in Tor's
// own on-disk formats. The directory is built in a temporary location and
// renamed into place so a reader never sees a partially written candidate.
// Secret key material is never logged — only the address.
func writeVanityCandidate(dir, address string, pub ed25519.PublicKey, secret []byte) error {
	final := filepath.Join(dir, address)
	if _, err := os.Stat(final); err == nil {
		return nil
	}
	tmp, err := os.MkdirTemp(dir, ".candidate-")
	if err != nil {
		return fmt.Errorf("create temp candidate dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := os.Chmod(tmp, 0o700); err != nil {
		return fmt.Errorf("chmod temp candidate dir: %w", err)
	}
	files := map[string][]byte{
		"hs_ed25519_secret_key": append(append([]byte{}, nativeKeyHeader...), secret...),
		"hs_ed25519_public_key": append(append([]byte{}, nativePublicKeyHeader...), pub...),
		"hostname":              []byte(address + "\n"),
	}
	for name, content := range files {
		if err := writeSecureTorFile(filepath.Join(tmp, name), content); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	if err := os.Rename(tmp, final); err != nil {
		if _, statErr := os.Stat(final); statErr == nil {
			return nil
		}
		return fmt.Errorf("publish candidate dir: %w", err)
	}
	return nil
}

// ListVanityCandidates returns the sorted addresses of every complete
// candidate stored under {data_dir}/tor/vanity/.
func ListVanityCandidates(dataDir string) []string {
	dir := filepath.Join(dataDir, "tor", "vanity")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	candidates := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "hs_ed25519_secret_key")); err != nil {
			continue
		}
		candidates = append(candidates, e.Name())
	}
	sort.Strings(candidates)
	return candidates
}

// ResolveVanityCandidate maps a user-supplied argument onto exactly one
// stored candidate. An empty argument is accepted when exactly one candidate
// exists; any unique prefix of a candidate address is also accepted.
func ResolveVanityCandidate(dataDir, arg string) (string, error) {
	candidates := ListVanityCandidates(dataDir)
	if len(candidates) == 0 {
		return "", fmt.Errorf("no vanity candidates found in %s", filepath.Join(dataDir, "tor", "vanity"))
	}
	arg = strings.ToLower(strings.TrimSpace(arg))
	if arg == "" {
		if len(candidates) == 1 {
			return candidates[0], nil
		}
		return "", fmt.Errorf("multiple vanity candidates available, specify one: %s", strings.Join(candidates, ", "))
	}
	matches := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if strings.HasPrefix(c, arg) {
			matches = append(matches, c)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("no vanity candidate matches %q, available: %s", arg, strings.Join(candidates, ", "))
	default:
		return "", fmt.Errorf("%q matches multiple vanity candidates: %s", arg, strings.Join(matches, ", "))
	}
}

// normalizeSecretKeyBlob accepts either Tor's native 96-byte secret-key file
// or a bare 64-byte expanded key and returns the native form.
func normalizeSecretKeyBlob(data []byte) ([]byte, error) {
	switch len(data) {
	case nativeKeyFileLen:
		if !strings.HasPrefix(string(data), string(nativeKeyHeader)) {
			return nil, fmt.Errorf("secret key file has an unrecognized header")
		}
		return data, nil
	case legacyKeyBlobLen:
		return append(append([]byte{}, nativeKeyHeader...), data...), nil
	default:
		return nil, fmt.Errorf("expected a %d-byte native or %d-byte legacy ed25519 secret key, got %d bytes", nativeKeyFileLen, legacyKeyBlobLen, len(data))
	}
}

// VanityApply swaps a stored candidate into the live hidden-service site
// directory and restarts Tor, verifying that the published hostname matches
// the candidate before the candidate directory is removed.
func (m *Manager) VanityApply(arg string) (string, error) {
	address, err := ResolveVanityCandidate(m.cfg.DataDir, arg)
	if err != nil {
		return "", err
	}
	return m.applyKeyDir(filepath.Join(m.vanityDir(), address), address, true)
}

// ImportKeyPath installs hidden-service key material from a path. A directory
// is treated exactly like a found vanity candidate (same swap path); a single
// file is treated as a bare secret-key file.
func (m *Manager) ImportKeyPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("import path is required")
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("import path: %w", err)
	}
	if info.IsDir() {
		expected := ""
		if data, readErr := os.ReadFile(filepath.Join(p, "hostname")); readErr == nil {
			expected = strings.ToLower(strings.TrimSpace(string(data)))
		}
		return m.applyKeyDir(p, expected, false)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("read key file: %w", err)
	}
	native, err := normalizeSecretKeyBlob(data)
	if err != nil {
		return "", err
	}
	return m.ApplyKeys(native)
}

// applyKeyDir performs the destructive swap: stop Tor, delete the old site
// keys, move the candidate's files in, start Tor, and verify the published
// hostname. On a mismatch it fails loudly and leaves the candidate in place.
func (m *Manager) applyKeyDir(dir, expected string, removeSource bool) (string, error) {
	secret, err := os.ReadFile(filepath.Join(dir, "hs_ed25519_secret_key"))
	if err != nil {
		return "", fmt.Errorf("read candidate secret key: %w", err)
	}
	native, err := normalizeSecretKeyBlob(secret)
	if err != nil {
		return "", err
	}
	public, publicErr := os.ReadFile(filepath.Join(dir, "hs_ed25519_public_key"))
	hostname, hostnameErr := os.ReadFile(filepath.Join(dir, "hostname"))

	m.mu.Lock()
	siteDir := filepath.Join(m.cfg.DataDir, "tor", "site")
	if err := os.MkdirAll(siteDir, 0o700); err != nil {
		m.mu.Unlock()
		return "", fmt.Errorf("create site dir: %w", err)
	}
	m.closeSvcLocked()
	for _, name := range []string{"hs_ed25519_secret_key", "hs_ed25519_public_key", "hostname"} {
		if err := os.Remove(filepath.Join(siteDir, name)); err != nil && !os.IsNotExist(err) {
			m.mu.Unlock()
			return "", fmt.Errorf("remove old %s: %w", name, err)
		}
	}
	if err := writeSecureTorFile(filepath.Join(siteDir, "hs_ed25519_secret_key"), native); err != nil {
		m.mu.Unlock()
		return "", fmt.Errorf("install secret key: %w", err)
	}
	if publicErr == nil {
		if err := writeSecureTorFile(filepath.Join(siteDir, "hs_ed25519_public_key"), public); err != nil {
			m.mu.Unlock()
			return "", fmt.Errorf("install public key: %w", err)
		}
	}
	if hostnameErr == nil {
		if err := writeSecureTorFile(filepath.Join(siteDir, "hostname"), hostname); err != nil {
			m.mu.Unlock()
			return "", fmt.Errorf("install hostname: %w", err)
		}
	}
	startErr := m.startLocked()
	published := ""
	if m.svc != nil {
		published = m.svc.onionAddress
	}
	m.mu.Unlock()
	if startErr != nil {
		return "", startErr
	}
	if expected != "" && published != "" && published != expected {
		log.Printf("ERROR: Tor: published hidden-service address %s does not match applied identity %s", published, expected)
		return "", fmt.Errorf("published address %s does not match applied identity %s", published, expected)
	}
	if removeSource {
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("Tor: warning: could not remove applied candidate dir: %v", err)
		}
	}
	if published == "" {
		return expected, nil
	}
	return published, nil
}
