// Package api wires HTTP+WebSocket handlers to the gateway's deps.
//
// Routing is plain net/http - no router library, because we have <20
// endpoints and the indirection costs more than it saves. Each handler
// is small and easy to grep for.
package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/iicpc/gateway/internal/bus"
	"github.com/iicpc/gateway/internal/cache"
	"github.com/iicpc/gateway/internal/sandbox"
	"github.com/iicpc/gateway/internal/store"
)

type Deps struct {
	DB      *store.DB
	Cache   *cache.Cache
	Bus     *bus.Bus
	Sandbox *sandbox.Sandbox
	Now     func() time.Time
}

func NewRouter(d *Deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "ts": d.Now().UnixMilli()})
	})

	// Submissions
	mux.HandleFunc("POST /api/submissions", d.createSubmission)
	mux.HandleFunc("GET /api/submissions/{id}", d.getSubmission)

	// Runs
	mux.HandleFunc("POST /api/runs", d.startRun)
	mux.HandleFunc("GET /api/runs/{id}", d.getRun)
	mux.HandleFunc("POST /api/runs/{id}/cancel", d.cancelRun)

	// Leaderboard
	mux.HandleFunc("GET /api/leaderboard", d.getLeaderboard)

	// Live telemetry stream
	mux.HandleFunc("GET /ws/runs/{id}", d.streamRun)

	return mux
}

// WithMiddleware composes the request-scoped pipeline: panic recovery,
// CORS for the frontend, structured access logs.
func WithMiddleware(h http.Handler) http.Handler {
	return panicRecovery(corsHeaders(accessLog(h)))
}

func panicRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[api] PANIC %s %s: %v", r.Method, r.URL.Path, rec)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func corsHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(ww, r)
		log.Printf("[api] %d %s %s %v", ww.status, r.Method, r.URL.Path, time.Since(start))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Hijack lets the WebSocket library take over the raw connection for the
// /ws/* upgrade. Without this passthrough the wrapped ResponseWriter no
// longer satisfies http.Hijacker and websocket.Accept fails with 501.
func (s *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := s.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, errors.New("underlying ResponseWriter does not support hijacking")
}

// Flush passes through streaming flushes (used by the WS keepalive path).
func (s *statusWriter) Flush() {
	if fl, ok := s.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

// writeJSON marshals + flushes a response body with the right content
// type. Errors are logged but not propagated - there's nothing useful
// the caller can do once the headers have flushed.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[api] write json: %v", err)
	}
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func newID(prefix string) string {
	return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
}

// withTimeout returns a context bounded by the given duration. Use it
// for handlers that touch external services (DB/Redis/NATS) so a stuck
// dependency can't tie up an HTTP worker indefinitely.
func withTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, d)
}
