# TODO.AI.md

## Remaining implementation gaps

### PART 28 — Browser E2E feature coverage and runner health

- Complete the IDEA-derived named Tier 1, Tier 2, and Tier 3 matrix for every
  user-facing feature. Current coverage includes the native paste lifecycle,
  browser and no-JavaScript form creation, raw/download/embed/QR views, expiry,
  burn-after-read, owner-token API rejection and management form rendering,
  all listed compat create/read protocols, theme and locale changes, responsive
  layout, CSRF, docs pages, and themed 404/500 responses. Tier 2 also submits
  an unlisted, expiring Go paste and verifies rendered highlighting and that it
  stays out of recent results. Compatibility wire protocols and several other
  IDEA features still lack scenarios in all tiers.
- Crawl every navigable page in all applicable tiers and check every referenced
  asset and browser console result. Tier 1 smoke-requests every registered GET
  route, crawls internal links, and checks referenced assets. Tier 3 now follows
  same-origin links recursively and records console and resource failures;
  routes that are not linked from navigable pages still need browser scenarios.
- Add local test doubles for outbound integrations (including GeoIP, Tor,
  notification delivery, and update/blocklist sources) and exercise them. The
  temporary E2E Compose network is internal-only, with GeoIP and Tor disabled;
  this prevents external access but does not replace local doubles.

## AI.md alignment pass (AI.md is source of truth; code conforms to it)

Work one PART at a time: read the PART slice, compare code/config/docs, fix
deviations directly, then tick the PART. Any deviation that is not fixed in the
same pass gets its own line item here.

- [x] PART 2 License & attribution
- [x] PART 3 Project structure
- [x] PART 4 OS-specific paths
- [x] PART 5 Configuration
- [x] PART 6 Application modes
- [x] PART 7 Binary requirements
- [x] PART 8 Server binary CLI (startup order/privilege drop gap logged below)
- [x] PART 9 Error handling & caching
- [x] PART 10 Database
- [x] PART 11 Security & logging (gaps logged below)
- [x] PART 12 Server configuration (web.security split logged below)
- [x] PART 13 Health & versioning
- [x] PART 14 API structure
- [x] PART 15 SSL/TLS & Let's Encrypt
- [x] PART 16 Web frontend (sampled; gaps logged below)
- [x] PART 17 Email & notifications (gaps logged below)
- [x] PART 18 Scheduler
- [x] PART 19 GeoIP
- [x] PART 20 Metrics
- [x] PART 21 Backup & restore
- [x] PART 22 Update command
- [x] PART 23 Privilege escalation & service
- [x] PART 24 Service support
- [x] PART 25 Makefile
- [x] PART 26 Docker
- [x] PART 27 CI/CD workflows
- [x] PART 28 Testing & development
- [x] PART 29 ReadTheDocs documentation
- [x] PART 30 I18N & A11Y
- [x] PART 31 Overlay networks (Tor & I2P)
- [ ] PART 32 Client
- [ ] PART 33 IDEA.md reference

### Follow-ups found during the alignment pass

- PART 3 lists `golang.org/x/time/rate`, `go-playground/validator/v10`,
  `rs/cors`, `gorilla/websocket`, and `go-co-op/gocron/v2` as required
  libraries; none is imported by `src/` (only x/time and validator appear, as
  indirect deps). Decide per feature in PART 5/11 (rate limit), PART 14 (CORS,
  validation), PART 18 (scheduler) whether the in-house implementations must be
  replaced.
- AI.md PART 3 contradicts itself on `docker/rootfs/` (listed as gitignored and
  as committed overlay). Left committed; entrypoint.sh is required to build.
- PART 4 macOS places security databases at `.../data/security/` for both root
  and user, but the macOS user data dir has no `data/` segment. Callers join
  `{data_dir}/security` directly; add a `security` path helper and migrate
  GeoIP/blocklist/CVE/Trivy callers when auditing PART 19.
- PART 5 config-file booleans: yaml.v3 decodes only YAML 1.1 `yes/no/on/off`
  into `bool` fields; spec requires the full truthy/falsy set (`enable`,
  `oui`, ...) for config values via `config.ParseBool`. Needs a flexible bool
  type or a pre-decode normalization pass across all `bool` fields in
  `src/config/config.go`.
- PART 5 `SafePath` (src/path) validates lowercase `[a-z0-9_-]` segments and
  strips the leading slash, so it cannot validate absolute operator paths
  (`--data`, `--config`) or dotted filenames as the AI.md examples imply.
  `SafeFilePath` is added; backup filenames use a base-name/`..` guard instead.
- PART 6 `server.debug.*` config block (pprof, log_queries, log_cache,
  log_bodies, max_body_log_size, block_profile_rate, mutex_profile_fraction,
  runtime_endpoints) is not implemented; only the fixed on/off profiling rates
  tied to the debug flag exist. Also missing: DB-query and cache-operation
  debug logging (`debugLogDB`, `debugLogCache`) and the expvar counters
  (`requests_total`, `errors_total`, `uptime_seconds`, ...) from the spec.
- PART 8 startup order: AI.md steps 8a-8i require creating dirs, binding
  privileged ports, spawning Tor with service-user setuid credentials, then
  dropping privileges BEFORE logging, PID file, config load and DB init. The
  code drops privileges inside `server.bindAndDrop` (after DB/config/logging
  already ran as root and were chowned), and Tor/I2P children are started
  without `SysProcAttr.Credential`. Needs a restructure of `src/main.go`.
- PART 8 test helper `TestSelfPIDHelper` (src/pid/pid_internal_test.go) uses
  os.Exit(3)/os.Exit(4), outside the standard exit-code ranges; switch to
  in-range codes and update the parent test's expectations.
- PART 9 gaps: no shared `withRetry`/`isRetryable` exponential-backoff helper
  (0s,1s,2s,4s,8s, cap 30s; retry only network/timeout/503) — outbound
  fetches (GeoIP, blocklist, CVE, update check) each use ad-hoc retry; and no
  cache warming (`config:server`, `security:blocklist`, `geoip:info`) at
  startup. Decide the callers before adding either, so neither ships unused.
- PART 12 config layout vs AI.md: the AES-256-GCM `encryption_key` and the
  other secret/abuse/allowlist settings live under `web.security.*`
  (AI.md PART 11: `server.security.*`; only the security.txt fields belong to
  `web.security`). Split `SecurityConfig` accordingly and migrate keys with
  `migrateLegacyKeys`. (`rate_limit` and `compression` already moved to
  `server.*`; the legacy top-level `rate_limit` is still read.)
- PART 8/CSRF: `POST /pastes` (browser form route) still requires a CSRF token,
  so a plain `curl -d content=... {base}/pastes` is rejected; API/compat routes
  are exempt via `config.CSRFBuiltinExemptPaths`. Confirm this split with the
  PART 16 frontend audit.
- PART 11 MCP follow-ups: operator-only tool mechanism exists but no operator
  API route is annotated yet; audit `source: mcp` is set via request context
  (verify it reaches paste create/delete audit events); `server.mcp.enabled`
  has no `--maintenance`/docs surface beyond config. Also PART 11 still to
  review: Output Sanitization Pipeline chokepoint (stages 1-4), logging
  sections, audit log events, compliance modes, abuse detection, IP blocks.
- PART 11 audit events still not emitted (spec "Audit Log Events"):
  `backup.deleted` (retention removals in src/task/backup_guard.go),
  `security.ip_allowlisted`/`ip_allowlist_removed` (allowlist is config-only;
  would need a diff on hot reload), `config.*_updated`/`ssl_expired` (config hot-reload
  does not diff and emit), `server.updated`, and the rotation events below.
- PART 11 `--maintenance secret rotate installation_secret|encryption_key` is
  not implemented (listed in the spec's maintenance subcommands): operator
  re-prompt + audit `security.*_rotated`; installation_secret rotation must
  re-wrap the PGP private key and keep the previous secret 7 days for
  in-flight `{security_id}` URLs; encryption_key rotation must re-encrypt
  `api_tokens.token_enc` and report bodies and keep the previous key 30 days.
- PART 11 reports config (`server.security.reports.rate_limit_per_minute`,
  `rate_limit_per_ip_burst`) is fixed in code (60/min per IP); expose it in
  config. `nel` and other report groups are accepted and discarded, not
  logged.
- PART 11 abuse detection: only the request-flood detector + temporary/permanent
  IP blocks exist (src/server/abuse.go). Not implemented: API-abuse throttling,
  database-anomaly (injection pattern) blocking, config-tampering rollback.
  Allowlist/blocked_ips are plain string lists; AI.md uses `{cidr, description}`
  objects. Temporary blocks are in-memory only (lost on restart).
- PART 16 frontend (sampled via a rule scan, not a line-by-line read): the
  prefers-reduced-motion block in static/css/common.css.tmpl uses
  `!important`, which AI.md allows only in print styles (needs durations driven
  by CSS custom properties instead); physical `margin-left/right` and
  `padding-left/right` remain in ~10 rules (spec: CSS logical properties for
  RTL); the `/server/*` standard pages, empty states, PWA, themes, branding/SEO,
  cookie banner and footer sections were not individually re-verified.
  Template policy (no inline handlers/styles/scripts, no alert/confirm/prompt)
  is now enforced by src/server/template_policy_test.go.
- PART 17: AI.md references `{project_name} email test` (lines 28164, 48828) but the server flag set (PART 7) has no `email` command; resolve by routing it through `--maintenance` or confirming the CLI client owns it. Save-time template validation (unknown variable, empty subject/body) has no caller because there is no admin UI; add a validator for custom files in `{config_dir}/template/email/` at load.
- PART 18: spec lists CLI `scheduler list|show|run|enable|disable|history` (client); no `scheduler` command exists in src/client or the server. Add to the client against the server scheduler API.
- PART 21: no restore API or WebUI exists (spec forbids an admin UI); the 400 VALIDATION_FAILED encrypted-restore API response is therefore not applicable until a restore endpoint is specified.
- PART 24: the spec systemd unit sets `ProtectSystem=strict` with `ReadWritePaths` limited to the four app dirs, but PART 23 has the binary create the service user (useradd/groupadd write `/etc/passwd`, `/etc/group`) at normal startup. Under the spec unit `/etc` is read-only, so first-start account creation can fail when launched by systemd. The unit now matches the spec exactly (previous extras `ReadWritePaths=/etc`, `NoNewPrivileges`, `RuntimeDirectory` removed); confirm the startup path degrades safely (or pre-creates the user during `--service --install`) once PART 8 startup order is done.
- PART 24: Windows service (VSA, `svc.Run`) and launchd/runit/rc.d/OpenRC/SysV templates now generated from `src/service/templates.go` and matched to spec; Windows path not exercised in Docker (needs a Windows runner).
- PART 26: AI.md contradicts itself on dev/test compose identity. The requirements table ("name: and network keep the -dev / -test suffix") conflicts with the Field Reference and the example YAML (same `name:` and network `{project_name}` in all three files). Followed the field reference and examples; revisit if the table is meant to win. Dev volumes omit `:z` (rule text) although the dev example shows `:z`.
- PART 26: remaining project extras (`shadow` package, e2e browser service and E2E_* env in the test compose) are not in IDEA.md; confirm or document them.
- PART 28: tests/e2e (42 tests) and tests/docker.sh / incus.sh were checked for presence and structure only, not re-run or mapped feature-by-feature to IDEA.md. Confirm Universal Coverage items (responsive 375x812, Swagger/GraphiQL pages, MCP tools/call with owner/linked/no token, i18n switching, static asset 200s) each have a named test; run `./tests/e2e.sh` on demand.
- PART 30: not verified line-by-line: hardcoded user-facing strings in Go handlers (`http.Error`/JSON messages must use `errors.*` keys), Swagger/GraphQL/email translation by request language, and the CLI/TUI/GUI `--help` text per language; the reduced-motion `!important` block is tracked under PART 16. `meta.direction`-driven `dir` and logical CSS properties are tracked under PART 16 as well.
- PART 31: Tor and I2P were checked at the config/torrc/lifecycle level only (no live Tor or i2pd run); verify against a real Tor in `tests/incus.sh`: bootstrap, key migration, RegenerateAddress/ApplyKeys, `Log notice file` tor.log permissions, and the I2P Model A tunnels.conf / Model B SAM paths. The 15 outbound-Tor use cases and the app-scoped-only SSRF rule (no inbound-supplied URL ever fetched via the Tor client) were not audited.
