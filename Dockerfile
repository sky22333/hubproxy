FROM node:24-alpine AS frontend

WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ .
RUN npm run build

FROM golang:1.26-alpine AS builder

ARG TARGETARCH
ARG VERSION=dev

WORKDIR /app
COPY src/go.mod src/go.sum ./
RUN apk add --no-cache upx && go mod download

COPY src/ .
COPY --from=frontend /src/dist ./dist

RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -ldflags="-s -w -X main.Version=${VERSION}" -trimpath -o hubproxy . && upx -9 hubproxy

FROM alpine

WORKDIR /app

COPY --from=builder /app/hubproxy .
COPY --from=builder /app/config.toml .

CMD ["./hubproxy"]
