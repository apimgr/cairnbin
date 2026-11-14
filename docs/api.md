# API Reference

Base URL: `https://pste.us` (official site) or your self-hosted instance  
API version prefix: `/api/v1`

Protected native API routes accept credentials only through `Authorization: Bearer {token}`. Alternate token headers and `?token=` are ignored. Compatibility routes keep the credential formats required by their emulated protocols.

CORS defaults to `Access-Control-Allow-Origin: *`; configured allowlists echo only an allowed origin, and credentials are enabled only for explicit origins. See the CORS section in `docs/configuration.md`.

## Create Paste

```
POST https://pste.us/api/v1/pastes
```

**Request body** (JSON, form, or multipart):

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `content` | string | required | Paste body text. If the entire trimmed content is exactly one absolute `http://`/`https://` URL and nothing else, the paste is auto-detected as a link — there is no field to request this explicitly |
| `title` | string | `""` | Optional title |
| `language` | string | `text` | Chroma language identifier; ignored when content is auto-detected as a link |
| `visibility` | string | `public` | `public` or `unlisted` |
| `expires_in` | string | `never` | `1h`, `1d`, `1w`, `1m`, `3m`, `6m`, `1y`, `18m`, `2y`, `never`, or seconds |
| `burn_after` | integer | `0` | Delete after N views; `0` = disabled; max `9999` |

**Response** (201 Created):

```json
{
  "ok": true,
  "data": {
    "id": "abc12345",
    "title": "",
    "language": "text",
    "visibility": 0,
    "is_link": false,
    "burn_after": 0,
    "expires_at": null,
    "views": 0,
    "created_at": "2025-01-01T00:00:00Z",
    "link": "https://pste.us/abc12345",
    "owner_token": "tok_AbCdEfGh12345678ijklMNOP87654321"
  }
}
```

`visibility` is `0` for public and `1` for unlisted.

!!! warning
    A new owner receives an `owner_token` in the creation response. It remains viewable, masked by default, at `/server/preferences/tokens`; use a linked token for long-lived clients.

## Frontend Content Negotiation

Frontend resource routes use the same client detection as the API. Browsers receive HTML, known HTTP tools receive text, and explicit `Accept` values win in this order: `application/json`, `text/html`, then `text/plain`.

| Frontend route | JSON response |
|---------------|---------------|
| `GET /` | `{ "ok": true, "data": [...] }` with recent public pastes |
| `GET /pastes` | `{ "ok": true, "data": [...], "pagination": {...} }` with the requested list and pagination |
| `GET /{id}` | The same paste object envelope as `GET /api/v1/pastes/{id}`; link pastes redirect to their target |
| `POST /pastes` | JSON create response when `Accept: application/json` is sent |

Use `Accept: text/plain` for the CLI-friendly text representation, or `Accept: text/html` to force the browser page.

## Get Paste

```
GET https://pste.us/api/v1/pastes/{id}
```

**Response** (200 OK):

```json
{
  "ok": true,
  "data": {
    "id": "abc12345",
    "title": "My Paste",
    "content": "Hello, World!",
    "content_type": "",
    "language": "text",
    "visibility": 0,
    "is_link": false,
    "burn_after": 0,
    "expires_at": null,
    "views": 3,
    "created_at": "2025-01-01T00:00:00Z",
    "updated_at": "2025-01-01T00:00:00Z"
  }
}
```

`visibility` is `0` for public and `1` for unlisted. `content_type` is empty for text pastes and set to the detected MIME type for binary uploads. Multipart uploads use file bytes and recognized filename extensions to identify formats Go's byte sniffer cannot distinguish, including DOCX and other Office documents. Raster images render as images and Markdown files receive a sanitized preview; other document formats are available through the download endpoint.

If the paste is a link (`is_link: true`), the frontend and API view routes (`GET /{id}` and `GET /api/v1/pastes/{id}`) respond with a `302 Found` redirect to the target URL instead of the JSON body above. Use the `/raw` endpoint to retrieve the target URL as plain text without redirecting.

## Link Pastes

A paste automatically becomes a redirecting link when its entire trimmed content is exactly one absolute `http://` or `https://` URL and nothing else — no extra whitespace, no additional lines, no surrounding text. There is no request field, header, or checkbox to opt in or out; it is derived purely from content shape:

```bash
curl -q -LSsf -X POST https://pste.us/api/v1/pastes \
  --header "Content-Type: application/json" \
  --data '{"content":"https://example.com/some/long/path"}'
```

- Only `http://` and `https://` target URLs are accepted; anything else (extra text, multiple lines, other schemes) is stored as a normal text paste instead.
- Short IDs are auto-generated — there is no custom/vanity slug support.
- The target URL is never fetched server-side.
- `GET /{id}` (web and API) issues a `302 Found` redirect to the target.
- `GET /api/v1/pastes/{id}/raw` (and the frontend `/view/raw`, `/dl` routes) always return the target URL as plain text, without redirecting, regardless of `is_link`.
- Expiry, burn-after-read, visibility, and owner-token deletion all work identically to text pastes.

## Delete Paste

```
DELETE https://pste.us/api/v1/pastes/{id}
Authorization: Bearer <owner-token>
```

Authenticate with the `owner_token` returned at creation by sending `Authorization: Bearer <owner-token>`. Owner tokens remain viewable, masked by default, at `/server/preferences/tokens`. The global operator `server.token` can delete any paste. Other headers and query parameters are ignored on native API routes.

**Response** (200 OK):

```json
{
  "ok": true,
  "data": {
    "message": "paste deleted"
  }
}
```

## Raw Paste Text

```
GET https://pste.us/api/v1/pastes/{id}/raw
```

Returns plain text.

## List Recent Pastes

```
GET https://pste.us/api/v1/pastes?page=1&limit=20&type=image
```

Returns paginated list of public (non-unlisted) pastes. `page` defaults to 1;
`limit` defaults to 250 and is capped at 250. The optional `search` parameter
matches paste ID, title, or content. The optional `type` parameter filters by
`text`, `image`, `audio`, `video`, or `file`.

Set `scope=owned` and send a valid owner or linked token to list that owner's
non-expired pastes, including unlisted ones:

```bash
curl -q -LSsf 'https://pste.us/api/v1/pastes?scope=owned' \
  -H 'Authorization: Bearer {owner_token}'
```

Without a valid owner token, `scope=owned` returns `401`. This option is also
available to MCP clients through the generated `getApiV1Pastes` tool's `scope`
argument.

## Health Check

```
GET https://pste.us/server/healthz
GET https://pste.us/api/v1/server/healthz
GET https://pste.us/api/healthz
```

`/server/healthz` and the optional `/healthz` root alias use the same content negotiation: `Accept: application/json` returns the health JSON (and takes precedence when the header also lists HTML or plain text), `Accept: text/html` returns HTML, and `Accept: text/plain` or a CLI user agent returns plain text. The API routes return JSON by default; `Accept: application/json` takes precedence over `Accept: text/plain`, while `.txt`, `Accept: text/plain`, or a curl-style client requests plain text. `/api/healthz` is a direct alias of `/api/v1/server/healthz` with no redirect.

### Server pages

```
GET  https://pste.us/api/v1/server/about
GET  https://pste.us/api/v1/server/privacy
GET  https://pste.us/api/v1/server/help
GET  https://pste.us/api/v1/server/help/mcp   # 404 when MCP is disabled
GET  https://pste.us/api/v1/server/terms
GET  https://pste.us/api/v1/server/contact
POST https://pste.us/api/v1/server/contact
GET  https://pste.us/api/v1/server/auth/check
```

The GET routes return `{"ok": true, "data": {...}}` with the same facts the matching `/server/*` page shows. `GET /server/contact` describes the form fields, required flags, character limits, captcha provider, security URL and public abuse email; it never returns recipient addresses. Name, email, subject and message limits are 100, 254, 200 and 5000 Unicode characters after trimming. `POST /server/contact` takes the same JSON fields (plus `captcha_token` and `captcha` when required) and follows the web form's validation.

`GET /server/auth/check` accepts only `Authorization: Bearer {token}`. It returns `200` with `data.kind` set to `operator` or `owner` for a valid credential, and `401` for a missing or invalid token. The check is rate-limited, does not update token activity, and never returns the token.

## Guest Preferences

Guest UI preferences (theme, language, list preferences) are cookie-based. The JSON mirror of the preferences form:

```bash
# Current preferences
curl -q -LSsf -H 'Accept: application/json' https://pste.us/api/v1/server/preferences

# Partial update: only the fields you send are changed
curl -q -LSsf -X POST https://pste.us/api/v1/server/preferences \
  -H 'Content-Type: application/json' \
  -d '{"theme":"light","lang":"es","cairnbin_pref_sort":"name"}'

# A non-empty import code takes precedence over the submitted preference fields
curl -q -LSsf -X POST https://pste.us/api/v1/server/preferences \
  -H 'Content-Type: application/json' \
  -d '{"code":"bGFuZz1mciZ0aGVtZT1kYXJr"}'

# Export and import (stateless; the code or URL IS the preference values)
curl -q -LSsf -H 'Accept: application/json' https://pste.us/api/v1/server/preferences/export
curl -q -LSsf 'https://pste.us/api/v1/server/preferences/import?code=bGFuZz1mciZ0aGVtZT1kYXJr'
```

Every response carries `{"ok":true,"data":{"theme","lang","prefs"}}`; `prefs` is keyed by the bare preference key and is `{}` when none are set. Export adds `url` and `code`. Import and save add `applied` and `ignored` arrays of parameter names. Invalid values are listed in `ignored` instead of failing the request; save returns `400 VALIDATION_FAILED` only when no supplied value was valid. Only `theme`, `lang` and `cairnbin_pref_*` are ever read or written; consent and credential cookies are never part of the exchange. A non-empty import `code` overrides preference fields in the same request. The web `GET /server/preferences/import` and `POST /server/preferences` answer `303 See Other`.

### Owner Token Management

The HTML page is `GET /server/preferences/tokens`. Its web action forms use the primary owner cookie and CSRF protection. The API mirror requires the primary owner's Bearer token; linked tokens cannot manage tokens.

| Method | API route | Purpose |
|--------|-----------|---------|
| `GET` | `/api/v1/server/preferences/tokens` | List masked owner tokens |
| `POST` | `/api/v1/server/preferences/tokens/primary/reveal` | Reveal the primary token |
| `POST` | `/api/v1/server/preferences/tokens/primary/rotate` | Rotate the primary token |
| `POST` | `/api/v1/server/preferences/tokens` | Create a linked token |
| `POST` | `/api/v1/server/preferences/tokens/{id}/reveal` | Reveal a linked token |
| `POST` | `/api/v1/server/preferences/tokens/{id}/rotate` | Rotate a linked token |
| `PATCH` | `/api/v1/server/preferences/tokens/{id}` | Rename or change expiry |
| `DELETE` | `/api/v1/server/preferences/tokens/{id}` | Revoke a linked token |

Restore by pasting the primary token is web-only (`POST /server/preferences/token/restore`).

## OpenAPI / Swagger

Interactive API docs (Swagger UI) at `https://pste.us/server/docs/swagger`. The OpenAPI document is served at `https://pste.us/api/v1/server/swagger` with the unversioned alias `https://pste.us/api/swagger`.

## GraphQL

Endpoint: `https://pste.us/api/v1/server/graphql` (unversioned alias: `https://pste.us/api/graphql`). Interactive GraphiQL UI: `https://pste.us/server/docs/graphql`.

```graphql
# Get a paste
query {
  paste(id: "abc12345") {
    id
    title
    content
    language
    views
    createdAt
  }
}

# List recent pastes
query {
  pastes(page: 1, limit: 20) {
    id
    title
    language
    views
    createdAt
  }
}
```

Mutations are not available via GraphQL — use REST for create and delete.

## Raw Body (curl -q -LSsf pipe)

```bash
echo "Hello" | curl -q -LSsf -X POST https://pste.us/api/v1/pastes \
  --data-binary @- -H 'Content-Type: text/plain'
```
