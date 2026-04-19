package mcp

import (
	"context"
	"strings"
	"testing"
)

type fakeScheduler struct {
	lastIsRepeating bool
}

type fakeTask struct {
	ID          string `json:"id"`
	Prompt      string `json:"prompt"`
	IntervalSec int    `json:"interval_sec"`
	RunAt       string `json:"run_at"`
	IsRepeating bool   `json:"is_repeating"`
}

func (f *fakeScheduler) AddTask(prompt string, intervalSec int, runAt string, providerType, providerName, providerModel string) (interface{}, error) {
	return f.AddTaskWithRepeat(prompt, intervalSec, runAt, intervalSec > 0, providerType, providerName, providerModel)
}

func (f *fakeScheduler) AddTaskWithRepeat(prompt string, intervalSec int, runAt string, isRepeating bool, providerType, providerName, providerModel string) (interface{}, error) {
	f.lastIsRepeating = isRepeating
	return &fakeTask{ID: "ABCD", Prompt: prompt, IntervalSec: intervalSec, RunAt: runAt, IsRepeating: isRepeating}, nil
}

func (f *fakeScheduler) StopTask(id string) bool       { return true }
func (f *fakeScheduler) RemoveTask(id string) bool     { return true }
func (f *fakeScheduler) ListTasks() []interface{}      { return nil }
func (f *fakeScheduler) GetTask(id string) interface{} { return nil }

func TestSchedulerTool_RunOnceForcesNonRepeating(t *testing.T) {
	fs := &fakeScheduler{}
	server := NewSchedulerServer(fs, nil)

	_, err := server.CallTool(context.Background(), "scheduler", map[string]interface{}{
		"prompt":       "hi",
		"interval_sec": float64(60),
		"run_once":     true,
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if fs.lastIsRepeating {
		t.Fatalf("expected isRepeating=false when run_once=true")
	}
}

func TestSchedulerTool_SchemaIncludesRunOnce(t *testing.T) {
	server := NewSchedulerServer(&fakeScheduler{}, nil)
	tools := server.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if !strings.Contains(string(tools[0].InputSchema), `"run_once"`) {
		t.Fatalf("expected InputSchema to include run_once, got: %s", string(tools[0].InputSchema))
	}
}
