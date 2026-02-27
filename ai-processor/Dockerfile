FROM golang:1.23-alpine AS builder
WORKDIR /app

COPY go.mod ./
RUN go mod download

COPY . ./
RUN CGO_ENABLED=0 GOOS=linux go build -o ai-processor .

FROM alpine:3.20
WORKDIR /app
RUN adduser -D -g '' appuser
COPY --from=builder /app/ai-processor /app/ai-processor
USER appuser
EXPOSE 8080
ENTRYPOINT ["/app/ai-processor"]
