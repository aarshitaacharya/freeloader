# 0010: Observability with kube-prometheus-stack

**Status:** accepted (level 8)

## Decision

- Install `kube-prometheus-stack` (Helm, pinned) into a `monitoring` namespace, trimmed for kind: no Alertmanager, node-exporter, kube-state-metrics or control-plane scraping, 6 h retention.
- Each component gets a `Service` and a `ServiceMonitor` (gateway, ledger, guard, fl-cache). Prometheus selects ServiceMonitors and rules from every namespace.
- The dashboard is **generated** by `tools/dashboard/gen.py` into `deploy/k8s/monitoring/freeloader-dashboard.json` and loaded by Grafana's sidecar from a labelled ConfigMap. CI re-runs the generator and fails if the committed JSON differs.
- Alert rules live in `deploy/k8s/monitoring/alerts.yaml` as a `PrometheusRule`. They fire in Prometheus; with no Alertmanager there is nowhere to route them yet.
- The Grafana admin password is generated once into a cluster Secret (`make kind-grafana` prints it).

## Why

- A generator keeps 28 panels reviewable as code; hand-edited Grafana JSON is unreadable in diffs.
- Every panel query uses a metric the services really export. There is no quota *limit* gauge, so the quota row shows usage and the guard's exhausted flags, not a percentage of limit.
- Alertmanager is left out because there is no receiver to configure on a laptop.

## Known limits

- `FreeloaderTargetDown` cannot fire for a pod that was deleted (its `up` series disappears); it covers pods that exist but stop answering scrapes.
- Redpanda and Postgres are not scraped yet.
