#!/usr/bin/env bash
# Build everything and deploy it to a local kind cluster, from nothing:
#   deploy/kind/up.sh          real providers (keys from .env)
#   deploy/kind/up.sh --mock   offline, with the fake provider
set -euo pipefail
cd "$(dirname "$0")/../.."
export PATH="$PWD/.bin:$PATH"
MOCK=0; [ "${1:-}" = "--mock" ] && MOCK=1
NS=freeloader
INGRESS_NGINX=controller-v1.13.1
K="kubectl --context kind-freeloader"

for t in docker kind kubectl helm; do
  command -v "$t" >/dev/null || { echo "missing $t (kind and helm: see Makefile target kind-tools)"; exit 1; }
done

# 1. cluster
if ! kind get clusters 2>/dev/null | grep -qx freeloader; then
  kind create cluster --config deploy/kind/cluster.yaml
fi

# 2. images (same Dockerfiles as Compose), loaded straight into the nodes
RESTART=()   # deployments whose image changed: the :dev tag never changes, so they must be restarted
build() {    # build NAME DEPLOYMENT [docker build args...]
  local name=$1 dep=$2; shift 2
  local before; before=$(docker images -q "$name:dev" 2>/dev/null || true)
  docker build -q -t "$name:dev" "$@" >/dev/null
  [ "$(docker images -q "$name:dev")" = "$before" ] || RESTART+=("deploy/$dep")
}
build fl-gateway gateway gateway
build fl-ledger  ledger  ledger
build fl-guard   guard   guard
build fl-cache   fl-cache -f cache/Dockerfile .
build fl-panel   panel   panel
kind load docker-image --name freeloader fl-gateway:dev fl-ledger:dev fl-guard:dev fl-cache:dev fl-panel:dev

# 3. ingress controller
$K apply -f "https://raw.githubusercontent.com/kubernetes/ingress-nginx/${INGRESS_NGINX}/deploy/static/provider/kind/deploy.yaml" >/dev/null
# hostPort 80 only reaches the host on the node that has the port mapping (the control plane)
$K -n ingress-nginx patch deploy/ingress-nginx-controller --type merge \
  -p '{"spec":{"template":{"spec":{"nodeSelector":{"ingress-ready":"true"}}}}}' >/dev/null
$K -n ingress-nginx rollout status deploy/ingress-nginx-controller --timeout=180s

# 4. secrets and config, built from local files so nothing sensitive is in the repo
$K create namespace $NS --dry-run=client -o yaml | $K apply -f - >/dev/null
existing() { $K -n $NS get secret freeloader-secrets -o "jsonpath={.data.$1}" 2>/dev/null | base64 -d 2>/dev/null || true; }
PGPASS=$(existing POSTGRES_PASSWORD); [ -n "$PGPASS" ] || PGPASS=$(openssl rand -hex 16)
ADMIN=$(existing ADMIN_TOKEN);        [ -n "$ADMIN" ]  || ADMIN=$(openssl rand -hex 24)
envval() { [ -f .env ] && grep -E "^$1=" .env | tail -1 | cut -d= -f2- || true; }
args=(--from-literal=POSTGRES_PASSWORD="$PGPASS" --from-literal=ADMIN_TOKEN="$ADMIN"
      --from-literal=DATABASE_URL="postgres://freeloader:${PGPASS}@postgres:5432/freeloader?sslmode=disable")
if [ $MOCK = 1 ]; then
  args+=(--from-literal=MOCK_API_KEY=mock-key)
  CONFIG=config/providers.mock.yaml
else
  for k in GROQ_API_KEY GEMINI_API_KEY OPENROUTER_API_KEY; do args+=(--from-literal="$k=$(envval $k)"); done
  CONFIG=config/providers.yaml; [ -f "$CONFIG" ] || CONFIG=config/providers.example.yaml
fi
$K -n $NS create secret generic freeloader-secrets "${args[@]}" --dry-run=client -o yaml | $K apply -f - >/dev/null
CFG_CHANGED=$($K -n $NS create configmap freeloader-config --from-file=providers.yaml="$CONFIG" --dry-run=client -o yaml | $K apply -f - | grep -c configured || true)
$K -n $NS create configmap freeloader-migrations --from-file=migrations --dry-run=client -o yaml | $K apply -f - >/dev/null
if [ $MOCK = 1 ]; then
  $K -n $NS create configmap mock-provider-code --from-file=tools/mock-provider/mock_provider.py --dry-run=client -o yaml | $K apply -f - >/dev/null
fi

# 5. Redpanda (single broker, Helm)
helm repo add redpanda https://charts.redpanda.com >/dev/null 2>&1 || true
helm repo update redpanda >/dev/null
helm upgrade --install redpanda redpanda/redpanda -n $NS -f deploy/k8s/redpanda-values.yaml --wait --timeout 8m >/dev/null

# 6. data layer and migrations first, then the services that need the schema
OVERLAY=deploy/k8s/overlays/dev; [ $MOCK = 1 ] && OVERLAY=deploy/k8s/overlays/mock
FRESH=0; $K -n $NS get deploy/gateway >/dev/null 2>&1 || FRESH=1
$K delete job migrate topic-init -n $NS --ignore-not-found >/dev/null   # Jobs are immutable; rerun them
$K apply -k $OVERLAY -l 'tier!=app' >/dev/null
$K -n $NS rollout status statefulset/postgres --timeout=180s
$K -n $NS wait --for=condition=complete job/migrate job/topic-init --timeout=240s
$K apply -k $OVERLAY >/dev/null
# config is read at startup, so a changed providers.yaml needs a restart; so does a rebuilt image
[ "$CFG_CHANGED" = 0 ] || RESTART+=(deploy/gateway deploy/guard)
if [ "$FRESH" = 0 ] && [ ${#RESTART[@]} -gt 0 ]; then   # a first deploy already runs the new images
  printf '%s\n' "${RESTART[@]}" | sort -u | xargs $K -n $NS rollout restart >/dev/null
fi
for d in fl-cache gateway ledger guard panel; do $K -n $NS rollout status deploy/$d --timeout=240s; done

echo
echo "up. gateway: http://127.0.0.1:8090/v1   panel: http://127.0.0.1:8090/   (create an app: make kind-app NAME=demo)"
echo "admin token: kubectl --context kind-freeloader -n $NS get secret freeloader-secrets -o jsonpath='{.data.ADMIN_TOKEN}' | base64 -d"
