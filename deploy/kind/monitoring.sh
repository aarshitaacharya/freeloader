#!/usr/bin/env bash
# Prometheus + Grafana (kube-prometheus-stack) with Freeloader's ServiceMonitors, alert rules
# and provisioned dashboard. Needs the cluster from up.sh.
set -euo pipefail
cd "$(dirname "$0")/../.."
export PATH="$PWD/.bin:$PATH"
K="kubectl --context kind-freeloader"
NS=monitoring

$K create namespace $NS --dry-run=client -o yaml | $K apply -f - >/dev/null
# Grafana's admin password is generated once and kept in the cluster; see `make kind-grafana`.
if ! $K -n $NS get secret grafana-admin >/dev/null 2>&1; then
  $K -n $NS create secret generic grafana-admin --from-literal=user=admin --from-literal=password="$(openssl rand -hex 12)" >/dev/null
fi

helm repo add prometheus-community https://prometheus-community.github.io/helm-charts >/dev/null 2>&1 || true
helm repo update prometheus-community >/dev/null
helm upgrade --install kps prometheus-community/kube-prometheus-stack -n $NS \
  -f deploy/k8s/monitoring/values.yaml --version "${KPS_VERSION:-92.11.0}" --wait --timeout 8m >/dev/null

# Services must carry the labels the ServiceMonitors select, so refresh the app manifests too.
$K apply -k deploy/k8s/overlays/"${OVERLAY:-dev}" >/dev/null
$K apply -k deploy/k8s/monitoring >/dev/null
echo "monitoring up. Prometheus/Grafana: make kind-grafana  (Grafana on http://127.0.0.1:3000, Prometheus on :9090)"
