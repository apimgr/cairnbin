# Project Rules (PART 2, 3, 4)

⚠️ **These rules are NON-NEGOTIABLE. Violations are bugs.** ⚠️

## CRITICAL - NEVER DO

- Never use a license other than MIT for the project
- Never use GPL/AGPL/LGPL (copyleft) dependencies — always avoid, no exceptions
- Never omit `LICENSE.md` from the project root
- Never skip embedding third-party dependency licenses in `LICENSE.md`
- Never leave third-party license attributions outdated after a dependency change
- Never use a static/fake license badge (`img.shields.io/badge/license-MIT-blue.svg`) — always use `img.shields.io/github/license/{project_org}/{project_name}` so GitHub can detect it
- Never add a `LICENSE` label to the Dockerfile — license metadata is an OCI annotation at build time, not a Dockerfile `LABEL`
- Never hardcode `{project_name}` or `{project_org}` — always infer from git remote or directory path
- Never change `{internal_name}` after first-time setup except on an explicit org/repo rename (with a coordinated migration of every directory, service, and plist on every host) or at a fork's first setup — an ordinary project rename never touches it
- Never assume the current working directory is project root — use `git rev-parse --show-toplevel` or compute it explicitly
- Never mix runtime-directory purposes — user-editable config never goes in `{data_dir}`, app-managed data never goes in `{config_dir}`
- Never skip support for any of the 4 required OSes (Linux, BSD, macOS, Windows) or either architecture (AMD64, ARM64)
- Never hardcode a specific Go version anywhere (docs, Docker, CI) — always latest stable, unpinned `casjaysdev/go:latest`
- Never use `github.com/mattn/go-sqlite3` (requires CGO) — always `modernc.org/sqlite`
- Never use a CGO-requiring library anywhere — breaks `CGO_ENABLED=0` static builds
- Never use forbidden libraries: `lib/pq`, `ooni/go-libtor`, `dgrijalva/jwt-go`, `gorilla/mux`, `go-redis/redis` (old path)
- Never use `.yaml` — config file is always `server.yml`
- Never commit `binaries/`, `releases/`, `docker/rootfs/`, or `volumes/` — always gitignored
- Never include `.git/`, CI workflow dirs, `volumes/`, `binaries/`, `releases/`, `tests/`, `docs/`, `*.md`, or `Makefile` in the Docker build context — always in `.dockerignore`
- Never exclude `src/`, `go.mod`/`go.sum`, `docker/`, or `docker/rootfs/` from `.dockerignore`
- Never put inline YAML comments — always above the setting
- Never run `go` commands directly — always through Makefile targets

## CRITICAL - ALWAYS DO

- Always use MIT License with `LICENSE.md` in the project root
- Always embed third-party licenses in `LICENSE.md` (compact table format recommended for 10+ deps)
- Always automate license checking in CI to block GPL/AGPL/LGPL
- Always include a linked license badge in README.md near the top
- Always apply license metadata as an OCI annotation (`org.opencontainers.image.licenses=MIT`) at `docker buildx build` time
- Always infer `{project_name}`/`{project_org}` from git remote first, falling back to directory path
- Always keep `{internal_name}` stable at its initial value (equal to `{project_name}`) after first-time setup — reset only on an explicit org/repo rename or a fork's first setup
- Always make all file paths relative to project root, never to `$PWD`
- Always support Linux, BSD, macOS, Windows on both AMD64 and ARM64
- Always use `modernc.org/sqlite` for SQLite (pure Go, `CGO_ENABLED=0`) and normalize `sqlite`/`sqlite2`/`sqlite3` aliases to it
- Always use `github.com/tursodatabase/libsql-client-go` for libSQL/Turso (remote-only)
- Always start `.gitignore` with the two required header lines: `# gitignore created on MM/DD/YY at HH:MM` then literal `ignoredirmessage`
- Always gitignore `binaries/`, `releases/`, `volumes/`, IDE files, personal AI settings, credentials, caches, locks, and runtime state
- Always commit shared AI team configuration such as `.claude/CLAUDE.md`, rules, memory, agents, hooks, commands, plans, and shared settings
- Always place YAML comments above the setting they describe (exception: GitHub Actions SHA-pin inline version comments)
- Always maintain the full `.claude/rules/` set and commit team-owned rule/config files for other AI tools when those tool directories are present

## Key Rules Summary

**License compatibility (MIT project):** can use MIT, Apache 2.0, BSD, ISC, Public Domain. Cannot use GPL, AGPL, LGPL.

**License attribution requirement by type:**

| License | Full Text Required |
|---------|--------------------|
| MIT | No — copyright notice only |
| ISC | No — copyright notice only |
| BSD-2-Clause | No — copyright notice only |
| BSD-3-Clause | Yes (brief, non-endorsement clause) |
| Apache 2.0 | Only NOTICE file if the library has one |
| MPL 2.0 | Reference/link only |

**Variable naming (PART 3):**

| Placeholder | Case | Stable? | Example |
|-------------|------|---------|---------|
| `{project_name}` | lower | No | `jokes` |
| `{PROJECT_NAME}` | UPPER | No | `PROJECT_NAME=jokes` |
| `{project_org}` | lower | No | `casjay` |
| `{PROJECT_ORG}` | UPPER | No | `PROJECT_ORG=casjay` |
| `{internal_name}` | lower | **Yes** | `jokes` |
| `{INTERNAL_NAME}` | UPPER | **Yes** | `INTERNAL_NAME=jokes` |
| `{plist_name}` | derived | Yes | `io.github.{project_org}.{internal_name}` |

**`{internal_name}` rule:** set once at first-time setup (initial value = `{project_name}`), then stable across ordinary project renames. It resets only on an explicit user-directed org/repo rename (update `IDEA.md`, grep every consumer, migrate every directory, service, and plist on every host) or at a fork's first setup (reset to the fork's own `{project_name}` so it never shares paths, units, or identifiers with the upstream). Used for every on-disk identifier (`{config_dir}`, `{data_dir}`, `{log_dir}`, `{cache_dir}`, `{pid_file}`, systemd unit name, `{plist_name}`) so a project rename does not orphan paths or services.

**Recommended local path:** `~/Projects/{gitprovider}/{project_org}/{internal_name}` (any location technically valid; `local` provider for prototyping without VCS/registry).

**Required top-level directories:** `.github/` or `.gitea/` workflows, `.claude/rules/` (13 files), `docs/` (MkDocs/ReadTheDocs), `src/`, `scripts/`, `tests/` (`run_tests.sh`, `docker.sh`, `incus.sh`, `e2e.sh`), `docker/` (`Dockerfile`, `Dockerfile.dev`, 3 compose files, `rootfs/`), `volumes/` (gitignored), `binaries/` (gitignored), `releases/` (gitignored).

**Required root files:** `README.md`, `LICENSE.md`, `AI.md`, `IDEA.md`, `AGENTS.md` (identical copy of `CLAUDE.md`, for non-Claude agents — real file, never a symlink), `Jenkinsfile`, and `release.txt`. Optional root files: `TODO.AI.md` (required only while tracking three or more active tasks), `TODO.md` and `PLAN.md` (human-owned — AI marks items done only, never deletes), `PLAN.AI.md` (AI-owned), `SPEC.md` (project overrides, may be empty), and `site.txt` (official site URL).

**Runtime directory purpose table:**

| Directory | Purpose |
|-----------|---------|
| `{config_dir}` | User-editable config, SSL certs, custom themes |
| `{data_dir}` | App-managed data: DB, Tor keys, caches, GeoIP DBs |
| `{log_dir}` | Log files |
| `{backup_dir}` | `.tar.gz` backup archives |

**OS path table (Linux privileged, abbreviated — full table per-OS in AI.md PART 4):**

| Type | Linux (root) | Linux (user) | Docker |
|------|---------------|---------------|--------|
| Config file | `/etc/{internal_org}/{internal_name}/server.yml` | `~/.config/{internal_org}/{internal_name}/server.yml` | `/config/{project_name}/server.yml` |
| Data | `/var/lib/{internal_org}/{internal_name}/` | `~/.local/share/{internal_org}/{internal_name}/` | `/data/{project_name}/` |
| Logs | `/var/log/{internal_org}/{internal_name}/server.log` | `~/.local/log/.../server.log` | `/data/log/{project_name}/server.log` |
| SQLite DB | `/var/lib/.../db/server.db` | `~/.local/share/.../db/server.db` | `/data/db/sqlite/server.db` |
| PID | `/var/run/{internal_org}/{internal_name}.pid` | `~/.local/share/.../{internal_name}.pid` | — |
| Service | `/etc/systemd/system/{internal_name}.service` | — | — |

macOS uses `/Library/Application Support/...` (root) and `~/Library/Application Support/...` (user), with LaunchDaemon/LaunchAgent `.plist` services. BSD mirrors Linux but under `/usr/local/etc/`, `/var/db/`. Windows uses `%ProgramData%\...` (privileged) / `%AppData%`, `%LocalAppData%\...` (user).

**Docker-only paths** (`/data/**`, `/config/**`) apply exclusively inside containers — never used for native OS deployments. Internal container port is always `80`.

**Required Go libraries:** chi/v5 (router), modernc.org/sqlite, tursodatabase/libsql-client-go, redis/go-redis/v9, bradfitz/gomemcache, yaml.v3, google/uuid, cretz/bine (Tor), gorilla/websocket, rs/cors, go-co-op/gocron/v2, golang.org/x/time/rate, go-playground/validator/v10.

For complete details, see AI.md PART 2, 3, 4
