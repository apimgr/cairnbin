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
GET  https://pste.us/api/v1/server/dpo
GET  https://pste.us/api/v1/server/help
GET  https://pste.us/api/v1/server/help/mcp   # 404 when MCP is disabled
GET  https://pste.us/api/v1/server/terms
GET  https://pste.us/api/v1/server/contact
POST https://pste.us/api/v1/server/contact
GET  https://pste.us/api/v1/server/auth/check
```

`GET /api/v1/server/dpo` returns the public contact name and email plus the resolved absolute privacy-page URL. The email uses the configured DPO address, then the general contact address; the admin address is never exposed. If neither public address is configured, `email` is empty.

The GET routes return `{"ok": true, "data": {...}}` with the same facts the matching `/server/*` page shows. `GET /api/v1/server/contact` describes the report fields, conditional requirements, character limits, captcha provider, available types, security URL and public abuse email; it never returns recipient addresses. It also returns a signed `form_token`. Submit `type` as `abuse`, `dmca` or `privacy`, include the required fields for that type, and send an empty `website` honeypot field only on browser forms. Request bodies are limited to 256 KiB and email must be one address. Name, email, subject and message limits are 100, 254, 200 and 5000 Unicode characters after trimming. For the `simple` captcha, GET returns a one-use `captcha_id` and question; POST accepts `captcha_id` with `captcha_answer`. Provider captchas accept `g-recaptcha-response` or `h-captcha-response`, with the simple challenge available as the no-JavaScript fallback. A browser form POST must include a same-origin `Origin`, `Referer` or `Sec-Fetch-Site` header.

Submission takes two POSTs. The first includes `step: "submit"` and returns a `review` object, `review_token`, `confirm_after_seconds` and a fresh `form_token`; it never delivers the report. The second repeats the same report fields with `step: "confirm"`, that `review_token` and `confirmed: true`. The token is single-use, covers the normalized report fields, and enforces the configured review delay. Delivery happens only after confirmation. Abuse and DMCA reports go to the abuse recipient role; privacy requests go to the DPO role. Each accepted submission sends one plain-text email from the configured sender, with the reporter address only in `Reply-To`, and never sends an automatic reply.

`GET /api/v1/server/auth/check` accepts only `Authorization: Bearer {token}`. It returns `200` with `data.kind` set to `operator` or `owner` for a valid credential, and `401` for a missing or invalid token. The check is rate-limited, does not update token activity, and never returns the token.

When `GET /api/v1/server/contact?security_id={id}` returns security-report fields, submit them to `POST /api/v1/server/contact` with `security_id` and the report fields (`researcher_gpg`, `component`, `component_other`, `endpoint`, `severity`, `summary`, `steps`, `impact`, `suggested_fix`, `cve_requested`, `disclosure_days`, `credit_preference`, `credit_name`, and `agreement`). The server revalidates the ID and applies the same two-step review flow; only the confirmed request allocates a `tracking_id` and enters the encrypted report pipeline. GPG URLs are limited to 2048 characters; a pasted key may contain up to 16384 Unicode characters. `credit_name` is required only when `credit_preference` is `handle`; `name` uses the submitted reporter name, and other preferences ignore `credit_name`. A successful confirmation returns a `tracking_id`, sends one best-effort maintainer notification, and may send one researcher acknowledgment. The acknowledgment contains only the tracking ID and private status URL, uses the configured sender, and is never retried; its failure does not fail the report submission.

Security-report text limits are counted in Unicode characters after trimming:

| Field | Maximum |
|-------|---------|
| `name` / `email` | 100 / 254 |
| `researcher_gpg` | 16384 (2048 when it is an HTTPS URL) |
| `component` / `component_other` | 255 |
| `endpoint` | 2048 |
| `summary` / `credit_name` | 200 / 100 |
| `steps` | 10000 |
| `impact` / `suggested_fix` | 5000 |

Enum, checkbox, number, generated and hidden fields have `max_length: null` in the metadata. `credit_name` is required only when `credit_preference` is `handle`; the API descriptor currently reports `required: false` because the shared field schema has no conditional requirement.

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

### Browser privacy controls

These HTML form actions are available to browser visitors and require the
page's CSRF token:

| Method | Route | Form fields | Result |
|--------|-------|-------------|--------|
| `POST` | `/server/consent` | `choice=accept`, `decline`, or `save`; for `save`, optional `preferences` and `analytics` | Saves essential and selected cookie categories in `cookie_consent`, then redirects to the same-site referring page or `/` |
| `POST` | `/server/ccpa` | `choice=opt-in` to clear the opt-out; any other value opts out | Sets or clears the one-year `ccpa_opt_out` cookie, then redirects to the same-site referring page or the privacy page |
| `POST` | `/announcements/dismiss` | `id`; optional same-site relative `return_to` | Adds the banner ID to `dismissed_announcements` for one year and redirects to `return_to` or `/` |

The consent form always preserves essential storage. Analytics consent is only
recorded when server-side tracking is configured. Choosing a narrower consent
state can trigger the configured `Clear-Site-Data` behavior.

## Server Operations

### Version

```http
GET /api/v1/server/version
```

Returns the server version and build information in the standard success
envelope. The endpoint is public.

### Scheduler

The scheduler API is rooted at `/api/v1/schedulers`. Listing tasks, viewing one
task, and viewing its recent history are public read operations. Running,
enabling, or disabling a task requires the operator token in the Bearer
authorization header.

| Method | Route | Access | Purpose |
|--------|-------|--------|---------|
| `GET` | `/api/v1/schedulers` | Public | List scheduled tasks |
| `GET` | `/api/v1/schedulers/{id}` | Public | Show one task |
| `GET` | `/api/v1/schedulers/{id}/history` | Public | Show recent execution history |
| `POST` | `/api/v1/schedulers/{id}/run` | Operator token | Run a task immediately |
| `POST` | `/api/v1/schedulers/{id}/enable` | Operator token | Enable a task |
| `POST` | `/api/v1/schedulers/{id}/disable` | Operator token | Disable a task |

### Browser reports

```http
POST /api/v1/server/reports/{name}
```

Accepts browser security reports for `csp`, `nel`, `default`, `deprecation`,
`intervention`, and `crash`. The server bounds and sanitizes report fields,
applies per-IP rate limits, and returns `204 No Content` without echoing the
submitted body. See [Configuration](configuration.md) for the
`web.reports.*` rate-limit settings.

### Internal metrics

Metrics are available only when `server.metrics.enabled` is enabled. Each
service endpoint is protected by its configured service-specific Bearer token;
these endpoints are intended for internal scrapers and are not public paste
API routes.

| Service | Canonical route | Aliases |
|---------|----------------|---------|
| Prometheus | `/server/metrics/prometheus` | `/server/metrics`, `/api/v1/server/metrics/prometheus`, `/api/v1/server/metrics`, `/api/metrics/prometheus`, `/api/metrics`, and `/metrics` |
| Grafana | `/server/metrics/grafana` | `/api/v1/server/metrics/grafana`, `/api/metrics/grafana` |
| Loki | `/server/metrics/loki` | `/api/v1/server/metrics/loki`, `/api/metrics/loki` |

The root aliases `/metrics`, `/metrics/prometheus`, `/metrics/grafana`, and
`/metrics/loki` also require `server.metrics.root.enabled`. Configure service
tokens under `server.metrics.auth.tokens`; see [Security](security.md) and
[Configuration](configuration.md).

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
