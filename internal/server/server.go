package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/AoManoh/openpe/internal/enhancer"
)

type Handler struct {
	service  *enhancer.Service
	errorLog *log.Logger
}

// Options configures the HTTP server handler. The zero value preserves the
// historical no-auth, no-CORS behaviour and is what New uses internally.
type Options struct {
	// Token, when non-empty, enables bearer-token authentication for all
	// routes except those in SkipAuthPaths.
	Token string
	// SkipAuthPaths is the set of request paths that bypass the bearer
	// check. When nil, defaults to ["/healthz"]; pass an explicit slice
	// (possibly empty) to override.
	SkipAuthPaths []string
	// CORS configures Cross-Origin Resource Sharing for Electron / webview
	// callers. Empty AllowedOrigins disables CORS handling entirely.
	CORS CORSOptions
	// Info supplies metadata returned by GET /v1/info. The AuthEnabled,
	// CORSEnabled and CORSOrigins fields are derived automatically from
	// the other Options fields; callers should leave them blank. Version,
	// ListenAddr, and StartedAt are caller-supplied.
	Info ServerInfo
	// ErrorLog receives full internal / upstream errors together with the
	// request_id returned to the client. Nil disables handler-level logging.
	ErrorLog io.Writer
}

// New returns a server handler with no authentication and no CORS handling.
// Convenience wrapper over NewWithOptions kept for backwards compatibility
// with existing call sites (tests, ad-hoc tooling, code that does not yet
// care about auth or CORS).
func New(service *enhancer.Service) http.Handler {
	return NewWithOptions(service, Options{})
}

// NewWithOptions returns a server handler honouring the supplied options.
// When opts.Token and opts.CORS.AllowedOrigins are both zero values the
// returned handler is identical to New(service).
//
// Middleware order (outer → inner): CORS → auth → mux. CORS sits outermost so
// browser preflight (OPTIONS) requests succeed without an Authorization
// header; auth then guards the actual data routes.
func NewWithOptions(service *enhancer.Service, opts Options) http.Handler {
	h := &Handler{service: service, errorLog: newErrorLogger(opts.ErrorLog)}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.health)
	mux.HandleFunc("/v1/prompt-enhance", h.promptEnhance)
	info := opts.Info
	info.AuthEnabled = opts.Token != ""
	info.CORSEnabled = len(opts.CORS.AllowedOrigins) > 0
	info.CORSOrigins = opts.CORS.AllowedOrigins
	mux.Handle("/v1/info", infoHandler(info))
	skip := opts.SkipAuthPaths
	if skip == nil {
		skip = []string{"/healthz"}
	}
	return corsMiddleware(authMiddleware(mux, opts.Token, skip), opts.CORS)
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
		requestID := newRequestID()
		h.logError(r, requestID, http.StatusInternalServerError, errors.New("enhancer service is not configured"))
		writeErrorWithRequestID(w, http.StatusInternalServerError, "internal server error", requestID)
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
		requestID := newRequestID()
		h.logError(r, requestID, http.StatusBadGateway, err)
		writeErrorWithRequestID(w, http.StatusBadGateway, "prompt enhancement failed", requestID)
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
	writeErrorWithRequestID(w, status, message, "")
}

func writeErrorWithRequestID(w http.ResponseWriter, status int, message string, requestID string) {
	if requestID != "" {
		w.Header().Set("x-request-id", requestID)
	}
	writeJSON(w, status, errorResponse{Error: message, RequestID: requestID})
}

type errorResponse struct {
	Error     string `json:"error"`
	RequestID string `json:"request_id,omitempty"`
}

func newErrorLogger(w io.Writer) *log.Logger {
	if w == nil {
		return nil
	}
	return log.New(w, "", log.LstdFlags)
}

func (h *Handler) logError(r *http.Request, requestID string, status int, err error) {
	if h.errorLog == nil {
		return
	}
	path := ""
	if r != nil && r.URL != nil {
		path = r.URL.Path
	}
	method := ""
	if r != nil {
		method = r.Method
	}
	h.errorLog.Printf("request_id=%s method=%s path=%s status=%d error=%v", requestID, method, path, status, err)
}

func newRequestID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err == nil {
		return hex.EncodeToString(data[:])
	}
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}
