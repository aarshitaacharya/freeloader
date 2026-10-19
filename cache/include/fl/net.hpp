#pragma once
#include <arpa/inet.h>
#include <netinet/in.h>
#include <sys/socket.h>

#include <cerrno>
#include <cstring>
#include <stdexcept>
#include <string>

#include "fl/unique_fd.hpp"

namespace fl {

// Creates a non-blocking IPv4 listening socket. `actual_port` receives the
// bound port (useful when port 0 was requested).
inline UniqueFd listen_tcp(uint16_t port, int backlog, uint16_t& actual_port) {
  auto fail = [](const char* what) {
    return std::runtime_error(std::string(what) + ": " + std::strerror(errno));
  };
  UniqueFd fd(::socket(AF_INET, SOCK_STREAM | SOCK_NONBLOCK | SOCK_CLOEXEC, 0));
  if (!fd.valid()) throw fail("socket");
  int yes = 1;
  ::setsockopt(fd.get(), SOL_SOCKET, SO_REUSEADDR, &yes, sizeof yes);
  sockaddr_in addr{};
  addr.sin_family = AF_INET;
  addr.sin_addr.s_addr = htonl(INADDR_ANY);
  addr.sin_port = htons(port);
  if (::bind(fd.get(), reinterpret_cast<sockaddr*>(&addr), sizeof addr) < 0) throw fail("bind");
  if (::listen(fd.get(), backlog) < 0) throw fail("listen");
  socklen_t len = sizeof addr;
  ::getsockname(fd.get(), reinterpret_cast<sockaddr*>(&addr), &len);
  actual_port = ntohs(addr.sin_port);
  return fd;
}

}  // namespace fl
