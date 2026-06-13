# =====================================================================
# IICPC GATEWAY · multi-stage build, static binary, scratch runtime
# ---------------------------------------------------------------------
# Static linking + scratch base → ~12MB image. Pulls a Docker client
# library so it can talk to the host's Docker socket and spawn
# developer containers as siblings.
# =====================================================================
FROM golang:1.22-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum* ./
RUN go mod download || true
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/gateway ./cmd

FROM alpine:3.19
RUN apk add --no-cache ca-certificates docker-cli
WORKDIR /app
COPY --from=build /out/gateway /usr/local/bin/gateway
EXPOSE 7070
ENTRYPOINT ["/usr/local/bin/gateway"]
