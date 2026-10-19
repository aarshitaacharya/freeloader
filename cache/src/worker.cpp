#include "fl/worker.hpp"

#include <sys/eventfd.h>

#include <array>
#include <chrono>
#include <cerrno>
#include <cstring>

namespace fl {
namespace {
constexpr uint32_t kConnEvents = EPOLLIN | EPOLLOUT | EPOLLRDHUP | EPOLLET;
constexpr auto kDrainTimeout = std::chrono::seconds(2);
}  // namespace

Worker::Worker(Store& store, const InfoFn* info, const std::atomic<bool>& stop)
    : store_(store), info_(info), stop_(stop), wake_(::eventfd(0, EFD_NONBLOCK | EFD_CLOEXEC)) {
  if (!wake_.valid()) throw std::runtime_error("eventfd: " + std::string(strerror(errno)));
  if (!ep_.add(wake_.get(), EPOLLIN)) throw std::runtime_error("epoll_ctl(wake) failed");
}

void Worker::start() { thread_ = std::jthread([this] { loop(); }); }

void Worker::join() {
  if (thread_.joinable()) thread_.join();
}

void Worker::add_connection(UniqueFd fd) {
  {
    std::lock_guard lock(mu_);
    pending_.push_back(std::move(fd));
  }
  const uint64_t one = 1;
  ssize_t r = ::write(wake_.get(), &one, sizeof one);
  (void)r;  // eventfd write only fails if the counter would overflow
}

void Worker::register_pending(bool discard) {
  std::vector<UniqueFd> batch;
  {
    std::lock_guard lock(mu_);
    batch.swap(pending_);
  }
  if (discard) return;  // UniqueFd closes them
  for (auto& f : batch) {
    const int raw = f.get();
    auto conn = std::make_unique<Connection>(std::move(f), store_, info_, commands_);
    if (!ep_.add(raw, kConnEvents)) continue;  // conn (and its fd) destroyed
    conns_.emplace(raw, std::move(conn));
    nconns_.fetch_add(1, std::memory_order_relaxed);
  }
}

// Shutdown: stop reading; keep only connections that still owe the client bytes.
void Worker::begin_drain() {
  register_pending(/*discard=*/true);
  for (auto it = conns_.begin(); it != conns_.end();) {
    if (!it->second->flush() || !it->second->has_pending_output()) {
      it = conns_.erase(it);
      nconns_.fetch_sub(1, std::memory_order_relaxed);
    } else {
      ++it;
    }
  }
}

void Worker::loop() {
  std::array<epoll_event, 256> events;
  std::vector<int> ready;
  std::vector<int> again;
  bool draining = false;
  std::chrono::steady_clock::time_point deadline;

  for (;;) {
    if (!draining && stop_.load(std::memory_order_relaxed)) {
      draining = true;
      deadline = std::chrono::steady_clock::now() + kDrainTimeout;
      begin_drain();
    }
    if (draining && (conns_.empty() || std::chrono::steady_clock::now() >= deadline)) break;

    const int timeout = (!again.empty() && !draining) ? 0 : 100;
    const int n = ep_.wait(events.data(), static_cast<int>(events.size()), timeout);
    if (n < 0) break;

    ready.clear();
    ready.swap(again);
    for (int i = 0; i < n; ++i) {
      const int fd = events[i].data.fd;
      if (fd == wake_.get()) {
        uint64_t counter;
        ssize_t r = ::read(wake_.get(), &counter, sizeof counter);
        (void)r;
        register_pending(draining);
      } else {
        ready.push_back(fd);
      }
    }

    for (int fd : ready) {
      auto it = conns_.find(fd);
      if (it == conns_.end()) continue;  // already closed earlier in this batch
      Connection& c = *it->second;
      bool close = false;
      if (draining) {
        close = !c.flush() || !c.has_pending_output();
      } else {
        switch (c.service()) {
          case Connection::Result::Done: break;
          case Connection::Result::Again: again.push_back(fd); break;
          case Connection::Result::Close: close = true; break;
        }
      }
      if (close) {
        conns_.erase(it);  // ~Connection closes the fd, which removes it from epoll
        nconns_.fetch_sub(1, std::memory_order_relaxed);
      }
    }
  }
  nconns_.fetch_sub(static_cast<int64_t>(conns_.size()), std::memory_order_relaxed);
  conns_.clear();
}

}  // namespace fl
