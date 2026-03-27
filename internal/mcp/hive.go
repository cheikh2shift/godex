package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cheikh-seck/godex/internal/hive"
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
			Description: "Delegate a task to another hive instance. If target_id is omitted, an instance will be selected automatically.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string"},"target_id":{"type":"string"}},"required":["prompt"]}`),
		},
	}
}

func (s *HiveServer) CallTool(ctx context.Context, name string, args map[string]interface{}) (string, error) {
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
		instances, err := s.manager.Instances()
		if err != nil {
			return "", err
		}
		var target hive.Instance
		if strings.TrimSpace(targetID) == "" {
			best, ok := pickInstance(instances, s.manager.Instance().ID)
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

func pickInstance(instances []hive.Instance, selfID string) (hive.Instance, bool) {
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
