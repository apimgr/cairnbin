package compat

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/apimgr/cairnbin/src/model"
)

// ─── termbin (raw-TCP fiche protocol) ─────────────────────────────────────────

// TermbinServe handles a single termbin/fiche raw-TCP connection: it reads up
// to maxSize bytes until the client half-closes the connection or the deadline
// elapses, creates a paste, and writes back "{base}/{id}\n". base is the URL
// origin without a trailing slash. The connection is always closed on return.
//
// The raw-TCP listener on the dedicated port (see server.go) is the canonical
// termbin protocol. termbinHTTPRespond below is an additional HTTP-reachable
// alias for the tb./termbin. subdomain, for clients that can't open a raw TCP
// connection; it mirrors the same minimal text-in/URL-out convention.
func (c *Handler) TermbinServe(conn net.Conn, base string, maxSize int64, timeout time.Duration) {
	defer conn.Close()

	if timeout > 0 {
		// SetReadDeadline error is non-fatal; the read below still bounds the work.
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
	}

	// Read one extra byte so an over-limit upload can be detected and rejected.
	data, err := io.ReadAll(io.LimitReader(conn, maxSize+1))
	if err != nil && len(data) == 0 {
		fmt.Fprintln(conn, "Error: read failed")
		return
	}
	if int64(len(data)) > maxSize {
		fmt.Fprintln(conn, "Error: paste too large")
		return
	}

	content := strings.TrimRight(string(data), "\r\n")
	if strings.TrimSpace(content) == "" {
		fmt.Fprintln(conn, "Error: no content")
		return
	}

	pasteID, _, err := c.ph.CreatePasteInternal("", content, "text", model.VisibilityPublic, 0, nil)
	if err != nil {
		fmt.Fprintln(conn, "Error: could not create paste")
		return
	}

	fmt.Fprintf(conn, "%s/%s\n", strings.TrimRight(base, "/"), pasteID)
}

// termbinHTTPRespond creates a paste from a plain-body POST / on the
// tb./termbin. subdomain and writes back "{base}/{id}\n" — the same bare
// id-URL format TermbinServe writes over raw TCP (deliberately not the
// /raw/{id} URL the other curl-upload targets return), so a script that
// switches from `nc host 9999` to `curl --data-binary @- https://tb.{fqdn}/`
// sees an identical response shape.
func (c *Handler) termbinHTTPRespond(w http.ResponseWriter, r *http.Request, content string) {
	content = strings.TrimRight(content, "\r\n")
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
	fmt.Fprintf(w, "%s/%s\n", strings.TrimRight(c.origin(r), "/"), pasteID)
}
