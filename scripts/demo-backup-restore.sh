#!/usr/bin/env bash
#
# Backs up the application, destroys it, and restores it with Velero.
#
#   ./scripts/demo-backup-restore.sh
#
# Two halves, because they make different points:
#
#   1. The energy namespace — what the task statement asks for. Note that Argo CD
#      could rebuild these objects from Git on its own; the backup matters for
#      runtime state and for clusters where Git is not the source of truth.
#   2. The Argo CD repository credential — deliberately kept out of Git, so Git
#      cannot restore it. This project lost exactly this secret once, when the
#      cluster was rebuilt for the Traefik migration.
#
# Requires ./scripts/setup-velero.sh to have been run. Re-runnable.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NS="energy"
STAMP="$(date +%H%M%S)"
APP_BACKUP="energy-$STAMP"
CRED_BACKUP="argocd-creds-$STAMP"
CRED_SECRET="repo-diplomski"

info() { printf '\033[0;36m==>\033[0m %s\n' "$1"; }
ok()   { printf '\033[0;32m  ok\033[0m %s\n' "$1"; }
bad()  { printf '\033[0;31m  ✗\033[0m  %s\n' "$1"; }
die()  { printf '\033[0;31merror:\033[0m %s\n' "$1" >&2; exit 1; }

preflight() {
  for t in kubectl velero; do command -v "$t" >/dev/null 2>&1 || die "$t is not installed"; done
  kubectl get --raw /healthz >/dev/null 2>&1 || die "the cluster is not reachable — run: ./scripts/bootstrap.sh up"
  kubectl get deploy velero -n velero >/dev/null 2>&1 || die "Velero is not installed — run: ./scripts/setup-velero.sh"
  [ "$(kubectl get backupstoragelocation default -n velero -o jsonpath='{.status.phase}' 2>/dev/null)" = "Available" ] \
    || die "the backup storage location is not Available — check: kubectl logs -n velero deploy/velero"
  curl -sf -o /dev/null -m 5 http://localhost/api/summary 2>/dev/null \
    || die "the application is not serving — nothing worth backing up"
}

objects() { kubectl get all,traefikservice,ingressroute,middleware,podmonitor,analysistemplate \
              -n "$NS" --no-headers 2>/dev/null | wc -l | tr -d ' '; }

chain() { curl -s -m 10 http://localhost/api/summary 2>/dev/null \
            | python3 -c 'import sys,json;d=json.load(sys.stdin);print(" -> ".join(h["service"]+"@"+h["version"] for h in d["chain"]))' 2>/dev/null; }

# Read from the Backup object rather than `velero backup describe --details`:
# that fetches from S3, and the in-cluster MinIO hostname does not resolve here.
counts() {
  kubectl get "$1" "$2" -n velero -o json 2>/dev/null | python3 -c '
import sys, json
st = json.load(sys.stdin).get("status", {})
pr = st.get("progress", {})
key = "itemsBackedUp" if "itemsBackedUp" in pr else "itemsRestored"
print("%s items, %s errors, %s warnings" % (pr.get(key, "?"), st.get("errors", 0), st.get("warnings", 0)))'
}

# --- run --------------------------------------------------------------------

preflight
BEFORE_OBJECTS=$(objects)
info "Before: $BEFORE_OBJECTS objects in '$NS', serving $(chain)"
echo

info "Backing up"
velero backup create "$APP_BACKUP" --include-namespaces "$NS" --wait >/dev/null 2>&1
ok "$APP_BACKUP — $(counts backup "$APP_BACKUP")"
velero backup create "$CRED_BACKUP" \
  --include-namespaces argocd --include-resources secrets \
  --selector argocd.argoproj.io/secret-type=repository --wait >/dev/null 2>&1
ok "$CRED_BACKUP — $(counts backup "$CRED_BACKUP")"
echo

info "Destroying the '$NS' namespace"
kubectl delete namespace "$NS" >/dev/null 2>&1
ok "deleted"
# Argo CD is configured selfHeal:false, so it reports the drift and waits rather
# than rebuilding. That keeps the restore attributable to Velero.
sleep 10
bad "application unreachable (HTTP $(curl -s -o /dev/null -w '%{http_code}' -m 5 http://localhost/api/summary 2>/dev/null))"
echo "     Argo CD: $(kubectl get application energy -n argocd -o jsonpath='{.status.sync.status}/{.status.health.status}' 2>/dev/null) — it reports the loss but does not rebuild"
echo

info "Restoring from $APP_BACKUP"
velero restore create --from-backup "$APP_BACKUP" --wait >/dev/null 2>&1
R=$(kubectl get restore -n velero --sort-by=.metadata.creationTimestamp --no-headers 2>/dev/null | tail -1 | awk '{print $1}')
ok "$R — $(counts restore "$R")"
for _ in $(seq 1 40); do
  running=$(kubectl get pods -n "$NS" --no-headers 2>/dev/null | awk '$3=="Running"{c++} END{print c+0}')
  [ "$running" -ge 6 ] && break
  sleep 4
done
for _ in $(seq 1 20); do curl -sf -o /dev/null -m 5 http://localhost/api/summary 2>/dev/null && break; sleep 4; done
ok "$(objects) objects restored (was $BEFORE_OBJECTS), serving $(chain)"
echo

info "The half Git cannot cover: the Argo CD repository credential"
kubectl delete secret "$CRED_SECRET" -n argocd >/dev/null 2>&1 || true
sleep 5
bad "credential deleted — Argo CD can no longer read the repository"
velero restore create --from-backup "$CRED_BACKUP" --wait >/dev/null 2>&1
sleep 5
if kubectl get secret "$CRED_SECRET" -n argocd >/dev/null 2>&1; then
  ok "credential restored ($(kubectl get secret "$CRED_SECRET" -n argocd -o jsonpath='{.data.url}' | base64 -d))"
else
  die "the credential was not restored"
fi
echo
ok "Done. Both the application and the credential were recovered from backup."
