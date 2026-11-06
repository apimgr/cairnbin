# Testing, Documentation & I18N Rules (PART 28, 29, 30)

⚠️ **These rules are NON-NEGOTIABLE. Violations are bugs.** ⚠️

## CRITICAL - NEVER DO

- NEVER run binaries, `go build`, or `go test` directly on the local machine — the local machine has no Go, everything runs in Docker/Incus
- NEVER use the project directory for test/runtime data — all runtime/test data goes to `/tmp/{project_org}/{internal_name}-XXXXXX/`
- NEVER use bare `/tmp` root, `mktemp -d` without org prefix, or generic paths like `/tmp/test-data`
- NEVER run `docker compose up` with `docker/docker-compose.yml` or `docker/docker-compose.dev.yml` (human-only, production/dev data) — AI may only use `docker/docker-compose.test.yml`
- NEVER mount `./volumes/`, `./docker/rootfs/`, or any project-directory path as a runtime volume
- NEVER create or modify files in the project directory during testing
- NEVER put config files (`server.yml`, `cli.yml`, torrc) in the repository — they are runtime-generated only
- NEVER use `pkill -f`, `pkill {name}` without `-x`, `killall`, `kill -9` as first resort, `docker kill`, or any `docker *prune`/`rm $(docker ps -aq)` style broad-sweep command
- NEVER delete user data directories, config files, database files, SSL certs, or git repos without confirmation
- NEVER run `rm -rf /`, `rm -rf ~`, `rm -rf .`, `rm -rf *`
- NEVER defer unit tests to the end — create/update `*_test.go` in the same work pass as the logic change
- NEVER put full HTTP requests, DB operations, external services, or auth flows in Go unit tests — those belong in `./tests/*.sh`
- NEVER put other files in `docs/` besides ReadTheDocs/MkDocs documentation — source goes in `src/`, scripts in `scripts/`
- NEVER hardcode a user-facing string anywhere (web, API, Swagger, GraphQL, email, CLI, agent, health page, cookie consent, legal pages) — every string MUST use a translation key
- NEVER let an unsupported `--lang`/`Accept-Language`/`?lang=` value error or crash — always silently fall back to `en`
- NEVER omit a key from a non-English locale file — every key in `en.json` MUST exist in all other language files (build-time enforced)
- NEVER convey information by color alone in UI
- NEVER move focus on toast/notification announcements — use `aria-live` instead

## CRITICAL - ALWAYS DO

- ALWAYS run host-affecting test steps (systemd, iptables, mount, reboot, package install) inside a container/VM/namespace, never on the host
- ALWAYS use `/tmp/{project_org}/{internal_name}-XXXXXX/` structure for all temp/test directories
- ALWAYS build via Docker (`casjaysdev/go:latest`) with `-e GOFLAGS=-buildvcs=false`, output to `binaries/`
- ALWAYS run Phase 1 (`make test`) for code changes; Phase 2 (`./tests/run_tests.sh`) is manual, developer-initiated binary validation unless the task calls for end-to-end verification
- ALWAYS treat `./tests/e2e.sh` as a standalone browser suite; it is not dispatched by `./tests/run_tests.sh`
- ALWAYS create/update the matching `*_test.go` immediately when adding or changing package logic
- ALWAYS test every route with all applicable Accept headers (frontend: `text/html` + `text/plain`; API: `application/json` + `text/plain`) and all `.txt` endpoints
- ALWAYS test full CRUD when a project has CRUD operations
- ALWAYS achieve ≥60% Go code coverage (`go test -cover`) and 100% endpoint/route coverage
- ALWAYS use `trap` for cleanup in test scripts; exit 0 on success, non-zero on failure
- ALWAYS identify exact PID/container/image before killing/removing, and scope operations to the exact project name/tag
- ALWAYS host documentation on ReadTheDocs using MkDocs Material theme with dark/light/auto switching
- ALWAYS keep `docs/` in sync with the shipped product's actual behavior (browser, CLI, API, config, public discovery surfaces)
- ALWAYS maintain WCAG AA contrast (4.5:1 minimum) if customizing docs theme colors, in both light and dark themes
- ALWAYS route every human-readable string through `t()`/`{{t .Lang key}}`
- ALWAYS use the fallback chain `?lang= → cookie → Accept-Language → en` (web) or `--lang flag → config → LANG/LC_ALL → en` (CLI/server)
- ALWAYS fall back to English when a translation key is missing in the active language
- ALWAYS run `make i18n-validate` after adding/changing translations
- ALWAYS set `dir="rtl"` for Arabic and use CSS logical properties (`margin-inline-start`, not `margin-left`)
- ALWAYS meet WCAG 2.1 AA: keyboard navigation, screen reader support, 4.5:1 text contrast, visible focus indicators, 44x44px touch targets
- ALWAYS include skip links as the first focusable elements on every page

## Key Rules Summary

### Test Phases
| Phase | Files | Run With | Coverage Target |
|-------|-------|----------|------------------|
| Phase 1 — Toolchain Gate | `*_test.go` | `make test` | ≥60% code coverage (`go test -cover`) |
| Phase 2 — Binary Validation | `./tests/*.sh` | `./tests/run_tests.sh` (manual) | 100% endpoint/route coverage |

### Required Test Scripts (`tests/` dir, executable, WTFPL license header)
| Script | Purpose |
|--------|---------|
| `tests/run_tests.sh` | Auto-detects incus/docker, dispatches |
| `tests/docker.sh` | Full integration tests, Docker Alpine |
| `tests/incus.sh` | Full integration + systemd tests, Debian |
| `tests/e2e.sh` | Standalone browser testing with headless Chromium; run separately |

### Container Images
| Purpose | Image |
|---------|-------|
| Building Go | `casjaysdev/go:latest` |
| Container testing | `alpine:latest` |
| Full OS testing | `debian:latest` (Incus, preferred: `images:debian/trixie`) |

### Temp Directory Pattern
`/tmp/{project_org}/{internal_name}-XXXXXX/` — the ONLY acceptable pattern. Subpaths: `volumes/config/`, `volumes/data/`.

### Docs Structure (ReadTheDocs / MkDocs, `docs/` dir)
| File | Required |
|------|:--------:|
| `mkdocs.yml`, `.readthedocs.yaml` (root) | ✓ |
| `docs/index.md` | ✓ |
| `docs/installation.md` | ✓ |
| `docs/configuration.md` | ✓ |
| `docs/api.md` | ✓ |
| `docs/cli.md` | If applicable |
| `docs/security.md` | ✓ |
| `docs/integrations.md` | ✓ |
| `docs/development.md` | ✓ |
| `docs/requirements.txt` | ✓ |
| `docs/stylesheets/dark.css`, `light.css` | Optional |

RTD URL formats: `{project_org}-{project_name}.readthedocs.io`, `{project_name}.readthedocs.io`, or custom domain.

### I18N — Supported Languages (all binaries, no partial support)
| Code | Language | Direction | Plural Categories |
|------|----------|-----------|--------------------|
| en | English | ltr | one, other |
| es | Spanish | ltr | one, other |
| zh | Chinese | ltr | other |
| fr | French | ltr | one, other (0 = one) |
| ar | Arabic | rtl | zero, one, two, few, many, other |
| de | German | ltr | one, other |
| ja | Japanese | ltr | other |

### I18N Key Conventions
- Location: `src/common/i18n/locales/{lang}.json`, embedded via `go:embed` in ALL binaries
- Key naming: dot-separated lowercase (e.g. `health.status.title`)
- Interpolation: `{variable}` syntax
- Plurals: nested under key with `zero`/`one`/`two`/`few`/`many`/`other` (CLDR categories)
- Same word, different meaning → different keys (e.g. `common.close` vs `nav.close`)
- `?lang=` sets a persistent cookie (`lang`, Max-Age 1 year, `SameSite=Lax`), no URL path prefixes

### Language Detection Priority
Web: `?lang=` → `lang` cookie → `Accept-Language` header → `en`
CLI/Server: `--lang` flag → config file → `LANG`/`LC_ALL` env → `en`

### A11y Requirements
| Requirement | Standard |
|-------------|----------|
| WCAG 2.1 AA | Mandatory, full compliance |
| Color contrast, normal text | 4.5:1 minimum |
| Color contrast, large text (18pt+) | 3:1 minimum |
| UI components / focus indicators | 3:1 minimum |
| Touch targets | 44x44px minimum |
| Keyboard navigation | All functionality reachable |
| Screen readers | NVDA, JAWS, VoiceOver support |

Required patterns: skip links (first focusable elements), ARIA live regions (`role="status"`/`role="alert"`), modal focus trap + return focus on close, landmark roles (`banner`/`navigation`/`main`/`complementary`/`contentinfo`), associated form labels with `aria-describedby`/`aria-required`, `.sr-only` class for screen-reader-only text.

Testing tools: axe DevTools, WAVE, Lighthouse, NVDA/VoiceOver, manual keyboard-only pass.

For complete details, see AI.md PART 28, 29, 30
