#include "fl/server.hpp"

#include <arpa/inet.h>
#include <netinet/in.h>
#include <poll.h>
#include <sys/socket.h>
#include <sys/time.h>

#include <atomic>
#include <cerrno>
#include <cstring>
#include <stdexcept>
#include <string>

#include "fl/resp.hpp"

namespace fl {
namespace {
std::atomic<bool> g_stop{false};

bool write_all(int fd, const std::string& data) {
  size_t off = 0;
  while (off < data.size()) {
    ssize_t n = ::send(fd, data.data() + off, data.size() - off, 0);
    if (n < 0) {
      if (errno == EINTR) continue;
      return false;
    }
    off += static_cast<size_t>(n);
  }
  return true;
}
}  // namespace

Server::Server(uint16_t port) {
  listen_fd_.reset(::socket(AF_INET, SOCK_STREAM, 0));
  if (!listen_fd_.valid()) throw std::runtime_error("socket: " + std::string(strerror(errno)));
  int yes = 1;
  ::setsockopt(listen_fd_.get(), SOL_SOCKET, SO_REUSEADDR, &yes, sizeof yes);

  sockaddr_in addr{};
  addr.sin_family = AF_INET;
  addr.sin_addr.s_addr = htonl(INADDR_ANY);
  addr.sin_port = htons(port);
  if (::bind(listen_fd_.get(), reinterpret_cast<sockaddr*>(&addr), sizeof addr) < 0)
    throw std::runtime_error("bind: " + std::string(strerror(errno)));
  if (::listen(listen_fd_.get(), 128) < 0)
    throw std::runtime_error("listen: " + std::string(strerror(errno)));

  socklen_t len = sizeof addr;
  ::getsockname(listen_fd_.get(), reinterpret_cast<sockaddr*>(&addr), &len);
  port_ = ntohs(addr.sin_port);
}

void Server::stop() {
  g_stop = true;  // run() and serve() poll this flag at least every 200 ms
}

void Server::run() {
  while (!g_stop) {
    // poll() with a timeout so a signal-set stop flag is noticed even where
    // accept() is restarted after a signal (macOS).
    pollfd pfd{listen_fd_.get(), POLLIN, 0};
    int ready = ::poll(&pfd, 1, 200);
    if (ready == 0 || (ready < 0 && errno == EINTR)) continue;
    if (ready < 0) throw std::runtime_error("poll: " + std::string(strerror(errno)));
    int fd = ::accept(listen_fd_.get(), nullptr, nullptr);
    if (fd < 0) {
      if (errno == EINTR) continue;
      if (g_stop) break;
      throw std::runtime_error("accept: " + std::string(strerror(errno)));
    }
    serve(UniqueFd(fd));
  }
}

void Server::serve(UniqueFd client) {
  // Wake periodically so a blocked recv() notices a shutdown request.
  timeval tv{0, 200 * 1000};
  ::setsockopt(client.get(), SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof tv);

  std::string in;
  char chunk[4096];
  for (;;) {
    ssize_t n = ::recv(client.get(), chunk, sizeof chunk, 0);
    if (n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK)) {
      if (g_stop) return;
      continue;
    }
    if (n < 0 && errno == EINTR) continue;
    if (n <= 0) return;  // EOF or error: UniqueFd closes the socket
    in.append(chunk, static_cast<size_t>(n));

    std::string out;
    size_t off = 0;
    for (;;) {  // drain every complete (possibly pipelined) command
      ParseResult r = parse_command(std::string_view(in).substr(off));
      if (r.status == ParseStatus::Incomplete) break;
      if (r.status == ParseStatus::Error) {
        out += reply_error("ERR " + r.error);
        write_all(client.get(), out);
        return;  // protocol errors are unrecoverable: drop the connection
      }
      out += execute(store_, r.args);
      off += r.consumed;
    }
    in.erase(0, off);
    if (!out.empty() && !write_all(client.get(), out)) return;
  }
}

}  // namespace fl
