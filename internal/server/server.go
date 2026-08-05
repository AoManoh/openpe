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
	service                 *enhancer.Service
	errorLog                *log.Logger
	defaultMaxContextTokens int
}

type requestOptions struct {
	MaxContextTokens int   `json:"max_context_tokens,omitempty"`
	ReturnMetadata   *bool `json:"return_metadata,omitempty"`
}

type enhanceRequest struct {
	Prompt     string             `json:"prompt"`
	Client     string             `json:"client,omitempty"`
	CWD        string             `json:"cwd,omitempty"`
	Mode       string             `json:"mode,omitempty"`
	History    []enhancer.Message `json:"history,omitempty"`
	Rules      []string           `json:"rules,omitempty"`
	Guidelines []string           `json:"guidelines,omitempty"`
	Context    enhancer.Context   `json:"context,omitempty"`
	Options    requestOptions     `json:"options,omitempty"`
}

func (r enhanceRequest) canonical(defaultMaxContextTokens int) enhancer.Request {
	maxContextTokens := r.Options.MaxContextTokens
	if maxContextTokens == 0 {
		maxContextTokens = defaultMaxContextTokens
	}
	return enhancer.Request{
		Prompt:     r.Prompt,
		Client:     r.Client,
		CWD:        r.CWD,
		Mode:       r.Mode,
		History:    r.History,
		Rules:      r.Rules,
		Guidelines: r.Guidelines,
		Context:    r.Context,
		Options:    enhancer.Options{MaxContextTokens: maxContextTokens},
	}
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
	// DefaultMaxContextTokens fills enhancer.Request.Options.MaxContextTokens
	// when the request leaves it unset, so the operator-level budget
	// (OPENPE_MAX_CONTEXT_TOKENS) governs HTTP callers exactly like it
	// governs every hook path. Zero keeps the historical no-budget default.
	DefaultMaxContextTokens int
	// PromptTimeout limits the whole /v1/prompt-enhance handler. It sits
	// inside CORS/auth so synthesized timeout responses retain CORS headers.
	PromptTimeout time.Duration
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
	h := &Handler{
		service:                 service,
		errorLog:                newErrorLogger(opts.ErrorLog),
		defaultMaxContextTokens: opts.DefaultMaxContextTokens,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.health)
	promptHandler := http.Handler(http.HandlerFunc(h.promptEnhance))
	if opts.PromptTimeout > 0 {
		promptHandler = timeoutJSON(promptHandler, opts.PromptTimeout)
	}
	mux.Handle("/v1/prompt-enhance", promptHandler)
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
	var wireReq enhanceRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wireReq); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	// The body must be exactly one JSON document. Without this check the
	// decoder accepted `{"prompt":"a"}{"prompt":"b"}` and silently served the
	// first object — an ambiguous request smuggled past validation.
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid json: trailing data after the request object")
		return
	}
	resp, err := h.service.Enhance(r.Context(), wireReq.canonical(h.defaultMaxContextTokens))
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
	if wireReq.Options.ReturnMetadata != nil && !*wireReq.Options.ReturnMetadata {
		writeJSON(w, http.StatusOK, struct {
			EnhancedPrompt string   `json:"enhanced_prompt"`
			Warnings       []string `json:"warnings,omitempty"`
		}{resp.EnhancedPrompt, resp.Warnings})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func timeoutJSON(next http.Handler, timeout time.Duration) http.Handler {
	timed := http.TimeoutHandler(next, timeout, `{"error":"request timed out"}`+"\n")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		timed.ServeHTTP(w, r)
	})
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
