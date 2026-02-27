FROM golang:1.23-alpine AS builder
WORKDIR /app

COPY go.mod ./
RUN go mod download

COPY . ./
RUN CGO_ENABLED=0 GOOS=linux go build -o nats-agent .

FROM alpine:3.20
WORKDIR /app
RUN adduser -D -g '' appuser
COPY --from=builder /app/nats-agent /app/nats-agent
USER appuser
EXPOSE 9090
ENTRYPOINT ["/app/nats-agent"]
