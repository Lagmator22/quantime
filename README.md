# QuanTime

A distributed benchmarking & hosting platform for trading infrastructure - built for the **IICPC Summer Hackathon 2026** challenge.

> Developers upload a matching engine. QuanTime containerizes it under strict isolation, hammers it with a distributed bot fleet, captures real telemetry, and ranks teams on a live composite of latency, throughput, and correctness.

**Read these alongside this file:**
- [DESIGN.md](DESIGN.md) - full system design document (architecture, data flow, scoring, ADRs, verified results)
- [BLUEPRINT.md](BLUEPRINT.md) - architecture, data model, scoring formula
- [LIMITATIONS.md](LIMITATIONS.md) - what's built vs. roadmap, honestly

> **Status:** The full pipeline - upload → containerized deploy → distributed load → real-time scoring - is implemented and verified end-to-end via `docker compose up`. See [§ Verified results](#verified-results).

---

## Quickstart - one command

```bash
git clone https://github.com/Lagmator22/quantime.git && cd quantime
docker compose up --build
```

Open <http://localhost:8080>. That brings up:

| Service | Port | What it is |
|---|---|---|
| Caddy (frontend + reverse proxy) | 8080 | Open this in a browser |
| Gateway (HTTP/WS API) | internal | `/api/*`, `/ws/*` |
| AI Analyzer | internal | Multi-agent code analysis (Gemini; local-LLM on roadmap) |
| Bot fleet | internal | Load generator (scale with `--scale botfleet=N`) |
| Telemetry ingester | internal | NATS → TimescaleDB + Redis |
| TimescaleDB | 5432 | Time-series DB |
| Redis | 6379 | Hot state + live pub/sub |
| NATS | 4222 | Message bus |
| Sample submission | internal | Reference engine on port 9001 |

---

## End-to-end demo (5 minutes)

The whole pipeline, against a real upload. (`jq` and a running Docker are the only prerequisites.)

```bash
# 1. Boot the stack
docker compose up -d --build
until curl -sf http://localhost:8080/api/health; do sleep 2; done

# 2. Package + submit the sample engine.
#    NOTE: curl uploads a TAR ARCHIVE, not a directory - pack it first.
tar -czf /tmp/sample.tar.gz -C examples/sample-engine-go .
SUB=$(curl -s -F "teamId=t_demo" -F "name=sample" -F "lang=go" \
      -F "source=@/tmp/sample.tar.gz" http://localhost:8080/api/submissions | jq -r .id)
echo "submission: $SUB"

# 3. Wait for build + deploy (SaveSource → docker build → isolated sibling container)
until [ "$(curl -s http://localhost:8080/api/submissions/$SUB | jq -r .Status)" = "deployed" ]; do
  echo "  building…"; sleep 2
done

# 4. Launch a 30s stress run
RUN=$(curl -s -H "Content-Type: application/json" -X POST \
      -d "{\"submissionId\":\"$SUB\",\"profile\":\"sustained\",\"seed\":42,\"durationSec\":30,\"botsPerFleet\":50}" \
      http://localhost:8080/api/runs | jq -r .id)
echo "run: $RUN"

# 5. Watch live telemetry stream (npm i -g wscat first, or just watch the leaderboard)
wscat -c ws://localhost:8080/ws/runs/$RUN
#   → {"type":"metrics","orders":...,"tps":...,"avgLatMs":...} every 1s, then {"type":"final",...}

# 6. After it finishes, see the score + leaderboard
curl -s http://localhost:8080/api/runs/$RUN | jq .          # status:finished, score, finishedAt
curl -s http://localhost:8080/api/leaderboard | jq .        # ranked teams w/ p50/p99/tps

# 7. AI code analysis (optional - requires GEMINI_API_KEY in .env, see AI setup below)
curl -s -H "Content-Type: application/json" -X POST \
     -d '{"sourceCode":"package main\nfunc submit(o Order) {}"}' \
     http://localhost:8080/api/analyze | jq .
#   → {"riskScore":45,"findings":[...],"recommendations":[...]}

# 8. Scale the bot fleet horizontally
docker compose up -d --scale botfleet=4
```

**Fastest path (skip the upload):** the stack seeds a pre-deployed submission `sub_sample`, so you
can launch a run immediately - `POST /api/runs` with `{"submissionId":"sub_sample", …}` - to see
load → telemetry → score → leaderboard without building anything.

---

## Hosting & deployment

> **What's verified:** **Docker Compose** is the fully-tested, end-to-end path (local, Codespaces,
> or any VM). The Kubernetes, Terraform, and DigitalOcean assets below are **reference
> Infrastructure-as-Code** that demonstrate the horizontal-scale design; they are provided as a
> starting point, not a one-click guarantee. See [LIMITATIONS.md](LIMITATIONS.md) for the honest status of each.

| Path | What's hosted | Cost | Always-on? |
|---|---|---|---|
| **Docker Compose** (verified) | Full stack, one command, any machine with Docker | Free | While running |
| **GitHub Codespaces** | Full stack via `docker compose up` | 180 hrs/mo free w/ Student Pack | On-demand |
| **GitHub Pages** | Frontend **prototype only** - a self-contained browser simulation, **not** the real backend | Free | Yes |
| **DigitalOcean / AWS Terraform** | Full stack on a single VM | student credit / ~$30–60/mo | Yes |

> ⚠️ **About GitHub Pages:** Pages can only serve static files, so it hosts the in-browser
> *prototype* (`frontend/`), which simulates the pipeline in JavaScript. The real distributed
> system needs Docker/Postgres/Redis/NATS and runs via Compose on a real machine - it cannot run
> on Pages. To get a live public URL for the real backend, run Compose on a VM (or your own
> machine) and expose it with a free tunnel (e.g. Cloudflare Tunnel).

### GitHub Codespaces (full stack, on-demand)

**Code → Codespaces → Create codespace on master.** The `.devcontainer/devcontainer.json`
provisions Docker-in-Docker, Go 1.22, Terraform, and kubectl, and forwards port 8080 with a public
preview URL. Inside the shell: `docker compose up --build`, then open the forwarded `:8080`.

### DigitalOcean droplet

```bash
SSH_KEY=<your-fingerprint> ./deploy/digitalocean/deploy.sh
# Or paste deploy/digitalocean/cloud-init.yaml into the DO console (Advanced → user data).
```

### AWS via Terraform

```bash
cd terraform
terraform init
terraform apply -var "repo_url=https://github.com/Lagmator22/quantime.git"
open $(terraform output -raw url)
```

Single EC2 + EBS + Elastic IP + SSM Session Manager (no SSH key required).

### Kubernetes (reference manifests)

```bash
kubectl apply -f k8s/namespace.yaml
kubectl apply -f k8s/datastores.yaml
kubectl apply -f k8s/services.yaml
kubectl apply -f k8s/ingress.yaml
kubectl -n iicpc get pods
kubectl -n iicpc port-forward svc/caddy 8080:80
```

The manifests model the horizontal-scale design: a NATS JetStream cluster, HPAs for the gateway
and bot fleet (4→50), a NetworkPolicy isolating submission pods, and RBAC. They require published
service images + the init SQL synced as a ConfigMap before they will fully run (see LIMITATIONS).

---

## Repo layout

```
quantime/
├── README.md                - this file
├── DESIGN.md                - full system design document
├── BLUEPRINT.md             - architecture
├── LIMITATIONS.md           - what's built vs roadmap, honestly
├── docker-compose.yml       - one-command local stack (verified)
├── Caddyfile                - edge / reverse proxy
├── .env.example             - env config (copy to .env)
├── sql/init.sql             - TimescaleDB schema + hypertable + cagg
├── frontend/                - static UI (HTML/CSS/JS prototype)
│   ├── index.html           - public landing
│   └── platform/            - developer portal pages
│       ├── dashboard.html  submit.html  run.html  correctness.html
│       ├── analyze.html     - AI code analysis page (NEW)
│       ├── leaderboard.html  judge.html  architecture.html  docs.html
│       └── assets/          - shared CSS/JS, engine, runtimes
├── services/
│   ├── gateway/             - Go: HTTP/WS API, Docker sandbox spawner
│   │   └── internal/        - api · store(pgx) · cache(redis) · bus(nats) · sandbox(docker)
│   ├── ai-analyzer/         - Go: multi-agent code review via LLM (NEW)
│   │   └── internal/        - agents(security/perf/correctness + synthesizer) · gemini · report
│   ├── botfleet/            - Go: goroutine-per-bot, fasthttp, xoshiro256** RNG
│   └── telemetry/           - Go: NATS → batched CopyFrom → TimescaleDB + Redis ZADD + live WS
├── tests/                   - standalone unit tests (27 tests)
│   ├── sandbox_test.go      - archive extraction, path traversal, Dockerfile validation
│   ├── scoring_test.go      - composite score math, edge cases
│   └── agent_test.go        - risk scoring, recommendation dedup, strengths
├── examples/sample-engine-go/ - reference matching engine (the "submission")
├── .github/workflows/ci.yml - CI: build + vet + test(-race) + docker build
├── terraform/  k8s/  deploy/digitalocean/   - reference IaC
└── scripts/demo.sh          - end-to-end demo script
```

---

## Engineering principles

1. **Honest about what's a prototype.** See LIMITATIONS.md. We've shipped a working distributed system that's verified end-to-end; we have *not* shipped a hardened production system. Judges respect the distinction.
2. **Real container isolation, not Web-Worker theatre.** Submissions run in `docker run` containers with `--memory --cpus --pids-limit --read-only --cap-drop=ALL --security-opt no-new-privileges` on a network-isolated bridge.
3. **Integer ticks for prices, BIGINT for quantities.** Floats in a matching engine are a quant red flag.
4. **Deterministic replay.** Same `(submission, seed)` → same bot order stream. Forensic replay is `SELECT * FROM telemetry WHERE run_id=$1 ORDER BY ts`.
5. **One database, one bus, one cache.** Postgres+Timescale (relational + time-series), NATS (messaging), Redis (hot state + live pub/sub). No premature CQRS, no exotic stores, no service mesh - until justified.
6. **Comments explain *why*, not *what*.** Every file header explains what the thing is and why it exists.

---

## Contributing

Each service has its own Go module. To work on one:

```bash
cd services/gateway
go mod tidy
go vet ./...
go run ./cmd
```

Run unit tests (no external deps needed):

```bash
cd tests
go test -v -count=1 -race ./...
# 27 tests: sandbox extraction, scoring math, agent risk scoring
```

Run the full stack via `docker compose up --build` and iterate; `docker compose up --build gateway` rebuilds just that service.

### AI analysis setup

The AI Analyzer currently uses Google's **Gemini** API (generous free tier, no card required):

```bash
# 1. Get a free key at https://aistudio.google.com/app/apikey
cp .env.example .env
echo "GEMINI_API_KEY=your-key-here" >> .env
# 2. Rebuild and start
docker compose up --build ai-analyzer
```

> **Roadmap - local/offline AI:** a pluggable backend for a local LLM (e.g. Ollama + Qwen2.5-Coder)
> so proprietary trading code never leaves your infrastructure. Tracked in LIMITATIONS.md. Until
> then, the AI features are optional - the core benchmarking pipeline runs without any API key.

---

## Test Coverage

| Test File | Tests | What it verifies |
|---|---|---|
| `sandbox_test.go` | 6 | tar.gz/zip extraction, Dockerfile validation, path-traversal protection |
| `scoring_test.go` | 13 | Composite scoring formula, edge cases (zero, negative, overflow) |
| `agent_test.go` | 8 | Risk score computation, recommendation dedup, strength detection |

---

## <a name="verified-results"></a>Verified results

Measured on a developer laptop (Apple Silicon, 16 GB) with `docker compose up`, 50 bots, ~20 s:

- **5 real engines benchmarked + oracle-verified + ranked**: Go, Python, C++, [timothewt's order book](https://github.com/timothewt/OrderBook), and **[Liquibook](https://github.com/objectcomputing/liquibook)** — the canonical 1.5k★ open-source matching engine (Object Computing). All speak `POST /submit`; Liquibook passes the correctness oracle 10/10.
- **~24,000 orders/sec** on the fastest engine (Go, p99 35 ms); the C++ engines reveal that a 2M-ops/sec core (Liquibook) is capped to ~6.5k tps by its single-threaded HTTP layer — QuanTime measures the **real end-to-end** path, not just the matching core.
- **Tail latency** (p50…p99.99/max), **open-loop breaking-point discovery**, **cross-version regression gate**, and **LOBSTER market-data replay** — all live in the Console.
- Full **upload path** verified: tarball → build → isolated sibling container → correctness oracle → benchmark.

See [DESIGN.md § 19](DESIGN.md) for the full methodology and the honest roadmap.
