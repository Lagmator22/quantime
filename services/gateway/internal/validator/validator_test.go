package validator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

var fixedNow = func() time.Time { return time.Unix(0, 0) }

// The oracle must compute these exact taker fills for the scenario. If this
// breaks, the correctness score is meaningless, so it's the load-bearing test.
func TestOracleScenarioFills(t *testing.T) {
	want := []int64{0, 0, 0, 6, 4, 3, 0, 0, 2, 11}
	b := newBook()
	for i, o := range scenario() {
		if got := b.apply(o); got != want[i] {
			t.Errorf("scenario[%d] id=%d: oracle filled=%d, want %d", i, o.ID, got, want[i])
		}
	}
}

// A perfectly-correct engine (echoes the oracle's fills) must score 100.
func TestValidate_CorrectEngine(t *testing.T) {
	srv := httptest.NewServer(echoOracle())
	defer srv.Close()
	r := Validate(context.Background(), srv.URL, fixedNow)
	if r.Score != 100 || r.Passed != r.Total {
		t.Fatalf("correct engine scored %.0f (%d/%d), want 100", r.Score, r.Passed, r.Total)
	}
}

// A broken engine that never fills must be CAUGHT - proving the oracle is a
// real signal, not always-100. Only the 5 expected-zero orders pass → 50%.
func TestValidate_BrokenEngineCaught(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"acks": []map[string]any{{"status": "rejected", "filled": 0}}})
	}))
	defer srv.Close()
	r := Validate(context.Background(), srv.URL, fixedNow)
	if r.Score == 100 {
		t.Fatal("broken (never-fills) engine scored 100 - oracle failed to catch it")
	}
	if r.Score != 50 {
		t.Errorf("never-fills engine scored %.0f, want 50 (the 5 expected-zero orders)", r.Score)
	}
}

// echoOracle is a stub engine that replays the scenario through its own copy
// of the reference book and reports the oracle's fills - i.e. a correct engine.
func echoOracle() http.HandlerFunc {
	b := newBook()
	return func(w http.ResponseWriter, r *http.Request) {
		var o struct {
			ID       int64  `json:"id"`
			ClientID int64  `json:"clientId"`
			Side     string `json:"side"`
			Type     string `json:"type"`
			Px       int64  `json:"price"`
			Qty      int64  `json:"qty"`
			TargetID int64  `json:"targetId"`
		}
		json.NewDecoder(r.Body).Decode(&o)
		filled := b.apply(vOrder{ID: o.ID, ClientID: o.ClientID, Side: o.Side, Type: o.Type, Px: o.Px, Qty: o.Qty, TargetID: o.TargetID})
		json.NewEncoder(w).Encode(map[string]any{"acks": []map[string]any{{"status": "ok", "filled": filled}}})
	}
}
