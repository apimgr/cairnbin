package server

import (
	"fmt"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"

	"github.com/go-chi/chi/v5"
)

// cliBinaryName matches the only file names the CLI download route serves:
// {project_name}-cli-{os}-{arch}[.exe] (PART 32 CLI Auto-Update).
var cliBinaryName = regexp.MustCompile(`^pastebin-cli-(linux|darwin|windows|freebsd)-(amd64|arm64)(\.exe)?$`)

// cliBinary describes one distributable CLI build.
type cliBinary struct {
	Path   string
	SHA256 string
}

// cliHashCache memoises SHA-256 digests keyed by path+size+mtime so
// /api/autodiscover never re-hashes an unchanged binary.
var cliHashCache sync.Map

type cliHashEntry struct {
	key string
	sum string
}

func hashCLIBinary(path string, info os.FileInfo) (string, error) {
	key := fmt.Sprintf("%s|%d|%d", path, info.Size(), info.ModTime().UnixNano())
	if v, ok := cliHashCache.Load(path); ok {
		if e := v.(cliHashEntry); e.key == key {
			return e.sum, nil
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	cliHashCache.Store(path, cliHashEntry{key: key, sum: sum})
	return sum, nil
}

// cliBinaryIndex lists the CLI builds this server can hand out, keyed by
// "{os}-{arch}". Sources: files named pastebin-cli-{os}-{arch}[.exe] in
// {data_dir}/cli/binaries/, plus the CLI binary installed beside the running
// server (the container image ships it) for the server's own platform.
func (s *Server) cliBinaryIndex() map[string]cliBinary {
	out := map[string]cliBinary{}
	add := func(path string) {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return
		}
		m := cliBinaryName.FindStringSubmatch(filepath.Base(path))
		if m == nil {
			return
		}
		sum, err := hashCLIBinary(path, info)
		if err != nil {
			return
		}
		out[m[1]+"-"+m[2]] = cliBinary{Path: path, SHA256: sum}
	}
	if entries, err := os.ReadDir(filepath.Join(s.dataDir, "cli", "binaries")); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				add(filepath.Join(s.dataDir, "cli", "binaries", e.Name()))
			}
		}
	}
	if exe, err := os.Executable(); err == nil {
		key := runtime.GOOS + "-" + runtime.GOARCH
		if _, have := out[key]; !have {
			name := "pastebin-cli"
			if runtime.GOOS == "windows" {
				name += ".exe"
			}
			path := filepath.Join(filepath.Dir(exe), name)
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
				if sum, err := hashCLIBinary(path, info); err == nil {
					out[key] = cliBinary{Path: path, SHA256: sum}
				}
			}
		}
	}
	return out
}

// cliVersionsPayload builds the autodiscover cli_versions map.
func (s *Server) cliVersionsPayload() map[string]interface{} {
	payload := map[string]interface{}{}
	for key, b := range s.cliBinaryIndex() {
		payload[key] = map[string]interface{}{"version": s.version, "sha256": b.SHA256}
	}
	return payload
}

// handleCLIBinary streams GET /cli/binaries/{name}. Public by default;
// server.cli.binary_download.require_auth demands a bearer token.
func (s *Server) handleCLIBinary(w http.ResponseWriter, r *http.Request) {
	if s.liveCfg().Server.CLI.BinaryDownload.RequireAuth {
		incoming, _ := TokenFromContext(r.Context())
		if incoming == "" {
			incoming, _ = extractRequestToken(r)
		}
		sum := sha256.Sum256([]byte(incoming))
		if incoming == "" || subtle.ConstantTimeCompare(sum[:], s.operatorTokenHash[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="pastebin"`)
			writeJSON(w, http.StatusUnauthorized, map[string]interface{}{
				"ok": false, "error": "UNAUTHORIZED", "message": "authentication required",
			})
			return
		}
	}
	name := chi.URLParam(r, "name")
	m := cliBinaryName.FindStringSubmatch(name)
	if m == nil {
		http.NotFound(w, r)
		return
	}
	bin, ok := s.cliBinaryIndex()[m[1]+"-"+m[2]]
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(bin.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=300")
	http.ServeContent(w, r, name, info.ModTime(), f)
}
