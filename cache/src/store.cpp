#include "fl/store.hpp"

#include <algorithm>
#include <cctype>

#include "fl/resp.hpp"

namespace fl {

std::optional<std::string> Store::get(const std::string& key) const {
  auto it = map_.find(key);
  if (it == map_.end()) return std::nullopt;
  return it->second;
}

void Store::set(std::string key, std::string value) {
  map_[std::move(key)] = std::move(value);
}

bool Store::del(const std::string& key) { return map_.erase(key) > 0; }

std::string execute(Store& store, const std::vector<std::string>& args) {
  std::string cmd = args[0];
  std::transform(cmd.begin(), cmd.end(), cmd.begin(),
                 [](unsigned char c) { return std::toupper(c); });

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
    if (args.size() != 3) return arity_err();
    store.set(args[1], args[2]);
    return reply_simple("OK");
  }
  if (cmd == "DEL") {
    if (args.size() < 2) return arity_err();
    long long n = 0;
    for (size_t i = 1; i < args.size(); ++i) n += store.del(args[i]) ? 1 : 0;
    return reply_int(n);
  }
  return reply_error("ERR unknown command '" + args[0] + "'");
}

}  // namespace fl
