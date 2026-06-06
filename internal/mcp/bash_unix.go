//go:build !windows

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
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

func setProcessGroup(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// GetUnixTools returns the list of tools that are only available on Unix systems (mac, linux)
func GetUnixTools() []Tool {
	return []Tool{
		{
			Name:        "run_bash_script",
			Description: "Run a bash script and return its output. For servers/services/long-running scripts, you MUST set background: true. Only available on macOS and Linux.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"code":{"type":"string","description":"Bash code to run"},"timeout":{"type":"number","description":"Timeout in seconds (default 60)"},"background":{"type":"boolean","description":"Run in background as a goroutine (default false)"}},"required":["code"]}`),
		},
	}
}

// HandleRunBashScript handles the run_bash_script command - only available on Unix
func (s *BashServer) HandleRunBashScript(ctx context.Context, args map[string]any) (string, error) {
	code, ok := args["code"].(string)
	if !ok || code == "" {
		return "", fmt.Errorf("code is required")
	}

	// Get timeout (default 60 seconds)
	timeout := 60
	if timeoutVal, ok := args["timeout"].(float64); ok {
		timeout = int(timeoutVal)
	}
	// Cap at 5 minutes max to prevent hanging
	if timeout > 300 {
		timeout = 300
	}

	background := false
	if b, ok := args["background"].(bool); ok {
		background = b
	}

	// Detect manual background operator (& at end)
	trimmed := strings.TrimSpace(code)
	if strings.HasSuffix(trimmed, "&") {
		background = true
		code = strings.TrimSpace(strings.TrimSuffix(trimmed, "&"))
	}

	if background {
		return s.runBackgroundJob(ctx, "bash -c "+shellQuote(code), timeout)
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

func shellQuote(input string) string {
	if input == "" {
		return "''"
	}
	if !strings.ContainsAny(input, " \t\n'\"\\$&;|<>`(){}[]*?!") {
		return input
	}
	return "'" + strings.ReplaceAll(input, "'", `'\'\'`) + "'"
}
