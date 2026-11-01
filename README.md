# Pastebin

[![CI](https://github.com/apimgr/pastebin/actions/workflows/ci.yml/badge.svg)](https://github.com/apimgr/pastebin/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/apimgr/pastebin)](https://github.com/apimgr/pastebin/releases)
[![License](https://img.shields.io/github/license/apimgr/pastebin)](LICENSE.md)
[![Docs](https://readthedocs.org/projects/apimgr-pastebin/badge/?version=latest)](https://apimgr-pastebin.readthedocs.io)

## About

A fast, full-stack Go web application for creating and sharing text snippets anonymously.
Drop-in replacement for pastebin.com, microbin, and lenpaste — existing scripts, CLIs,
and integrations work without modification.

## Official Site

**[https://pste.us](https://pste.us)** — the official hosted instance. The CLI client
uses it as the default server; use `--server <url>` to target your own instance.

## Features

- Anonymous paste creation via web form, JSON API, raw body, or multipart file upload
- Server-side syntax highlighting via Chroma (no client-side JS required)
- Language auto-detection from file extension on upload
- Expiry options: `1h`, `1d`, `1w`, `1m`, `3m`, `6m`, `1y`, `18m`, `2y`, `never`, or custom seconds
- Burn after N reads — paste deleted once view count reaches threshold
- Public and unlisted visibility
- Link pastes — auto-detected when content is exactly one `http://`/`https://` URL and nothing else; visiting the paste 302-redirects to the target (`/{id}/raw` returns the target URL as text instead)
- Owner token — cryptographically random `tok_...` value returned once at creation (as `owner_token`), stored only as a SHA-256 hash, required to delete a paste
- Raw paste view at `/raw/{id}` and `/{id}/raw`
- Download at `/dl/{id}`
- Embedded view at `/emb/{id}` (iframe-embeddable)
- QR code at `/qr/{id}`
- View count tracking
- Automatic background cleanup of expired and burned pastes
- Full web frontend (server-side Go templates, dark/light/auto theme, PWA, mobile-first)
- Server pages: `/server/about`, `/server/help`, `/server/healthz`, `/server/privacy`, `/server/terms`
- CLI client (`pastebin-cli`)
- OpenAPI/Swagger docs at `/api/v1/server/swagger`
- GraphQL at `/api/v1/server/graphql` (GraphiQL UI at `/server/docs/graphql`, read-only queries)
- MCP endpoint at `/api/v1/server/mcp` (aliases `/api/mcp`, `/mcp`) so AI clients can use the API as tools with the same Bearer tokens
- Full route compatibility with pastebin.com, microbin, lenpaste, stikked, hastebin/haste-server, dpaste, the curl-upload family (sprunge/0x0/ix.io), and termbin/fiche (raw TCP, opt-in)
- Built-in scheduler for expiry cleanup, backups, security-database updates, and self-update checks (no external cron)
- Backup and restore with optional AES-256-GCM encryption (Argon2id key derivation)
- Self-update with SHA-256 checksum verification (stable/beta/daily channels)
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
  --name pastebin \
  -p 172.17.0.1:64580:80 \
  -v ./volumes/config:/config:z \
  -v ./volumes/data:/data:z \
  ghcr.io/apimgr/pastebin:latest
```

### Docker Compose

```bash
curl -q -LSsf -O https://raw.githubusercontent.com/apimgr/pastebin/main/docker/docker-compose.yml
docker compose up -d
```

### Binary

```bash
# Download latest release
curl -q -LSsf -O https://github.com/apimgr/pastebin/releases/latest/download/pastebin-linux-amd64

# Make executable and run
chmod +x pastebin-linux-amd64
./pastebin-linux-amd64
```

## Client

A companion client, `pastebin-cli`, is available for interacting with the server API.

### Install

```bash
# Download latest release
curl -q -LSsf -O https://github.com/apimgr/pastebin/releases/latest/download/pastebin-cli-linux-amd64
chmod +x pastebin-cli-linux-amd64
sudo mv pastebin-cli-linux-amd64 /usr/local/bin/pastebin-cli
```

### Configure

```bash
# Target a custom server (default: https://pste.us)
pastebin-cli --server https://your-server.example.com create myfile.txt
```

### Usage

```bash
# Create a paste from stdin
echo "Hello, World!" | pastebin-cli create

# Create from file
pastebin-cli create myfile.go --lang go --expiry 1d

# Fetch paste content
pastebin-cli get abc12345

# Delete paste
pastebin-cli delete abc12345 <owner-token>

# List recent pastes
pastebin-cli list --limit 20
```

## Configuration

The server reads `/etc/apimgr/pastebin/server.yml` on Linux (created automatically on
first run). All settings can be overridden via CLI flags.

### Server (`pastebin`) Environment Variables

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
| `SITE_TITLE` | `Pastebin` | Site title shown in the web UI |
| `THEME` | `dark` | UI theme: `dark`, `light`, or `auto` |
| `MAX_SIZE` | `10mb` | Maximum paste size; accepts `kb`/`mb`/`gb`/`tb` suffixes, a bare number (defaults to MB), or `0`/negative for unlimited |
| `MAX_SIZE_BYTES` | unset | Deprecated alias for `MAX_SIZE` in raw bytes |
| `NO_COLOR` | unset | Set to any value to disable ANSI color output |
| `_DAEMON_CHILD` | unset | Internal: set by `--daemon` to mark the child process |

Platform default paths:

| Platform | Config | Data | Logs |
|----------|--------|------|------|
| Linux (root/service) | `/etc/apimgr/pastebin/` | `/var/lib/apimgr/pastebin/` | `/var/log/apimgr/pastebin/` |
| Linux (user) | `~/.config/apimgr/pastebin/` | `~/.local/share/apimgr/pastebin/` | `~/.local/log/apimgr/pastebin/` |
| Container | `/config/pastebin/` | `/data/pastebin/` | `/data/log/pastebin/` |
| macOS (user) | `~/Library/Application Support/apimgr/pastebin/` | same | `~/Library/Logs/apimgr/pastebin/` |
| Windows (user) | `%AppData%\apimgr\pastebin\` | `%LocalAppData%\apimgr\pastebin\` | `%LocalAppData%\apimgr\pastebin\logs\` |

### Client (`pastebin-cli`) Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PASTEBIN_SERVER_PRIMARY` | `https://pste.us` (embedded) | Server base URL override for self-hosted instances |
| `PASTEBIN_TOKEN` | unset | Operator/owner API token (overridden by `--token`; falls back to `cli.yml` `auth.token`) |
| `PASTEBIN_SERVER_TIMEOUT` | `cli.yml` value | Server request timeout override |
| `PASTEBIN_SERVER_RETRY` | `cli.yml` value | Retry count override |
| `PASTEBIN_SERVER_RETRY_DELAY` | `cli.yml` value | Retry delay override |
| `PASTEBIN_SERVER_API_VERSION` | `cli.yml` value | API version override |
| `PASTEBIN_OUTPUT_FORMAT` | `cli.yml` value | Output format override (`json`, `table`, `plain`) |
| `PASTEBIN_OUTPUT_COLOR` | `auto` | Color output override (`auto`, `yes`, `no`) |
| `PASTEBIN_DEBUG` | `cli.yml` value | Debug output override (truthy/falsy) |
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

- Check logs: `docker logs pastebin-app`
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
git clone https://github.com/apimgr/pastebin
cd pastebin

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
./pastebin

# Custom address and port
./pastebin --address 127.0.0.1 --port 8080

# Show version
./pastebin --version

# Show status
./pastebin --status
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
