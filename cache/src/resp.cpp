#include "fl/resp.hpp"

#include <charconv>

namespace fl {
namespace {

ParseResult fail(std::string msg) {
  ParseResult r;
  r.status = ParseStatus::Error;
  r.error = std::move(msg);
  return r;
}

// Reads a CRLF-terminated line starting at `pos`. Returns false if incomplete.
bool read_line(std::string_view buf, size_t pos, std::string_view& line, size_t& next) {
  size_t end = buf.find("\r\n", pos);
  if (end == std::string_view::npos) return false;
  line = buf.substr(pos, end - pos);
  next = end + 2;
  return true;
}

bool parse_len(std::string_view s, long long& out) {
  auto [p, ec] = std::from_chars(s.data(), s.data() + s.size(), out);
  return ec == std::errc() && p == s.data() + s.size();
}

}  // namespace

ParseResult parse_command(std::string_view buf) {
  ParseResult r;
  if (buf.empty()) return r;
  if (buf[0] != '*') return fail("Protocol error: expected '*'");

  std::string_view line;
  size_t pos = 0;
  if (!read_line(buf, 1, line, pos)) {
    // An unterminated header can't be allowed to grow forever.
    if (buf.size() > 32) return fail("Protocol error: invalid multibulk length");
    return r;
  }
  long long n;
  if (!parse_len(line, n) || n < 1 || static_cast<size_t>(n) > kMaxArgs)
    return fail("Protocol error: invalid multibulk length");

  std::vector<std::string> args;
  args.reserve(std::min<size_t>(static_cast<size_t>(n), 16));
  for (long long i = 0; i < n; ++i) {
    if (pos >= buf.size()) return r;
    if (buf[pos] != '$') return fail("Protocol error: expected '$'");
    if (!read_line(buf, pos + 1, line, pos)) {
      if (buf.size() - pos > 32) return fail("Protocol error: invalid bulk length");
      return r;
    }
    long long len;
    if (!parse_len(line, len) || len < 0 || static_cast<size_t>(len) > kMaxBulkLen)
      return fail("Protocol error: invalid bulk length");
    size_t need = static_cast<size_t>(len) + 2;
    if (buf.size() - pos < need) return r;
    if (buf.compare(pos + len, 2, "\r\n") != 0)
      return fail("Protocol error: bulk string not terminated by CRLF");
    args.emplace_back(buf.substr(pos, len));
    pos += need;
  }
  r.status = ParseStatus::Ok;
  r.consumed = pos;
  r.args = std::move(args);
  return r;
}

std::string reply_simple(std::string_view s) { return "+" + std::string(s) + "\r\n"; }
std::string reply_error(std::string_view s) { return "-" + std::string(s) + "\r\n"; }
std::string reply_int(long long n) { return ":" + std::to_string(n) + "\r\n"; }
std::string reply_bulk(std::string_view s) {
  return "$" + std::to_string(s.size()) + "\r\n" + std::string(s) + "\r\n";
}
std::string reply_null() { return "$-1\r\n"; }

}  // namespace fl
