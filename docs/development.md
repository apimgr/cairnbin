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

# Build for current platform only
make dev

# Run tests
make test

# Build Docker image
make docker
```

## Project Structure

```
src/
  main.go          — server binary entry point
  client/          — cairnbin-cli client binary
  config/          — configuration loading
  database/        — SQLite database layer
  handler/         — HTTP request handlers
  model/           — data models
  path/            — platform-aware path resolution
  pid/             — PID file management
  scheduler/       — built-in cron scheduler
  server/          — HTTP server and routing
  shell/           — shell completion generation
docker/
  Dockerfile       — production container (:latest)
  Dockerfile.dev   — development container (:devel)
  Dockerfile.build — CI toolchain image (:build)
docs/              — MkDocs documentation
tests/             — integration test scripts
```

## Running Locally

```bash
# Start in development mode
./cairnbin --mode development --port 8080

# Or via Docker Compose
docker compose -f docker/docker-compose.dev.yml up
```

## Testing

```bash
# Unit tests
go test ./src/...

# Container integration tests
bash tests/docker.sh

# Incus VM tests
bash tests/incus.sh
```

## Scheduler Tasks

The built-in scheduler runs the following tasks automatically:

| Task | Schedule | Description |
|------|----------|-------------|
| `expire-pastes` | Every 10 min | Remove expired and burned pastes |
| `ssl_renewal` | Daily 03:00 | Renew SSL certificates |
| `geoip_update` | Sunday 03:00 | Update GeoIP databases |
| `blocklist_update` | Daily 04:00 | Update IP/domain blocklists |
| `cve_update` | Daily 05:00 | Update CVE database |
| `token_cleanup` | Every 15 min | Remove expired tokens |
| `log_rotation` | Daily 00:00 | Rotate and compress logs |
| `backup_daily` | Daily 02:00 | Full daily backup |
| `backup_hourly` | Hourly (disabled) | Hourly incremental backup |
| `healthcheck_self` | Every 5 min | Self-health check |
| `tor_health` | Every 10 min | Tor connectivity check |
