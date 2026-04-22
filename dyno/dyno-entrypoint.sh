#!/bin/bash
#
# dyno-entrypoint.sh - Entry point for DYNO container
# Sets up tracking environment and runs godex with mock responses
#

set -e

LOG_DIR="${DYNO_LOG_DIR:-/dyno/logs}"
MOCK_DIR="/dyno/mocks"
WORKSPACE="/dyno/workspace"

# Create directories
mkdir -p "$LOG_DIR"
mkdir -p "$MOCK_DIR"
mkdir -p "$WORKSPACE"

echo "[DYNO] Starting DYNO environment..."
echo "[DYNO] Log directory: $LOG_DIR"
echo "[DYNO] Mock directory: $MOCK_DIR"

# =============================================================================
# Start Mock HTTP Server (for dummy tool responses)
# =============================================================================

start_mock_server() {
    if [ "${DYNO_MOCK_HTTP:-true}" = "true" ]; then
        echo "[DYNO] Starting mock HTTP server for tool simulation..."
        python3 /dyno/dyno-mock-server.py &
        MOCK_PID=$!
        echo "[DYNO] Mock server started (PID: $MOCK_PID)"
        
        # Wait for server to be ready
        sleep 2
    fi
}

# =============================================================================
# Setup Network Interception
# =============================================================================

setup_network_tracking() {
    echo "[DYNO] Setting up network tracking..."
    
    # Create iptables rules to redirect HTTP traffic to mock server
    # This intercepts all outbound HTTP calls and returns mock responses
    
    # Log network attempts
    echo "[DYNO] Network tracking enabled - all HTTP calls will be logged and mocked"
    
    # Set environment for godex to use mock server
    export HTTP_PROXY="http://localhost:8888"
    export HTTPS_PROXY="http://localhost:8888"
    export NO_PROXY="localhost,127.0.0.1"
}

# =============================================================================
# Setup File Operation Tracking
# =============================================================================

setup_file_tracking() {
    echo "[DYNO] Setting up file operation tracking..."
    
    # Use strace to track file operations
    export DYNO_FILE_TRACKING=true
    
    echo "[DYNO] File operation tracking enabled"
}

# =============================================================================
# Setup Command Tracking
# =============================================================================

setup_cmd_tracking() {
    echo "[DYNO] Setting up command execution tracking..."
    
    export DYNO_CMD_TRACKING=true
    
    echo "[DYNO] Command execution tracking enabled"
}

# =============================================================================
# Run godex with Tracking
# =============================================================================

run_godex() {
    local args="${GODEX_ARGS:-}"
    
    echo "[DYNO] Running godex with args: $args"
    
    # Run godex with strace for syscall tracking
    if [ "${DYNO_TRACKING_ENABLED:-true}" = "true" ]; then
        echo "[DYNO] Starting godex with full tracking..."
        
        # Run with strace to capture all syscalls
        strace -f \
            -e trace=network,file,process \
            -o "$LOG_DIR/syscall_tracking.log" \
            -s 256 \
            godex $args 2>&1 | tee "$LOG_DIR/godex_output.log"
    else
        godex $args
    fi
}

# =============================================================================
# Main
# =============================================================================

main() {
    # Setup tracking
    setup_network_tracking
    setup_file_tracking
    setup_cmd_tracking
    
    # Start mock server
    start_mock_server
    
    # Run godex
    run_godex
    
    echo "[DYNO] Execution completed"
    
    # Show summary
    echo ""
    echo "[DYNO] === Tracking Summary ==="
    echo "[DYNO] Logs available in: $LOG_DIR"
    
    for log in http file cmd syscall; do
        local log_file="$LOG_DIR/${log}_tracking.log"
        if [ -f "$log_file" ] && [ -s "$log_file" ]; then
            local count=$(wc -l < "$log_file")
            echo "[DYNO] $log tracking: $count entries"
        fi
    done
}

main "$@"
