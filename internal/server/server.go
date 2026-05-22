package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/AoManoh/openpe/internal/enhancer"
)

type Handler struct {
	service *enhancer.Service
}

// Options configures the HTTP server handler. The zero value preserves the
// historical no-auth behaviour and is what New uses internally.
type Options struct {
	// Token, when non-empty, enables bearer-token authentication for all
	// routes except those in SkipAuthPaths.
	Token string
	// SkipAuthPaths is the set of request paths that bypass the bearer
	// check. When nil, defaults to ["/healthz"]; pass an explicit slice
	// (possibly empty) to override.
	SkipAuthPaths []string
}

// New returns a server handler with no authentication. Convenience wrapper
// over NewWithOptions kept for backwards compatibility with existing call
// sites (tests, ad-hoc tooling, code that does not yet care about auth).
func New(service *enhancer.Service) http.Handler {
	return NewWithOptions(service, Options{})
}

// NewWithOptions returns a server handler honouring the supplied options.
// When opts.Token is empty the returned handler is identical to New(service).
func NewWithOptions(service *enhancer.Service, opts Options) http.Handler {
	h := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.health)
	mux.HandleFunc("/v1/prompt-enhance", h.promptEnhance)
	skip := opts.SkipAuthPaths
	if skip == nil {
		skip = []string{"/healthz"}
	}
	return authMiddleware(mux, opts.Token, skip)
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) promptEnhance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.service == nil {
		writeError(w, http.StatusInternalServerError, "enhancer service is not configured")
		return
	}
	defer r.Body.Close()
	var req enhancer.Request
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	resp, err := h.service.Enhance(r.Context(), req)
	if err != nil {
		var validation enhancer.ValidationError
		if errors.As(err, &validation) {
			writeError(w, http.StatusBadRequest, validation.Error())
			return
		}
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
