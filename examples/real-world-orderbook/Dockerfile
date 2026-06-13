FROM alpine:latest AS build
RUN apk add --no-cache g++ make curl
WORKDIR /app
RUN curl -fsSLO https://raw.githubusercontent.com/yhirose/cpp-httplib/master/httplib.h
COPY src/ src/
COPY server.cpp .
RUN g++ -O3 -std=c++20 -pthread server.cpp src/*.cpp -o engine

FROM alpine:latest
RUN apk add --no-cache libstdc++
WORKDIR /app
COPY --from=build /app/engine .
EXPOSE 9004
CMD ["./engine"]
