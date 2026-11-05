//go:build e2e

package server

import (
	"net/http"

	"github.com/apimgr/cairnbin/src/mode"
	"github.com/go-chi/chi/v5"
)

func registerE2ETestRoutes(r chi.Router) {
	if !mode.ShouldShowDebugEndpoints() {
		return
	}
	r.Get("/__e2e__/panic", func(http.ResponseWriter, *http.Request) {
		panic("intentional e2e panic")
	})
}
