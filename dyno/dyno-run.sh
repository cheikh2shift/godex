#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
LOG_DIR="$SCRIPT_DIR/logs"
DOCKER_IMAGE="godex-dyno:latest"
CONTAINER_NAME="godex-dyno-$$"

log_info() { echo "[DYNO] $1"; }
log_success() { echo "[DYNO] $1"; }
log_error() { echo "[DYNO] ERROR: $1"; }

build_dyno_image() {
    log_info "Building DYNO Docker image..."
    cd "$PROJECT_ROOT"
build_dyno_image() {
    log_info "Building DYNO Docker image..."
    cd "$SCRIPT_DIR"
    docker build -t "$DOCKER_IMAGE" .
    log_success "Built Docker image: $DOCKER_IMAGE"
}
    log_info "Running godex in isolated DYNO container..."
    log_info "Arguments: $args"

    if ! docker image inspect "$DOCKER_IMAGE" &>/dev/null; then
        log_info "Docker image not found. Building..."
        build_dyno_image
    fi

    mkdir -p "$LOG_DIR"

    docker run --rm \
        --name "$CONTAINER_NAME" \
        --cap-add=SYS_PTRACE \
        -v "$LOG_DIR:/dyno/logs" \
        -v "$SCRIPT_DIR/workspace:/dyno/workspace" \
        -e DYNO_MODE=true \
        -e DYNO_TRACKING_ENABLED=true \
        -e DYNO_MOCK_HTTP=true \
        -e DYNO_MOCK_FILEOPS=true \
        -e DYNO_MOCK_COMMANDS=true \
        -e DYNO_LOG_DIR=/dyno/logs \
        -e GODEX_ARGS="$args" \
        "$DOCKER_IMAGE" \
        godex $args

    log_success "DYNO execution completed"
}

show_logs() {
    log_info "=== DYNO Tracking Logs ==="
    for log in http file cmd syscall; do
        local log_file="$LOG_DIR/${log}_tracking.log"
        if [ -f "$log_file" ]; then
            echo "=== $log tracking ==="
            cat "$log_file"
        fi
    done
}

main() {
    local cmd="${1:-run}"
    shift || true

    case "$cmd" in
        build)
            build_dyno_image
            ;;
        run)
            run_dyno "$@"
            ;;
        logs)
            show_logs
            ;;
        *)
            run_dyno "$cmd $@"
            ;;
    esac
}

main "$@"
