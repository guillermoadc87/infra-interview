FROM golang:1.21-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o api-service .

# Final stage - running as root (not ideal!)
FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/api-service .

# No health check
# No non-root user
# No resource limits

EXPOSE 8080

CMD ["./api-service"]
