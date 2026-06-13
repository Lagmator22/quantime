find_package(GTest REQUIRED)

add_executable(Google_Tests_run OrderBookTest.cpp)

target_link_libraries(Google_Tests_run 
    PRIVATE
        ${CMAKE_PROJECT_NAME}_lib
        GTest::gtest_main
)
