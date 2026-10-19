#pragma once
#include <cstddef>
#include <string>
#include <string_view>
#include <vector>

namespace fl {

// Incremental parser for RESP2 client requests (arrays of bulk strings).
enum class ParseStatus { Ok, Incomplete, Error };

struct ParseResult {
  ParseStatus status = ParseStatus::Incomplete;
  size_t consumed = 0;                // bytes used by one complete command (Ok only)
  std::vector<std::string> args;      // command name + arguments (Ok only)
  std::string error;                  // reason (Error only)
};

constexpr size_t kMaxArgs = 1024 * 1024;
constexpr size_t kMaxBulkLen = 512 * 1024 * 1024;

// Parses at most one command from the front of `buf`. Never reads past `buf`.
ParseResult parse_command(std::string_view buf);

// Reply encoders.
std::string reply_simple(std::string_view s);
std::string reply_error(std::string_view s);
std::string reply_int(long long n);
std::string reply_bulk(std::string_view s);
std::string reply_null();

}  // namespace fl
