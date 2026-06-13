set(SOURCES
        Book.cpp
        Limit.cpp
        Order.cpp
        Book.h
        Limit.h
        Order.h
        Trade.h
        Types.h
)

add_library(${CMAKE_PROJECT_NAME}_lib STATIC ${SOURCES})

target_include_directories(${CMAKE_PROJECT_NAME}_lib 
    PUBLIC 
        ${CMAKE_CURRENT_SOURCE_DIR}
)