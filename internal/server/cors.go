package server

import (
	"net/http"
	"strings"
)

// CORSOptions controls Cross-Origin Resource Sharing handling for callers
// running inside an Electron host (Windsurf, Cursor, VS Code Composer, ...).
// These hosts often present origins that browsers normally reject:
//
//   - "null"            — content loaded from file:// or a sandboxed iframe.
//   - "app://*"         — packaged Electron app schemes.
//   - "vscode-webview://*" — VS Code webview iframes.
//
// The zero value disables CORS entirely (no headers emitted), matching the
// historical behaviour for callers that only use the local CLI / hook path.
type CORSOptions struct {
	// AllowedOrigins is the explicit list of origin strings allowed to
	// access the API. Special values:
	//   "*"     — allow any origin (NOT recommended with bearer auth).
	//   "null"  — allow Electron / file-based webviews that send Origin: null.
	AllowedOrigins []string
	// AllowedHeaders extends the default header allowlist; defaults already
	// include Authorization, Content-Type, Accept.
	AllowedHeaders []string
	// AllowedMethods extends the default method allowlist; defaults already
	// include GET, POST, OPTIONS.
	AllowedMethods []string
}

var (
	defaultAllowedHeaders = []string{"Authorization", "Content-Type", "Accept"}
	defaultAllowedMethods = []string{"GET", "POST", "OPTIONS"}
)

// corsMiddleware wraps next with CORS handling. When opts.AllowedOrigins is
// empty the middleware is a pass-through, preserving the historical behaviour
// (no CORS headers emitted) for callers that don't need cross-origin access.
//
// Preflight (OPTIONS) requests always return 204 with the negotiated CORS
// headers and never invoke next, so the middleware composes correctly with
// authMiddleware: place corsMiddleware as the outer wrapper so preflight
// succeeds without an Authorization header.
func corsMiddleware(next http.Handler, opts CORSOptions) http.Handler {
	if len(opts.AllowedOrigins) == 0 {
		return next
	}
	allowed := make(map[string]struct{}, len(opts.AllowedOrigins))
	allowAny := false
	for _, o := range opts.AllowedOrigins {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		if o == "*" {
			allowAny = true
		}
		allowed[o] = struct{}{}
	}
	headers := mergeUnique(defaultAllowedHeaders, opts.AllowedHeaders)
	methods := mergeUnique(defaultAllowedMethods, opts.AllowedMethods)
	headersHeader := strings.Join(headers, ", ")
	methodsHeader := strings.Join(methods, ", ")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && (allowAny || hasOrigin(allowed, origin)) {
			// Reflect the requesting origin rather than echoing "*" so that
			// Authorization headers remain usable across origins (the CORS +
			// credentials spec forbids the wildcard when credentials are on).
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", headersHeader)
			w.Header().Set("Access-Control-Allow-Methods", methodsHeader)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func hasOrigin(allowed map[string]struct{}, origin string) bool {
	_, ok := allowed[origin]
	return ok
}

func mergeUnique(base, extra []string) []string {
	seen := make(map[string]struct{}, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, list := range [][]string{base, extra} {
		for _, v := range list {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			key := strings.ToLower(v)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}
