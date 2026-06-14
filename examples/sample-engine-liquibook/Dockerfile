FROM alpine:latest AS build
RUN apk add --no-cache g++ make curl git
WORKDIR /app
# Liquibook — Object Computing's canonical open-source C++ matching engine.
RUN git clone --depth 1 https://github.com/objectcomputing/liquibook /liquibook
RUN curl -fsSLO https://raw.githubusercontent.com/yhirose/cpp-httplib/master/httplib.h
COPY server.cpp .
RUN g++ -O2 -std=c++17 -pthread -I/liquibook/src server.cpp /liquibook/src/simple/simple_order.cpp -o engine

FROM alpine:latest
RUN apk add --no-cache libstdc++
COPY --from=build /app/engine /usr/local/bin/engine
EXPOSE 9005
CMD ["engine"]
