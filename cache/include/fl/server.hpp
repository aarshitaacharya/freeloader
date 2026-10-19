#pragma once
#include <cstdint>

#include "fl/store.hpp"
#include "fl/unique_fd.hpp"

namespace fl {

// Level 1: blocking accept loop, serves one connection at a time.
class Server {
 public:
  // Binds and listens. Throws std::runtime_error on failure.
  explicit Server(uint16_t port);

  uint16_t port() const { return port_; }  // actual port (useful when 0 was requested)
  void run();                              // runs until stop() is called
  void stop();                             // async-signal-safe

 private:
  void serve(UniqueFd client);

  UniqueFd listen_fd_;
  uint16_t port_ = 0;
  Store store_;
};

}  // namespace fl
