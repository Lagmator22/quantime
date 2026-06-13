FROM golang:1.24-alpine
WORKDIR /app
COPY main.go .
RUN go build -o engine main.go
EXPOSE 9001
CMD ["./engine"]