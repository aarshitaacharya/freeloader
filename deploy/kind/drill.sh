#!/usr/bin/env bash
# Delete a gateway pod 20 s into a 60 s load test. Passes only if every request succeeded.
# Needs the mock stack:  make kind-up-mock
set -euo pipefail
cd "$(dirname "$0")/../.."
K="kubectl --context kind-freeloader -n freeloader"
RPS=${RPS:-50}

name="drill-$(date +%s)"
key=$($K exec deploy/gateway -- /usr/local/bin/mkapp --name "$name" --rpm 0 --tokens-per-day 0 --cache-ttl 0 | grep -o 'fl_[0-9a-f]*' | head -1)
[ -n "$key" ] || { echo "could not create an app"; exit 1; }

victim=$($K get pods -l app=gateway -o jsonpath='{.items[0].metadata.name}')
echo "gateway pods running: $($K get pods -l app=gateway --field-selector=status.phase=Running --no-headers | wc -l | tr -d ' '); will delete $victim at t=20s"
(sleep 20; echo "t=20s deleting $victim"; $K delete pod "$victim" --wait=false >/dev/null) &

status=0
(cd gateway && go run ./cmd/loadgen --url http://127.0.0.1:8090 --key "$key" --duration 60s --rps "$RPS") || status=$?
wait
$K get pods -l app=gateway --no-headers
exit $status
