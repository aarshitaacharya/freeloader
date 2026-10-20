# fl-cache ownership: who owns what, and when it dies

Written for interview prep. The rule of thumb: **every resource has exactly one owner, ownership
moves only by `std::move`, and anything shared across threads is either immutable, atomic, or behind
a mutex that is never held across I/O.**

## Ownership table

| Object | Owned by | Created | Destroyed | Touched by |
|---|---|---|---|---|
| `UniqueFd` (any socket / epoll / eventfd) | whoever holds it (move-only) | `accept4`, `socket`, `epoll_create1`, `eventfd` | owner's destructor calls `close()` — **the only `close()` call in the codebase** | its owner's thread |
| `Server` | `main()` (stack) | before signal handlers are installed | after `run()` returns | `main` thread; `stop()` from a signal handler |
| listen socket | `Server` (`UniqueFd`) | `Server` constructor | `Server` destructor | the acceptor thread (the thread calling `run()`) |
| `Store` | `Server` (member) | `Server` constructor | `Server` destructor (after workers: member order) | all workers, the sweeper thread, the metrics thread (via snapshot) |
| `Shard` (mutex + map) | `Store` (`unique_ptr<Shard[]>`) | `Store` constructor | `Store` destructor | any thread, **only while holding that shard's mutex** |
| `Worker` | `Server` (`unique_ptr` in `workers_`) | `Server` constructor | `Server` destructor (joins its thread first) | acceptor calls `add_connection()`; everything else is the worker's own thread |
| worker `Epoll` + wake `eventfd` | `Worker` | `Worker` constructor | `Worker` destructor | worker thread (the acceptor only `write()`s the eventfd) |
| `pending_` handoff queue | `Worker` | `Worker` constructor | `Worker` destructor | acceptor pushes, worker pops — **only under `Worker::mu_`** |
| `Connection` | exactly one `Worker`, in `conns_` (`unique_ptr`) | worker thread, when it drains `pending_` | worker thread: peer closed / error / shutdown drain | **only its worker's thread** — no locks |
| a connection's `UniqueFd` | its `Connection` | moved in from `pending_` | `~Connection` | its worker's thread |
| `WorkerMetrics` | `Worker` (member) | `Worker` constructor | `Worker` destructor | worker thread writes (single writer); metrics/INFO threads read with relaxed atomics |
| sweeper thread | local `std::jthread` in `Server::run()` | `run()` | `run()` scope exit | calls `Store::expire_cycle()` |
| `MetricsServer` + its thread | `Server` (`unique_ptr`) / local `jthread` in `run()` | constructor / `run()` | destructor / `run()` scope exit | metrics thread; reads `Server::snapshot()` |

`new`/`delete` do not appear in application code: `make_unique` for owned objects, containers for
buffers, RAII wrappers for descriptors.

## Why connections need no locks

A connection is accepted by the acceptor thread, wrapped in a `UniqueFd`, and **moved** into one
worker's `pending_` queue (under that worker's mutex). The worker thread moves it out, builds a
`Connection`, and from then on is its only user. The acceptor never touches it again; no other
worker ever sees its fd. So `Connection` state (`in_`, `out_`, flags) is thread-confined.
The only shared mutable data in the whole server is: the `Store` shards, `pending_`, atomics
(`stop_`, the key counter, metrics counters).

## Locking rules

1. A `Store` operation locks **one** shard and never holds a second shard lock. No lock ordering
   problem is possible, so no deadlock is possible among shards.
2. No lock is held across a syscall. `Worker::mu_` guards only a `vector::push_back` / `swap`.
   Shard locks are held only for map operations (and the copy of a ≤512 MB value in the worst case).
3. Counters shared between threads are `std::atomic`. Per-command counters are *per worker*
   (single writer, cache-line aligned) so the hot path never contends on a shared counter; a scrape
   sums them. The global key count is the one shared atomic on the write path (touched on insert/delete,
   not on overwrite).

## Lifetime of a request

1. `epoll_wait` returns the connection's fd (edge-triggered).
2. `Connection::service()` reads until `EAGAIN` (bounded by a per-call read budget; if the budget is
   exhausted the worker re-queues the connection without waiting for an event, so one busy client
   can't starve the others).
3. `parse_command` → `execute` (locks one shard) → reply appended to `out_`.
4. `send` until done or `EAGAIN`. If the socket is full the reply stays in `out_`; the next
   `EPOLLOUT` edge resumes. If `out_` exceeds 1 MiB the connection stops *executing* pipelined
   commands until it drains (backpressure against slow readers).

## Lifetime of the process (SIGTERM)

1. The handler stores `true` into `Server::stop_` (a lock-free atomic — async-signal-safe) and does nothing else.
2. The acceptor's `poll` loop (100 ms timeout) sees it and leaves `run()`'s loop.
3. A scope-exit guard in `run()` re-asserts the flag and `join()`s every worker. This guard also runs if
   the acceptor throws, so threads are never left running.
4. Each worker sees the flag within 100 ms and **drains**: it discards not-yet-registered connections,
   flushes every connection's already-produced replies, and exits when all output is flushed or after
   2 s, whichever is first. Destroying each `Connection` closes its fd.
5. The sweeper and metrics threads see the flag and exit; `run()` returns; `main` returns 0.

**What "drain" does and does not promise:** replies that were already produced are delivered. Commands a
client sent that this server had not yet read/executed are dropped. That is the same contract a Kubernetes
`SIGTERM` → `terminationGracePeriodSeconds` expects: finish what you've said you'd do, then go away.

## Where this can bite you

- `Server::stop()` is called from a signal handler via a raw pointer in `main.cpp`. The pointer must
  not outlive the `Server` (see `g_server` handling in `main`).
- A `Connection` holds *references* to `Store`, the `InfoFn`, and its worker's `WorkerMetrics`. They
  outlive every connection because `Server`'s members are declared so that workers are destroyed before
  `Store` and `info_`. Reordering the members in `server.hpp` would turn this into a use-after-free.
- The `--max-keys` cap is global but eviction is per shard, so the real key count can exceed the cap by up
  to one key per shard transiently (see `Store::insert_new`).
