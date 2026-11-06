# Makefile Rules (PART 25)

⚠️ **These rules are NON-NEGOTIABLE. Violations are bugs.** ⚠️

**Note: The Makefile is for LOCAL DEV ONLY, not CI/CD.** Automated releases (stable/beta/devel) use CI/CD workflows, never `make release`.

## CRITICAL - NEVER DO

- NEVER add Makefile targets beyond the six core targets: `dev`, `local`, `build`, `test`, `release`, `docker`
- NEVER hardcode `PROJECT_NAME` or `PROJECT_ORG` — always derive from `git remote get-url origin` or directory path
- NEVER add a `v` prefix to a text version (`dev`, `beta`, `devel`) — `vdev`, `vbeta` are always wrong
- NEVER add a `v` prefix to a timestamp version (`20251218060432`)
- NEVER double the `v` prefix (`vv0.3.0`)
- NEVER symlink or copy binaries out of `binaries/` (e.g. to `/usr/local/bin`, `~/bin`, `/tmp`) — run them in place
- NEVER copy binaries between `binaries/` and `releases/` manually — only `make release` / CI/CD does this
- NEVER skip test coverage enforcement — `make test` must fail if coverage < 60% (or the project's IDEA.md-configured higher floor)
- NEVER use `-short`, `-count=0`, or `//go:coverage ignore` to bypass the coverage gate
- NEVER build on the host — all Makefile build/test/dev/local targets run inside Docker (`casjaysdev/go:latest`)
- NEVER let `release.txt`, if present, be overridden by `VERSION` env var or git tag — `release.txt` always wins

## CRITICAL - ALWAYS DO

- ALWAYS use exactly six targets: `dev`, `local`, `build`, `test`, `release`, `docker`
- ALWAYS resolve version precedence: `release.txt` (if exists) > `VERSION` env var > `devel` fallback
- ALWAYS embed `Version`, `CommitID`, `BuildDate`, `OfficialSite` via `-ldflags -X` at build time
- ALWAYS use `CGO_ENABLED=0` and `GOFLAGS=-buildvcs=false` for Go Docker builds
- ALWAYS run `clean` before `build` and `local` (removes previous artifacts)
- ALWAYS output `make dev` builds to `${TMPDIR}/${PROJECT_ORG}/${INTERNAL_NAME}-XXXXXX/` — isolated, gitignored, no version info embedded
- ALWAYS output `make local`/`make build` to `binaries/` with full ldflags version info
- ALWAYS build all 8 platforms for `make build`: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64, windows/arm64, freebsd/amd64, freebsd/arm64
- ALWAYS strip binaries (remove debug symbols) during `make release`
- ALWAYS include `version.txt` and a source tarball (excluding `.git`, `.github`, `.gitea`, `binaries/`, `releases/`) in every release
- ALWAYS use `docker buildx build --push` with multi-arch (`linux/amd64,linux/arm64`) for `make docker`
- ALWAYS pass `VERSION`, `BUILD_DATE`, `COMMIT_ID` as `--build-arg` to the docker target
- ALWAYS name distributed binaries `{project_name}[-cli]-{os}-{arch}[.exe]`; local binaries are unsuffixed (`{project_name}`, `{project_name}-cli`)

## Key Rules Summary

### Six Core Targets

| Target | Purpose | Output Location |
|--------|---------|------------------|
| `dev` | Quick dev build | `${TMPDIR}/${PROJECT_ORG}/${INTERNAL_NAME}-XXXXXX/` |
| `local` | Production test build | `binaries/` (versioned) |
| `build` | Full release, all 8 platforms | `binaries/` |
| `test` | Unit tests + coverage gate | Coverage report |
| `release` | Manual local release + source archive | `releases/` |
| `docker` | Multi-arch build and push | `$REGISTRY` |

### Version Tag `v` Prefix Rules

| Input | Type | Add `v`? |
|-------|------|----------|
| `0.2.0`, `1.2.3` | Semver number | YES |
| `v1.2.0` | Already has v | NO (don't double) |
| `dev`, `beta`, `devel` | Text | NO |
| `20251218060432` | Timestamp | NO |

### Version Priority

1. `release.txt` (wins if present)
2. `VERSION` env var
3. `devel` fallback

### Coverage Gate

- All Go projects: 60% minimum (override upward only via IDEA.md `coverage_minimum`)
- Enforced in both `make test` and CI on every push

### Binary Naming Pattern

`{project_name}[-cli]-{os}-{arch}[.exe]` — local builds have no os/arch suffix.

### Never Copy or Symlink Binaries

| WRONG | CORRECT |
|-------|---------|
| `ln -s binaries/app /usr/local/bin/app` | `./binaries/app` |
| `cp binaries/app /tmp/` | `./binaries/app --test` |
| `cp binaries/* releases/` | CI/CD or `make release` handles it |

Exception: CI/CD release process copies + strips binaries and uploads to GitHub Releases.

### Release Types

| Type | Trigger | Version Format | v-Prefix | Max Releases |
|------|---------|-----------------|----------|---------------|
| Stable | Git tag `v*`/`*.*.*` | `X.Y.Z` | YES | Unlimited |
| Beta | Push to `beta` branch | `{YYYYMMDDHHMMSS}-beta` | NO | Unlimited |
| Devel | Schedule + push to main | short commit id (7 chars) | NO | 1 (rolling `devel` tag, replaces previous) |

`make release` = manual local stable release only. Beta/devel are CI/CD-only.

### Local Development Workflow (NOT CI/CD)

| Stage | Command |
|-------|---------|
| 1. Coding | `make dev` |
| 2. Quick test | Run binary in Docker |
| 3. Toolchain gate | `make test` (pre-commit requirement) |
| 4. Binary validation | `./tests/run_tests.sh` |
| 5. Production test | `make local` |
| 6. Release | `make build` |

For complete details, see AI.md PART 25
