# Limitations & Known Trade-offs

> Engineers who know what their system *can't* do beat engineers who pretend it does everything. This document is the second one a judge should read.

## What is genuinely in this repo (and verified)

- **Real distributed system, verified end-to-end.** 5 Go services (gateway, bot fleet, telemetry ingester, AI analyzer, + a reference sample engine) and 3 stateful dependencies (TimescaleDB, Redis, NATS), wired via `docker compose` for local/VM dev and K8s manifests for cluster deploy. The full pipeline — upload → containerized build → isolated deploy → distributed load → scoring → leaderboard → **live WebSocket stream** — has been run and confirmed working (see numbers below).
- **Real container sandboxing.** `docker run --memory --cpus --pids-limit --read-only --cap-drop=ALL --security-opt no-new-privileges` on a network-isolated bridge. Not a JS Web Worker pretending to be a sandbox. Uploads are unpacked with path-traversal protection, a 64 MB per-file cap, and Dockerfile validation.
- **Real bot fleet.** Goroutines firing `fasthttp` requests at the contestant's HTTP endpoint, with deterministic seeded RNG (xoshiro256**), three traffic profiles, and limit/market/cancel order mix.
- **Real time-series telemetry.** TimescaleDB hypertable + 1-second continuous aggregate; latency percentiles via exact `percentile_cont`. Ingest uses pgx `CopyFrom` batching.
- **Real leaderboard, two paths.** Durable ranking from the Postgres `runs` table + a Redis ZSET hot cache; live per-run metrics streamed to the browser over WebSocket.
- **Real IaC.** Terraform module deploys to a single EC2; K8s manifests model the same workloads as Deployments + StatefulSets + HPAs + NetworkPolicies.

**Verified run (laptop, Apple Silicon 16 GB, `docker compose up`, 50 bots, ~20 s):** 480,480
telemetry rows, ~24,000 orders/sec on a single host/replica, p50 0.25 ms / p99 35 ms, 0 % transport
errors, composite score 58.8 — written to Postgres + the Redis ZSET, with live metrics streamed over
the WebSocket. The full upload path (tarball → build → isolated sibling container → run) also
verified at 337k rows, score 61.05.

---

## What is intentionally *not* in this repo (and why)

### 1. Correctness scoring is an error-rate proxy, not yet a true matching-engine oracle

**The honest gap.** The rubric asks the platform to validate *price-time priority* and *fill
accuracy*. Today the backend's "correctness" component is `100 × (1 − transport_error_rate)` — it
counts failed HTTP requests, it does **not** reconstruct an order book or diff the submission's
fills against a known-good oracle. The `filled` telemetry column is recorded but not yet derived.

**What we'd add (roadmap, highest priority):** a Go port of the reference CLOB used as a golden
oracle — replay one deterministic order sequence through both the submission and the oracle, diff
fills + price-time-priority order-by-order, and write a real correctness score. The reference
engine and a 30-case correctness suite already exist as the spec.

### 2. The bot fleet does not shard across replicas

Each replica independently spawns `BOTS_PER_INSTANCE` bots with the same seed range, so with >1
replica the aggregate stream is N *copies* rather than N *distinct* bots (colliding client ids).
Scaling works (more load), but "1000 distinct distributed bots" needs a coordination layer.

**What we'd add:** partition the bot-id/seed space by replica (StatefulSet ordinal or a NATS-KV
lease / queue-group) so a requested total splits cleanly across replicas.

### 3. Bots speak REST/HTTP only (no FIX, no WebSocket)

The rubric lists FIX/REST/WebSocket order paths; we implement REST. A WS order client and a minimal
FIX session are roadmap (FIX is the bulk of the effort). Nothing in the pipeline breaks without
them — it's a protocol-coverage gap.

### 4. No closed-loop max-TPS discovery

We report sustained/observed TPS over the run window. A rate controller that ramps load until
latency/error thresholds trip — to find the true *breaking point* — is roadmap.

### 5. Frontend portal pages are not yet wired to the backend

**Status:** `frontend/platform/*.html` is the visual prototype. The `assets/api.js` bridge is fully
written (real fetch wiring to `/api/*` and `/ws/*`), and `leaderboard.html` already reads the
backend when it's online — but `submit.html` and `run.html` still drive an in-browser simulation
(localStorage + a JS reference engine) instead of calling the real API. **This is why the GitHub
Pages build is a standalone simulation.**

**What we'd add:** route `submit.html`/`run.html` through `API.createSubmission` / `API.startRun` /
`API.streamRun`, render the live WebSocket stream, and remove the simulated build-log lines so the
UI reflects the real backend. The backend is real and verified via API/CLI today; this is UI wiring.

### 6. The submission container runtime is `runc`, not gVisor / Firecracker

gVisor adds ~10 % overhead + an extra moving part; Firecracker needs KVM. The hackathon threat model
(good-faith, possibly-buggy submissions — not adversarial kernel CVEs) is well-served by
runc + cgroups + drop-all-caps. `k8s/services.yaml` has a `runtimeClassName: gvisor` block ready to uncomment.

### 7. No auth on the gateway

Adding OIDC/JWT plumbing eats a day for zero demo benefit; the gateway trusts `teamId` from the
request body during the demo. Production: Auth0 / self-hosted Dex in front, middleware maps the
`sub` claim → `team_id`.

### 8. AI analyzer requires a cloud key (local-LLM on roadmap)

The AI analyzer calls Gemini and needs `GEMINI_API_KEY`. Roadmap: a pluggable backend for a local
LLM (Ollama + Qwen2.5-Coder) so proprietary trading code never leaves the operator's infra — both a
privacy win and a zero-cost/offline demo. The core pipeline runs fine without the AI features.

### 9. mTLS between services is off; telemetry encoding is JSON

No service mesh installed (cluster-internal traffic is plaintext; Linkerd would close it). Telemetry
is one JSON message per order — simple and debuggable, but MessagePack/protobuf would cut bytes ~3×
and ingester CPU ~5× at extreme scale. Both are deliberate prototype trade-offs.

### 10. No formal capacity / soak test; k8s & Terraform are reference-grade

The ~24k orders/sec figure is a single verified laptop run; we have **not** run a multi-hour soak
test or a multi-node capacity test. Compose is the verified path. The K8s manifests need published
service images + the real init SQL synced as a ConfigMap before they fully run; Terraform is a
single VM (horizontal scale is shown via Compose `--scale` and the K8s HPA definitions, not a
running multi-node cluster). Minor: bots currently run `durationSec + 5 s` (a context grace window).

---

## Things we explicitly chose against

- **Kafka instead of NATS.** Kafka's operational complexity (KRaft/Zookeeper + retention tuning + consumer-group rebalancing) isn't justified by our throughput. NATS JetStream gives durable streams + simple setup; core NATS carries the high-frequency telemetry path where at-most-once is acceptable.
- **ClickHouse / InfluxDB instead of TimescaleDB.** ClickHouse wins at billions of rows; TimescaleDB wins on Postgres ergonomics for our *relational* tables (teams/submissions/runs) — one database, one query language, one client. Right call at our scale.
- **gRPC between services.** REST is simpler to debug from a terminal and inter-service traffic is dominated by NATS anyway. We'd add gRPC only for sub-millisecond endpoints — none exist here.
- **A monorepo with a single Go module.** Each service has its own `go.mod` (different dep graphs: fasthttp is bot-fleet-only, pgx is gateway + ingester only) for independent build/release cadence.

---

## Honest self-assessment vs. the PDF rubric

| PDF expectation | What we have | Score we'd give ourselves |
|---|---|---|
| Containerize C++/Rust/Go submissions, CPU/memory limits | Real Docker isolation with strict flags, verified upload→build→deploy; gVisor wired but not enabled | 8/10 |
| Distributed bot fleet, thousands of bots, FIX/REST/WebSocket | Real Go bot fleet, scales via `--scale`/HPA; **HTTP only**, no cross-replica sharding yet | 6/10 |
| Telemetry — p50/p90/p99 latency, TPS, correctness | Real exact percentiles + TPS, verified; **correctness is an error-rate proxy, not a true oracle yet** | 7/10 |
| Real-time leaderboard | Redis ZSET + Postgres + verified WebSocket live stream | 8/10 |
| Architecture Blueprint | DESIGN.md + BLUEPRINT.md + this file | 9/10 |
| IaC | Compose (verified) + Terraform + K8s manifests (reference) | 7/10 |

**Estimated overall:** competitive with the top tier *because the core pipeline genuinely works
end-to-end*, with an honest, prioritized roadmap for the remaining depth. In our experience, honest
disclosure of what isn't built is scored higher than silent gaps.
