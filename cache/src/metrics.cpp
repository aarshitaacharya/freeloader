#include "fl/metrics.hpp"

#include <cstdio>

namespace fl {
namespace {
constexpr const char* kNames[kCmdCount] = {"PING", "GET",    "SET", "DEL", "INCRBY",
                                           "EXPIRE", "TTL", "DBSIZE", "INFO", "UNKNOWN"};

void bump(std::atomic<uint64_t>& a, uint64_t n) {
  a.store(a.load(std::memory_order_relaxed) + n, std::memory_order_relaxed);  // single writer
}

std::string num(double v) {
  char buf[40];
  std::snprintf(buf, sizeof buf, "%.9g", v);
  return buf;
}
}  // namespace

const char* cmd_name(Cmd c) { return kNames[static_cast<size_t>(c)]; }

void WorkerMetrics::observe(Cmd c, uint64_t ns) {
  bump(commands[static_cast<size_t>(c)], 1);
  size_t b = 0;
  while (b < kLatencyBounds.size() && static_cast<double>(ns) * 1e-9 > kLatencyBounds[b]) ++b;
  bump(buckets[b], 1);
  bump(sum_ns, ns);
}

uint64_t WorkerMetrics::total_commands() const {
  uint64_t t = 0;
  for (const auto& c : commands) t += c.load(std::memory_order_relaxed);
  return t;
}

void MetricsSnapshot::add(const WorkerMetrics& w) {
  for (size_t i = 0; i < kCmdCount; ++i) commands[i] += w.commands[i].load(std::memory_order_relaxed);
  for (size_t i = 0; i < kBucketCount; ++i) buckets[i] += w.buckets[i].load(std::memory_order_relaxed);
  sum_ns += w.sum_ns.load(std::memory_order_relaxed);
}

std::string render_prometheus(const MetricsSnapshot& s) {
  std::string o;
  o += "# HELP fl_cache_commands_total Commands executed, by command name.\n";
  o += "# TYPE fl_cache_commands_total counter\n";
  for (size_t i = 0; i < kCmdCount; ++i)
    o += std::string("fl_cache_commands_total{cmd=\"") + kNames[i] + "\"} " +
         std::to_string(s.commands[i]) + "\n";

  o += "# HELP fl_cache_command_seconds Time to execute a command (excludes network I/O).\n";
  o += "# TYPE fl_cache_command_seconds histogram\n";
  uint64_t cumulative = 0;
  for (size_t i = 0; i < kLatencyBounds.size(); ++i) {
    cumulative += s.buckets[i];
    o += "fl_cache_command_seconds_bucket{le=\"" + num(kLatencyBounds[i]) + "\"} " +
         std::to_string(cumulative) + "\n";
  }
  cumulative += s.buckets[kLatencyBounds.size()];
  o += "fl_cache_command_seconds_bucket{le=\"+Inf\"} " + std::to_string(cumulative) + "\n";
  o += "fl_cache_command_seconds_sum " + num(static_cast<double>(s.sum_ns) * 1e-9) + "\n";
  o += "fl_cache_command_seconds_count " + std::to_string(cumulative) + "\n";

  o += "# HELP fl_cache_connections Currently open client connections.\n";
  o += "# TYPE fl_cache_connections gauge\n";
  o += "fl_cache_connections " + std::to_string(s.connections) + "\n";
  o += "# HELP fl_cache_keys Keys currently stored (may include expired keys not yet swept).\n";
  o += "# TYPE fl_cache_keys gauge\n";
  o += "fl_cache_keys " + std::to_string(s.keys) + "\n";
  o += "# HELP fl_cache_evictions_total Keys evicted to stay under --max-keys.\n";
  o += "# TYPE fl_cache_evictions_total counter\n";
  o += "fl_cache_evictions_total " + std::to_string(s.evictions) + "\n";
  o += "# HELP fl_cache_expired_total Keys removed because their TTL elapsed.\n";
  o += "# TYPE fl_cache_expired_total counter\n";
  o += "fl_cache_expired_total " + std::to_string(s.expired) + "\n";
  return o;
}

}  // namespace fl
