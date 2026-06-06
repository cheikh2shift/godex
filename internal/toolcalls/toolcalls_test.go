package toolcalls

import "testing"

func TestExtractAllToolCalls_PlainJSON(t *testing.T) {
	in := `I'll do it.

{
  "name": "run_command",
  "arguments": {
    "command": "echo hi"
  }
}`

	calls := ExtractAllToolCalls(in)
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0]["name"] != "run_command" {
		t.Fatalf("expected name=run_command, got %#v", calls[0]["name"])
	}
	args, ok := calls[0]["arguments"].(map[string]any)
	if !ok {
		t.Fatalf("expected arguments map, got %#v", calls[0]["arguments"])
	}
	if args["command"] != "echo hi" {
		t.Fatalf("expected command=echo hi, got %#v", args["command"])
	}
}

func TestExtractAllToolCalls_FencedJSON(t *testing.T) {
	in := "```json\n{\"name\":\"run_command\",\"arguments\":{\"command\":\"echo hi\"}}\n```"
	calls := ExtractAllToolCalls(in)
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0]["name"] != "run_command" {
		t.Fatalf("expected name=run_command, got %#v", calls[0]["name"])
	}
}

func TestExtractAllToolCalls_JSONArray(t *testing.T) {
	in := `[{"name":"run_command","arguments":{"command":"echo a"}},{"name":"run_command","arguments":{"command":"echo b"}}]`
	calls := ExtractAllToolCalls(in)
	if len(calls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(calls))
	}
}
