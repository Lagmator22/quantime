// =====================================================================
// IICPC GATEWAY · main
// ---------------------------------------------------------------------
// HTTP + WebSocket API surface for the platform. Responsibilities:
//
//   • POST /api/submissions       — accept code upload, persist, build container
//   • POST /api/runs              — kick off a stress run (publishes nats msg)
//   • GET  /api/runs/:id          — run metadata + final metrics
//   • GET  /api/leaderboard       — composite score ranking
//   • GET  /ws/runs/:id           — live telemetry stream while a run is active
//
// The gateway is the only service that holds a Docker socket. It uses
// it to (a) build images from uploaded source via `docker build` and
// (b) `docker run --memory --cpus --network iicpc-net` to spawn each
// contestant submission as a sibling container.
//
// Failure modes the gateway accounts for:
//   • Postgres connection blip → retry with backoff on startup
//   • Submission container crashlooping → kill after 3 restarts in 30s
//   • Run abandoned by client → cleanup hook on WS close
//   • Concurrent run cap → rejected with 429 if > MAX_CONCURRENT_RUNS
// =====================================================================

package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/iicpc/gateway/internal/api"
	"github.com/iicpc/gateway/internal/bus"
	"github.com/iicpc/gateway/internal/cache"
	"github.com/iicpc/gateway/internal/sandbox"
	"github.com/iicpc/gateway/internal/store"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)
	log.Println("[gateway] booting...")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── Wire up dependencies. Each connect retries with backoff so a
	// slow Postgres / NATS startup doesn't crash the gateway. ─────────
	db, err := store.Connect(ctx, mustEnv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("[gateway] postgres: %v", err)
	}
	defer db.Close()

	rdb, err := cache.Connect(ctx, mustEnv("REDIS_URL"))
	if err != nil {
		log.Fatalf("[gateway] redis: %v", err)
	}
	defer rdb.Close()

	nc, err := bus.Connect(ctx, mustEnv("NATS_URL"))
	if err != nil {
		log.Fatalf("[gateway] nats: %v", err)
	}
	defer nc.Drain()

	sb, err := sandbox.New(os.Getenv("DOCKER_HOST"), envOr("SUBMISSIONS_DIR", "/data/submissions"))
	if err != nil {
		log.Fatalf("[gateway] sandbox: %v", err)
	}

	deps := &api.Deps{
		DB:      db,
		Cache:   rdb,
		Bus:     nc,
		Sandbox: sb,
		Now:     time.Now,
	}

	mux := api.NewRouter(deps)
	srv := &http.Server{
		Addr:              ":7070",
		Handler:           api.WithMiddleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
		// Long write timeout for /ws streams; the websocket layer
		// applies its own per-message deadlines.
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	// ── Graceful shutdown ──────────────────────────────────────────
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Printf("[gateway] listening on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[gateway] http: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Println("[gateway] shutdown initiated")

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	_ = srv.Shutdown(shutdownCtx)
	cancel()
	wg.Wait()
	log.Println("[gateway] bye")
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("[gateway] missing env: %s", k)
	}
	return v
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
