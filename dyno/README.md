# DYNO - Docker-based Dynamometer Test Harness for godex

> Like a dynamometer ("dyno") used to test cars and engines under controlled
> conditions, this harness tests godex in an isolated Docker container.

## Overview

DYNO runs godex in an isolated Docker container that tracks all:
- **Network operations** (HTTP requests via mock server)
- **File operations** (open, read, write, stat via strace)
- **Command executions** (system, execve)
- **Syscalls** (via strace)

All external network interactions return **dummy responses** to simulate tool calling.

## Quick Start

```bash
# Build the Docker image
./dyno-run.sh build

# Run godex with dyno
./dyno-run.sh run "your prompt here"

# View tracking logs
./dyno-run.sh logs
```

## Directory Structure

```
dyno/
├── Dockerfile              # Docker image definition
├── dyno-run.sh             # Main test harness script
├── dyno-entrypoint.sh      # Container entry point
├── dyno-mock-server.py     # Mock HTTP server for tool simulation
├── dyno.env                # Environment configuration
├── README.md               # This file
├── logs/                   # Tracking logs (created at runtime)
│   ├── http_tracking.log
│   ├── file_tracking.log
│   ├── cmd_tracking.log
│   ├── syscall_tracking.log
│   └── godex_output.log
└── workspace/              # Shared workspace directory
```

## How It Works

### Docker Build
- Builds godex from source code inside the Docker container
- Installs all necessary tracking tools (strace, tcpdump, etc.)
- Creates isolated environment for testing

### Mock HTTP Server
- Runs on port 8888 inside the container
- Intercepts all HTTP requests from godex
- Returns dummy responses that simulate AI tool calling
- Logs all requests to `http_tracking.log`

### Tracking
- **Network**: All HTTP calls logged and mocked
- **File**: strace captures file operations
- **Commands**: All execve/syscall tracked
- **Syscalls**: Full syscall trace to `syscall_tracking.log`

## Mock Responses

The mock server simulates tool calling by:
1. Detecting when godex makes an API call with tools
2. Returning a mock tool call response
3. Then returning a mock tool result when godex follows up

This allows testing godex's tool-calling behavior without real API calls.

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `DYNO_MODE` | Enable DYNO mode | true |
| `DYNO_TRACKING_ENABLED` | Enable tracking | true |
| `DYNO_MOCK_HTTP` | Mock HTTP requests | true |
| `DYNO_MOCK_PORT` | Mock server port | 8888 |
| `DYNO_LOG_DIR` | Log directory | /dyno/logs |

## Requirements

- Docker
- bash
- Project source code (go.mod)

## Notes

- All network calls are intercepted and mocked
- No real API calls are made
- Perfect for testing and development
- Logs are persisted to host directory
