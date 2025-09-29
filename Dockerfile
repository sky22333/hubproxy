FROM alpine

WORKDIR /root/

ENV BINARY_NAME=hubproxy

ARG TARGETOS
ARG TARGETARCH

COPY src/config.toml .
COPY build/hubproxy/${BINARY_NAME}-${TARGETOS}-${TARGETARCH} ${BINARY_NAME}

CMD ["./hubproxy"]