#!/usr/bin/env bash
# Network benchmark: fl-cache (1 shard / 64 shards) vs Redis, using redis-benchmark.
# Runs inside the fl-cache-dev container; start it with `make bench`.
#
#   REPS=5 CLIENTS="1 8 32 128" ./run.sh        (defaults shown)
#
# Writes bench/raw/results.csv and bench/raw/env.txt.
set -euo pipefail
cd "$(dirname "$0")"

FL=../cache/build/release/fl-cache
OUT=raw
PORT=6500
REPS=${REPS:-5}
CLIENTS=${CLIENTS:-"1 8 32 128"}
KEYSPACE=100000
PAYLOAD=64
SERVER_CPUS=0-3     # every server gets the same 4 vCPUs
CLIENT_CPUS=8-15    # load generator is kept off the server's CPUs
CONFIGS=${CONFIGS:-"fl-64shards fl-1shard redis redis-io4"}

mkdir -p "$OUT"
CSV=$OUT/results.csv
echo "config,clients,test,rep,rps,avg_ms,p50_ms,p95_ms,p99_ms,max_ms" > "$CSV"

SERVER_PID=""
start_server() {
  case "$1" in
    fl-64shards) taskset -c $SERVER_CPUS $FL --port $PORT --threads 4 --shards 64 --metrics-port 0 2>/dev/null & ;;
    fl-1shard)   taskset -c $SERVER_CPUS $FL --port $PORT --threads 4 --shards 1  --metrics-port 0 2>/dev/null & ;;
    redis)       taskset -c $SERVER_CPUS redis-server --port $PORT --save "" --appendonly no >/dev/null 2>&1 & ;;
    redis-io4)   taskset -c $SERVER_CPUS redis-server --port $PORT --save "" --appendonly no \
                   --io-threads 4 --io-threads-do-reads yes >/dev/null 2>&1 & ;;
    *) echo "unknown config $1" >&2; exit 1 ;;
  esac
  SERVER_PID=$!
  for _ in $(seq 50); do
    [ "$(redis-cli -p $PORT PING 2>/dev/null)" = "PONG" ] && return 0
    sleep 0.1
  done
  echo "server $1 did not start" >&2; exit 1
}
stop_server() {
  kill -TERM "$SERVER_PID" 2>/dev/null || true
  wait "$SERVER_PID" 2>/dev/null || true
}
trap stop_server EXIT

preload() {  # every key the benchmark can touch exists, so GETs always hit
  python3 -I preload.py $PORT $KEYSPACE $PAYLOAD
}

bench() {  # bench <clients> <test>  -> prints CSV data line
  local c=$1 t=$2 n threads
  n=500000; [ "$c" -le 8 ] && n=300000; [ "$c" -le 1 ] && n=50000
  threads=$(( c < 8 ? c : 8 ))
  taskset -c $CLIENT_CPUS redis-benchmark -p $PORT -c "$c" -n "$n" -d $PAYLOAD -r $KEYSPACE \
      -t "$t" --threads "$threads" --csv 2>/dev/null \
    | tr -d '"' | grep -E "^(SET|GET)," || true
}

{
  echo "date: $(date -u +%FT%TZ)"
  echo "kernel: $(uname -srm)"
  echo "vcpus: $(nproc)"
  echo "cpu: $(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2- | sed 's/^ //')"
  echo "mem_mb: $(free -m | awk '/Mem:/ {print $2}')"
  echo "compiler: $(g++ --version | head -1)"
  echo "redis: $(redis-server --version)"
  echo "fl-cache git: $(git -C .. rev-parse --short HEAD 2>/dev/null || echo unknown)"
  echo "params: reps=$REPS clients=[$CLIENTS] keyspace=$KEYSPACE payload=${PAYLOAD}B server_cpus=$SERVER_CPUS client_cpus=$CLIENT_CPUS"
  echo "fl-cache: --threads 4 --metrics-port 0 (CMake Release build type)"
  echo "redis: --save '' --appendonly no (redis-io4 adds --io-threads 4 --io-threads-do-reads yes)"
} > "$OUT/env.txt"

for rep in $(seq "$REPS"); do
  for cfg in $CONFIGS; do
    echo "rep $rep/$REPS  $cfg" >&2
    start_server "$cfg"
    preload
    bench 32 get >/dev/null   # warm-up, discarded
    for c in $CLIENTS; do
      for t in set get; do
        line=$(bench "$c" "$t")
        [ -n "$line" ] || { echo "no result for $cfg c=$c $t" >&2; continue; }
        # test,rps,avg,min,p50,p95,p99,max  ->  config,clients,test,rep,rps,avg,p50,p95,p99,max
        echo "$line" | awk -F, -v cfg="$cfg" -v c="$c" -v rep="$rep" \
          '{ print cfg "," c "," tolower($1) "," rep "," $2 "," $3 "," $5 "," $6 "," $7 "," $8 }' >> "$CSV"
      done
    done
    sz=$(redis-cli -p $PORT DBSIZE | tr -dc 0-9)
    [ "$sz" = "$KEYSPACE" ] || echo "WARNING: $cfg DBSIZE=$sz (expected $KEYSPACE): benchmark key names may not match preload" >&2
    stop_server
  done
done
echo "wrote $CSV" >&2
