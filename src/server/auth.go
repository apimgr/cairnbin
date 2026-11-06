package server

import (
	"context"
	"net/http"
	"strings"
)

// ctxKeyAuthToken carries the bearer/API token presented with the request.
const ctxKeyAuthToken ctxKeyType = 101

// ctxKeyAuthSource records where the token came from, for audit logging and
// metric labelling.
const ctxKeyAuthSource ctxKeyType = 102

// extractRequestToken returns the token presented with the request and the
// name of its source. It returns empty strings when no valid bearer token is
// present. Cookies are deliberately never consulted: API routes must not honour ambient authority
// (PART 8), and the web routes that do accept the owner-token cookie read it
// explicitly in their own handlers.
func extractRequestToken(r *http.Request) (string, string) {
	v := strings.TrimSpace(r.Header.Get("Authorization"))
	const prefix = "bearer "
	if len(v) > len(prefix) && strings.EqualFold(v[:len(prefix)], prefix) {
		if tok := strings.TrimSpace(v[len(prefix):]); tok != "" {
			return tok, "authorization"
		}
	}
	return "", ""
}

// TokenFromContext returns the token attached by authMiddleware and the source
// it was read from. Both are empty when the request presented no credentials.
func TokenFromContext(ctx context.Context) (string, string) {
	tok, _ := ctx.Value(ctxKeyAuthToken).(string)
	src, _ := ctx.Value(ctxKeyAuthSource).(string)
	return tok, src
}

// authMiddleware is execution step 9 of the canonical middleware chain
// (AI.md:7180). It only resolves the credential presented with the request into
// the request context — it never rejects a request. Authorization is performed
// per route by requireOperatorToken and the resource-owner checks, so that
// unauthenticated public routes keep working unchanged.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, source := extractRequestToken(r)
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyAuthToken, token)
		ctx = context.WithValue(ctx, ctxKeyAuthSource, source)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
