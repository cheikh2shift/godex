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

RUN pip3 install --no-cache-dir pip --upgrade && \
    pip3 install --no-cache-dir black flake8 pytest

ENV HOME=/root
ENV PATH=$HOME/go/bin:$PATH

COPY godex /usr/local/bin/godex
RUN chmod +x /usr/local/bin/godex

WORKDIR /workspace

ENTRYPOINT ["godex"]
CMD ["--wizard"]
