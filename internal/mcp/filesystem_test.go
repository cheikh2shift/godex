package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileSystemServer_DefaultAllowsCwd(t *testing.T) {
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

	filePath := filepath.Join(tmp, "hello.txt")
	if err := os.WriteFile(filePath, []byte("hi"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	server := NewFileSystemServer(nil, false)
	out, err := server.CallTool(context.Background(), "list_directory", map[string]any{
		"path": ".",
	})
	if err != nil {
		t.Fatalf("list_directory failed: %v", err)
	}
	if !strings.Contains(out, "hello.txt") {
		t.Fatalf("expected listing to include hello.txt, got: %s", out)
	}
}

func TestFileSystemServer_SanitizesEmptyAllowedPaths(t *testing.T) {
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

	server := NewFileSystemServer([]string{""}, false)
	paths := server.AllowedPaths()
	if len(paths) == 0 || strings.TrimSpace(paths[0]) == "" {
		t.Fatalf("expected default allowed path, got: %v", paths)
	}
}

func TestFileSystemServer_ListDirectory_DoesNotPanicOnInfoError(t *testing.T) {
	tmp := t.TempDir()

	// Create a dangling symlink so DirEntry.Info() returns an error.
	linkPath := filepath.Join(tmp, "dangling-link")
	if err := os.Symlink(filepath.Join(tmp, "does-not-exist"), linkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	server := NewFileSystemServer([]string{tmp}, false)
	out, err := server.CallTool(context.Background(), "list_directory", map[string]any{
		"path": tmp,
	})
	if err != nil {
		t.Fatalf("list_directory failed: %v", err)
	}
	if !strings.Contains(out, "dangling-link") {
		t.Fatalf("expected listing to include dangling-link, got: %s", out)
	}
}
