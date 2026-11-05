package compat

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/apimgr/cairnbin/src/metric"
	"github.com/apimgr/cairnbin/src/model"
)

// ─── hastebin / haste-server compatibility ──────────────────────────────────

// HastebinCreate handles POST /documents (haste-server create).
// The request body is the raw paste content. Responds {"key":"<id>"} on success;
// raw retrieval is served by the native GET /raw/{id} route.
func (c *Handler) HastebinCreate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, c.ph.EffectiveReadLimit()))
	if err != nil || strings.TrimSpace(string(body)) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "no content"})
		return
	}
	pasteID, _, err := c.ph.CreatePasteInternal("", string(body), "text", model.VisibilityPublic, 0, nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "could not create document"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"key": pasteID})
}

// HastebinGet handles GET /documents/{id} (haste-server fetch).
func (c *Handler) HastebinGet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	paste, err := c.db.GetPasteByID(id)
	if err != nil || paste == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "document not found"})
		return
	}
	if paste.ExpiresAt != nil && paste.ExpiresAt.Before(time.Now()) {
		c.db.DeletePaste(id)
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "document not found"})
		return
	}
	if _, burned, verr := c.db.IncrementViewsAndCheckBurn(id); verr == nil && burned {
		c.ph.InvalidatePasteCache(id)
		metric.PastesDeletedTotal.Inc()
	}
	writeJSON(w, http.StatusOK, map[string]string{"key": paste.ID, "data": paste.Content})
}
