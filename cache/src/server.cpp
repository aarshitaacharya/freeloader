#include "fl/server.hpp"

#include <arpa/inet.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <poll.h>
#include <sys/socket.h>

#include <cerrno>
#include <chrono>
#include <cstring>
#include <iostream>
#include <stdexcept>
#include <string>
#include <thread>

namespace fl {
namespace {

std::string sys_error(const char* what) { return std::string(what) + ": " + strerror(errno); }

template <class F>
struct ScopeExit {
  F f;
  ~ScopeExit() { f(); }
};
template <class F>
ScopeExit(F) -> ScopeExit<F>;

}  // namespace

Server::Server(const ServerConfig& cfg)
    : store_(StoreOptions{cfg.shards, cfg.max_keys, {}}) {
  listen_fd_.reset(::socket(AF_INET, SOCK_STREAM | SOCK_NONBLOCK | SOCK_CLOEXEC, 0));
  if (!listen_fd_.valid()) throw std::runtime_error(sys_error("socket"));
  int yes = 1;
  ::setsockopt(listen_fd_.get(), SOL_SOCKET, SO_REUSEADDR, &yes, sizeof yes);

  sockaddr_in addr{};
  addr.sin_family = AF_INET;
  addr.sin_addr.s_addr = htonl(INADDR_ANY);
  addr.sin_port = htons(cfg.port);
  if (::bind(listen_fd_.get(), reinterpret_cast<sockaddr*>(&addr), sizeof addr) < 0)
    throw std::runtime_error(sys_error("bind"));
  if (::listen(listen_fd_.get(), 511) < 0) throw std::runtime_error(sys_error("listen"));
  socklen_t len = sizeof addr;
  ::getsockname(listen_fd_.get(), reinterpret_cast<sockaddr*>(&addr), &len);
  port_ = ntohs(addr.sin_port);

  info_ = [this] {
    int64_t conns = 0;
    uint64_t cmds = 0;
    for (const auto& w : workers_) {
      conns += w->connections();
      cmds += w->commands();
    }
    return "connected_clients:" + std::to_string(conns) +
           "\r\ntotal_commands_processed:" + std::to_string(cmds) +
           "\r\nworker_threads:" + std::to_string(workers_.size()) + "\r\n";
  };

  size_t n = cfg.threads ? cfg.threads : std::max(1u, std::thread::hardware_concurrency());
  for (size_t i = 0; i < n; ++i)
    workers_.push_back(std::make_unique<Worker>(store_, &info_, stop_));
}

void Server::accept_ready(size_t& next_worker) {
  for (;;) {
    int fd = ::accept4(listen_fd_.get(), nullptr, nullptr, SOCK_NONBLOCK | SOCK_CLOEXEC);
    if (fd < 0) {
      if (errno == EAGAIN || errno == EWOULDBLOCK) return;
      if (errno == EINTR || errno == ECONNABORTED) continue;
      if (errno == EMFILE || errno == ENFILE) {  // out of fds: back off instead of spinning
        std::cerr << "accept: " << strerror(errno) << "\n";
        std::this_thread::sleep_for(std::chrono::milliseconds(10));
        return;
      }
      throw std::runtime_error(sys_error("accept4"));
    }
    UniqueFd client(fd);
    int yes = 1;
    ::setsockopt(client.get(), IPPROTO_TCP, TCP_NODELAY, &yes, sizeof yes);
    workers_[next_worker++ % workers_.size()]->add_connection(std::move(client));
  }
}

void Server::run() {
  // Declared before the threads below so it is destroyed after them; the
  // guard further down raises stop_ first, so every exit path joins cleanly.
  std::jthread sweeper;
  for (auto& w : workers_) w->start();
  ScopeExit join_all{[this] {
    stop_.store(true, std::memory_order_relaxed);
    for (auto& w : workers_) w->join();  // workers drain in-flight replies
  }};
  sweeper = std::jthread([this] {
    while (!stop_.load(std::memory_order_relaxed)) {
      store_.expire_cycle();
      std::this_thread::sleep_for(std::chrono::milliseconds(100));
    }
  });

  size_t next_worker = 0;
  while (!stop_.load(std::memory_order_relaxed)) {
    // poll() with a timeout (instead of blocking in accept) so a stop request
    // from a signal handler is noticed promptly.
    pollfd pfd{listen_fd_.get(), POLLIN, 0};
    int ready = ::poll(&pfd, 1, 100);
    if (ready < 0 && errno != EINTR) throw std::runtime_error(sys_error("poll"));
    if (ready > 0) accept_ready(next_worker);
  }
}

}  // namespace fl
