# Development

## Prerequisites

- Docker (all builds and tests run in the `casjaysdev/go:latest` toolchain
  image, which always carries the latest stable Go — no host Go install is
  required or supported)
- make

## Building

```bash
# Build all binaries (uses Docker toolchain image)
make build

# Build for the current platform with version metadata
make local

# Quick development build; prints the temporary output directory
make dev

# Run tests
make test

# Build and publish the multi-architecture Docker image
make docker
```

`make dev` writes unversioned binaries to the temporary directory it prints.
Run the server or client from that directory. `make local` writes binaries to
`binaries/`. The `docker` target pushes its image to the configured registry;
it is not a local-only image build.

## Project Structure

```
src/
  main.go          — server binary entry point
  client/          — cairnbin-cli client binary
  config/          — configuration loading
  database/        — SQLite and libSQL database layer
  handler/         — HTTP request handlers
  model/           — data models
  paths/           — platform-aware path resolution
  pid/             — PID file management
  scheduler/       — built-in scheduler
  task/            — scheduled task implementations
  server/          — HTTP server, pages, templates, and routing
  server/static/   — embedded CSS and JavaScript
  server/template/ — embedded HTML and email templates
  common/          — shared i18n, terminal, theme, URL, and HTTP utilities
  client/tui/      — interactive terminal client
  client/gui/      — graphical client
  graphql/         — GraphQL API and browser UI
  swagger/         — OpenAPI generation and browser UI
  mcp/             — Model Context Protocol API
  geoip/           — GeoIP database and request filtering
  tor/, i2p/       — optional overlay network services
  service/         — operating-system service management
  maintenance/     — administrative maintenance operations
  updater/         — binary update support
  shell/           — shell completion generation
docker/
  Dockerfile       — production container (:latest)
  Dockerfile.dev   — development container (:devel)
docs/              — MkDocs documentation
tests/             — integration test scripts
```

## Running Locally

```bash
# Build and start in development mode
make dev
# Run the printed temporary binary path with the documented options:
<temporary-directory>/cairnbin --mode development --port 8080

# Docker Compose must run from a project-scoped temporary directory so its
# relative volume mounts do not write runtime data into the source checkout.
mkdir -p "${TMPDIR:-/tmp}/apimgr"
DEV_DIR=$(mktemp -d "${TMPDIR:-/tmp}/apimgr/cairnbin-XXXXXX")
cp docker/docker-compose.dev.yml "$DEV_DIR/docker-compose.yml"
(cd "$DEV_DIR" && docker compose up -d)

# Stop the stack and remove its temporary runtime data when finished.
if [[ -n "${DEV_DIR:-}" && "$DEV_DIR" == "${TMPDIR:-/tmp}/apimgr/cairnbin-"* ]]; then
  (cd "$DEV_DIR" && docker compose down)
  rm -rf -- "$DEV_DIR"
fi
```

## Testing

```bash
# Unit tests
make test

# Docker-only basic binary validation
bash tests/run_tests.sh

# Full application and systemd integration tests in an Incus container
bash tests/incus.sh
```

Go build and test commands run inside the project Docker toolchain image;
use the Makefile targets instead of running `go` directly on the host.

## Scheduler Tasks

The built-in scheduler runs the following tasks automatically:

| Task | Schedule | Description |
|------|----------|-------------|
| `expire-pastes` | Every 10 min | Remove expired and burned pastes |
| `ssl_renewal` | Daily 03:00 | Renew app-managed Let's Encrypt certificates when 30 days or fewer remain; warn and alert on expiring certificates |
| `blocklist_update` | Daily 04:00 | Update IP/domain blocklists |
| `cve_update` | Daily 05:00 | Update CVE/NVD and Trivy vulnerability databases |
| `update_check` | Daily 06:00 | Check the configured release channel; notify only unless automatic installation is enabled |
| `backup_daily` | Daily 02:00 | Full backup and daily incremental backup |
| `geoip_update` | Sunday 03:00 | Update GeoIP databases (only when GeoIP is enabled) |
| `log_rotation` | Daily 00:00 | Rotate and compress logs |
| `public_ip_refresh` | Every 12 hours | Refresh the cached public IP (also runs at startup) |
| `ip_block_release` | Every minute | Release expired temporary IP blocks |
| `token_cleanup` | Every 15 min | Expire CAPTCHA challenges and apply owner-token idle-purge policy |
| `backup_hourly` | Hourly (disabled) | Hourly incremental backup |
| `disk_check` | Every 5 min | Check available disk space and report low-space conditions |
| `healthcheck_self` | Every 5 min | Self-health check |
| `dataset_health` | Every 60 sec | Refresh cached data-set health checks without making health requests read data files |
| `tor_health` | Every 10 min | Tor connectivity check |
| `i2p_health` | Every 10 min when I2P is enabled | I2P connectivity check; startup is asynchronous and failure degrades health without stopping the HTTP listener |

Remote branding images refresh lazily when a cached image is requested after
24 hours. This fixed interval is part of the branding cache and is not a
scheduler task or operator setting.
