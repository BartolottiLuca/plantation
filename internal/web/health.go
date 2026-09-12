package web

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Version and Commit are injected at link time via -ldflags -X. Defaults are
// for `go run` / unstamped local builds.
var (
	Version = "dev"
	Commit  = "unknown"
)

const pingTimeout = time.Second

// RegisterHealth mounts liveness, readiness, and version on mux.
// ping is the readiness check (typically a database ping). It is never called
// from /healthz — a CNPG failover must not restart a live process.
func RegisterHealth(mux *http.ServeMux, ping func(context.Context) error) {
	mux.HandleFunc("GET /healthz", noIndex(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	mux.HandleFunc("GET /readyz", noIndex(func(w http.ResponseWriter, r *http.Request) {
		if ping == nil {
			w.WriteHeader(http.StatusOK)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), pingTimeout)
		defer cancel()
		if err := ping(ctx); err != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	mux.HandleFunc("GET /version", noIndex(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Version string `json:"version"`
			Commit  string `json:"commit"`
		}{Version: Version, Commit: Commit})
	}))
}

func noIndex(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Robots-Tag", "noindex")
		next(w, r)
	}
}
