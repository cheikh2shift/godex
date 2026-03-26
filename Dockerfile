FROM alpine:3.19

RUN apk add --no-cache \
    bash \
    python3 \
    py3-pip \
    nodejs \
    npm \
    git \
    curl \
    wget \
    vim \
    nano \
    tar \
    gzip \
    unzip \
    zip \
    htop \
    tree \
    jq \
    ripgrep \
    fd \
    fzf \
    make \
    cmake \
    gcc \
    g++ \
    musl-dev

RUN curl -fsSL https://go.dev/dl/go1.25.7.linux-amd64.tar.gz -o /tmp/go.tar.gz && \
    rm -rf /usr/local/go && \
    tar -C /usr/local -xzf /tmp/go.tar.gz && \
    rm /tmp/go.tar.gz

ENV PATH=/usr/local/go/bin:$PATH

RUN addgroup -g 10001 godex && \
    adduser -D -u 10001 -G godex -h /godex-home godex && \
    mkdir -p /godex-home/.godex && \
    chown -R godex:godex /godex-home

RUN git clone https://github.com/cheikh2shift/godex.git /tmp/godex && \
    cd /tmp/godex && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /usr/local/bin/godex ./cmd/godex && \
    rm -rf /tmp/godex

RUN chmod +x /usr/local/bin/godex

RUN printf '%s\n' \
  '#!/bin/sh' \
  'set -eu' \
  '' \
  'CONFIG_PATH="${CODEX_CONFIG_PATH:-/godex-home/.godex/providers.yaml}"' \
  'PROVIDER_NAME="${CODEX_PROVIDER_NAME:-}"' \
  '' \
  'if [ -f "$CONFIG_PATH" ]; then' \
  '  if [ -n "$PROVIDER_NAME" ]; then' \
  '    exec godex --config "$CONFIG_PATH" --provider "$PROVIDER_NAME"' \
  '  fi' \
  '  exec godex --config "$CONFIG_PATH"' \
  'fi' \
  '' \
  'exec godex --wizard' \
  > /usr/local/bin/godex-entrypoint.sh && chmod +x /usr/local/bin/godex-entrypoint.sh

ENV HOME=/godex-home

WORKDIR /workspace

USER godex

ENTRYPOINT ["/usr/local/bin/godex-entrypoint.sh"]
