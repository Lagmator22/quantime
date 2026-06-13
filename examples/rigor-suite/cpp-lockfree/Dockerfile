FROM alpine:latest
RUN apk add --no-cache netcat-openbsd
EXPOSE 9001
CMD ["sh", "-c", "while true; do echo -e 'HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{\"latencyNs\": 1000, \"fills\": []}' | nc -l -p 9001; done"]