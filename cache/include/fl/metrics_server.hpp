#pragma once
#include <atomic>
#include <cstdint>
#include <functional>
#include <string>

#include "fl/unique_fd.hpp"

namespace fl {

// Minimal hand-written HTTP/1.1 listener for Prometheus: GET /metrics,
// /healthz, /readyz. One thread, one request per connection ("Connection:
// close"), short socket timeouts so a stalled client can't wedge it for long.
class MetricsServer {
 public:
  using RenderFn = std::function<std::string()>;
  using ReadyFn = std::function<bool()>;

  MetricsServer(uint16_t port, RenderFn render, ReadyFn ready);
  uint16_t port() const { return port_; }
  void run(const std::atomic<bool>& stop);  // blocks until stop is set

 private:
  void handle(UniqueFd client);

  uint16_t port_ = 0;  // declared before listen_fd_: the ctor fills it while binding
  UniqueFd listen_fd_;
  RenderFn render_;
  ReadyFn ready_;
};

}  // namespace fl
