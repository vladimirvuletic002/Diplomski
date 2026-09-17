#!/usr/bin/env bash
#
# Installs MinIO and Velero for the backup/restore demonstration.
#
#   ./scripts/setup-velero.sh          install
#   ./scripts/setup-velero.sh status   show what is installed
#   ./scripts/setup-velero.sh uninstall
#
# Kept out of bootstrap.sh: optional, and it costs ~400 MiB and two minutes that
# most cluster rebuilds do not need.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

VELERO_VERSION="v1.18.2"          # matches the pinned CLI
VELERO_AWS_PLUGIN="v1.14.2"       # the v1.14.x line pairs with Velero v1.18.x
BUCKET="velero"
# Resolved by in-cluster DNS; MinIO has no route out of the cluster.
S3_URL="http://minio.velero.svc.cluster.local:9000"
CREDS_FILE="$REPO_ROOT/k8s/velero/credentials-velero"

info() { printf '\033[0;36m==>\033[0m %s\n' "$1"; }
ok()   { printf '\033[0;32m  ok\033[0m %s\n' "$1"; }
warn() { printf '\033[0;33m  !\033[0m  %s\n' "$1"; }
die()  { printf '\033[0;31merror:\033[0m %s\n' "$1" >&2; exit 1; }

preflight() {
  for t in kubectl velero; do
    command -v "$t" >/dev/null 2>&1 || die "$t is not installed"
  done
  kubectl get --raw /healthz >/dev/null 2>&1 \
    || die "the cluster is not reachable — run: ./scripts/bootstrap.sh up"
}

install_minio() {
  info "Installing MinIO (object storage for backups)"
  kubectl apply -f "$REPO_ROOT/k8s/velero/minio.yaml" >/dev/null
  for _ in $(seq 1 45); do
    if kubectl get pods -n velero -l app=minio \
         -o jsonpath='{.items[*].status.conditions[?(@.type=="Ready")].status}' 2>/dev/null \
         | grep -q True; then
      ok "MinIO ready at $S3_URL (bucket: $BUCKET)"
      return 0
    fi
    sleep 4
  done
  die "MinIO did not become ready — check: kubectl get pods -n velero"
}

write_credentials() {
  # Velero reads S3 credentials from a file. Written here from the same values
  # the MinIO Secret holds, and git-ignored.
  local user pass
  user=$(kubectl get secret minio-credentials -n velero -o jsonpath='{.data.root-user}' | base64 -d)
  pass=$(kubectl get secret minio-credentials -n velero -o jsonpath='{.data.root-password}' | base64 -d)
  umask 077
  cat > "$CREDS_FILE" <<EOF
[default]
aws_access_key_id=$user
aws_secret_access_key=$pass
EOF
}

install_velero() {
  if kubectl get deploy velero -n velero >/dev/null 2>&1; then
    info "Velero already installed — leaving it in place"
    return 0
  fi
  info "Installing Velero $VELERO_VERSION (plugin $VELERO_AWS_PLUGIN)"
  write_credentials
  velero install \
    --provider aws \
    --plugins "velero/velero-plugin-for-aws:$VELERO_AWS_PLUGIN" \
    --bucket "$BUCKET" \
    --secret-file "$CREDS_FILE" \
    --use-volume-snapshots=false \
    --use-node-agent=false \
    --backup-location-config "region=minio,s3ForcePathStyle=true,s3Url=$S3_URL" \
    --wait >/dev/null
  ok "Velero installed"
}

# Stuck Unavailable usually means a wrong s3Url or a missing bucket — backups
# then fail confusingly rather than loudly.
verify_backup_location() {
  info "Checking the backup storage location"
  for _ in $(seq 1 30); do
    phase=$(kubectl get backupstoragelocation default -n velero \
              -o jsonpath='{.status.phase}' 2>/dev/null || true)
    if [ "$phase" = "Available" ]; then
      ok "backup storage location Available"
      return 0
    fi
    sleep 4
  done
  kubectl get backupstoragelocation -n velero 2>/dev/null | sed 's/^/    /'
  die "the backup storage location did not become Available — check: kubectl logs -n velero deploy/velero"
}

cmd_status() {
  preflight
  if ! kubectl get ns velero >/dev/null 2>&1; then
    echo "  velero namespace: absent (run: $0)"
    return 0
  fi
  kubectl get pods -n velero --no-headers 2>/dev/null | awk '{printf "  %-40s %-6s %s\n", $1,$2,$3}'
  echo
  echo "  backup location: $(kubectl get backupstoragelocation default -n velero -o jsonpath='{.status.phase}' 2>/dev/null || echo 'n/a')"
  echo "  backups:"
  velero backup get 2>/dev/null | tail -n +2 | awk '{printf "    %-32s %s\n", $1, $2}' || echo "    none"
}

cmd_uninstall() {
  preflight
  info "Removing Velero and MinIO"
  velero uninstall --force >/dev/null 2>&1 || true
  kubectl delete -f "$REPO_ROOT/k8s/velero/minio.yaml" --ignore-not-found >/dev/null 2>&1 || true
  rm -f "$CREDS_FILE"
  ok "removed"
}

case "${1:-install}" in
  install)
    preflight
    install_minio
    install_velero
    verify_backup_location
    echo
    ok "Ready. Run the demonstration with: ./scripts/demo-backup-restore.sh"
    ;;
  status)    cmd_status ;;
  uninstall) cmd_uninstall ;;
  *)
    echo "usage: $0 [install|status|uninstall]"
    exit 1
    ;;
esac
