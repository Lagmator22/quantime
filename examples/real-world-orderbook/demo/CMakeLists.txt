add_library(demo_lib STATIC demo.cpp)

target_link_libraries(demo_lib 
    PUBLIC 
        ${CMAKE_PROJECT_NAME}_lib
)