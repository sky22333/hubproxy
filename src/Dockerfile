FROM golang:1.24-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -trimpath -o hubproxy .

FROM alpine

WORKDIR /root/

# 安装skopeo
RUN apk add --no-cache skopeo && mkdir -p temp && chmod 700 temp

COPY --from=builder /app/hubproxy .
COPY --from=builder /app/config.toml .
COPY --from=builder /app/public ./public

CMD ["./hubproxy"]
