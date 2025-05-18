FROM golang:1.23-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o ghproxy .

FROM alpine

WORKDIR /root/

# 安装skopeo
RUN apk add --no-cache skopeo && mkdir -p temp && chmod 700 temp

COPY --from=builder /app/ghproxy .
COPY --from=builder /app/config.json .
COPY --from=builder /app/public ./public

CMD ["./ghproxy"]
