FROM golang:1.24-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum* ./
RUN go mod download || true
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/telemetry ./cmd

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=build /out/telemetry /usr/local/bin/telemetry
ENTRYPOINT ["/usr/local/bin/telemetry"]
