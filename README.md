# Cairnbin

[![CI](https://github.com/apimgr/cairnbin/actions/workflows/ci.yml/badge.svg)](https://github.com/apimgr/cairnbin/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/apimgr/cairnbin)](https://github.com/apimgr/cairnbin/releases)
[![License](https://img.shields.io/github/license/apimgr/cairnbin)](LICENSE.md)
[![Docs](https://readthedocs.org/projects/apimgr-cairnbin/badge/?version=latest)](https://apimgr-cairnbin.readthedocs.io)

## About

A fast, full-stack Go web application for creating and sharing text snippets anonymously.
Drop-in replacement for pastebin.com, microbin, and lenpaste — existing scripts, CLIs,
and integrations work without modification.

## Official Site

**[https://pste.us](https://pste.us)** — the official hosted instance. The CLI client
uses it as the default server; use `--server <url>` to target your own instance.

## Features

- Anonymous paste creation via web form, JSON API, raw body, or multipart file upload
- MIME-detected images, audio, and video render as safe media; active formats such as SVG and HTML are served as downloads
- Server-side syntax highlighting via Chroma (no client-side JS required)
- Language auto-detection from file extension on upload
- Expiry options: `1h`, `1d`, `1w`, `1m`, `3m`, `6m`, `1y`, `18m`, `2y`, `never`, or custom seconds
- Burn after N reads — paste deleted once view count reaches threshold
- Public and unlisted visibility
- Link pastes — auto-detected when content is exactly one `http://`/`https://` URL and nothing else; visiting the paste 302-redirects to the target (`/{id}/raw` returns the target URL as text instead)
- Owner token — cryptographically random `tok_...` value returned at creation as `owner_token`, required to delete a paste, and viewable masked by default in `/server/preferences/tokens`
- Raw paste view at `/raw/{id}` and `/{id}/raw`
- Download at `/dl/{id}`
- Embedded view at `/emb/{id}` (iframe-embeddable)
- QR code at `/qr/{id}`
- View count tracking
- Automatic background cleanup of expired and burned pastes
- Full web frontend (server-side Go templates, dark/light/auto theme, PWA, mobile-first)
- Cookie-based preferences with inline export/import, consent controls, and local reset at `/server/preferences`; owner-token management at `/server/preferences/tokens`
- Server pages: `/server/about`, `/server/contact`, `/server/help`, `/server/help/mcp` (when MCP is enabled), `/server/healthz`, `/server/privacy`, `/server/security`, `/server/security/policy`, `/server/security/thanks`, `/server/security/report/{tracking_id}`, `/server/terms`
- CLI client (`cairnbin-cli`)
- OpenAPI/Swagger docs at `/api/v1/server/swagger`
- GraphQL at `/api/v1/server/graphql` (GraphiQL UI at `/server/docs/graphql`, read-only queries)
- MCP endpoint at `/api/v1/server/mcp` (aliases `/api/mcp`, `/mcp`) so AI clients can use the API as tools with the same Bearer tokens
- Full route compatibility with pastebin.com, microbin, lenpaste, stikked, hastebin/haste-server, dpaste, the curl-upload family (sprunge/0x0/ix.io), and termbin/fiche (raw TCP on port 9999, enabled by default)
- Built-in scheduler for expiry cleanup, backups, security-database updates, and self-update checks (no external cron)
- Backup and restore with optional AES-256-GCM encryption (Argon2id key derivation)
- Self-update with SHA-256 checksum verification (stable/beta/devel channels)
- Prometheus metrics endpoint (`/metrics`, internal-only)
- GeoIP country allow/deny (advisory) with auto-downloaded, scheduler-updated databases
- Rate limiting per read/write/health class
- Tor hidden-service support (auto-enabled when the `tor` binary is present)
- Internationalization: English, Spanish, Chinese, French, Arabic (RTL), German, Japanese
- Operator email notifications with SMTP auto-detection (no email attempted without working SMTP)
- Cookie-consent banner and privacy/terms pages
- Cross-platform service management (systemd, OpenRC, SysVinit, runit, launchd, rc.d, Windows Service)
- Single self-contained static binary with embedded SQLite

## Production

### Docker (Recommended)

```bash
docker run -d \
  --name cairnbin \
  -p 172.17.0.1:64580:80 \
  -p 9999:9999 \
  -v ./volumes/config:/config:z \
  -v ./volumes/data:/data:z \
  ghcr.io/apimgr/cairnbin:latest
```

### Docker Compose

```bash
curl -q -LSsf -O https://raw.githubusercontent.com/apimgr/cairnbin/main/docker/docker-compose.yml
docker compose up -d
```

### Binary

```bash
# Download latest release
curl -q -LSsf -O https://github.com/apimgr/cairnbin/releases/latest/download/cairnbin-linux-amd64

# Make executable and run
chmod +x cairnbin-linux-amd64
./cairnbin-linux-amd64
```

## Client

A companion client, `cairnbin-cli`, is available for interacting with the server API.

### Install

```bash
# Download latest release
curl -q -LSsf -O https://github.com/apimgr/cairnbin/releases/latest/download/cairnbin-cli-linux-amd64
chmod +x cairnbin-cli-linux-amd64
sudo mv cairnbin-cli-linux-amd64 /usr/local/bin/cairnbin-cli
```

### Configure

```bash
# Target a custom server (default: https://pste.us)
cairnbin-cli --server https://your-server.example.com create myfile.txt
```

### Usage

```bash
# Create a paste from stdin
echo "Hello, World!" | cairnbin-cli create

# Create from file
cairnbin-cli create myfile.go --lang go --expiry 1d

# Fetch paste content
cairnbin-cli get abc12345

# Delete paste
cairnbin-cli delete abc12345 <owner-token>

# List recent pastes
cairnbin-cli list --limit 20
```

## Configuration

The server reads `/etc/apimgr/cairnbin/server.yml` on Linux (created automatically on
first run). All settings can be overridden via CLI flags.

### Server (`cairnbin`) Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `CONFIG_DIR` | Platform default | Configuration directory path |
| `DATA_DIR` | Platform default | Data directory path |
| `LOG_DIR` | Platform default | Log directory path (`LOGS_DIR` accepted as an alias) |
| `CACHE_DIR` | Platform default | Cache directory path |
| `BACKUP_DIR` | Platform default | Backup directory path |
| `PID_FILE` | Platform default | PID file path |
| `PORT` | Random 64xxx | Listen port (80 in container) |
| `ADDRESS` | `0.0.0.0` | Listen address |
| `BASE_URL` | Auto-detected | Public base URL for link generation |
| `DB_PATH` | `{data_dir}/db/server.db` | SQLite database path |
| `SITE_TITLE` | `Cairnbin` | Site title shown in the web UI |
| `THEME` | `dark` | UI theme: `dark`, `light`, or `auto` |
| `MAX_SIZE` | `10mb` | Maximum paste size; accepts `kb`/`mb`/`gb`/`tb` suffixes, a bare number (defaults to MB), or `0`/negative for unlimited |
| `MAX_SIZE_BYTES` | unset | Deprecated alias for `MAX_SIZE` in raw bytes |
| `NO_COLOR` | unset | Set to any value to disable ANSI color output |
| `_DAEMON_CHILD` | unset | Internal: set by `--daemon` to mark the child process |

Platform default paths:

| Platform | Config | Data | Logs |
|----------|--------|------|------|
| Linux (root/service) | `/etc/apimgr/cairnbin/` | `/var/lib/apimgr/cairnbin/` | `/var/log/apimgr/cairnbin/` |
| Linux (user) | `~/.config/apimgr/cairnbin/` | `~/.local/share/apimgr/cairnbin/` | `~/.local/log/apimgr/cairnbin/` |
| Container | `/config/cairnbin/` | `/data/cairnbin/` | `/data/log/cairnbin/` |
| macOS (user) | `~/Library/Application Support/apimgr/cairnbin/` | same | `~/Library/Logs/apimgr/cairnbin/` |
| Windows (user) | `%AppData%\apimgr\cairnbin\` | `%LocalAppData%\apimgr\cairnbin\` | `%LocalAppData%\apimgr\cairnbin\logs\` |

### Client (`cairnbin-cli`) Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `CAIRNBIN_SERVER_PRIMARY` | `https://pste.us` (embedded) | Server base URL override for self-hosted instances |
| `CAIRNBIN_TOKEN` | unset | Operator/owner API token (overridden by `--token`; falls back to `cli.yml` `auth.token`) |
| `CAIRNBIN_SERVER_TIMEOUT` | `cli.yml` value | Server request timeout override |
| `CAIRNBIN_SERVER_RETRY` | `cli.yml` value | Retry count override |
| `CAIRNBIN_SERVER_RETRY_DELAY` | `cli.yml` value | Retry delay override |
| `CAIRNBIN_SERVER_API_VERSION` | `cli.yml` value | API version override |
| `CAIRNBIN_OUTPUT_FORMAT` | `cli.yml` value | Output format override (`json`, `table`, `plain`) |
| `CAIRNBIN_OUTPUT_COLOR` | `auto` | Color output override (`auto`, `yes`, `no`) |
| `CAIRNBIN_DEBUG` | `cli.yml` value | Debug output override (truthy/falsy) |
| `CLI_CONFIG` | Platform default | Path to the client `cli.yml` configuration file |
| `NO_COLOR` | unset | Set to any value to disable ANSI color output |

## API

API documentation available at `https://pste.us/api/v1/server/swagger` when running.

| Endpoint | Description |
|----------|-------------|
| `GET https://pste.us/server/healthz` | Health check |
| `POST https://pste.us/api/v1/pastes` | Create a paste |
| `GET https://pste.us/api/v1/pastes/{id}` | Get a paste |
| `DELETE https://pste.us/api/v1/pastes/{id}` | Delete a paste |
| `GET https://pste.us/api/v1/pastes` | List recent pastes |

### Examples

```bash
# Create a paste
curl -q -LSsf -X POST https://pste.us/api/v1/pastes \
  -H 'Content-Type: application/json' \
  -d '{"content":"Hello","language":"text","expires_in":"1d"}'

# Get a paste
curl -q -LSsf https://pste.us/api/v1/pastes/{id}

# Delete a paste
curl -q -LSsf -X DELETE https://pste.us/api/v1/pastes/{id} \
  -H 'Authorization: Bearer <owner-token>'

# List recent pastes
curl -q -LSsf https://pste.us/api/v1/pastes

# Pipe to paste (raw body)
curl -q -LSsf -X POST https://pste.us/api/v1/pastes \
  --data-binary @file.txt -H 'Content-Type: text/plain'
```

## Other

### Troubleshooting

- Check logs: `docker logs cairnbin-app`
- Health check: `curl -q -LSsf https://pste.us/server/healthz`

## Development

**Development instructions are for contributors only.**

### Prerequisites

- Go (latest stable)
- Docker (for containerized builds)

The server does not support cross-compilation to NetBSD arm64 because the upstream `modernc.org/libc` dependency currently provides no NetBSD arm64 `errno` implementation. NetBSD amd64 remains supported for cross-compilation.

### Build

Requires Docker (no local Go toolchain needed):

```bash
# Clone
git clone https://github.com/apimgr/cairnbin
cd cairnbin

# Full build (all 8 platforms, outputs to binaries/)
make build

# Current platform only
make local

# Quick development build
make dev

# Run unit tests
make test

# Run the manual browser E2E suite (requires Docker and Chromium sidecar)
bash tests/e2e.sh
```

### Usage (local build)

```bash
# Start server (binds all interfaces on a random port in 64000-64999, saved to server.yml on first run)
./cairnbin

# Custom address and port
./cairnbin --address 127.0.0.1 --port 8080

# Show version
./cairnbin --version

# Show status
./cairnbin --status
```

## Disclaimer

This software is provided "as is" without warranty of any kind. Use at your own risk.

- **No Warranty**: The authors are not responsible for any damages, data loss, or issues arising from use of this software
- **Not Professional Advice**: This software does not constitute legal, financial, medical, or other professional advice
- **Third-Party Services**: If this software connects to external APIs or services, their terms of service apply separately
- **Security**: While we strive to follow security best practices, no software is guaranteed to be free of vulnerabilities
- **Production Use**: Evaluate thoroughly before deploying in production environments

By using this software, you acknowledge that you have read and understood this disclaimer.

## License

MIT — see [LICENSE.md](LICENSE.md)
