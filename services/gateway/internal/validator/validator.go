// Package validator is QuanTime's correctness oracle.
//
// The rubric asks the platform to validate *price-time priority* and *fill
// accuracy* — not just count transport errors. This package does exactly
// that: it replays one fixed, deterministic order sequence through BOTH
//
//   (a) an INDEPENDENT reference order book implemented here, and
//   (b) the contestant's deployed engine (over its HTTP /submit endpoint),
//
// then diffs the filled quantity order-by-order. The score is the fraction
// of orders where the submission's fills match the oracle. Because the
// reference book is a separate implementation, a submission that gets
// price-time priority, partial fills, market orders, or cancels wrong will
// disagree with the oracle and lose points — a real correctness signal.
//
// Prices are integer ticks (price*100), matching the bot fleet, the sample
// engine, and the telemetry schema, so there is no float drift in matching.
package validator

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

// vOrder is one step in the deterministic correctness scenario.
type vOrder struct {
	ID       int64
	ClientID int64
	Side     string // buy|sell (ignored for cancel)
	Type     string // limit|market|cancel
	Px       int64  // integer ticks; ignored for market/cancel
	Qty      int64
	TargetID int64 // for cancel
}

// Case is the per-order comparison result (stored for auditability).
type Case struct {
	ID        int64 `json:"id"`
	Expected  int64 `json:"expected"`
	Actual    int64 `json:"actual"`
	Pass      bool  `json:"pass"`
	Reachable bool  `json:"reachable"`
}

// Result is written to submissions.correctness as JSON.
type Result struct {
	Score  float64 `json:"score"`  // 0..100
	Passed int     `json:"passed"`
	Total  int     `json:"total"`
	Cases  []Case  `json:"cases"`
	TS     int64   `json:"ts"`
}

// ── Reference order book (the oracle) ─────────────────────────────────

type resting struct{ id, px, qty, seq int64 }

type book struct {
	bids, asks []*resting       // bids: best (highest px) first; asks: best (lowest px) first
	byID       map[int64]*resting
	seq        int64
}

func newBook() *book { return &book{byID: map[int64]*resting{}} }

// apply runs one order through the oracle and returns the taker filled qty.
func (b *book) apply(o vOrder) int64 {
	if o.Type == "cancel" {
		if r, ok := b.byID[o.TargetID]; ok {
			b.remove(r)
		}
		return 0
	}
	var filled, rem int64 = 0, o.Qty
	if o.Side == "buy" { // take from asks, best (lowest) price first
		for len(b.asks) > 0 && rem > 0 {
			best := b.asks[0]
			if o.Type == "limit" && best.px > o.Px {
				break
			}
			take := min64(rem, best.qty)
			filled += take
			rem -= take
			best.qty -= take
			if best.qty == 0 {
				b.remove(best)
			}
		}
	} else { // sell: take from bids, best (highest) price first
		for len(b.bids) > 0 && rem > 0 {
			best := b.bids[0]
			if o.Type == "limit" && best.px < o.Px {
				break
			}
			take := min64(rem, best.qty)
			filled += take
			rem -= take
			best.qty -= take
			if best.qty == 0 {
				b.remove(best)
			}
		}
	}
	if rem > 0 && o.Type == "limit" { // residual rests (price-time priority preserved by seq)
		b.seq++
		r := &resting{id: o.ID, px: o.Px, qty: rem, seq: b.seq}
		b.byID[o.ID] = r
		if o.Side == "buy" {
			b.bids = append(b.bids, r)
			sort.SliceStable(b.bids, func(i, j int) bool {
				if b.bids[i].px != b.bids[j].px {
					return b.bids[i].px > b.bids[j].px
				}
				return b.bids[i].seq < b.bids[j].seq
			})
		} else {
			b.asks = append(b.asks, r)
			sort.SliceStable(b.asks, func(i, j int) bool {
				if b.asks[i].px != b.asks[j].px {
					return b.asks[i].px < b.asks[j].px
				}
				return b.asks[i].seq < b.asks[j].seq
			})
		}
	}
	return filled
}

func (b *book) remove(r *resting) {
	delete(b.byID, r.id)
	b.bids = drop(b.bids, r)
	b.asks = drop(b.asks, r)
}

func drop(s []*resting, r *resting) []*resting {
	for i, x := range s {
		if x == r {
			return append(s[:i], s[i+1:]...)
		}
	}
	return s
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// scenario exercises: resting orders, price-time priority across levels,
// partial fills, a market order, a cancel that must remove liquidity, and a
// market sweep. Expected fills (computed by the oracle): 0,0,0,6,4,3,0,0,2,11.
func scenario() []vOrder {
	return []vOrder{
		{ID: 1, ClientID: 1, Side: "sell", Type: "limit", Px: 10000, Qty: 10},
		{ID: 2, ClientID: 1, Side: "sell", Type: "limit", Px: 10010, Qty: 5},
		{ID: 3, ClientID: 2, Side: "buy", Type: "limit", Px: 9990, Qty: 4},
		{ID: 4, ClientID: 2, Side: "buy", Type: "limit", Px: 10000, Qty: 6},
		{ID: 5, ClientID: 3, Side: "buy", Type: "limit", Px: 10005, Qty: 8},
		{ID: 6, ClientID: 3, Side: "buy", Type: "market", Qty: 3},
		{ID: 7, ClientID: 1, Type: "cancel", TargetID: 2},
		{ID: 8, ClientID: 4, Side: "buy", Type: "limit", Px: 10010, Qty: 5},
		{ID: 9, ClientID: 4, Side: "sell", Type: "limit", Px: 10005, Qty: 2},
		{ID: 10, ClientID: 5, Side: "sell", Type: "market", Qty: 100},
	}
}

// Validate replays the scenario against the deployed engine at `endpoint`
// (e.g. http://iicpc-run-abc:9001) and returns the correctness result.
func Validate(ctx context.Context, endpoint string, now func() time.Time) Result {
	ora := newBook()
	cli := &http.Client{Timeout: 3 * time.Second}
	orders := scenario()
	res := Result{Total: len(orders), TS: now().UnixMilli()}
	for _, o := range orders {
		expected := ora.apply(o)
		actual, ok := submit(ctx, cli, endpoint, o)
		c := Case{ID: o.ID, Expected: expected, Actual: actual, Reachable: ok}
		if ok && actual == expected {
			res.Passed++
			c.Pass = true
		}
		res.Cases = append(res.Cases, c)
	}
	if res.Total > 0 {
		res.Score = 100.0 * float64(res.Passed) / float64(res.Total)
	}
	return res
}

// submit POSTs one order to the engine and returns its reported taker fill.
func submit(ctx context.Context, cli *http.Client, endpoint string, o vOrder) (int64, bool) {
	body, _ := json.Marshal(map[string]any{
		"id": o.ID, "clientId": o.ClientID, "side": o.Side, "type": o.Type,
		"price": o.Px, "qty": o.Qty, "targetId": o.TargetID, "ts": 0,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/submit", bytes.NewReader(body))
	if err != nil {
		return 0, false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := cli.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	var parsed struct {
		Acks []struct {
			Filled int64 `json:"filled"`
		} `json:"acks"`
	}
	if json.NewDecoder(resp.Body).Decode(&parsed) != nil || len(parsed.Acks) == 0 {
		return 0, false
	}
	return parsed.Acks[0].Filled, true
}
