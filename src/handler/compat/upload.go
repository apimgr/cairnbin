package compat

import (
	"bytes"
	"io"
	"net/http"
	"strings"
)

// ─── curl-upload dispatcher (sprunge / 0x0.st / ix.io) ───────────────────────

// RootUpload handles POST / for the curl-upload family. It inspects the form
// fields to identify the client and delegates to that target's own respond
// method; when no recognised field is present and the request arrived on a
// recognised curl-upload subdomain (tb./termbin., ix., sprunge., 0x0./st. —
// see the server's compatModeGate), the whole raw body is treated as that
// target's content, so a plain `curl --data-binary @- https://sprunge.{fqdn}/`
// works without the distinguishing form field. Absent both a field and a
// subdomain match, the request falls through to the native create handler so
// the lenpaste form-POST-to-root behaviour is preserved.
//
// This dispatcher stays shared because all these targets genuinely share one
// HTTP route (POST /) — but each target's response formatting lives in its
// own file (sprunge.go, zerox0.go, ixio.go, termbin.go) for readability, even
// though that duplicates the create-and-write logic across them. Every path
// through here honors the same operator-configured paste.max_size and
// requires no auth token, exactly like the field-dispatched routes below —
// compat create never gates on server.token.
//
//	0x0.st:  multipart file field "file" (delete token returned in X-Token)
//	sprunge: form field "sprunge"
//	ix.io:   form field "f:1"
//	termbin: subdomain only (raw body, no distinguishing field)
func (c *Handler) RootUpload(w http.ResponseWriter, r *http.Request) {
	// Bound the whole request body (multipart or urlencoded) to the
	// configured paste.max_size before any field is read, so a large
	// "file" part can't spool unbounded data to disk and the sprunge/ix.io
	// text fields honor the same limit as every other create path.
	r.Body = http.MaxBytesReader(w, r.Body, c.ph.EffectiveReadLimit())
	// Explicitly parse the form first: for a urlencoded body (sprunge/f:1),
	// r.FormFile below would call ParseMultipartForm, which short-circuits
	// on a non-multipart Content-Type via ErrNotMultipart *before* checking
	// the error from this same ParseForm call it makes internally, silently
	// discarding a MaxBytesError. Capturing it here first avoids that.
	//
	// On a recognised curl-upload subdomain the whole body may be the paste
	// (`curl --data-binary @- https://tb.{fqdn}/` sends it as
	// application/x-www-form-urlencoded, which ParseForm would consume). Buffer
	// the already size-bounded body so it can be replayed for ParseForm and
	// still used raw below.
	target := targetFromContext(r)
	var rawBody []byte
	rawCaptured := false
	if target != "" && !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		b, err := io.ReadAll(r.Body)
		if err != nil && isMaxBytesErr(err) {
			http.Error(w, "content exceeds the maximum allowed size", http.StatusRequestEntityTooLarge)
			return
		}
		rawBody, rawCaptured = b, true
		r.Body = io.NopCloser(bytes.NewReader(b))
	}
	if err := r.ParseForm(); err != nil && isMaxBytesErr(err) {
		http.Error(w, "content exceeds the maximum allowed size", http.StatusRequestEntityTooLarge)
		return
	}
	if file, _, err := r.FormFile("file"); err == nil {
		defer file.Close()
		body, _ := io.ReadAll(io.LimitReader(file, c.ph.EffectiveReadLimit()))
		c.zeroXRespond(w, r, string(body), r.FormValue("expires"))
		return
	} else if isMaxBytesErr(err) {
		http.Error(w, "content exceeds the maximum allowed size", http.StatusRequestEntityTooLarge)
		return
	}
	if v := r.FormValue("sprunge"); strings.TrimSpace(v) != "" {
		c.sprungeRespond(w, r, v)
		return
	}
	if v := r.FormValue("f:1"); strings.TrimSpace(v) != "" {
		c.ixioRespond(w, r, v)
		return
	}
	// No distinguishing field was present. If the request arrived on a
	// recognised curl-upload subdomain, treat the whole (still
	// MaxBytesReader-bounded) body as that target's content instead of
	// falling through to the native create handler.
	if target != "" {
		body := rawBody
		if !rawCaptured {
			var err error
			body, err = io.ReadAll(r.Body)
			if err != nil && isMaxBytesErr(err) {
				http.Error(w, "content exceeds the maximum allowed size", http.StatusRequestEntityTooLarge)
				return
			}
		}
		switch target {
		case "termbin":
			c.termbinHTTPRespond(w, r, string(body))
			return
		case "ixio":
			c.ixioRespond(w, r, string(body))
			return
		case "sprunge":
			c.sprungeRespond(w, r, string(body))
			return
		case "zerox0":
			c.zeroXRespond(w, r, string(body), r.FormValue("expires"))
			return
		}
	}
	c.ph.CreatePaste(w, r)
}
