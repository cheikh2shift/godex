package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cheikh2shift/godex/internal/mcp"
)

type fakeToolServer struct {
	name  string
	tools []mcp.Tool
}

func (f *fakeToolServer) Name() string { return f.name }
func (f *fakeToolServer) Tools() []mcp.Tool {
	return f.tools
}
func (f *fakeToolServer) CallTool(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	if name == "run_command" {
		return "ok", nil
	}
	return "bad:" + name, nil
}

func TestScheduler_GetToolsDescription_MatchesMainFormat(t *testing.T) {
	s := &Scheduler{}
	s.SetServers([]interface{}{
		&fakeToolServer{
			name: "bash",
			tools: []mcp.Tool{
				{Name: "run_command", Description: "Run a shell command"},
			},
		},
	})

	desc := s.getToolsDescription()
	if desc == "" {
		t.Fatalf("expected non-empty tools description")
	}
	if want := "[bash]"; !strings.Contains(desc, want) {
		t.Fatalf("expected tools description to contain %q, got: %s", want, desc)
	}
	if want := "run_command"; !strings.Contains(desc, want) {
		t.Fatalf("expected tools description to contain %q, got: %s", want, desc)
	}
}

func TestScheduler_CallTool_FindsTool(t *testing.T) {
	s := &Scheduler{}
	s.SetServers([]interface{}{
		&fakeToolServer{
			name: "bash",
			tools: []mcp.Tool{
				{Name: "run_command", Description: "Run a shell command"},
			},
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	out, err := s.callTool(ctx, "run_command", map[string]interface{}{"command": "echo hi"}, 1)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if out != "ok" {
		t.Fatalf("expected ok, got %q", out)
	}
}

func TestScheduler_CallTool_ServerNameCompatibility(t *testing.T) {
	s := &Scheduler{}
	s.SetServers([]interface{}{
		&fakeToolServer{
			name: "bash",
			tools: []mcp.Tool{
				{Name: "run_command", Description: "Run a shell command"},
			},
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	out, err := s.callTool(ctx, "Bash", map[string]interface{}{"command": "echo hi"}, 1)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if out != "ok" {
		t.Fatalf("expected ok, got %q", out)
	}
}
