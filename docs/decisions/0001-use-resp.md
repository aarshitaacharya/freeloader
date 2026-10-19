# ADR 0001: fl-cache speaks RESP2

**Status:** accepted

## Context
The gateway needs a shared store for the response cache, rate-limit counters and provider flags.
A real team would run Redis. We are writing our own server as a deliberate systems exercise.

## Decision
fl-cache implements a subset of the Redis wire protocol (RESP2) instead of a custom protocol.

## Consequences
- Any Redis client (including the Go gateway's) works unchanged, and `redis-cli` is our test tool.
- We can swap in real Redis and benchmark against it with `redis-benchmark`.
- We must handle partial reads, pipelining and malformed input as Redis does.
- Only a subset of commands is supported; unknown commands return an error.
