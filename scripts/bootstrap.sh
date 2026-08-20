#!/usr/bin/env bash
#
# Creates the local Kubernetes cluster and installs the platform the thesis
# demonstration runs on: Traefik, Argo Rollouts, Prometheus and Argo CD.
#
#   ./scripts/bootstrap.sh up        create the cluster and install everything
#   ./scripts/bootstrap.sh load      rebuild the service images and load them
#   ./scripts/bootstrap.sh status    show what is running
#   ./scripts/bootstrap.sh ui <x>    open a UI (argocd | grafana | prometheus | rollouts)
#   ./scripts/bootstrap.sh down      delete the cluster
#
# `up` is idempotent: re-running it against an existing cluster reinstalls the
# platform in place rather than failing, so it is safe to run at any time.
#
# Every version below is pinned. A thesis has to be reproducible months later,
# and "latest" is not a version.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# --- pinned versions --------------------------------------------------------

CLUSTER_NAME="diplomski"
APP_NAMESPACE="energy"

# Traefik replaces the retired ingress-nginx. Argo Rollouts drives it natively
# through the TraefikService CRD — no plugin required.
TRAEFIK_CHART_VERSION="41.1.1"

# v1.9.1 over v1.9.0: security fix (CVE-2026-35469) plus traffic-routing fixes,
# which is the subsystem the canary depends on.
ARGO_ROLLOUTS_VERSION="v1.9.1"

ARGOCD_VERSION="v3.4.5"
KPS_CHART_VERSION="87.21.0"

# service:tag pairs built from services/<name> and side-loaded into the cluster.
# metering-service is built twice so the canary demo has a second version to
# promote; the two images differ only by the version baked into them.
IMAGES=(
  "metering-service:v1.0.0"
  "metering-service:v2.0.0"
  "aggregation-service:v1.1.0"
  "gateway:v1.0.0"
)
REGISTRY="ghcr.io/vladimirvuletic002/diplomski"

# --- output helpers ---------------------------------------------------------

info() { printf '\033[0;36m==>\033[0m %s\n' "$1"; }
ok()   { printf '\033[0;32m  ok\033[0m %s\n' "$1"; }
warn() { printf '\033[0;33m  !\033[0m  %s\n' "$1"; }
die()  { printf '\033[0;31merror:\033[0m %s\n' "$1" >&2; exit 1; }

# --- preconditions ----------------------------------------------------------

require_tools() {
  local missing=()
  for t in docker kind kubectl helm; do
    command -v "$t" >/dev/null 2>&1 || missing+=("$t")
  done
  [ ${#missing[@]} -eq 0 ] || die "missing required tools: ${missing[*]}"
  docker info >/dev/null 2>&1 || die "the Docker daemon is not running — start Docker Desktop first"
}

cluster_exists() {
  kind get clusters 2>/dev/null | grep -qx "$CLUSTER_NAME"
}

# Docker Desktop stops the node container whenever the host sleeps or Docker
# restarts. The cluster itself survives, but the API server needs a few seconds
# to answer again, and every kubectl call fails meanwhile. Waiting here turns a
# confusing "connection refused" into a short pause.
wait_for_api() {
  local tries=${1:-90}
  for _ in $(seq 1 "$tries"); do
    kubectl --context "kind-$CLUSTER_NAME" get --raw /healthz >/dev/null 2>&1 && return 0
    sleep 2
  done
  die "the Kubernetes API did not become reachable"
}

# Waits until at least one pod matching a selector is Ready.
#
# `kubectl wait --for=condition=ready pod --selector=...` cannot be used on its
# own here: on a freshly created cluster the pod does not exist yet when the
# command runs, and kubectl treats "no matching resources" as an immediate error
# rather than something to wait for. Polling handles both the appearing and the
# becoming-ready phases.
wait_for_ready_pod() {
  local ns="$1" selector="$2" tries="${3:-60}"
  for _ in $(seq 1 "$tries"); do
    if kubectl get pods -n "$ns" --selector="$selector" \
         -o jsonpath='{.items[*].status.conditions[?(@.type=="Ready")].status}' 2>/dev/null \
         | grep -q True; then
      return 0
    fi
    sleep 4
  done
  return 1
}

# Waits for every pod in a namespace to be Running or Completed.
wait_for_pods() {
  local ns="$1" want="${2:-1}" tries="${3:-90}"
  for _ in $(seq 1 "$tries"); do
    local total notready
    total=$(kubectl get pods -n "$ns" --no-headers 2>/dev/null | wc -l | tr -d ' ')
    notready=$(kubectl get pods -n "$ns" --no-headers 2>/dev/null \
      | awk '$3!="Running" && $3!="Completed" {c++} END {print c+0}')
    if [ "$total" -ge "$want" ] && [ "$notready" = "0" ]; then
      ok "$ns ready ($total pods)"
      return 0
    fi
    sleep 4
  done
  # Reporting success while pods are still broken would be worse than failing:
  # the next phase would start against a half-installed platform.
  die "$ns did not become ready in time — check: kubectl get pods -n $ns"
}

# Create only if absent. Piping a dry-run manifest into `apply` would also be
# idempotent, but it warns noisily about a missing last-applied-configuration
# annotation on namespaces that were originally created imperatively.
ensure_namespace() {
  kubectl get namespace "$1" >/dev/null 2>&1 || kubectl create namespace "$1" >/dev/null
}

# --- install steps ----------------------------------------------------------

create_cluster() {
  if cluster_exists; then
    info "Cluster '$CLUSTER_NAME' already exists — reusing it"
    wait_for_api
    ok "API reachable"
  else
    info "Creating cluster '$CLUSTER_NAME'"
    kind create cluster --config "$REPO_ROOT/k8s/kind/cluster.yaml" >/dev/null
    wait_for_api
    ok "cluster created (Kubernetes 1.36)"
  fi
  kubectl config use-context "kind-$CLUSTER_NAME" >/dev/null 2>&1 || true
}

install_ingress() {
  info "Installing Traefik (chart $TRAEFIK_CHART_VERSION)"
  helm repo add traefik https://traefik.github.io/charts >/dev/null 2>&1 || true
  helm repo update traefik >/dev/null 2>&1

  helm upgrade --install traefik traefik/traefik \
    --namespace traefik --create-namespace \
    --version "$TRAEFIK_CHART_VERSION" \
    --values "$REPO_ROOT/k8s/traefik/values.yaml" \
    >/dev/null

  if wait_for_ready_pod traefik app.kubernetes.io/name=traefik 60; then
    ok "Traefik ready — cluster reachable on http://localhost"
  else
    die "Traefik did not become ready — check: kubectl get pods -n traefik"
  fi
}

install_rollouts() {
  info "Installing Argo Rollouts ($ARGO_ROLLOUTS_VERSION)"
  ensure_namespace argo-rollouts
  kubectl apply -n argo-rollouts -f \
    "https://github.com/argoproj/argo-rollouts/releases/download/${ARGO_ROLLOUTS_VERSION}/install.yaml" \
    >/dev/null
  # v1.9.1 defaults to Traefik v2's API group (traefik.containo.us), which Traefik
  # v3 no longer serves. Without this the controller cannot find the
  # TraefikService and every rollout fails with TrafficRoutingError. Strategic
  # merge, not JSON merge: the latter replaces the container instead of merging.
  kubectl patch deploy argo-rollouts -n argo-rollouts --type=strategic -p '{
    "spec": {"template": {"spec": {"containers": [{
      "name": "argo-rollouts",
      "args": ["--traefik-api-group=traefik.io", "--traefik-api-version=traefik.io/v1alpha1"]
    }]}}}
  }' >/dev/null

  kubectl wait --for=condition=available deploy/argo-rollouts \
    -n argo-rollouts --timeout=240s >/dev/null 2>&1 \
    || die "the Argo Rollouts controller did not become available — check: kubectl get pods -n argo-rollouts"
  ok "Rollout types registered; controller pointed at Traefik v3 API group"
}

install_monitoring() {
  info "Installing kube-prometheus-stack (chart $KPS_CHART_VERSION)"
  helm repo add prometheus-community https://prometheus-community.github.io/helm-charts >/dev/null 2>&1 || true
  helm repo update prometheus-community >/dev/null 2>&1

  # upgrade --install rather than install, so re-running does not fail on an
  # existing release.
  helm upgrade --install monitoring prometheus-community/kube-prometheus-stack \
    --namespace monitoring --create-namespace \
    --version "$KPS_CHART_VERSION" \
    --values "$REPO_ROOT/k8s/monitoring/values.yaml" \
    >/dev/null
  wait_for_pods monitoring 4
  ok "Prometheus scraping at 15s; ServiceMonitors discovered regardless of labels"
}

install_argocd() {
  info "Installing Argo CD ($ARGOCD_VERSION)"
  ensure_namespace argocd
  # Server-side apply is required: the ApplicationSet CRD is larger than the
  # 256 KB limit on the last-applied-configuration annotation that a normal
  # client-side apply would try to write.
  kubectl apply --server-side --force-conflicts -n argocd -f \
    "https://raw.githubusercontent.com/argoproj/argo-cd/${ARGOCD_VERSION}/manifests/install.yaml" \
    >/dev/null
  wait_for_pods argocd 7
  ok "Argo CD installed"
}

build_and_load_images() {
  info "Building service images and loading them into the cluster"
  for entry in "${IMAGES[@]}"; do
    local svc="${entry%%:*}" tag="${entry##*:}"
    local image="$REGISTRY/$svc:$tag"
    docker build --quiet --build-arg "VERSION=$tag" -t "$image" "$REPO_ROOT/services/$svc" >/dev/null
    # Images are side-loaded rather than pulled: they are not in any registry the
    # cluster can reach. Manifests must therefore use imagePullPolicy:IfNotPresent.
    kind load docker-image "$image" --name "$CLUSTER_NAME" >/dev/null 2>&1
    ok "$svc:$tag"
  done
}

# --- commands ---------------------------------------------------------------

cmd_up() {
  require_tools
  create_cluster
  # Monitoring goes first: it registers the ServiceMonitor type, which Traefik
  # and the Phase 4 application manifests both create.
  install_monitoring
  install_ingress
  install_rollouts
  install_argocd
  ensure_namespace "$APP_NAMESPACE"
  build_and_load_images

  echo
  ok "Platform ready."
  echo
  echo "  Application namespace: $APP_NAMESPACE (empty until Phase 4 manifests are applied)"
  echo "  Ingress:               http://localhost"
  echo
  echo "  Open a UI with:"
  echo "    ./scripts/bootstrap.sh ui argocd"
  echo "    ./scripts/bootstrap.sh ui grafana"
  echo "    ./scripts/bootstrap.sh ui prometheus"
  echo "    ./scripts/bootstrap.sh ui rollouts"
  echo
}

cmd_load() {
  require_tools
  cluster_exists || die "no cluster — run: $0 up"
  wait_for_api
  build_and_load_images
  echo
  ok "Images reloaded. Restart a rollout to pick them up, e.g.:"
  echo "    kubectl argo rollouts restart <name> -n $APP_NAMESPACE"
}

cmd_status() {
  require_tools
  if ! cluster_exists; then
    echo "  cluster '$CLUSTER_NAME': not created  (run: $0 up)"
    return 0
  fi
  if ! kubectl --context "kind-$CLUSTER_NAME" get --raw /healthz >/dev/null 2>&1; then
    warn "cluster exists but the API is not answering yet (Docker may have just restarted)"
    return 0
  fi

  echo "  cluster '$CLUSTER_NAME': running   Kubernetes $(kubectl version -o json 2>/dev/null \
    | python3 -c 'import sys,json;print(json.load(sys.stdin)["serverVersion"]["gitVersion"])' 2>/dev/null || echo '?')"
  echo
  printf '  %-20s %s\n' "NAMESPACE" "PODS RUNNING"
  for ns in traefik argo-rollouts monitoring argocd "$APP_NAMESPACE"; do
    local running total
    running=$(kubectl get pods -n "$ns" --no-headers 2>/dev/null | awk '$3=="Running"{c++} END {print c+0}')
    total=$(kubectl get pods -n "$ns" --no-headers 2>/dev/null | wc -l | tr -d ' ')
    printf '  %-20s %s/%s\n' "$ns" "$running" "$total"
  done
  echo
  echo "  images loaded in the cluster:"
  docker exec "${CLUSTER_NAME}-control-plane" crictl images 2>/dev/null \
    | grep diplomski | awk '{printf "    %s:%s\n", $1, $2}' || echo "    (none)"
  echo
  echo "  ingress: $(curl -s -o /dev/null -w '%{http_code}' -m 5 http://localhost/ 2>/dev/null || echo 'unreachable') \
(404 is expected until an application Ingress exists)"
}

cmd_ui() {
  require_tools
  cluster_exists || die "no cluster — run: $0 up"
  wait_for_api

  case "${1:-}" in
    argocd)
      local pw
      pw=$(kubectl -n argocd get secret argocd-initial-admin-secret \
             -o jsonpath='{.data.password}' 2>/dev/null | base64 -d)
      info "Argo CD → https://localhost:8090   (user: admin, password: ${pw:-<unavailable>})"
      warn "the browser will warn about the self-signed certificate; that is expected"
      echo "  Ctrl-C to stop."
      kubectl port-forward -n argocd svc/argocd-server 8090:443
      ;;
    grafana)
      info "Grafana → http://localhost:3000   (user: admin, password: admin)"
      echo "  Ctrl-C to stop."
      kubectl port-forward -n monitoring svc/monitoring-grafana 3000:80
      ;;
    prometheus)
      info "Prometheus → http://localhost:9090"
      echo "  Ctrl-C to stop."
      kubectl port-forward -n monitoring svc/monitoring-kube-prometheus-prometheus 9090:9090
      ;;
    rollouts)
      command -v kubectl-argo-rollouts >/dev/null 2>&1 \
        || die "the kubectl-argo-rollouts plugin is not installed"
      info "Argo Rollouts dashboard → http://localhost:3100"
      echo "  Ctrl-C to stop."
      kubectl argo rollouts dashboard
      ;;
    *)
      die "usage: $0 ui <argocd|grafana|prometheus|rollouts>"
      ;;
  esac
}

cmd_down() {
  require_tools
  if cluster_exists; then
    info "Deleting cluster '$CLUSTER_NAME'"
    kind delete cluster --name "$CLUSTER_NAME" >/dev/null
    ok "deleted"
  else
    ok "no cluster to delete"
  fi
}

# --- entrypoint -------------------------------------------------------------

case "${1:-}" in
  up)     cmd_up ;;
  load)   cmd_load ;;
  status) cmd_status ;;
  ui)     shift; cmd_ui "${1:-}" ;;
  down)   cmd_down ;;
  *)
    cat <<EOF
Creates the Kubernetes cluster and platform for the thesis demonstration.

  $0 up        create the cluster and install ingress, Argo Rollouts,
               Prometheus and Argo CD, then load the service images
  $0 load      rebuild the service images and reload them into the cluster
  $0 status    show what is running
  $0 ui <x>    port-forward a UI: argocd | grafana | prometheus | rollouts
  $0 down      delete the cluster

Safe to re-run: 'up' reuses an existing cluster and reinstalls in place.
EOF
    exit 1
    ;;
esac
