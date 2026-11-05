package compat

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/apimgr/cairnbin/src/model"
)

// ─── 0x0.st (curl -F 'file=@-') ───────────────────────────────────────────────

// zeroXRespond creates a paste from a 0x0.st-style multipart "file" upload and
// writes back a bare raw URL plus a trailing newline, matching 0x0.st's own
// upload behaviour. The compat delete token is returned in the X-Token
// response header, and expires is an optional 0x0-style hours value
// (non-positive or unparsable means never).
func (c *Handler) zeroXRespond(w http.ResponseWriter, r *http.Request, content, expires string) {
	if strings.TrimSpace(content) == "" {
		http.Error(w, "no content", http.StatusBadRequest)
		return
	}
	var expiresAt *time.Time
	if h, err := strconv.Atoi(expires); err == nil && h > 0 {
		t := time.Now().Add(time.Duration(h) * time.Hour)
		expiresAt = &t
	}
	pasteID, deleteToken, err := c.ph.CreatePasteInternal("", content, "text", model.VisibilityPublic, 0, expiresAt)
	if err != nil {
		http.Error(w, "could not create paste", http.StatusInternalServerError)
		return
	}
	w.Header().Set("X-Token", deleteToken)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, c.rawURL(r, pasteID))
}
