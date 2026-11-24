// Package compat implements route handlers for third-party paste-tool API
// compatibility so existing scripts and CLIs work unmodified by pointing
// them at this server.
//
// Supported wire-compatible APIs and their reference docs:
//
//	pastebin.com:        https://pastebin.com/doc_api
//	microbin:            https://github.com/szabodanika/microbin
//	lenpaste fork:       https://github.com/forksmgr/lcomrade-lenpaste
//	stikked:             https://github.com/claudehohl/Stikked
//	hastebin/haste:      https://github.com/toptal/haste-server
//	dpaste:              https://github.com/bartTC/dpaste
//	sprunge/0x0/ix.io:   curl-upload family (POST / with a single field)
package compat

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/apimgr/cairnbin/src/database"
	"github.com/apimgr/cairnbin/src/handler"
)

const maxCompatBodySize int64 = 10 << 20

// errCompatBodyTooLarge is returned by parseFormLimited when the request body
// exceeds the configured paste.max_size limit.
var errCompatBodyTooLarge = errors.New("request body exceeds maximum paste size")

// Handler handles compatibility routes.
type Handler struct {
	ph      *handler.PasteHandler
	db      database.DB
	version string
}

// New creates a new Handler.
func New(ph *handler.PasteHandler, db database.DB, version string) *Handler {
	return &Handler{ph: ph, db: db, version: version}
}

// parseFormLimited wraps r.Body with http.MaxBytesReader using the lower of
// configured paste.max_size and the compatibility cap before calling
// r.ParseForm(). Returns errCompatBodyTooLarge when the limit is exceeded so
// callers can respond with a 413 in their protocol's own error format.
func (c *Handler) parseFormLimited(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, c.effectiveReadLimit())
	if err := r.ParseForm(); err != nil {
		if isMaxBytesErr(err) {
			return errCompatBodyTooLarge
		}
		return err
	}
	return nil
}

// effectiveReadLimit applies both the configured native paste limit and the
// fixed 10 MiB compatibility-protocol cap described in IDEA.md.
func (c *Handler) effectiveReadLimit() int64 {
	limit := c.ph.EffectiveReadLimit()
	if limit > maxCompatBodySize {
		return maxCompatBodySize
	}
	return limit
}

// isMaxBytesErr reports whether err (or one it wraps) is the overflow error
// http.MaxBytesReader produces once the configured size limit is exceeded.
func isMaxBytesErr(err error) bool {
	var tooLarge *http.MaxBytesError
	return errors.As(err, &tooLarge)
}

// origin returns the scheme+host base URL for this request, honouring the
// configured base URL override when set.
func (c *Handler) origin(r *http.Request) string {
	return c.ph.Base(r)
}

// rawURL returns the raw-content URL for a paste ID.
func (c *Handler) rawURL(r *http.Request, id string) string {
	return c.origin(r) + "/raw/" + id
}

// writeJSON encodes v as indented JSON and writes it to w.
// SetEscapeHTML(false) prevents < > & from being mangled to < > &.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		http.Error(w, `{"ok":false,"error":"SERVER_ERROR","message":"Internal server error"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// expiryUnix returns the Unix timestamp for t, or 0 when t is nil.
func expiryUnix(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.Unix()
}
