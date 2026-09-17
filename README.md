Bachelor's thesis project: three microservices promoted through canary releases with
metric-driven automated rollback (Argo CD + Argo Rollouts + Prometheus on Kubernetes).

## Requirements

- Docker (for the container workflow and the cluster)
- `kind`, `kubectl`, `helm` (for the cluster)
- Go 1.26.5 or newer (for running from source and for the tests)

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

## Endpoints

Every service exposes:

| Route | Purpose |
|---|---|
| `GET /` | Main payload, including the serving `version` (on the gateway: the dashboard) |
| `GET /healthz` | Liveness/readiness probe |
| `GET /metrics` | Prometheus exposition |

The gateway additionally exposes `GET /api/summary`, returning the full version chain.

## Tests

```bash
for s in services/*/; do (cd "$s" && go vet ./... && go test ./...); done
```

## Licence

MIT — see [LICENSE](LICENSE).
