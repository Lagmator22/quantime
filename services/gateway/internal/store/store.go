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
	ID          string
	TeamID      string
	Name        string
	Lang        string
	Hash        string
	ImageTag    string
	Endpoint    string
	Status      string
	SizeBytes   int64
	Correctness *string `json:",omitempty"` // raw JSON: {score,passed,total,cases,ts}
	CreatedAt   time.Time
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
		SELECT id, team_id, name, lang, hash, COALESCE(image_tag,''), COALESCE(endpoint,''), status, COALESCE(size_bytes,0), correctness::text, created_at
		FROM submissions WHERE id = $1
	`, id).Scan(&s.ID, &s.TeamID, &s.Name, &s.Lang, &s.Hash, &s.ImageTag, &s.Endpoint, &s.Status, &s.SizeBytes, &s.Correctness, &s.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

// UpdateSubmissionCorrectness stores the correctness-oracle result (raw JSON).
func (d *DB) UpdateSubmissionCorrectness(ctx context.Context, id string, correctnessJSON []byte) error {
	_, err := d.pool.Exec(ctx, `UPDATE submissions SET correctness=$2 WHERE id=$1`, id, string(correctnessJSON))
	return err
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
	RawLog       string
}

func (d *DB) GetBestSubmissionCode(ctx context.Context, teamID string) (string, error) {
	var code string
	q := `SELECT source_code FROM submissions WHERE team_id = $1 AND source_code IS NOT NULL ORDER BY created_at DESC LIMIT 1`
	err := d.pool.QueryRow(ctx, q, teamID).Scan(&code)
	return code, err
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

// GetRun fetches a single run by ID. Returns nil if not found.
func (d *DB) GetRun(ctx context.Context, id string) (*Run, error) {
	r := &Run{}
	err := d.pool.QueryRow(ctx, `
		SELECT id, submission_id, team_id, profile, seed, status, started_at,
		       finished_at, score, COALESCE(raw_log, '')
		FROM runs WHERE id = $1
	`, id).Scan(
		&r.ID, &r.SubmissionID, &r.TeamID, &r.Profile, &r.Seed,
		&r.Status, &r.StartedAt, &r.FinishedAt, &r.Score, &r.RawLog,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// ── Regression baselines ──────────────────────────────────────────────

// RunResult is a finished run's scoreable output, for regression diffs.
type RunResult struct {
	RunID      string
	TeamID     string
	Score      *float64
	Metrics    []byte // raw JSON: {p50,p90,p99,p99_9,p99_99,max,tps,err_pct}
	IsBaseline bool
}

// SetBaseline makes runID the team's single regression baseline.
func (d *DB) SetBaseline(ctx context.Context, runID, teamID string) error {
	_, err := d.pool.Exec(ctx,
		`UPDATE runs SET is_baseline = (id = $1) WHERE team_id = $2`, runID, teamID)
	return err
}

// GetRunResult returns a run's metrics/score for diffing. nil if not found.
func (d *DB) GetRunResult(ctx context.Context, runID string) (*RunResult, error) {
	rr := &RunResult{}
	var metrics *string
	err := d.pool.QueryRow(ctx,
		`SELECT id, team_id, score, metrics::text, is_baseline FROM runs WHERE id = $1`, runID,
	).Scan(&rr.RunID, &rr.TeamID, &rr.Score, &metrics, &rr.IsBaseline)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if metrics != nil {
		rr.Metrics = []byte(*metrics)
	}
	return rr, err
}

// GetTeamBaseline returns the team's current finished baseline run, or nil.
func (d *DB) GetTeamBaseline(ctx context.Context, teamID string) (*RunResult, error) {
	rr := &RunResult{}
	var metrics *string
	err := d.pool.QueryRow(ctx, `
		SELECT id, team_id, score, metrics::text, is_baseline
		FROM runs WHERE team_id = $1 AND is_baseline = true AND status = 'finished'
		ORDER BY finished_at DESC LIMIT 1
	`, teamID).Scan(&rr.RunID, &rr.TeamID, &rr.Score, &metrics, &rr.IsBaseline)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if metrics != nil {
		rr.Metrics = []byte(*metrics)
	}
	return rr, err
}

// UpdateRunLog appends container logs to a finished or failed run.
func (d *DB) UpdateRunLog(ctx context.Context, id, logText string) error {
	_, err := d.pool.Exec(ctx, `UPDATE runs SET raw_log=$2 WHERE id=$1`, id, logText)
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

// ── AI Analysis ───────────────────────────────────────────────────────

type AnalysisReport struct {
	ID              string
	SubmissionID    string
	TeamID          string
	RiskScore       int
	Summary         string
	Findings        []byte
	Strengths       []byte
	Recommendations []byte
	CreatedAt       time.Time
}

func (d *DB) InsertAnalysisReport(ctx context.Context, r *AnalysisReport) error {
	_, err := d.pool.Exec(ctx, `
		INSERT INTO analysis_reports (id, submission_id, team_id, risk_score, summary, findings, strengths, recommendations)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (id) DO NOTHING
	`, r.ID, r.SubmissionID, r.TeamID, r.RiskScore, r.Summary, r.Findings, r.Strengths, r.Recommendations)
	return err
}

type Team struct {
	ID      string
	Name    string
	Region  string
	Members string
}

func (d *DB) GetTeamsMap(ctx context.Context, ids []string) (map[string]Team, error) {
	if len(ids) == 0 {
		return map[string]Team{}, nil
	}
	rows, err := d.pool.Query(ctx, `SELECT id, name, COALESCE(region, ''), members::text FROM teams WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]Team, len(ids))
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.Name, &t.Region, &t.Members); err == nil {
			out[t.ID] = t
		}
	}
	return out, rows.Err()
}

// ── Admin / Judge Console Methods ─────────────────────────────────────

type AdminTeamRow struct {
	TeamID    string  `json:"teamId"`
	Name      string  `json:"name"`
	Region    string  `json:"region"`
	BestScore float64 `json:"bestScore"`
	Runs      int     `json:"runs"`
	Status    string  `json:"status"`
}

func (d *DB) GetAllTeams(ctx context.Context) ([]AdminTeamRow, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT t.id, t.name, COALESCE(t.region,''), 
		       COALESCE(MAX(r.score), 0) as best_score,
		       COUNT(r.id) as run_count
		FROM teams t
		LEFT JOIN runs r ON r.team_id = t.id AND r.status = 'finished'
		GROUP BY t.id, t.name, t.region
		ORDER BY best_score DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AdminTeamRow
	for rows.Next() {
		var r AdminTeamRow
		if err := rows.Scan(&r.TeamID, &r.Name, &r.Region, &r.BestScore, &r.Runs); err != nil {
			return nil, err
		}
		r.Status = "active"
		out = append(out, r)
	}
	return out, nil
}

type AdminRunRow struct {
	RunID        string  `json:"runId"`
	TeamID       string  `json:"teamId"`
	TeamName     string  `json:"teamName"`
	SubmissionID string  `json:"submissionId"`
	P50          float64 `json:"p50"`
	P99          float64 `json:"p99"`
	TPS          float64 `json:"tps"`
	ErrPct       float64 `json:"err"`
	Score        float64 `json:"score"`
	Status       string  `json:"status"`
}

func (d *DB) GetAllRuns(ctx context.Context, limit int) ([]AdminRunRow, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT r.id, r.team_id, t.name, r.submission_id,
		       COALESCE((r.metrics->>'p50')::float, 0),
		       COALESCE((r.metrics->>'p99')::float, 0),
		       COALESCE((r.metrics->>'tps')::float, 0),
		       COALESCE((r.metrics->>'err_pct')::float, 0),
		       COALESCE(r.score, 0), r.status
		FROM runs r
		JOIN teams t ON t.id = r.team_id
		ORDER BY r.started_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AdminRunRow
	for rows.Next() {
		var r AdminRunRow
		if err := rows.Scan(&r.RunID, &r.TeamID, &r.TeamName, &r.SubmissionID,
			&r.P50, &r.P99, &r.TPS, &r.ErrPct, &r.Score, &r.Status); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (d *DB) ResetPlatform(ctx context.Context) error {
	_, err := d.pool.Exec(ctx, `
		TRUNCATE runs, submissions, analysis_reports CASCADE;
	`)
	return err
}
