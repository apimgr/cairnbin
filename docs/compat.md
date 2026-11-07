# Compatibility

Cairnbin is a drop-in replacement for pastebin.com, microbin, lenpaste,
stikked, hastebin/haste-server, dpaste, 0x0.st, sprunge.us, ix.io, and
termbin/fiche. Existing scripts, CLIs, and
integrations targeting any of those services work against this server without
modification. The service has no user accounts; unused API and user-key fields
are accepted and ignored, while protocol-specific delete tokens remain required
to delete compat-created pastes.

## pastebin.com Routes

| Route | Behavior |
|-------|----------|
| `GET https://pste.us/{key}` | View paste |
| `GET https://pste.us/raw/{key}` | Raw paste text |
| `GET https://pste.us/archive` | Recent public pastes |
| `GET https://pste.us/trends` | Recent public pastes |
| `GET https://pste.us/u/{username}` | Redirect 302 to `/` |

## pastebin.com API

| Route | Behavior |
|-------|----------|
| `POST https://pste.us/api/api_login.php` | Returns `"ANONYMOUS"` (HTTP 200) |
| `POST https://pste.us/api/api_post.php` | Create, list, delete pastes |
| `POST https://pste.us/api/api_raw.php` | Raw paste text |

All `api_dev_key` and `api_user_key` fields are accepted but ignored.

**`api_post.php` options:**

| `api_option` | Action |
|---|---|
| `paste` | Create paste |
| `list` | Return recent public pastes as XML |
| `delete` | Delete paste (token via `api_user_key`) |
| `userdetails` | Returns stub anonymous user record |

## microbin Routes

| Route | Behavior |
|-------|----------|
| `POST https://pste.us/api/v1/pasta` | Create paste (form, multipart, or JSON: `content`, `title`, `visibility`, `expiry`, `burn_after`); `curl -q -LSsf -d "content=Hello" https://pste.us/api/v1/pasta` |
| `GET https://pste.us/api/v1/pasta` | List pastes |
| `GET https://pste.us/api/v1/pasta/{id}` | Retrieve a paste |
| `DELETE https://pste.us/api/v1/pasta/{id}` | Delete a paste |
| `POST https://pste.us/upload` | Create paste (multipart) |
| `GET https://pste.us/upload/{id}` | View paste |
| `GET https://pste.us/p/{id}` | View paste |
| `GET https://pste.us/raw/{id}` | Raw text |
| `GET https://pste.us/url/{id}` | Redirect if content is URL |
| `GET https://pste.us/file/{id}` | Download paste |
| `GET https://pste.us/qr/{id}` | QR code PNG |
| `GET https://pste.us/list` | Recent public pastes |
| `GET https://pste.us/remove/{id}` | Show delete form |
| `POST https://pste.us/remove/{id}` | Delete paste |
| `GET https://pste.us/guide` | Redirect to `/server/help` |

## lenpaste Routes

| Route | Behavior |
|-------|----------|
| `POST https://pste.us/` | Create paste; redirect to `/{id}` |
| `GET https://pste.us/{id}` | View paste |
| `GET https://pste.us/raw/{id}` | Raw text |
| `GET https://pste.us/dl/{id}` | Download |
| `GET https://pste.us/emb/{id}` | Embedded view |
| `POST https://pste.us/api/new`, `POST https://pste.us/api/v1/new` | Create paste (form: `title`, `body`, `syntax`, `expiration` seconds, `oneUse`); returns `{"id","url","deleteToken"}` — `curl -q -LSsf -d "body=Hello&expiration=86400" https://pste.us/api/new` |
| `GET https://pste.us/api/get?id=`, `GET https://pste.us/api/v1/get?id=` | Get paste JSON |
| `DELETE https://pste.us/api/remove?id=&deleteToken=` | Delete with the compat delete token |
| `GET https://pste.us/api/list` | List pastes |
| `GET https://pste.us/api/v1/getServerInfo` | Server info JSON |
| `GET https://pste.us/about` | Redirect to `/server/about` |
| `GET https://pste.us/emb_help/` | Redirect to `/server/help` |

## stikked Routes

| Route | Behavior |
|-------|----------|
| `POST https://pste.us/api/create` | Create paste (form fields `text`, `title`, `lang`, `expire` minutes, `private=1`); responds with a plain-text `/view/{id}` URL, or `Error: <msg>` |
| `GET https://pste.us/api/paste/{id}` | Paste metadata + raw body as JSON |

`name` and `apikey` fields are accepted and ignored.

## hastebin / haste-server Routes

| Route | Behavior |
|-------|----------|
| `POST https://pste.us/documents` | Create from raw request body; responds `{"key": "{id}"}` |
| `GET https://pste.us/documents/{id}` | Responds `{"key": "{id}", "data": "..."}` |

Raw bodies are capped at 10 MiB.

## dpaste Routes

| Route | Behavior |
|-------|----------|
| `POST https://pste.us/api/` | Create paste (form fields `content`, `lexer`/`syntax`/`filename`, `expires` days) |
| `POST https://pste.us/api/v2/` | Same as `/api/` |

`format=url` returns a bare URL, `format=json` returns `{"url","content","lexer"}`, and the default returns a quoted URL string.

## curl-upload Family (sprunge / 0x0 / ix.io)

| Route | Behavior |
|-------|----------|
| `POST https://pste.us/` (field `file=@-`) | Create from multipart file; raw `/raw/{id}` URL returned, delete token in `X-Token` header |
| `POST https://pste.us/` (field `sprunge=` or `f:1=`) | Create from form field; returns the raw paste URL |

The `expires` field (hours) is honored for the file-upload form. Bodies are capped at 10 MiB.

## termbin / fiche (raw TCP)

A raw-TCP listener speaking the termbin/fiche protocol is enabled by default on port `9999`. Disable it with `server.termbin.enabled: false` or `TERMBIN_ENABLED=false`. Pipe content over a socket and receive the paste URL back:

```bash
echo "hello" | nc pste.us 9999
```

Uploads are capped at `server.termbin.max_size` bytes (default 32768), with an idle/read timeout of `server.termbin.timeout` (default `5s`). Configure via `server.termbin.enabled`, `server.termbin.port`, `server.termbin.max_size`, and `server.termbin.timeout`.

## Auth Stub Routes

These routes exist for compatibility with scripts that probe them.
No authentication is ever performed — they redirect silently.

| Route | Response |
|-------|----------|
| `GET https://pste.us/login` | 302 → `/` |
| `GET https://pste.us/register` | 302 → `/` |
| `POST https://pste.us/login` | 302 → `/` |
| `POST https://pste.us/register` | 302 → `/` |
| `POST https://pste.us/logout` | 302 → `/` |
| `GET https://pste.us/settings` | 302 → `/` |
| `GET https://pste.us/auth/{id}` | 302 → `/{id}` |
| `GET https://pste.us/auth_raw/{id}` | 302 → `/raw/{id}` |

## Authentication and CSRF

Every compat create route is anonymous and tokenless: no `Authorization` header, cookie, or CSRF token is needed, and any API key a protocol supplies is accepted and ignored. The whole `/api/*` tree, `POST /documents`, `POST /upload`, and `POST /` are exempt from CSRF validation; browser form routes such as `POST /pastes` still require the CSRF token.
