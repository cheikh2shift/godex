package providers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cheikh2shift/godex/internal/rxcache"
)

func TestParseOllamaLibraryContextRegex(t *testing.T) {
	tests := []struct {
		name     string
		html     string
		expected int
		wantErr  bool
	}{
		{
			name:     "context window with K suffix lowercase",
			html:     `<html><body><div>128k context window</div></body></html>`,
			expected: 128000,
			wantErr:  false,
		},
		{
			name:     "context window with K suffix uppercase",
			html:     `<html><body><div>32K context window</div></body></html>`,
			expected: 32000,
			wantErr:  false,
		},
		{
			name:     "context window with spaces between",
			html:     `<html><body><div>8K    context    window</div></body></html>`,
			expected: 8000,
			wantErr:  false,
		},
		{
			name:     "context window in complex HTML",
			html:     `<html><body><div class="model-info"><span>Some model</span><p>32K context window for this model</p></div></body></html>`,
			expected: 32000,
			wantErr:  false,
		},
		{
			name:     "no context window in HTML",
			html:     `<html><body><div>No context window here</div></body></html>`,
			expected: 0,
			wantErr:  true,
		},
		{
			name:     "empty HTML",
			html:     ``,
			expected: 0,
			wantErr:  true,
		},
		{
			name:     "context window at end of file",
			html:     `<html><body><div class="info">Model parameters</div><p>64k context window</p></body></html>`,
			expected: 64000,
			wantErr:  false,
		},
		{
			name:     "multiple K values - first match",
			html:     `<html><body><div>128K context window</div><p>32K other metric</p></body></html>`,
			expected: 128000,
			wantErr:  false,
		},
	}

	re := rxcache.MustCompile(`(\d+)[Kk]\s*context\s*window`)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := re.FindStringSubmatch(tt.html)
			if tt.wantErr {
				if len(matches) >= 2 {
					t.Errorf("expected no match, got %q", matches[1])
				}
				return
			}

			if len(matches) < 2 {
				t.Fatalf("expected match, got none")
			}

			val, err := strconv.Atoi(matches[1])
			if err != nil {
				t.Fatalf("strconv.Atoi failed: %v", err)
			}
			val *= 1000

			if val != tt.expected {
				t.Errorf("got context value %d, want %d", val, tt.expected)
			}
		})
	}
}

func TestExtractContextFromOllamaHTML(t *testing.T) {
	re := rxcache.MustCompile(`(\d+)[Kk]\s*context\s*window`)

	realisticHTML := `
	<!DOCTYPE html>
	<html lang="en">
	<head><title>Llama 3.2 - Ollama Library</title></head>
	<body>
		<main>
			<div class="repository-header">
				<h1>llama3.2</h1>
			</div>
			<div class="tag-list">
				<div class="tag">
					<span class="name">3.2</span>
					<div class="size">2.0 GB</div>
					<div class="meta">
						<span class="modified">2 months ago</span>
					</div>
				</div>
			</div>
			<div class="model-info">
				<p>The Llama 3.2 collection offers a 128K context window for handling long documents.</p>
				<p>Supports multilingual text generation.</p>
			</div>
		</main>
	</body>
	</html>
	`

	matches := re.FindStringSubmatch(realisticHTML)
	if len(matches) < 2 {
		t.Fatal("expected to find context window in realistic HTML")
	}

	val, err := strconv.Atoi(matches[1])
	if err != nil {
		t.Fatalf("strconv.Atoi failed: %v", err)
	}

	if val != 128 {
		t.Errorf("expected 128, got %d", val)
	}

	contextVal := val * 1000
	if contextVal != 128000 {
		t.Errorf("expected context limit 128000, got %d", contextVal)
	}
}

func TestExtractContextFromOllamaHTMLVariations(t *testing.T) {
	variations := []struct {
		name     string
		html     string
		expected int
	}{
		{
			name:     "k lowercase",
			html:     `<div>64k context window supported</div>`,
			expected: 64000,
		},
		{
			name:     "K uppercase",
			html:     `<div>16K context window available</div>`,
			expected: 16000,
		},
		{
			name:     "mixed case numbers",
			html:     `<div>4k context window</div>`,
			expected: 4000,
		},
		{
			name:     "with newlines",
			html:     "<div>256k\ncontext\nwindow</div>",
			expected: 256000,
		},
	}

	re := rxcache.MustCompile(`(\d+)[Kk]\s*context\s*window`)

	for _, v := range variations {
		t.Run(v.name, func(t *testing.T) {
			matches := re.FindStringSubmatch(v.html)
			if len(matches) < 2 {
				t.Fatalf("expected match for variation: %s", v.name)
			}

			val, _ := strconv.Atoi(matches[1])
			contextVal := val * 1000

			if contextVal != v.expected {
				t.Errorf("got %d, want %d", contextVal, v.expected)
			}
		})
	}
}

func TestExtractContextNoMatch(t *testing.T) {
	noMatchCases := []string{
		`<div>context window without number</div>`,
		`<div>128 context</div>`,
		`<div>128k window context</div>`,
		`<div>contexting window</div>`,
		`<div>window context</div>`,
		`<div>just numbers 128K here</div>`,
	}

	re := rxcache.MustCompile(`(\d+)[Kk]\s*context\s*window`)

	for _, html := range noMatchCases {
		matches := re.FindStringSubmatch(html)
		if len(matches) >= 2 {
			t.Errorf("expected no match for %q, got %q", html, matches[0])
		}
	}
}

func TestModelNameExtraction(t *testing.T) {
	tests := []struct {
		model    string
		expected string
	}{
		{"llama3.2:3b", "llama3.2"},
		{"llama3.2", "llama3.2"},
		{"nomic-embed-text:v1.5", "nomic-embed-text"},
		{"qwen2.5-coder:3b", "qwen2.5-coder"},
		{"minimax-m2.7:cloud", "minimax-m2.7"},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			modelName := tt.model
			if idx := strings.Index(modelName, ":"); idx > 0 {
				modelName = modelName[:idx]
			}

			if modelName != tt.expected {
				t.Errorf("got %q, want %q", modelName, tt.expected)
			}
		})
	}
}

func TestBuildOllamaLibraryURL(t *testing.T) {
	tests := []struct {
		model    string
		expected string
	}{
		{"llama3.2", "https://ollama.com/library/llama3.2/tags"},
		{"llama3.2:3b", "https://ollama.com/library/llama3.2/tags"},
		{"nomic-embed-text:v1.5", "https://ollama.com/library/nomic-embed-text/tags"},
		{"qwen2.5-coder", "https://ollama.com/library/qwen2.5-coder/tags"},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			modelName := tt.model
			if idx := strings.Index(modelName, ":"); idx > 0 {
				modelName = modelName[:idx]
			}

			url := "https://ollama.com/library/" + modelName + "/tags"

			if url != tt.expected {
				t.Errorf("got %q, want %q", url, tt.expected)
			}
		})
	}
}

func TestFetchOllamaLibraryContextIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tests := []struct {
		model      string
		wantMinCtx int
	}{
		{"llama3.2", 128000},
		{"llama3", 8000},
		{"mistral", 32000},
		{"qwen2.5-coder", 32000},
		{"nomic-embed-text", 2000},
	}

	client := &http.Client{Timeout: 30 * time.Second}
	re := rxcache.MustCompile(`(\d+)[Kk]\s*context\s*window`)

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			url := fmt.Sprintf("https://ollama.com/library/%s/tags", tt.model)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				t.Fatalf("failed to create request: %v", err)
			}

			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("failed to fetch %s: %v", url, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode >= 400 {
				t.Fatalf("ollama.com returned status %d", resp.StatusCode)
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("failed to read response body: %v", err)
			}

			matches := re.FindStringSubmatch(string(body))
			if len(matches) < 2 {
				t.Fatalf("could not find context window in HTML for model %s", tt.model)
			}

			val, err := strconv.Atoi(matches[1])
			if err != nil {
				t.Fatalf("failed to parse context value: %v", err)
			}

			contextLimit := val * 1000
			if contextLimit < tt.wantMinCtx {
				t.Errorf("context limit %d for %s is less than expected minimum %d",
					contextLimit, tt.model, tt.wantMinCtx)
			}

			t.Logf("Model %s: %dK context window (%d tokens)", tt.model, val, contextLimit)
		})
	}
}

func TestGetOllamaContextLimitIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	models := []string{"llama3.2", "mistral", "qwen2.5"}

	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			limit, err := GetOllamaContextLimit(model)
			if err != nil {
				t.Skipf("could not get context limit for %s: %v", model, err)
			}

			if limit <= 0 {
				t.Errorf("expected positive context limit for %s, got %d", model, limit)
			}

			t.Logf("Model %s: context limit = %d", model, limit)
		})
	}
}

func TestGetOllamaContextLimitNonexistent(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	_, err := GetOllamaContextLimit("nonexistent-model-12345-xyz")
	if err == nil {
		t.Error("expected error for nonexistent model, got nil")
	}
}
