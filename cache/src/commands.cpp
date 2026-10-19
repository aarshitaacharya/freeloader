#include "fl/commands.hpp"

#include <algorithm>
#include <cctype>
#include <charconv>
#include <unordered_map>

#include "fl/resp.hpp"

namespace fl {
namespace {

// ~317 years; keeps seconds * 1000 + now far from int64 overflow.
constexpr int64_t kMaxExpireSeconds = 10'000'000'000LL;

bool parse_i64(const std::string& s, int64_t& out) {
  auto [p, ec] = std::from_chars(s.data(), s.data() + s.size(), out);
  return ec == std::errc() && p == s.data() + s.size();
}

std::string upper(std::string s) {
  std::transform(s.begin(), s.end(), s.begin(),
                 [](unsigned char c) { return static_cast<char>(std::toupper(c)); });
  return s;
}

Cmd lookup(const std::string& upper_name) {
  static const std::unordered_map<std::string, Cmd> table = {
      {"PING", Cmd::Ping},     {"GET", Cmd::Get},       {"SET", Cmd::Set},
      {"DEL", Cmd::Del},       {"INCRBY", Cmd::Incrby}, {"EXPIRE", Cmd::Expire},
      {"TTL", Cmd::Ttl},       {"DBSIZE", Cmd::Dbsize}, {"INFO", Cmd::Info}};
  auto it = table.find(upper_name);
  return it == table.end() ? Cmd::Unknown : it->second;
}

const char* kNotInt = "ERR value is not an integer or out of range";

}  // namespace

std::string execute(Store& store, const std::vector<std::string>& args, const InfoFn* extra_info,
                    Cmd* ran) {
  const std::string cmd = upper(args[0]);
  if (ran) *ran = lookup(cmd);
  auto arity_err = [&] {
    return reply_error("ERR wrong number of arguments for '" + cmd + "' command");
  };

  if (cmd == "PING") {
    if (args.size() == 1) return reply_simple("PONG");
    if (args.size() == 2) return reply_bulk(args[1]);
    return arity_err();
  }
  if (cmd == "GET") {
    if (args.size() != 2) return arity_err();
    auto v = store.get(args[1]);
    return v ? reply_bulk(*v) : reply_null();
  }
  if (cmd == "SET") {
    if (args.size() < 3) return arity_err();
    SetOptions opts;
    for (size_t i = 3; i < args.size(); ++i) {
      const std::string opt = upper(args[i]);
      if (opt == "NX") {
        opts.nx = true;
      } else if (opt == "EX") {
        int64_t secs;
        if (i + 1 >= args.size()) return reply_error("ERR syntax error");
        if (!parse_i64(args[++i], secs)) return reply_error(kNotInt);
        if (secs <= 0 || secs > kMaxExpireSeconds)
          return reply_error("ERR invalid expire time in 'set' command");
        opts.ttl_ms = secs * 1000;
      } else {
        return reply_error("ERR syntax error");
      }
    }
    return store.set(args[1], args[2], opts) ? reply_simple("OK") : reply_null();
  }
  if (cmd == "DEL") {
    if (args.size() < 2) return arity_err();
    long long n = 0;
    for (size_t i = 1; i < args.size(); ++i) n += store.del(args[i]) ? 1 : 0;
    return reply_int(n);
  }
  if (cmd == "INCRBY") {
    if (args.size() != 3) return arity_err();
    int64_t delta;
    if (!parse_i64(args[2], delta)) return reply_error(kNotInt);
    IncrResult r = store.incrby(args[1], delta);
    switch (r.status) {
      case IncrStatus::Ok: return reply_int(r.value);
      case IncrStatus::NotInteger: return reply_error(kNotInt);
      case IncrStatus::Overflow: return reply_error("ERR increment or decrement would overflow");
    }
  }
  if (cmd == "EXPIRE") {
    if (args.size() != 3) return arity_err();
    int64_t secs;
    if (!parse_i64(args[2], secs)) return reply_error(kNotInt);
    if (secs > kMaxExpireSeconds) return reply_error("ERR invalid expire time in 'expire' command");
    return reply_int(store.expire(args[1], secs) ? 1 : 0);
  }
  if (cmd == "TTL") {
    if (args.size() != 2) return arity_err();
    int64_t ms = store.ttl_ms(args[1]);
    return reply_int(ms < 0 ? ms : (ms + 500) / 1000);  // round like Redis
  }
  if (cmd == "DBSIZE") {
    if (args.size() != 1) return arity_err();
    return reply_int(static_cast<long long>(store.size()));
  }
  if (cmd == "INFO") {
    std::string info = "# Stats\r\nkeys:" + std::to_string(store.size()) +
                       "\r\nshards:" + std::to_string(store.shard_count()) +
                       "\r\nevicted_keys:" + std::to_string(store.evictions()) +
                       "\r\nexpired_keys:" + std::to_string(store.expired()) + "\r\n";
    if (extra_info) info += (*extra_info)();
    return reply_bulk(info);
  }
  return reply_error("ERR unknown command '" + args[0] + "'");
}

}  // namespace fl
