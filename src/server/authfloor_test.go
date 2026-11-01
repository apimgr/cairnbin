package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func serveWithFloor(status int, method string, bearer bool) time.Duration {
	s := &Server{}
	h := s.authFailureFloorMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))
	r := httptest.NewRequest(method, "/api/v1/pastes/abc", nil)
	if bearer {
		r.Header.Set("Authorization", "Bearer tok_x")
	}
	start := time.Now()
	h.ServeHTTP(httptest.NewRecorder(), r)
	return time.Since(start)
}

// TestAuthFailureFloor verifies failed-auth responses take at least the floor
// and ordinary responses are not delayed.
func TestAuthFailureFloor(t *testing.T) {
	slow := []struct {
		name   string
		status int
		method string
		bearer bool
	}{
		{"401", http.StatusUnauthorized, http.MethodGet, false},
		{"403", http.StatusForbidden, http.MethodPost, false},
		{"404 on token-bearing delete", http.StatusNotFound, http.MethodDelete, true},
	}
	for _, tc := range slow {
		if d := serveWithFloor(tc.status, tc.method, tc.bearer); d < authFailureFloor {
			t.Errorf("%s: took %v, want >= %v", tc.name, d, authFailureFloor)
		}
	}
	fast := []struct {
		name   string
		status int
		method string
		bearer bool
	}{
		{"200", http.StatusOK, http.MethodGet, false},
		{"404 plain GET", http.StatusNotFound, http.MethodGet, false},
		{"404 delete without credential", http.StatusNotFound, http.MethodDelete, false},
	}
	for _, tc := range fast {
		if d := serveWithFloor(tc.status, tc.method, tc.bearer); d >= authFailureFloor {
			t.Errorf("%s: took %v, want < %v", tc.name, d, authFailureFloor)
		}
	}
}
