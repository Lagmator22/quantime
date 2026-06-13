# IICPC AI ANALYZER: multi-stage build, minimal runtime
# Static Go binary calling Gemini API for code analysis.
# No special OS deps needed. Alpine base for ca-certificates.
FROM golang:1.22-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum* ./
RUN go mod download || true
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/ai-analyzer ./cmd

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/ai-analyzer /usr/local/bin/ai-analyzer
EXPOSE 7080
ENTRYPOINT ["/usr/local/bin/ai-analyzer"]
