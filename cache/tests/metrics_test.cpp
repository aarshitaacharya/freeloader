#include <arpa/inet.h>
#include <gtest/gtest.h>
#include <netinet/in.h>
#include <sys/socket.h>

#include <map>
#include <sstream>
#include <thread>

#include "fl/server.hpp"

using namespace fl;

namespace {

// Parses "name{labels} value" sample lines into a map keyed by "name{labels}".
std::map<std::string, double> parse_samples(const std::string& text) {
  std::map<std::string, double> out;
  std::istringstream in(text);
  for (std::string line; std::getline(in, line);) {
    if (line.empty() || line[0] == '#') continue;
    size_t sp = line.rfind(' ');
    out[line.substr(0, sp)] = std::stod(line.substr(sp + 1));
  }
  return out;
}

std::string http(uint16_t port, const std::string& request) {
  UniqueFd fd(::socket(AF_INET, SOCK_STREAM, 0));
  sockaddr_in a{};
  a.sin_family = AF_INET;
  a.sin_port = htons(port);
  a.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
  if (::connect(fd.get(), reinterpret_cast<sockaddr*>(&a), sizeof a) != 0) return "CONNECT FAILED";
  timeval tv{5, 0};
  ::setsockopt(fd.get(), SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof tv);
  ::send(fd.get(), request.data(), request.size(), MSG_NOSIGNAL);
  std::string out;
  char buf[4096];
  for (ssize_t n; (n = ::recv(fd.get(), buf, sizeof buf, 0)) > 0;) out.append(buf, n);
  return out;
}

std::string body_of(const std::string& resp) { return resp.substr(resp.find("\r\n\r\n") + 4); }

std::string resp_cmd(std::initializer_list<std::string> args) {
  std::string s = "*" + std::to_string(args.size()) + "\r\n";
  for (const auto& a : args) s += "$" + std::to_string(a.size()) + "\r\n" + a + "\r\n";
  return s;
}

void roundtrip(uint16_t port, const std::string& payload, size_t reply_len) {
  UniqueFd fd(::socket(AF_INET, SOCK_STREAM, 0));
  sockaddr_in a{};
  a.sin_family = AF_INET;
  a.sin_port = htons(port);
  a.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
  ASSERT_EQ(::connect(fd.get(), reinterpret_cast<sockaddr*>(&a), sizeof a), 0);
  ::send(fd.get(), payload.data(), payload.size(), MSG_NOSIGNAL);
  std::string got;
  char buf[4096];
  while (got.size() < reply_len) {
    ssize_t n = ::recv(fd.get(), buf, sizeof buf, 0);
    if (n <= 0) break;
    got.append(buf, n);
  }
  ASSERT_EQ(got.size(), reply_len);
}

// Stops the server even if an ASSERT bails out of the test early (otherwise the
// jthread join would hang forever).
struct Running {
  Server server;
  std::jthread thread;
  explicit Running(ServerConfig cfg) : server(cfg), thread([this] { server.run(); }) {}
  ~Running() { server.stop(); }
};

}  // namespace

TEST(MetricsRender, HistogramIsCumulativeAndConsistent) {
  WorkerMetrics w;
  w.observe(Cmd::Get, 500);          // 0.5us -> first bucket (le 1e-6)
  w.observe(Cmd::Get, 3'000);        // 3us   -> le 5e-6
  w.observe(Cmd::Set, 2'000'000);    // 2ms   -> le 1e-2
  w.observe(Cmd::Unknown, 5'000'000'000ull);  // 5s -> +Inf only
  MetricsSnapshot s;
  s.add(w);
  auto m = parse_samples(render_prometheus(s));

  EXPECT_EQ(m["fl_cache_commands_total{cmd=\"GET\"}"], 2);
  EXPECT_EQ(m["fl_cache_commands_total{cmd=\"SET\"}"], 1);
  EXPECT_EQ(m["fl_cache_commands_total{cmd=\"UNKNOWN\"}"], 1);
  EXPECT_EQ(m["fl_cache_commands_total{cmd=\"DEL\"}"], 0);
  EXPECT_EQ(m["fl_cache_command_seconds_bucket{le=\"1e-06\"}"], 1);
  EXPECT_EQ(m["fl_cache_command_seconds_bucket{le=\"5e-06\"}"], 2);
  EXPECT_EQ(m["fl_cache_command_seconds_bucket{le=\"0.01\"}"], 3);
  EXPECT_EQ(m["fl_cache_command_seconds_bucket{le=\"0.1\"}"], 3);
  EXPECT_EQ(m["fl_cache_command_seconds_bucket{le=\"+Inf\"}"], 4);
  EXPECT_EQ(m["fl_cache_command_seconds_count"], 4);
  EXPECT_NEAR(m["fl_cache_command_seconds_sum"], 5.002003500, 1e-9);
}

TEST(MetricsRender, EveryMetricHasHelpAndType) {
  std::string text = render_prometheus(MetricsSnapshot{});
  for (const char* name : {"fl_cache_commands_total", "fl_cache_command_seconds",
                           "fl_cache_connections", "fl_cache_keys", "fl_cache_evictions_total",
                           "fl_cache_expired_total"}) {
    EXPECT_NE(text.find(std::string("# HELP ") + name + " "), std::string::npos) << name;
    EXPECT_NE(text.find(std::string("# TYPE ") + name + " "), std::string::npos) << name;
  }
}

TEST(MetricsRender, CommandLabelsAreAFixedSet) {
  // Label values must come from the enum, never from client input.
  auto m = parse_samples(render_prometheus(MetricsSnapshot{}));
  size_t n = 0;
  for (auto& [k, v] : m)
    if (k.rfind("fl_cache_commands_total{", 0) == 0) ++n;
  EXPECT_EQ(n, kCmdCount);
}

TEST(MetricsServerHttp, ServesMetricsHealthAndErrors) {
  Running r({.port = 0, .threads = 2, .shards = 8, .max_keys = 0, .metrics_port = 0});
  Server& server = r.server;
  const uint16_t cport = server.port(), mport = server.metrics_port();
  ASSERT_NE(mport, 0);

  roundtrip(cport, resp_cmd({"SET", "a", "1"}) + resp_cmd({"GET", "a"}) + resp_cmd({"GET", "b"}) +
                       resp_cmd({"NOPE"}),
            5 + 7 + 5 + 29);

  std::string resp = http(mport, "GET /metrics HTTP/1.1\r\nHost: x\r\n\r\n");
  EXPECT_EQ(resp.rfind("HTTP/1.1 200 OK", 0), 0u);
  EXPECT_NE(resp.find("Content-Type: text/plain; version=0.0.4"), std::string::npos);
  auto m = parse_samples(body_of(resp));
  EXPECT_EQ(m["fl_cache_commands_total{cmd=\"SET\"}"], 1);
  EXPECT_EQ(m["fl_cache_commands_total{cmd=\"GET\"}"], 2);
  EXPECT_EQ(m["fl_cache_commands_total{cmd=\"UNKNOWN\"}"], 1);
  EXPECT_EQ(m["fl_cache_command_seconds_count"], 4);
  EXPECT_EQ(m["fl_cache_command_seconds_bucket{le=\"+Inf\"}"], 4);
  EXPECT_EQ(m["fl_cache_keys"], 1);

  EXPECT_EQ(http(mport, "GET /healthz HTTP/1.1\r\n\r\n").rfind("HTTP/1.1 200", 0), 0u);
  EXPECT_EQ(http(mport, "GET /readyz HTTP/1.1\r\n\r\n").rfind("HTTP/1.1 200", 0), 0u);
  EXPECT_EQ(http(mport, "GET /nope HTTP/1.1\r\n\r\n").rfind("HTTP/1.1 404", 0), 0u);
  EXPECT_EQ(http(mport, "POST /metrics HTTP/1.1\r\n\r\n").rfind("HTTP/1.1 405", 0), 0u);
  EXPECT_EQ(http(mport, "garbage\r\n\r\n").rfind("HTTP/1.1 400", 0), 0u);
  EXPECT_EQ(http(mport, "GET /metrics?x=1 HTTP/1.1\r\n\r\n").rfind("HTTP/1.1 200", 0), 0u);

}

TEST(MetricsServerHttp, StalledScraperDoesNotBlockForever) {
  Running r({.port = 0, .threads = 1, .shards = 1, .max_keys = 0, .metrics_port = 0});
  Server& server = r.server;
  // Connect and send nothing: the listener must time this client out (1s) and
  // then serve a real request.
  UniqueFd stalled(::socket(AF_INET, SOCK_STREAM, 0));
  sockaddr_in a{};
  a.sin_family = AF_INET;
  a.sin_port = htons(server.metrics_port());
  a.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
  ASSERT_EQ(::connect(stalled.get(), reinterpret_cast<sockaddr*>(&a), sizeof a), 0);
  auto t0 = std::chrono::steady_clock::now();
  std::string resp = http(server.metrics_port(), "GET /healthz HTTP/1.1\r\n\r\n");
  EXPECT_EQ(resp.rfind("HTTP/1.1 200", 0), 0u);
  EXPECT_LT(std::chrono::steady_clock::now() - t0, std::chrono::seconds(4));
}
