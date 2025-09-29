FROM golang:1.25-alpine AS builder

ARG TARGETARCH

WORKDIR /app
COPY src/go.mod src/go.sum ./
RUN go mod download && apk add upx

COPY src/ .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -ldflags="-s -w" -trimpath -o hubproxy . && upx -9 hubproxy

FROM alpine

WORKDIR /root/

COPY --from=builder /app/hubproxy .
COPY --from=builder /app/config.toml .

CMD ["./hubproxy"]