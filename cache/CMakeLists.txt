cmake_minimum_required(VERSION 3.24)
project(fl-cache LANGUAGES CXX)

if(NOT CMAKE_SYSTEM_NAME STREQUAL "Linux")
  message(FATAL_ERROR "fl-cache uses epoll and builds on Linux only. From the repo root run: make cache-test")
endif()

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

# Sanitizer flags apply to everything (including GoogleTest) so instrumentation is consistent.
if(SANITIZE STREQUAL "address")
  add_compile_options(-fsanitize=address,undefined -fno-omit-frame-pointer)
  add_link_options(-fsanitize=address,undefined)
elseif(SANITIZE STREQUAL "thread")
  add_compile_options(-fsanitize=thread)
  add_link_options(-fsanitize=thread)
endif()

# Warnings-as-errors apply to our targets only, not third-party code.
add_library(fl_warnings INTERFACE)
target_compile_options(fl_warnings INTERFACE -Wall -Wextra -Werror)

find_package(Threads REQUIRED)

add_library(flcore STATIC
  src/resp.cpp src/store.cpp src/commands.cpp
  src/metrics.cpp src/metrics_server.cpp
  src/connection.cpp src/worker.cpp src/server.cpp)
target_include_directories(flcore PUBLIC include)
target_link_libraries(flcore PUBLIC Threads::Threads PRIVATE fl_warnings)

add_executable(fl-cache src/main.cpp)
target_link_libraries(fl-cache PRIVATE flcore fl_warnings)

enable_testing()
include(FetchContent)
FetchContent_Declare(googletest
  URL https://github.com/google/googletest/archive/refs/tags/v1.15.2.zip)
set(INSTALL_GTEST OFF CACHE BOOL "" FORCE)
FetchContent_MakeAvailable(googletest)

add_executable(fl-tests
  tests/resp_test.cpp tests/store_test.cpp tests/commands_test.cpp tests/metrics_test.cpp tests/server_test.cpp)
target_link_libraries(fl-tests PRIVATE flcore fl_warnings GTest::gtest_main)
include(GoogleTest)
gtest_discover_tests(fl-tests DISCOVERY_TIMEOUT 60)

# In-process store microbenchmark (see bench/).
add_executable(fl-store-bench ../bench/store_bench.cpp)
target_link_libraries(fl-store-bench PRIVATE flcore fl_warnings)
