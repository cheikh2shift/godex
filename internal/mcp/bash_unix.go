//go:build !windows

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

func killProcessGroup(targetPID int, removeTracking func()) (string, error) {
	pgid, err := syscall.Getpgid(targetPID)
	if err != nil {
		return "", err
	}
	if pgid > 0 && pgid != syscall.Getpgrp() {
		if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				removeTracking()
				return fmt.Sprintf("Process %d not running; removed from tracking", targetPID), nil
			}
			return "", err
		}
		removeTracking()
		return fmt.Sprintf("Killed process group %d", pgid), nil
	}
	return "", nil
}

// GetUnixTools returns the list of tools that are only available on Unix systems (mac, linux)
func GetUnixTools() []Tool {
	return []Tool{
		{
			Name:        "run_bash_script",
			Description: "Run a bash script and return its output. Only available on macOS and Linux.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"code":{"type":"string","description":"Bash code to run"},"timeout":{"type":"number","description":"Timeout in seconds (default 60)"}},"required":["code"]}`),
		},
	}
}

// HandleRunBashScript handles the run_bash_script command - only available on Unix
func (s *BashServer) HandleRunBashScript(ctx context.Context, args map[string]interface{}) (string, error) {
	code, ok := args["code"].(string)
	if !ok || code == "" {
		return "", fmt.Errorf("code is required")
	}

	// Get timeout (default 60 seconds)
	timeout := 60
	if timeoutVal, ok := args["timeout"].(float64); ok {
		timeout = int(timeoutVal)
	}

	// Run the bash code
	cmd := exec.Command("bash", "-c", code)
	cmd.Stdin = nil

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	output, err := cmd.CombinedOutput()

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("command timed out after %d seconds", timeout)
		}
		return string(output), err
	}

	return string(output), nil
}
