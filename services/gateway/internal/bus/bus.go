// Package bus is a thin wrapper around NATS that gives us:
//   • exactly-once-style publish (JetStream) for run-lifecycle events
//   • core NATS for high-frequency telemetry messages (at-most-once,
//     because losing one of a million telemetry samples is fine and
//     JetStream's per-message cost would dominate at that rate)
//
// Subject layout:
//   runs.<runID>.control     — start/stop/cancel commands (JetStream)
//   runs.<runID>.telemetry   — per-order samples (core NATS)
//   runs.<runID>.summary     — final per-run aggregate (JetStream)
package bus

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/nats-io/nats.go"
)

type Bus struct {
	NC *nats.Conn
	JS nats.JetStreamContext
}

func Connect(ctx context.Context, url string) (*Bus, error) {
	opts := []nats.Option{
		nats.Name("iicpc-gateway"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
		nats.PingInterval(20 * time.Second),
	}
	var nc *nats.Conn
	var err error
	deadline := time.Now().Add(30 * time.Second)
	for {
		nc, err = nats.Connect(url, opts...)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("nats unreachable: %w", err)
		}
		log.Println("[bus] nats not ready, retrying...")
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	js, err := nc.JetStream()
	if err != nil {
		return nil, fmt.Errorf("jetstream: %w", err)
	}

	// Ensure the streams we depend on exist. Safe to call repeatedly.
	_, _ = js.AddStream(&nats.StreamConfig{
		Name:      "RUNCTL",
		Subjects:  []string{"runs.*.control", "runs.*.summary"},
		Retention: nats.LimitsPolicy,
		MaxAge:    24 * time.Hour,
		Storage:   nats.FileStorage,
	})
	log.Println("[bus] connected")
	return &Bus{NC: nc, JS: js}, nil
}

func (b *Bus) Drain() { _ = b.NC.Drain() }

// PublishRunControl is durable; surviving telemetry+gateway crashes is
// important because losing a "stop" command would orphan a run.
func (b *Bus) PublishRunControl(runID string, payload []byte) error {
	_, err := b.JS.Publish(fmt.Sprintf("runs.%s.control", runID), payload)
	return err
}

// PublishTelemetry is the hot path: at-most-once is acceptable because
// we have hundreds of thousands of samples per run.
func (b *Bus) PublishTelemetry(runID string, payload []byte) error {
	return b.NC.Publish(fmt.Sprintf("runs.%s.telemetry", runID), payload)
}
