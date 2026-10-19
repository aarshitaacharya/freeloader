#include <csignal>
#include <cstdlib>
#include <cstring>
#include <iostream>

#include "fl/server.hpp"

namespace {
fl::Server* g_server = nullptr;
void on_signal(int) {
  if (g_server) g_server->stop();
}
}  // namespace

int main(int argc, char** argv) {
  uint16_t port = 6400;
  for (int i = 1; i < argc; ++i) {
    if (!std::strcmp(argv[i], "--port") && i + 1 < argc) {
      port = static_cast<uint16_t>(std::atoi(argv[++i]));
    } else {
      std::cerr << "usage: fl-cache [--port N]\n";
      return 2;
    }
  }
  try {
    fl::Server server(port);
    g_server = &server;
    std::signal(SIGINT, on_signal);
    std::signal(SIGTERM, on_signal);
    std::cerr << "fl-cache listening on port " << server.port() << "\n";
    server.run();
  } catch (const std::exception& e) {
    std::cerr << "fatal: " << e.what() << "\n";
    return 1;
  }
  return 0;
}
