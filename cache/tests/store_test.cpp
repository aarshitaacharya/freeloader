#include <gtest/gtest.h>

#include <atomic>
#include <thread>
#include <vector>

#include "fl/store.hpp"

using namespace fl;

namespace {
struct FakeClock {
  std::atomic<int64_t> now{1000};
  ClockFn fn() {
    return [this] { return now.load(); };
  }
};
}  // namespace

TEST(Store, SetGetDel) {
  Store s;
  EXPECT_FALSE(s.get("a"));
  EXPECT_TRUE(s.set("a", "1"));
  EXPECT_EQ(s.get("a"), "1");
  EXPECT_TRUE(s.set("a", "2"));
  EXPECT_EQ(s.get("a"), "2");
  EXPECT_EQ(s.size(), 1u);
  EXPECT_TRUE(s.del("a"));
  EXPECT_FALSE(s.del("a"));
  EXPECT_EQ(s.size(), 0u);
}

TEST(Store, NxOnlySetsMissingKeys) {
  Store s;
  EXPECT_TRUE(s.set("k", "first", {.nx = true}));
  EXPECT_FALSE(s.set("k", "second", {.nx = true}));
  EXPECT_EQ(s.get("k"), "first");
}

TEST(Store, SingleShardWorks) {
  Store s(StoreOptions{.shards = 1});
  for (int i = 0; i < 100; ++i) s.set("k" + std::to_string(i), "v");
  EXPECT_EQ(s.size(), 100u);
  EXPECT_EQ(s.shard_count(), 1u);
}

TEST(StoreExpiry, LazyExpiryOnAccess) {
  FakeClock c;
  Store s(StoreOptions{.clock = c.fn()});
  s.set("k", "v", {.ttl_ms = 5000});
  EXPECT_EQ(s.get("k"), "v");
  c.now += 4999;
  EXPECT_EQ(s.get("k"), "v");
  c.now += 1;
  EXPECT_FALSE(s.get("k"));
  EXPECT_EQ(s.size(), 0u);
  EXPECT_EQ(s.expired(), 1u);
}

TEST(StoreExpiry, NxTreatsExpiredKeyAsMissing) {
  FakeClock c;
  Store s(StoreOptions{.clock = c.fn()});
  s.set("k", "old", {.ttl_ms = 100});
  c.now += 100;
  EXPECT_TRUE(s.set("k", "new", {.nx = true}));
  EXPECT_EQ(s.get("k"), "new");
}

TEST(StoreExpiry, PlainSetClearsTtl) {
  FakeClock c;
  Store s(StoreOptions{.clock = c.fn()});
  s.set("k", "v", {.ttl_ms = 100});
  s.set("k", "v2");
  EXPECT_EQ(s.ttl_ms("k"), -1);
  c.now += 1'000'000;
  EXPECT_EQ(s.get("k"), "v2");
}

TEST(StoreExpiry, ExpireAndTtl) {
  FakeClock c;
  Store s(StoreOptions{.clock = c.fn()});
  EXPECT_EQ(s.ttl_ms("missing"), -2);
  EXPECT_FALSE(s.expire("missing", 10));
  s.set("k", "v");
  EXPECT_EQ(s.ttl_ms("k"), -1);
  EXPECT_TRUE(s.expire("k", 10));
  EXPECT_EQ(s.ttl_ms("k"), 10'000);
  c.now += 2500;
  EXPECT_EQ(s.ttl_ms("k"), 7500);
  EXPECT_TRUE(s.expire("k", 0));  // non-positive deletes
  EXPECT_EQ(s.ttl_ms("k"), -2);
}

TEST(StoreExpiry, SweeperRemovesUntouchedExpiredKeys) {
  FakeClock c;
  Store s(StoreOptions{.shards = 4, .clock = c.fn()});
  for (int i = 0; i < 40; ++i) s.set("k" + std::to_string(i), "v", {.ttl_ms = 100});
  s.set("keep", "v");
  c.now += 100;
  // Sampling is probabilistic per pass (20 keys/shard), but 40 keys over 4
  // shards fit well inside the sample, so a few passes clear them all.
  for (int i = 0; i < 5; ++i) s.expire_cycle();
  EXPECT_EQ(s.size(), 1u);
  EXPECT_EQ(s.expired(), 40u);
  EXPECT_EQ(s.get("keep"), "v");
}

TEST(StoreIncr, CreatesAndAdds) {
  Store s;
  auto r = s.incrby("n", 5);
  EXPECT_EQ(r.status, IncrStatus::Ok);
  EXPECT_EQ(r.value, 5);
  EXPECT_EQ(s.incrby("n", -7).value, -2);
  EXPECT_EQ(s.get("n"), "-2");
}

TEST(StoreIncr, RejectsNonIntegersAndOverflow) {
  Store s;
  s.set("s", "abc");
  EXPECT_EQ(s.incrby("s", 1).status, IncrStatus::NotInteger);
  EXPECT_EQ(s.get("s"), "abc");  // unchanged
  s.set("big", "9223372036854775807");
  EXPECT_EQ(s.incrby("big", 1).status, IncrStatus::Overflow);
  EXPECT_EQ(s.get("big"), "9223372036854775807");
}

TEST(StoreIncr, PreservesTtl) {
  FakeClock c;
  Store s(StoreOptions{.clock = c.fn()});
  s.set("n", "1", {.ttl_ms = 5000});
  s.incrby("n", 1);
  EXPECT_EQ(s.ttl_ms("n"), 5000);
}

TEST(StoreEviction, EvictsLeastRecentlyUsedWhenFull) {
  // One shard and a sample size (5) larger than the key count makes the
  // "approximate" LRU exact, so the victim is deterministic.
  Store s(StoreOptions{.shards = 1, .max_keys = 3});
  s.set("a", "1");
  s.set("b", "2");
  s.set("c", "3");
  s.get("a");  // a is now most recently used; b is the oldest
  s.set("d", "4");
  EXPECT_EQ(s.size(), 3u);
  EXPECT_EQ(s.evictions(), 1u);
  EXPECT_FALSE(s.get("b"));
  EXPECT_TRUE(s.get("a"));
  EXPECT_TRUE(s.get("c"));
  EXPECT_TRUE(s.get("d"));
}

TEST(StoreEviction, OverwriteDoesNotEvict) {
  Store s(StoreOptions{.shards = 1, .max_keys = 2});
  s.set("a", "1");
  s.set("b", "2");
  s.set("a", "3");
  EXPECT_EQ(s.evictions(), 0u);
  EXPECT_EQ(s.size(), 2u);
}

TEST(StoreEviction, PrefersExpiredKeysAndCountsThemAsExpired) {
  FakeClock c;
  Store s(StoreOptions{.shards = 1, .max_keys = 2, .clock = c.fn()});
  s.set("old", "1", {.ttl_ms = 10});
  s.set("live", "2");
  s.get("old");
  c.now += 10;
  s.set("new", "3");
  EXPECT_EQ(s.evictions(), 0u);
  EXPECT_EQ(s.expired(), 1u);
  EXPECT_TRUE(s.get("live"));
}

TEST(StoreEviction, CapHoldsApproximatelyAcrossShards) {
  Store s(StoreOptions{.shards = 8, .max_keys = 100});
  for (int i = 0; i < 5000; ++i) s.set("key" + std::to_string(i), "v");
  EXPECT_LE(s.size(), 100u + 8u);  // documented slack: < one extra key per shard
  EXPECT_GT(s.evictions(), 4000u);
}

TEST(StoreConcurrency, ConcurrentIncrbyIsExact) {
  Store s;
  constexpr int kThreads = 8, kIters = 5000;
  std::vector<std::thread> ts;
  for (int t = 0; t < kThreads; ++t)
    ts.emplace_back([&] {
      for (int i = 0; i < kIters; ++i) s.incrby("ctr", 1);
    });
  for (auto& t : ts) t.join();
  EXPECT_EQ(s.get("ctr"), std::to_string(kThreads * kIters));
}

TEST(StoreConcurrency, MixedOperationsWithSweeperAndEviction) {
  Store s(StoreOptions{.shards = 4, .max_keys = 200});
  std::atomic<bool> stop{false};
  std::thread sweeper([&] {
    while (!stop) s.expire_cycle();
  });
  std::vector<std::thread> ts;
  for (int t = 0; t < 4; ++t)
    ts.emplace_back([&, t] {
      for (int i = 0; i < 20000; ++i) {
        std::string k = "k" + std::to_string((i * 7 + t) % 500);
        switch (i % 5) {
          case 0: s.set(k, "v", {.ttl_ms = 1}); break;
          case 1: s.get(k); break;
          case 2: s.incrby("n" + k, 1); break;
          case 3: s.expire(k, 1); break;
          case 4: s.del(k); break;
        }
      }
    });
  for (auto& t : ts) t.join();
  stop = true;
  sweeper.join();
  EXPECT_LE(s.size(), 200u + 4u);
}
