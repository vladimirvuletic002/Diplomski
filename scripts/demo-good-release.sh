#!/usr/bin/env bash
#
# Promotes metering-service v1.0.0 -> v2.0.0 and watches the canary complete.
#
#   ./scripts/demo-good-release.sh
#
# Re-runnable: resets to v1.0.0 first, so it does not matter what is deployed
# when it starts.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NS="energy"
ROLLOUT="metering-service"
MANIFEST="$REPO_ROOT/k8s/base/metering-service.yaml"
REGISTRY="ghcr.io/vladimirvuletic002/diplomski"

BASELINE="v1.0.0"
TARGET="v2.0.0"

info() { printf '\033[0;36m==>\033[0m %s\n' "$1"; }
ok()   { printf '\033[0;32m  ok\033[0m %s\n' "$1"; }
die()  { printf '\033[0;31merror:\033[0m %s\n' "$1" >&2; exit 1; }

LOAD_PID=""
cleanup() { [ -n "$LOAD_PID" ] && kill "$LOAD_PID" 2>/dev/null || true; }
trap cleanup EXIT

preflight() {
  command -v kubectl >/dev/null 2>&1 || die "kubectl is not installed"
  kubectl get --raw /healthz >/dev/null 2>&1 || die "the cluster is not reachable — run: ./scripts/bootstrap.sh up"
  kubectl get rollout "$ROLLOUT" -n "$NS" >/dev/null 2>&1 \
    || die "rollout/$ROLLOUT not found — apply k8s/base first"
  curl -sf -o /dev/null -m 5 http://localhost/api/summary 2>/dev/null \
    || die "http://localhost is not serving — check Traefik and the IngressRoutes"
}

# Image and version label move together — the analysis reads the label to decide
# what to measure, so bumping only the image would score the previous release.
#
# JSON patch, not strategic merge: Rollout is a custom resource, and strategic
# merge only works for built-in types.
set_version() {
  kubectl patch rollout "$ROLLOUT" -n "$NS" --type=json -p "[
    {\"op\": \"replace\", \"path\": \"/metadata/labels/version\", \"value\": \"$1\"},
    {\"op\": \"replace\", \"path\": \"/spec/template/spec/containers/0/image\", \"value\": \"$REGISTRY/$ROLLOUT:$1\"}
  ]" >/dev/null
}

wait_healthy() {
  for _ in $(seq 1 "${1:-40}"); do
    [ "$(kubectl argo rollouts status "$ROLLOUT" -n "$NS" --timeout 3s 2>/dev/null)" = "Healthy" ] && return 0
    sleep 3
  done
  return 1
}

weights() {
  local out
  for _ in $(seq 1 10); do
    out=$(kubectl get traefikservice "$ROLLOUT-weighted" -n "$NS" -o json 2>/dev/null | python3 -c '
import sys, json
try:
    s = json.load(sys.stdin)["spec"]["weighted"]["services"]
    vals = [x.get("weight") for x in s]
    print("/".join(str(v) for v in vals) if all(v is not None for v in vals) else "")
except Exception:
    print("")')
    [ -n "$out" ] && { echo "$out"; return; }
    sleep 1
  done
  # Applying the manifest strips the weights — arrays are replaced wholesale on a
  # custom resource — and the controller only rewrites them during a rollout.
  # Harmless while stable, since both Services then select the same pods.
  echo "—"
}

start_load() {
  ( while true; do curl -s -o /dev/null -m 5 http://localhost/api/summary; sleep 0.2; done ) &
  LOAD_PID=$!
  disown "$LOAD_PID" 2>/dev/null || true
}

# Several requests, not one: at 10% weight a single request almost always lands
# on stable and shows nothing.
observed_split() {
  local n=12
  for _ in $(seq 1 $n); do
    curl -s -m 5 http://localhost/api/summary 2>/dev/null \
      | python3 -c 'import sys,json;print(json.load(sys.stdin)["chain"][2]["version"])' 2>/dev/null
  done | sort | uniq -c | awk '{printf "%s×%s ", $2, $1}'
}

# --- run --------------------------------------------------------------------

preflight

info "Resetting to $BASELINE"
set_version "$BASELINE"
sleep 2
# --full skips the canary steps: this is setup, not the demonstration.
kubectl argo rollouts promote "$ROLLOUT" -n "$NS" --full >/dev/null 2>&1 || true
wait_healthy 40 || die "could not reach a healthy $BASELINE baseline"
ok "$BASELINE is stable, traffic weights $(weights)"

start_load
ok "traffic generator running"
echo

info "Promoting $BASELINE -> $TARGET"
set_version "$TARGET"

echo
printf '  %-7s %-12s %-13s %s\n' "TIME" "WEIGHTS" "PHASE" "OBSERVED (12 requests)"
start=$SECONDS
for _ in $(seq 1 80); do
  phase=$(kubectl get rollout "$ROLLOUT" -n "$NS" -o jsonpath='{.status.phase}' 2>/dev/null)
  printf '  %-7s %-12s %-13s %s\n' "$((SECONDS-start))s" "$(weights)" "${phase:-?}" "$(observed_split)"
  [ "$phase" = "Healthy" ] && break
  [ "$phase" = "Degraded" ] && break
  sleep 3
done

echo
phase=$(kubectl get rollout "$ROLLOUT" -n "$NS" -o jsonpath='{.status.phase}' 2>/dev/null)
if [ "$phase" = "Healthy" ]; then
  ok "Promoted. $TARGET is now stable after passing analysis at every step."
  kubectl get analysisrun -n "$NS" --sort-by=.metadata.creationTimestamp --no-headers 2>/dev/null \
    | tail -1 | awk '{printf "  AnalysisRun %s: %s\n", $1, $2}'
else
  die "expected a healthy promotion but the rollout is $phase"
fi
