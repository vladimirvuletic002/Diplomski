#!/usr/bin/env bash
#
# Local canary demo — a DEVELOPMENT AID for Phase 1, not the thesis demo.
#
# Runs metering-service v1.0.0 (stable) and v2.0.0 (canary) side by side behind a
# hand-driven traffic splitter, so the gateway dashboard can be seen splitting
# traffic between two versions before a Kubernetes cluster exists.
#
# What this is NOT: the real system promotes versions automatically. Argo Rollouts
# sets the traffic weight, advances it through its configured steps, queries
# Prometheus, and aborts on a bad release with no human involved (Phases 4 and 6).
# Here a person types the weight by hand and nothing rolls back on its own.
#
# Usage:
#   ./scripts/demo-local-canary.sh start          # start stable + healthy canary
#   ./scripts/demo-local-canary.sh start --bad    # canary with ERROR_RATE=0.3
#   ./scripts/demo-local-canary.sh weight 25      # send 25% of traffic to v2.0.0
#   ./scripts/demo-local-canary.sh status
#   ./scripts/demo-local-canary.sh stop
#
# Re-running `start` is safe: it stops anything already running first.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="$REPO_ROOT/.run"
BIN_DIR="$RUN_DIR/bin"
LOG_DIR="$RUN_DIR/log"
WEIGHT_FILE="$RUN_DIR/canary-weight"
MODE_FILE="$RUN_DIR/canary-mode"

STABLE_VERSION="v1.0.0"
CANARY_VERSION="v2.0.0"
AGGREGATION_VERSION="v1.1.0"
GATEWAY_VERSION="v1.0.0"

PORT_GATEWAY=8080
PORT_STABLE=8081
PORT_AGGREGATION=8082
PORT_CANARY=8083
PORT_SPLIT=8090

BAD_ERROR_RATE="0.3"

# --- helpers ----------------------------------------------------------------

info()  { printf '\033[0;36m==>\033[0m %s\n' "$1"; }
ok()    { printf '\033[0;32m  ok\033[0m %s\n' "$1"; }
warn()  { printf '\033[0;33m  !\033[0m  %s\n' "$1"; }
die()   { printf '\033[0;31merror:\033[0m %s\n' "$1" >&2; exit 1; }

# kill_port terminates whatever holds a port, so a stale run never blocks a new one.
kill_port() {
  local port=$1 pid
  pid="$(lsof -ti "tcp:$port" 2>/dev/null || true)"
  if [ -n "$pid" ]; then
    kill -TERM $pid 2>/dev/null || true
    for _ in $(seq 1 20); do
      lsof -ti "tcp:$port" >/dev/null 2>&1 || return 0
      sleep 0.1
    done
    kill -9 $pid 2>/dev/null || true
  fi
}

wait_for() {
  local url=$1 name=$2
  for _ in $(seq 1 100); do
    if curl -sf -o /dev/null "$url" 2>/dev/null; then
      ok "$name"
      return 0
    fi
    sleep 0.1
  done
  die "$name did not become ready — see $LOG_DIR/"
}

# The version is baked in at build time exactly as the Docker build will do it,
# so what runs here reports its version the same way the real image does.
build_service() {
  local dir=$1 version=$2 out=$3
  ( cd "$REPO_ROOT/services/$dir" \
    && go build -ldflags "-X main.version=$version" -o "$BIN_DIR/$out" . )
}

# --- commands ---------------------------------------------------------------

cmd_start() {
  local bad="${1:-}"

  command -v go >/dev/null    || die "go is not installed"
  command -v lsof >/dev/null  || die "lsof is not installed"
  python3 --version >/dev/null 2>&1 || die "python3 is not installed"

  cmd_stop_quiet
  mkdir -p "$BIN_DIR" "$LOG_DIR"

  info "Building services (version baked in via -ldflags)"
  build_service metering-service    "$STABLE_VERSION"      "metering-$STABLE_VERSION"
  build_service metering-service    "$CANARY_VERSION"      "metering-$CANARY_VERSION"
  build_service aggregation-service "$AGGREGATION_VERSION" "aggregation"
  build_service gateway             "$GATEWAY_VERSION"     "gateway"
  ok "built into ${BIN_DIR#"$REPO_ROOT"/}"

  # Start with all traffic on stable; the canary is live but receives nothing,
  # which is where a real canary rollout begins too.
  echo 0 > "$WEIGHT_FILE"

  info "Starting metering-service $STABLE_VERSION (stable) on :$PORT_STABLE"
  PORT=$PORT_STABLE "$BIN_DIR/metering-$STABLE_VERSION" > "$LOG_DIR/metering-stable.log" 2>&1 &
  wait_for "http://localhost:$PORT_STABLE/healthz" "stable $STABLE_VERSION"

  if [ "$bad" = "--bad" ]; then
    # Same binary as the healthy canary — only the configuration differs. That is
    # the point: a faulty release is a config change, not a different build.
    info "Starting metering-service $CANARY_VERSION (canary, ERROR_RATE=$BAD_ERROR_RATE) on :$PORT_CANARY"
    PORT=$PORT_CANARY ERROR_RATE=$BAD_ERROR_RATE \
      "$BIN_DIR/metering-$CANARY_VERSION" > "$LOG_DIR/metering-canary.log" 2>&1 &
    echo "bad" > "$MODE_FILE"
  else
    info "Starting metering-service $CANARY_VERSION (canary, healthy) on :$PORT_CANARY"
    PORT=$PORT_CANARY "$BIN_DIR/metering-$CANARY_VERSION" > "$LOG_DIR/metering-canary.log" 2>&1 &
    echo "healthy" > "$MODE_FILE"
  fi
  wait_for "http://localhost:$PORT_CANARY/healthz" "canary $CANARY_VERSION"

  info "Starting traffic splitter on :$PORT_SPLIT"
  ( cd "$REPO_ROOT" && PORT=$PORT_SPLIT \
      STABLE_URL="http://localhost:$PORT_STABLE" \
      CANARY_URL="http://localhost:$PORT_CANARY" \
      WEIGHT_FILE="$WEIGHT_FILE" \
      python3 scripts/split.py > "$LOG_DIR/split.log" 2>&1 & )
  wait_for "http://localhost:$PORT_SPLIT/" "splitter"

  info "Starting aggregation-service $AGGREGATION_VERSION on :$PORT_AGGREGATION"
  PORT=$PORT_AGGREGATION METERING_URL="http://localhost:$PORT_SPLIT" \
    "$BIN_DIR/aggregation" > "$LOG_DIR/aggregation.log" 2>&1 &
  wait_for "http://localhost:$PORT_AGGREGATION/healthz" "aggregation-service"

  info "Starting gateway $GATEWAY_VERSION on :$PORT_GATEWAY"
  PORT=$PORT_GATEWAY AGGREGATION_URL="http://localhost:$PORT_AGGREGATION" \
    "$BIN_DIR/gateway" > "$LOG_DIR/gateway.log" 2>&1 &
  wait_for "http://localhost:$PORT_GATEWAY/healthz" "gateway"

  echo
  ok "Dashboard: http://localhost:$PORT_GATEWAY"
  echo
  echo "  All traffic is on $STABLE_VERSION. Shift it to the canary with:"
  echo "    ./scripts/demo-local-canary.sh weight 10"
  echo "    ./scripts/demo-local-canary.sh weight 25"
  echo "    ./scripts/demo-local-canary.sh weight 50"
  echo "    ./scripts/demo-local-canary.sh weight 100"
  echo
}

cmd_weight() {
  local pct="${1:-}"
  [ -n "$pct" ] || die "usage: $0 weight <0-100>"
  case "$pct" in
    ''|*[!0-9]*) die "weight must be a whole number between 0 and 100, got '$pct'" ;;
  esac
  [ "$pct" -le 100 ] || die "weight must be between 0 and 100, got '$pct'"
  [ -f "$WEIGHT_FILE" ] || die "nothing running — start it with: $0 start"

  echo "$pct" > "$WEIGHT_FILE"
  ok "$pct% of traffic -> metering-service $CANARY_VERSION (canary), $((100 - pct))% -> $STABLE_VERSION"
  echo "  The splitter re-reads the weight per request, so the dashboard shifts within a few seconds."
}

cmd_status() {
  local running=0
  printf '%-22s %-7s %s\n' "COMPONENT" "PORT" "STATE"
  for entry in \
    "gateway:$PORT_GATEWAY" \
    "aggregation-service:$PORT_AGGREGATION" \
    "metering $STABLE_VERSION:$PORT_STABLE" \
    "metering $CANARY_VERSION:$PORT_CANARY" \
    "splitter:$PORT_SPLIT"
  do
    local name="${entry%:*}" port="${entry##*:}"
    if lsof -ti "tcp:$port" >/dev/null 2>&1; then
      printf '%-22s %-7s %s\n' "$name" "$port" "running"
      running=1
    else
      printf '%-22s %-7s %s\n' "$name" "$port" "-"
    fi
  done

  echo
  if [ "$running" = "1" ] && [ -f "$WEIGHT_FILE" ]; then
    local pct mode
    pct="$(cat "$WEIGHT_FILE" 2>/dev/null || echo '?')"
    mode="$(cat "$MODE_FILE" 2>/dev/null || echo '?')"
    echo "canary weight: ${pct}% to $CANARY_VERSION (canary is $mode)"
  else
    echo "nothing running — start it with: $0 start"
  fi
}

cmd_stop_quiet() {
  for port in $PORT_GATEWAY $PORT_AGGREGATION $PORT_STABLE $PORT_CANARY $PORT_SPLIT; do
    kill_port "$port"
  done
  rm -f "$WEIGHT_FILE" "$MODE_FILE"
}

cmd_stop() {
  info "Stopping"
  cmd_stop_quiet
  ok "all ports free"
}

# --- entrypoint -------------------------------------------------------------

case "${1:-}" in
  start)  shift; cmd_start "${1:-}" ;;
  weight) shift; cmd_weight "${1:-}" ;;
  status) cmd_status ;;
  stop)   cmd_stop ;;
  *)
    cat <<EOF
Local canary demo — runs metering-service $STABLE_VERSION and $CANARY_VERSION side by side.

  $0 start          start stable + healthy canary (all traffic on stable)
  $0 start --bad    same, but the canary fails 30% of its requests
  $0 weight <0-100> send this % of traffic to the canary
  $0 status         show what is running and the current weight
  $0 stop           stop everything

This is a Phase 1 development aid. In the real system Argo Rollouts sets the
weight and advances it automatically, and rolls back on its own.
EOF
    exit 1
    ;;
esac
