# Contributing to Cairnbin

Thank you for your interest in contributing!

## Local Setup

Requires Docker (no local Go toolchain needed).

```bash
git clone https://github.com/apimgr/cairnbin
cd cairnbin
make dev          # quick development build
make test         # run unit tests
make local        # production build (current platform)
```

## Branch and PR Workflow

1. Fork the repository
2. Create a feature branch from `main`: `git checkout -b feature/my-feature`
3. Make your changes
4. Ensure tests pass: `make test`
5. Ensure build succeeds: `make local`
6. Open a pull request against `main`

Direct pushes to `main` are not permitted. All changes require a pull request.

## Code Requirements

- Read `AI.md` before implementing any feature — it is the source of truth
- All source code lives in `src/`
- CGO_ENABLED=0 always — pure Go only
- No dependencies on host-installed toolchain — use `make` targets (Docker internally)
- Tests must pass before merging
- Update docs and config reference when changing user-facing behavior

## Reporting Vulnerabilities

Do NOT open a public issue for security vulnerabilities. See [SECURITY.md](SECURITY.md) for the responsible disclosure process.

## Documentation Updates

When changing routes, config options, or user-facing behavior, update the corresponding pages in `docs/` and the Swagger annotations in `src/swagger/`.
