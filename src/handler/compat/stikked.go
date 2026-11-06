package compat

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/apimgr/cairnbin/src/model"
)

// ─── stikked compatibility ──────────────────────────────────────────────────

// StikkedCreate handles POST /api/create (stikked API create).
//
// stikked form fields:
//
//	text     — content (required)
//	title    — paste title (optional)
//	name     — author name (ignored)
//	lang     — language (default "text")
//	expire   — minutes until expiry (0/absent = never)
//	private  — "1" marks the paste unlisted
//	apikey   — ignored (open instance)
//
// Responds with a plain-text view URL, or "Error: <msg>" on failure.
func (c *Handler) StikkedCreate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if err := c.parseFormLimited(w, r); err != nil {
		if errors.Is(err, errCompatBodyTooLarge) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			_, _ = fmt.Fprint(w, "Error: paste exceeds the maximum allowed size.")
			return
		}
		_, _ = fmt.Fprint(w, "Error: invalid form")
		return
	}
	content := r.FormValue("text")
	if strings.TrimSpace(content) == "" {
		_, _ = fmt.Fprint(w, "Error: No paste data sent.")
		return
	}
	title := r.FormValue("title")
	lang := r.FormValue("lang")
	if lang == "" {
		lang = "text"
	}
	vis := model.VisibilityPublic
	if r.FormValue("private") == "1" {
		vis = model.VisibilityUnlisted
	}
	var expiresAt *time.Time
	if m, err := strconv.Atoi(r.FormValue("expire")); err == nil && m > 0 {
		t := time.Now().Add(time.Duration(m) * time.Minute)
		expiresAt = &t
	}
	pasteID, _, err := c.ph.CreatePasteInternal(title, content, lang, vis, 0, expiresAt)
	if err != nil {
		_, _ = fmt.Fprint(w, "Error: could not create paste")
		return
	}
	_, _ = fmt.Fprint(w, c.origin(r)+"/view/"+pasteID)
}

// StikkedJSON handles GET /api/paste/{id} — stikked JSON metadata + raw body.
func (c *Handler) StikkedJSON(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	paste, err := c.db.GetPasteByID(id)
	if err != nil || paste == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "paste not found"})
		return
	}
	if paste.ExpiresAt != nil && paste.ExpiresAt.Before(time.Now()) {
		_ = c.db.DeletePaste(id)
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "paste not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"pid":     paste.ID,
		"title":   paste.Title,
		"name":    "",
		"created": paste.CreatedAt.Unix(),
		"lang":    paste.Language,
		"raw":     paste.Content,
		"hits":    paste.Views,
	})
}
