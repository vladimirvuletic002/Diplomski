# Diplomski — Microservice Versioning and Automated Provisioning of New Releases

Bachelor's thesis project: three microservices promoted through canary releases with
metric-driven automated rollback (Argo CD + Argo Rollouts + Prometheus on Kubernetes).

> Status: **implementation complete** — services, cluster, progressive delivery, CI/CD,
> automated rollback and backup/restore all work end to end. Remaining work is the thesis text.

## Requirements

- Docker (for the container workflow and the cluster)
- `kind`, `kubectl`, `helm` (for the cluster)
- Go 1.26.5 or newer (for running from source and for the tests)
- `python3` and `lsof` (only for the local canary demo script)

## Kubernetes cluster

One command creates the cluster and installs the whole platform:

```bash
./scripts/bootstrap.sh up
```

That produces a single-node [kind](https://kind.sigs.k8s.io) cluster running Kubernetes 1.36 with:

| Namespace | Component | Role |
|---|---|---|
| `traefik` | Traefik Proxy v3.7.9 | Traffic entry; canary weighting |
| `argo-rollouts` | Argo Rollouts v1.9.1 | Canary strategy and automated rollback |
| `monitoring` | kube-prometheus-stack 87.21.0 | Metrics the rollout analysis acts on |
| `argocd` | Argo CD v3.4.5 | GitOps reconciliation |
| `energy` | the three services | Rollouts, weighted TraefikService, analysis |

The script is idempotent — re-running `up` reuses an existing cluster and reinstalls in place.

```bash
./scripts/bootstrap.sh status        # what is running, and which images are loaded
./scripts/bootstrap.sh load          # rebuild the service images and reload them
./scripts/bootstrap.sh ui argocd     # also: grafana | prometheus | rollouts
./scripts/bootstrap.sh down          # delete the cluster
```

The cluster is reachable at <http://localhost>; until an application route exists, that
correctly returns 404 from Traefik.

`bootstrap.sh` side-loads locally built images with `kind load docker-image`, so manifests set
`imagePullPolicy: IfNotPresent`. A release driven by CI instead pushes to ghcr.io and the cluster
pulls from there — the packages are public, so no pull secret is needed.

> Traefik replaces the NGINX Ingress Controller, which was retired in March 2026. Argo Rollouts
> drives Traefik natively through the `TraefikService` CRD, with no plugin required.

## Services

| Service | Port | Role |
|---|---|---|
| `gateway` | 8080 | Single entry point; serves the dashboard and `/api/summary` |
| `metering-service` | 8081 | Simulated electricity-consumption readings |
| `aggregation-service` | 8082 | Calls metering-service, derives totals |

Call chain: `gateway → aggregation-service → metering-service`

Each service is its own Go module, so build and test commands run from that service's directory.

## Running with Docker Compose

The quickest way to run the whole system:

```bash
docker compose up --build
```

Then open <http://localhost:8080>.

Only the gateway publishes a port. metering-service and aggregation-service are reachable only
from inside the compose network, so all traffic enters through the gateway.

Each image is built from a multi-stage Dockerfile onto `distroless/static`, giving ~20 MB images
with no shell or package manager. The version is baked in at build time from `build.args.VERSION`,
so a container always reports the version of the image tag it shipped in.

To make a service faulty — same image, configuration only, no rebuild:

```bash
ERROR_RATE=0.3 docker compose up -d --force-recreate metering-service   # 30% of its requests fail
docker compose up -d --force-recreate metering-service                  # back to healthy
```

`EXTRA_LATENCY_MS` works the same way. Stop everything with `docker compose down`.

Building a single image by hand:

```bash
docker build --build-arg VERSION=v2.0.0 \
  -t ghcr.io/vladimirvuletic002/diplomski/metering-service:v2.0.0 \
  services/metering-service
```

## Running from source

Start each service in its own terminal:

```bash
# terminal 1
cd services/metering-service    && VERSION=v1.0.0 PORT=8081 go run .
# terminal 2
cd services/aggregation-service && VERSION=v1.1.0 PORT=8082 METERING_URL=http://localhost:8081 go run .
# terminal 3
cd services/gateway             && VERSION=v1.0.0 PORT=8080 AGGREGATION_URL=http://localhost:8082 go run .
```

Then open <http://localhost:8080> for the live dashboard.

Check the version chain from the command line:

```bash
curl -s localhost:8080/api/summary | python3 -m json.tool
```

```json
{
  "service": "gateway",
  "version": "v1.0.0",
  "chain": [
    {"service": "gateway", "version": "v1.0.0"},
    {"service": "aggregation-service", "version": "v1.1.0"},
    {"service": "metering-service", "version": "v1.0.0"}
  ],
  "data": {"meterCount": 5, "totalKWh": 137.99, "averageKWh": 27.6, "minKWh": 12.4, "maxKWh": 40.8}
}
```

### Building with a baked-in version

`VERSION` above is a convenience for local runs. Builds bake the version in at compile time so it
matches the image tag:

```bash
cd services/metering-service
go build -ldflags "-X main.version=v1.0.0" -o /tmp/metering
PORT=8081 /tmp/metering
```

### Simulating a bad release

Same binary, different configuration:

```bash
# 30% of requests return HTTP 500
cd services/metering-service && VERSION=v2.0.0 ERROR_RATE=0.3 PORT=8081 go run .

# every request takes an extra 500 ms
cd services/metering-service && VERSION=v2.1.0 EXTRA_LATENCY_MS=500 PORT=8081 go run .
```

Both are visible in `/metrics`, split by version:

```bash
curl -s localhost:8081/metrics | grep '^http_requests_total'
```

```
http_requests_total{method="GET",route="/",service="metering-service",status="200",version="v2.0.0"} 136
http_requests_total{method="GET",route="/",service="metering-service",status="500",version="v2.0.0"} 64
```

## Running a local canary (v1.0.0 vs v2.0.0)

`scripts/demo-local-canary.sh` runs metering-service `v1.0.0` and `v2.0.0` side by side behind a
traffic splitter, so the dashboard can be seen splitting traffic between two versions.

> This is a **development aid**, not the thesis demo. It stands in for the cluster ingress, and the
> weight is set by hand. In the real system (Phase 4) Argo Rollouts sets the weight, advances it
> automatically, and aborts on a bad release with no human involved.

Start it — the canary comes up live but receiving no traffic, where a real rollout begins:

```bash
./scripts/demo-local-canary.sh start
```

Open <http://localhost:8080>, then shift traffic to the canary. Each command takes effect within a
couple of seconds; nothing restarts, so the dashboard keeps its rolling window:

```bash
./scripts/demo-local-canary.sh weight 10
./scripts/demo-local-canary.sh weight 25
./scripts/demo-local-canary.sh weight 50
./scripts/demo-local-canary.sh weight 100
```

Watch `metering-service` flip to **2 versions live** and the bars track each weight.

To demo a failing release, start with a canary that fails 30% of its requests — same binary as the
healthy one, only the configuration differs:

```bash
./scripts/demo-local-canary.sh start --bad
./scripts/demo-local-canary.sh weight 25
```

The success rate turns red, and in the request strip the failed requests are visibly the canary's.
Argo Rollouts is what turns this observation into an automatic rollback, in Phase 6.

```bash
./scripts/demo-local-canary.sh status   # what is running, and the current weight
./scripts/demo-local-canary.sh stop     # stop everything
```

## Release demos (on the cluster)

```bash
./scripts/demo-good-release.sh   # v1.0.0 -> v2.0.0, canary promotes
./scripts/demo-bad-release.sh    # faulty v3.0.0, analysis aborts and rolls back
```

Both reset to a known baseline first, so they are safe to re-run.

A release can also be driven the way it is in production — push a service-scoped tag
and GitHub Actions builds, pushes to ghcr.io and commits the manifest bump, which
Argo CD then syncs:

```bash
git tag metering-service/v3.2.0 && git push origin metering-service/v3.2.0
```

## Backup and restore

Optional; installs MinIO as an in-cluster S3 backend plus Velero.

```bash
./scripts/setup-velero.sh          # install (adds ~400 MiB)
./scripts/demo-backup-restore.sh   # backup, destroy the namespace, restore
./scripts/setup-velero.sh status
```

The demo restores two things: the `energy` namespace, and the Argo CD repository
credential — which is deliberately kept out of Git and therefore cannot be recovered
from it.

Ports used: 8080 gateway, 8081 stable, 8082 aggregation, 8083 canary, 8090 splitter.
Logs and built binaries go to `.run/` (git-ignored). Re-running `start` is safe — it stops any
previous run first. Requires `python3` and `lsof`.

## Endpoints

Every service exposes:

| Route | Purpose |
|---|---|
| `GET /` | Main payload, including the serving `version` (on the gateway: the dashboard) |
| `GET /healthz` | Liveness/readiness probe |
| `GET /metrics` | Prometheus exposition |

The gateway additionally exposes `GET /api/summary`, returning the full version chain.

`ERROR_RATE` and `EXTRA_LATENCY_MS` affect only `/` and `/api/summary`; `/healthz` and `/metrics`
are never faulted.

## Configuration

All configuration is environment-driven. Invalid values fail fast at startup.

| Variable | Default | Applies to | Meaning |
|---|---|---|---|
| `PORT` | per service | all | Listen port |
| `VERSION` | ldflags value | all | Overrides the build-time version (local dev only) |
| `SERVICE_NAME` | per service | all | Name reported in responses and metrics |
| `ERROR_RATE` | `0` | all | Fraction of requests (0.0–1.0) failed with HTTP 500 |
| `EXTRA_LATENCY_MS` | `0` | all | Artificial delay added per request |
| `LOG_LEVEL` | `info` | all | `debug` / `info` / `warn` / `error` |
| `METERING_URL` | `http://localhost:8081` | aggregation | Upstream metering-service |
| `AGGREGATION_URL` | `http://localhost:8082` | gateway | Upstream aggregation-service |
| `UPSTREAM_TIMEOUT_MS` | `2000` | aggregation, gateway | Upstream call timeout |

## Tests

```bash
for s in services/*/; do (cd "$s" && go vet ./... && go test ./...); done
```

## Licence

MIT — see [LICENSE](LICENSE).
