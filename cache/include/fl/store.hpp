#pragma once
#include <optional>
#include <string>
#include <string_view>
#include <unordered_map>
#include <vector>

namespace fl {

// Level 1: one unsharded map, no locking, single-threaded use only.
class Store {
 public:
  std::optional<std::string> get(const std::string& key) const;
  void set(std::string key, std::string value);
  bool del(const std::string& key);
  size_t size() const { return map_.size(); }

 private:
  std::unordered_map<std::string, std::string> map_;
};

// Executes one parsed command against the store and returns the encoded reply.
// Unknown commands and bad arity produce RESP errors, never exceptions.
std::string execute(Store& store, const std::vector<std::string>& args);

}  // namespace fl
