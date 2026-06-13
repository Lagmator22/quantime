// =====================================================================
// IICPC BOT FLEET · main
// ---------------------------------------------------------------------
// Each botfleet container subscribes to runs.*.control. When a "start"
// command arrives, it spawns N bot goroutines that hammer the named
// endpoint with deterministic orders for the configured duration.
//
// Telemetry is fired-and-forget over NATS to the ingester. We use
// fasthttp instead of net/http because at high RPS the standard
// client's connection pool contention becomes the bottleneck.
//
// To scale horizontally, run more replicas: each replica handles
// (BOTS_PER_INSTANCE) bots. The system as a whole hits
//   total_bots = replicas × BOTS_PER_INSTANCE
// orders, so a 4-replica × 250-bot config = 1000 concurrent bots, which
// is enough to saturate a single-pod submission on a modest VM.
// =====================================================================

package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/iicpc/botfleet/internal/bot"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
)

type startCmd struct {
	Type         string `json:"type"`
	RunID        string `json:"runId"`
	Endpoint     string `json:"endpoint"`
	Profile      string `json:"profile"`
	Seed         int64  `json:"seed"`
	DurationSec  int    `json:"durationSec"`
	BotsPerFleet int    `json:"botsPerFleet"`
}

type cancelCmd struct {
	Type  string `json:"type"`
	RunID string `json:"runId"`
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Println("[botfleet] booting")

	natsURL := envOr("NATS_URL", "nats://nats:4222")
	bots := envInt("BOTS_PER_INSTANCE", 50)
	redisURL := envOr("REDIS_URL", "redis://redis:6379")

	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Fatalf("[botfleet] redis parse url: %v", err)
	}
	rdb := redis.NewClient(opts)
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		log.Fatalf("[botfleet] redis ping: %v", err)
	}
	log.Println("[botfleet] redis connected")

	nc, err := nats.Connect(natsURL,
		nats.Name("iicpc-botfleet"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)
	if err != nil {
		log.Fatalf("[botfleet] nats: %v", err)
	}
	defer nc.Drain()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// One supervisor per active run. The map is guarded by mu so we
	// can safely add/remove from inside multiple NATS handlers.
	var mu sync.Mutex
	supervisors := map[string]context.CancelFunc{}

	// Subscribe to control messages for any run. The wildcard means we
	// don't need to coordinate which botfleet container picks up which
	// run - every replica receives every command and joins the fleet.
	_, err = nc.Subscribe("runs.*.control", func(m *nats.Msg) {
		// Inspect the type field first; this is a hot path during a
		// busy hackathon (~10s of runs/minute starting), so we avoid
		// allocating two full structs.
		var head struct {
			Type  string `json:"type"`
			RunID string `json:"runId"`
		}
		if err := json.Unmarshal(m.Data, &head); err != nil {
			return
		}
		switch head.Type {
		case "start":
			var c startCmd
			if err := json.Unmarshal(m.Data, &c); err != nil {
				log.Printf("[botfleet] bad start: %v", err)
				return
			}
			if c.BotsPerFleet > 0 {
				bots = c.BotsPerFleet
			}
			mu.Lock()
			if _, exists := supervisors[c.RunID]; exists {
				mu.Unlock()
				return // already running on this replica
			}
			runCtx, runCancel := context.WithTimeout(ctx, time.Duration(c.DurationSec+5)*time.Second)
			supervisors[c.RunID] = runCancel
			mu.Unlock()

			go func() {
				defer func() {
					mu.Lock()
					delete(supervisors, c.RunID)
					mu.Unlock()
				}()

				// 1. Claim replica index via Redis
				key := "run:" + c.RunID + ":replica_index"
				val, err := rdb.Incr(runCtx, key).Result()
				if err != nil {
					log.Printf("[botfleet] failed to get replica index for %s: %v", c.RunID, err)
					return
				}
				rdb.Expire(runCtx, key, time.Hour)

				replicaIdx := int(val - 1)
				runFleet(runCtx, nc, c, bots, replicaIdx)
			}()

		case "cancel":
			var c cancelCmd
			if err := json.Unmarshal(m.Data, &c); err != nil {
				return
			}
			mu.Lock()
			if cancelFn, ok := supervisors[c.RunID]; ok {
				cancelFn()
			}
			mu.Unlock()
		}
	})
	if err != nil {
		log.Fatalf("[botfleet] subscribe: %v", err)
	}
	log.Printf("[botfleet] ready; %d bots/instance", bots)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Println("[botfleet] shutting down")
	mu.Lock()
	for _, c := range supervisors {
		c()
	}
	mu.Unlock()
	time.Sleep(500 * time.Millisecond)
}

func runFleet(ctx context.Context, nc *nats.Conn, c startCmd, bots, replicaIdx int) {
	log.Printf("[botfleet] run=%s endpoint=%s profile=%s bots=%d replicaIdx=%d", c.RunID, c.Endpoint, c.Profile, bots, replicaIdx)
	start := time.Now()

	// Stagger bot start to avoid thundering-herd on the submission's
	// first-connect path. We spread launches over the first ~250ms.
	stagger := time.Duration(0)
	if bots > 0 {
		stagger = 250 * time.Millisecond / time.Duration(bots)
	}

	var wg sync.WaitGroup
	for i := 0; i < bots; i++ {
		wg.Add(1)
		go func(localBotIdx int) {
			defer wg.Done()
			time.Sleep(time.Duration(localBotIdx) * stagger)
			
			botID := replicaIdx*bots + localBotIdx
			
			b := bot.New(bot.Config{
				BotID:    botID,
				RunID:    c.RunID,
				Endpoint: c.Endpoint,
				Profile:  c.Profile,
				Seed:     c.Seed + int64(botID), // per-bot seed
				NATS:     nc,
			})
			b.Run(ctx)
		}(i)
	}
	wg.Wait()
	log.Printf("[botfleet] run=%s done in %v", c.RunID, time.Since(start))

	// Publish a summary message so the telemetry ingester can finalize
	// the run row and update the leaderboard.
	summary, _ := json.Marshal(map[string]any{
		"type":     "summary",
		"runId":    c.RunID,
		"bots":     bots,
		"duration": time.Since(start).Milliseconds(),
		"endedAt":  time.Now().UnixMilli(),
	})
	_ = nc.Publish("runs."+c.RunID+".summary", summary)
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func envInt(k string, d int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return d
}
