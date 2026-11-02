package server

// Tor CLI-to-running-server control channel (AI.md PART 31.1, "CLI-to-running-
// server control channel"). The server binary owns the embedded Tor process,
// so a separately-invoked "{project_name} tor ..." CLI subcommand cannot touch
// Tor directly — it reaches the running server over these internal, loopback-
// only endpoints instead. They are exactly as internal as /server/metrics
// (PART 20): never in OpenAPI, GraphQL, well-known, or FeaturesInfo, and never
// reachable through /api/{api_version}/**. A request from any non-loopback
// peer gets 404 (not 403) so the endpoints are not discoverable.

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/apimgr/pastebin/src/path"
	"github.com/apimgr/pastebin/src/tor"
)

// torControlLoopbackMiddleware restricts the /server/tor/* control channel to
// requests whose immediate TCP peer is loopback (127.0.0.1/::1) — the same
// trusted set as PART 12's trusted-proxy loopback rule, but with no config-
// driven allowlist extension (Tor control has no legitimate remote caller;
// the CLI always runs on the same host as the server). Any other peer gets a
// bare 404 so the endpoint is not discoverable.
//
// The check reads the peer address preserved by realIPMiddleware, never the
// possibly-rewritten r.RemoteAddr: a request relayed by a trusted proxy can
// carry X-Real-IP: 127.0.0.1, and honouring that would hand the Tor control
// channel to any remote caller behind that proxy.
func torControlLoopbackMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer := peerAddr(r)
		host, _, err := net.SplitHostPort(peer)
		if err != nil {
			host = peer
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// torControlConfig rebuilds the tor.Config used at server startup (see New())
// from the live config plus the server's cached config/data directories, for
// use by handlers that need to inspect Tor settings without a live Manager
// (e.g. validate).
func (s *Server) torControlConfig() tor.Config {
	cfg := s.liveCfg()
	return tor.Config{
		Binary:                    cfg.Server.Tor.Binary,
		UseNetwork:                cfg.Server.Tor.UseNetwork,
		MaxCircuits:               cfg.Server.Tor.MaxCircuits,
		CircuitTimeout:            cfg.Server.Tor.CircuitTimeout,
		BootstrapTimeout:          cfg.Server.Tor.BootstrapTimeout,
		SafeLogging:               cfg.Server.Tor.SafeLogging,
		MaxStreamsPerCircuit:      cfg.Server.Tor.MaxStreamsPerCircuit,
		CloseCircuitOnStreamLimit: cfg.Server.Tor.CloseCircuitOnStreamLimit,
		BandwidthRate:             cfg.Server.Tor.BandwidthRate,
		BandwidthBurst:            cfg.Server.Tor.BandwidthBurst,
		MaxMonthlyBandwidth:       cfg.Server.Tor.MaxMonthlyBandwidth,
		NumIntroPoints:            cfg.Server.Tor.NumIntroPoints,
		VirtualPort:               cfg.Server.Tor.VirtualPort,
		ConfigDir:                 s.configDir,
		DataDir:                   s.dataDir,
		LogDir:                    path.GetLogsDir("pastebin"),
	}
}

// torControlStatus is the internal status payload: the same TorInfo fields
// buildTorInfo produces, plus the vanity-search progress block. The vanity
// block lives only here — never on the public TorInfo, which is embedded in
// FeaturesInfo.
type torControlStatus struct {
	TorInfo
	Vanity tor.VanityStatus `json:"vanity"`
}

// handleTorControlStatus handles GET /server/tor/status (internal, loopback-
// only): returns the same TorInfo shape used by buildTorInfo, so the CLI and
// the server's own status computation never drift apart, plus the vanity
// search state per AI.md PART 31.1 ("Progress").
func (s *Server) handleTorControlStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok": true,
		"data": torControlStatus{
			TorInfo: s.buildTorInfo(),
			Vanity:  s.TorVanityStatus(),
		},
	})
}

// torValidateCheck is one line item of a /server/tor/validate response.
type torValidateCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// handleTorControlValidate handles POST /server/tor/validate (internal,
// loopback-only): checks Tor binary presence, virtual_port range, and
// config/data tor-subdirectory writability, mirroring the CLI's on-disk
// fallback checks (validateTorConfig in main.go) without starting Tor.
func (s *Server) handleTorControlValidate(w http.ResponseWriter, r *http.Request) {
	cfg := s.torControlConfig()
	valid := true
	checks := make([]torValidateCheck, 0, 4)

	if bin := tor.FindBinary(cfg.Binary); bin == "" {
		checks = append(checks, torValidateCheck{
			Name: "binary", Status: "warn",
			Detail: "tor binary not found (hidden service will be disabled, not an error)",
		})
	} else {
		checks = append(checks, torValidateCheck{Name: "binary", Status: "ok", Detail: bin})
	}

	if cfg.VirtualPort <= 0 || cfg.VirtualPort > 65535 {
		checks = append(checks, torValidateCheck{
			Name: "virtual_port", Status: "fail",
			Detail: fmt.Sprintf("virtual_port %d out of range 1-65535", cfg.VirtualPort),
		})
		valid = false
	} else {
		checks = append(checks, torValidateCheck{
			Name: "virtual_port", Status: "ok", Detail: strconv.Itoa(cfg.VirtualPort),
		})
	}

	for _, dir := range []string{filepath.Join(cfg.ConfigDir, "tor"), filepath.Join(cfg.DataDir, "tor")} {
		if err := path.EnsureDir(dir); err != nil {
			checks = append(checks, torValidateCheck{Name: dir, Status: "fail", Detail: err.Error()})
			valid = false
			continue
		}
		checks = append(checks, torValidateCheck{Name: dir, Status: "ok", Detail: "writable"})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok": true,
		"data": map[string]interface{}{
			"valid":  valid,
			"checks": checks,
		},
	})
}

// handleTorControlRestart handles POST /server/tor/restart (internal,
// loopback-only): restarts the server's embedded Tor manager in place.
func (s *Server) handleTorControlRestart(w http.ResponseWriter, r *http.Request) {
	if s.torManager == nil {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"ok": false, "error": "CONFLICT", "message": "tor is not configured on this server",
		})
		return
	}
	if err := s.TorRestart(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"ok": false, "error": "SERVER_ERROR", "message": "tor restart failed",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":   true,
		"data": s.buildTorInfo(),
	})
}

// handleTorControlRegenerate handles POST /server/tor/regenerate (internal,
// loopback-only): deletes the current hidden-service identity and has the
// embedded Tor manager generate a fresh native .onion address.
func (s *Server) handleTorControlRegenerate(w http.ResponseWriter, r *http.Request) {
	if s.torManager == nil {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"ok": false, "error": "CONFLICT", "message": "tor is not configured on this server",
		})
		return
	}
	addr, err := s.TorRegenerateAddress()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"ok": false, "error": "SERVER_ERROR", "message": "tor regenerate failed",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":   true,
		"data": map[string]interface{}{"hostname": addr},
	})
}

// handleTorControlVanityStart handles POST /server/tor/vanity/start (internal,
// loopback-only): starts the in-process vanity .onion address search described
// in AI.md PART 31.1. Body: {"prefix": "abc", "workers": 4}. Only one search
// may run at a time — starting a second one returns 409.
func (s *Server) handleTorControlVanityStart(w http.ResponseWriter, r *http.Request) {
	if s.torManager == nil {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"ok": false, "error": "CONFLICT", "message": "tor is not configured on this server",
		})
		return
	}
	var body struct {
		Prefix  string `json:"prefix"`
		Workers int    `json:"workers"`
	}
	if r.Body != nil {
		defer r.Body.Close()
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	}
	prefix := strings.ToLower(strings.TrimSpace(body.Prefix))
	if err := tor.ValidateVanityPrefix(prefix); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"ok": false, "error": "BAD_REQUEST", "message": err.Error(),
		})
		return
	}
	if current := s.TorVanityStatus(); current.State == "running" {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"ok": false, "error": "CONFLICT",
			"message": fmt.Sprintf("a vanity search is already running for prefix %q", current.Prefix),
			"details": map[string]interface{}{"prefix": current.Prefix},
		})
		return
	}
	status, err := s.TorVanityStart(prefix, body.Workers)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"ok": false, "error": "BAD_REQUEST", "message": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":   true,
		"data": status,
	})
}

// handleTorControlVanityStop handles POST /server/tor/vanity/stop (internal,
// loopback-only): cancels a running vanity search. Stopping when nothing is
// running is a successful no-op; candidates already written to disk are kept.
func (s *Server) handleTorControlVanityStop(w http.ResponseWriter, r *http.Request) {
	if s.torManager == nil {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"ok": false, "error": "CONFLICT", "message": "tor is not configured on this server",
		})
		return
	}
	stopped := s.TorVanityStop()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok": true,
		"data": map[string]interface{}{
			"stopped": stopped,
			"vanity":  s.TorVanityStatus(),
		},
	})
}

// handleTorControlVanityApply handles POST /server/tor/vanity/apply (internal,
// loopback-only): swaps a stored candidate's key files into the live hidden-
// service site directory and restarts Tor. Body: {"address": "..."}, where the
// address may be omitted when exactly one candidate exists and may be any
// unique prefix of a candidate address.
func (s *Server) handleTorControlVanityApply(w http.ResponseWriter, r *http.Request) {
	if s.torManager == nil {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"ok": false, "error": "CONFLICT", "message": "tor is not configured on this server",
		})
		return
	}
	var body struct {
		Address string `json:"address"`
	}
	if r.Body != nil {
		defer r.Body.Close()
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	}
	candidates := tor.ListVanityCandidates(s.dataDir)
	if len(candidates) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{
			"ok": false, "error": "NOT_FOUND", "message": "no vanity candidates found",
		})
		return
	}
	address, err := tor.ResolveVanityCandidate(s.dataDir, strings.TrimSpace(body.Address))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"ok": false, "error": "BAD_REQUEST", "message": err.Error(),
			"details": map[string]interface{}{"candidates": candidates},
		})
		return
	}
	applied, err := s.TorVanityApply(address)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"ok": false, "error": "SERVER_ERROR", "message": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":   true,
		"data": map[string]interface{}{"hostname": applied},
	})
}

// handleTorControlImportKeys handles POST /server/tor/import-keys (internal,
// loopback-only). A JSON body {"path": "..."} names a directory (treated
// exactly like a found vanity candidate, taking the same swap path) or a bare
// secret-key file. Any other content type is accepted as raw secret-key bytes
// so key material can be piped straight in.
func (s *Server) handleTorControlImportKeys(w http.ResponseWriter, r *http.Request) {
	if s.torManager == nil {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"ok": false, "error": "CONFLICT", "message": "tor is not configured on this server",
		})
		return
	}
	if r.Body == nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"ok": false, "error": "BAD_REQUEST", "message": "key path or key data is required",
		})
		return
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil || len(raw) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"ok": false, "error": "BAD_REQUEST", "message": "key path or key data is required",
		})
		return
	}
	if strings.HasPrefix(strings.TrimSpace(r.Header.Get("Content-Type")), "application/json") {
		var body struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(raw, &body); err != nil || strings.TrimSpace(body.Path) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"ok": false, "error": "BAD_REQUEST", "message": "key path is required",
			})
			return
		}
		addr, err := s.TorImportKeyPath(strings.TrimSpace(body.Path))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"ok": false, "error": "BAD_REQUEST", "message": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":   true,
			"data": map[string]interface{}{"hostname": addr},
		})
		return
	}
	addr, err := s.TorApplyKeys(raw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"ok": false, "error": "BAD_REQUEST", "message": "invalid key data",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":   true,
		"data": map[string]interface{}{"hostname": addr},
	})
}
