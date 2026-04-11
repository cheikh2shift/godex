package providers

import (
	"context"
	"fmt"
	"testing"
)

func TestGetGGUFFiles(t *testing.T) {
	ctx := context.Background()

	files, err := getGGUFFiles(ctx, "Qwen/Qwen2.5-3B-Instruct-GGUF")
	if err != nil {
		t.Fatalf("Failed to get GGUF files: %v", err)
	}

	if len(files) == 0 {
		t.Fatal("Expected GGUF files, got none")
	}

	t.Logf("Found %d GGUF files:", len(files))
	for _, f := range files {
		t.Logf("  - %s", f)
	}
}

func TestGetGGUFFilesNotExist(t *testing.T) {
	ctx := context.Background()

	_, err := getGGUFFiles(ctx, "nonexistent/model-does-not-exist-12345")
	if err == nil {
		t.Fatal("Expected error for nonexistent model, got nil")
	}
	t.Logf("Got expected error: %v", err)
}

func TestSelectBestGGUF(t *testing.T) {
	tests := []struct {
		files    []string
		expected string
	}{
		{[]string{"model-Q4_K_M.gguf", "model-Q5_K_M.gguf", "model-F16.gguf"}, "model-Q4_K_M.gguf"},
		{[]string{"model-Q5_K_S.gguf", "model-Q4_K_M.gguf"}, "model-Q4_K_M.gguf"},
		{[]string{"model-Q4_0.gguf"}, "model-Q4_0.gguf"},
		{[]string{"model.bin"}, ""},
		{[]string{}, ""},
		{[]string{"model.Q4_K_M.gguf"}, "model.Q4_K_M.gguf"},
	}

	for _, tt := range tests {
		result := selectBestGGUF(tt.files)
		if result != tt.expected {
			t.Errorf("selectBestGGUF(%v) = %q, want %q", tt.files, result, tt.expected)
		}
	}
}

func TestGetDownloadURL(t *testing.T) {
	url := getDownloadURL("Qwen/Qwen2.5-3B-Instruct-GGUF", "qwen2.5-3b-instruct-q4_k_m.gguf")
	expected := "https://huggingface.co/Qwen/Qwen2.5-3B-Instruct-GGUF/resolve/main/qwen2.5-3b-instruct-q4_k_m.gguf"
	if url != expected {
		t.Errorf("getDownloadURL() = %q, want %q", url, expected)
	}
}

func TestResolveModelPathWithDownload(t *testing.T) {
	t.Skip("Skipping download test - requires user input for quantization selection")
}

func TestDownloadProgress(t *testing.T) {
	p := DownloadProgress{
		Downloaded: 1024 * 1024 * 50,
		Total:      1024 * 1024 * 100,
		Filename:   "test-model-q4_k_m.gguf",
	}

	percent := float64(p.Downloaded) / float64(p.Total) * 100
	fmt.Printf("Download progress: %.1f%% (%d / %d bytes)\n", percent, p.Downloaded, p.Total)

	if p.Downloaded > p.Total {
		t.Error("Downloaded should not exceed total")
	}
}
