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
- [ ] PART 6 Application modes
- [ ] PART 7 Binary requirements
- [ ] PART 8 Server binary CLI
- [ ] PART 9 Error handling & caching
- [ ] PART 10 Database
- [ ] PART 11 Security & logging
- [ ] PART 12 Server configuration
- [ ] PART 13 Health & versioning
- [ ] PART 14 API structure
- [ ] PART 15 SSL/TLS & Let's Encrypt
- [ ] PART 16 Web frontend
- [ ] PART 17 Email & notifications
- [ ] PART 18 Scheduler
- [ ] PART 19 GeoIP
- [ ] PART 20 Metrics
- [ ] PART 21 Backup & restore
- [ ] PART 22 Update command
- [ ] PART 23 Privilege escalation & service
- [ ] PART 24 Service support
- [ ] PART 25 Makefile
- [ ] PART 26 Docker
- [ ] PART 27 CI/CD workflows
- [ ] PART 28 Testing & development
- [ ] PART 29 ReadTheDocs documentation
- [ ] PART 30 I18N & A11Y
- [ ] PART 31 Overlay networks (Tor & I2P)
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
