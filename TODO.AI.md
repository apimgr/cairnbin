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
- [x] PART 32 Client
- [x] PART 33 IDEA.md reference — IDEA.md has the three required sections in order and the project variables; the compat route tables are external-service protocols so they stay
- [ ] PART 33 checklist: it says `.claude/rules` and `.claude/memory` are committed, while PART 2 and the standing instruction keep `.claude/` gitignored and untracked; left untracked per the standing instruction
- [ ] PART 33 checklist: `.claude/memory/` files need YAML frontmatter and no AI.md duplication; only MEMORY.md exists, not re-audited

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
- PART 5 `SafePath` (src/path) validates lowercase `[a-z0-9_-]` segments and
  strips the leading slash, so it cannot validate absolute operator paths
  (`--data`, `--config`) or dotted filenames as the AI.md examples imply.
  `SafeFilePath` is added; backup filenames use a base-name/`..` guard instead.
- PART 6 `server.debug.*`: block implemented (pprof, log_cache, log_bodies, max_body_log_size, profile rates, runtime_endpoints). Still missing: `log_queries` is parsed but no SQL statement logging exists (needs a driver-level hook), and the expvar counters (`requests_total`, `errors_total`, `uptime_seconds`, ...).
- PART 8 startup order: AI.md steps 8a-8i require creating dirs, binding
  privileged ports, spawning Tor with service-user setuid credentials, then
  dropping privileges BEFORE logging, PID file, config load and DB init. The
  code drops privileges inside `server.bindAndDrop` (after DB/config/logging
  already ran as root and were chowned), and Tor/I2P children are started
  without `SysProcAttr.Credential`. Needs a restructure of `src/main.go`.
- PART 8 test helper `TestSelfPIDHelper` (src/pid/pid_internal_test.go) uses
  os.Exit(3)/os.Exit(4), outside the standard exit-code ranges; switch to
  in-range codes and update the parent test's expectations.
- PART 9: `src/common/retry` implements `withRetry`/`isRetryable` and is used by the blocklist/CVE/Trivy-source downloads and the GeoIP download. Still open: the update check and CLI client keep their own retry; cache warming (`config:server`, `security:blocklist`, `geoip:info`) has no consumer yet, so it is not added.
- PART 12 config layout: `encryption_key`, `allowlist`, `blocked_ips`, `abuse_detection`, `blocklists`, `cve`, `trivy` now live under `server.security.*` (legacy `web.security.*` values migrate on load). `allowlist` entries are `{cidr, description}` maps (bare strings still accepted).
- PART 8/CSRF: `POST /pastes` (browser form route) still requires a CSRF token,
  so a plain `curl -d content=... {base}/pastes` is rejected; API/compat routes
  are exempt via `config.CSRFBuiltinExemptPaths`. Confirm this split with the
  PART 16 frontend audit.
- PART 11 MCP follow-ups: operator-only tool mechanism exists but no operator
  API route is annotated yet; audit `source: mcp` is set via request context
  (verify it reaches paste create/delete audit events); `server.mcp.enabled`
  has no `--maintenance`/docs surface beyond config. Output Sanitization Pipeline: stages 2-5 now run on error envelopes in
  httputil.WriteJSON; success bodies are not rewritten (they carry paste content, and stage 1 is the typed
  handler structs); stage 6 constant-time padding exists only for failed auth. Still to review: logging
  sections. Compliance: audit retention minimum is now enforced; the per-standard behaviors tables (session
  timeout, breach alerts, DPO, per-record PHI keys, change approval, etc.) are not implemented — most describe
  features this app lacks (no user sessions, no PHI/cardholder data).
- PART 11 audit events still not emitted (spec "Audit Log Events"):
  `security.ip_allowlisted`/`ip_allowlist_removed` (allowlist is config-only;
  would need a diff on hot reload), `config.smtp_updated`/`ssl_updated`/
  `ssl_expired`/`branding_updated` (config hot-reload does not diff and emit),
  and `server.updated` (emitted by the update flow in a different process
  from the restarted server). `backup.deleted` and
  `config.tor_address_regenerated` are now emitted.
- PART 11 `--maintenance secret rotate` is implemented (src/maintenance/secret.go). Not done: the running server
  keeps the old secret in memory until restart (the command says to restart); the typed-confirmation flow is
  not re-prompting the operator token for root callers, matching the other sensitive operations.
- PART 11 reports: `web.reports.*` limits are now configurable. `nel` and other report groups
  are still accepted and discarded, not logged.
- PART 11 abuse detection: request-flood detection, temporary blocks (auto-release by scheduler) and permanent
  blocks exist; allowlist uses `{cidr, description}`. The spec table also lists API Abuse (throttle), Database
  Anomaly (block) and Config Tampering (rollback) but defines no thresholds, config keys or detection rules for
  them, so they are not implemented. Temporary blocks are in-memory only (lost on restart).
- PART 16 frontend: reduced-motion `!important` and physical left/right CSS
  properties are fixed and enforced by src/server/css_policy_test.go. The
  `/server/*` standard pages, empty states, PWA, themes, branding/SEO, cookie
  banner and footer sections were not individually re-verified. Template policy
  is enforced by src/server/template_policy_test.go.
- PART 17: `email test` is implemented as a bare positional command (like `tor`). Template validation is implemented (src/common/email/validate.go): invalid custom files are logged and ignored in favor of the default. Not done: the deprecated-variable and missing-recommended-section warnings (spec defines no deprecated variables or section lists).
- PART 18: `scheduler list|show|run|enable|disable|history` is now a client command (src/client/scheduler.go) against /api/v1/server/schedulers. The spec writes `{project_name} scheduler ...` but the server flag set (PART 7) is fixed, so it lives on the client; the bordered box layout and `[Run All Now]` are not reproduced, and the show view omits the backup list/retention panel.
- PART 21: no restore API or WebUI exists (spec forbids an admin UI); the 400 VALIDATION_FAILED encrypted-restore API response is therefore not applicable until a restore endpoint is specified.
- PART 24: the spec systemd unit sets `ProtectSystem=strict` with `ReadWritePaths` limited to the four app dirs, but PART 23 has the binary create the service user (useradd/groupadd write `/etc/passwd`, `/etc/group`) at normal startup. Under the spec unit `/etc` is read-only, so first-start account creation can fail when launched by systemd. The unit now matches the spec exactly (previous extras `ReadWritePaths=/etc`, `NoNewPrivileges`, `RuntimeDirectory` removed); confirm the startup path degrades safely (or pre-creates the user during `--service --install`) once PART 8 startup order is done.
- PART 24: Windows service (VSA, `svc.Run`) and launchd/runit/rc.d/OpenRC/SysV templates now generated from `src/service/templates.go` and matched to spec; Windows path not exercised in Docker (needs a Windows runner).
- PART 26: AI.md contradicts itself on dev/test compose identity. The requirements table ("name: and network keep the -dev / -test suffix") conflicts with the Field Reference and the example YAML (same `name:` and network `{project_name}` in all three files). Followed the field reference and examples; revisit if the table is meant to win. Dev volumes omit `:z` (rule text) although the dev example shows `:z`.
- PART 26: remaining project extras (`shadow` package, e2e browser service and E2E_* env in the test compose) are not in IDEA.md; confirm or document them.
- PART 28: tests/e2e (42 tests) and tests/docker.sh / incus.sh were checked for presence and structure only, not re-run or mapped feature-by-feature to IDEA.md. Confirm Universal Coverage items (responsive 375x812, Swagger/GraphiQL pages, MCP tools/call with owner/linked/no token, i18n switching, static asset 200s) each have a named test; run `./tests/e2e.sh` on demand.
- PART 30: not verified line-by-line: hardcoded user-facing strings in Go handlers (`http.Error`/JSON messages must use `errors.*` keys), Swagger/GraphQL/email translation by request language, and the CLI/TUI/GUI `--help` text per language; the reduced-motion `!important` block is tracked under PART 16. `meta.direction`-driven `dir` and logical CSS properties are tracked under PART 16 as well.
- PART 31: Tor and I2P were checked at the config/torrc/lifecycle level only (no live Tor or i2pd run); verify against a real Tor in `tests/incus.sh`: bootstrap, key migration, RegenerateAddress/ApplyKeys, `Log notice file` tor.log permissions, and the I2P Model A tunnels.conf / Model B SAM paths. The 15 outbound-Tor use cases and the app-scoped-only SSRF rule (no inbound-supplied URL ever fetched via the Tor client) were not audited.
- PART 32: reviewed so far: server-side CLI binary hosting and cli_versions, autodiscover envelope, --help/--version/--shell/<command> help, smart argument detection (--file/--text), exit codes 3/5/64, extended --version, cli.yml 0600, create wire format. NOT yet reviewed line by line: CLI auto-update flow end to end (prompt vs update.auto, cli.update_* audit events, re-exec), TUI/GUI mode detection and setup wizard, SSH/Mosh/container rules, theming and responsive layouts, shell completions content, URL-encoding helper usage, Flag-to-Config save rules, token-revoked (401 TOKEN_REVOKED) handling, list `pagination` lacking page/limit. Server also never emits the cli.* audit events.
- PART 11 logging: default `server.logs.level` is now `warn` (schema at the Logging Configuration block). PART 8 startup step 10 says "default log level (info)"; treated as the pre-config interim level, confirm when the startup-order restructure is done.
- PART 16 footer: rows use <div> where AI.md markup uses consecutive <p> rows, and page links should sit in a <nav>; CSS targets the classes today. Align markup.
- PART 16 Themes: spec dark Muted #6272a4 on Background #282a36 is ~3.3:1 (<4.5:1 AA) where used for body-size text; spec palette applied verbatim, review which elements use --color-text-muted and bump weight/size or confirm with spec owner.
- PART 16 Themes: spec file layout (src/server/theme.go, src/client/tui/styles.go, src/client/cli/output.go) differs from current layout; verify TUI/CLI consume TerminalPalette and NO_COLOR via ColorEnabled.
- PART 16 Sitemap: sitemap index (/sitemap-N.xml) for >50000 URLs not implemented (pastebin lists ~12 URLs).
- PART 16 PWA: manifest has no `shortcuts`/`screenshots`, and no iOS `apple-touch-startup-image` splash links (AI.md optional items); icons are generated, so splash/screenshot assets need a generator.
