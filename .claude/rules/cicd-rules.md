# CI/CD Rules (PART 27)

⚠️ **These rules are NON-NEGOTIABLE. Violations are bugs.** ⚠️

## CRITICAL - NEVER DO
- NEVER use Makefile targets inside CI/CD workflows — commands must be explicit (`go build ...` with all flags visible), unlike local dev which always uses Makefile targets
- NEVER reference local user paths like `~/.local/share/go` in CI — use `/tmp/` or CI-native caching
- NEVER depend on local Docker containers for the build step on GitHub/Gitea Actions — they use native `casjaysdev/go:latest` container images, not host Docker
- NEVER cross-cancel different release refs — concurrency auto-cancel may only cancel an older run for the *exact same* branch/tag ref (e.g. a `v1.2.4` run must never cancel `v1.2.3`)
- NEVER install tools inline in `ci.yml` jobs (no `apk add`, no `go install`) — all tooling (git, bash, govulncheck, cyclonedx-gomod, staticcheck, etc.) is pre-installed in `casjaysdev/go:latest`
- NEVER use `github.event.before`/`default_branch` incorrectly for secret-scan range — never use `default_branch` (resolves to HEAD after push, silently skips the scan); use `github.event.before` / `github.event.after`
- NEVER create a `build-toolchain.yml` or `ensure-build-image` gate for Go projects — `casjaysdev/go:latest` is externally maintained
- NEVER include `-musl` suffix in binary names
- NEVER skip building any of the 8 required platform/arch combinations
- NEVER run container jobs without `options: "--user 0:0"` — the runner execs into the job container as a user the image's `/etc/passwd` may not define, which fails after all real work passes

## CRITICAL - ALWAYS DO
- ALWAYS use Docker `casjaysdev/go:latest` as the toolchain image in both local dev and CI/CD (only the build *commands* differ, not the image)
- ALWAYS use CI-native caching (not local host cache paths) in CI/CD
- ALWAYS set `VERSION`, `COMMIT_ID`, `BUILD_DATE` explicitly in every workflow via a "Set build info" step (never as static `env:`)
- ALWAYS build all platforms in a matrix (8 total: linux/darwin/windows/freebsd × amd64/arm64)
- ALWAYS auto-cancel older in-progress runs for the same ref on push workflows targeting `main`, `master`, `devel`, `dev`, or `beta` via `concurrency:` + `cancel-in-progress`
- ALWAYS apply the same auto-cancel rule per-exact-tag for tag-only release workflows
- ALWAYS run all `ci.yml` jobs inside `container: image: casjaysdev/go:latest`
- ALWAYS pin third-party GitHub Actions to a full commit SHA (never a tag) — see SHA table below
- ALWAYS build the CLI binaries (`-cli` suffix) only when `src/client/` exists (`hashFiles('src/client/') != ''` / `[ -d src/client ]`)
- ALWAYS require `ci.yml` and `release.yml` on every project; `beta.yml`/`devel.yml`/`docker.yml` are optional, project-specific
- ALWAYS enforce the Go test coverage threshold (60%) in `ci.yml`
- ALWAYS include `secret-scan`, `workflow-policy`, `vuln-scan`, `image-scan` security jobs in `ci.yml`, running on push/PR and weekly cron (`0 6 * * 1`); add `if: github.event_name != 'schedule'` to skip non-security jobs on scheduled runs
- ALWAYS use truffleHog (Apache-2.0) for mandatory secret scanning on every public repo
- ALWAYS require Jenkins agent labels `amd64` AND `arm64` to be available, with Docker + buildx on the amd64 runner

## Key Rules Summary

### Workflow files by provider

| File | Trigger | Required? |
|------|---------|-----------|
| `ci.yml` | push/PR to default branch + weekly cron (security jobs) | Required, all providers |
| `release.yml` | tag push `v*` / `*.*.*` | Required, all providers |
| `beta.yml` | push to `beta` branch | Optional |
| `devel.yml` | daily 3am UTC + push to main/master (rolling `devel` release) | Optional |
| `docker.yml` | any push (all branches) + version tags | Optional |
| `.gitlab-ci.yml` | single file, stages: build/test/package/release/docker | Required for GitLab |
| `Jenkinsfile` | pipeline stages matching above triggers | Required for Jenkins |

### Config locations by provider

| Provider | Directory | Self-hosted |
|----------|-----------|--------------|
| GitHub | `.github/workflows/*.yml` | No (github.com only) |
| Gitea | `.gitea/workflows/*.yml` | Yes |
| Forgejo | `.forgejo/workflows/*.yml` (or `.gitea/workflows/`) | Yes (self-hosted only) |
| GitLab | `.gitlab-ci.yml` | Yes |
| Jenkins | `Jenkinsfile` | Yes |

### SHA-pinned actions used in this spec (never use tags)

| Action | SHA | Version comment |
|--------|-----|------------------|
| `actions/checkout` | `3d3c42e5aac5ba805825da76410c181273ba90b1` | v7.0.1 |
| `actions/upload-artifact` | `043fb46d1a93c77aae656e7c1c64a875d1fc6a0a` | v7.0.1 |
| `actions/download-artifact` | `3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c` | v8.0.1 |
| `actions/attest-build-provenance` | `4d101475d8b20a2381f78447822ac1eab6504dd8` | v4.2.2 |
| `softprops/action-gh-release` | `efb35369e0ad2afab669f228072c1b0d510eae64` | v3.0.3 |
| `trufflesecurity/trufflehog` | `b2b0a92070f206ab7b5a1105d82a7f2f48d92341` | v3.99.0 |
| `aquasecurity/trivy-action` | `ed142fd0673e97e23eac54620cfb913e5ce36c25` | v0.36.0 |
| `docker/setup-qemu-action` | `99012661954931238ded8c8b007157a8430204e1` | v4.4.0 |
| `docker/setup-buildx-action` | `f87e5991a6d7451dcb8d9637bfbc97413f497069` | v4.4.1 |
| `docker/login-action` | `dbcb813823bdd20940b903addbd779551569679f` | v4.6.0 |
| `docker/build-push-action` | `c3c9e263c25d99ce0380d002d59b67737d91b0dc` | v7.4.0 |
| `docker/metadata-action` | `dc802804100637a589fabce1cb79ff13a1411302` | v6.2.0 |

Note: verify/refresh these SHAs against upstream before reuse — pins age.

### Build info variables (every workflow)

Set in a "Set build info" step (not static `env:`):
- `VERSION` — from `release.txt` if present, else derived from ref/date
- `COMMIT_ID` — `git rev-parse --short HEAD`
- `BUILD_DATE` — `date +"%a %b %d, %Y at %H:%M:%S %Z"`
- `OFFICIAL_SITE` — `site.txt` wins, else secret/CI var, else empty (never guess)
- `LDFLAGS="-s -w -X 'main.Version=...' -X 'main.CommitID=...' -X 'main.BuildDate=...' -X 'main.OfficialSite=...'"`
- Build command: `go build -buildvcs=false -trimpath -ldflags "${LDFLAGS}" -o {name} ./src` (CI); local dev never uses `-buildvcs=false` trick without the Docker mount workaround

### Coverage threshold

- `THRESHOLD=60` — `go test -cover -coverprofile=...` then `go tool cover -func=... | awk '/^total:/'`; `exit 1` if `PCT < THRESHOLD`

### Docker image tags

| Trigger | Tags |
|---------|------|
| Any push (all branches) | `devel`, `{commit_id}` |
| Push to `beta` | `devel`, `beta`, `{commit_id}` |
| Version tag | `{version}`, `latest`, `YYMM` |

- Registry: `ghcr.io` (GitHub); auto-detected from server URL (Gitea/Forgejo); `$CI_REGISTRY_IMAGE` (GitLab); provider-specific (Jenkins)
- Platforms: `linux/amd64,linux/arm64` via `docker buildx`
- `{commit_id}` = 7-char short SHA; `YYMM` = e.g. `2512`
- Standard image: `docker/Dockerfile` (alpine, app binary only); Dev image: `docker/Dockerfile.dev` (alpine + debug tooling, `:devel` tag)
- OCI labels/annotations required on every image build: `org.opencontainers.image.{vendor,authors,title,base.name,description,version,created,revision,url,source,documentation,licenses=MIT}` (both `labels:` and `manifest:`-prefixed `annotations:`)

### Platform matrix (8 required builds)

`linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64` (.exe), `windows/arm64` (.exe), `freebsd/amd64`, `freebsd/arm64` — CLI builds duplicate this matrix with `-cli` suffix only if `src/client/` exists.

### Variable mapping (GitHub → Gitea → Forgejo → GitLab)

| GitHub | Gitea | Forgejo | GitLab |
|--------|-------|---------|--------|
| `${{ github.* }}` | `${{ gitea.* }}` | `${{ forgejo.* }}` | `$CI_*` |
| `GITHUB_ENV` | `GITEA_ENV` | `FORGEJO_ENV` | `dotenv` artifact |
| `GITHUB_OUTPUT` | `GITEA_OUTPUT` | `FORGEJO_OUTPUT` | n/a |
| `GITHUB_REF_NAME` | `GITEA_REF_NAME` | `FORGEJO_REF_NAME` | `$CI_COMMIT_REF_NAME` |
| `github.token` | `secrets.GITEA_TOKEN` | `secrets.FORGEJO_TOKEN` | n/a |
| `github.sha` | — | — | `$CI_COMMIT_SHA` |
| `github.repository` | `gitea.repository` | `forgejo.repository` | `$CI_PROJECT_PATH` |
| `github.server_url` | `gitea.server_url` | `forgejo.server_url` | `$CI_SERVER_URL` |
| `secrets.NAME` | `secrets.NAME` | `secrets.NAME` | `$NAME` |

### Required tokens/permissions

| Provider | Token permissions |
|----------|--------------------|
| GitHub | `write:packages`, `read:packages`, `delete:packages` |
| Gitea/Forgejo | `package:write` |
| GitLab | `write_registry`, `read_registry` |
| Docker Hub | Read/Write |

### Daily build cleanup

- Delete previous `devel` tag/release before creating new one: `gh release delete devel --yes` + `git push origin :refs/tags/devel` (GitHub); Gitea API `DELETE .../releases/tags/devel` (Gitea/Forgejo)

For complete details, see AI.md PART 27
