// Package cache wraps Redis for hot-path state: current-run snapshot,
// leaderboard ZSET, and per-submission lock keys.
package cache

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

type Cache struct {
	cli *redis.Client
}

func Connect(ctx context.Context, dsn string) (*Cache, error) {
	opt, err := redis.ParseURL(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse redis dsn: %w", err)
	}
	cli := redis.NewClient(opt)

	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := cli.Ping(ctx).Err(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("redis unreachable")
		}
		log.Println("[cache] redis not ready, retrying...")
		time.Sleep(2 * time.Second)
	}
	log.Println("[cache] redis connected")
	return &Cache{cli: cli}, nil
}

func (c *Cache) Close() error { return c.cli.Close() }

// PublishRunState is used by the gateway to mirror the run's current
// metrics into Redis so WebSocket subscribers can fan out fast without
// touching Postgres.
func (c *Cache) PublishRunState(ctx context.Context, runID string, payload []byte) error {
	pipe := c.cli.Pipeline()
	pipe.Set(ctx, "run:"+runID+":state", payload, 5*time.Minute)
	pipe.Publish(ctx, "run:"+runID+":updates", payload)
	_, err := pipe.Exec(ctx)
	return err
}

// SubscribeRun returns a channel of payloads for live run updates.
// The caller must call the returned cancel func to release the
// subscription when done.
func (c *Cache) SubscribeRun(ctx context.Context, runID string) (<-chan []byte, func(), error) {
	ps := c.cli.Subscribe(ctx, "run:"+runID+":updates")
	ch := make(chan []byte, 256)
	go func() {
		defer close(ch)
		for msg := range ps.Channel() {
			select {
			case ch <- []byte(msg.Payload):
			default:
				// Slow consumer - drop the message rather than blocking
				// the broker. Consumer will catch up via /api/runs/:id.
			}
		}
	}()
	cancel := func() { _ = ps.Close() }
	return ch, cancel, nil
}

// LeaderboardZAdd updates the team's best-ever score in the ZSET.
// Idempotent: ZADD GT only writes if the new score is strictly greater.
func (c *Cache) LeaderboardZAdd(ctx context.Context, teamID string, score float64) error {
	return c.cli.ZAddGT(ctx, "leaderboard:scores", redis.Z{Score: score, Member: teamID}).Err()
}

// LeaderboardTop returns the top N teams from the ZSET, ranked desc.
func (c *Cache) LeaderboardTop(ctx context.Context, n int64) ([]redis.Z, error) {
	return c.cli.ZRevRangeWithScores(ctx, "leaderboard:scores", 0, n-1).Result()
}

// LeaderboardMetrics returns the metrics JSON strings for a list of team IDs.
func (c *Cache) LeaderboardMetrics(ctx context.Context, teamIDs []string) ([]string, error) {
	if len(teamIDs) == 0 {
		return nil, nil
	}
	res, err := c.cli.HMGet(ctx, "leaderboard:metrics", teamIDs...).Result()
	if err != nil {
		return nil, err
	}
	out := make([]string, len(res))
	for i, v := range res {
		if v != nil {
			out[i] = v.(string)
		}
	}
	return out, nil
}
