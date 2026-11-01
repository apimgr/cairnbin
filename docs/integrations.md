# Integrations

Pastebin is a drop-in replacement for [pastebin.com](https://pastebin.com), [microbin](https://microbin.eu), and [lenpaste](https://lenpaste.net). Existing clients and tools that target any of those services work without modification.

## Compatible APIs

| API | Base Path | Description |
|-----|-----------|-------------|
| Native REST | `https://pste.us/api/v1/pastes` | Full-featured JSON API |
| pastebin.com | `https://pste.us/api/api_post.php` | Drop-in replacement |
| lenpaste | `https://pste.us/api/v1/new`, `https://pste.us/api/v1/get` | Compatible paste creation/retrieval |
| microbin | `https://pste.us/upload`, `https://pste.us/p/{id}` | Compatible upload and retrieval |
| GraphQL | `https://pste.us/api/v1/server/graphql` | Read-only query interface (GraphiQL UI at `/server/docs/graphql`) |

## Native REST API

```bash
# Create a paste
curl -X POST https://pste.us/api/v1/pastes \
  -H "Content-Type: application/json" \
  -d '{"content":"hello world","language":"text"}'

# Get a paste
curl https://pste.us/api/v1/pastes/{id}

# Get raw text
curl https://pste.us/api/v1/pastes/{id}/raw

# Delete a paste
curl -X DELETE https://pste.us/api/v1/pastes/{id}?token={owner_token}

# List pastes
curl https://pste.us/api/v1/pastes
```

## pastebin.com Compatibility

```bash
curl -X POST https://pste.us/api/api_post.php \
  -d "api_option=paste&api_paste_code=hello+world&api_paste_format=text"
```

## lenpaste Compatibility

```bash
curl -X POST https://pste.us/api/v1/new \
  -d "text=hello+world"
```

## MCP (AI clients)

AI clients (Claude, IDE agents, other MCP clients) can use the paste API as tools through the Model Context Protocol endpoint. It is Streamable HTTP, JSON-RPC 2.0 over `POST`:

| Route | Notes |
|-------|-------|
| `POST https://pste.us/api/v1/server/mcp` | Canonical endpoint |
| `POST https://pste.us/api/mcp`, `POST https://pste.us/mcp` | Aliases, same handler |

- Authentication is the API's own `Authorization: Bearer tok_...` header. There is no MCP token. Create a linked token on `/server/preferences` for each MCP client so it can be revoked on its own.
- Tools are generated from the API routes: `postApiV1Pastes`, `getApiV1Pastes`, `getApiV1PastesId`, `deleteApiV1PastesId`, `getApiV1PastesIdRaw`, `getApiV1ServerHealthz`, `getApiV1ServerVersion`. Token management is never a tool.
- An anonymous `postApiV1Pastes` call returns the `owner_token` in the tool result; store it to keep control of the paste.
- `GET` and `DELETE` return `405`. Disable with `server.mcp.enabled: false` (every MCP route then returns `404`).

```bash
curl -X POST https://pste.us/mcp -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
```

## CLI Tool

See the [CLI Reference](cli.md) for the `pastebin-cli` tool.
