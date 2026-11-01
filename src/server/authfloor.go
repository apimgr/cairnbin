package server

import (
	"net/http"
	"time"
)

// authFailureFloor is the minimum time a failed-authentication response takes
// (PART 11 "Identical auth response timing"): padding every failure to the same
// floor stops an attacker telling "no such token" from "wrong owner" by timing.
const authFailureFloor = 100 * time.Millisecond

// authFloorWriter delays the response header of a failed-auth response until
// authFailureFloor has elapsed since the request started.
type authFloorWriter struct {
	http.ResponseWriter
	start   time.Time
	padFor  func(status int) bool
	started bool
}

// WriteHeader pads failed-auth statuses to the floor, then forwards the header.
func (w *authFloorWriter) WriteHeader(status int) {
	if !w.started {
		w.started = true
		if w.padFor(status) {
			if remaining := authFailureFloor - time.Since(w.start); remaining > 0 {
				time.Sleep(remaining)
			}
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

// Write ensures an implicit 200 still goes through WriteHeader exactly once.
func (w *authFloorWriter) Write(b []byte) (int, error) {
	if !w.started {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Flush forwards to the underlying writer when it supports flushing.
func (w *authFloorWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *authFloorWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// authFailureFloorMiddleware pads failed-authentication responses to the
// authFailureFloor. A response counts as a failed auth when it is a 401 or 403,
// or a 404 on a mutating request that presented a credential (an unowned
// resource is deliberately indistinguishable from a missing one, so it must
// also be indistinguishable by timing).
func (s *Server) authFailureFloorMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutating := r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
		presented := false
		if mutating {
			tok, _ := extractRequestToken(r)
			presented = tok != ""
		}
		fw := &authFloorWriter{
			ResponseWriter: w,
			start:          time.Now(),
			padFor: func(status int) bool {
				return status == http.StatusUnauthorized || status == http.StatusForbidden ||
					(status == http.StatusNotFound && presented)
			},
		}
		next.ServeHTTP(fw, r)
	})
}
