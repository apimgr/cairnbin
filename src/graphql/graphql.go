package graphql

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/apimgr/cairnbin/src/common/buildinfo"
	"github.com/apimgr/cairnbin/src/common/i18n"
)

// Handler handles both the GraphQL POST API and the GraphiQL browser UI.
type Handler struct {
	resolver *Resolver
	title    string
	// assetPrefixFn resolves the {baseurl} path prefix (PART 12) for
	// static asset links (CSS/JS); returns "" when unset.
	assetPrefixFn func(*http.Request) string
	// themeFn resolves the project-wide theme (dark/light/auto) for the
	// current request so the viewer renders class="theme-{mode}" in sync
	// with the rest of the site; returns "dark" when unset.
	themeFn func(*http.Request) string
	// csrfTokenFn resolves the CSRF token bound to the request's csrf_token
	// cookie, used by the no-JS theme-toggle POST form; returns "" when unset.
	csrfTokenFn func(*http.Request) string
}

// New creates a Handler using the given database and site title.
func New(db DB, title string) *Handler {
	return &Handler{
		resolver: NewResolver(db),
		title:    title,
	}
}

// SetAssetPrefixResolver registers the {baseurl} path-prefix resolver used to
// build the CSS/JS asset links emitted by the UI (PART 12 baseurl resolution).
func (h *Handler) SetAssetPrefixResolver(fn func(*http.Request) string) {
	h.assetPrefixFn = fn
}

// assetPrefix returns the resolved baseurl path prefix, or "" when unset.
func (h *Handler) assetPrefix(r *http.Request) string {
	if h.assetPrefixFn == nil {
		return ""
	}
	return h.assetPrefixFn(r)
}

// SetThemeResolver registers the project-wide theme resolver (reads the same
// `theme` cookie the rest of the site uses) so the GraphiQL viewer stays
// synchronized with the site-wide theme toggle instead of keeping its own
// independent state.
func (h *Handler) SetThemeResolver(fn func(*http.Request) string) {
	h.themeFn = fn
}

// theme returns the resolved theme mode ("dark", "light", or "auto"),
// defaulting to "dark" when no resolver is registered.
func (h *Handler) theme(r *http.Request) string {
	if h.themeFn == nil {
		return "dark"
	}
	if t := h.themeFn(r); t != "" {
		return t
	}
	return "dark"
}

// SetCSRFTokenResolver registers the CSRF token resolver used by the no-JS
// theme-toggle POST form (double-submit cookie pattern, PART 16).
func (h *Handler) SetCSRFTokenResolver(fn func(*http.Request) string) {
	h.csrfTokenFn = fn
}

// csrfToken returns the resolved CSRF token, or "" when unset.
func (h *Handler) csrfToken(r *http.Request) string {
	if h.csrfTokenFn == nil {
		return ""
	}
	return h.csrfTokenFn(r)
}

// ServeHTTP dispatches GET (GraphiQL UI) and POST (GraphQL query) requests.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// Serve GraphiQL UI unless the client sends a query string (GET query).
		if q := r.URL.Query().Get("query"); q != "" {
			h.serveQuery(w, r, q, nil)
			return
		}
		h.serveUI(w, r)
	case http.MethodPost:
		h.servePost(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{
			"errors": []map[string]interface{}{{"message": "method not allowed"}},
		})
	}
}

// servePost decodes a JSON-encoded GraphQL request and executes it.
func (h *Handler) servePost(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query     string                 `json:"query"`
		Variables map[string]interface{} `json:"variables"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"errors": []map[string]interface{}{{"message": "invalid JSON: " + err.Error()}},
		})
		return
	}
	h.serveQuery(w, r, body.Query, body.Variables)
}

// serveQuery executes a query and writes the GraphQL response.
func (h *Handler) serveQuery(w http.ResponseWriter, r *http.Request, query string, vars map[string]interface{}) {
	data, errs := h.resolver.ResolveLocalized(query, vars, i18n.LangFromRequest(r))
	resp := map[string]interface{}{"data": data}
	if len(errs) > 0 {
		resp["errors"] = errs
	}
	writeJSON(w, http.StatusOK, resp)
}

// serveUI renders the self-contained GraphiQL HTML interface.
func (h *Handler) serveUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// HTML is never cached; the build-stamp ETag makes an intermediary that
	// ignores no-store revalidate anyway (AI.md PART 9, line 13287).
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("ETag", `"`+buildinfo.AssetStamp()+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, h.renderUI(r))
}

// writeJSON encodes v as indented JSON and writes it to w.
// SetEscapeHTML(false) prevents < > & from being mangled to < > &.
func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"message":"internal server error"}]}` + "\n"))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(buf.Bytes())
}

// renderUI returns the GraphiQL viewer shell. Component styling lives in
// the shared static/css/components.css stylesheet and behaviour in the
// site's single JS file (static/js/app.js) — PART 16 forbids inline
// <style>/<script> and inline event-handler attributes, and the CSP ships
// script-src 'self' only. The theme class and toggle form mirror the
// project-wide mechanism (server-rendered `theme` cookie + no-JS POST to
// /theme) so this viewer never keeps independent theme state.
func (h *Handler) renderUI(r *http.Request) string {
	prefix := h.assetPrefix(r)
	theme := h.theme(r)
	lang := i18n.LangFromRequest(r)
	direction := i18n.Direction(lang)
	t := func(key string) string { return i18n.Translate(lang, key) }
	return `<!DOCTYPE html>
<html lang="` + lang + `" dir="` + direction + `" class="theme-` + theme + `">
  <head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>` + escapeHTML(h.title) + ` — GraphQL</title>
    <link rel="stylesheet" href="` + buildinfo.AssetURL(prefix, "/static/css/common.css") + `">
    <link rel="stylesheet" href="` + buildinfo.AssetURL(prefix, "/static/css/components.css") + `">
  </head>
  <body class="graphql-page">
    <a href="#graphiql" class="skip-link">` + t("nav.skip_to_content") + `</a>
    <header class="header">
      <nav class="nav" aria-label="` + t("nav.main_navigation") + `">
        <a href="` + prefix + `/" class="nav-brand">
          <div class="nav-brand-icon">📋</div>
          <span>` + escapeHTML(h.title) + ` ` + t("nav.graphql") + ` API</span>
        </a>
        <div class="nav-links">
          <a href="` + prefix + `/" class="nav-link">` + t("nav.home") + `</a>
          <a href="` + prefix + `/create" class="nav-link">` + t("nav.create") + `</a>
          <a href="` + prefix + `/recent" class="nav-link">` + t("nav.recent") + `</a>
          <a href="` + prefix + `/server/docs/swagger" class="nav-link">` + t("nav.swagger") + `</a>
          <form class="theme-toggle" method="post" action="` + prefix + `/theme"><input type="hidden" name="csrf_token" value="` + h.csrfToken(r) + `"><input type="hidden" name="theme" value="` + nextTheme(theme) + `"><button type="submit" class="theme-button" data-theme-toggle aria-label="` + t("theme.toggle") + `">🌙</button></form>
        </div>
      </nav>
    </header>
    <div class="graphiql-container" id="graphiql" data-api-url="` + prefix + `/api/graphql">
      <div class="pane">
        <div class="pane-header">
          <span>` + t("graphql.query") + `</span>
          <button class="execute-button" type="button" data-action="run-graphql-query">&#9654; ` + t("graphql.run") + `</button>
        </div>
        <textarea id="query" spellcheck="false" autocomplete="off" placeholder="` + t("graphql.query_placeholder") + `">{ pastes(page: 1, limit: 5) { total page limit pastes { id title language created_at } } }</textarea>
        <div class="vars-pane">
          <div class="pane-header"><span>` + t("graphql.variables_json") + `</span></div>
          <textarea id="vars" spellcheck="false" placeholder='{"id": "abc123"}'></textarea>
        </div>
      </div>
      <div class="pane">
        <div class="pane-header"><span>` + t("graphql.response") + `</span></div>
        <div class="result-window" id="result">` + t("graphql.result_placeholder") + `</div>
      </div>
      <div class="schema-panel">
        <h3>` + t("graphql.schema") + `</h3>
        <pre>` + escapeHTML(localizedSchemaSDL(lang)) + `</pre>
      </div>
    </div>
    <footer class="footer">
      <div class="footer-content">
        <nav class="footer-links" aria-label="` + t("footer.navigation") + `">
          <a href="` + prefix + `/server/about" class="footer-link">` + t("footer.about") + `</a>
          <span class="footer-sep" aria-hidden="true">·</span>
          <a href="` + prefix + `/server/privacy" class="footer-link">` + t("footer.privacy") + `</a>
          <span class="footer-sep" aria-hidden="true">·</span>
          <a href="` + prefix + `/server/contact" class="footer-link">` + t("footer.contact") + `</a>
          <span class="footer-sep" aria-hidden="true">·</span>
          <a href="` + prefix + `/server/help" class="footer-link">` + t("footer.help") + `</a>
        </nav>
        <div class="footer-made">
          <a href="https://github.com/apimgr/cairnbin" target="_blank" rel="noopener noreferrer" class="footer-repo">` + t("footer.made_with") + ` Go</a> ❤️
        </div>
      </div>
    </footer>
    <script src="` + buildinfo.AssetURL(prefix, "/static/js/app.js") + `"></script>
  </body>
</html>
`
}

// escapeHTML escapes HTML special characters in s.
func escapeHTML(s string) string {
	var out []byte
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '&':
			out = append(out, []byte("&amp;")...)
		case '<':
			out = append(out, []byte("&lt;")...)
		case '>':
			out = append(out, []byte("&gt;")...)
		default:
			out = append(out, s[i])
		}
	}
	return string(out)
}
