#pragma once
#include <atomic>
#include <cstdint>
#include <functional>
#include <memory>
#include <mutex>
#include <optional>
#include <string>
#include <unordered_map>

namespace fl {

using ClockFn = std::function<int64_t()>;  // monotonic milliseconds
int64_t steady_now_ms();

namespace detail {
struct Entry {
  std::string value;
  int64_t expire_ms = -1;  // -1 = no expiry
  uint64_t access = 0;     // shard-local logical clock, for approximate LRU
};

// One lock-protected partition of the keyspace. Cache-line aligned so
// neighbouring shards' mutexes don't false-share.
struct alignas(64) Shard {
  using Map = std::unordered_map<std::string, Entry>;
  std::mutex mu;
  Map map;
  uint64_t tick = 0;
  uint64_t rng = 0x9E3779B97F4A7C15ull;
  uint64_t next_rand() {  // xorshift64; guarded by mu
    rng ^= rng << 13;
    rng ^= rng >> 7;
    rng ^= rng << 17;
    return rng;
  }
};
}  // namespace detail

struct StoreOptions {
  size_t shards = 64;    // 1 = single-big-lock baseline
  size_t max_keys = 0;   // 0 = unlimited
  ClockFn clock{};       // defaults to steady_now_ms; tests inject a fake
};

struct SetOptions {
  std::optional<int64_t> ttl_ms{};
  bool nx = false;
};

enum class IncrStatus { Ok, NotInteger, Overflow };
struct IncrResult {
  IncrStatus status;
  int64_t value = 0;
};

// Thread-safe sharded key-value store. shard = hash(key) mod K; each shard has
// its own mutex. No operation ever holds two shard locks.
class Store {
 public:
  explicit Store(StoreOptions opts = {});
  Store(const Store&) = delete;
  Store& operator=(const Store&) = delete;

  std::optional<std::string> get(const std::string& key);
  // Returns false only when nx is set and the key already exists.
  bool set(const std::string& key, std::string value, const SetOptions& opts = {});
  bool del(const std::string& key);
  IncrResult incrby(const std::string& key, int64_t delta);
  // seconds <= 0 deletes the key. Returns false if the key doesn't exist.
  bool expire(const std::string& key, int64_t seconds);
  // -2 missing, -1 no expiry, otherwise remaining milliseconds.
  int64_t ttl_ms(const std::string& key);

  size_t size() const { return key_count_.load(std::memory_order_relaxed); }
  size_t shard_count() const { return nshards_; }
  uint64_t evictions() const { return evictions_.load(std::memory_order_relaxed); }
  uint64_t expired() const { return expired_.load(std::memory_order_relaxed); }

  // One pass of the background sweeper: samples 20 keys per shard, deletes expired.
  void expire_cycle();

 private:
  detail::Shard& shard_for(const std::string& key) const;
  detail::Shard::Map::iterator live_find(detail::Shard& sh, const std::string& key, int64_t now);
  detail::Entry& insert_new(detail::Shard& sh, const std::string& key, int64_t now);
  void evict_one(detail::Shard& sh, int64_t now);

  size_t nshards_;
  size_t max_keys_;
  ClockFn clock_;
  std::unique_ptr<detail::Shard[]> shards_;
  std::atomic<size_t> key_count_{0};
  std::atomic<uint64_t> evictions_{0};
  std::atomic<uint64_t> expired_{0};
};

}  // namespace fl
