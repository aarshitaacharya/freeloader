#!/usr/bin/env bash
# In-process store microbenchmark: 1 shard vs 64 shards across thread counts.
# Runs inside the fl-cache-dev container; start it with `make bench-store`.
set -euo pipefail
cd "$(dirname "$0")"
BIN=../cache/build/release/fl-store-bench
REPS=${REPS:-5}
SECONDS_PER_RUN=${SECONDS_PER_RUN:-3}
mkdir -p raw
echo "shards,threads,rep,seconds,total_ops,ops_per_sec" > raw/store.csv
for rep in $(seq "$REPS"); do
  for threads in 1 2 4 8 16; do
    for shards in 1 64; do
      row=$($BIN --shards $shards --threads $threads --seconds $SECONDS_PER_RUN)
      echo "${row%%,*},$(echo "$row" | cut -d, -f2),$rep,$(echo "$row" | cut -d, -f3-)" >> raw/store.csv
    done
  done
  echo "rep $rep/$REPS done" >&2
done
