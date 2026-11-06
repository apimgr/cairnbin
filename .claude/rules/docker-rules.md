# Docker Rules (PART 26)

⚠️ **These rules are NON-NEGOTIABLE. Violations are bugs.** ⚠️

## CRITICAL - NEVER DO

- NEVER place `Dockerfile` or `docker-compose.yml` in the project root — always under `docker/`
- NEVER modify `ENTRYPOINT` or `CMD` in the Dockerfile — all customization goes through `entrypoint.sh`
- NEVER add `LABEL` blocks to the Dockerfile — all OCI metadata is applied by CI at build time via `--label`/`--annotation` flags
- NEVER add a Dockerfile `USER` directive or create the app user in the image — the binary creates its user/group and drops privileges during startup after setting up directories and permissions
- NEVER create directories in the Dockerfile — the binary handles all directory/permission/user setup based on env vars
- NEVER include `build:` or `version:` keys in any docker-compose file
- NEVER create `.env`, `.env.example`, or `.env.sample` files
- NEVER use `${VAR}` without a fallback (stack breaks if the var is unset) — always `${VAR:-default}`
- NEVER use list-style (`- KEY=value`) environment syntax in compose — always YAML map style (`KEY: value`)
- NEVER set `DEBUG`/`MODE` in `docker-compose.yml` (production) — only `docker-compose.dev.yml` and `docker-compose.test.yml` set `DEBUG: 1` / `MODE: dev`
- NEVER run `docker compose` from the project directory — always copy the compose file to a temp dir first and run from there
- NEVER mount volumes to `{project_root}/volumes/` or pass `--project-directory` pointing at the project root
- NEVER commit runtime `./volumes/` content from local runs to git
- NEVER use `docker-compose.dev.yml` or `docker-compose.yml` (production) as an AI/automated agent — those are HUMAN USE ONLY; AI uses `tests/run_tests.sh` or `docker-compose.test.yml` (as fallback)
- NEVER push `:dev` or `:test` image tags to the production registry
- NEVER add an `ENABLE_TOR` flag — Tor auto-enables if the `tor` binary is present in the image

## CRITICAL - ALWAYS DO

- ALWAYS use `docker/` for Dockerfiles/compose files and `docker/rootfs/` for the build-time container overlay
- ALWAYS use a multi-stage Dockerfile: builder stage `casjaysdev/go:latest`, runtime stage `alpine:latest`; do not add a runtime `USER` directive
- ALWAYS build context from project root (`.`), Dockerfile referenced as `-f docker/Dockerfile`
- ALWAYS end `entrypoint.sh` with `exec "$@"` (or `exec <binary> ... "$@"`) so the app becomes PID 1 and receives signals
- ALWAYS use `tini` as init: `ENTRYPOINT [ "tini", "-p", "SIGTERM", "--", "/usr/local/bin/entrypoint.sh" ]`
- ALWAYS set `STOPSIGNAL SIGRTMIN+3`
- ALWAYS set `HEALTHCHECK --start-period=10m --interval=5m --timeout=15s --retries=3`
- ALWAYS expose internal port 80
- ALWAYS install git, curl, bash, tini, tor in the runtime image
- ALWAYS include the `x-logging` anchor (`max-size: 5m`, `max-file: 1`, `driver: json-file`) in every compose file and reference it on every service
- ALWAYS mount exactly two volumes in compose: `./volumes/config:/config:z` and `./volumes/data:/data:z` (production uses `:z`; dev may omit it)
- ALWAYS use the temp-dir workflow to run docker compose: create `${TMPDIR}/${PROJECT_ORG}/${INTERNAL_NAME}-XXXXXX/`, copy the compose file there, create `volumes/config` and `volumes/data`, `cd` there, run, then clean up
- ALWAYS bind production ports to the Docker bridge (`172.17.0.1:{port}:80`); dev binds all interfaces (`{port}:80`)
- ALWAYS use a random unused port in the `64xxx` range for the external port
- ALWAYS name the main compose service `{project_name}`, container `{project_name}-app`; cache service `{project_name}-cache`
- ALWAYS set `pull_policy: always` and `restart: always` on every service
- ALWAYS ship three compose variants: `docker-compose.yml` (prod), `docker-compose.dev.yml` (human dev, `:devel` tag), `docker-compose.test.yml` (AI/automated testing, ephemeral tmpfs cache)
- ALWAYS use `${VAR:-default}` inline fallbacks for compose environment values so the stack works with zero `.env` files; operators may still override via `.env` or shell env

## Key Rules Summary

### Directory Layout

```
docker/
├── Dockerfile
├── Dockerfile.dev
├── docker-compose.yml          # production, HUMAN USE ONLY
├── docker-compose.dev.yml      # dev, HUMAN USE ONLY
├── docker-compose.test.yml     # AI/automated testing
└── rootfs/usr/local/bin/entrypoint.sh
```

### Dockerfile Requirements

| Requirement | Value |
|-------------|-------|
| Builder image | `casjaysdev/go:latest` |
| Runtime image | `alpine:latest` |
| Binary location | `/usr/local/bin/{project_name}` |
| Entrypoint | `/usr/local/bin/entrypoint.sh` |
| Init | tini |
| Internal port | 80 |
| User setup | The binary creates the app user/group and drops privileges at startup |
| STOPSIGNAL | `SIGRTMIN+3` |
| ENV MODE | not set in image; compose sets it explicitly |

### Container Path Reference

| Path | Purpose |
|------|---------|
| `/config/{project_name}/` | App config (server.yml, ssl/, tor/) |
| `/data/{project_name}/` | App data (uploads, cache, tor/) |
| `/data/db/sqlite/` | SQLite (always `server.db`) |
| `/data/db/valkey/` | Valkey persistence |
| `/data/log/{project_name}/` | Logs |
| `/data/backups/{project_name}/` | Backups |

### Compose Service Naming

| Type | Service Name | Container Name |
|------|---------------|-----------------|
| Main app | `{project_name}` | `{project_name}-app` |
| Database | `{project_name}-db` | `{project_name}-db` |
| Cache | `{project_name}-cache` | `{project_name}-cache` |
| Proxy | `{project_name}-proxy` | `{project_name}-proxy` |

### Compose Variants

| File | Image Tag | Cache | Usage |
|------|-----------|-------|-------|
| `docker-compose.yml` | `:latest` | Valkey, persistent volume | Production (human) |
| `docker-compose.dev.yml` | `:devel` | none | Local dev (human only) |
| `docker-compose.test.yml` | `:latest` | Valkey, ephemeral tmpfs | AI/automated testing |

### Port Mapping

| Mode | Format |
|------|--------|
| Development (human) | `{port}:80` (all interfaces) |
| Production / Test | `172.17.0.1:{port}:80` (bridge only) |

### Environment Variables

Stack must work with zero `.env` files — use inline `${VAR:-default}` fallbacks.

| Rule | Description |
|------|-------------|
| NEVER | Create `.env`, `.env.example`, `.env.sample` files |
| NEVER | Use `${VAR}` without a fallback (stack breaks if var is unset) |
| NEVER | Use list-style env vars (`- KEY=value`) — always YAML map style (`KEY: value`) |
| ALWAYS | Use `${VAR:-default}` so the stack works without any `.env` |
| ALWAYS | Operators may override via `.env` or shell env — the fallback just handles zero-config |

### OCI Labels (applied by CI, never in Dockerfile)

`maintainer`, `org.opencontainers.image.{vendor,authors,title,base.name,description,licenses,created,version,schema-version,revision,url,source,documentation,vcs-type}`, `com.github.containers.toolbox`.

### Image Tags

| Tag | Use |
|-----|-----|
| `:latest` | Latest stable release |
| `:{version}` | Specific version |
| `:{YYMM}` | Year/month |
| `:{commit}` (7 char) | Git commit |
| `:dev`, `:test` | Local-only, never pushed to registry |

For complete details, see AI.md PART 26
