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
    go


ENV HOME=/root
ENV PATH=$HOME/go/bin:$PATH

RUN git clone https://github.com/cheikh2shift/godex.git /tmp/godex && \
    cd /tmp/godex && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /usr/local/bin/godex ./cmd/godex && \
    rm -rf /tmp/godex

RUN chmod +x /usr/local/bin/godex

WORKDIR /workspace

ENTRYPOINT ["godex"]
