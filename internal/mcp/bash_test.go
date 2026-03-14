package mcp

import (
	"os"
	"strings"
	"testing"
)

func TestBashServer_SanitizesEmptyAllowedPaths(t *testing.T) {
	tmp := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() {
		_ = os.Chdir(old)
	}()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	server := NewBashServer([]string{""})
	paths := server.AllowedPaths()
	if len(paths) == 0 || strings.TrimSpace(paths[0]) == "" {
		t.Fatalf("expected default allowed path, got: %v", paths)
	}
}
