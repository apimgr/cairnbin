# Integrations

Cairnbin is a drop-in replacement for pastebin.com, microbin, lenpaste, stikked, hastebin/haste-server, dpaste, the curl-upload family (0x0.st, sprunge.us, ix.io), and termbin/fiche. Existing clients and tools that target those services work without modification.

## Compatible APIs

| API | Base Path | Description |
|-----|-----------|-------------|
| Native REST | `https://pste.us/api/v1/pastes` | Full-featured JSON API |
| pastebin.com | `https://pste.us/api/api_post.php` | Drop-in replacement |
| lenpaste | `https://pste.us/api/v1/new`, `https://pste.us/api/v1/get` | Compatible paste creation/retrieval |
| microbin | `https://pste.us/api/v1/pasta`, `https://pste.us/upload`, `https://pste.us/p/{id}` | Compatible API, upload and retrieval |
| stikked | `https://pste.us/api/create`, `https://pste.us/view/{id}` | Compatible create and view routes |
| hastebin / haste-server | `https://pste.us/documents` | Compatible document create and retrieval routes |
| dpaste | `https://pste.us/api/`, `https://pste.us/api/v2/` | Compatible create routes |
| curl-upload family | `https://pste.us/` | 0x0.st, sprunge.us, and ix.io field-dispatched upload protocols |
| termbin / fiche | TCP port `9999` | Raw-TCP upload protocol, enabled by default |
| GraphQL | `https://pste.us/api/v1/server/graphql` | Read-only query interface (GraphiQL UI at `/server/docs/graphql`) |

Recognized compatibility subdomains are routed automatically when a target label is the leftmost DNS label; no `server.yml` setting is required.

| Label(s) | Compatibility target |
|----------|----------------------|
| `pb`, `pastebin` | pastebin.com |
| `lp`, `lenpaste` | lenpaste |
| `sk`, `stikked` | stikked |
| `hb`, `hastebin` | hastebin / haste-server |
| `dp`, `dpaste` | dpaste |
| `mb`, `microbin` | microbin |
| `tb`, `termbin` | termbin / fiche |
| `ix` | ix.io |
| `sprunge` | sprunge.us |
| `0x0`, `st` | 0x0.st |

## Native REST API

```bash
# Create a paste
curl -q -LSsf -X POST https://pste.us/api/v1/pastes \
  -H "Content-Type: application/json" \
  -d '{"content":"hello world","language":"text"}'

# Get a paste
curl -q -LSsf https://pste.us/api/v1/pastes/{id}

# Get raw text
curl -q -LSsf https://pste.us/api/v1/pastes/{id}/raw

# Delete a paste
curl -q -LSsf -X DELETE https://pste.us/api/v1/pastes/{id} \
  -H 'Authorization: Bearer {owner_token}'

# List pastes
curl -q -LSsf https://pste.us/api/v1/pastes
```

## pastebin.com Compatibility

```bash
curl -q -LSsf -X POST https://pste.us/api/api_post.php \
  -d "api_option=paste&api_paste_code=hello+world&api_paste_format=text"
```

## lenpaste Compatibility

```bash
curl -q -LSsf -X POST https://pste.us/api/v1/new \
  -d "text=hello+world"
```

## MCP (AI clients)

AI clients (Claude, IDE agents, other MCP clients) can use the paste API as tools through the Model Context Protocol endpoint. It is Streamable HTTP, JSON-RPC 2.0 over `POST`:

| Route | Notes |
|-------|-------|
| `POST https://pste.us/api/v1/server/mcp` | Canonical endpoint |
| `POST https://pste.us/api/mcp`, `POST https://pste.us/mcp` | Aliases, same handler |

- Connection instructions, the live public tool list, and project usage notes are at `https://pste.us/server/help/mcp`; the JSON mirror is `GET /api/v1/server/help/mcp`. Both help routes are always available.
- Authentication is the API's own `Authorization: Bearer own_...` header. There is no MCP token. Create a linked token on `/server/preferences/tokens` for each MCP client so it can be revoked on its own.
- Tools are generated from the API routes: `getApiV1Pastes`, `postApiV1Pastes`, `getApiV1PastesId`, `getApiV1PastesIdRaw`, `deleteApiV1PastesId`, `getApiV1ServerHealthz`, and `getApiV1ServerVersion`. Token management is never a tool.
- `getApiV1Pastes` lists public pastes by default; pass `scope: "owned"` with an owner or linked Bearer token to list that owner's public and unlisted pastes.
- The MCP endpoint requires the same API Bearer token on every request, including `tools/list`; use a separately created linked token for each client. Token issuance is never exposed as a tool.
- `GET` and `DELETE` return `405`. MCP is always available.

```bash
curl -q -LSsf -X POST https://pste.us/mcp -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer own_...' -H 'MCP-Protocol-Version: 2026-07-28' \
  -H 'Mcp-Method: tools/list' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}'
```

## Discovery & Protocol Endpoints

| Endpoint | Purpose |
|----------|---------|
| `/api/autodiscover` | Unversioned client bootstrap: `name`, `version`, `api`, `urls`, `cli_versions`, and `cli_min_version`; live onion and I2P addresses appear only when that overlay is enabled, its `server.tor.expose` or `server.i2p.expose` setting is true, and the address is available. No settings or config schema is returned. |
| `/server/docs/swagger`, `/server/docs/graphql` | Swagger UI and GraphiQL |
| `/server/help`, `/server/help/mcp` | General API help and the MCP connection guide |
| `/server/preferences`, `/server/preferences/tokens` | Guest preferences and owner-token management |
| `/server/dpo` | Public Data Protection Officer contact page (uses the configured DPO address, then the general contact address; never exposes the admin address) |
| `/server/security`, `/server/security/policy`, `/server/security/thanks`, `/server/security/report/{tracking_id}` | Security overview, disclosure policy, report acknowledgment, and researcher status |
| `/api/swagger`, `/api/graphql`, `/api/healthz` | Unversioned aliases of the versioned `/api/v1/server/*` routes (served directly, never redirected) |
| `/.well-known/llms.txt`, `/llms.txt` | AI-agent discovery document |
| `/sitemap.xml`, `/robots.txt`, `/feed.xml`, `/rss.xml` | Search and syndication metadata |
| `/manifest.json`, `/sw.js` | Base-path-aware PWA manifest and service worker; a 10-second boot watchdog clears stale service workers and Cache API entries once before reloading, then shows the static offline page if recovery fails; updates wait for user acceptance before reloading |
| `9999/tcp` | termbin/fiche raw-TCP listener (`echo text \| nc host 9999`); disable with `server.termbin.enabled: false` or `TERMBIN_ENABLED=false` |

## External Identity

None enabled. The service has no user accounts, no federated or machine-to-machine identity flows; ownership of a paste is proven with its owner token only.

## Platform Integrations

None enabled: no Android App Links, Apple Universal Links, WebFinger, OpenID Provider metadata, MTA-STS, inbound webhooks or federation. Outbound operator notifications are optional: email settings live under `server.notifications.email` in `yaml.d/notifications.yml`; role-specific webhook destinations live under `server.contact.{admin,security,abuse,general}.webhooks` in `yaml.d/contact.yml`. Built-in webhook transports include Telegram, Discord, Slack, Mattermost, Pushover, and Gotify; `generic` sends JSON by HTTPS POST. The server signs each delivery; webhook payloads contain summaries only and never include report bodies or contact details. See the [configuration reference](configuration.md) for the contact-role fallback rules and secret handling.

## Operator Notes

- Everything above is on by default except the optional pieces: PGP publishing, the `/healthz` root alias, I2P, and operator notifications. MCP and metrics are always enabled. The termbin listener is enabled by default on TCP port `9999`.
- Nothing needs to be configured before use; each optional integration is switched on in its assigned `yaml.d/` area file and picked up by live reload (listen address and port need a restart).

## CLI Tool

See the [CLI Reference](cli.md) for the `cairnbin-cli` tool.
