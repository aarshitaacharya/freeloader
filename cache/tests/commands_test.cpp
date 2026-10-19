#include <gtest/gtest.h>

#include "fl/commands.hpp"

using namespace fl;

namespace {
std::string run(Store& s, std::vector<std::string> args) { return execute(s, args); }
}  // namespace

TEST(Commands, Basics) {
  Store s;
  EXPECT_EQ(run(s, {"PING"}), "+PONG\r\n");
  EXPECT_EQ(run(s, {"ping", "hi"}), "$2\r\nhi\r\n");
  EXPECT_EQ(run(s, {"SET", "a", "1"}), "+OK\r\n");
  EXPECT_EQ(run(s, {"get", "a"}), "$1\r\n1\r\n");
  EXPECT_EQ(run(s, {"GET", "nope"}), "$-1\r\n");
  EXPECT_EQ(run(s, {"DEL", "a", "b"}), ":1\r\n");
  EXPECT_EQ(run(s, {"DBSIZE"}), ":0\r\n");
}

TEST(Commands, SetOptions) {
  Store s;
  EXPECT_EQ(run(s, {"SET", "k", "v", "NX"}), "+OK\r\n");
  EXPECT_EQ(run(s, {"SET", "k", "v2", "nx"}), "$-1\r\n");
  EXPECT_EQ(run(s, {"SET", "e", "v", "EX", "100"}), "+OK\r\n");
  EXPECT_EQ(run(s, {"TTL", "e"}), ":100\r\n");
  EXPECT_EQ(run(s, {"SET", "e2", "v", "ex", "50", "NX"}), "+OK\r\n");
  EXPECT_EQ(run(s, {"TTL", "e2"}), ":50\r\n");
}

TEST(Commands, SetOptionErrors) {
  Store s;
  EXPECT_EQ(run(s, {"SET", "k", "v", "EX"})[0], '-');
  EXPECT_EQ(run(s, {"SET", "k", "v", "EX", "abc"})[0], '-');
  EXPECT_EQ(run(s, {"SET", "k", "v", "EX", "0"})[0], '-');
  EXPECT_EQ(run(s, {"SET", "k", "v", "EX", "-5"})[0], '-');
  EXPECT_EQ(run(s, {"SET", "k", "v", "EX", "99999999999999"})[0], '-');
  EXPECT_EQ(run(s, {"SET", "k", "v", "BOGUS"})[0], '-');
  EXPECT_EQ(s.size(), 0u);  // failed SETs must not write
}

TEST(Commands, IncrbyExpireTtl) {
  Store s;
  EXPECT_EQ(run(s, {"INCRBY", "n", "5"}), ":5\r\n");
  EXPECT_EQ(run(s, {"INCRBY", "n", "-2"}), ":3\r\n");
  EXPECT_EQ(run(s, {"INCRBY", "n", "x"})[0], '-');
  run(s, {"SET", "str", "abc"});
  EXPECT_EQ(run(s, {"INCRBY", "str", "1"})[0], '-');
  EXPECT_EQ(run(s, {"TTL", "n"}), ":-1\r\n");
  EXPECT_EQ(run(s, {"EXPIRE", "n", "60"}), ":1\r\n");
  EXPECT_EQ(run(s, {"TTL", "n"}), ":60\r\n");
  EXPECT_EQ(run(s, {"EXPIRE", "missing", "60"}), ":0\r\n");
  EXPECT_EQ(run(s, {"TTL", "missing"}), ":-2\r\n");
  EXPECT_EQ(run(s, {"EXPIRE", "n", "abc"})[0], '-');
  EXPECT_EQ(run(s, {"EXPIRE", "n", "99999999999999"})[0], '-');
}

TEST(Commands, InfoIncludesStoreAndExtraSections) {
  Store s;
  s.set("a", "1");
  InfoFn extra = [] { return std::string("connected_clients:7\r\n"); };
  std::string r = execute(s, {"INFO"}, &extra);
  EXPECT_NE(r.find("keys:1"), std::string::npos);
  EXPECT_NE(r.find("connected_clients:7"), std::string::npos);
}

TEST(Commands, UnknownAndBadArityAreErrorsNotCrashes) {
  Store s;
  for (auto& args : std::vector<std::vector<std::string>>{
           {"GET"}, {"SET", "a"}, {"DEL"}, {"INCRBY", "a"}, {"EXPIRE", "a"}, {"TTL"},
           {"DBSIZE", "x"}, {"PING", "a", "b"}, {"FLUSHALL"}, {""}})
    EXPECT_EQ(run(s, args)[0], '-') << args[0];
}
