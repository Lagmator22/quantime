# IICPC Platform

A distributed benchmarking and hosting platform for the **IICPC Summer Hackathon 2026** trading-infrastructure challenge.

> Contestants upload a matching engine. The platform containerizes it, hammers it with a distributed bot fleet, captures real telemetry, and ranks teams on a composite of latency, throughput, and correctness.

**Two documents you should read alongside this one:**
- [BLUEPRINT.md](BLUEPRINT.md) — architecture, data model, scoring formula
- [LIMITATIONS.md](LIMITATIONS.md) — what's not built and why

---

## Quickstart — one command

```bash
git clone <this-repo> && cd iicpc-platform
docker compose up --build
```

Open <http://localhost:8080>.

That brings up:

| Service | Port | What it is |
|---|---|---|
| Caddy (frontend + reverse proxy) | 8080 | Open this in a browser |
| Gateway (HTTP/WS API) | internal | `/api/*`, `/ws/*` |
| Bot fleet | internal | Load generator (scale with `--scale botfleet=N`) |
| Telemetry ingester | internal | NATS → TimescaleDB |
| TimescaleDB | 5432 | Time-series DB |
| Redis | 6379 | Hot state |
| NATS | 4222 | Message bus |
| Sample submission | internal | Reference engine on port 9001 |

---

## End-to-end demo (5 minutes)

```bash
# 1. Boot the stack
docker compose up -d --build

# 2. Submit the sample engine to the platform
curl -F "teamId=t_demo" -F "name=sample" -F "lang=go" \
     -F "source=@examples/sample-engine-go" \
     http://localhost:8080/api/submissions
# → {"id":"sub_xxx","hash":"...","status":"uploaded"}

# 3. Wait for build + deploy (poll status)
watch -n1 'curl -s http://localhost:8080/api/submissions/sub_xxx'
# → status transitions: uploaded → building → built → deployed

# 4. Launch a stress run
curl -H "Content-Type: application/json" -X POST \
     -d '{"submissionId":"sub_xxx","profile":"sustained","seed":42,"durationSec":30,"botsPerFleet":50}' \
     http://localhost:8080/api/runs
# → {"id":"run_yyy","status":"running"}

# 5. Watch live telemetry
wscat -c ws://localhost:8080/ws/runs/run_yyy

# 6. After 30s, see the leaderboard
curl http://localhost:8080/api/leaderboard | jq .

# 7. Scale the bot fleet horizontally
docker compose up -d --scale botfleet=4
```

---

## Hosted demo paths

| Path | What's hosted | Cost | Always-on? |
|---|---|---|---|
| **GitHub Pages** | Frontend only (localStorage prototype) | Free | Yes |
| **GitHub Codespaces** | Full stack via `docker compose up` | 180 hrs/mo free w/ Student Pack | On-demand |
| **DigitalOcean droplet** | Full stack always running | $200 student credit ≈ 8 months on $24/mo | Yes |
| **AWS Terraform (below)** | Full stack on EC2 | $30–60/mo | Yes |

### A. GitHub Pages (frontend only)

The `.github/workflows/pages.yml` action publishes `frontend/` on every push to `master`. After the first run:

1. Repo → **Settings → Pages → Source: GitHub Actions**
2. Site goes live at `https://<your-handle>.github.io/iicpc-platform/`

What's published: the in-browser prototype. The correctness suite + UI all work; there's no real distributed backend (that needs B, C, or D below).

### B. GitHub Codespaces (full stack, on-demand)

Click **Code → Codespaces → Create codespace on master**. The `.devcontainer/devcontainer.json` provisions:

- Docker-in-Docker (so the gateway can spawn submission containers)
- Go 1.22, Terraform, kubectl
- Port 8080 auto-forwards with a public preview URL

Inside the Codespace shell:

```bash
docker compose up --build
```

Click the forwarded `:8080` URL to demo. Stop the Codespace when done to preserve free hours.

### C. DigitalOcean droplet (full stack, always-on, cheapest with student credit)

```bash
# One-shot via doctl:
SSH_KEY=<your-fingerprint> ./deploy/digitalocean/deploy.sh

# Or paste deploy/digitalocean/cloud-init.yaml into the DO console:
#   Create Droplet → Advanced options → Add Initialization scripts (user data)
```

Droplet boots, installs Docker, clones the repo, brings the stack up via systemd. ~5 minutes from "Create" to public URL.

### D. AWS via Terraform

```bash
cd terraform
terraform init
terraform apply -var "repo_url=https://github.com/your-team/iicpc-platform.git"
# → outputs: public_ip, url
open $(terraform output -raw url)
```

Single t3.large EC2 + EBS + Elastic IP + SSM Session Manager (no SSH key required).

---

## Deploy to Kubernetes

```bash
# Any cluster: EKS, GKE, AKS, kind, k3s, …
kubectl apply -f k8s/namespace.yaml
kubectl apply -f k8s/datastores.yaml
kubectl apply -f k8s/services.yaml
kubectl apply -f k8s/ingress.yaml

kubectl -n iicpc get pods   # wait for everything Ready
kubectl -n iicpc port-forward svc/caddy 8080:80   # access locally
```

The K8s deploy adds:
- 3-node NATS JetStream cluster
- HPAs for gateway (2..10) and bot fleet (4..50)
- NetworkPolicy isolating submission pods (only the bot fleet can reach them)
- RBAC for the gateway to spawn submission pods dynamically

---

## Repo layout

```
iicpc-platform/
├── README.md                — this file
├── BLUEPRINT.md             — architecture
├── LIMITATIONS.md           — what's not built and why
├── docker-compose.yml       — one-command local stack
├── Caddyfile                — edge / reverse proxy
├── sql/init.sql             — TimescaleDB schema + hypertable + cagg
├── frontend/                — static UI (HTML/CSS/JS, design bundle)
│   ├── index.html           — public landing
│   └── platform/            — contestant portal pages
│       ├── dashboard.html
│       ├── submit.html
│       ├── run.html
│       ├── correctness.html
│       ├── leaderboard.html
│       ├── judge.html
│       ├── architecture.html
│       ├── docs.html
│       └── assets/          — shared CSS/JS, engine, runtimes
├── services/
│   ├── gateway/             — Go: HTTP/WS API, Docker sandbox spawner
│   │   ├── cmd/main.go
│   │   └── internal/
│   │       ├── api/         — handlers, ws, middleware
│   │       ├── store/       — pgx + TimescaleDB
│   │       ├── cache/       — Redis pubsub + ZSET
│   │       ├── bus/         — NATS / JetStream
│   │       └── sandbox/     — docker build + run with strict flags
│   ├── botfleet/            — Go: goroutine-per-bot, fasthttp client
│   │   ├── cmd/main.go
│   │   └── internal/bot/    — bot loop + xoshiro256** RNG
│   └── telemetry/           — Go: NATS → batched COPY → TimescaleDB
│       └── cmd/main.go
├── examples/
│   └── sample-engine-go/    — reference matching engine (the "submission")
│       ├── Dockerfile
│       └── main.go
├── terraform/               — single-EC2 AWS deploy
│   ├── main.tf
│   ├── variables.tf
│   ├── outputs.tf
│   └── cloud-init.yaml
├── k8s/                     — production Kubernetes manifests
│   ├── namespace.yaml       — namespace + ResourceQuota + NetworkPolicy
│   ├── datastores.yaml      — TimescaleDB + Redis + NATS StatefulSets
│   ├── services.yaml        — gateway + botfleet + telemetry Deployments + HPAs
│   └── ingress.yaml         — Caddy + Ingress
├── scripts/
│   └── demo.sh              — end-to-end demo script
└── docs/                    — additional diagrams (if any)
```

---

## Engineering principles

1. **Honest about what's a prototype.** See LIMITATIONS.md. We've shipped a working distributed system; we have *not* shipped a hardened production system. Judges will respect the distinction.
2. **Real container isolation, not Web-Worker theatre.** Submissions run in `docker run` containers with `--memory --cpus --pids-limit --read-only --cap-drop=ALL --security-opt no-new-privileges`. Network-isolated bridge.
3. **Integer ticks for prices, BIGINT for quantities.** Floats in a matching engine are a quant red flag.
4. **Deterministic replay.** Same `(submission, seed)` → same bot order stream. Forensic replay is `SELECT * FROM telemetry WHERE run_id=$1 ORDER BY ts`.
5. **One database, one bus, one cache.** Postgres+Timescale for relational + time-series, NATS for messaging, Redis for hot state. No premature CQRS, no exotic stores, no service mesh — until justified.
6. **Comments explain *why*, not *what*.** Every file's header explains what the thing is and why it exists. Inline comments mark non-obvious decisions only.

---

## Contributing

Each service has its own Go module. To work on one:

```bash
cd services/gateway
go mod tidy
go test ./...
go run ./cmd
```

Run the full stack via `docker compose up --build` and iterate. Hot-reload isn't wired (`reflex` or `air` would do it); for now `docker compose up --build gateway` rebuilds just that service.
