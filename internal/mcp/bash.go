package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func GetWorkingDir() (string, error) {
	return os.Getwd()
}

type BashServer struct {
	allowedPaths []string
	tools        []Tool
}

func NewBashServer(allowedPaths []string) *BashServer {
	if len(allowedPaths) == 0 {
		wd, _ := GetWorkingDir()
		allowedPaths = []string{wd}
	}

	return &BashServer{
		allowedPaths: allowedPaths,
		tools: []Tool{
			{
				Name:        "run_command",
				Description: "Run a shell command and return its output",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"Shell command to run"},"timeout":{"type":"number","description":"Timeout in seconds (default 60)"}},"required":["command"]}`),
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

	timeout := 60
	if t, ok := args["timeout"].(float64); ok {
		timeout = int(t)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	output, err := cmd.CombinedOutput()

	if err != nil {
		return fmt.Sprintf("Error: %v\nOutput: %s", err, string(output)), nil
	}

	return string(output), nil
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
	for _, p := range s.allowedPaths {
		if p == path {
			return nil
		}
	}
	s.allowedPaths = append(s.allowedPaths, path)
	return nil
}

func (s *BashServer) AllowedPaths() []string {
	return s.allowedPaths
}

func (s *BashServer) Close() error {
	return nil
}
