# Frontend Rules (PART 16)

⚠️ **These rules are NON-NEGOTIABLE. Violations are bugs.** ⚠️

## CRITICAL - NEVER DO

- NEVER ship a project without a fully functional, professional frontend — all features must work in browser, not just via API.
- NEVER make the frontend break or become unusable with JavaScript disabled — JS enhances, it never enables core functionality.
- NEVER put admin/server-administration links, hints, or a Dashboard/Settings link in public nav — there is no admin web UI; server admin is config-file/CLI only.
- NEVER use trailing slashes as canonical URLs — normalize and 301-redirect `/path/` → `/path` (except root `/` and explicit file requests).
- NEVER let long unbreakable strings (IPv6, onion addresses, tokens, hashes, UUIDs) overflow their container or break mobile layout — always `word-break: break-all; overflow-wrap: break-word` or horizontal scroll.
- NEVER use `<br />` spacers for vertical rhythm in footers/lists — use consecutive `<p>` elements styled with CSS margin.
- NEVER use JS frameworks (React, Vue, Alpine, jQuery, etc.), bundlers (webpack/vite/rollup), transpilers (TypeScript/Babel), or npm/node for the frontend.
- NEVER split JavaScript across multiple files — all JS lives in ONE file: `static/js/app.js`.
- NEVER use inline `onclick`/`onchange`/inline event handler attributes — CSP blocks them; bind via `addEventListener` on `data-action`/IDs in `app.js`.
- NEVER use inline CSS (`style="..."`) or inline `<style>` in HTML — all styles in external CSS files.
- NEVER use `!important` in CSS except in print styles.
- NEVER use browser-native `alert()`, `confirm()`, or `prompt()` — use custom modals/toasts or native `<dialog>`.
- NEVER use desktop-first CSS (base styles for desktop + `max-width` media queries) — CSS MUST be mobile-first.
- NEVER put the theme class on `<body>` — it goes on `<html>`.
- NEVER create layout-scoped/theme-scoped CSS files — one global theme system for the whole project (web, Swagger, GraphiQL, CLI, TUI, GUI).
- NEVER show blank space for empty lists/tables/data views — always render a proper empty state.
- NEVER use deprecated HTML5 elements (`<center>`, `<font>`, `<marquee>`, `<blink>`) or deprecated attributes (`align`, `bgcolor`, `border`, `cellpadding`).
- NEVER pass user-submitted/untrusted content (pastes, repo blobs, markdown files) through `template.HTML` unless it passed an allow-list sanitizer for an explicitly approved field.
- NEVER pin/fix header, nav, or footer to the viewport — nothing is `position: fixed`/`sticky` except the cookie consent banner (fixed bottom) and toast container (fixed).
- NEVER use generic placeholder text ("Your application name here", "Feature 1, Feature 2") on `/server/about` or `/server/help` — content MUST come from `IDEA.md`.
- NEVER render the raw `/.well-known/security.txt` link from `/server/contact` — link to `/server/security` instead.
- NEVER render `server.contact.admin.email` publicly on the contact page.
- NEVER load analytics/tracking scripts, set preference cookies, or load third-party embeds after a user declines cookie consent.
- NEVER allow a CORS wildcard (`*`) on `Access-Control-Allow-Headers` — every supported auth header must be listed by name; wildcards are invalid with credentials.
- NEVER send `Access-Control-Allow-Credentials: true` together with `Access-Control-Allow-Origin: *`.
- NEVER skip CSRF validation via an Origin-header check — there is no Origin-based bypass.
- NEVER render unstyled/plain browser default error pages — all error pages (400/401/403/404/500/502/503) MUST use the site theme via `error.tmpl`.
- NEVER allow remote SVG logos/images without sanitizing and rasterizing first.
- NEVER fetch remote branding images over `http://`, from localhost/private/internal IPs, or without size/type/timeout limits.
- NEVER embed GeoIP databases, IP/domain blocklists, CVE databases, or SSL certs in the binary — these are downloaded/updated externally; only templates/CSS/JS/images/fonts/app data are embedded.
- NEVER render verification meta tags with empty content, invalid characters, or content exceeding max length.
- NEVER include authenticated/server-management pages or `/api/*` endpoints in `/sitemap.xml`.

## CRITICAL - ALWAYS DO

- ALWAYS make ALL user-facing API features accessible via the browser frontend (API is source of truth; frontend consumes it).
- ALWAYS validate the same rules on the frontend as the backend, and show real-time success/error feedback from backend responses.
- ALWAYS support full CRUD via HTML forms (browser), JSON API (programmatic), and form-encoded/text (CLI) — all three must work.
- ALWAYS detect client type (HTML vs text vs JSON) via `Accept` header first, then User-Agent, defaulting to HTML.
- ALWAYS write mobile-first CSS: base styles = mobile, `@media (min-width: …)` enhances for larger screens.
- ALWAYS ensure minimum touch target size of 44x44px on mobile.
- ALWAYS use semantic HTML5 elements appropriate to content (`<code>`, `<pre><code>`, `<kbd>`, `<samp>`, `<var>`, `<time>`, `<mark>`, `<abbr>`, `<header>`, `<nav>`, `<main>`, `<footer>`, `<article>`, `<section>`, `<aside>`).
- ALWAYS show a visible "Copied!" confirmation (icon + i18n label + `.copied` class, `aria-live="polite"`) on every copy button, reverting after 2 seconds.
- ALWAYS disable the submit button immediately on click, show a loading-state label (e.g. "Saving..."), and re-enable after success or error.
- ALWAYS use native `<dialog>` for modals (focus trap, Escape, backdrop are native); close/cancel buttons use `<form method="dialog">` with zero JS.
- ALWAYS use TOAST for non-blocking confirmations and MODAL for decisions/input/destructive-action confirmation (see decision table below) — never interchange them.
- ALWAYS respect `prefers-reduced-motion` by shortening animation/transition durations to near-zero.
- ALWAYS make forms accessible: associated `<label>`, `aria-describedby` linking errors, `aria-invalid`, errors announced via `aria-live`/`role="alert"`.
- ALWAYS trim whitespace on text inputs before validation; reject (don't silently trim) passwords with leading/trailing whitespace.
- ALWAYS embed templates, CSS, JS, images, fonts, and app data in the binary via `go:embed`.
- ALWAYS include header, nav, and footer partials on every page — no page defines its own.
- ALWAYS render the theme class (`theme-dark`/`theme-light`/`theme-auto`) server-side on `<html>` from the `theme` cookie — no init JS, no FOUC.
- ALWAYS make both light and dark themes WCAG AA compliant (4.5:1 contrast minimum) with no invisible/unreadable elements.
- ALWAYS serve a dynamically generated `/sitemap.xml`.
- ALWAYS sanitize `web.footer.custom_html` with an allow-list policy (bluemonday) before rendering — strip scripts, event handlers, `javascript:` URLs, `style` attribute.
- ALWAYS show the cookie consent banner (fixed bottom, full width) until a valid `cookie_consent` cookie exists — this project always uses cookies, so it's always enabled.
- ALWAYS gate CSRF validation on: mutating method (POST/PUT/PATCH/DELETE) AND no syntactically valid `Authorization: Bearer` header present; use the double-submit cookie pattern (`csrf_token` cookie, `SameSite=Strict`, not HttpOnly).
- ALWAYS populate `/server/about` and `/server/help` content from `IDEA.md` — real examples, real features, real FAQ, never generic placeholders.
- ALWAYS validate remote branding/logo/favicon URLs for scheme (https only), private/loopback/internal IP (SSRF), size limit, content-type, and redirect chain before fetching.
- ALWAYS end JSON and text (non-HTML) responses with exactly one trailing `\n`.
- ALWAYS use the unified `{"ok": bool, "data"/"error"+"message"}` response envelope for all server responses.

## Route Structure

| API Route | Frontend Route | Page Type |
|---|---|---|
| `GET /api/{api_version}/{resource}` | `GET /{resource}` | Resource list |
| `POST /api/{api_version}/{resource}` | `POST /{resource}` | Create form |
| `GET /api/{api_version}/{resource}/{id}` | `GET /{resource}/{id}` | Resource detail |
| `GET /api/{api_version}/server/about` | `GET /server/about` | About page |

Route priority (highest to lowest): `/api/{api_version}/*` → `/server/healthz` → `/static/*` → `/server/*` → reserved names → project-specific catch-all (`/{slug}`, lowest priority, registered last).

Reserved names (must block from slug registration): `api, server, static, assets, healthz, metrics, webhook, webhooks, search, explore, discover, trending, help, support, docs, documentation, about, contact, terms, privacy, legal, security, graphql, swagger, rest, rpc, ws, websocket, cdn, media, uploads, files, images, .well-known, robots.txt, sitemap.xml, favicon.ico` (plus project-specific ones in IDEA.md).

### Middleware order
1. URLNormalizeMiddleware
2. RequestIDMiddleware
3. PathSecurityMiddleware
4. SecurityHeadersMiddleware
5. AllowlistMiddleware
6. BlocklistMiddleware
7. RateLimitMiddleware
8. GeoIPMiddleware
9. AuthMiddleware
10. LoggingMiddleware

### Standard `/server/*` pages

| Route | Purpose |
|---|---|
| `/server` | 301 → `/server/about` |
| `/server/about` | About (content from IDEA.md) |
| `/server/privacy` | Privacy policy (auto-generated from `server.privacy` config) |
| `/server/contact` | Contact form (+ Security Issues / Abuse Reports sections) |
| `/server/help` | Help/docs (content from IDEA.md) |
| `/server/terms` | Terms of service |
| `/server/healthz` | Health page |
| `/server/docs/swagger` | Swagger UI |
| `/server/docs/graphql` | GraphiQL UI |

## Content Negotiation

| Client | Detection | Response |
|---|---|---|
| Browser | `Accept: text/html` or browser UA (Mozilla/, Chrome/, etc.) | HTML |
| CLI/curl | `Accept: text/plain`, CLI UA (curl/, Wget/, etc.), or empty UA | Text |
| Programmatic | `Accept: application/json` | JSON |
| Unknown | fallback | HTML |

Testing rule: prefer `Accept: text/plain` / CLI auto-detect for automated tests — never parse HTML in test scripts.

## Mobile-First Breakpoints

| Breakpoint | Target |
|---|---|
| Base (no query) | Mobile <768px |
| `min-width: 768px` | Tablet+ |
| `min-width: 1024px` | Desktop+ |
| `min-width: 1280px` | Large desktop (optional) |

| Screen | Container width |
|---|---|
| ≥768px | 90% (max-width 1400px), centered |
| <768px | 100%, 1rem padding |

## HTML5/CSS-over-JS Priority

1. HTML5 (structure, forms, `required`/`pattern`/`type=`, `<details>`, `<dialog>`, `<progress>`, `<input type="range/date/color">`)
2. CSS (styling, layout, themes, `:hover`/`:focus-within`/`:target`, animations, checkbox-hack menus)
3. JavaScript — only when HTML5/CSS truly cannot do it (API calls, complex state, WebSockets, clipboard, toasts)

## Technology Stack

| Layer | Rule |
|---|---|
| Templates | Go `html/template`, `.tmpl` extension only |
| JS | Pure vanilla, ONE file: `static/js/app.js`, no frameworks/bundlers/transpilers/npm |
| CSS | `common.css` → `components.css` → `public.css` load order; BEM-like naming; mobile-first |
| Styling | CSS custom properties only, no inline styles |

## File / Template Layout

```
src/server/template/
├── layout/public.tmpl
├── partial/
│   ├── public/{header,nav,footer}.tmpl   (REQUIRED)
│   ├── head.tmpl                          (REQUIRED)
│   └── scripts.tmpl                       (REQUIRED)
├── page/{index,healthz,error}.tmpl
└── component/{modal,toast}.tmpl

src/server/static/
├── css/{common,public,components}.css
├── js/app.js
├── images/{logo.svg,favicon.ico,icons/}
└── fonts/
```

## Toast vs Modal

| Use TOAST | Use MODAL |
|---|---|
| Confirmation ("Saved", "Deleted", "Copied") | Requires decision ("Delete this item?") |
| Non-blocking info | Requires input (forms, passwords) |
| Errors that don't need input | Destructive action confirmation |
| User can keep working | Blocking workflow (login, terms) |

Toast rules: top-right, stack newest-on-top, max 5 visible, auto-dismiss success/info 3s, warning 5s, error never; click-to-dismiss; Escape dismisses topmost; pause on hover.

## Theme System

- Themes: dark (default), light, auto (`prefers-color-scheme`, pure CSS, no `matchMedia` JS).
- Persisted via `theme` cookie (`dark`/`light`/`auto`), read server-side, class rendered on `<html>` (never `<body>`).
- Applies project-wide: Web, Swagger, GraphiQL, CLI, TUI, GUI — single palette source of truth in `src/common/theme/colors.go`.
- No-JS switching via `<noscript>` form POST to theme endpoint.

## Cookie Reference

| Cookie | Values | Default | Notes |
|---|---|---|---|
| `theme` | dark\|light\|auto | dark | server-readable |
| `lang` | BCP 47 | Accept-Language | server-readable |
| `cookie_consent` | JSON categories+timestamp | unset (banner shown) | essential/preferences/analytics |
| `ccpa_opt_out` | true | unset | only relevant if `data.sold=true` |
| `csrf_token` | random | — | SameSite=Strict, NOT HttpOnly |
| `owner_token` | token | — | HttpOnly, Secure, SameSite=Strict; WEB routes only, API ignores it |
| `dismissed_announcements` | comma ids | — | site banner dismissal |

Consent rules: a first visit waits for a preferences choice; do not write `theme`, `lang`, or `{project_name}_pref_*` cookies until preferences are accepted. Decline blocks them. An honored `Sec-GPC: 1` or configured `DNT: 1` also blocks non-essential preference writes.

## CSS Variables (theming)

```
--color-bg, --color-bg-secondary, --color-bg-card, --color-bg-hover, --color-bg-active,
--color-code-bg, --color-text, --color-muted, --color-border, --color-border-hover,
--color-success(+bg), --color-error(+bg), --color-warning(+bg), --color-primary(+bg)
```
Light overrides via `html.theme-light { ... }`.

## Accessibility (WCAG 2.1 AA)

Keyboard navigation on all interactive elements, visible focus rings, `aria-label`/`aria-describedby`/`role`, 4.5:1 contrast (3:1 large text), `alt` on all images, `<label>` on all inputs, `aria-live` errors, skip-to-content link, proper heading hierarchy, `prefers-reduced-motion` support.

## PWA Requirements

Manifest (`/manifest.json`), service worker (`/sw.js`, install/activate/fetch lifecycle), offline fallback (`/offline.html`), icons 72–512px + maskable, HTTPS required, install prompt handling (`beforeinstallprompt` + iOS manual instructions), background sync, geolocation (permission-gated, request only on user action), app update notification via `skipWaiting`/`controllerchange`.

Cache strategy: static assets cache-first, HTML network-first w/ cache fallback, API calls network-only (never cached, queued for background sync), max cache 50MB, static asset expiry 7 days.

## CORS Defaults

`Access-Control-Allow-Origin: *` by default (`server.cors.allowed_origins`); credentials only sent with an explicit origin list, never with `*`. Resolution order: explicit config → `DOMAIN` env → reverse-proxy-learned hosts (trusted proxies only) → fallback `*`.

## CSRF

Double-submit cookie (`csrf_token` cookie + `X-CSRF-Token` header or `csrf_token` form field). Validated only for mutating methods without an `Authorization: Bearer` header. Other token headers do not bypass CSRF. Bypassed for GET/HEAD/OPTIONS, WebSocket upgrades, and `server.csrf.exempt_paths`. Failure → `403 CSRF_FAILED`.

## Standard Error Codes

| Code | HTTP | 
|---|---|
| BAD_REQUEST / VALIDATION_FAILED | 400 |
| UNAUTHORIZED / TOKEN_EXPIRED / TOKEN_INVALID | 401 |
| FORBIDDEN / ACCOUNT_LOCKED / CSRF_FAILED | 403 |
| NOT_FOUND | 404 |
| METHOD_NOT_ALLOWED | 405 |
| CONFLICT | 409 |
| RATE_LIMITED | 429 |
| SERVER_ERROR | 500 |
| MAINTENANCE | 503 |

## Frontend UI Element Rules

| Never Use | Always Use |
|---|---|
| `alert()` | Custom modal |
| `confirm()` | Native `<dialog>` confirmation |
| `prompt()` | Custom input modal / inline form |
| Plain text for options | `<select>` dropdown |
| Plain text yes/no | Checkbox / toggle switch |

Mobile nav: CSS-only checkbox-hack hamburger menu, slides in from right, theme toggle always stays in header (never inside the menu). No fixed/pinned header/nav/footer — everything scrolls with the page except toast container and cookie banner.

For complete details, see AI.md PART 16
