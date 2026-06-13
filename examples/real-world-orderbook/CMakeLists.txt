cmake_minimum_required(VERSION 3.28)
project(OrderBook)

set(CMAKE_EXPORT_COMPILE_COMMANDS ON)
set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

add_subdirectory(src)
add_subdirectory(demo)
add_subdirectory(tests)

add_executable(${CMAKE_PROJECT_NAME}_run main.cpp)
target_link_libraries(${CMAKE_PROJECT_NAME}_run demo_lib)
