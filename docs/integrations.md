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

- Connection instructions, the live public tool list, and project usage notes are at `https://pste.us/server/help/mcp`; the JSON mirror is `GET /api/v1/server/help/mcp`. Both help routes return `404` when MCP is disabled.
- Authentication is the API's own `Authorization: Bearer tok_...` header. There is no MCP token. Create a linked token on `/server/preferences/tokens` for each MCP client so it can be revoked on its own.
- Tools are generated from the API routes: `getApiV1Pastes`, `postApiV1Pastes`, `getApiV1PastesId`, `getApiV1PastesIdRaw`, `deleteApiV1PastesId`, `getApiV1ServerHealthz`, and `getApiV1ServerVersion`. Token management is never a tool.
- `getApiV1Pastes` lists public pastes by default; pass `scope: "owned"` with an owner or linked Bearer token to list that owner's public and unlisted pastes.
- An anonymous `postApiV1Pastes` call returns the `owner_token` in the tool result; store it to keep control of the paste.
- `GET` and `DELETE` return `405`. Disable with `server.mcp.enabled: false` (every MCP route then returns `404`).

```bash
curl -q -LSsf -X POST https://pste.us/mcp -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
```

## Discovery & Protocol Endpoints

| Endpoint | Purpose |
|----------|---------|
| `/api/autodiscover` | Unversioned client bootstrap: server name, version, API version, base URL, feature flags and `cli_min_version` |
| `/server/docs/swagger`, `/server/docs/graphql` | Swagger UI and GraphiQL |
| `/server/help`, `/server/help/mcp` | General API help and the MCP connection guide (MCP guide is hidden when disabled) |
| `/server/preferences`, `/server/preferences/tokens` | Guest preferences and owner-token management |
| `/server/security`, `/server/security/policy`, `/server/security/thanks`, `/server/security/report/{tracking_id}` | Security overview, disclosure policy, report acknowledgment, and researcher status |
| `/api/swagger`, `/api/graphql`, `/api/healthz` | Unversioned aliases of the versioned `/api/v1/server/*` routes (served directly, never redirected) |
| `/.well-known/llms.txt`, `/llms.txt` | AI-agent discovery document |
| `/sitemap.xml`, `/robots.txt`, `/manifest.json`, `/sw.js` | Search, crawler and PWA metadata |
| `9999/tcp` | termbin/fiche raw-TCP listener (`echo text \| nc host 9999`); disable with `server.termbin.enabled: false` or `TERMBIN_ENABLED=false` |

## External Identity

None enabled. The service has no user accounts, no federated or machine-to-machine identity flows; ownership of a paste is proven with its owner token only.

## Platform Integrations

None enabled: no Android App Links, Apple Universal Links, WebFinger, OpenID Provider metadata, MTA-STS, inbound webhooks or federation. Outbound operator notifications (email and chat webhooks) are optional and configured only under `server.notifications` in `server.yml`.

## Operator Notes

- Everything above is on by default except the optional pieces: PGP publishing, the `/healthz` root alias, I2P, and operator notifications. MCP and metrics are enabled by default; MCP can be disabled with `server.mcp.enabled: false`. The termbin listener is enabled by default on TCP port `9999`.
- Nothing needs to be configured before use; each optional integration is switched on in `server.yml` and picked up by live reload (listen address and port need a restart).

## CLI Tool

See the [CLI Reference](cli.md) for the `cairnbin-cli` tool.
