# DYNO Dockerfile - Builds godex and runs it in an isolated container
# that tracks all network, file, and command executions

FROM alpine:3.19 AS builder

# Install build dependencies
RUN apk add --no-cache \
    bash \
    git \
    curl \
    go \
    musl-dev \
    gcc

# Build godex from source (build context is project root)
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o godex ./cmd/godex

# Final runtime image
FROM alpine:3.19

RUN apk add --no-cache \
    bash \
    python3 \
    py3-pip \
    curl \
    wget \
    strace \
    tcpdump \
    netcat-openbsd \
    iptables \
    tree \
    jq

# Install mock HTTP server dependencies
RUN pip3 install --no-cache-dir flask requests

# Create directories
RUN mkdir -p /dyno/logs /dyno/workspace /dyno/mocks

# Copy built binary
COPY --from=builder /build/godex /usr/local/bin/godex

# Copy dyno scripts
COPY dyno-mock-server.py /dyno/dyno-mock-server.py
COPY dyno-entrypoint.sh /dyno/dyno-entrypoint.sh
COPY dyno.env /dyno/dyno.env

RUN chmod +x /dyno/*.sh /dyno/dyno-mock-server.py

WORKDIR /dyno

# Environment for tracking
ENV DYNO_MODE=true
ENV DYNO_LOG_DIR=/dyno/logs
ENV DYNO_MOCK_HTTP=true
ENV DYNO_MOCK_FILEOPS=true
ENV DYNO_MOCK_COMMANDS=true
ENV DYNO_TRACKING_ENABLED=true

# Volume for outputs
VOLUME ["/dyno/logs", "/dyno/workspace"]
ENTRYPOINT ["/dyno/dyno-entrypoint.sh"]
