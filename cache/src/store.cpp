#include "fl/store.hpp"

#include <charconv>
#include <chrono>
#include <limits>
#include <vector>

namespace fl {
namespace {

constexpr size_t kEvictionSample = 5;
constexpr size_t kExpirySample = 20;

bool is_expired(const detail::Entry& e, int64_t now) {
  return e.expire_ms >= 0 && now >= e.expire_ms;
}

// Visits up to n entries starting from a random bucket (the Redis approach to
// "random" sampling without an index). Caller holds the shard lock and must not
// mutate the map inside `visit`.
template <class F>
void sample(detail::Shard& sh, size_t n, F&& visit) {
  if (sh.map.empty()) return;
  const size_t buckets = sh.map.bucket_count();
  const size_t start = sh.next_rand() % buckets;
  size_t seen = 0;
  for (size_t i = 0; i < buckets && seen < n; ++i) {
    const size_t b = (start + i) % buckets;
    for (auto it = sh.map.begin(b); it != sh.map.end(b) && seen < n; ++it, ++seen)
      visit(it->first, it->second);
  }
}

}  // namespace

int64_t steady_now_ms() {
  using namespace std::chrono;
  return duration_cast<milliseconds>(steady_clock::now().time_since_epoch()).count();
}

Store::Store(StoreOptions opts)
    : nshards_(opts.shards == 0 ? 1 : opts.shards),
      max_keys_(opts.max_keys),
      clock_(opts.clock ? std::move(opts.clock) : ClockFn(steady_now_ms)),
      shards_(std::make_unique<detail::Shard[]>(nshards_)) {
  for (size_t i = 0; i < nshards_; ++i) shards_[i].rng ^= (i + 1) * 0xD6E8FEB86659FD93ull;
}

detail::Shard& Store::shard_for(const std::string& key) const {
  return shards_[std::hash<std::string>{}(key) % nshards_];
}

detail::Shard::Map::iterator Store::live_find(detail::Shard& sh, const std::string& key,
                                              int64_t now) {
  auto it = sh.map.find(key);
  if (it == sh.map.end()) return it;
  if (is_expired(it->second, now)) {  // lazy expiry
    sh.map.erase(it);
    key_count_.fetch_sub(1, std::memory_order_relaxed);
    expired_.fetch_add(1, std::memory_order_relaxed);
    return sh.map.end();
  }
  return it;
}

void Store::evict_one(detail::Shard& sh, int64_t now) {
  const std::string* victim = nullptr;
  uint64_t oldest = std::numeric_limits<uint64_t>::max();
  bool victim_expired = false;
  sample(sh, kEvictionSample, [&](const std::string& k, const detail::Entry& e) {
    if (victim_expired) return;
    if (is_expired(e, now)) {
      victim = &k;
      victim_expired = true;
    } else if (e.access < oldest) {
      oldest = e.access;
      victim = &k;
    }
  });
  if (!victim) return;
  const std::string key = *victim;  // copy: erase() invalidates the reference
  sh.map.erase(key);
  key_count_.fetch_sub(1, std::memory_order_relaxed);
  (victim_expired ? expired_ : evictions_).fetch_add(1, std::memory_order_relaxed);
}

detail::Entry& Store::insert_new(detail::Shard& sh, const std::string& key, int64_t now) {
  // The cap is global but eviction is shard-local, so it is approximate: an
  // empty shard can't evict, so the total may briefly exceed max_keys by < K.
  if (max_keys_ != 0 && key_count_.load(std::memory_order_relaxed) >= max_keys_)
    evict_one(sh, now);
  auto [it, inserted] = sh.map.try_emplace(key);
  (void)inserted;
  key_count_.fetch_add(1, std::memory_order_relaxed);
  it->second.access = ++sh.tick;
  return it->second;
}

std::optional<std::string> Store::get(const std::string& key) {
  const int64_t now = clock_();
  auto& sh = shard_for(key);
  std::lock_guard lock(sh.mu);
  auto it = live_find(sh, key, now);
  if (it == sh.map.end()) return std::nullopt;
  it->second.access = ++sh.tick;
  return it->second.value;
}

bool Store::set(const std::string& key, std::string value, const SetOptions& opts) {
  const int64_t now = clock_();
  auto& sh = shard_for(key);
  std::lock_guard lock(sh.mu);
  auto it = live_find(sh, key, now);
  detail::Entry* e;
  if (it != sh.map.end()) {
    if (opts.nx) return false;
    e = &it->second;
  } else {
    e = &insert_new(sh, key, now);
  }
  e->value = std::move(value);
  e->expire_ms = opts.ttl_ms ? now + *opts.ttl_ms : -1;  // plain SET clears any TTL
  e->access = ++sh.tick;
  return true;
}

bool Store::del(const std::string& key) {
  const int64_t now = clock_();
  auto& sh = shard_for(key);
  std::lock_guard lock(sh.mu);
  auto it = live_find(sh, key, now);
  if (it == sh.map.end()) return false;
  sh.map.erase(it);
  key_count_.fetch_sub(1, std::memory_order_relaxed);
  return true;
}

IncrResult Store::incrby(const std::string& key, int64_t delta) {
  const int64_t now = clock_();
  auto& sh = shard_for(key);
  std::lock_guard lock(sh.mu);
  auto it = live_find(sh, key, now);
  int64_t current = 0;
  if (it != sh.map.end()) {
    const std::string& v = it->second.value;
    auto [p, ec] = std::from_chars(v.data(), v.data() + v.size(), current);
    if (ec != std::errc() || p != v.data() + v.size()) return {IncrStatus::NotInteger};
  }
  int64_t result;
  if (__builtin_add_overflow(current, delta, &result)) return {IncrStatus::Overflow};
  detail::Entry& e = it != sh.map.end() ? it->second : insert_new(sh, key, now);
  e.value = std::to_string(result);  // existing TTL is preserved
  e.access = ++sh.tick;
  return {IncrStatus::Ok, result};
}

bool Store::expire(const std::string& key, int64_t seconds) {
  const int64_t now = clock_();
  auto& sh = shard_for(key);
  std::lock_guard lock(sh.mu);
  auto it = live_find(sh, key, now);
  if (it == sh.map.end()) return false;
  if (seconds <= 0) {
    sh.map.erase(it);
    key_count_.fetch_sub(1, std::memory_order_relaxed);
  } else {
    it->second.expire_ms = now + seconds * 1000;  // caller bounds seconds
  }
  return true;
}

int64_t Store::ttl_ms(const std::string& key) {
  const int64_t now = clock_();
  auto& sh = shard_for(key);
  std::lock_guard lock(sh.mu);
  auto it = live_find(sh, key, now);
  if (it == sh.map.end()) return -2;
  if (it->second.expire_ms < 0) return -1;
  return it->second.expire_ms - now;
}

void Store::expire_cycle() {
  const int64_t now = clock_();
  std::vector<std::string> dead;
  for (size_t i = 0; i < nshards_; ++i) {
    auto& sh = shards_[i];
    std::lock_guard lock(sh.mu);
    dead.clear();
    sample(sh, kExpirySample, [&](const std::string& k, const detail::Entry& e) {
      if (is_expired(e, now)) dead.push_back(k);
    });
    for (const auto& k : dead) {
      sh.map.erase(k);
      key_count_.fetch_sub(1, std::memory_order_relaxed);
      expired_.fetch_add(1, std::memory_order_relaxed);
    }
  }
}

}  // namespace fl
