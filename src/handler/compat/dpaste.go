package compat

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/apimgr/cairnbin/src/model"
)

// ─── dpaste compatibility ────────────────────────────────────────────────────

// DpasteCreate handles POST /api/ and POST /api/v2/ (dpaste create).
//
// dpaste form fields:
//
//	content  — content (required)
//	lexer    — language (aliases: "syntax", "filename")
//	expires  — days until expiry (0/absent = never)
//	format   — "default" (quoted URL), "url" (bare URL), "json"
//
// The native GET /{id}/raw route serves raw content for dpaste clients.
func (c *Handler) DpasteCreate(w http.ResponseWriter, r *http.Request) {
	if err := c.parseFormLimited(w, r); err != nil {
		if errors.Is(err, errCompatBodyTooLarge) {
			http.Error(w, "content exceeds the maximum allowed size", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	content := r.FormValue("content")
	if strings.TrimSpace(content) == "" {
		http.Error(w, "This field is required.", http.StatusBadRequest)
		return
	}
	lang := r.FormValue("lexer")
	if lang == "" {
		lang = r.FormValue("syntax")
	}
	if lang == "" {
		lang = r.FormValue("filename")
	}
	if lang == "" {
		lang = "text"
	}
	var expiresAt *time.Time
	if d, err := strconv.Atoi(r.FormValue("expires")); err == nil && d > 0 {
		t := time.Now().Add(time.Duration(d) * 24 * time.Hour)
		expiresAt = &t
	}
	pasteID, _, err := c.ph.CreatePasteInternal("", content, lang, model.VisibilityPublic, 0, expiresAt)
	if err != nil {
		http.Error(w, "could not create snippet", http.StatusInternalServerError)
		return
	}
	link := c.origin(r) + "/" + pasteID
	switch r.FormValue("format") {
	case "url":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintln(w, link)
	case "json":
		writeJSON(w, http.StatusOK, map[string]string{
			"url":     link,
			"content": content,
			"lexer":   lang,
		})
	default:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintf(w, "%q", link)
	}
}
