#pragma once
#include <atomic>
#include <memory>
#include <mutex>
#include <thread>
#include <unordered_map>
#include <vector>

#include "fl/commands.hpp"
#include "fl/connection.hpp"
#include "fl/epoll.hpp"
#include "fl/store.hpp"
#include "fl/unique_fd.hpp"

namespace fl {

// One epoll event-loop thread. Connections are handed over by the acceptor via
// add_connection() and are then owned (and destroyed) by this worker's thread.
class Worker {
 public:
  Worker(Store& store, const InfoFn* info, const std::atomic<bool>& stop);
  ~Worker() { join(); }
  Worker(const Worker&) = delete;
  Worker& operator=(const Worker&) = delete;

  void start();
  void join();
  void add_connection(UniqueFd fd);  // thread-safe; called from the acceptor

  uint64_t commands() const { return metrics_.total_commands(); }
  const WorkerMetrics& metrics() const { return metrics_; }
  int64_t connections() const { return nconns_.load(std::memory_order_relaxed); }

 private:
  void loop();
  void register_pending(bool discard);
  void begin_drain();

  Store& store_;
  const InfoFn* info_;
  const std::atomic<bool>& stop_;
  Epoll ep_;
  UniqueFd wake_;  // eventfd: acceptor -> worker "new connection queued"

  std::mutex mu_;                      // guards pending_ only
  std::vector<UniqueFd> pending_;
  std::unordered_map<int, std::unique_ptr<Connection>> conns_;  // loop thread only

  WorkerMetrics metrics_;  // cache-line aligned; written only by this worker's thread
  std::atomic<int64_t> nconns_{0};
  std::jthread thread_;
};

}  // namespace fl
