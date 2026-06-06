package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cheikh2shift/godex/internal/hive"
)

type HiveServer struct {
	manager *hive.Manager
}

func NewHiveServer(manager *hive.Manager) *HiveServer {
	return &HiveServer{manager: manager}
}

func (s *HiveServer) Name() string {
	return "hive"
}

func (s *HiveServer) Tools() []Tool {
	return []Tool{
		{
			Name:        "hive_list",
			Description: "List available hive instances",
			InputSchema: json.RawMessage(`{"type":"object","properties":{},"required":[]}`),
		},
		{
			Name:        "hive_delegate",
			Description: "Delegate a task to another hive instance. If target_id is omitted, selects the instance with the best matching MCP server skills for the task. Specify required_tools to pick an agent with those capabilities.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string","description":"The task to delegate"},"target_id":{"type":"string","description":"Specific instance ID to delegate to (optional)"},"required_tools":{"type":"array","items":{"type":"string"},"description":"MCP server names required for this task (optional)"}},"required":["prompt"]}`),
		},
	}
}

func (s *HiveServer) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	switch name {
	case "hive_list":
		instances, err := s.manager.Instances()
		if err != nil {
			return "", err
		}
		data, _ := json.MarshalIndent(instances, "", "  ")
		return string(data), nil
	case "hive_delegate":
		prompt, _ := args["prompt"].(string)
		prompt = strings.TrimSpace(prompt)
		if prompt == "" {
			return "", fmt.Errorf("prompt is required")
		}
		s.manager.RecordDelegate(prompt)
		targetID, _ := args["target_id"].(string)

		var requiredTools []string
		if toolsRaw, ok := args["required_tools"].([]any); ok {
			for _, t := range toolsRaw {
				if s, ok := t.(string); ok {
					requiredTools = append(requiredTools, s)
				}
			}
		}

		instances, err := s.manager.Instances()
		if err != nil {
			return "", err
		}
		var target hive.Instance
		if strings.TrimSpace(targetID) == "" {
			best, ok := pickInstance(instances, s.manager.Instance().ID, requiredTools)
			if !ok {
				return "", fmt.Errorf("no other hive instances available")
			}
			target = best
		} else {
			if targetID == s.manager.Instance().ID {
				return "", fmt.Errorf("cannot delegate a task to the same instance")
			}
			found := false
			for _, inst := range instances {
				if inst.ID == targetID {
					target = inst
					found = true
					break
				}
			}
			if !found {
				return "", fmt.Errorf("instance not found: %s", targetID)
			}
		}
		statusName := target.Name
		if strings.TrimSpace(statusName) == "" {
			statusName = target.ID
		}
		s.manager.Status(fmt.Sprintf("Hive: awaiting results (%s)", statusName))
		if err := s.manager.DelegateAsync(ctx, target.ID, prompt); err != nil {
			s.manager.Status("Hive: idle")
			return "", err
		}
		return fmt.Sprintf("Hive: delegated to %s", statusName), nil
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

func (s *HiveServer) AllowedPaths() []string {
	return nil
}

func (s *HiveServer) AddPath(ctx context.Context, path string) error {
	return fmt.Errorf("hive server does not support paths")
}

func (s *HiveServer) TempAddPath(path string) {}

func (s *HiveServer) AddURL(ctx context.Context, url string) error {
	return fmt.Errorf("hive server does not support urls")
}

func (s *HiveServer) RemovePath(ctx context.Context, path string) error {
	return fmt.Errorf("hive server does not support paths")
}

func (s *HiveServer) RemoveURL(ctx context.Context, url string) error {
	return fmt.Errorf("hive server does not support urls")
}

func (s *HiveServer) Close() error {
	return nil
}

func pickInstance(instances []hive.Instance, selfID string, requiredTools []string) (hive.Instance, bool) {
	hasRequired := len(requiredTools) == 0

	if hasRequired {
		var best hive.Instance
		found := false
		for _, inst := range instances {
			if inst.ID == selfID {
				continue
			}
			if !found || inst.MaxTokens > best.MaxTokens {
				best = inst
				found = true
			}
		}
		return best, found
	}

	var bestMatch hive.Instance
	bestMatchCount := -1
	found := false

	for _, inst := range instances {
		if inst.ID == selfID {
			continue
		}
		matchCount := countMatchingTools(inst.MCPServers, requiredTools)
		if matchCount > bestMatchCount {
			bestMatch = inst
			bestMatchCount = matchCount
			found = true
		}
	}

	if found && bestMatchCount > 0 {
		return bestMatch, true
	}

	var best hive.Instance
	bestFound := false
	for _, inst := range instances {
		if inst.ID == selfID {
			continue
		}
		if !bestFound || inst.MaxTokens > best.MaxTokens {
			best = inst
			bestFound = true
		}
	}
	return best, bestFound
}

func countMatchingTools(available, required []string) int {
	count := 0
	availableSet := make(map[string]bool)
	for _, s := range available {
		availableSet[s] = true
	}
	for _, s := range required {
		if availableSet[s] {
			count++
		}
	}
	return count
}
