#pragma once
#include <functional>
#include <string>
#include <vector>

#include "fl/store.hpp"

namespace fl {

// Supplies the server-wide part of INFO (connections, commands processed).
using InfoFn = std::function<std::string()>;

// Executes one parsed command and returns the encoded RESP reply. Unknown
// commands and bad arguments produce RESP errors, never exceptions.
std::string execute(Store& store, const std::vector<std::string>& args,
                    const InfoFn* extra_info = nullptr);

}  // namespace fl
