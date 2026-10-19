#include "fl/metrics_server.hpp"

#include <poll.h>
#include <sys/socket.h>
#include <sys/time.h>

#include <cerrno>
#include <string_view>

#include "fl/net.hpp"

namespace fl {
namespace {

constexpr size_t kMaxRequest = 8192;

void send_all(int fd, const std::string& data) {
  size_t off = 0;
  while (off < data.size()) {
    ssize_t n = ::send(fd, data.data() + off, data.size() - off, MSG_NOSIGNAL);
    if (n > 0) off += static_cast<size_t>(n);
    else if (n < 0 && errno == EINTR) continue;
    else return;  // error or send timeout: give up on this client
  }
}

std::string response(int code, const char* reason, const std::string& body,
                     const char* type = "text/plain; charset=utf-8") {
  return "HTTP/1.1 " + std::to_string(code) + " " + reason + "\r\nContent-Type: " + type +
         "\r\nContent-Length: " + std::to_string(body.size()) + "\r\nConnection: close\r\n\r\n" +
         body;
}

}  // namespace

MetricsServer::MetricsServer(uint16_t port, RenderFn render, ReadyFn ready)
    : listen_fd_(listen_tcp(port, 16, port_)), render_(std::move(render)), ready_(std::move(ready)) {}

void MetricsServer::run(const std::atomic<bool>& stop) {
  while (!stop.load(std::memory_order_relaxed)) {
    pollfd pfd{listen_fd_.get(), POLLIN, 0};
    int ready = ::poll(&pfd, 1, 100);
    if (ready <= 0) continue;
    int fd = ::accept4(listen_fd_.get(), nullptr, nullptr, SOCK_CLOEXEC);
    if (fd >= 0) handle(UniqueFd(fd));
  }
}

void MetricsServer::handle(UniqueFd client) {
  timeval tv{1, 0};  // a scraper that stalls for >1s is dropped
  ::setsockopt(client.get(), SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof tv);
  ::setsockopt(client.get(), SOL_SOCKET, SO_SNDTIMEO, &tv, sizeof tv);

  std::string req;
  char buf[2048];
  while (req.find("\r\n\r\n") == std::string::npos && req.size() < kMaxRequest) {
    ssize_t n = ::recv(client.get(), buf, sizeof buf, 0);
    if (n > 0) req.append(buf, static_cast<size_t>(n));
    else if (n < 0 && errno == EINTR) continue;
    else return;
  }
  if (req.find("\r\n\r\n") == std::string::npos) {
    send_all(client.get(), response(431, "Request Header Fields Too Large", "too large\n"));
    return;
  }

  std::string_view line(req.data(), req.find("\r\n"));
  size_t sp1 = line.find(' ');
  size_t sp2 = sp1 == std::string_view::npos ? sp1 : line.find(' ', sp1 + 1);
  if (sp2 == std::string_view::npos) {
    send_all(client.get(), response(400, "Bad Request", "bad request\n"));
    return;
  }
  std::string_view method = line.substr(0, sp1);
  std::string_view path = line.substr(sp1 + 1, sp2 - sp1 - 1);
  path = path.substr(0, path.find('?'));

  if (method != "GET") {
    send_all(client.get(), response(405, "Method Not Allowed", "method not allowed\n"));
  } else if (path == "/metrics") {
    send_all(client.get(),
             response(200, "OK", render_(), "text/plain; version=0.0.4; charset=utf-8"));
  } else if (path == "/healthz") {
    send_all(client.get(), response(200, "OK", "ok\n"));
  } else if (path == "/readyz") {
    send_all(client.get(), ready_() ? response(200, "OK", "ok\n")
                                    : response(503, "Service Unavailable", "shutting down\n"));
  } else {
    send_all(client.get(), response(404, "Not Found", "not found\n"));
  }
}

}  // namespace fl
