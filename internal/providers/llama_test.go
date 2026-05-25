package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cheikh2shift/godex/internal/rxcache"
)

func TestResolveModelPathLocal(t *testing.T) {
	tests := []struct {
		model    string
		wantPath bool
	}{
		{"llama.cpp", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			path, isLocal, err := resolveModelPath(tt.model)
			if err != nil && tt.model != "" {
				t.Errorf("unexpected error: %v", err)
			}
			if tt.wantPath && !isLocal {
				t.Errorf("expected local path for %q, got path=%q isLocal=%v", tt.model, path, isLocal)
			}
		})
	}
}

func TestQuantizationRegexParsing(t *testing.T) {
	re := rxcache.MustCompile(`-([A-Za-z0-9_]+)\.gguf$`)

	tests := []struct {
		filename  string
		wantQuant string
		wantMatch bool
	}{
		{"qwen2.5-3b-instruct-q4_k_m.gguf", "q4_k_m", true},
		{"model-q4_k_m.gguf", "q4_k_m", true},
		{"model-fp16.gguf", "fp16", true},
		{"model-q8_0.gguf", "q8_0", true},
		{"model.gguf", "", false},
		{"modelnoquant.gguf", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			matches := re.FindStringSubmatch(tt.filename)
			if tt.wantMatch {
				if len(matches) < 2 {
					t.Errorf("expected match for %q, got none", tt.filename)
					return
				}
				if !strings.EqualFold(matches[1], tt.wantQuant) {
					t.Errorf("got quantization %q, want %q", matches[1], tt.wantQuant)
				}
			} else {
				if len(matches) >= 2 {
					t.Errorf("expected no match for %q, got %q", tt.filename, matches[1])
				}
			}
		})
	}
}

func TestLlamaSelectBestGGUF(t *testing.T) {
	tests := []struct {
		files    []string
		expected string
	}{
		{[]string{"model-Q4_K_M.gguf", "model-Q5_K_M.gguf"}, "model-Q4_K_M.gguf"},
		{[]string{"model-Q5_K_S.gguf", "model-Q4_K_M.gguf"}, "model-Q4_K_M.gguf"},
		{[]string{"model-Q4_0.gguf", "model-Q4_K_M.gguf"}, "model-Q4_K_M.gguf"},
		{[]string{"model-Q8_0.gguf", "model-Q4_K_M.gguf"}, "model-Q4_K_M.gguf"},
		{[]string{"model-F16.gguf"}, "model-F16.gguf"},
		{[]string{"model.bin"}, ""},
		{[]string{}, ""},
		{[]string{"model.Q4_K_M.gguf"}, "model.Q4_K_M.gguf"},
		{[]string{"model-q4_k_s.gguf", "model-q3_k_m.gguf"}, "model-q4_k_s.gguf"},
	}

	for _, tt := range tests {
		t.Run(strings.Join(tt.files, ","), func(t *testing.T) {
			result := selectBestGGUF(tt.files)
			if result != tt.expected {
				t.Errorf("selectBestGGUF(%v) = %q, want %q", tt.files, result, tt.expected)
			}
		})
	}
}

func TestLlamaGetDownloadURL(t *testing.T) {
	tests := []struct {
		modelID  string
		filename string
		expected string
	}{
		{"Qwen/Qwen2.5-3B-Instruct-GGUF", "qwen2.5-3b-instruct-q4_k_m.gguf",
			"https://huggingface.co/Qwen/Qwen2.5-3B-Instruct-GGUF/resolve/main/qwen2.5-3b-instruct-q4_k_m.gguf"},
		{"mistralai/Mistral-7B", "mistral-7b-q4_k_m.gguf",
			"https://huggingface.co/mistralai/Mistral-7B/resolve/main/mistral-7b-q4_k_m.gguf"},
	}

	for _, tt := range tests {
		t.Run(tt.modelID, func(t *testing.T) {
			result := getDownloadURL(tt.modelID, tt.filename)
			if result != tt.expected {
				t.Errorf("getDownloadURL(%q, %q) = %q, want %q", tt.modelID, tt.filename, result, tt.expected)
			}
		})
	}
}

func TestSortQuantizations(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			"full sort",
			[]string{"Q2_K", "Q4_K_M", "Q8_0", "Q3_K_S", "F16", "Q5_K_M", "Q4_0"},
			[]string{"Q2_K", "Q3_K_S", "Q4_0", "Q4_K_M", "Q5_K_M", "Q8_0", "F16"},
		},
		{
			"already sorted",
			[]string{"Q2_K", "Q3_K_S", "Q4_K_M", "Q5_K_M", "Q8_0"},
			[]string{"Q2_K", "Q3_K_S", "Q4_K_M", "Q5_K_M", "Q8_0"},
		},
		{
			"reverse",
			[]string{"Q8_0", "Q5_K_M", "Q4_K_M", "Q3_K_S", "Q2_K"},
			[]string{"Q2_K", "Q3_K_S", "Q4_K_M", "Q5_K_M", "Q8_0"},
		},
		{
			"unknown quants",
			[]string{"Q9_0", "UNKNOWN", "Q2_K"},
			[]string{"Q2_K", "Q9_0", "UNKNOWN"},
		},
		{
			"empty",
			[]string{},
			[]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SortQuantizations(tt.input)
			if len(result) != len(tt.expected) {
				t.Errorf("got %d items, want %d", len(result), len(tt.expected))
				return
			}
			for i, exp := range tt.expected {
				if result[i] != exp {
					t.Errorf("position %d: got %q, want %q", i, result[i], exp)
				}
			}
		})
	}
}

func TestGetQuantizationDescription(t *testing.T) {
	tests := []struct {
		quant    string
		expected string
		wantOk   bool
	}{
		{"Q4_K_M", "Best quality/size ratio (Recommended, ~5GB for 7B)", true},
		{"Q2_K", "Lowest quality, smallest size (~3GB for 7B)", true},
		{"Q8_0", "Full quality, large size (~10GB for 7B)", true},
		{"F16", "Full precision, very large (~14GB for 7B)", true},
		{"UNKNOWN", "", false},
		{"", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.quant, func(t *testing.T) {
			result := GetQuantizationDescription(tt.quant)
			if tt.wantOk && result == "" {
				t.Errorf("expected description for %q, got empty", tt.quant)
			}
			if !tt.wantOk && result != "" {
				t.Errorf("expected no description for %q, got %q", tt.quant, result)
			}
			if tt.wantOk && result != tt.expected {
				t.Errorf("GetQuantizationDescription(%q) = %q, want %q", tt.quant, result, tt.expected)
			}
		})
	}
}

func TestLlamaGetGGUFFilesIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	files, err := getGGUFFiles(ctx, "Qwen/Qwen2.5-3B-Instruct-GGUF")
	if err != nil {
		t.Skipf("could not fetch GGUF files: %v", err)
	}

	if len(files) == 0 {
		t.Fatal("expected at least one GGUF file")
	}

	t.Logf("Found %d GGUF files:", len(files))
	for _, f := range files[:min(5, len(files))] {
		t.Logf("  - %s", f)
	}
}

func TestLlamaGetGGUFFilesIntegrationNotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := getGGUFFiles(ctx, "nonexistent/model-does-not-exist-12345")
	if err == nil {
		t.Error("expected error for nonexistent model, got nil")
	}
}

type mockServer struct {
	*httptest.Server
	modelResp    string
	propsResp    string
	healthStatus int
	slotsResp    string
}

func newMockLlamaServer(t *testing.T) *mockServer {
	m := &mockServer{
		healthStatus: 200,
		modelResp:    `{"data":[{"id":"test-model","context_length":8192}]}`,
		propsResp:    `{"default_generation_settings":{"n_ctx":8192}}`,
		slotsResp:    `[]`,
	}

	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(m.healthStatus)
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(m.modelResp))
		case "/props":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(m.propsResp))
		case "/slots":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(m.slotsResp))
		default:
			http.NotFound(w, r)
		}
	}))

	return m
}

func (m *mockServer) close() {
	m.Server.Close()
}

func TestFetchModelInfoFromMockServer(t *testing.T) {
	server := newMockLlamaServer(t)
	defer server.close()

	client := &http.Client{Timeout: 10 * time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/models", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("failed to fetch models: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		t.Fatalf("server returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}

	var result struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int    `json:"context_length"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if len(result.Data) == 0 {
		t.Fatal("expected at least one model")
	}

	if result.Data[0].ContextLength != 8192 {
		t.Errorf("expected context_length 8192, got %d", result.Data[0].ContextLength)
	}
}

func TestFetchContextFromPropsMockServer(t *testing.T) {
	server := newMockLlamaServer(t)
	defer server.close()

	client := &http.Client{Timeout: 10 * time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/props", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("failed to fetch props: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}

	var result struct {
		DefaultGenerationSettings struct {
			NCtx int `json:"n_ctx"`
		} `json:"default_generation_settings"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if result.DefaultGenerationSettings.NCtx != 8192 {
		t.Errorf("expected n_ctx 8192, got %d", result.DefaultGenerationSettings.NCtx)
	}
}

func TestLlamaServerHealthCheck(t *testing.T) {
	tests := []struct {
		name         string
		healthStatus int
		wantErr      bool
	}{
		{"healthy", 200, false},
		{"degraded", 429, false},
		{"server error", 500, true},
		{"internal error", 503, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newMockLlamaServer(t)
			server.healthStatus = tt.healthStatus
			defer server.close()

			client := &http.Client{Timeout: 10 * time.Second}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/health", nil)
			if err != nil {
				t.Fatalf("failed to create request: %v", err)
			}

			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			gotErr := resp.StatusCode >= 500
			if gotErr != tt.wantErr {
				t.Errorf("health check returned error=%v, want error=%v", gotErr, tt.wantErr)
			}
		})
	}
}

func TestGetSlotInfoMockServer(t *testing.T) {
	server := newMockLlamaServer(t)
	defer server.close()

	slotsResp := `[
		{"id":0,"is_processing":false,"n_ctx":8192,"id_task":0,"next_token":[]},
		{"id":1,"is_processing":true,"n_ctx":8192,"id_task":123,"next_token":[{"n_decoded":50}]}
	]`
	server.slotsResp = slotsResp

	client := &http.Client{Timeout: 10 * time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/slots", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("failed to fetch slots: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}

	var slots []slotInfo
	if err := json.Unmarshal(body, &slots); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if len(slots) != 2 {
		t.Errorf("expected 2 slots, got %d", len(slots))
	}

	if slots[1].IsProcessing != true {
		t.Errorf("expected slot 1 to be processing")
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{500, "500 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{52428800, "50.0 MB"},
		{1073741824, "1.0 GB"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := formatBytes(tt.input)
			if result != tt.expected {
				t.Errorf("formatBytes(%d) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestFindFreePort(t *testing.T) {
	port1, err := findFreePort()
	if err != nil {
		t.Fatalf("findFreePort() failed: %v", err)
	}

	port2, err := findFreePort()
	if err != nil {
		t.Fatalf("findFreePort() failed: %v", err)
	}

	if port1 == port2 {
		t.Errorf("expected different ports, got %d and %d", port1, port2)
	}

	if port1 < 1024 || port1 > 65535 {
		t.Errorf("port %d out of valid range", port1)
	}
}

func TestFileExists(t *testing.T) {
	if fileExists("nonexistent-file-12345.txt") {
		t.Error("expected file to not exist")
	}

	if !fileExists(".") {
		t.Error("expected directory to exist")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestParseContextValueFromHTML(t *testing.T) {
	re := rxcache.MustCompile(`(\d+)[Kk]\s*context\s*window`)

	tests := []struct {
		html     string
		expected int
		wantErr  bool
	}{
		{"128K context window", 128000, false},
		{"32k context window", 32000, false},
		{"8K context window", 8000, false},
		{"no context here", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.html, func(t *testing.T) {
			matches := re.FindStringSubmatch(tt.html)
			if tt.wantErr {
				if len(matches) >= 2 {
					t.Errorf("expected no match, got %q", matches[1])
				}
				return
			}

			if len(matches) < 2 {
				t.Fatalf("no match found")
			}

			val, err := strconv.Atoi(matches[1])
			if err != nil {
				t.Fatalf("strconv.Atoi() failed: %v", err)
			}

			result := val * 1000
			if result != tt.expected {
				t.Errorf("got %d, want %d", result, tt.expected)
			}
		})
	}
}
