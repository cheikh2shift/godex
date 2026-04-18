package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type SchedulerServer struct {
	scheduler *schedulerExt
}

type schedulerExt struct {
	scheduler interface {
		AddTask(prompt string, intervalSec int, runAt string, providerType, providerName, providerModel string) (interface{}, error)
		RunNow(prompt string, providerType, providerName, providerModel string) (string, error)
		StopTask(id string) bool
		RemoveTask(id string) bool
		ListTasks() []interface{}
		GetTask(id string) interface{}
	}
	mu interface {
		Lock()
		Unlock()
	}
	provider interface {
		GetProvider(cfg interface{}) (interface{}, error)
	}
	providerMu interface {
		Lock()
		Unlock()
	}
}

type TaskInfo struct {
	ID          string    `json:"id"`
	Prompt      string    `json:"prompt"`
	IntervalSec int       `json:"interval_sec"`
	RunAt       string    `json:"run_at"`
	IsRepeating bool      `json:"is_repeating"`
	CreatedAt   time.Time `json:"created_at"`
	LastRun     time.Time `json:"last_run"`
	RunCount    int       `json:"run_count"`
	LastError   string    `json:"last_error"`
}

func NewSchedulerServer(scheduler interface {
	AddTask(prompt string, intervalSec int, runAt string, providerType, providerName, providerModel string) (interface{}, error)
	RunNow(prompt string, providerType, providerName, providerModel string) (string, error)
	StopTask(id string) bool
	RemoveTask(id string) bool
	ListTasks() []interface{}
	GetTask(id string) interface{}
}, providerGetter interface {
	GetProvider(cfg interface{}) (interface{}, error)
}) *SchedulerServer {
	return &SchedulerServer{
		scheduler: &schedulerExt{
			scheduler: scheduler,
		},
	}
}

func (s *SchedulerServer) Name() string {
	return "scheduler"
}

func (s *SchedulerServer) Tools() []Tool {
	currentTime := time.Now().Format("2006-01-02 15:04:05")
	return []Tool{
		{
			Name:        "schedule_task",
			Description: fmt.Sprintf("Schedule a task to run a prompt at a specific time or at regular intervals. Current time: %s. Use interval_sec for repeating tasks (e.g., 60 for every minute, 3600 for every hour), or run_at for specific times (e.g., \"14:30\" for 2:30 PM daily, or \"now\" to execute immediately).", currentTime),
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"prompt": {
						"type": "string",
						"description": "The prompt to execute when the task runs"
					},
					"interval_sec": {
						"type": "integer",
						"description": "Interval in seconds for repeating tasks (e.g., 60, 300, 3600). Use 0 or omit for one-time task."
					},
					"run_at": {
						"type": "string",
						"description": "Specific time to run in HH:MM format (e.g., '14:30' for 2:30 PM), or 'now' to execute immediately."
					}
				},
				"required": ["prompt"]
			}`),
		},
	}
}

func (s *SchedulerServer) CallTool(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	if name != "schedule_task" {
		return "", fmt.Errorf("unknown tool: %s", name)
	}

	prompt, ok := args["prompt"].(string)
	if !ok || strings.TrimSpace(prompt) == "" {
		return "", fmt.Errorf("prompt is required")
	}

	intervalSec := 0
	if iv, ok := args["interval_sec"].(float64); ok {
		intervalSec = int(iv)
	}

	runAt := ""
	if rt, ok := args["run_at"].(string); ok {
		runAt = strings.TrimSpace(rt)
	}

	isNow := strings.ToLower(runAt) == "now"

	if intervalSec <= 0 && runAt == "" {
		return "", fmt.Errorf("must specify either interval_sec or run_at")
	}

	if isNow {
		result, err := s.scheduler.scheduler.RunNow(prompt, "unknown", "unknown", "unknown")
		if err != nil {
			return fmt.Sprintf("Error running task: %v", err), nil
		}
		return fmt.Sprintf("Task executed immediately.\n\nResult:\n%s", result), nil
	}

	task, err := s.scheduler.scheduler.AddTask(prompt, intervalSec, runAt, "unknown", "unknown", "unknown")
	if err != nil {
		return fmt.Sprintf("Error scheduling task: %v", err), nil
	}

	if task == nil {
		return "Error: task was not created", nil
	}

	taskInfo, ok := task.(TaskInfo)
	if !ok {
		return fmt.Sprintf("Task scheduled successfully but could not get ID"), nil
	}

	return fmt.Sprintf("Task scheduled successfully with ID: %s\nPrompt: %s\nInterval: %d seconds\nRun at: %s",
		taskInfo.ID, taskInfo.Prompt, taskInfo.IntervalSec, taskInfo.RunAt), nil
}

func (s *SchedulerServer) AllowedPaths() []string {
	return nil
}

func (s *SchedulerServer) AddPath(ctx context.Context, path string) error {
	return fmt.Errorf("scheduler server does not support paths")
}

func (s *SchedulerServer) TempAddPath(path string) {
}

func (s *SchedulerServer) AddURL(ctx context.Context, url string) error {
	return fmt.Errorf("scheduler server does not support URLs")
}

func (s *SchedulerServer) RemovePath(ctx context.Context, path string) error {
	return fmt.Errorf("scheduler server does not support paths")
}

func (s *SchedulerServer) RemoveURL(ctx context.Context, url string) error {
	return fmt.Errorf("scheduler server does not support URLs")
}

func (s *SchedulerServer) Close() error {
	return nil
}
