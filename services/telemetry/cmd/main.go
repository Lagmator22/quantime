// =====================================================================
// IICPC TELEMETRY INGESTER · main
// ---------------------------------------------------------------------
// Reads runs.*.telemetry from NATS and bulk-inserts into the TimescaleDB
// hypertable. We use pgx's CopyFrom for ~10x throughput vs INSERT.
//
// Batching policy:
//   • Flush whenever buffer hits 5000 samples, OR
//   • Flush every 250ms, whichever comes first.
//
// This keeps per-row commit overhead amortized while guaranteeing the
// live leaderboard is at most ~250ms stale.
//
// On runs.*.summary, we compute the final composite score and write the
// runs row's finished_at + score + metrics fields.
// =====================================================================

package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
)

type sample struct {
	RunID     string  `json:"runId"`
	BotID     int     `json:"botId"`
	OrderID   int64   `json:"orderId"`
	Side      string  `json:"side"`
	Type      string  `json:"type"`
	PriceX100 int64   `json:"priceX100"`
	Qty       int64   `json:"qty"`
	SendTs    int64   `json:"sendTs"` // unix nanos
	AckTs     int64   `json:"ackTs"`
	LatencyNs int64   `json:"latencyNs"`
	Status    string  `json:"status"`
	Err       *string `json:"err"`
}

type summary struct {
	Type       string `json:"type"`
	RunID      string `json:"runId"`
	Bots       int    `json:"bots"`
	DurationMs int64  `json:"duration"`
	EndedAt    int64  `json:"endedAt"`
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Println("[telemetry] booting")

	natsURL := mustEnv("NATS_URL")
	dbURL := mustEnv("DATABASE_URL")
	redisURL := os.Getenv("REDIS_URL") // Optional: leaderboard cache

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Connect Redis for leaderboard ZADD (non-fatal if unavailable)
	var rdb *redis.Client
	if redisURL != "" {
		opts, err := redis.ParseURL(redisURL)
		if err != nil {
			log.Printf("[telemetry] redis parse: %v (leaderboard cache disabled)", err)
		} else {
			rdb = redis.NewClient(opts)
			if pingErr := rdb.Ping(ctx).Err(); pingErr != nil {
				log.Printf("[telemetry] redis ping: %v (leaderboard cache disabled)", pingErr)
				rdb = nil
			} else {
				log.Println("[telemetry] redis connected")
			}
		}
	}


	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("[telemetry] pg: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("[telemetry] pg ping: %v", err)
	}

	nc, err := nats.Connect(natsURL,
		nats.Name("iicpc-telemetry"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)
	if err != nil {
		log.Fatalf("[telemetry] nats: %v", err)
	}
	defer nc.Drain()

	// ── Buffered batching ──────────────────────────────────────────
	buf := make(chan sample, 50_000)

	_, err = nc.Subscribe("runs.*.telemetry", func(m *nats.Msg) {
		var s sample
		if err := json.Unmarshal(m.Data, &s); err != nil {
			return
		}
		select {
		case buf <- s:
		default:
			// Buffer full — drop sample rather than block the NATS
			// callback (which would back up the entire bus). At-most-
			// once is the documented contract for telemetry.
		}
	})
	if err != nil {
		log.Fatalf("[telemetry] subscribe telemetry: %v", err)
	}

	// ── Run-finished handler: compute final score, write runs row ──
	_, err = nc.Subscribe("runs.*.summary", func(m *nats.Msg) {
		var s summary
		if err := json.Unmarshal(m.Data, &s); err != nil {
			return
		}
		finalizeRun(context.Background(), pool, rdb, s)
	})
	if err != nil {
		log.Fatalf("[telemetry] subscribe summary: %v", err)
	}

	// ── Flusher loop ───────────────────────────────────────────────
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		flusher(ctx, pool, rdb, buf)
	}()

	log.Println("[telemetry] ready")
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	cancel()
	wg.Wait()
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("missing env: %s", k)
	}
	return v
}

// runAgg holds the live rolling counters for one in-flight run. It is owned
// exclusively by the flusher goroutine, so it needs no synchronization.
type runAgg struct {
	orders     int64 // cumulative orders seen this run
	errors     int64 // cumulative transport errors
	sumLatency int64 // cumulative latency (ns) for a running average
	lastOrders int64 // orders count at the previous live tick (for instantaneous TPS)
	idleTicks  int   // consecutive live ticks with no new orders (for GC)
}

func flusher(ctx context.Context, pool *pgxpool.Pool, rdb *redis.Client, in <-chan sample) {
	const batchMax = 5000
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()

	// Live snapshot ticker: every 1s we push a rolling per-run aggregate to
	// the Redis pubsub channel the gateway WebSocket fans out to the browser.
	live := time.NewTicker(1 * time.Second)
	defer live.Stop()
	aggs := map[string]*runAgg{}

	batch := make([]sample, 0, batchMax)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := bulkInsert(ctx, pool, batch); err != nil {
			log.Printf("[telemetry] flush %d rows: %v", len(batch), err)
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case s := <-in:
			batch = append(batch, s)
			a := aggs[s.RunID]
			if a == nil {
				a = &runAgg{}
				aggs[s.RunID] = a
			}
			a.orders++
			a.sumLatency += s.LatencyNs
			if s.Err != nil {
				a.errors++
			}
			if len(batch) >= batchMax {
				flush()
			}
		case <-tick.C:
			flush()
		case <-live.C:
			publishLive(ctx, rdb, aggs)
		}
	}
}

// publishLive emits a rolling metrics snapshot per active run to the Redis
// channel run:<id>:updates. The gateway's /ws/runs/{id} handler subscribes to
// this channel, so this is what makes the live leaderboard actually stream.
// Runs that go idle for 15 consecutive ticks are dropped to bound memory.
func publishLive(ctx context.Context, rdb *redis.Client, aggs map[string]*runAgg) {
	if rdb == nil {
		return
	}
	for runID, a := range aggs {
		tps := a.orders - a.lastOrders // orders observed in the last ~1s
		a.lastOrders = a.orders
		if tps == 0 {
			a.idleTicks++
			if a.idleTicks >= 15 {
				delete(aggs, runID)
			}
			continue // nothing new to report this tick
		}
		a.idleTicks = 0

		var avgLatMs, errPct float64
		if a.orders > 0 {
			avgLatMs = float64(a.sumLatency) / float64(a.orders) / 1e6
			errPct = 100 * float64(a.errors) / float64(a.orders)
		}
		payload, _ := json.Marshal(map[string]any{
			"type":     "metrics",
			"runId":    runID,
			"status":   "running",
			"orders":   a.orders,
			"tps":      tps,
			"avgLatMs": avgLatMs,
			"errPct":   errPct,
			"ts":       time.Now().UnixMilli(),
		})
		if err := rdb.Publish(ctx, "run:"+runID+":updates", payload).Err(); err != nil {
			log.Printf("[telemetry] live publish run=%s: %v", runID, err)
		}
	}
}

// bulkInsert uses CopyFrom — the fastest way to land bulk rows in
// Postgres. At 50k rows/sec it's bottlenecked by network, not by SQL.
func bulkInsert(ctx context.Context, pool *pgxpool.Pool, rows []sample) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	src := pgxCopySource{rows: rows, idx: -1}
	_, err = conn.Conn().CopyFrom(
		ctx,
		[]string{"telemetry"},
		[]string{"ts", "run_id", "bot_id", "order_id", "side", "type", "price_x100", "qty", "latency_ns", "status", "filled", "err"},
		&src,
	)
	return err
}

type pgxCopySource struct {
	rows []sample
	idx  int
}

func (s *pgxCopySource) Next() bool {
	s.idx++
	return s.idx < len(s.rows)
}

func (s *pgxCopySource) Values() ([]any, error) {
	r := s.rows[s.idx]
	ts := time.Unix(0, r.AckTs)
	return []any{
		ts,
		r.RunID,
		r.BotID,
		r.OrderID,
		encodeSide(r.Side),
		encodeType(r.Type),
		r.PriceX100,
		r.Qty,
		r.LatencyNs,
		r.Status,
		0, // filled — derived later
		r.Err,
	}, nil
}

func (s *pgxCopySource) Err() error { return nil }

func encodeSide(s string) int16 {
	if strings.EqualFold(s, "sell") {
		return 1
	}
	return 0
}

func encodeType(t string) int16 {
	switch strings.ToLower(t) {
	case "market":
		return 1
	case "ioc":
		return 2
	case "fok":
		return 3
	case "postonly":
		return 4
	case "cancel":
		return 5
	}
	return 0
}

// finalizeRun computes the composite score from the hypertable rollup
// and updates the runs row. We pull the 1-second continuous aggregate
// view so this query stays cheap regardless of run length.
func finalizeRun(ctx context.Context, pool *pgxpool.Pool, rdb *redis.Client, s summary) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Aggregate latency percentiles and throughput from telemetry rows
	row := pool.QueryRow(ctx, `
		WITH agg AS (
			SELECT
			    percentile_cont(0.50) WITHIN GROUP (ORDER BY latency_ns) AS p50,
			    percentile_cont(0.90) WITHIN GROUP (ORDER BY latency_ns) AS p90,
			    percentile_cont(0.99) WITHIN GROUP (ORDER BY latency_ns) AS p99,
			    count(*)::float / NULLIF(EXTRACT(EPOCH FROM (max(ts) - min(ts))),0) AS tps,
			    sum(CASE WHEN err IS NOT NULL THEN 1 ELSE 0 END)::float / NULLIF(count(*),0) AS err_rate
			FROM telemetry
			WHERE run_id = $1
		)
		SELECT COALESCE(p50,0), COALESCE(p90,0), COALESCE(p99,0), COALESCE(tps,0), COALESCE(err_rate,0) FROM agg
	`, s.RunID)
	var p50, p90, p99, tps, errRate float64
	if err := row.Scan(&p50, &p90, &p99, &tps, &errRate); err != nil {
		log.Printf("[telemetry] aggregate run=%s: %v", s.RunID, err)
		return
	}

	// Composite score: weights mirror the judge console defaults.
	// Lower latency / higher tps = higher score. Errors cost points.
	speedScore := 100.0 * mathExpDecay(p99, 200_000_000) // p99 in ns, 200ms yields ~37
	tputScore := 100.0 * mathSat(tps, 200_000)           // 200k ops/s caps at 100

	// Correctness: prefer the real oracle score (price-time-priority + fill
	// accuracy, validated at deploy time and stored on the submission) for
	// this run's submission. Fall back to the transport error-rate proxy only
	// when no oracle score is available.
	correctnessScore := 100.0 * (1 - errRate)
	var oracle *float64
	if qerr := pool.QueryRow(ctx, `
		SELECT (s.correctness->>'score')::float
		FROM runs r JOIN submissions s ON s.id = r.submission_id
		WHERE r.id = $1 AND s.correctness IS NOT NULL
	`, s.RunID).Scan(&oracle); qerr == nil && oracle != nil {
		correctnessScore = *oracle
	}

	composite := 0.4*speedScore + 0.4*tputScore + 0.2*correctnessScore

	metrics, _ := json.Marshal(map[string]any{
		"p50":     p50,
		"p90":     p90,
		"p99":     p99,
		"tps":     tps,
		"err_pct": errRate * 100,
	})

	_, err := pool.Exec(ctx, `
		UPDATE runs SET status='finished', finished_at=now(), score=$2, metrics=$3 WHERE id=$1
	`, s.RunID, composite, metrics)
	if err != nil {
		log.Printf("[telemetry] update run %s: %v", s.RunID, err)
	}

	// Resolve team_id for this run so we can key the leaderboard
	var teamID string
	teamErr := pool.QueryRow(ctx, `SELECT team_id FROM runs WHERE id=$1`, s.RunID).Scan(&teamID)
	if teamErr != nil {
		log.Printf("[telemetry] resolve team for run %s: %v", s.RunID, teamErr)
		teamID = s.RunID // Fallback to runID if team lookup fails
	}

	// Write score to Redis sorted set for sub-millisecond leaderboard reads.
	// ZADD only updates if the new score is higher (GT flag).
	if rdb != nil {
		zaddErr := rdb.ZAddGT(ctx, "leaderboard:scores", redis.Z{
			Score:  composite,
			Member: teamID,
		}).Err()
		if zaddErr != nil {
			log.Printf("[telemetry] redis ZADD run=%s: %v", s.RunID, zaddErr)
		}

		// Store metrics JSON per team for leaderboard detail display
		rdb.HSet(ctx, "leaderboard:metrics", teamID, string(metrics))

		// Push a final snapshot to the live WS stream so the run page shows
		// completion with the authoritative percentiles + composite score.
		final, _ := json.Marshal(map[string]any{
			"type":   "final",
			"runId":  s.RunID,
			"status": "finished",
			"p50":    p50,
			"p90":    p90,
			"p99":    p99,
			"tps":    tps,
			"errPct": errRate * 100,
			"score":  composite,
			"ts":     time.Now().UnixMilli(),
		})
		if pubErr := rdb.Publish(ctx, "run:"+s.RunID+":updates", final).Err(); pubErr != nil {
			log.Printf("[telemetry] final publish run=%s: %v", s.RunID, pubErr)
		}
	}

	log.Printf("[telemetry] run=%s team=%s p50=%.0fns p99=%.0fns tps=%.0f err=%.2f%% score=%.1f",
		s.RunID, teamID, p50, p99, tps, errRate*100, composite)
}

func mathExpDecay(x, k float64) float64 {
	if x <= 0 {
		return 1
	}
	v := 1.0 / (1.0 + x/k)
	return v
}

func mathSat(x, k float64) float64 {
	v := x / k
	if v > 1 {
		return 1
	}
	return v
}
