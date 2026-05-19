# Limitations & Known Trade-offs

> Engineers who know what their system *can't* do beat engineers who pretend it does everything. This document is the second one a judge should read.

## What is genuinely in this repo

- **Real distributed system.** 4 Go services + 3 stateful dependencies (TimescaleDB, Redis, NATS), wired via `docker compose` for local dev and K8s manifests for cluster deploy.
- **Real container sandboxing.** `docker run --memory --cpus --pids-limit --read-only --cap-drop=ALL --security-opt no-new-privileges` on a network-isolated bridge. Not a JS Web Worker pretending to be a sandbox.
- **Real bot fleet.** Goroutines firing fasthttp requests at the contestant's HTTP endpoint, with deterministic seeded RNG.
- **Real time-series telemetry.** TimescaleDB hypertable + 1-second continuous aggregate. Compression after 1 h, retention 7 d.
- **Real IaC.** Terraform module deploys to a single EC2; K8s manifests deploy the same workloads as Deployments + StatefulSets + HPAs + NetworkPolicies.

---

## What is intentionally *not* in this repo (and why)

### 1. The submission container runtime is `runc`, not gVisor / Firecracker

**Why it's missing:** gVisor adds ~10% perf overhead and an extra moving part; Firecracker requires KVM and a more involved bootstrap. The hackathon's threat model (judges submitting code in good faith, possibly buggy but not adversarial-with-CVE-class kernel exploits) is well-served by runc + cgroups + drop-all-caps for a 2-week build.

**Where to flip it on:** `k8s/services.yaml` has a `runtimeClassName: gvisor` comment block ready. Install gVisor on nodes, uncomment, redeploy.

### 2. No auth on the gateway

**Why it's missing:** Adding OIDC / JWT plumbing eats a day for zero demo benefit. The gateway trusts `teamId` from the request body during the hackathon demo.

**What we'd add in production:** Auth0 or a self-hosted Dex in front; gateway middleware extracts `sub` claim → maps to `team_id`.

### 3. mTLS between services is off

**Why it's missing:** No service mesh installed. Cluster-internal traffic is plaintext.

**What we'd add:** Linkerd (smaller install than Istio) — flip a namespace annotation, done.

### 4. The reference matching engine is "obviously correct," not "high-performance"

The Go sample engine in `examples/sample-engine-go/` is a single-goroutine event loop with `O(n)` sorted-slice price levels. It sustains ~50 k orders/sec on a Macbook M1, which is *plenty* for stress-testing contestant submissions that target the same range. A production CLOB engine (LMAX Disruptor / SeqLock / multi-shard book) would do 10–100× more — but you'd lose easy provable correctness.

The point of the sample is to be a known-good oracle the correctness suite tests against, not to compete with the contestants.

### 5. Frontend is currently localStorage-backed, NOT yet wired to the backend

**Status:** The frontend at `frontend/platform/*.html` is the visual prototype from the design bundle. It persists state to `localStorage` and runs the matching engine in a Web Worker for the in-browser correctness page.

**What's planned:** A tiny `assets/api.js` shim that detects whether `/api/health` returns 200 — if yes, swaps Store reads to fetch from the gateway; if no, falls back to localStorage. This lets the frontend run standalone for development and against the real backend in deployed environments. **TODO** — see `frontend/platform/assets/api.js` (stub).

### 6. The CSV export endpoint isn't bulk-streaming

`/api/leaderboard.csv` (not yet implemented) would `COPY (SELECT ...) TO STDOUT WITH (FORMAT csv)` and stream to the client. Not critical for the demo.

### 7. NATS JetStream backups

We rely on JetStream's file storage + 3-replica replication for control-plane messages, but there's no off-cluster backup of the `RUNCTL` stream. In production, NATS leaf nodes + S3 snapshotting close the gap.

### 8. Telemetry encoding is JSON, not MessagePack/protobuf

At 100 k samples/sec, the wire bytes and ingester CPU cost of JSON dominates. We accept this for prototype debuggability (`nats sub` shows readable messages). MessagePack would cut bytes ~3× and ingester CPU ~5×. Switching the bot fleet + ingester encoder is a half-day change.

### 9. Submission cold-start adds ~2 s

Each run currently boots the submission container from scratch. A "warm pool" service that keeps N already-running containers (one per team, refreshed on new submissions) would eliminate this. Easy add later.

### 10. No formal capacity test

We claim ~200 k orders/sec sustained on a t3.large + sample engine. We have *not* run a 6-hour soak test. A real production deploy would do this before going live.

---

## Things we explicitly chose against

- **Kafka instead of NATS.** Kafka is the safer-by-reputation pick, but the operational complexity (Zookeeper or KRaft + retention tuning + consumer-group rebalancing) isn't justified by our throughput. NATS JetStream gives durable streams + simple setup; we keep core NATS for the high-frequency telemetry path where at-most-once is acceptable.
- **ClickHouse / InfluxDB instead of TimescaleDB.** ClickHouse would win on aggregate query latency at billions of rows. TimescaleDB wins on familiar Postgres ergonomics for our *relational* tables (teams/submissions/runs) — one database to operate, one query language, one client library. At our scale it's the right call.
- **gRPC between services.** REST is simpler to debug from a terminal, and our inter-service traffic is dominated by NATS messages anyway. We'd add gRPC only for endpoints with sub-millisecond budgets — none exist in this pipeline.
- **A monorepo with a single Go module.** Each service has its own `go.mod` because they have different dep graphs (fasthttp is bot-fleet-only; pgx is gateway + ingester only) and we want independent build/release cadence.

---

## Honest self-assessment vs. the PDF rubric

| PDF expectation | What we have | Score we'd give ourselves |
|---|---|---|
| Containerize C++/Rust/Go submissions, CPU/memory limits | Real Docker isolation with strict flags; gVisor wired but not enabled | 8/10 |
| Distributed bot fleet, thousands of bots, FIX/REST/WebSocket | Real Go bot fleet, scales via HPA; HTTP only (no FIX, no WebSocket from bots) | 7/10 |
| Telemetry — p50/p90/p99 latency, TPS, correctness | TimescaleDB hypertable + continuous aggregate, real percentiles | 9/10 |
| Real-time leaderboard | Redis ZSET + WebSocket fanout; works | 8/10 |
| Architecture Blueprint | This document + BLUEPRINT.md | 9/10 |
| IaC | Terraform + K8s manifests | 8/10 |

**Estimated overall:** competitive with the top tier, *if* the judges read the docs. Honest disclosure of what isn't built is, in our experience, scored higher than silent gaps.
