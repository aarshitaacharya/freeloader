// In-process microbenchmark of the sharded Store: no sockets, no parsing.
// It isolates lock contention, which the network benchmark can hide behind
// syscall cost. Prints one CSV row: shards,threads,seconds,total_ops,ops_per_sec
#include <atomic>
#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <thread>
#include <vector>

#include "fl/store.hpp"

int main(int argc, char** argv) {
  size_t shards = 64, threads = 4;
  double seconds = 3;
  constexpr size_t kKeys = 100000;
  for (int i = 1; i + 1 < argc; i += 2) {
    if (!std::strcmp(argv[i], "--shards")) shards = std::strtoul(argv[i + 1], nullptr, 10);
    else if (!std::strcmp(argv[i], "--threads")) threads = std::strtoul(argv[i + 1], nullptr, 10);
    else if (!std::strcmp(argv[i], "--seconds")) seconds = std::strtod(argv[i + 1], nullptr);
    else { std::fprintf(stderr, "usage: fl-store-bench --shards N --threads N --seconds S\n"); return 2; }
  }

  std::vector<std::string> keys;
  keys.reserve(kKeys);
  for (size_t i = 0; i < kKeys; ++i) {
    char buf[32];
    std::snprintf(buf, sizeof buf, "key:%012zu", i);
    keys.emplace_back(buf);
  }
  const std::string value(64, 'x');

  fl::Store store(fl::StoreOptions{.shards = shards});
  for (const auto& k : keys) store.set(k, value);  // preload: every GET hits

  std::atomic<bool> go{false}, stop{false};
  std::vector<uint64_t> ops(threads, 0);
  std::vector<std::thread> pool;
  for (size_t t = 0; t < threads; ++t)
    pool.emplace_back([&, t] {
      uint64_t x = 0x9E3779B97F4A7C15ull * (t + 1);  // xorshift64 per thread
      uint64_t n = 0;
      while (!go.load(std::memory_order_acquire)) {}
      while (!stop.load(std::memory_order_relaxed)) {
        for (int i = 0; i < 256; ++i) {  // check the stop flag every 256 ops
          x ^= x << 13; x ^= x >> 7; x ^= x << 17;
          const std::string& k = keys[x % kKeys];
          if (x & (1ull << 40)) store.set(k, value);  // 50% SET / 50% GET
          else store.get(k);
          ++n;
        }
      }
      ops[t] = n;
    });

  const auto t0 = std::chrono::steady_clock::now();
  go.store(true, std::memory_order_release);
  std::this_thread::sleep_for(std::chrono::duration<double>(seconds));
  stop.store(true, std::memory_order_relaxed);
  for (auto& t : pool) t.join();
  const double elapsed = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();

  uint64_t total = 0;
  for (auto n : ops) total += n;
  std::printf("%zu,%zu,%.3f,%llu,%.0f\n", shards, threads, elapsed,
              static_cast<unsigned long long>(total), static_cast<double>(total) / elapsed);
  return 0;
}
