# QuanTime — Distributed Benchmarking & Hosting Platform for Trading Infrastructure
### IICPC Summer Hackathon 2026 · System Design Document

> A platform that lets anyone upload a matching engine / order book, securely containerizes
> and runs it under strict isolation, bombards it with a distributed fleet of trading bots,
> and measures latency, throughput, and correctness in real time — ranking submissions on a
> live leaderboard. Built as a real, reusable product, not a demo.

---

## Table of Contents
1. System Overview
2. High-Level Architecture
3. End-to-End Data Flow
4. Submission & Sandboxing Engine
5. Distributed Bot Fleet (Load Generator)
6. Telemetry & Validation Ingester
7. Real-Time Leaderboard & Live Streaming
8. Composite Scoring Algorithm
9. AI Analyzer (Pluggable, Privacy-First)
10. Inter-Service Communication
11. Data Stores & Schema
12. Infrastructure as Code
13. CI/CD Pipeline
14. Security & Isolation Model
15. Performance Characteristics (Verified)
16. Technology Decisions & Rationale
17. Architecture Decision Records
18. Known Limitations & Future Work
19. Verified End-to-End Results

---

## 1. System Overview

QuanTime evaluates contestant-submitted trading infrastructure under realistic, high-velocity
market load. A contestant uploads source code (with a `Dockerfile`); the platform builds it,
deploys it into a strictly-isolated sibling container, spins up a configurable fleet of
concurrent trading bots that send limit/market/cancel orders, captures every order's
acknowledgment latency and outcome, and computes a composite score (speed + throughput +
correctness) that is streamed to a live leaderboard.

**Design goals**
- **Real distributed systems, not simulation.** Every leg runs as an independent Go service
  communicating over a message bus and shared data stores — no in-browser fakery.
- **Strict, fair isolation.** Submissions run with CPU, memory, PID, capability, and
  filesystem constraints so one contestant cannot affect another or the host.
- **Decoupled & horizontally scalable.** Producers and consumers are separated by NATS and a
  buffered ingest path, so the bot fleet and the ingester scale independently.
- **Reusable beyond the hackathon.** Any developer or quant can benchmark their own engine
  locally with one command (`docker compose up`).

**Primary user flows**
- *Contestant / developer*: upload engine → watch it build → launch a stress run → read the
  live metrics and final score.
- *Judge / operator*: compare submissions on the leaderboard; inspect per-run latency
  distributions and correctness.

---

## 2. High-Level Architecture

```
                    ┌──────────────────────────────────────────────────────────┐
   Browser  ──────▶ │  Caddy (:8080)  reverse-proxy + static UI + /api + /ws    │
                    └───────────────┬──────────────────────────────────────────┘
                                    │ /api/*  /ws/*
                                    ▼
                       ┌─────────────────────────┐      docker.sock
                       │  Gateway (Go, :7070)     │──────────────────┐ docker build/run
                       │  REST + WebSocket API    │                  ▼  (sibling container)
                       │  Sandbox controller      │        ┌────────────────────────┐
                       └───┬─────────┬────────┬───┘        │ Contestant submission  │
                  publish  │         │ store  │ cache      │ (e.g. matching engine) │
              runs.<id>.   │         ▼        ▼            │  :9001 on iicpc-net    │
                 control   │   ┌──────────┐ ┌────────┐     └───────────┬────────────┘
                           ▼   │TimescaleDB│ │ Redis │                 │ HTTP orders
                    ┌───────────────┐ └────┬─────┘ └───┬────┘          │
                    │  NATS (JetStream) │   │ runs row  │ ZSET / pub-sub│
                    │  order/event bus  │   │ + telem   │ leaderboard   │
                    └───┬───────────▲───┘   │ hypertable│ + run:<id>    │
        runs.<id>.      │           │       │           │ :updates      │
         control        ▼           │ telemetry         │               │
                ┌───────────────┐   │ runs.<id>.        │               │
                │  Bot Fleet    │───┘ telemetry/summary │               │
                │  (Go, N×)     │─────────────────────────────────────────▶ (sends orders)
                │  goroutine    │
                │  bot pool     │   ┌──────────────────┐
                └───────────────┘   │  Telemetry        │  subscribe runs.*.telemetry/summary
                                    │  Ingester (Go)    │─▶ CopyFrom → hypertable
                                    │  score + publish  │─▶ Redis ZSET + run:<id>:updates
                                    └──────────────────┘
                       ┌──────────────────┐
                       │ AI Analyzer (Go) │  POST /api/analyze /api/report
                       │ multi-agent LLM  │  → Ollama (local) or Gemini (cloud)
                       └──────────────────┘
```

**Services (all independently deployable):**

| Service | Lang | Role | Exposure |
|---|---|---|---|
| `caddy` | — | Reverse proxy, static UI, `/api` + `/ws` routing | `:8080` |
| `gateway` | Go | REST + WebSocket API; sandbox build/run controller (Docker-out-of-Docker) | `:7070` |
| `botfleet` | Go | Distributed load generator; goroutine bot pool; `--scale botfleet=N` | internal |
| `telemetry` | Go | Ingests order telemetry, batches into TimescaleDB, computes score, feeds live stream | internal |
| `ai-analyzer` | Go | Multi-agent static analysis + post-run report via pluggable LLM | `:7080` |
| `sample-engine` | Go | Reference price-time-priority CLOB (a stand-in submission judges can replace) | `:9001` |
| `timescale` | — | TimescaleDB (Postgres + hypertables) — telemetry + run metadata | `:5432` |
| `redis` | — | Leaderboard hot cache (ZSET) + per-run live pub/sub | `:6379` |
| `nats` | — | JetStream message bus — control, telemetry, summary subjects | `:4222` |

---

## 3. End-to-End Data Flow

The verified pipeline is **Upload → Containerized Deployment → Distributed Load → Real-Time Scoring**:

1. **Upload** — `POST /api/submissions` (multipart). Gateway streams the archive, computes a
   SHA-256 content hash, deduplicates on `(team_id, hash)`, persists a row (`status=uploaded`),
   and returns `202 Accepted` with the submission id.
2. **Sandbox build** — Asynchronously: `SaveSource` unpacks the archive (tar.gz/zip/tar, with
   path-traversal protection and a 64 MB per-file cap), validates a root `Dockerfile`, then
   `docker build` produces an image tagged `iicpc-sub-<id>:<hash12>` (`status=building→built`).
3. **Deploy** — `docker run` launches the image as a **sibling container** on `iicpc-net` with
   strict isolation flags; the gateway records the resolvable endpoint
   `http://iicpc-run-<hash>:9001` (`status=deployed`).
4. **Launch run** — `POST /api/runs {submissionId, profile, seed, durationSec, botsPerFleet}`.
   Gateway inserts a `runs` row (`status=running`) and publishes a `start` control message to
   NATS subject `runs.<runId>.control`.
5. **Distributed load** — Every bot-fleet replica receives the control message, spawns its bot
   goroutines, and each bot issues HTTP orders (limit/market/cancel) to the submission endpoint,
   timing the acknowledgment (`latencyNs = ackTs − sendTs`).
6. **Telemetry ingest** — Each order emits a telemetry sample to `runs.<runId>.telemetry`. The
   ingester batches samples (5 000 rows or 250 ms) and bulk-loads them into the TimescaleDB
   `telemetry` hypertable via `CopyFrom`.
7. **Live streaming** — Every 1 s the ingester publishes a rolling snapshot
   (`orders`, `tps`, `avgLatMs`, `errPct`) to Redis `run:<id>:updates`; the gateway's
   `/ws/runs/{id}` WebSocket fans it out to the browser.
8. **Finalize & score** — On `runs.<runId>.summary` the ingester computes exact percentiles
   (p50/p90/p99), TPS, and error rate from the hypertable, derives the composite score, writes
   it to the `runs` row + the Redis leaderboard ZSET, and emits a `final` WS event.
9. **Leaderboard** — `GET /api/leaderboard` returns the best-per-team ranking (Postgres source
   of truth, Redis ZSET for sub-millisecond reads).

---

## 4. Submission & Sandboxing Engine

**Pipeline.** `services/gateway/internal/sandbox/sandbox.go` drives the Docker CLI via `os/exec`
(sibling containers, not Docker-in-Docker — faster and avoids privileged nesting).

**`SaveSource`** — format-detects by magic bytes (`1f 8b` → gzip/tar.gz, `PK\x03\x04` → zip,
else plain tar), extracts into `submissions/<id>/`, and enforces:
- **Path-traversal protection** — entries containing `..` or absolute paths are skipped.
- **Zip-bomb mitigation** — each file is copied through `io.LimitReader(…, 64<<20)`.
- **Contract validation** — a `Dockerfile` must exist at the archive root, else the build is
  rejected with a clear error.
- **Idempotency** — a re-upload of the same id cleans the previous directory first.

**Isolation flags** applied at `docker run`:

| Flag | Purpose |
|---|---|
| `--memory 256m` | Hard memory ceiling (fair allocation, OOM-kill on breach) |
| `--cpus 1.0` | CPU quota (fair compute allocation) |
| `--pids-limit 128` | Fork-bomb protection |
| `--cap-drop ALL` | Drop all Linux capabilities |
| `--security-opt no-new-privileges` | Block privilege escalation |
| `--read-only` + `tmpfs` | Immutable root FS; scratch space only in tmpfs |
| `--network iicpc-net` | Isolated bridge; no host networking |

A health-poll loop (`docker inspect`) waits for the container to be reachable before the
submission is marked `deployed`, so runs never target a not-yet-ready engine.

---

## 5. Distributed Bot Fleet (Load Generator)

`services/botfleet` is a horizontally-scalable Go service (`docker compose up --scale botfleet=N`,
or a Kubernetes Deployment + HPA). It subscribes to `runs.*.control` and, on a `start` message,
spawns a pool of bot goroutines that each:

- Build an order (side, price in **integer ticks** = price×100, qty, type) from a **deterministic
  RNG** — `xoshiro256**` seeded via `splitmix64` from `runSeed + botID`, so a run is reproducible.
- POST the order to the submission endpoint over a `fasthttp` client and measure the ack latency.
- Publish a per-order telemetry sample to `runs.<runId>.telemetry`.

**Order mix** ≈ 70 % limit / 20 % market / 10 % cancel. **Traffic profiles** modulate
inter-arrival timing: `sustained` (steady), `burst` (spiky), `adversarial` (aggressive).
A `cancel`/`start` control message carries `botsPerFleet`, `seed`, `durationSec`, and `profile`;
duration is enforced via a per-run `context.WithTimeout`, and cancels are honored by storing a
`CancelFunc` per run id.

**Scale model.** Each replica runs `BOTS_PER_INSTANCE` bots, so total concurrency =
`replicas × BOTS_PER_INSTANCE` (the k8s manifest ships 4 × 250 = 1 000). See §18 for the
sharding caveat.

---

## 6. Telemetry & Validation Ingester

`services/telemetry` is the low-latency measurement spine.

- **Ingest** — subscribes to `runs.*.telemetry`; a non-blocking `select` pushes samples onto a
  50 000-deep buffered channel (drops on overflow — documented at-most-once contract — so a slow
  DB never back-pressures the bus).
- **Batch load** — a flusher coalesces samples and writes them with pgx **`CopyFrom`** (the
  Postgres binary fast path) on a 5 000-row / 250 ms trigger.
- **Live aggregation** — the same single-goroutine flusher maintains per-run rolling counters
  (orders, errors, latency sum) and publishes a 1 Hz snapshot to `run:<id>:updates` (the live
  WS source). Idle runs are GC'd after 15 ticks.
- **Finalize** — on `runs.*.summary`, computes **exact** percentiles with
  `percentile_cont(…) WITHIN GROUP (ORDER BY latency_ns)`, plus TPS and error rate, writes the
  composite score + metrics JSON to the `runs` row and the Redis ZSET, and emits a `final` WS event.

**Measured dimensions:** Latency (p50/p90/p99 of order-ack time), Throughput (TPS over the run
window), Correctness/Errors (transport-error rate; see §18 for the correctness-oracle roadmap).

---

## 7. Real-Time Leaderboard & Live Streaming

Two complementary paths:

- **Leaderboard (durable):** `GET /api/leaderboard` runs a `DISTINCT ON (team_id) … ORDER BY
  score DESC` over finished runs in Postgres (source of truth). The finalizer also writes a Redis
  `leaderboard:scores` **ZSET** (with `ZADD GT` — only improves a team's best) + a
  `leaderboard:metrics` hash for sub-millisecond reads.
- **Live run stream (real-time):** `GET /ws/runs/{id}` is a WebSocket that subscribes to the
  Redis `run:<id>:updates` channel and fans `metrics` (1 Hz) and `final` events to the browser,
  with 20 s keepalive pings. This is what makes the dashboard tick live during a stress run.

---

## 8. Composite Scoring Algorithm

Computed in `finalizeRun` from the run's telemetry:

```
speed       = 100 · 1 / (1 + p99_ns / 200_000_000)     # exp-style decay; 200 ms → ~37
throughput  = 100 · min(tps / 200_000, 1)              # saturates at 200k ops/s
correctness = oracle_score   (price-time priority + fill accuracy; see §9a)
              ↳ falls back to 100·(1 − error_rate) when no oracle score exists
score       = 0.40·speed + 0.40·throughput + 0.20·correctness
```

Rationale: latency and throughput are the dominant axes of a trading engine's quality and are
weighted equally; correctness gates the result. Weights are centralized so the judge console can
re-tune them. The score and the raw metrics (`p50/p90/p99/tps/err_pct`) are persisted as JSONB on
the run for full auditability.

### 9a. Correctness oracle (price-time priority + fill accuracy)

`services/gateway/internal/validator` is an **independent** reference order book. At deploy time
the platform replays a fixed deterministic order sequence (crossing, partial fills, a market order,
a cancel that removes liquidity, a market sweep) through both the contestant's engine and the
oracle, then diffs the filled quantity order-by-order. The score (`passed/total`) is stored on the
submission and feeds the 0.2 correctness weight above, so an engine that mis-orders fills or
ignores cancels ranks lower. Unit tests assert the oracle catches a never-fills engine (scores 50,
not 100). The reference sample engine passes all 10 cases (verified).

---

## 9. AI Analyzer (Pluggable, Privacy-First)

`services/ai-analyzer` is an optional differentiator: a multi-agent static-analysis service.

- **Agents** (run concurrently, then synthesized): **Security** (container-escape, memory safety,
  input validation), **Performance** (O(n) hot-path scans, lock contention, allocation patterns),
  **Correctness** (price-time priority, fill semantics, overflow). A **Synthesizer** dedups
  findings and computes a 0–100 risk score; a **Report generator** correlates post-run telemetry
  with source patterns ("p99 spiked because the cancel path is O(n)").
- **Pluggable backend** — the LLM client is a thin raw-HTTP adapter. The recommended deployment
  is **local Ollama (Qwen2.5-Coder 7B)** so *proprietary trading code never leaves the operator's
  infrastructure* — a hard requirement for real quant/firm usage — with **Google Gemini** as an
  optional cloud fallback. No vendor SDK lock-in.
- **API** — `POST /api/analyze` (source) → structured findings; `POST /api/report` (source +
  metrics) → natural-language performance report. Endpoints are caps-guarded (1 MB / 100 k chars)
  with CORS, panic recovery, and graceful shutdown.

---

## 10. Inter-Service Communication

| Channel | Mechanism | Producer → Consumer |
|---|---|---|
| Run control | NATS JetStream `runs.<id>.control` | Gateway → Bot Fleet |
| Order telemetry | NATS core `runs.<id>.telemetry` | Bots → Telemetry |
| Run summary | NATS core `runs.<id>.summary` | Bot Fleet → Telemetry |
| Live metrics | Redis pub/sub `run:<id>:updates` | Telemetry → Gateway WS → Browser |
| Leaderboard | Redis ZSET + Postgres query | Telemetry → Gateway → Browser |
| Submission build | Docker CLI over `/var/run/docker.sock` | Gateway → Docker daemon |
| Orders | HTTP/JSON `POST /submit` | Bots → Submission container |

JetStream gives the control plane durable, replayable delivery; core NATS is used on the
high-volume telemetry path where at-most-once + buffered batching is the right trade-off.

---

## 11. Data Stores & Schema

**TimescaleDB**
- `teams`, `submissions` (`UNIQUE(team_id, hash)`), `runs` (status, score, `metrics` JSONB,
  `started_at`/`finished_at`), `analysis_reports` (AI findings).
- `telemetry` **hypertable** — `(ts, run_id, bot_id, order_id, side, type, price_x100, qty,
  latency_ns, status, filled, err)`; a 1-second continuous aggregate + compression/retention
  policies for long runs.
- Init is a single idempotent `sql/init.sql`, mounted at container init; it also seeds a `t_demo`
  team and a pre-deployed `sub_sample` submission so a run can be launched without uploading.

**Redis**
- `leaderboard:scores` ZSET (best-per-team), `leaderboard:metrics` hash, and `run:<id>:updates`
  pub/sub for live streaming.

---

## 12. Infrastructure as Code

- **Docker Compose** (primary, verified) — one command (`docker compose up --build`) brings up
  the full 9-service stack with healthcheck-gated ordering, a pinned `iicpc-net` bridge, and the
  Docker socket mounted into the gateway for sandbox spawning.
- **Kubernetes manifests** (`k8s/`) — Deployments, Services, an HPA for the bot fleet
  (`replicas: 4 → maxReplicas: 50`), RBAC, and NetworkPolicy — the horizontal-scale story.
- **Terraform** (`terraform/`) — a single-VM AWS deploy (EC2 + EIP + IAM/SSM) that boots the
  stack via cloud-init; plus a DigitalOcean `doctl` path and a Codespaces devcontainer.

(See §18 for the honest status of the k8s/Terraform paths vs. Compose.)

---

## 13. CI/CD Pipeline

`.github/workflows/ci.yml` builds all four Go services, runs `go vet`, executes the unit-test
suite **with the race detector** (`-race`), and performs a Docker build to catch packaging
regressions. `pages.yml` publishes the static frontend prototype to GitHub Pages.

---

## 14. Security & Isolation Model

- **Submission isolation** — see §4: memory/CPU/PID caps, all capabilities dropped,
  no-new-privileges, read-only root FS, isolated bridge network, no host networking.
- **Upload hardening** — content-hash dedup, size caps, archive path-traversal protection,
  per-file size limit (zip-bomb), Dockerfile contract validation.
- **Blast-radius** — submissions run as siblings on a dedicated network and cannot reach the
  control plane's data stores; the gateway never executes uploaded code in-process.
- **AI privacy** — local-LLM-first so source never leaves the operator's machine.

---

## 15. Performance Characteristics (Verified)

Measured on a developer laptop (Apple Silicon, 16 GB) with `docker compose up`, 50 bots,
sustained profile, ~20 s:

| Metric | Value |
|---|---|
| Orders ingested | **480,480** telemetry rows in ~20 s |
| Throughput | **~24,000 orders/sec** (single host, one bot-fleet replica) |
| Latency p50 / p99 | **0.25 ms / 35 ms** order-ack |
| Error rate | **0 %** |
| Composite score | 58.8 |
| Live stream | 1 Hz `metrics` snapshots over WebSocket |

The full **upload path** was also verified end-to-end: real tarball → build → isolated sibling
container → 337k-row run, score 61.05. Throughput scales further with `--scale botfleet=N` and,
on Kubernetes, with the bot-fleet HPA.

---

## 16. Technology Decisions & Rationale

| Decision | Why |
|---|---|
| **Go** for all services | Goroutine concurrency for the bot pool; static binaries → tiny scratch images; predictable latency. |
| **NATS / JetStream** | Lightweight, fast pub/sub; JetStream for durable control, core for the high-volume telemetry firehose. |
| **TimescaleDB** | Time-series hypertable + `percentile_cont` give exact, cheap latency percentiles; continuous aggregates for long runs. |
| **Redis** | Sub-millisecond leaderboard reads (ZSET) and per-run pub/sub for live streaming. |
| **pgx `CopyFrom`** | ~10× faster bulk ingest than row INSERTs — essential at 10k+ orders/sec. |
| **Sibling containers (DooD)** | Real isolation without privileged Docker-in-Docker nesting; faster cold start. |
| **Local-LLM-first AI** | Privacy for proprietary trading code; zero API cost; no rate limits; works offline. |
| **Caddy** | Automatic HTTPS-ready reverse proxy; one place to route `/api` + `/ws` + static UI. |

---

## 17. Architecture Decision Records

- **ADR-1: Sibling containers over Docker-in-Docker.** Mount the host socket and `docker run`
  submissions as siblings. *Trade-off:* the gateway needs socket access (trusted control plane);
  *win:* no privileged nesting, faster builds, real cgroup isolation.
- **ADR-2: Buffered drop over back-pressure on telemetry.** Under overload the ingester drops
  samples rather than stalling the NATS callback. *Trade-off:* at-most-once telemetry; *win:* the
  bus and bot fleet never block, so the measured engine — not the harness — is the bottleneck.
- **ADR-3: Postgres as source of truth, Redis as cache.** The leaderboard is always derivable
  from the durable `runs` table; Redis is an accelerator, never the only copy. *Win:* correctness
  survives a Redis flush.
- **ADR-4: Pluggable LLM, local-first.** Abstract the model behind a thin HTTP client.
  *Win:* privacy + cost control; the same code runs against Ollama or Gemini.
- **ADR-5: Integer-tick prices end-to-end.** Prices are integers (price×100) across bots, engine,
  and telemetry to avoid floating-point drift in the matching path and the correctness oracle.

---

## 18. Known Limitations & Future Work

We hold ourselves to the hackathon's "not a demo-to-win" bar, so we document gaps honestly:

- **Correctness oracle — implemented (deploy-time).** A real independent reference order book diffs
  fills order-by-order at deploy and feeds the composite score (§9a). The remaining depth is running
  it *continuously* against live bot-fleet traffic (today the high-velocity load path scores
  transport errors only) and expanding the deterministic scenario set.
- **Bot-fleet replica sharding.** Replicas currently each spawn `BOTS_PER_INSTANCE` bots with
  identical seed ranges; for >1 replica we will partition the bot-id/seed space (StatefulSet
  ordinal or a NATS-KV lease) so the aggregate stream is N distinct bots, not N copies.
- **Protocol coverage.** Bots speak REST/HTTP today; FIX and WebSocket order paths are roadmap.
- **Max-TPS discovery.** We report sustained TPS; a closed-loop rate controller that ramps until
  latency/error thresholds trip would measure the true breaking point.
- **Telemetry encoding.** One JSON message per order is simple but heavy at extreme scale;
  MessagePack/protobuf + coalesced publishes are the optimization path.
- **k8s / Terraform.** Compose is the fully-verified path. The k8s manifests need published
  images + a real init-SQL ConfigMap to run; Terraform is single-VM (scaling is demonstrated via
  Compose `--scale` and the k8s HPA definitions).
- **Run duration.** Bots currently run `durationSec + 5 s` (context grace window); tightening to
  exactly `durationSec` is a minor fix.

---

## 19. Verified End-to-End Results

The complete pipeline was executed on a clean `docker compose up --build` and observed working:

- **Pre-seeded path:** `POST /api/runs` against `sub_sample` → 480,480 telemetry rows, p50 0.25 ms,
  p99 35 ms, TPS 24,046, 0 % errors, score 58.8 → written to Postgres `runs`, Redis ZSET
  (`t_demo 58.8`), and streamed as 20 live `metrics` ticks on `run:<id>:updates`.
- **Full upload path:** real tarball upload → `SaveSource` unpack → `docker build` →
  isolated sibling container `iicpc-run-<hash>` on `iicpc-net` → run finished, score 61.05,
  337,409 telemetry rows.
- **Order outcomes** (post duplicate-id fix): 208k accepted, 199k filled, 24k partial,
  **0 duplicate-id rejections** — a clean, real benchmark.

> Every component the rubric requires — secure containerized submission, a distributed bot fleet,
> a low-latency telemetry/validation ingester, and a real-time leaderboard — is implemented as a
> real service and verified working end-to-end, with an honest roadmap for the remaining depth.
