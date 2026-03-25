FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETPLATFORM
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o godex-${TARGETOS}-${TARGETARCH} ./cmd/godex

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
    musl-dev \
    go \
    rustc \
    cargo

RUN pip3 install --no-cache-dir pip --upgrade && \
    pip3 install --no-cache-dir black flake8 pytest

ENV HOME=/root
ENV PATH=$HOME/go/bin:$PATH

COPY --from=builder /build/godex-${TARGETOS}-${TARGETARCH} /usr/local/bin/godex
RUN chmod +x /usr/local/bin/godex

WORKDIR /workspace

ENTRYPOINT ["godex"]
CMD ["--wizard"]
