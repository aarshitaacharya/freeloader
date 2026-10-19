#pragma once
#include <array>
#include <atomic>
#include <cstddef>
#include <cstdint>
#include <string>

namespace fl {

// Commands we label individually. Anything else is "UNKNOWN", so a client
// can't create unbounded label cardinality by inventing command names.
enum class Cmd : uint8_t { Ping, Get, Set, Del, Incrby, Expire, Ttl, Dbsize, Info, Unknown };
constexpr size_t kCmdCount = 10;
const char* cmd_name(Cmd c);

// Upper bounds (seconds) of the command-latency histogram buckets; +Inf is implicit.
// This times execute() only (parse -> store -> reply encode), not socket I/O.
constexpr std::array<double, 12> kLatencyBounds = {1e-6, 2.5e-6, 5e-6, 1e-5, 2.5e-5, 5e-5,
                                                   1e-4, 2.5e-4, 5e-4, 1e-3, 1e-2,  1e-1};
constexpr size_t kBucketCount = kLatencyBounds.size() + 1;  // + the +Inf bucket

// Per-worker counters. Exactly one thread (the worker) writes; the scraper
// reads. Single-writer means plain load+store (no locked read-modify-write)
// and one cache line group per worker means no false sharing between workers.
struct alignas(64) WorkerMetrics {
  std::array<std::atomic<uint64_t>, kCmdCount> commands{};
  std::array<std::atomic<uint64_t>, kBucketCount> buckets{};  // non-cumulative
  std::atomic<uint64_t> sum_ns{0};

  void observe(Cmd c, uint64_t ns);
  uint64_t total_commands() const;
};

// Aggregate across workers + store, ready to render.
struct MetricsSnapshot {
  std::array<uint64_t, kCmdCount> commands{};
  std::array<uint64_t, kBucketCount> buckets{};  // non-cumulative
  uint64_t sum_ns = 0;
  int64_t connections = 0;
  uint64_t keys = 0;
  uint64_t evictions = 0;
  uint64_t expired = 0;

  void add(const WorkerMetrics& w);
};

// Prometheus text exposition format 0.0.4.
std::string render_prometheus(const MetricsSnapshot& s);

}  // namespace fl
