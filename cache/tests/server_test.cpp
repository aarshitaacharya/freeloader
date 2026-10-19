#include <arpa/inet.h>
#include <gtest/gtest.h>
#include <netinet/in.h>
#include <sys/socket.h>

#include <chrono>
#include <string>
#include <thread>
#include <vector>

#include "fl/server.hpp"

using namespace fl;

namespace {

// Runs a real Server (port 0) on a background thread.
struct TestServer {
  Server server;
  std::jthread thread;
  explicit TestServer(ServerConfig cfg = {.port = 0, .threads = 2})
      : server(cfg), thread([this] { server.run(); }) {}
  ~TestServer() { server.stop(); }  // thread joins after this body runs
};

class Client {
 public:
  explicit Client(uint16_t port) : fd_(::socket(AF_INET, SOCK_STREAM, 0)) {
    sockaddr_in a{};
    a.sin_family = AF_INET;
    a.sin_port = htons(port);
    a.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    if (::connect(fd_.get(), reinterpret_cast<sockaddr*>(&a), sizeof a) != 0)
      throw std::runtime_error("connect failed");
    timeval tv{10, 0};
    ::setsockopt(fd_.get(), SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof tv);
  }

  void send(const std::string& data) {
    size_t off = 0;
    while (off < data.size()) {
      ssize_t n = ::send(fd_.get(), data.data() + off, data.size() - off, MSG_NOSIGNAL);
      ASSERT_GT(n, 0);
      off += static_cast<size_t>(n);
    }
  }
  // Reads exactly n bytes (fewer only if the peer closes or times out).
  std::string read_n(size_t n) {
    std::string out;
    char buf[65536];
    while (out.size() < n) {
      ssize_t r = ::recv(fd_.get(), buf, std::min(sizeof buf, n - out.size()), 0);
      if (r <= 0) break;
      out.append(buf, static_cast<size_t>(r));
    }
    return out;
  }
  std::string read_all_until_close() {
    std::string out;
    char buf[65536];
    for (ssize_t r; (r = ::recv(fd_.get(), buf, sizeof buf, 0)) > 0;) out.append(buf, r);
    return out;
  }
  void shutdown_write() { ::shutdown(fd_.get(), SHUT_WR); }

  static std::string cmd(std::initializer_list<std::string> args) {
    std::string s = "*" + std::to_string(args.size()) + "\r\n";
    for (const auto& a : args) s += "$" + std::to_string(a.size()) + "\r\n" + a + "\r\n";
    return s;
  }
  std::string roundtrip(std::initializer_list<std::string> args, size_t reply_len) {
    send(cmd(args));
    return read_n(reply_len);
  }

 private:
  UniqueFd fd_;
};

}  // namespace

TEST(Server, SetGetDel) {
  TestServer ts;
  Client c(ts.server.port());
  EXPECT_EQ(c.roundtrip({"SET", "a", "1"}, 5), "+OK\r\n");
  EXPECT_EQ(c.roundtrip({"GET", "a"}, 7), "$1\r\n1\r\n");
  EXPECT_EQ(c.roundtrip({"DEL", "a"}, 4), ":1\r\n");
  EXPECT_EQ(c.roundtrip({"GET", "a"}, 5), "$-1\r\n");
}

TEST(Server, UnknownCommandKeepsConnectionOpen) {
  TestServer ts;
  Client c(ts.server.port());
  std::string err = "-ERR unknown command 'NOPE'\r\n";
  EXPECT_EQ(c.roundtrip({"NOPE"}, err.size()), err);
  EXPECT_EQ(c.roundtrip({"PING"}, 7), "+PONG\r\n");
}

TEST(Server, PipelinedCommandsInOneWrite) {
  TestServer ts;
  Client c(ts.server.port());
  std::string batch;
  for (int i = 0; i < 1000; ++i) batch += Client::cmd({"PING"});
  c.send(batch);
  std::string expect;
  for (int i = 0; i < 1000; ++i) expect += "+PONG\r\n";
  EXPECT_EQ(c.read_n(expect.size()), expect);
}

TEST(Server, CommandSplitIntoSingleBytes) {
  TestServer ts;
  Client c(ts.server.port());
  for (char ch : Client::cmd({"SET", "k", "hello"})) {
    c.send(std::string(1, ch));
    std::this_thread::sleep_for(std::chrono::microseconds(200));
  }
  EXPECT_EQ(c.read_n(5), "+OK\r\n");
  EXPECT_EQ(c.roundtrip({"GET", "k"}, 11), "$5\r\nhello\r\n");
}

TEST(Server, LargeValueRoundtrip) {
  TestServer ts;
  Client c(ts.server.port());
  std::string big(3 * 1024 * 1024, 'x');
  big[12345] = 'y';
  std::thread writer([&] { c.send(Client::cmd({"SET", "big", big})); });
  EXPECT_EQ(c.read_n(5), "+OK\r\n");
  writer.join();
  c.send(Client::cmd({"GET", "big"}));
  std::string header = "$" + std::to_string(big.size()) + "\r\n";
  std::string reply = c.read_n(header.size() + big.size() + 2);
  EXPECT_EQ(reply, header + big + "\r\n");
}

TEST(Server, InlineCommandsWork) {
  TestServer ts;
  Client c(ts.server.port());
  c.send("PING\r\n\r\nSET a 1\r\nGET a\r\n");
  EXPECT_EQ(c.read_n(7 + 5 + 7), "+PONG\r\n+OK\r\n$1\r\n1\r\n");
}

TEST(Server, MalformedInputGetsErrorThenClose) {
  TestServer ts;
  Client c(ts.server.port());
  c.send("*x\r\n");
  std::string reply = c.read_all_until_close();
  EXPECT_EQ(reply.rfind("-ERR Protocol error", 0), 0u) << reply;
}

TEST(Server, HalfCloseStillGetsAllReplies) {
  TestServer ts;
  Client c(ts.server.port());
  std::string batch;
  for (int i = 0; i < 100; ++i) batch += Client::cmd({"PING"});
  c.send(batch);
  c.shutdown_write();  // like `printf ... | nc`
  EXPECT_EQ(c.read_all_until_close().size(), 100u * 7);
}

TEST(Server, ManyConcurrentClientsIncrementExactly) {
  TestServer ts({.port = 0, .threads = 4, .shards = 16});
  constexpr int kClients = 16, kIters = 500;
  std::vector<std::thread> ts_;
  for (int i = 0; i < kClients; ++i)
    ts_.emplace_back([&] {
      Client c(ts.server.port());
      std::string batch, expect;
      for (int j = 0; j < kIters; ++j) batch += Client::cmd({"INCRBY", "ctr", "1"});
      c.send(batch);
      // Replies are :N\r\n with N unknown here; just consume kIters lines.
      std::string got;
      size_t lines = 0;
      while (lines < kIters) {
        std::string ch = c.read_n(1);
        ASSERT_EQ(ch.size(), 1u);
        if (ch == "\n") ++lines;
      }
    });
  for (auto& t : ts_) t.join();
  Client c(ts.server.port());
  std::string want = std::to_string(kClients * kIters);
  const std::string expect = "$" + std::to_string(want.size()) + "\r\n" + want + "\r\n";
  EXPECT_EQ(c.roundtrip({"GET", "ctr"}, expect.size()), expect);
}

TEST(Server, SlowReaderGetsEveryByteViaBackpressure) {
  TestServer ts;
  Client c(ts.server.port());
  const std::string value(100 * 1024, 'v');
  ASSERT_EQ(c.roundtrip({"SET", "v", value}, 5), "+OK\r\n");

  constexpr int kGets = 300;
  const size_t per_reply = std::string("$102400\r\n").size() + value.size() + 2;
  std::thread writer([&] {
    std::string batch;
    for (int i = 0; i < kGets; ++i) batch += Client::cmd({"GET", "v"});
    c.send(batch);
  });
  std::this_thread::sleep_for(std::chrono::milliseconds(300));  // don't read yet
  std::string all = c.read_n(per_reply * kGets);
  writer.join();
  EXPECT_EQ(all.size(), per_reply * kGets);
}

TEST(Server, ClientVanishingMidReplyDoesNotHurtOthers) {
  TestServer ts;
  {
    Client rude(ts.server.port());
    ASSERT_EQ(rude.roundtrip({"SET", "v", std::string(200 * 1024, 'v')}, 5), "+OK\r\n");
    std::string batch;
    for (int i = 0; i < 200; ++i) batch += Client::cmd({"GET", "v"});
    rude.send(batch);
  }  // closes without reading
  Client c(ts.server.port());
  EXPECT_EQ(c.roundtrip({"PING"}, 7), "+PONG\r\n");
}

TEST(Server, InfoReportsConnectionsAndCommands) {
  TestServer ts;
  Client c(ts.server.port());
  c.send(Client::cmd({"INFO"}));
  std::string head = c.read_n(1);
  ASSERT_EQ(head, "$");
  std::string len;
  for (char ch; (ch = c.read_n(1)[0]) != '\r';) len += ch;
  c.read_n(1);
  std::string body = c.read_n(std::stoul(len));
  EXPECT_NE(body.find("connected_clients:1"), std::string::npos) << body;
  EXPECT_NE(body.find("worker_threads:2"), std::string::npos) << body;
}

TEST(Server, StopWithIdleConnectionsReturnsPromptly) {
  auto ts = std::make_unique<TestServer>();
  Client a(ts->server.port()), b(ts->server.port());
  ASSERT_EQ(a.roundtrip({"PING"}, 7), "+PONG\r\n");
  auto t0 = std::chrono::steady_clock::now();
  ts.reset();  // stop() + join
  auto took = std::chrono::steady_clock::now() - t0;
  EXPECT_LT(took, std::chrono::seconds(2));
  EXPECT_EQ(a.read_all_until_close(), "");  // server closed the connection
}

TEST(Server, StopDeliversRepliesAlreadyProduced) {
  auto ts = std::make_unique<TestServer>();
  Client c(ts->server.port());
  std::string batch;
  for (int i = 0; i < 2000; ++i) batch += Client::cmd({"PING"});
  c.send(batch);
  ts->server.stop();
  std::string got = c.read_all_until_close();
  // Whatever arrived must be whole, in-order replies (a prefix of the stream).
  ASSERT_EQ(got.size() % 7, 0u);
  for (size_t i = 0; i < got.size(); i += 7) ASSERT_EQ(got.substr(i, 7), "+PONG\r\n");
}

TEST(Server, SingleShardConfigServes) {
  TestServer ts({.port = 0, .threads = 2, .shards = 1});
  Client c(ts.server.port());
  EXPECT_EQ(c.roundtrip({"SET", "a", "1"}, 5), "+OK\r\n");
  EXPECT_EQ(c.roundtrip({"GET", "a"}, 7), "$1\r\n1\r\n");
}
