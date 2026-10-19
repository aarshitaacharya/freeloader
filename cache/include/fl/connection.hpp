#pragma once
#include <atomic>
#include <string>

#include "fl/commands.hpp"
#include "fl/store.hpp"
#include "fl/unique_fd.hpp"

namespace fl {

// One client connection. Owned by exactly one Worker and only ever touched by
// that worker's thread, so it needs no locks. Sockets are non-blocking and
// registered edge-triggered, so service() must read until EAGAIN.
class Connection {
 public:
  enum class Result {
    Done,   // nothing more to do until the next epoll event
    Again,  // used its fairness budget; call again without waiting for an event
    Close,  // destroy this connection
  };

  Connection(UniqueFd fd, Store& store, const InfoFn* info, std::atomic<uint64_t>& commands);

  int fd() const { return fd_.get(); }
  Result service();
  bool flush();  // false on a fatal write error
  bool has_pending_output() const { return out_off_ < out_.size(); }

 private:
  void process_input();
  size_t pending() const { return out_.size() - out_off_; }

  // Stop executing pipelined commands once this much output is queued; resume
  // when the client drains it (backpressure for slow readers).
  static constexpr size_t kHighWater = 1 << 20;
  static constexpr size_t kMaxInput = 600u << 20;  // bulk limit + slack
  static constexpr int kReadBudget = 16;           // reads per service() call

  UniqueFd fd_;
  Store& store_;
  const InfoFn* info_;
  std::atomic<uint64_t>& commands_;
  std::string in_;
  std::string out_;
  size_t out_off_ = 0;
  bool eof_ = false;         // peer closed, or we are closing after a protocol error
  bool more_input_ = false;  // buffered commands left unexecuted due to backpressure
};

}  // namespace fl
