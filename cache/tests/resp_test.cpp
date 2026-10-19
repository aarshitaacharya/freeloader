#include <gtest/gtest.h>

#include "fl/resp.hpp"

using namespace fl;

TEST(RespParser, ParsesCompleteCommand) {
  auto r = parse_command("*2\r\n$3\r\nGET\r\n$1\r\na\r\n");
  ASSERT_EQ(r.status, ParseStatus::Ok);
  EXPECT_EQ(r.consumed, 20u);
  EXPECT_EQ(r.args, (std::vector<std::string>{"GET", "a"}));
}

TEST(RespParser, EmptyBufferIsIncomplete) {
  EXPECT_EQ(parse_command("").status, ParseStatus::Incomplete);
}

TEST(RespParser, EveryStrictPrefixIsIncomplete) {
  const std::string full = "*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$5\r\nhello\r\n";
  for (size_t i = 0; i < full.size(); ++i) {
    EXPECT_EQ(parse_command(std::string_view(full).substr(0, i)).status,
              ParseStatus::Incomplete)
        << "prefix length " << i;
  }
  EXPECT_EQ(parse_command(full).status, ParseStatus::Ok);
}

TEST(RespParser, PipelinedCommandsConsumeOneAtATime) {
  std::string buf = "*1\r\n$4\r\nPING\r\n*2\r\n$3\r\nGET\r\n$1\r\nx\r\n";
  auto a = parse_command(buf);
  ASSERT_EQ(a.status, ParseStatus::Ok);
  EXPECT_EQ(a.args[0], "PING");
  auto b = parse_command(std::string_view(buf).substr(a.consumed));
  ASSERT_EQ(b.status, ParseStatus::Ok);
  EXPECT_EQ(b.args, (std::vector<std::string>{"GET", "x"}));
  EXPECT_EQ(a.consumed + b.consumed, buf.size());
}

TEST(RespParser, BinarySafeBulk) {
  std::string payload("a\r\nb\0c", 6);
  std::string buf = "*1\r\n$6\r\n" + payload + "\r\n";
  auto r = parse_command(buf);
  ASSERT_EQ(r.status, ParseStatus::Ok);
  EXPECT_EQ(r.args[0], payload);
}

TEST(RespParser, EmptyBulkString) {
  auto r = parse_command("*2\r\n$3\r\nGET\r\n$0\r\n\r\n");
  ASSERT_EQ(r.status, ParseStatus::Ok);
  EXPECT_EQ(r.args[1], "");
}

TEST(RespParser, MalformedInputIsError) {
  const char* bad[] = {
      "PING\r\n",                       // not an array
      "*x\r\n",                         // non-numeric count
      "*0\r\n",                         // zero args
      "*-1\r\n",                        // negative count
      "*1\r\n:5\r\n",                   // wrong element type
      "*1\r\n$-1\r\n",                  // null bulk not valid in a request
      "*1\r\n$abc\r\n",                 // non-numeric bulk length
      "*1\r\n$3\r\nabcXY",              // missing CRLF after bulk
      "*99999999999\r\n",               // absurd count
      "*1\r\n$99999999999\r\n",         // absurd bulk length
  };
  for (const char* b : bad)
    EXPECT_EQ(parse_command(b).status, ParseStatus::Error) << b;
}

TEST(RespParser, OverlongUnterminatedHeaderIsError) {
  EXPECT_EQ(parse_command("*" + std::string(100, '1')).status, ParseStatus::Error);
}

TEST(RespReply, Encoders) {
  EXPECT_EQ(reply_simple("OK"), "+OK\r\n");
  EXPECT_EQ(reply_error("ERR x"), "-ERR x\r\n");
  EXPECT_EQ(reply_int(-3), ":-3\r\n");
  EXPECT_EQ(reply_bulk("hi"), "$2\r\nhi\r\n");
  EXPECT_EQ(reply_null(), "$-1\r\n");
}
