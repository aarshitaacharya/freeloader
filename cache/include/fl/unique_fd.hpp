#pragma once
#include <unistd.h>

#include <utility>

namespace fl {

// Move-only RAII owner of a file descriptor. The only place close() is called.
class UniqueFd {
 public:
  UniqueFd() = default;
  explicit UniqueFd(int fd) : fd_(fd) {}
  ~UniqueFd() { reset(); }

  UniqueFd(const UniqueFd&) = delete;
  UniqueFd& operator=(const UniqueFd&) = delete;
  UniqueFd(UniqueFd&& o) noexcept : fd_(o.release()) {}
  UniqueFd& operator=(UniqueFd&& o) noexcept {
    if (this != &o) reset(o.release());
    return *this;
  }

  int get() const { return fd_; }
  bool valid() const { return fd_ >= 0; }
  int release() { return std::exchange(fd_, -1); }
  void reset(int fd = -1) {
    if (fd_ >= 0) ::close(fd_);
    fd_ = fd;
  }

 private:
  int fd_ = -1;
};

}  // namespace fl
