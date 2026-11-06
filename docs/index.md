# Cairnbin

A fast, full-stack Go web application for creating and sharing text snippets anonymously.
Drop-in replacement for pastebin.com, microbin, and lenpaste.

## Official Site

**[https://pste.us](https://pste.us)** — the official hosted instance. The CLI client
uses it as the default server; use `--server <url>` to target your own instance.

## Features

- Anonymous paste creation via web form, JSON API, raw body, or multipart file upload
- MIME-detected images, audio, and video render as safe media; active formats such as SVG and HTML are served as downloads
- Server-side syntax highlighting via Chroma — no client-side JS required
- Language auto-detection from file extension on upload
- Expiry options: `1h`, `1d`, `1w`, `1m`, `3m`, `6m`, `1y`, `18m`, `2y`, `never`, or custom seconds
- Burn after N reads — paste deleted once view count reaches threshold
- Public and unlisted visibility
- Owner token — cryptographically random `tok_` value returned when a new owner is created, stored as a SHA-256 hash plus an encrypted recoverable copy, and viewable masked by default at `/server/preferences/tokens`
- QR code generation at `/qr/{id}`
- View count tracking with automatic background cleanup
- Full web frontend (dark/light/auto theme, PWA, mobile-first)
- CLI client (`cairnbin-cli`)
- OpenAPI/Swagger docs at `/api/v1/server/swagger`
- Server information at `/server/about`, `/server/contact`, `/server/help`, `/server/privacy`, and `/server/terms`; the MCP guide is at `/server/help/mcp` when enabled
- Guest preferences at `/server/preferences`; owner-token management at `/server/preferences/tokens`
- Security overview, policy, acknowledgment, and report status at `/server/security` and its subroutes
- GraphQL at `/api/v1/server/graphql` (GraphiQL UI at `/server/docs/graphql`, read-only)
- Full route compatibility with pastebin.com, microbin, and lenpaste
- Single self-contained static binary with embedded SQLite

## Quick Start

```bash
# Docker
docker run --name "cairnbin-$(tr -dc 'a-z0-9' </dev/urandom | head -c8)" -p 172.17.0.1:64580:80 ghcr.io/apimgr/cairnbin:latest

# Binary
./cairnbin-linux-amd64
```

On first run the server binds all interfaces on a random port in the 64000-64999 range and prints the URL to the console. Open that URL in your browser, or pass `--port` to pin a specific port.

## Documentation

- [Installation](installation.md) - How to install and run
- [Configuration](configuration.md) - All configuration options
- [API Reference](api.md) - REST API, Swagger, GraphQL
- [CLI Reference](cli.md) - `cairnbin-cli` flags and commands
- [Compatibility](compat.md) - pastebin.com, microbin, lenpaste and more
- [Security](security.md) - Auth, public endpoints, reporting, and hardening behavior
- [Integrations](integrations.md) - Discovery, protocol endpoints, and platform integrations
- [Development](development.md) - Contributing guide

## Links

- [Official Site](https://pste.us)
- [Live Demo](https://pste.us)
- [API Documentation](https://pste.us/server/docs/swagger) (Swagger UI)
- [GraphQL Playground](https://pste.us/server/docs/graphql)
- [Source on GitHub](https://github.com/apimgr/cairnbin)

## License

MIT - See [LICENSE.md](https://github.com/apimgr/cairnbin/blob/main/LICENSE.md)
