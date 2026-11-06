# AI Assistant Rules (PART 0, 1)

⚠️ **These rules are NON-NEGOTIABLE. Violations are bugs.** ⚠️

## CRITICAL - NEVER DO

- Never guess or assume a requirement, file location, or value — search or ask instead
- Never claim "done" without reading, testing, and verifying first
- Never rely on memory of the spec — read the relevant PART before implementing
- Never fill in gaps when the spec seems incomplete — ask for clarification
- Never "improve" or "optimize" the spec, or invent patterns not in it
- Never add unrequested features
- Never edit `AI.md` or `TEMPLATE.md` content — read-only; project changes go in `IDEA.md`
- Never read an image larger than 1000x1000 directly — resize to a tmp copy first
- Never treat a non-conforming `IDEA.md` as authoritative without running migration
- Never output "Found X issues. Here's a list..." during an audit — fix them instead
- Never use `TODO.AI.md` for audit findings — use temporary `AUDIT.AI.md` (>5 issues only), delete when resolved
- Never leave `AUDIT.AI.md` with unchecked items, or leave it around after completion
- Never use plain `git commit` or `git push` — use `gitcommit <command>` only
- Never run `gitcommit -m "..."` / `--message` — message must come from `.git/COMMIT_MESS`
- Never run `gitcommit` mid-task with files in an inconsistent state
- Subagents must never write `.git/COMMIT_MESS` or call `gitcommit` — only the parent instance commits
- Subagents must never run `make`, tests, lint, or any build/test gate — the parent instance runs them once after reviewing the full diff
- Never use bare `@name` in a commit body (creates GitHub notification) — no `@` or wrap in backticks
- Never delete files without confirmation
- Never change NON-NEGOTIABLE spec sections
- Never skip validation or hardcode secrets
- Never use deprecated APIs
- Never include tool-authorship notices anywhere in code, comments, commits, PRs, or documentation — all output must read as human-authored senior-developer work
- Never place comments inline or below code — always above
- Never use `SELECT *` in application code
- Never trust input — never pass user input directly to shell, SQL, or eval
- Never reveal sensitive info to clients: resource/token existence, DB structure, internal IPs/hostnames, stack traces, dependency versions, specific auth-failure reasons
- Never log sensitive data (passwords, tokens, keys) or include it in error messages/stack traces
- Never let README.md go outdated — it's the first thing users see
- Never use relative/bare paths in embedded code (Go/JS/templates/emails) — use `{fqdn}`/`BuildURL()`; exception: internal router registration only
- Never install Go (or any language toolchain) on the local machine — all builds/tests run in Docker or Incus
- Never run `go` commands directly on the host
- Never jump between features — complete one fully before starting the next
- Never skip "minor" implementation details — every spec detail is required
- Never implement a "lite"/partial version of a project because it "seems simple"
- Never invent CLI flags, rename spec names, or change directory/file structure during migration
- Never weaken security (authn/authz, TLS, CSRF/CSP/CORS, rate limiting, input validation, least privilege) to reduce friction
- Never truncate/empty `TODO.AI.md` all at once — remove items individually as each is resolved and committed
- Never delete or empty the human-owned `TODO.md`/`PLAN.md` — mark items done in place only

## CRITICAL - ALWAYS DO

- Always read the AI.md PART(s) relevant to the current task before implementing — do not pre-load speculatively
- Always search before creating — check if something already exists
- Always read a file before editing it
- Always test changes and verify output before claiming completion
- Always ask (numbered/lettered options) when uncertain, when multiple interpretations exist, or before a destructive/architectural decision
- Always run the Mandatory Verification Steps checklist before saying "done"
- Always keep `IDEA.md` in sync with features; `AI.md` stays read-only
- Always check `.claude/rules/` exists on first session with a project; create/update the 13 rule files if missing or AI.md is newer
- Always translate all user-facing text added or modified (see Translation Rule); update `en.json`/`es.json` and note other locales need the key
- Always write and re-read `{project_dir}/.git/COMMIT_MESS` before running `gitcommit`
- Always use `gitcommit --dir {dir} all` for commits, never plain git
- Always verify own work with real tools (tests, curl, build, browser) per change-type — never rely on "looks right"
- Always fix issues found directly (no silent fixes, no report-only files) unless report-only was requested
- Always use Docker (`casjaysdev/go:latest`) or Incus for building/testing Go code, never the host
- Always follow the spec exactly during migration and new-project implementation — no invented structure, flags, or formats
- Always implement ALL mandatory PARTs (0-33) fully for every new project, including PART 32 Client
- Always keep README.md, Swagger, GraphQL, docs/, and CLI --help in sync with actual code
- Always use parameterized queries, HTML-escaping, and CSRF tokens
- Always name database columns explicitly — never `SELECT *`
- Always treat server apps as internet-facing/hostile-exposed unless the user says otherwise
- Always trim whitespace on text input; reject (don't trim) passwords with leading/trailing whitespace
- Always make configuration changes apply via live-reload (except listen address/port/DB driver)
- Always keep all settings configurable via `server.yml` only — no runtime config-mutation API, no admin web UI for config
- Always show tokens/passwords only once at generation time
- Always assume self-hosted/SMB users are NOT tech-savvy

## Key Rules Summary

**Session start sequence (first read of a project with AI.md):**
1. Read existing `CLAUDE.md`/`.claude/CLAUDE.md`
2. Migrate loader content into `IDEA.md` if missing
3. Check `.claude/rules/` — create/update if missing or stale
4. Check `.claude/memory/` — create it with an empty `MEMORY.md` index if missing (never fabricate entries)
5. Create `CLAUDE.md` loader if missing
6. Copy the final `CLAUDE.md` content into `AGENTS.md` at the project root (create or overwrite it) — a real file, never a symlink
7. Check `TODO.AI.md`/`TODO.md`
8. Read `.claude/memory/MEMORY.md` (if non-empty) and load referenced files as needed
9. Commit COMMIT/NEVER/MUST rules to memory

**Rule files table (13 files, PART 0):**

| File | PARTs |
|------|-------|
| `.claude/rules/ai-rules.md` | 0, 1 |
| `.claude/rules/project-rules.md` | 2, 3, 4 |
| `.claude/rules/config-rules.md` | 5, 6, 12 |
| `.claude/rules/binary-rules.md` | 7, 8, 32 |
| `.claude/rules/backend-rules.md` | 9, 10, 11, 31 |
| `.claude/rules/api-rules.md` | 13, 14, 15 |
| `.claude/rules/frontend-rules.md` | 16 |
| `.claude/rules/features-rules.md` | 17-22 |
| `.claude/rules/service-rules.md` | 23, 24 |
| `.claude/rules/makefile-rules.md` | 25 |
| `.claude/rules/docker-rules.md` | 26 |
| `.claude/rules/cicd-rules.md` | 27 |
| `.claude/rules/testing-rules.md` | 28, 29, 30 |

**File hierarchy:** SPEC.md > AI.md > global CLAUDE.md. `AI.md` = HOW (read-only), `IDEA.md` = WHAT (editable), `SPEC.md` = project-specific overrides.

**Audit** is triggered ONLY by explicit user command ("audit", "check compliance", "verify project") — not by discovery/Check Files/migration/normal dev. Use `AUDIT.AI.md` only when >5 issues found; delete when resolved.

**TODO.AI.md completion commit format:**
- Title: exactly `✅ all todo items have been completed ✅` (max 64 chars)
- Body summarizes completed tasks as bullets

**Naming conventions (PART 1):**

| Element | Convention | Example |
|---------|------------|---------|
| Files | `lowercase_snake.go` | `user_handler.go` |
| Packages | lowercase, single word | `server`, `config` |
| Public funcs/types | `PascalCase` | `GetUserByEmail()` |
| Private funcs/types | `camelCase` | `validateInput()` |
| Constants | `PascalCase`/`SCREAMING_SNAKE` | `MaxRetries` |
| Interfaces | `PascalCase` + `-er` | `Authenticator` |

Names must be intent-revealing — never generic `Mode`, `Type`, `Status`, `Config`, `Get()`, `Init()` without a qualifying prefix (e.g. `AppMode`, `IsEmailValid()`).

**curl standard:** always `curl -q -LSsf {url}` (add `-I`, `-o`, `-O`, `-H`, `-X`, `-d` as needed). Exception: `curl -q -LSs` (no `-f`) when capturing HTTP status codes with `-w`.

**URL rules:** documentation/README/docs use `{official_site}/path` (full URL); embedded Go/JS/template code uses `{fqdn}/path` via `BuildURL(r, ...)`, never bare `/path` (except internal router registration).

**README.md required section order:** Title/Badges → About → Official Site → Features → Production → Client → Configuration → API → Other → Development (last) → Disclaimer → License. Every badge must be a linked badge `[![alt](img)](link)`.

**Rate limiting defaults:** failed auth 5/15min; API configurable/1min; file upload 10/1hr.

**License:** MIT, `LICENSE.md` in repo root, do not modify license text except copyright year/name.

For complete details, see AI.md PART 0, 1
