-- =====================================================================
-- IICPC PLATFORM · TIMESCALEDB SCHEMA
-- ---------------------------------------------------------------------
-- Telemetry lives in TimescaleDB hypertables — chunked by time for fast
-- recent-window queries and automatic compression of older data.
--
-- Relational tables (teams, submissions, runs) live in plain Postgres
-- alongside; we only hypertable the time-series.
-- =====================================================================

CREATE EXTENSION IF NOT EXISTS timescaledb;

-- ── Teams + submissions ──────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS teams (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    region      TEXT,
    members     JSONB NOT NULL DEFAULT '[]',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS submissions (
    id          TEXT PRIMARY KEY,
    team_id     TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    lang        TEXT NOT NULL,
    hash        TEXT NOT NULL,
    image_tag   TEXT,                 -- docker image after build
    endpoint    TEXT,                 -- container hostname:port
    status      TEXT NOT NULL,        -- uploaded|built|deployed|failed|archived
    size_bytes  BIGINT,
    correctness JSONB,                -- {score, passed, total, ts}
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (team_id, hash)
);
CREATE INDEX IF NOT EXISTS submissions_team_idx ON submissions(team_id);

-- ── Runs (one row per stress-test execution) ──────────────────────────
CREATE TABLE IF NOT EXISTS runs (
    id            TEXT PRIMARY KEY,
    submission_id TEXT NOT NULL REFERENCES submissions(id) ON DELETE CASCADE,
    team_id       TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    profile       TEXT NOT NULL,    -- sustained|burst|adversarial
    seed          BIGINT NOT NULL,
    started_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at   TIMESTAMPTZ,
    status        TEXT NOT NULL,    -- queued|running|finished|failed|cancelled
    metrics       JSONB,            -- final {p50, p90, p99, tps, err_pct, ...}
    score         NUMERIC,
    raw_log       TEXT
);
CREATE INDEX IF NOT EXISTS runs_team_idx ON runs(team_id);
CREATE INDEX IF NOT EXISTS runs_submission_idx ON runs(submission_id);
CREATE INDEX IF NOT EXISTS runs_status_idx ON runs(status);

-- ── Per-order telemetry (hypertable) ──────────────────────────────────
-- This is the hot path: bots write one row per order. At 1M ops/s with
-- 1-minute chunks, each chunk holds ~60M rows. Compression after 1h.
CREATE TABLE IF NOT EXISTS telemetry (
    ts            TIMESTAMPTZ NOT NULL,
    run_id        TEXT NOT NULL,
    bot_id        INT NOT NULL,
    order_id      BIGINT NOT NULL,
    side          SMALLINT,         -- 0=buy 1=sell
    type          SMALLINT,         -- 0=limit 1=market 2=ioc 3=fok 4=postonly 5=cancel
    price_x100    BIGINT,           -- price * 100 → integer ticks, no float drift
    qty           BIGINT,
    latency_ns    BIGINT NOT NULL,  -- send → first ack roundtrip
    status        TEXT,             -- ack status code
    filled        BIGINT NOT NULL DEFAULT 0,
    err           TEXT              -- nullable error string
);
SELECT create_hypertable(
    'telemetry', 'ts',
    chunk_time_interval => INTERVAL '1 minute',
    if_not_exists => TRUE
);
CREATE INDEX IF NOT EXISTS telemetry_run_idx ON telemetry(run_id, ts DESC);

-- Compress chunks older than 1 hour. Reduces hot storage 5-10x.
ALTER TABLE telemetry SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'run_id',
    timescaledb.compress_orderby = 'ts DESC'
);
SELECT add_compression_policy('telemetry', INTERVAL '1 hour', if_not_exists => TRUE);

-- ── Continuous aggregate: 1-second rollups for live leaderboard ───────
CREATE MATERIALIZED VIEW IF NOT EXISTS telemetry_1s
WITH (timescaledb.continuous) AS
SELECT
    run_id,
    time_bucket('1 second', ts) AS bucket,
    count(*)                    AS orders,
    avg(latency_ns)             AS mean_lat,
    approx_percentile(0.50, percentile_agg(latency_ns)) AS p50,
    approx_percentile(0.90, percentile_agg(latency_ns)) AS p90,
    approx_percentile(0.99, percentile_agg(latency_ns)) AS p99,
    sum(CASE WHEN err IS NOT NULL THEN 1 ELSE 0 END)::float / count(*) AS err_rate
FROM telemetry
GROUP BY run_id, bucket
WITH NO DATA;

SELECT add_continuous_aggregate_policy('telemetry_1s',
    start_offset => INTERVAL '5 minutes',
    end_offset   => INTERVAL '5 seconds',
    schedule_interval => INTERVAL '5 seconds',
    if_not_exists => TRUE);

-- ── Retention: drop raw telemetry > 7 days ────────────────────────────
SELECT add_retention_policy('telemetry', INTERVAL '7 days', if_not_exists => TRUE);

-- ── Bootstrap: seed an unnamed team so the demo is usable instantly ───
INSERT INTO teams (id, name, region, members)
VALUES ('t_demo', 'demo-team', 'local', '[{"name":"You","role":"captain"}]')
ON CONFLICT (id) DO NOTHING;
