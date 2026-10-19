#pragma once
#include <sys/epoll.h>

#include <cerrno>
#include <cstring>
#include <stdexcept>
#include <string>

#include "fl/unique_fd.hpp"

namespace fl {

// Owns an epoll instance. Events carry the fd in epoll_event.data.fd.
class Epoll {
 public:
  Epoll() : fd_(::epoll_create1(EPOLL_CLOEXEC)) {
    if (!fd_.valid()) throw std::runtime_error("epoll_create1: " + std::string(strerror(errno)));
  }

  bool add(int fd, uint32_t events) {
    epoll_event ev{};
    ev.events = events;
    ev.data.fd = fd;
    return ::epoll_ctl(fd_.get(), EPOLL_CTL_ADD, fd, &ev) == 0;
  }

  // Returns the number of ready events (0 on timeout or EINTR); -1 on real error.
  int wait(epoll_event* out, int max, int timeout_ms) {
    int n = ::epoll_wait(fd_.get(), out, max, timeout_ms);
    if (n < 0 && errno == EINTR) return 0;
    return n;
  }

 private:
  UniqueFd fd_;
};

}  // namespace fl
