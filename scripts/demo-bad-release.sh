#!/usr/bin/env bash
#
# Deploys a deliberately faulty metering-service v3.0.0 and watches Argo Rollouts
# detect it and roll back on its own.
#
#   ./scripts/demo-bad-release.sh
#
# v3.0.0 is a clean build of the same source; the fault comes from ERROR_RATE=0.3
# in its pod template. It needs its own version tag because the analysis filters
# metrics by version — reusing the stable tag would blend the failing canary into
# the healthy stable traffic and the query would never cross its threshold.
#
# Re-runnable: resets to a healthy baseline first and restores it at the end.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NS="energy"
ROLLOUT="metering-service"
MANIFEST="$REPO_ROOT/k8s/base/metering-service.yaml"
REGISTRY="ghcr.io/vladimirvuletic002/diplomski"
CLUSTER="diplomski"

BASELINE="v2.0.0"
FAULTY="v3.0.0"
ERROR_RATE="0.3"

info() { printf '\033[0;36m==>\033[0m %s\n' "$1"; }
ok()   { printf '\033[0;32m  ok\033[0m %s\n' "$1"; }
bad()  { printf '\033[0;31m  ✗\033[0m  %s\n' "$1"; }
die()  { printf '\033[0;31merror:\033[0m %s\n' "$1" >&2; exit 1; }

LOAD_PID=""
cleanup() { [ -n "$LOAD_PID" ] && kill "$LOAD_PID" 2>/dev/null || true; }
trap cleanup EXIT

preflight() {
  for t in kubectl docker kind; do command -v $t >/dev/null 2>&1 || die "$t is not installed"; done
  kubectl get --raw /healthz >/dev/null 2>&1 || die "the cluster is not reachable — run: ./scripts/bootstrap.sh up"
  kubectl get rollout "$ROLLOUT" -n "$NS" >/dev/null 2>&1 || die "rollout/$ROLLOUT not found — apply k8s/base first"
  curl -sf -o /dev/null -m 5 http://localhost/api/summary 2>/dev/null \
    || die "http://localhost is not serving — check Traefik and the IngressRoutes"
}

ensure_faulty_image() {
  local img="$REGISTRY/$ROLLOUT:$FAULTY"
  if docker exec "${CLUSTER}-control-plane" crictl images 2>/dev/null | grep -q "$ROLLOUT *$FAULTY"; then
    ok "$FAULTY already loaded in the cluster"
    return
  fi
  info "Building and loading $FAULTY"
  docker build --quiet --build-arg "VERSION=$FAULTY" -t "$img" "$REPO_ROOT/services/$ROLLOUT" >/dev/null
  kind load docker-image "$img" --name "$CLUSTER" >/dev/null 2>&1
  ok "$FAULTY loaded"
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
  # Applying the manifest strips the weights (arrays are replaced wholesale on a
  # custom resource) and the controller only rewrites them once a rollout runs.
  # Harmless while stable: both Services then select the same pods.
  echo "—"
}

wait_healthy() {
  for _ in $(seq 1 "${1:-40}"); do
    [ "$(kubectl argo rollouts status "$ROLLOUT" -n "$NS" --timeout 3s 2>/dev/null)" = "Healthy" ] && return 0
    sleep 3
  done
  return 1
}

restore() {
  info "Restoring the healthy baseline"
  # Re-applying the committed manifest also removes ERROR_RATE, which a strategic
  # merge patch cannot do (it merges env entries, it does not delete them).
  kubectl apply -f "$MANIFEST" >/dev/null
  sleep 2
  kubectl argo rollouts promote "$ROLLOUT" -n "$NS" --full >/dev/null 2>&1 || true
  wait_healthy 40 || die "could not restore the baseline — check: kubectl argo rollouts get rollout $ROLLOUT -n $NS"
  ok "$BASELINE stable again, weights $(weights)"
}

# --- run --------------------------------------------------------------------

preflight
ensure_faulty_image

info "Establishing the $BASELINE baseline"
kubectl apply -f "$MANIFEST" >/dev/null
sleep 2
kubectl argo rollouts promote "$ROLLOUT" -n "$NS" --full >/dev/null 2>&1 || true
wait_healthy 40 || die "could not reach a healthy baseline"
ok "$BASELINE stable, weights $(weights)"

( while true; do curl -s -o /dev/null -m 5 http://localhost/api/summary; sleep 0.2; done ) &
LOAD_PID=$!
# Otherwise the shell prints a job-termination notice when it is killed below.
disown "$LOAD_PID" 2>/dev/null || true
ok "traffic generator running"
echo

PREV_AR=$(kubectl get analysisrun -n "$NS" --sort-by=.metadata.creationTimestamp --no-headers 2>/dev/null | tail -1 | awk '{print $1}')

info "Deploying faulty $FAULTY (ERROR_RATE=$ERROR_RATE — 30% of its requests will fail)"
# JSON patch, not strategic merge: Rollout is a custom resource, and strategic
# merge is only supported for built-in types. The env entry is appended rather
# than the whole list replaced; the baseline step above re-applies the manifest,
# so ERROR_RATE is never present twice.
kubectl patch rollout "$ROLLOUT" -n "$NS" --type=json -p "[
  {\"op\": \"replace\", \"path\": \"/metadata/labels/version\", \"value\": \"$FAULTY\"},
  {\"op\": \"replace\", \"path\": \"/spec/template/spec/containers/0/image\", \"value\": \"$REGISTRY/$ROLLOUT:$FAULTY\"},
  {\"op\": \"add\", \"path\": \"/spec/template/spec/containers/0/env/-\", \"value\": {\"name\": \"ERROR_RATE\", \"value\": \"$ERROR_RATE\"}}
]" >/dev/null

echo
printf '  %-7s %-12s %-12s %s\n' "TIME" "WEIGHTS" "PHASE" "ANALYSIS"
start=$SECONDS
aborted=0
for _ in $(seq 1 60); do
  phase=$(kubectl get rollout "$ROLLOUT" -n "$NS" -o jsonpath='{.status.phase}' 2>/dev/null)
  read -r ar_name ar <<<"$(kubectl get analysisrun -n "$NS" --sort-by=.metadata.creationTimestamp --no-headers 2>/dev/null | tail -1 | awk '{print $1, $2}')"
  [ "$ar_name" = "$PREV_AR" ] && ar="(starting)"
  printf '  %-7s %-12s %-12s %s\n' "$((SECONDS-start))s" "$(weights)" "${phase:-?}" "${ar:-none}"
  if [ "$phase" = "Degraded" ]; then aborted=1; break; fi
  sleep 4
done

echo
if [ "$aborted" = "1" ]; then
  bad "Rollout aborted — the faulty release was rejected automatically."
  echo
  echo "  reason:"
  kubectl get rollout "$ROLLOUT" -n "$NS" -o jsonpath='{.status.message}' 2>/dev/null | fold -sw 74 | sed 's/^/    /'
  echo
  echo
  echo "  measurements taken by the analysis:"
  kubectl get analysisrun -n "$NS" --sort-by=.metadata.creationTimestamp -o json 2>/dev/null | python3 -c '
import sys, json
items = json.load(sys.stdin)["items"]
if not items: sys.exit()
for m in items[-1]["status"].get("metricResults", []):
    for mm in m.get("measurements", []):
        val = (mm.get("value") or "").strip("[]")
        try: val = "%.3f" % float(val)
        except ValueError: pass
        print("    %-11s %s" % (mm["phase"], val))
'
  echo
  echo "  traffic served during the abort:"
  curl -s -m 5 http://localhost/api/summary 2>/dev/null \
    | python3 -c 'import sys,json;d=json.load(sys.stdin);print("    "+" -> ".join(h["service"]+"@"+h["version"] for h in d["chain"]))' 2>/dev/null
else
  die "the rollout did not abort within the expected window — check the analysis and Prometheus"
fi

echo
kill "$LOAD_PID" 2>/dev/null || true; LOAD_PID=""
restore
