#pragma once
#include <atomic>
#include <cstdint>
#include <memory>
#include <optional>
#include <vector>

#include "fl/commands.hpp"
#include "fl/metrics.hpp"
#include "fl/metrics_server.hpp"
#include "fl/store.hpp"
#include "fl/unique_fd.hpp"
#include "fl/worker.hpp"

namespace fl {

struct ServerConfig {
  uint16_t port = 6400;   // 0 = pick a free port (tests)
  size_t threads = 0;     // 0 = hardware concurrency
  size_t shards = 64;
  size_t max_keys = 0;    // 0 = unlimited
  std::optional<uint16_t> metrics_port{};  // nullopt = no metrics listener; 0 = any free port
};

// Acceptor (the thread that calls run()) + N epoll workers + a background
// expiry thread. Connections are assigned to workers round-robin.
class Server {
 public:
  explicit Server(const ServerConfig& cfg);
  Server(const Server&) = delete;
  Server& operator=(const Server&) = delete;

  uint16_t port() const { return port_; }
  Store& store() { return store_; }
  uint16_t metrics_port() const { return metrics_ ? metrics_->port() : 0; }
  MetricsSnapshot snapshot() const;

  // Blocks until stop() is called, then drains in-flight replies and returns.
  void run();
  // Async-signal-safe: only stores to a lock-free atomic.
  void stop() noexcept { stop_.store(true, std::memory_order_relaxed); }

 private:
  void accept_ready(size_t& next_worker);

  UniqueFd listen_fd_;
  uint16_t port_ = 0;
  Store store_;
  std::atomic<bool> stop_{false};
  InfoFn info_;
  std::vector<std::unique_ptr<Worker>> workers_;
  std::unique_ptr<MetricsServer> metrics_;
};

}  // namespace fl
