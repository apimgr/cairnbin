package compat

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/apimgr/cairnbin/src/model"
)

// ─── ix.io (curl -F 'f:1=<-') ─────────────────────────────────────────────────

// ixioRespond creates a paste from raw content posted to the ix.io "f:1" form
// field and writes back a bare raw URL plus a trailing newline, matching
// ix.io's own curl-upload behaviour.
func (c *Handler) ixioRespond(w http.ResponseWriter, r *http.Request, content string) {
	if strings.TrimSpace(content) == "" {
		http.Error(w, "no content", http.StatusBadRequest)
		return
	}
	pasteID, _, err := c.ph.CreatePasteInternal("", content, "text", model.VisibilityPublic, 0, nil)
	if err != nil {
		http.Error(w, "could not create paste", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, c.rawURL(r, pasteID))
}
