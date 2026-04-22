# DYNO Dockerfile - Builds godex and runs it in an isolated container
# that tracks all network, file, and command executions

FROM alpine:3.19 AS builder

# Install build dependencies
RUN apk add --no-cache \
    bash \
    git \
    curl \
    musl-dev \
    gcc

# Install newer Go version
RUN curl -fsSL https://go.dev/dl/go1.25.7.linux-amd64.tar.gz -o /tmp/go.tar.gz && \
    rm -rf /usr/local/go && \
    tar -C /usr/local -xzf /tmp/go.tar.gz && \
    rm /tmp/go.tar.gz

ENV PATH=/usr/local/go/bin:$PATH

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
RUN pip3 install --no-cache-dir --break-system-packages flask requests

# Create directories
RUN mkdir -p /dyno/logs /dyno/workspace /dyno/mocks /root/.godex

# Copy built binary
COPY --from=builder /build/godex /usr/local/bin/godex

# Copy dyno scripts (from dyno/ folder in build context)
COPY dyno/dyno-mock-server.py /dyno/dyno-mock-server.py
COPY dyno/dyno-entrypoint.sh /dyno/dyno-entrypoint.sh
COPY dyno/dyno.env /dyno/dyno.env
COPY dyno/dummy-provider.yaml /root/.godex/providers.yaml

RUN chmod +x /dyno/*.sh /dyno/dyno-mock-server.py

WORKDIR /dyno

# Environment for tracking
ENV DYNO_MODE=true
ENV DYNO_LOG_DIR=/dyno/logs
ENV DYNO_MOCK_HTTP=true
ENV DYNO_MOCK_FILEOPS=true
ENV DYNO_MOCK_COMMANDS=true
ENV DYNO_TRACKING_ENABLED=true
ENV CODEX_CONFIG_PATH=/root/.godex/providers.yaml
ENV CODEX_PROVIDER_NAME=dyno-mock

# Volume for outputs
VOLUME ["/dyno/logs", "/dyno/workspace"]
ENTRYPOINT ["/dyno/dyno-entrypoint.sh"]
