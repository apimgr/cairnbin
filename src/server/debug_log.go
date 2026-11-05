// Per-request debug diagnostics (AI.md src/server/debug_log.go): one line per
// request written to debug.log when debug mode is active, giving operators
// method/path/status/duration/size visibility beyond the access log.
package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/apimgr/cairnbin/src/cache"
	"github.com/apimgr/cairnbin/src/mode"
)

// debugLog writes one diagnostic line for a completed request. Gating on
// debug mode and on server.logs.debug.enabled happens inside the manager, so
// calls are free no-ops in production.
func (s *Server) debugLog(r *http.Request, status int, duration time.Duration, size int) {
	s.logManager.Debug("request completed",
		"method", r.Method,
		"path", r.URL.Path,
		"status", strconv.Itoa(status),
		"duration", duration.String(),
		"size", strconv.Itoa(size),
		"ip", clientIP(r),
		"ua", r.UserAgent())
}

// debugCache logs every cache operation to debug.log while debug mode is on
// and server.debug.log_cache is true (PART 6). Keys are logged, values are not.
type debugCache struct {
	cache.Cache
	s *Server
}

func (c *debugCache) enabled() bool {
	return mode.IsDebugEnabled() && c.s.liveCfg().Server.Debug.LogCache
}

func (c *debugCache) Get(ctx context.Context, key string) (string, error) {
	v, err := c.Cache.Get(ctx, key)
	if c.enabled() {
		result := "hit"
		if err != nil {
			result = "miss"
		}
		c.s.logManager.Debug("cache get", "key", key, "result", result)
	}
	return v, err
}

func (c *debugCache) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	err := c.Cache.Set(ctx, key, value, ttl)
	if c.enabled() {
		c.s.logManager.Debug("cache set", "key", key, "ttl", ttl.String(), "bytes", strconv.Itoa(len(value)))
	}
	return err
}

func (c *debugCache) Delete(ctx context.Context, key string) error {
	err := c.Cache.Delete(ctx, key)
	if c.enabled() {
		c.s.logManager.Debug("cache delete", "key", key)
	}
	return err
}

// bodyCapture records the head of a request body as the handler reads it so it
// can be logged with the response (PART 6 server.debug.log_bodies).
type bodyCapture struct {
	on   bool
	max  int
	req  bytes.Buffer
	resp bytes.Buffer
}

type captureReader struct {
	io.ReadCloser
	bc *bodyCapture
}

func (c *captureReader) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	if room := c.bc.max - c.bc.req.Len(); room > 0 && n > 0 {
		if n < room {
			room = n
		}
		c.bc.req.Write(p[:room])
	}
	return n, err
}

// newBodyCapture arms capture on w and r when debug and log_bodies are on.
func (s *Server) newBodyCapture(ww middleware.WrapResponseWriter, r *http.Request) *bodyCapture {
	d := s.liveCfg().Server.Debug
	bc := &bodyCapture{}
	if !mode.IsDebugEnabled() || !d.LogBodies {
		return bc
	}
	bc.on = true
	bc.max = d.MaxBodyLogBytes()
	if r.Body != nil {
		r.Body = &captureReader{ReadCloser: r.Body, bc: bc}
	}
	ww.Tee(&limitedWriter{buf: &bc.resp, max: bc.max})
	return bc
}

// limitedWriter keeps at most max bytes and silently drops the rest.
type limitedWriter struct {
	buf *bytes.Buffer
	max int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		l.buf.Write(p[:room])
	}
	return len(p), nil
}

func (bc *bodyCapture) log(s *Server, r *http.Request, status int) {
	if !bc.on {
		return
	}
	s.logManager.Debug("request bodies",
		"method", r.Method,
		"path", r.URL.Path,
		"status", strconv.Itoa(status),
		"request_body", sanitizeBodyForLog(bc.req.Bytes()),
		"response_body", sanitizeBodyForLog(bc.resp.Bytes()))
}

// sanitizeBodyForLog quotes a captured body so control characters cannot forge
// log lines; binary payloads are reduced to a marker.
func sanitizeBodyForLog(b []byte) string {
	if !utf8.Valid(b) {
		return "[binary " + strconv.Itoa(len(b)) + " bytes]"
	}
	return strconv.Quote(string(b))
}
