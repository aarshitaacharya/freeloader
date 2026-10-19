#include "fl/connection.hpp"

#include <sys/socket.h>

#include <cerrno>
#include <chrono>
#include <string_view>

#include "fl/resp.hpp"

namespace fl {

Connection::Connection(UniqueFd fd, Store& store, const InfoFn* info, WorkerMetrics& metrics)
    : fd_(std::move(fd)), store_(store), info_(info), metrics_(metrics) {}

bool Connection::flush() {
  while (out_off_ < out_.size()) {
    ssize_t n = ::send(fd_.get(), out_.data() + out_off_, out_.size() - out_off_, MSG_NOSIGNAL);
    if (n > 0) {
      out_off_ += static_cast<size_t>(n);
    } else if (n < 0 && errno == EINTR) {
      continue;
    } else if (n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK)) {
      return true;  // socket full; the next EPOLLOUT edge resumes us
    } else {
      return false;
    }
  }
  out_.clear();
  out_off_ = 0;
  if (out_.capacity() > kHighWater) std::string().swap(out_);  // release big buffers
  return true;
}

void Connection::process_input() {
  size_t off = 0;
  more_input_ = false;
  while (off < in_.size()) {
    if (pending() > kHighWater) {
      more_input_ = true;
      break;
    }
    ParseResult r = parse_command(std::string_view(in_).substr(off));
    if (r.status == ParseStatus::Incomplete) break;
    if (r.status == ParseStatus::Error) {
      out_ += reply_error("ERR " + r.error);
      eof_ = true;  // protocol errors are unrecoverable: flush the error, then close
      in_.clear();
      off = 0;
      break;
    }
    off += r.consumed;
    if (r.args.empty()) continue;  // blank inline line
    Cmd cmd = Cmd::Unknown;
    const auto t0 = std::chrono::steady_clock::now();
    out_ += execute(store_, r.args, info_, &cmd);
    const auto dt = std::chrono::steady_clock::now() - t0;
    metrics_.observe(cmd, static_cast<uint64_t>(
                              std::chrono::duration_cast<std::chrono::nanoseconds>(dt).count()));
  }
  if (off > 0) in_.erase(0, off);
  if (in_.size() > kMaxInput) {
    out_ += reply_error("ERR Protocol error: request too large");
    eof_ = true;
    in_.clear();
  }
}

Connection::Result Connection::service() {
  char buf[64 * 1024];
  int budget = kReadBudget;
  for (;;) {
    if (!flush()) return Result::Close;
    if (pending() > kHighWater) return Result::Done;  // wait for EPOLLOUT
    if (more_input_) {
      process_input();
      continue;
    }
    if (eof_) return has_pending_output() ? Result::Done : Result::Close;
    if (budget-- == 0) return Result::Again;

    ssize_t n = ::read(fd_.get(), buf, sizeof buf);
    if (n > 0) {
      in_.append(buf, static_cast<size_t>(n));
      process_input();
    } else if (n == 0) {
      eof_ = true;
    } else if (errno == EINTR) {
      continue;
    } else if (errno == EAGAIN || errno == EWOULDBLOCK) {
      return Result::Done;
    } else {
      return Result::Close;
    }
  }
}

}  // namespace fl
