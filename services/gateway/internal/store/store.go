// Package store is the gateway's Postgres/TimescaleDB layer.
//
// We use pgxpool (not GORM) because the gateway is on the hot path
// for run lifecycle writes and we want explicit SQL + connection
// pooling control. Migrations live in /sql/init.sql and are applied
// by TimescaleDB's docker-entrypoint at first boot.
package store

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	pool *pgxpool.Pool
}

// Connect dials Postgres with a bounded retry loop so we don't crash
// the gateway when docker-compose brings us up before TimescaleDB is
// ready to accept connections.
func Connect(ctx context.Context, dsn string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.MaxConns = 16
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute

	var pool *pgxpool.Pool
	deadline := time.Now().Add(60 * time.Second)
	for {
		pool, err = pgxpool.NewWithConfig(ctx, cfg)
		if err == nil {
			if pingErr := pool.Ping(ctx); pingErr == nil {
				break
			} else {
				pool.Close()
				err = pingErr
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("postgres unreachable: %w", err)
		}
		log.Printf("[store] postgres not ready (%v), retrying...", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	log.Println("[store] postgres connected")
	return &DB{pool: pool}, nil
}

func (d *DB) Close() { d.pool.Close() }

// ── Submissions ───────────────────────────────────────────────────────

type Submission struct {
	ID        string
	TeamID    string
	Name      string
	Lang      string
	Hash      string
	ImageTag  string
	Endpoint  string
	Status    string
	SizeBytes int64
	CreatedAt time.Time
}

func (d *DB) InsertSubmission(ctx context.Context, s *Submission) error {
	_, err := d.pool.Exec(ctx, `
		INSERT INTO submissions (id, team_id, name, lang, hash, image_tag, endpoint, status, size_bytes)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (team_id, hash) DO UPDATE
			SET status = EXCLUDED.status, image_tag = EXCLUDED.image_tag, endpoint = EXCLUDED.endpoint
	`, s.ID, s.TeamID, s.Name, s.Lang, s.Hash, s.ImageTag, s.Endpoint, s.Status, s.SizeBytes)
	return err
}

func (d *DB) GetSubmission(ctx context.Context, id string) (*Submission, error) {
	s := &Submission{}
	err := d.pool.QueryRow(ctx, `
		SELECT id, team_id, name, lang, hash, COALESCE(image_tag,''), COALESCE(endpoint,''), status, COALESCE(size_bytes,0), created_at
		FROM submissions WHERE id = $1
	`, id).Scan(&s.ID, &s.TeamID, &s.Name, &s.Lang, &s.Hash, &s.ImageTag, &s.Endpoint, &s.Status, &s.SizeBytes, &s.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func (d *DB) UpdateSubmissionStatus(ctx context.Context, id, status, imageTag, endpoint string) error {
	_, err := d.pool.Exec(ctx, `
		UPDATE submissions SET status=$2, image_tag=COALESCE(NULLIF($3,''), image_tag), endpoint=COALESCE(NULLIF($4,''), endpoint)
		WHERE id=$1
	`, id, status, imageTag, endpoint)
	return err
}

// ── Runs ──────────────────────────────────────────────────────────────

type Run struct {
	ID           string
	SubmissionID string
	TeamID       string
	Profile      string
	Seed         int64
	Status       string
	StartedAt    time.Time
	FinishedAt   *time.Time
	Score        *float64
}

func (d *DB) InsertRun(ctx context.Context, r *Run) error {
	_, err := d.pool.Exec(ctx, `
		INSERT INTO runs (id, submission_id, team_id, profile, seed, status)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, r.ID, r.SubmissionID, r.TeamID, r.Profile, r.Seed, r.Status)
	return err
}

func (d *DB) FinishRun(ctx context.Context, id, status string, score float64, metricsJSON []byte) error {
	_, err := d.pool.Exec(ctx, `
		UPDATE runs SET status=$2, finished_at=now(), score=$3, metrics=$4 WHERE id=$1
	`, id, status, score, metricsJSON)
	return err
}

// LeaderboardRows returns the current ranking, which is precomputed by
// a Redis ZSET in production. The Postgres fallback below is correct
// but slow at scale (~50ms vs <1ms for the Redis path); kept as a
// safety net for when the cache is cold.
type LeaderboardRow struct {
	TeamID  string  `json:"teamId"`
	Name    string  `json:"name"`
	Region  string  `json:"region"`
	Score   float64 `json:"score"`
	P50     float64 `json:"p50"`
	P99     float64 `json:"p99"`
	TPS     float64 `json:"tps"`
	ErrPct  float64 `json:"err"`
	LastRun int64   `json:"lastRun"`
}

func (d *DB) Leaderboard(ctx context.Context, limit int) ([]LeaderboardRow, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.pool.Query(ctx, `
		WITH best AS (
			SELECT DISTINCT ON (team_id) team_id, score, metrics, finished_at
			FROM runs
			WHERE status = 'finished'
			ORDER BY team_id, score DESC NULLS LAST
		)
		SELECT t.id, t.name, COALESCE(t.region,''),
		       COALESCE(b.score, 0),
		       COALESCE((b.metrics->>'p50')::float, 0),
		       COALESCE((b.metrics->>'p99')::float, 0),
		       COALESCE((b.metrics->>'tps')::float, 0),
		       COALESCE((b.metrics->>'err_pct')::float, 0),
		       COALESCE(extract(epoch from b.finished_at)*1000, 0)
		FROM teams t
		LEFT JOIN best b ON b.team_id = t.id
		ORDER BY COALESCE(b.score, 0) DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []LeaderboardRow{}
	for rows.Next() {
		var r LeaderboardRow
		var lastRun float64
		if err := rows.Scan(&r.TeamID, &r.Name, &r.Region, &r.Score, &r.P50, &r.P99, &r.TPS, &r.ErrPct, &lastRun); err != nil {
			return nil, err
		}
		r.LastRun = int64(lastRun)
		out = append(out, r)
	}
	return out, rows.Err()
}
