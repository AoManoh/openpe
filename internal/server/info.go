package server

import (
	"net/http"
	"time"
)

// ServerInfo summarises the running server's runtime metadata. It is returned
// from GET /v1/info after bearer authentication so IDE installers can confirm
// the descriptor they read from disk actually matches the live process.
//
// All fields are safe to expose post-authentication; no secrets are present.
// The bearer token itself is intentionally NOT included — installers already
// hold it (they used it to authenticate this call).
type ServerInfo struct {
	Version     string    `json:"version,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	ListenAddr  string    `json:"listen_addr,omitempty"`
	AuthEnabled bool      `json:"auth_enabled"`
	CORSEnabled bool      `json:"cors_enabled"`
	CORSOrigins []string  `json:"cors_origins,omitempty"`
}

// infoHandler returns an http.Handler that serves a frozen snapshot of info
// as JSON. The snapshot is taken at handler-construction time; concurrent
// requests do not interfere with each other.
func infoHandler(info ServerInfo) http.Handler {
	snapshot := info
	if snapshot.StartedAt.IsZero() {
		snapshot.StartedAt = time.Now().UTC()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
	})
}
