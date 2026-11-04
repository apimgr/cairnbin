# API Reference

Base URL: `https://pste.us` (official site) or your self-hosted instance  
API version prefix: `/api/v1`

All responses include `Access-Control-Allow-Origin: *`.

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
    "owner_token": "tok_raw-plaintext-token-shown-once-only"
  }
}
```

`visibility` is `0` for public and `1` for unlisted.

!!! warning
    The `owner_token` is shown **once only** at creation. Store it securely. Loss of the token means the paste cannot be deleted before natural expiry.

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

`visibility` is `0` for public and `1` for unlisted. `content_type` is empty for plain-text pastes and set to the detected MIME type for binary uploads.

If the paste is a link (`is_link: true`), the frontend and API view routes (`GET /{id}` and `GET /api/v1/pastes/{id}`) respond with a `302 Found` redirect to the target URL instead of the JSON body above. Use the `/raw` endpoint to retrieve the target URL as plain text without redirecting.

## Link Pastes

A paste automatically becomes a redirecting link when its entire trimmed content is exactly one absolute `http://` or `https://` URL and nothing else — no extra whitespace, no additional lines, no surrounding text. There is no request field, header, or checkbox to opt in or out; it is derived purely from content shape:

```bash
curl -X POST https://pste.us/api/v1/pastes \
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

Authenticate with the `owner_token` returned once at creation (the global operator `server.token` can delete any paste). The token is also accepted via the `X-Delete-Token` header or a `?token=<owner-token>` query parameter.

Or via query parameter: `DELETE https://pste.us/api/v1/pastes/{id}?token=<owner-token>`

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
GET https://pste.us/api/v1/pastes?page=1&limit=20
```

Returns paginated list of public (non-unlisted) pastes.

## Health Check

```
GET https://pste.us/server/healthz
GET https://pste.us/api/v1/server/healthz
GET https://pste.us/api/healthz
```

`/server/healthz` is content negotiated: HTML for browsers, JSON for API clients, plain text for CLI. The two API routes always return JSON; `/api/healthz` is a direct alias (no redirect). An optional `/healthz` alias exists when `server.healthz.root.enabled: true`.

### Server pages

```
GET  https://pste.us/api/v1/server/about
GET  https://pste.us/api/v1/server/privacy
GET  https://pste.us/api/v1/server/help
GET  https://pste.us/api/v1/server/terms
POST https://pste.us/api/v1/server/contact
```

The GET routes return `{"ok": true, "data": {...}}` with the same facts the matching `/server/*` page shows. `POST /server/contact` takes a JSON body (`name`, `email`, `subject`, `message`, plus `captcha_token` and `captcha` when the built-in captcha is on) and follows the same rules as the web form.

## Guest Preferences

Guest UI preferences (theme, language, list preferences) are cookie-based. The JSON mirror of the preferences form:

```bash
# Current preferences
curl -H 'Accept: application/json' https://pste.us/api/v1/server/preferences

# Partial update: only the fields you send are changed
curl -X POST https://pste.us/api/v1/server/preferences \
  -H 'Content-Type: application/json' \
  -d '{"theme":"light","lang":"es","prefs":{"sort":"name"}}'
```

The response is `{"ok":true,"data":{"applied":{...},"ignored":[...]}}`. Invalid values are listed in `ignored` instead of failing the request, and the same cookies as the web form are set.

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

## Raw Body (curl pipe)

```bash
echo "Hello" | curl -X POST https://pste.us/api/v1/pastes \
  --data-binary @- -H 'Content-Type: text/plain'
```
