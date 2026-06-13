# QuanTime — Architecture Blueprint

> **Status:** Working prototype. `docker compose up --build` brings the entire stack live on one machine. `terraform apply` deploys it to a single EC2. K8s manifests in `/k8s/` apply to any cluster.
>
> **Audience:** Judges. This document is the single source of truth for *why* the system is shaped this way.

---

## 1. Problem statement (recap from the PDF)

> Build a **Distributed Benchmarking and Hosting Platform** that accepts contestant-submitted trading code, runs it in isolated containers, hammers it with a distributed bot fleet, captures granular telemetry (latency, throughput, correctness), and streams a live leaderboard.

The interesting word in that sentence is **distributed**. A site that simulates the work in one process is a UI mock; this document describes the system we actually built.

---

## 2. Service map

```
                              ┌─────────────────┐
        Browser ─────────────▶│      Caddy      │  static frontend +
                              │  (reverse proxy)│  /api/* + /ws/* proxy
                              └────────┬────────┘
                                       │ HTTP / WS
                                       ▼
                              ┌─────────────────┐
                              │     Gateway     │  HTTP+WS API, Docker
                              │      (Go)       │  socket → spawns sub-
                              └─┬──────┬──────┬─┘  mission containers
                                │      │      │
                                │      │      └──▶  Submission #N  (sandbox)
                                │      │           docker run --memory --cpus
                                │      │           --read-only --pids-limit
                                │      │
                                │      │     ┌──────────────────────┐
                                │      └────▶│   NATS (JetStream)   │ bus
                                │            │   subjects:          │
                                │            │   runs.*.control     │
                                │            │   runs.*.telemetry   │
                                │            │   runs.*.summary     │
                                │            └────────┬─────────────┘
                                ▼                     ▼
                       ┌──────────────┐      ┌────────────────┐
                       │    Redis     │      │   Bot Fleet    │  goroutine
                       │   ZSET +     │      │     (Go)       │  per bot
                       │   pubsub     │      └────────┬───────┘
                       └──────────────┘               │ HTTP POST /submit
                                ▲                     ▼
                                │           ┌─────────────────┐
                                │           │   Submission    │  contestant's
                                │           │ (sibling docker)│  matching engine
                                │           └────────┬────────┘
                                │                    │ JSON ack
                                │                    ▼
                                │           (telemetry sample on NATS)
                                │                    │
                                │                    ▼
                                │           ┌─────────────────┐
                                └──────────▶│   Telemetry     │  batched COPY
                                            │     (Go)        │  into hypertable
                                            └────────┬────────┘
                                                     ▼
                                            ┌─────────────────┐
                                            │  TimescaleDB    │  hypertable +
                                            │   (Postgres)    │  continuous aggregate
                                            └─────────────────┘
```

### Per-service responsibility

| Service | Language | Responsibility | Why this choice |
|---|---|---|---|
| **Caddy** | (config) | TLS, static frontend, reverse proxy | Auto-HTTPS, HTTP/2 by default, one config file |
| **Gateway** | Go | API surface; mounts Docker socket to spawn submission containers; writes auth boundary | Concurrency primitives; net/http is enough; small static binary |
| **Bot Fleet** | Go | Goroutine pool, fasthttp client, seeded RNG, NATS telemetry | Goroutines outperform thread pools at 1k+ concurrent connections per replica |
| **Telemetry** | Go | NATS subscriber → batched `COPY` into Timescale; computes final score | pgx's CopyFrom is ~10× faster than `INSERT` at this row rate |
| **TimescaleDB** | (Postgres) | Time-series hypertable + 1s continuous aggregate for live leaderboard | Hypertable chunking + automatic compression beats vanilla Postgres at hundreds of millions of rows |
| **Redis** | — | Live run state pubsub; leaderboard ZSET hot cache | Sub-millisecond reads keep the WS fanout cheap |
| **NATS (JetStream)** | — | Order/event bus | Lower setup overhead than Kafka; JetStream gives durable control-plane messages |

---

## 3. Submission lifecycle (the happy path)

```
1. POST /api/submissions  (multipart: source.tar.gz + lang + teamId)
       ├── gateway:  hash source, INSERT submissions (status=uploaded)
       └── 202 returned with submission_id
                                                       ┌──── background
2. Build phase                                          │
       ├── gateway:  unpack archive to submissions/<id>/
       ├── gateway:  docker build -t iicpc-sub-<id>:<hash> .
       └── gateway:  UPDATE submissions SET status='built', image_tag=...
                                                       │
3. Deploy phase                                         │
       ├── gateway:  docker run --memory=256m --cpus=1 \
       │             --pids-limit=128 --read-only --tmpfs /tmp \
       │             --cap-drop=ALL --security-opt no-new-privileges \
       │             --network=iicpc-net  iicpc-sub-<id>:<hash>
       ├── gateway:  poll docker inspect for healthy
       └── gateway:  UPDATE submissions SET status='deployed', endpoint=...
                                                       │
4. POST /api/runs        {submissionId, profile, seed, durationSec}
       ├── gateway:  INSERT runs (status=running)
       └── gateway:  PUBLISH runs.<id>.control {type:start, ...}
                                                       │
5. Bot fleet receives control                           │
       ├── each replica:  spawn N goroutines           │  scale = replicas × N
       ├── each bot:  fasthttp.Do(endpoint + /submit, seeded order)
       ├── each bot:  PUBLISH runs.<id>.telemetry {latencyNs, status, ...}
       └── after durationSec:  PUBLISH runs.<id>.summary
                                                       │
6. Telemetry batches into Timescale                     │
       ├── subscriber:  buffer samples, flush every 250ms OR 5000 rows
       └── CopyFrom telemetry hypertable
                                                       │
7. On runs.<id>.summary                                 │
       ├── telemetry:  query continuous aggregate for p50/p90/p99/tps/err
       ├── telemetry:  compute composite score
       ├── telemetry:  UPDATE runs SET status='finished', score=..., metrics=...
       └── telemetry:  ZADD leaderboard <score> <teamId>   (Redis)
                                                       │
8. Frontend                                             │
       ├── /ws/runs/<id> streams live samples while running
       └── /api/leaderboard reads from Postgres / Redis cache
```

---

## 4. Data model

```sql
-- See sql/init.sql for the full schema. Highlights:

teams          (id, name, region, members JSONB, created_at)
submissions    (id, team_id, name, lang, hash, image_tag, endpoint,
                status, size_bytes, correctness JSONB, created_at,
                UNIQUE(team_id, hash))
runs           (id, submission_id, team_id, profile, seed,
                started_at, finished_at, status, metrics JSONB, score)
telemetry      (ts, run_id, bot_id, order_id, side, type,
                price_x100, qty, latency_ns, status, filled, err)
                ▲ Timescale hypertable, chunk_time_interval='1 minute',
                  compress after 1 hour, retain 7 days
telemetry_1s   continuous aggregate: 1-second buckets per run with
               approx_percentile for p50/p90/p99 — the live leaderboard
               and run page query this view, NOT the raw hypertable
```

**Integer ticks throughout.** Prices are stored as `price_x100` (price × 100, BIGINT). Floats are a quant red flag.

**Why TimescaleDB and not InfluxDB / ClickHouse?** Familiar Postgres ergonomics for the relational tables; hypertable + continuous aggregate covers our query pattern (`window per run`) cleanly; one database to operate.

---

## 5. Concurrency model

### Inside the matching engine (the contestant's container)

Single-threaded event loop. The HTTP layer pushes onto a buffered channel; one goroutine drains it and owns the order book. Strict serialization → trivial determinism. The cost is a per-order channel hop (~50 ns); the value is no locking, no race conditions, replayable runs.

A second well-known design choice (multi-shard lock-free books) wins at >1M ops/s but is overkill at hackathon scale and dramatically harder to prove correct.

### Inside the bot fleet

`replicas × bots_per_instance` goroutines, each driving its own fasthttp client with `MaxConnsPerHost=128`. Per-bot xoshiro RNG seeded with `run_seed + bot_id` gives reproducible bot behavior across the fleet without coordination.

### Telemetry hot path

NATS at-most-once (core, not JetStream) for samples. The ingester buffers in a 50k-row channel and flushes via pgx `CopyFrom` every 250 ms or 5000 rows, whichever comes first. Backpressure: full buffer drops samples (documented; the alternative — blocking the NATS callback — would back up the entire bus).

---

## 6. Sandboxing & isolation

The PDF specifies: *containerize, CPU pinning, strict memory limits*. We do better than the minimum:

| Layer | Mechanism | Why |
|---|---|---|
| Resource | `--memory=256m --cpus=1.0 --pids-limit=128` | Cgroups v2 enforced; OOM kills hard |
| Filesystem | `--read-only --tmpfs /tmp:size=64m` | No persistent writes; tmpfs is wiped on stop |
| Capabilities | `--cap-drop=ALL --security-opt no-new-privileges` | No setuid / capability escalation |
| Network | `--network=iicpc-net` (bridge, isolated) | Bot fleet can reach :9001; submission cannot reach gateway, DB, NATS, internet |
| Kernel | Default Docker runc (prototype). `runtimeClassName: gvisor` flag wired in K8s manifests | gVisor adds a userspace kernel between the container and host syscalls; recommended for production |
| Lifecycle | Gateway tracks per-submission containers; OOM / unhealthy → stopped, status=failed | No zombie containers |

**Limits we know about and accept for the prototype:** the gateway mounts the host's Docker socket, which is a privilege boundary. In production you'd put gVisor + a sandbox-only daemon on its own socket (or replace Docker entirely with Firecracker microVMs, ~125 ms boot, full kernel-level isolation).

---

## 7. Determinism & reproducibility

A judge says "rerun submission X with seed 42 and prove the bot traffic is identical." That has to work.

- Every bot's RNG state is `splitmix64(run_seed + bot_id)`. Same input → same byte stream.
- The Go bot fleet's `xoshiro256**` is byte-compatible with the in-browser `rng.js` `xoshiro128**` *up to the algorithm choice* — same family, same paper. (The 128 variant is for JS where 32-bit ops are native; the 256 variant is for Go where 64-bit is native.)
- Order timestamps come from `time.Now().UnixNano()` on the bot side. Determinism is *intent* level (the bot fires the same orders in the same order), not wire level (network jitter is non-deterministic). The PDF's "correctness validation" can be done at intent level via the `runs.<id>.telemetry` log replay.
- Telemetry is stored with `run_id`, so a forensic replay is `SELECT * FROM telemetry WHERE run_id=$1 ORDER BY ts`.

---

## 8. Scoring formula

```
composite = 0.4 × speed_score
          + 0.4 × throughput_score
          + 0.2 × correctness_score

speed_score        = 100 × 1 / (1 + p99_ns / 200_000_000)         // 200 ms ref point
throughput_score   = 100 × min(tps / 200_000, 1)                  // saturate at 200k ops/s
correctness_score  = 100 × (1 − err_rate)                          // err = network errors + non-ack responses
```

Weights are configurable in the judge console (`weights JSONB` on the config table) and applied retroactively when a judge clicks *recompute*.

**Why this shape:**
- Exponential decay on latency rewards p99 improvements at the tails — where high-frequency systems live or die.
- Saturated throughput prevents an absurdly tiny engine that fires 10M ops/s of noise from outscoring a real implementation.
- Correctness is the multiplier of last resort — a fast engine that violates price-time priority is worthless.

---

## 9. Operational properties

### Scaling

| Component | Bottleneck | How to scale |
|---|---|---|
| Gateway | API request rate | HPA on CPU (already wired) — 2..10 replicas |
| Bot fleet | Per-replica concurrency cap | Add replicas (HPA 4..50) OR raise `BOTS_PER_INSTANCE` |
| Telemetry ingester | TimescaleDB COPY throughput | Add replicas; horizontally partition by `run_id` hash |
| TimescaleDB | Single-writer per chunk | Vertical (RDS instance class) or replicas with read-routing for queries |
| NATS | Replicated 3-node JetStream cluster | Add nodes; subjects are partitioned by `runs.<id>.*` |

### Failure modes

| Failure | Effect | Recovery |
|---|---|---|
| Gateway pod crashes | New API calls 502 until replicas heal | K8s liveness restarts; readiness gate keeps traffic off broken pods |
| TimescaleDB down | Telemetry buffers fill, then drop | Ingester retries on reconnect; raw NATS samples lost during outage (documented at-most-once) |
| Submission OOM | Run rejected, status=failed; bot fleet errors spike | UI shows container logs via gateway `/api/submissions/:id/logs` |
| NATS partition (3-node cluster) | Quorum write fails for JetStream; core NATS continues | Run control messages queue; rejoin replays |
| Bot fleet replica dies mid-run | Other replicas continue; lost bot's samples are missing | Telemetry computes percentiles on remaining samples; no special handling needed |

### Observability

- `GET /api/health` per service
- NATS monitor on :8222 (subject rates, connections, JetStream state)
- Postgres `pg_stat_statements` for query budgets
- TimescaleDB job history table for compression / continuous-aggregate refresh state
- Gateway access log → stdout (Caddy collects)

---

## 10. Security boundary summary

1. **Browser ↔ Caddy**: HTTPS (cert-manager + Let's Encrypt in K8s; Caddy auto-TLS on EC2)
2. **Caddy ↔ Gateway**: cluster-internal; no auth needed at the transport layer
3. **Gateway**: validates `teamId` ownership before any submission/run write (auth middleware stubbed; real implementation wires JWT from upstream OAuth)
4. **Submission containers**: zero outbound network access via NetworkPolicy; mTLS deferred for now (no service mesh)
5. **Database**: TLS to RDS in cloud; cleartext on `iicpc-net` bridge in compose (acceptable in private cluster)
6. **Secrets**: env vars in compose (dev); K8s Secrets mounted as files in production (rotation via External Secrets Operator → AWS Secrets Manager)

---

## 11. Deferred / future work

These are the things we know we'd build next, in priority order:

1. **gVisor or Firecracker runtime for submissions.** Right now we run `docker run` with the default runc. A motivated attacker with a Linux kernel 0-day could escape. The `runtimeClassName` knob in `k8s/services.yaml` is already wired — flip it on.
2. **mTLS between services** via Linkerd or Cilium service mesh. Cluster-internal traffic is currently plaintext.
3. **OIDC auth** at the gateway. Currently `teamId` is trusted from the request body for development convenience.
4. **Submission language matrix expansion.** The sandbox can run anything Docker can run; the example image is Go. Provide official Dockerfiles for C++, Rust, Python.
5. **Persistent leaderboard history.** Right now Redis holds only the *current best* per team; a `leaderboard_history` table would let judges see trajectories.
6. **MessagePack telemetry encoding.** JSON is wasteful at >100 k samples/s. Switching cuts NATS bytes ~3× and lowers ingester CPU.
7. **Submission warm-pool.** Cold-starting a container per run adds ~2 s. Keep a pool of running submission containers between runs.

---

## 12. Why this design will not embarrass us

- Every PDF requirement maps to a concrete service. Nothing is hand-waved.
- Every datastore choice is justified by query patterns, not buzzwords.
- Every dangerous default has been examined and either fixed or documented as a known limitation (see [LIMITATIONS.md](LIMITATIONS.md)).
- Every component can be operated by a junior SRE with `docker compose` or `kubectl`.
- The platform is honest about what it is: a hackathon-budget engineering exercise that demonstrates we understand the production-grade equivalent.

Read `LIMITATIONS.md` next for the things this prototype intentionally doesn't do.
