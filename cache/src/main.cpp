#include <charconv>
#include <csignal>
#include <cstring>
#include <iostream>
#include <optional>
#include <string>
#include <string_view>

#include "fl/server.hpp"

namespace {
fl::Server* g_server = nullptr;
void on_signal(int) {
  if (g_server) g_server->stop();
}

bool parse_num(const char* s, size_t& out) {
  std::string_view v(s);
  auto [p, ec] = std::from_chars(v.data(), v.data() + v.size(), out);
  return ec == std::errc() && p == v.data() + v.size();
}

int usage() {
  std::cerr << "usage: fl-cache [--port N] [--threads N] [--shards N] [--max-keys N] [--metrics-port N]\n  (--metrics-port defaults to 9400; 0 disables the metrics listener)\n";
  return 2;
}
}  // namespace

int main(int argc, char** argv) {
  fl::ServerConfig cfg;
  cfg.metrics_port = 9400;
  for (int i = 1; i < argc; ++i) {
    size_t v;
    if (i + 1 >= argc || !parse_num(argv[i + 1], v)) return usage();
    if (!std::strcmp(argv[i], "--port") && v <= 65535) cfg.port = static_cast<uint16_t>(v);
    else if (!std::strcmp(argv[i], "--threads")) cfg.threads = v;
    else if (!std::strcmp(argv[i], "--shards") && v >= 1) cfg.shards = v;
    else if (!std::strcmp(argv[i], "--max-keys")) cfg.max_keys = v;
    else if (!std::strcmp(argv[i], "--metrics-port") && v <= 65535)
      cfg.metrics_port = v == 0 ? std::nullopt : std::optional<uint16_t>(static_cast<uint16_t>(v));
    else return usage();
    ++i;
  }
  try {
    fl::Server server(cfg);
    g_server = &server;
    std::signal(SIGINT, on_signal);
    std::signal(SIGTERM, on_signal);
    std::signal(SIGPIPE, SIG_IGN);
    std::cerr << "fl-cache listening on port " << server.port() << " (shards=" << cfg.shards
              << ", metrics on "
              << (server.metrics_port() ? std::to_string(server.metrics_port()) : "off") << ")\n";
    server.run();
    std::cerr << "fl-cache: shut down cleanly\n";
  } catch (const std::exception& e) {
    std::cerr << "fatal: " << e.what() << "\n";
    return 1;
  }
  return 0;
}
