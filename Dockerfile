FROM golang:1.24-alpine AS builder

WORKDIR /app
COPY src/go.mod src/go.sum ./
RUN go mod download

COPY src/ .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -trimpath -o hubproxy .

FROM alpine

WORKDIR /root/

COPY --from=builder /app/hubproxy .
COPY --from=builder /app/config.toml .

CMD ["./hubproxy"]
