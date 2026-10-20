Medians over 5 runs; ranges are in the tables below. The VM is noisy (run-to-run spread is large), so
I only call a difference real when the min–max ranges do not overlap.

1. **Sharding removes real lock contention — in the store.** With one big lock, adding threads makes the
   store *slower*: 1.15M ops/s at 1 thread falling to 0.61M at 16. With 64 shards it scales from 1.32M to
   4.49M ops/s, **7.4× the single-lock throughput at 16 threads**. From 2 threads up, the two
   configurations' ranges do not overlap.
2. **Over the network, 1 shard vs 64 shards is not distinguishable with these runs.** At 1, 8 and 32 clients
   they are within noise. At 128 clients the medians favour 64 shards (SET 181k vs 166k req/s; GET 181k vs
   142k; p99 1.57 vs 1.85 ms SET, 1.68 vs 1.99 ms GET), but the ranges overlap, so I would not claim it.
   Likely reason (an inference, not tested): peak network load here is ≈0.18M ops/s, well under the
   ≈0.6–1M ops/s where a single lock saturates in the microbenchmark, so the lock is not the bottleneck
   yet. A faster machine, more worker threads, or pipelined load would be needed to see it end to end.
3. **At high concurrency fl-cache out-scales Redis in this setup.** At 128 clients (SET): fl-cache 181k req/s
   vs 43k for default single-threaded Redis (≈4.2×) and 90k for Redis with 4 I/O threads (≈2×); p99 1.57 ms
   vs 5.11 ms and 2.84 ms. At 32 clients: ≈3× default Redis. This is **not** a per-core or per-feature
   comparison: fl-cache ran 4 worker threads, default Redis one main thread, and Redis does far more.
4. **At low concurrency Redis is slightly ahead on latency.** At 1 and 8 clients throughput is the same within
   noise (client round-trip bound), but Redis's p99 is lower (SET, 1 client: 0.21 vs 0.29 ms; 8 clients:
   0.45 vs 0.69 ms). I have not investigated why (candidates: epoll wake-up and handoff costs, per-command
   timing for the histogram).

**What this does and doesn't support for a resume bullet.** Supported: "sharded locks raised in-process store
throughput 7.4× at 16 threads (0.61M → 4.49M ops/s)", and "reached 181k req/s at 128 clients vs 43k for
single-threaded Redis 7.0 on the same pinned CPUs". Not supported: a network-level "sharding cut p99 from X to
Y ms" (finding 2), or "within N% of Redis throughput" (fl-cache exceeded it at ≥32 clients, in a Docker Desktop VM).
