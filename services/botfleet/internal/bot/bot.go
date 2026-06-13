// Package bot is one trading bot. Each instance runs in its own
// goroutine, picks a strategy from a deterministic mix, and fires
// HTTP POST /submit at the developer's endpoint until ctx done.
//
// We use a per-bot xoshiro RNG seeded with (run_seed + bot_id) so the
// entire run is byte-for-byte reproducible. Judges who suspect a
// favorable seed can replay any run with the same seed value.
package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/valyala/fasthttp"
)

type Config struct {
	BotID    int
	RunID    string
	Endpoint string
	Profile  string // sustained|burst|adversarial
	Seed     int64
	NATS     *nats.Conn
}

type Bot struct {
	cfg     Config
	cli     *fasthttp.Client
	rng     *xoshiro
	nextOID atomic.Int64
}

func New(cfg Config) *Bot {
	return &Bot{
		cfg: cfg,
		cli: &fasthttp.Client{
			MaxConnsPerHost:     128,
			MaxIdleConnDuration: 30 * time.Second,
			ReadTimeout:         500 * time.Millisecond,
			WriteTimeout:        500 * time.Millisecond,
		},
		rng: newXoshiro(uint64(cfg.Seed)),
	}
}

// Run drives the bot until the context is cancelled. The inter-order
// delay is profile-specific and modulated by the RNG so the load isn't
// pathologically periodic.
func (b *Bot) Run(ctx context.Context) {
	midPrice := 100_00 // price * 100, integer ticks
	url := b.cfg.Endpoint + "/submit"

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Profile-driven inter-arrival time. Lower = more aggressive.
		var pauseUs int64
		switch b.cfg.Profile {
		case "burst":
			// Every 1s, 100ms of high-rate; otherwise relaxed.
			if (time.Now().UnixMilli()/100)%10 == 0 {
				pauseUs = 50 + int64(b.rng.next()%200)
			} else {
				pauseUs = 500 + int64(b.rng.next()%500)
			}
		case "adversarial":
			pauseUs = 20 + int64(b.rng.next()%80) // very tight, cancels heavy
		default: // sustained
			pauseUs = 200 + int64(b.rng.next()%400)
		}
		time.Sleep(time.Duration(pauseUs) * time.Microsecond)

		// Build a plausible order. ~70% limit, ~20% market, ~10% cancel.
		orderID := b.nextOID.Add(1)
		side := "buy"
		if b.rng.next()%2 == 0 {
			side = "sell"
		}
		px := midPrice + int(b.rng.next()%50) - 25
		qty := 1 + int(b.rng.next()%9)
		mix := b.rng.next() % 100
		var typ string
		switch {
		case mix < 70:
			typ = "limit"
		case mix < 90:
			typ = "market"
		default:
			typ = "cancel"
		}

		body, _ := json.Marshal(map[string]any{
			"id":       orderID,
			"clientId": b.cfg.BotID,
			"side":     side,
			"type":     typ,
			"price":    px,
			"qty":      qty,
			"targetId": orderID - int64(b.rng.next()%5+1), // approximate prior id
			"ts":       time.Now().UnixNano(),
		})

		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		req.Header.SetMethod(fasthttp.MethodPost)
		req.Header.SetContentType("application/json")
		req.SetRequestURI(url)
		req.SetBody(body)

		sendTS := time.Now().UnixNano()
		err := b.cli.DoTimeout(req, resp, 500*time.Millisecond)
		ackTS := time.Now().UnixNano()
		status := ""
		var errStr *string
		if err != nil {
			s := err.Error()
			errStr = &s
		} else {
			// Parse the first ack's status if available, so the
			// telemetry table can distinguish accepted/rejected/etc.
			var r struct {
				Acks []struct{ Status string } `json:"acks"`
			}
			if json.Unmarshal(resp.Body(), &r) == nil && len(r.Acks) > 0 {
				status = r.Acks[0].Status
			}
		}

		// Publish telemetry. JSON is wasteful here in the steady state;
		// MessagePack or protobuf would cut the bytes ~3x. We stay with
		// JSON for now because debuggability in NATS CLI matters during
		// development. (See BLUEPRINT.md §6 for a migration plan.)
		sample := map[string]any{
			"runId":     b.cfg.RunID,
			"botId":     b.cfg.BotID,
			"orderId":   orderID,
			"side":      side,
			"type":      typ,
			"priceX100": px,
			"qty":       qty,
			"sendTs":    sendTS,
			"ackTs":     ackTS,
			"latencyNs": ackTS - sendTS,
			"status":    status,
			"err":       errStr,
		}
		buf, _ := json.Marshal(sample)
		_ = b.cfg.NATS.Publish(fmt.Sprintf("runs.%s.telemetry", b.cfg.RunID), buf)

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
	}
}
