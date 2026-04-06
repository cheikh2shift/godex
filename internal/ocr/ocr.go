package ocr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cheikh2shift/godex/internal/providers"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

const (
	wasmURL = "https://raw.githubusercontent.com/cheikh2shift/godex/main/packaging/ocr/ocr.wasm"
)

type OcrResult struct {
	Text       string  `json:"text"`
	Confidence float32 `json:"confidence"`
}

func EnsureRuntime(ctx context.Context) error {
	path, err := requiredPath()
	if err != nil {
		return err
	}
	if err := ensureFile(ctx, path, wasmURL); err != nil {
		return err
	}
	return nil
}

func ExtractText(ctx context.Context, imagePath string) (string, error) {
	path, err := requiredPath()
	if err != nil {
		return "", err
	}
	if !fileExists(path) {
		return "", fmt.Errorf("ocr runtime not available")
	}

	result, err := runWasm(ctx, path, imagePath)
	if err != nil {
		return "", err
	}

	if providers.DebugMode {
		fmt.Printf("[ocr] Extracted text: %s (confidence: %.2f)\n", result.Text, result.Confidence)
	}

	if result.Text == "" {
		return "No text detected in image", nil
	}

	return result.Text, nil
}

func requiredPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve home dir: %w", err)
	}
	return filepath.Join(home, ".godex", "models", "ocr", "ocr.wasm"), nil
}

func ensureFile(ctx context.Context, path, url string) error {
	if fileExists(path) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed (%s): HTTP %d", url, resp.StatusCode)
	}

	tmp := path + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}

	size := resp.ContentLength
	name := filepath.Base(path)
	if size > 0 {
		fmt.Printf("Downloading %s (%.1f MB)...\n", name, float64(size)/1e6)
	}

	_, err = io.Copy(out, &progressReader{body: resp.Body, size: size, name: name})
	if err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	fmt.Printf("Downloaded %s\n", name)
	out.Close()
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

type progressReader struct {
	body io.Reader
	size int64
	name string
	last int64
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.body.Read(p)
	r.last += int64(n)
	if r.size > 0 && r.last > 0 {
		pct := float64(r.last) / float64(r.size) * 100
		if pct >= 100 {
			pct = 100
		}
		fmt.Printf("\r  %s: %.1f%%", r.name, pct)
	}
	return n, err
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func runWasm(ctx context.Context, wasmPath, imagePath string) (OcrResult, error) {
	fmt.Printf("Loading ocr.wasm...\n")
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return OcrResult{}, err
	}
	fmt.Printf("Initializing Wasm runtime...\n")

	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)
	_, err = wasi_snapshot_preview1.Instantiate(ctx, r)
	if err != nil {
		return OcrResult{}, err
	}

	imageDir := filepath.Dir(imagePath)
	imageName := filepath.Base(imagePath)
	fsConfig := wazero.NewFSConfig()
	fsConfig = fsConfig.WithDirMount(imageDir, "/image")

	args := []string{
		"ocr",
		"/image/" + imageName,
		"/model/dummy.onnx",
	}

	fmt.Printf("Running OCR on %s...\n", imageName)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	modConfig := wazero.NewModuleConfig().
		WithStdout(&stdout).
		WithStderr(&stderr).
		WithArgs(args...).
		WithFSConfig(fsConfig)

	_, err = r.InstantiateWithConfig(ctx, wasmBytes, modConfig)
	if err != nil {
		return OcrResult{}, fmt.Errorf("ocr wasm failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	out := strings.TrimSpace(stdout.String())
	if out == "" {
		return OcrResult{}, fmt.Errorf("ocr wasm returned empty output: %s", strings.TrimSpace(stderr.String()))
	}

	var result OcrResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		return OcrResult{}, fmt.Errorf("ocr wasm invalid JSON: %w (out=%s)", err, out)
	}

	fmt.Printf("OCR complete.\n")
	return result, nil
}
