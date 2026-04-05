package providers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

type llamaServerProgressWriter struct {
	total       int64
	downloaded  int64
	lastPercent int
}

func (pw *llamaServerProgressWriter) Write(p []byte) (int, error) {
	n := len(p)
	pw.downloaded += int64(n)
	if pw.total > 0 {
		percent := int(float64(pw.downloaded) / float64(pw.total) * 100)
		if percent != pw.lastPercent && percent <= 100 {
			pw.lastPercent = percent
			barWidth := 40
			filled := (barWidth * percent) / 100
			bar := strings.Repeat("=", filled) + strings.Repeat(" ", barWidth-filled)
			fmt.Printf("\r[%s] %d%% (%.1f MB)", bar, percent, float64(pw.downloaded)/1024/1024)
			if percent == 100 {
				fmt.Println()
			}
		}
	}
	return n, nil
}

// CheckOrInstallLlamaServer ensures llama-server is available, prompting to download if missing.
func CheckOrInstallLlamaServer(reader *bufio.Reader) error {
	if _, err := detectLlamaServer(); err == nil {
		return nil
	}

	fmt.Println()
	fmt.Println("llama-server not found in PATH or ~/.godex/")
	fmt.Println("To use llama.cpp, you need to install llama-server.")
	fmt.Println()

	install := promptYesNo(reader, "Would you like to download and install llama-server now? (y/N)", false)
	if !install {
		return fmt.Errorf("llama-server not installed")
	}

	fmt.Println("Fetching available llama-server releases...")
	assets, err := fetchLlamaReleases()
	if err != nil {
		return fmt.Errorf("failed to fetch releases: %w", err)
	}

	if len(assets) == 0 {
		return fmt.Errorf("no llama-server releases found")
	}

	fmt.Println()
	fmt.Println("Available downloads:")
	fmt.Println()

	osGroups := make(map[string][]LlamaAsset)
	for _, asset := range assets {
		dispName := getOSDisplayName(asset.OS)
		osGroups[dispName] = append(osGroups[dispName], asset)
	}

	var options []string
	optionToAsset := make(map[int]LlamaAsset)
	idx := 1

	currentAssetOS := goOSToAssetOS()
	currentOSGroup := getOSDisplayName(currentAssetOS)

	if assets, ok := osGroups[currentOSGroup]; ok {
		fmt.Printf("%s:\n", currentOSGroup)
		for _, asset := range assets {
			fmt.Printf("  %d. %s (current OS)\n", idx, asset.FileName)
			optionToAsset[idx] = asset
			options = append(options, fmt.Sprintf("%s: %s", currentOSGroup, asset.FileName))
			idx++
		}
		fmt.Println()
	}

	fmt.Print("Select an option (number)")
	var defaultChoice int
	for i := range options {
		assetObj := optionToAsset[i+1]
		if getOSKey(assetObj.OS) == getOSKey(currentAssetOS) {
			defaultChoice = i + 1
			break
		}
	}
	if defaultChoice == 0 {
		defaultChoice = 1
	}

	fmt.Printf(" [default: %d]: ", defaultChoice)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)
	if input == "" {
		input = strconv.Itoa(defaultChoice)
	}

	choice, err := strconv.Atoi(input)
	if err != nil || choice < 1 || choice > len(options) {
		return fmt.Errorf("invalid selection")
	}

	selectedAsset := optionToAsset[choice]
	downloadURL := selectedAsset.URL
	isZip := strings.HasSuffix(selectedAsset.FileName, ".zip")

	homeDir, _ := os.UserHomeDir()
	godexDir := filepath.Join(homeDir, ".godex")
	installDir := filepath.Join(godexDir, "bin")

	if err := os.MkdirAll(installDir, 0755); err != nil {
		return fmt.Errorf("failed to create install directory: %w", err)
	}

	installPath := filepath.Join(installDir, "llama-server")

	resp, err := http.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("failed to download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return fmt.Errorf("download failed: asset not found (404)")
	}

	if resp.StatusCode != 200 {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	totalSize := resp.ContentLength
	fmt.Printf("Downloading %s...\n", downloadURL)
	if totalSize > 0 {
		fmt.Printf("Total size: %.1f MB\n", float64(totalSize)/1024/1024)
	}
	fmt.Println()

	ext := ".tar.gz"
	if isZip {
		ext = ".zip"
	}
	tmpFile := filepath.Join(os.TempDir(), "llama-server"+ext)
	outFile, err := os.Create(tmpFile)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	writer := &llamaServerProgressWriter{total: totalSize}
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := outFile.Write(buf[:n]); werr != nil {
				outFile.Close()
				return fmt.Errorf("failed to write: %w", werr)
			}
			writer.Write(buf[:n])
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			outFile.Close()
			return fmt.Errorf("failed to read: %w", err)
		}
	}
	outFile.Close()

	fmt.Println("Download complete. Extracting...")

	if isZip {
		extractCmd := exec.Command("unzip", "-o", tmpFile, "-d", installDir)
		if err := extractCmd.Run(); err != nil {
			os.Remove(tmpFile)
			return fmt.Errorf("failed to extract: %w", err)
		}
	} else {
		extractCmd := exec.Command("tar", "-xzf", tmpFile, "-C", installDir)
		if err := extractCmd.Run(); err != nil {
			os.Remove(tmpFile)
			return fmt.Errorf("failed to extract: %w", err)
		}
	}

	os.Remove(tmpFile)

	entries, err := os.ReadDir(installDir)
	if err != nil {
		return fmt.Errorf("failed to list extracted files: %w", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if strings.Contains(name, "llama-server") && !entry.IsDir() {
			extractedPath := filepath.Join(installDir, name)
			if installPath != extractedPath {
				if err := os.Rename(extractedPath, installPath); err != nil {
					if err := os.Chmod(extractedPath, 0755); err != nil {
						return fmt.Errorf("failed to set permissions: %w", err)
					}
				}
			}
			if err := os.Chmod(installPath, 0755); err != nil {
				return fmt.Errorf("failed to set permissions: %w", err)
			}
			break
		}
	}

	fmt.Printf("llama-server installed to %s\n", installPath)
	fmt.Println("Make sure ~/.godex/bin/llama-server is in your PATH")

	return nil
}

type LlamaAsset struct {
	OS       string
	Arch     string
	FileName string
	URL      string
}

func parseOSFromFilename(filename string) (os, arch string) {
	binMarker := "-bin-"

	binIdx := strings.Index(filename, binMarker)
	if binIdx == -1 {
		return "", ""
	}

	suffixIdx := -1
	for _, s := range []string{".tar.gz", ".zip"} {
		idx := strings.Index(filename, s)
		if idx != -1 && (suffixIdx == -1 || idx < suffixIdx) {
			suffixIdx = idx
		}
	}
	if suffixIdx == -1 {
		return "", ""
	}

	middle := filename[binIdx+len(binMarker) : suffixIdx]

	lastDash := strings.LastIndex(middle, "-")
	if lastDash == -1 {
		return "", ""
	}

	os = middle[:lastDash]
	arch = middle[lastDash+1:]

	return os, arch
}

type GitHubReleaseAsset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
}

type GitHubRelease struct {
	TagName string               `json:"tag_name"`
	Assets  []GitHubReleaseAsset `json:"assets"`
}

var httpDo = func(req *http.Request) (*http.Response, error) {
	client := &http.Client{}
	return client.Do(req)
}

func fetchLlamaReleases() ([]LlamaAsset, error) {
	req, err := http.NewRequest("GET", "https://api.github.com/repos/ggml-org/llama.cpp/releases", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "godex")

	resp, err := httpDo(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("failed to fetch releases: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var releases []GitHubRelease
	if err := json.Unmarshal(body, &releases); err != nil {
		return nil, fmt.Errorf("failed to parse releases data: %w", err)
	}

	if len(releases) == 0 {
		return nil, fmt.Errorf("no releases found")
	}

	release := releases[0]

	var assets []LlamaAsset
	for _, asset := range release.Assets {
		isValidExtension := strings.HasSuffix(asset.Name, ".tar.gz") || strings.HasSuffix(asset.Name, ".zip")
		if isValidExtension && strings.Contains(asset.Name, "-bin-") {
			osName, arch := parseOSFromFilename(asset.Name)
			if osName != "" && arch != "" {
				assets = append(assets, LlamaAsset{
					OS:       osName,
					Arch:     arch,
					FileName: asset.Name,
					URL:      asset.DownloadURL,
				})
			}
		}
	}

	return assets, nil
}

func getOSDisplayName(osName string) string {
	switch {
	case osName == "linux" || strings.HasPrefix(osName, "ubuntu"):
		return "Linux"
	case osName == "darwin" || osName == "macos":
		return "macOS"
	case osName == "windows" || strings.HasPrefix(osName, "win"):
		return "Windows"
	default:
		return osName
	}
}

func goOSToAssetOS() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	case "linux":
		return "ubuntu"
	case "windows":
		return "win"
	default:
		return runtime.GOOS
	}
}

func getOSKey(osName string) string {
	lower := strings.ToLower(osName)
	switch {
	case lower == "linux" || strings.HasPrefix(lower, "ubuntu"):
		return "linux"
	case lower == "darwin" || lower == "macos":
		return "darwin"
	case strings.HasPrefix(lower, "win"):
		return "windows"
	default:
		return lower
	}
}

func promptYesNo(reader *bufio.Reader, question string, def bool) bool {
	fmt.Print(question + " ")
	input, _ := reader.ReadString('\n')
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" {
		return def
	}
	return strings.HasPrefix(input, "y")
}
