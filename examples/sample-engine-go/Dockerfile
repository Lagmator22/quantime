# Submission Dockerfile — minimal, scratch-based, no shell, no package
# manager. Total image weighs ~8MB. Judges who submit their own code
# should aim for the same — image size is a tiebreaker signal.
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY main.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/engine .

FROM scratch
COPY --from=build /out/engine /engine
EXPOSE 9001
ENV PORT=9001
ENTRYPOINT ["/engine"]
