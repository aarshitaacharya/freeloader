# 0009: Kubernetes layout on kind

**Status:** accepted (level 7)

## Decision

- Kustomize `base` plus `dev` and `mock` overlays under `deploy/k8s/`. Redpanda comes from its Helm chart (single broker, TLS off). Everything else is plain manifests.
- Secrets and config are **not** in the repo. `deploy/kind/up.sh` builds `freeloader-secrets` (provider keys from `.env`, generated Postgres password and admin token, `DATABASE_URL`) and the config maps from local files. Re-running keeps the generated values.
- `up.sh` applies in two phases using the `tier=app` label: data layer and the migrate and topic jobs first, then gateway, ledger and guard. The services never start against a missing schema.
- Gateway: 3 replicas, `maxUnavailable: 0` rollouts, a PodDisruptionBudget with `minAvailable: 2`, a 5 s `preStop` sleep and `terminationGracePeriodSeconds: 40`.
- fl-cache and guard: one replica each, `Recreate` strategy.

## Why

- **preStop sleep.** On pod deletion the endpoint is removed from the Service and SIGTERM is sent at the same moment, and routers learn about the removal a moment later. Sleeping 5 s first keeps the pod serving until then. It is the native `lifecycle.preStop.sleep` action because the image is distroless and has no `sleep` binary. **Measured:** the pod-kill drill at 50 req/s passes with and without it (3000/3000 both times), because ingress-nginx retries a refused connection on another pod. So at this load it is insurance, not the thing that makes the drill pass; it matters for clients that reach pods without a retrying proxy in front.
- **One fl-cache.** Its data is disposable (0003) and the gateway fails open, so a restart costs a reset of rate-limit counters, not an outage. Sharding or replicating it would add a consistency problem the project does not need yet.
- **One guard.** It evaluates all providers from one view of the counters; a second replica would only repeat the same flags.
- **Secrets from a script, not a manifest.** The alternative (sealed-secrets, SOPS) is the right answer for a shared cluster, but is more machinery than a local kind cluster justifies. The repo only has to guarantee that nothing sensitive is committed.

## Known limits

- Images are built locally and loaded into kind (`imagePullPolicy: IfNotPresent`). There is no registry.
- The `/admin` ingress route points at the gateway, which has no admin API until level 9.
- Redpanda has a single broker, so topic replication factor is 1.
