package wizard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func TestParseOSFromFilename(t *testing.T) {
	tests := []struct {
		filename    string
		expectedOS  string
		expectedArc string
	}{
		{"llama-b8600-bin-linux-x64.tar.gz", "linux", "x64"},
		{"llama-b8600-bin-linux-arm64.tar.gz", "linux", "arm64"},
		{"llama-b8600-bin-macos-arm64.tar.gz", "macos", "arm64"},
		{"llama-b8600-bin-macos-x64.tar.gz", "macos", "x64"},
		{"llama-b8600-bin-windows-x64.tar.gz", "windows", "x64"},
		{"llama-b8600-bin-windows-arm64.tar.gz", "windows", "arm64"},
		{"llama-b8600-bin-ubuntu-x64.tar.gz", "ubuntu", "x64"},
		{"llama-b8600-bin-ubuntu-arm64.tar.gz", "ubuntu", "arm64"},
		{"llama-b8600-bin-ubuntu-s390x.tar.gz", "ubuntu", "s390x"},
		{"llama-b8600-bin-ubuntu-vulkan-x64.tar.gz", "ubuntu-vulkan", "x64"},
		{"llama-b8600-bin-ubuntu-vulkan-arm64.tar.gz", "ubuntu-vulkan", "arm64"},
		{"llama-b8600-bin-ubuntu-rocm-x64.tar.gz", "ubuntu-rocm", "x64"},
		{"llama-b8600-bin-ubuntu-openvino-2026.0-x64.tar.gz", "ubuntu-openvino-2026.0", "x64"},
		{"llama-b8600-bin-openeuler-x86.tar.gz", "openeuler", "x86"},
		{"llama-b8600-bin-openeuler-aarch64.tar.gz", "openeuler", "aarch64"},
		{"llama-server-linux-x64.tar.gz", "", ""},
		{"not-a-match", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			os, arch := parseOSFromFilename(tt.filename)
			if os != tt.expectedOS {
				t.Errorf("parseOSFromFilename(%q) os = %q, want %q", tt.filename, os, tt.expectedOS)
			}
			if arch != tt.expectedArc {
				t.Errorf("parseOSFromFilename(%q) arch = %q, want %q", tt.filename, arch, tt.expectedArc)
			}
		})
	}
}

func TestGitHubReleaseParsing(t *testing.T) {
	jsonData := `[
		{
			"tag_name": "b8600",
			"assets": [
				{
					"name": "llama-b8600-bin-linux-x64.tar.gz",
					"browser_download_url": "https://github.com/ggml-org/llama.cpp/releases/download/b8600/llama-b8600-bin-linux-x64.tar.gz"
				},
				{
					"name": "llama-b8600-bin-macos-arm64.tar.gz",
					"browser_download_url": "https://github.com/ggml-org/llama.cpp/releases/download/b8600/llama-b8600-bin-macos-arm64.tar.gz"
				}
			]
		},
		{
			"tag_name": "b8599",
			"assets": []
		}
	]`

	var releases []GitHubRelease
	err := json.Unmarshal([]byte(jsonData), &releases)
	if err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if len(releases) != 2 {
		t.Errorf("len(releases) = %d, want 2", len(releases))
	}

	if releases[0].TagName != "b8600" {
		t.Errorf("TagName = %q, want %q", releases[0].TagName, "b8600")
	}

	if len(releases[0].Assets) != 2 {
		t.Errorf("len(Assets) = %d, want 2", len(releases[0].Assets))
	}
}

func TestFilterLlamaServerAssets(t *testing.T) {
	assets := []GitHubReleaseAsset{
		{Name: "llama-b8600-bin-linux-x64.tar.gz", DownloadURL: "https://example.com/linux-x64"},
		{Name: "llama-b8600-bin-linux-arm64.tar.gz", DownloadURL: "https://example.com/linux-arm64"},
		{Name: "llama-b8600-bin-macos-arm64.tar.gz", DownloadURL: "https://example.com/macos-arm64"},
		{Name: "llama-b8600-bin-macos-x64.tar.gz", DownloadURL: "https://example.com/macos-x64"},
		{Name: "llama-b8600-bin-windows-x64.tar.gz", DownloadURL: "https://example.com/windows-x64"},
		{Name: "llama-b8600-bin-ubuntu-x64.tar.gz", DownloadURL: "https://example.com/ubuntu-x64"},
		{Name: "Source code (zip)", DownloadURL: "https://example.com/src.zip"},
	}

	var filtered []LlamaAsset
	for _, asset := range assets {
		if strings.HasSuffix(asset.Name, ".tar.gz") && strings.Contains(asset.Name, "-bin-") {
			osName, arch := parseOSFromFilename(asset.Name)
			if osName != "" && arch != "" {
				filtered = append(filtered, LlamaAsset{
					OS:       osName,
					Arch:     arch,
					FileName: asset.Name,
					URL:      asset.DownloadURL,
				})
			}
		}
	}

	if len(filtered) != 6 {
		t.Errorf("filtered assets count = %d, want 6", len(filtered))
	}

	expectedOS := map[string]bool{
		"linux":   true,
		"macos":   true,
		"windows": true,
		"ubuntu":  true,
	}

	for _, asset := range filtered {
		if !expectedOS[asset.OS] {
			t.Errorf("unexpected OS: %s", asset.OS)
		}
	}

	linuxCount := 0
	for _, asset := range filtered {
		if asset.OS == "linux" {
			linuxCount++
		}
	}
	if linuxCount != 2 {
		t.Errorf("linux assets count = %d, want 2", linuxCount)
	}
}

func TestGetOSDisplayName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"linux", "Linux"},
		{"ubuntu", "Linux"},
		{"ubuntu-vulkan", "Linux"},
		{"ubuntu-rocm", "Linux"},
		{"ubuntu-openvino-2026.0", "Linux"},
		{"darwin", "macOS"},
		{"macos", "macOS"},
		{"windows", "Windows"},
		{"openeuler", "openeuler"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := getOSDisplayName(tt.input)
			if result != tt.expected {
				t.Errorf("getOSDisplayName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestGetOSKey(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"linux", "linux"},
		{"ubuntu", "linux"},
		{"ubuntu-vulkan", "linux"},
		{"ubuntu-rocm", "linux"},
		{"darwin", "darwin"},
		{"macos", "darwin"},
		{"windows", "windows"},
		{"LINUX", "linux"},
		{"Darwin", "darwin"},
		{"openeuler", "openeuler"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := getOSKey(tt.input)
			if result != tt.expected {
				t.Errorf("getOSKey(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestGoOSToAssetOS(t *testing.T) {
	goOS := runtime.GOOS
	assetOS := goOSToAssetOS()

	switch goOS {
	case "darwin":
		if assetOS != "macos" {
			t.Errorf("goOSToAssetOS() on darwin = %q, want %q", assetOS, "macos")
		}
	case "linux":
		if assetOS != "ubuntu" {
			t.Errorf("goOSToAssetOS() on linux = %q, want %q", assetOS, "ubuntu")
		}
	case "windows":
		if assetOS != "windows" {
			t.Errorf("goOSToAssetOS() on windows = %q, want %q", assetOS, "windows")
		}
	}
}

func TestFetchLlamaReleasesFromMockServer(t *testing.T) {
	mockAPIResponse := `[
		{
			"tag_name": "b8600",
			"assets": [
				{
					"name": "llama-b8600-bin-linux-x64.tar.gz",
					"browser_download_url": "https://github.com/ggml-org/llama.cpp/releases/download/b8600/llama-b8600-bin-linux-x64.tar.gz"
				},
				{
					"name": "llama-b8600-bin-linux-arm64.tar.gz",
					"browser_download_url": "https://github.com/ggml-org/llama.cpp/releases/download/b8600/llama-b8600-bin-linux-arm64.tar.gz"
				},
				{
					"name": "llama-b8600-bin-macos-arm64.tar.gz",
					"browser_download_url": "https://github.com/ggml-org/llama.cpp/releases/download/b8600/llama-b8600-bin-macos-arm64.tar.gz"
				},
				{
					"name": "llama-b8600-bin-macos-x64.tar.gz",
					"browser_download_url": "https://github.com/ggml-org/llama.cpp/releases/download/b8600/llama-b8600-bin-macos-x64.tar.gz"
				},
				{
					"name": "llama-b8600-bin-windows-x64.tar.gz",
					"browser_download_url": "https://github.com/ggml-org/llama.cpp/releases/download/b8600/llama-b8600-bin-windows-x64.tar.gz"
				},
				{
					"name": "llama-b8600-bin-ubuntu-x64.tar.gz",
					"browser_download_url": "https://github.com/ggml-org/llama.cpp/releases/download/b8600/llama-b8600-bin-ubuntu-x64.tar.gz"
				},
				{
					"name": "Source code (zip)",
					"browser_download_url": "https://github.com/ggml-org/llama.cpp/archive/refs/tags/b8600.zip"
				}
			]
		}
	]`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(mockAPIResponse))
	}))
	defer server.Close()

	originalDo := httpDo
	httpDo = func(req *http.Request) (*http.Response, error) {
		newReq, _ := http.NewRequest(req.Method, server.URL+req.URL.Path, nil)
		newReq.Header = req.Header
		return http.DefaultClient.Do(newReq)
	}
	defer func() { httpDo = originalDo }()

	assets, err := fetchLlamaReleases()
	if err != nil {
		t.Fatalf("fetchLlamaReleases failed: %v", err)
	}

	if len(assets) != 6 {
		t.Errorf("got %d assets, want 6", len(assets))
	}

	osCounts := make(map[string]int)
	for _, asset := range assets {
		osCounts[asset.OS]++
	}

	if osCounts["linux"] != 2 {
		t.Errorf("linux assets = %d, want 2", osCounts["linux"])
	}
	if osCounts["macos"] != 2 {
		t.Errorf("macos assets = %d, want 2", osCounts["macos"])
	}
	if osCounts["windows"] != 1 {
		t.Errorf("windows assets = %d, want 1", osCounts["windows"])
	}
	if osCounts["ubuntu"] != 1 {
		t.Errorf("ubuntu assets = %d, want 1", osCounts["ubuntu"])
	}

	for _, asset := range assets {
		if asset.FileName == "" {
			t.Error("FileName should not be empty")
		}
		if asset.URL == "" {
			t.Error("URL should not be empty")
		}
		if !containsString(asset.FileName, "-bin-") {
			t.Errorf("FileName %q should contain -bin-", asset.FileName)
		}
	}
}

func TestFetchLlamaReleasesHandlesEmptyReleases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("[]"))
	}))
	defer server.Close()

	originalDo := httpDo
	httpDo = func(req *http.Request) (*http.Response, error) {
		newReq, _ := http.NewRequest(req.Method, server.URL+req.URL.Path, nil)
		newReq.Header = req.Header
		return http.DefaultClient.Do(newReq)
	}
	defer func() { httpDo = originalDo }()

	_, err := fetchLlamaReleases()
	if err == nil {
		t.Error("expected error for empty releases, got nil")
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
