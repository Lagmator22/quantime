FROM alpine:3.19 AS build
RUN apk add --no-cache g++ make curl
WORKDIR /app
# Download cpp-httplib single header
RUN curl -fsSLO https://raw.githubusercontent.com/yhirose/cpp-httplib/master/httplib.h
COPY main.cpp .
RUN g++ -O3 -std=c++11 -pthread main.cpp -o engine

FROM alpine:3.19
RUN apk add --no-cache libstdc++
WORKDIR /app
COPY --from=build /app/engine .
EXPOSE 9002
CMD ["./engine"]
