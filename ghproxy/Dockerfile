FROM golang:1.23-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main .

FROM alpine:3.20

WORKDIR /root/

# 安装skopeo
RUN apk add --no-cache skopeo

# 创建临时目录
RUN mkdir -p ./temp && chmod 777 ./temp

COPY --from=builder /app/main .
COPY --from=builder /app/config.json .
COPY --from=builder /app/public ./public

CMD ["./main"]