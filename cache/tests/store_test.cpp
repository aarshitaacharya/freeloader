#include <gtest/gtest.h>

#include "fl/store.hpp"

using namespace fl;

TEST(Store, SetGetDel) {
  Store s;
  EXPECT_FALSE(s.get("a"));
  s.set("a", "1");
  EXPECT_EQ(s.get("a"), "1");
  s.set("a", "2");
  EXPECT_EQ(s.get("a"), "2");
  EXPECT_TRUE(s.del("a"));
  EXPECT_FALSE(s.del("a"));
}

TEST(Execute, Commands) {
  Store s;
  EXPECT_EQ(execute(s, {"PING"}), "+PONG\r\n");
  EXPECT_EQ(execute(s, {"ping", "hi"}), "$2\r\nhi\r\n");
  EXPECT_EQ(execute(s, {"SET", "a", "1"}), "+OK\r\n");
  EXPECT_EQ(execute(s, {"get", "a"}), "$1\r\n1\r\n");
  EXPECT_EQ(execute(s, {"GET", "nope"}), "$-1\r\n");
  EXPECT_EQ(execute(s, {"DEL", "a", "b"}), ":1\r\n");
}

TEST(Execute, ErrorsNeverThrow) {
  Store s;
  EXPECT_EQ(execute(s, {"GET"})[0], '-');
  EXPECT_EQ(execute(s, {"SET", "a"})[0], '-');
  EXPECT_EQ(execute(s, {"DEL"})[0], '-');
  EXPECT_EQ(execute(s, {"FLUSHALL"})[0], '-');
}
