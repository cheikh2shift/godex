package vision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
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
	wasmURL   = "https://raw.githubusercontent.com/cheikh2shift/godex/main/packaging/vision/vision.wasm"
	modelURL  = "https://huggingface.co/onnxmodelzoo/efficientnet-lite4-11/resolve/main/model/efficientnet-lite4-11.onnx"
	labelsURL = "https://raw.githubusercontent.com/anishathalye/imagenet-simple-labels/master/imagenet-simple-labels.json"
)

type Summary struct {
	Primary string
	Top5    []LabelScore
	Stats   ImageStats
}

type LabelScore struct {
	Label string  `json:"label"`
	Score float32 `json:"score"`
}

type ImageStats struct {
	Width      int
	Height     int
	Aspect     float64
	Brightness float64
}

type wasmResult struct {
	Label string       `json:"label"`
	Top5  []LabelScore `json:"top5"`
}

func EnsureRuntime(ctx context.Context) error {
	paths, err := requiredPaths()
	if err != nil {
		return err
	}
	if err := ensureFile(ctx, paths.WasmPath, wasmURL); err != nil {
		return err
	}
	if err := ensureFile(ctx, paths.ModelPath, modelURL); err != nil {
		return err
	}
	if err := ensureFile(ctx, paths.LabelsPath, labelsURL); err != nil {
		return err
	}
	return nil
}

func SummarizeImage(ctx context.Context, prompt, imagePath string) (string, error) {
	paths, err := requiredPaths()
	if err != nil {
		return "", err
	}
	if !fileExists(paths.WasmPath) || !fileExists(paths.ModelPath) || !fileExists(paths.LabelsPath) {
		return "", fmt.Errorf("vision runtime not available")
	}

	res, err := runWasm(ctx, paths, imagePath)
	if err != nil {
		return "", err
	}
	visionDebug("Inference result: primary=%s top5=%v", res.Primary, res.Top5)
	stats, _ := computeStats(imagePath)
	lines := buildSummaryLines(prompt, res, stats)
	return strings.Join(lines, "\n"), nil
}

func visionDebug(format string, args ...interface{}) {
	if providers.DebugMode {
		fmt.Printf("[vision] "+format+"\n", args...)
	}
}

type runtimePaths struct {
	BaseDir    string
	WasmPath   string
	ModelPath  string
	LabelsPath string
}

func requiredPaths() (runtimePaths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return runtimePaths{}, fmt.Errorf("failed to resolve home dir: %w", err)
	}
	base := filepath.Join(home, ".godex", "models", "vision")
	return runtimePaths{
		BaseDir:    base,
		WasmPath:   filepath.Join(base, "vision.wasm"),
		ModelPath:  filepath.Join(base, "mobilenetv2-10.onnx"),
		LabelsPath: filepath.Join(base, "labels.json"),
	}, nil
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

func runWasm(ctx context.Context, paths runtimePaths, imagePath string) (Summary, error) {
	fmt.Printf("Loading vision.wasm...\n")
	wasmBytes, err := os.ReadFile(paths.WasmPath)
	if err != nil {
		return Summary{}, err
	}
	fmt.Printf("Initializing Wasm runtime...\n")

	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)
	_, err = wasi_snapshot_preview1.Instantiate(ctx, r)
	if err != nil {
		return Summary{}, err
	}

	imageDir := filepath.Dir(imagePath)
	imageName := filepath.Base(imagePath)
	fsConfig := wazero.NewFSConfig()
	fsConfig = fsConfig.WithDirMount(imageDir, "/image")
	fsConfig = fsConfig.WithDirMount(paths.BaseDir, "/model")

	args := []string{
		"vision",
		"/image/" + imageName,
		"/model/" + filepath.Base(paths.ModelPath),
		"/model/" + filepath.Base(paths.LabelsPath),
	}

	fmt.Printf("Running model inference on %s...\n", imageName)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	modConfig := wazero.NewModuleConfig().
		WithStdout(&stdout).
		WithStderr(&stderr).
		WithArgs(args...).
		WithFSConfig(fsConfig)

	_, err = r.InstantiateWithConfig(ctx, wasmBytes, modConfig)
	if err != nil {
		return Summary{}, fmt.Errorf("vision wasm failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	out := strings.TrimSpace(stdout.String())
	if out == "" {
		return Summary{}, fmt.Errorf("vision wasm returned empty output: %s", strings.TrimSpace(stderr.String()))
	}
	var parsed wasmResult
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return Summary{}, fmt.Errorf("vision wasm invalid JSON: %w (out=%s)", err, out)
	}

	fmt.Printf("Inference complete.\n")
	return Summary{Primary: parsed.Label, Top5: parsed.Top5}, nil
}

func computeStats(path string) (ImageStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return ImageStats{}, err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return ImageStats{}, err
	}
	b := img.Bounds()
	w := b.Dx()
	h := b.Dy()
	aspect := 0.0
	if h > 0 {
		aspect = float64(w) / float64(h)
	}
	brightness := sampleBrightness(img)
	return ImageStats{Width: w, Height: h, Aspect: aspect, Brightness: brightness}, nil
}

func sampleBrightness(img image.Image) float64 {
	b := img.Bounds()
	w := b.Dx()
	h := b.Dy()
	if w == 0 || h == 0 {
		return 0
	}
	stepX := int(math.Max(1, float64(w)/64))
	stepY := int(math.Max(1, float64(h)/64))

	var total float64
	var count float64
	for y := b.Min.Y; y < b.Max.Y; y += stepY {
		for x := b.Min.X; x < b.Max.X; x += stepX {
			r, g, b, _ := img.At(x, y).RGBA()
			lum := 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)
			lum = lum / 65535.0
			total += lum
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return total / count
}

func buildSummaryLines(prompt string, summary Summary, stats ImageStats) []string {
	lines := make([]string, 0, 6)
	lines = append(lines, "Local image analysis:")
	if summary.Primary != "" {
		lines = append(lines, fmt.Sprintf("Primary label: %s", summary.Primary))
	}
	if len(summary.Top5) > 0 {
		parts := make([]string, 0, len(summary.Top5))
		for _, item := range summary.Top5 {
			parts = append(parts, fmt.Sprintf("%s (%.2f)", item.Label, item.Score))
		}
		lines = append(lines, "Top-5 labels: "+strings.Join(parts, ", "))
	}
	if stats.Width > 0 && stats.Height > 0 {
		lines = append(lines, fmt.Sprintf("Image stats: %dx%d (aspect %.2f), brightness %.2f", stats.Width, stats.Height, stats.Aspect, stats.Brightness))
	}
	if summary.Primary != "" {
		lines = append(lines, fmt.Sprintf("Summary: likely contains %s", summary.Primary))
	}
	if strings.TrimSpace(prompt) != "" {
		lines = append(lines, fmt.Sprintf("Relevance to question: %s", strings.TrimSpace(prompt)))
	} else {
		lines = append(lines, "Relevance to question: (no prompt provided)")
	}
	for len(lines) < 5 {
		lines = append(lines, "Additional context: (unavailable)")
	}
	return lines
}
