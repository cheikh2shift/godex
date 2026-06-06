package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/cheikh2shift/godex/internal/mcp"
)

type fakeProviderGetter struct {
	p any
}

func (f *fakeProviderGetter) GetProvider(cfg any) (any, error) {
	return f.p, nil
}

type fakeLLMProvider struct {
	resp string
}

func (f *fakeLLMProvider) Send(ctx context.Context, prompt string) (string, error) {
	return f.resp, nil
}
func (f *fakeLLMProvider) SetThinkCallback(cb func(string)) {}
func (f *fakeLLMProvider) SupportsNativeToolCalls() bool    { return false }

type countingToolServer struct {
	name  string
	tools []mcp.Tool
	calls int
}

func (c *countingToolServer) Name() string      { return c.name }
func (c *countingToolServer) Tools() []mcp.Tool { return c.tools }
func (c *countingToolServer) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	c.calls++
	return "ok", nil
}

func TestExecuteWithTools_DedupesIdenticalToolCallsInOneResponse(t *testing.T) {
	s := &Scheduler{}
	server := &countingToolServer{
		name: "scheduler",
		tools: []mcp.Tool{
			{Name: "scheduler", Description: "schedule"},
		},
	}
	s.servers = []ToolServer{server}
	s.provider = &fakeProviderGetter{p: &fakeLLMProvider{
		// Two identical tool calls in one response. Should execute only once.
		resp: "```json\n" +
			"{\"name\":\"scheduler\",\"arguments\":{\"prompt\":\"x\",\"interval_sec\":60}}\n" +
			"```\n" +
			"```json\n" +
			"{\"name\":\"scheduler\",\"arguments\":{\"prompt\":\"x\",\"interval_sec\":60}}\n" +
			"```",
	}}
	s.maxRounds = 1
	s.toolTimeout = 1

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	task := &ScheduledTask{
		Prompt:        "ignored",
		ProviderType:  "current",
		ProviderName:  "current",
		ProviderModel: "current",
	}

	_, err := s.executeWithTools(ctx, task)
	if err != nil {
		t.Fatalf("executeWithTools: %v", err)
	}
	if server.calls != 1 {
		t.Fatalf("expected 1 tool execution, got %d", server.calls)
	}
}
