package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func GetWorkingDir() (string, error) {
	return os.Getwd()
}

type BashServer struct {
	allowedPaths   []string
	tools          []Tool
	backgroundPIDs []bgProcess
}

type bgProcess struct {
	PID     int
	Command string
}

func NewBashServer(allowedPaths []string) *BashServer {
	allowedPaths = sanitizeAllowedPaths(allowedPaths)
	allowedPaths = withDefaultCwd(allowedPaths)

	return &BashServer{
		allowedPaths: allowedPaths,
		tools: []Tool{
			{
				Name:        "run_command",
				Description: "Run a shell command and return its output. Use background: true to run in background with nohup.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"Shell command to run"},"timeout":{"type":"number","description":"Timeout in seconds (default 60)"},"background":{"type":"boolean","description":"Run in background using nohup (default false)"}},"required":["command"]}`),
			},
			{
				Name:        "kill_command",
				Description: "Kill a background process by PID (returned from run_command with background: true)",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"pid":{"type":"number","description":"Process ID to kill"}},"required":["pid"]}`),
			},
			{
				Name:        "run_python",
				Description: "Run a Python script and return its output",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"code":{"type":"string","description":"Python code to run"},"timeout":{"type":"number","description":"Timeout in seconds (default 30)"}},"required":["code"]}`),
			},
			{
				Name:        "run_node",
				Description: "Run a Node.js script and return its output",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"code":{"type":"string","description":"Node.js code to run"},"timeout":{"type":"number","description":"Timeout in seconds (default 30)"}},"required":["code"]}`),
			},
		},
	}
}

func (s *BashServer) Tools() []Tool {
	return s.tools
}

func (s *BashServer) CallTool(ctx context.Context, name string, arguments map[string]interface{}) (string, error) {
	switch name {
	case "run_command":
		return s.runCommand(arguments)
	case "kill_command":
		return s.killCommand(arguments)
	case "kill_all_background":
		return s.killAllBackground(arguments)
	case "run_python":
		return s.runPython(arguments)
	case "run_node":
		return s.runNode(arguments)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

func (s *BashServer) isPathAllowed(path string) bool {
	if path == "" {
		return true
	}
	for _, allowed := range s.allowedPaths {
		if strings.HasPrefix(path, allowed) || path == allowed {
			return true
		}
	}
	return false
}

func (s *BashServer) runCommand(args map[string]interface{}) (string, error) {
	command, ok := args["command"].(string)
	if !ok {
		return "", fmt.Errorf("command is required")
	}

	timeout := 180 // default 3 minutes
	if t, ok := args["timeout"].(float64); ok {
		timeout = int(t)
	}
	// Cap at 5 minutes max to prevent hanging
	if timeout > 300 {
		timeout = 300
	}

	background := false
	if b, ok := args["background"].(bool); ok {
		background = b
	}

	// Detect manual background operator (& at end of command)
	trimmed := strings.TrimSpace(command)
	if strings.HasSuffix(trimmed, "&") {
		background = true
		command = strings.TrimSuffix(trimmed, "&")
	}

	// Run in background with nohup
	if background {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
		defer cancel()

		// Use nohup to run in background, redirect output to nohup.out, and capture the PID.
		// Wrap the command in sh -c so shell operators like `cd` work.
		cmd := exec.CommandContext(ctx, "sh", "-c", "nohup sh -c \"$1\" > nohup.out 2>&1 & echo $!", "sh", command)
		cmd.Stdin = nil // Close stdin to prevent interactive prompts from blocking
		output, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("failed to start background command: %v (output: %s)", err, strings.TrimSpace(string(output)))
		}

		pidStr := strings.TrimSpace(string(output))
		pid, err := strconv.Atoi(pidStr)
		if err != nil || pid <= 0 {
			return "", fmt.Errorf("failed to parse background PID from output: %q", pidStr)
		}

		s.backgroundPIDs = append(s.backgroundPIDs, bgProcess{
			PID:     pid,
			Command: strings.TrimSpace(command),
		})
		return fmt.Sprintf("Started background process (PID: %d)\nOutput will be in nohup.out", pid), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stdin = nil // Close stdin to prevent interactive prompts from blocking

	output, err := cmd.CombinedOutput()

	// Cancel after command completes (either success or error)
	cancel()

	if err != nil {
		// Check if it was a timeout
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Sprintf("TIMEOUT: Command exceeded %d seconds and was killed", timeout), nil
		}
		return fmt.Sprintf("Error: %v\nOutput: %s", err, string(output)), nil
	}

	return string(output), nil
}

func (s *BashServer) killCommand(args map[string]interface{}) (string, error) {
	pid, ok := args["pid"].(float64)
	if !ok {
		return "", fmt.Errorf("pid is required")
	}

	targetPID := int(pid)
	removeTracking := func() {
		for i, p := range s.backgroundPIDs {
			if p.PID == targetPID {
				s.backgroundPIDs = append(s.backgroundPIDs[:i], s.backgroundPIDs[i+1:]...)
				break
			}
		}
	}
	killedGroup := false
	if pgid, err := syscall.Getpgid(targetPID); err == nil && pgid > 0 && pgid != syscall.Getpgrp() {
		if err := syscall.Kill(-pgid, syscall.SIGKILL); err == nil {
			killedGroup = true
		} else if errors.Is(err, syscall.ESRCH) {
			removeTracking()
			return fmt.Sprintf("Process %d not running; removed from tracking", targetPID), nil
		}
	}
	if !killedGroup {
		proc, err := os.FindProcess(targetPID)
		if err != nil {
			removeTracking()
			return fmt.Sprintf("Process %d not running; removed from tracking", targetPID), nil
		}

		err = proc.Kill()
		if err != nil {
			if errors.Is(err, syscall.ESRCH) {
				removeTracking()
				return fmt.Sprintf("Process %d not running; removed from tracking", targetPID), nil
			}
			return "", fmt.Errorf("failed to kill process: %v", err)
		}
	}

	removeTracking()

	if killedGroup {
		return fmt.Sprintf("Killed process group for PID %d", targetPID), nil
	}
	return fmt.Sprintf("Killed process %d", targetPID), nil
}

func (s *BashServer) killAllBackground(args map[string]interface{}) (string, error) {
	if len(s.backgroundPIDs) == 0 {
		return "No background processes running", nil
	}

	var killed []int
	var failed []int
	for _, procInfo := range s.backgroundPIDs {
		killedGroup := false
		if pgid, err := syscall.Getpgid(procInfo.PID); err == nil && pgid > 0 && pgid != syscall.Getpgrp() {
			if err := syscall.Kill(-pgid, syscall.SIGKILL); err == nil {
				killedGroup = true
			} else if errors.Is(err, syscall.ESRCH) {
				killedGroup = true
			}
		}
		if killedGroup {
			killed = append(killed, procInfo.PID)
			continue
		}

		proc, err := os.FindProcess(procInfo.PID)
		if err == nil {
			if err := proc.Kill(); err == nil {
				killed = append(killed, procInfo.PID)
			} else if errors.Is(err, syscall.ESRCH) {
				killed = append(killed, procInfo.PID)
			} else {
				failed = append(failed, procInfo.PID)
			}
		} else {
			failed = append(failed, procInfo.PID)
		}
	}

	s.backgroundPIDs = nil

	msg := fmt.Sprintf("Killed %d processes: %v", len(killed), killed)
	if len(failed) > 0 {
		msg += fmt.Sprintf("\nFailed to kill: %v", failed)
	}
	return msg, nil
}

func (s *BashServer) KillAllBackground() (string, error) {
	return s.killAllBackground(nil)
}

func (s *BashServer) runPython(args map[string]interface{}) (string, error) {
	code, ok := args["code"].(string)
	if !ok {
		return "", fmt.Errorf("code is required")
	}

	timeout := 30
	if t, ok := args["timeout"].(float64); ok {
		timeout = int(t)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "python3", "-c", code)
	output, err := cmd.CombinedOutput()

	if err != nil {
		return fmt.Sprintf("Error: %v\nOutput: %s", err, string(output)), nil
	}

	return string(output), nil
}

func (s *BashServer) runNode(args map[string]interface{}) (string, error) {
	code, ok := args["code"].(string)
	if !ok {
		return "", fmt.Errorf("code is required")
	}

	timeout := 30
	if t, ok := args["timeout"].(float64); ok {
		timeout = int(t)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "node", "-e", code)
	output, err := cmd.CombinedOutput()

	if err != nil {
		return fmt.Sprintf("Error: %v\nOutput: %s", err, string(output)), nil
	}

	return string(output), nil
}

func (s *BashServer) AddPath(ctx context.Context, path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	for _, p := range s.allowedPaths {
		if p == path {
			return nil
		}
	}
	s.allowedPaths = append(s.allowedPaths, path)
	return nil
}

func (s *BashServer) AddURL(ctx context.Context, url string) error {
	return fmt.Errorf("bash server does not support URLs")
}

func (s *BashServer) AllowedPaths() []string {
	return s.allowedPaths
}

func (s *BashServer) Close() error {
	return nil
}

func (s *BashServer) ListBackground() (string, error) {
	if len(s.backgroundPIDs) == 0 {
		return "No background processes", nil
	}
	var pids []string
	for _, procInfo := range s.backgroundPIDs {
		proc, err := os.FindProcess(procInfo.PID)
		if err == nil {
			err = proc.Signal(nil) // Check if process is alive
			status := "running"
			if err != nil {
				status = "not running"
			}
			if procInfo.Command != "" {
				pids = append(pids, fmt.Sprintf("PID %d (%s) - %s", procInfo.PID, status, procInfo.Command))
			} else {
				pids = append(pids, fmt.Sprintf("PID %d (%s)", procInfo.PID, status))
			}
		} else {
			if procInfo.Command != "" {
				pids = append(pids, fmt.Sprintf("PID %d - %s", procInfo.PID, procInfo.Command))
			} else {
				pids = append(pids, fmt.Sprintf("PID %d", procInfo.PID))
			}
		}
	}
	if len(pids) == 0 {
		return "No background processes", nil
	}
	return "Background processes: " + strings.Join(pids, ", "), nil
}

func (s *BashServer) BackgroundCount() int {
	return len(s.backgroundPIDs)
}

func (s *BashServer) PruneBackground() ([]int, error) {
	var alive []bgProcess
	var removed []int
	for _, procInfo := range s.backgroundPIDs {
		proc, err := os.FindProcess(procInfo.PID)
		if err != nil {
			removed = append(removed, procInfo.PID)
			continue
		}
		if err := proc.Signal(nil); err != nil {
			if errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
				removed = append(removed, procInfo.PID)
				continue
			}
		}
		alive = append(alive, procInfo)
	}
	s.backgroundPIDs = alive
	return removed, nil
}
