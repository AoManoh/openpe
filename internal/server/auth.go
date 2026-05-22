package server

import (
	"net/http"
	"strings"

	"github.com/AoManoh/openpe/internal/integration"
)

// authMiddleware returns an http.Handler that enforces bearer-token
// authentication when token is non-empty. Paths in skipPaths are passed
// through unauthenticated (typically /healthz, and later /v1/info).
//
// When token is empty the middleware is a pass-through, preserving the
// historical no-auth behaviour for users who run openpe-server purely
// for local hook / VSIX integrations.
//
// Comparisons go through integration.TokensEqual, which is constant time
// and rejects empty tokens on both sides.
func authMiddleware(next http.Handler, token string, skipPaths []string) http.Handler {
	if token == "" {
		return next
	}
	skip := make(map[string]struct{}, len(skipPaths))
	for _, p := range skipPaths {
		skip[p] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, exempt := skip[r.URL.Path]; exempt {
			next.ServeHTTP(w, r)
			return
		}
		provided := extractBearerToken(r.Header.Get("Authorization"))
		if !integration.TokensEqual(provided, token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="openpe"`)
			writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// extractBearerToken returns the token portion of a "Bearer <token>"
// authorization header, or empty string when the header is missing or
// not a bearer credential. The scheme match is case-insensitive per RFC 7235.
func extractBearerToken(header string) string {
	const prefix = "Bearer "
	if len(header) <= len(prefix) {
		return ""
	}
	if !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}
