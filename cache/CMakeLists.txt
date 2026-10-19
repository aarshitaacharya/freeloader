cmake_minimum_required(VERSION 3.24)
project(fl-cache LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

add_compile_options(-Wall -Wextra -Werror)

if(SANITIZE STREQUAL "address")
  add_compile_options(-fsanitize=address,undefined -fno-omit-frame-pointer)
  add_link_options(-fsanitize=address,undefined)
elseif(SANITIZE STREQUAL "thread")
  add_compile_options(-fsanitize=thread)
  add_link_options(-fsanitize=thread)
endif()

add_library(flcore STATIC src/resp.cpp src/store.cpp src/server.cpp)
target_include_directories(flcore PUBLIC include)

add_executable(fl-cache src/main.cpp)
target_link_libraries(fl-cache PRIVATE flcore)

enable_testing()
include(FetchContent)
FetchContent_Declare(googletest
  URL https://github.com/google/googletest/archive/refs/tags/v1.15.2.zip)
set(gtest_force_shared_crt ON CACHE BOOL "" FORCE)
set(INSTALL_GTEST OFF CACHE BOOL "" FORCE)
FetchContent_MakeAvailable(googletest)

add_executable(fl-tests tests/resp_test.cpp tests/store_test.cpp)
target_link_libraries(fl-tests PRIVATE flcore GTest::gtest_main)
include(GoogleTest)
gtest_discover_tests(fl-tests)
