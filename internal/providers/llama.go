package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cheikh2shift/godex/internal/config"
)

const (
	defaultLlamaServerTimeout = 10 * time.Minute
	llamaMaxRetries           = 3
	llamaRetryDelay           = 5 * time.Second
)

func llamaDebug(format string, args ...interface{}) {
	if DebugMode {
		log.Printf("[llama.cpp] "+format, args...)
	}
}

type llamaProvider struct {
	baseURL          string
	model            string
	modelIsHF        bool
	cfg              *config.Provider
	temperature      *float64
	client           *http.Client
	messages         []map[string]interface{}
	mu               sync.Mutex
	sendMu           sync.Mutex
	OnThink          func(string)
	cancelFunc       context.CancelFunc
	cancelGen        uint64
	contextLimit     int
	promptTokens     int
	completionTokens int
	statusCh         chan<- string
	tools            []map[string]interface{}
	pendingToolCalls map[string]string

	serverCmd      *exec.Cmd
	serverProcess  *os.Process
	serverPort     int
	serverPath     string
	tokenizePath   string
	externalServer bool
	serverMu       sync.Mutex
	serverReady    bool
	serverStarted  bool
	startMu        sync.Mutex

	OnDownloadProgress func(DownloadProgress)
}

var (
	quantizationCacheMu sync.Mutex
	quantizationCache   = map[string]string{}
)

func init() {
	Register("llama", newLlamaProvider)
	Register("llamacpp", newLlamaProvider)
}

func findFreePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		return 0, err
	}

	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer l.Close()

	return l.Addr().(*net.TCPAddr).Port, nil
}

func detectLlamaServer() (string, error) {
	paths := []string{}

	godexDir := os.Getenv("GODEX_DIR")
	if godexDir != "" {
		paths = append(paths, filepath.Join(godexDir, "llama-server"))
	}

	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		paths = append(paths,
			filepath.Join(homeDir, ".godex", "llama-server"),
			filepath.Join(homeDir, ".godex", "bin", "llama-server"),
		)
	}

	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			if err := os.Chmod(p, 0755); err == nil {
				if _, err := exec.LookPath(p); err == nil || fileExists(p) {
					return p, nil
				}
			}
		}
	}

	if path, err := exec.LookPath("llama-server"); err == nil {
		return path, nil
	}

	for _, p := range paths {
		if fileExists(p) {
			return p, nil
		}
	}

	if homeDir != "" {
		binDir := filepath.Join(homeDir, ".godex", "bin")
		if entries, err := os.ReadDir(binDir); err == nil {
			for _, entry := range entries {
				if !entry.IsDir() && strings.Contains(strings.ToLower(entry.Name()), "llama-server") {
					llamaPath := filepath.Join(binDir, entry.Name())
					os.Chmod(llamaPath, 0755)
					return llamaPath, nil
				}
			}
			for _, entry := range entries {
				if entry.IsDir() {
					subDir := filepath.Join(binDir, entry.Name())
					if subEntries, err := os.ReadDir(subDir); err == nil {
						for _, subEntry := range subEntries {
							if !subEntry.IsDir() && strings.Contains(strings.ToLower(subEntry.Name()), "llama-server") {
								llamaPath := filepath.Join(subDir, subEntry.Name())
								os.Chmod(llamaPath, 0755)
								return llamaPath, nil
							}
						}
					}
				}
			}
		}
	}

	return "", fmt.Errorf("llama-server not found")
}

func detectLlamaTokenize() (string, error) {
	paths := []string{}

	godexDir := os.Getenv("GODEX_DIR")
	if godexDir != "" {
		paths = append(paths, filepath.Join(godexDir, "llama-tokenize"))
	}

	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		paths = append(paths,
			filepath.Join(homeDir, ".godex", "llama-tokenize"),
			filepath.Join(homeDir, ".godex", "bin", "llama-tokenize"),
		)
	}

	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			if err := os.Chmod(p, 0755); err == nil {
				if _, err := exec.LookPath(p); err == nil || fileExists(p) {
					return p, nil
				}
			}
		}
	}

	if path, err := exec.LookPath("llama-tokenize"); err == nil {
		return path, nil
	}

	for _, p := range paths {
		if fileExists(p) {
			return p, nil
		}
	}

	if homeDir != "" {
		binDir := filepath.Join(homeDir, ".godex", "bin")
		if entries, err := os.ReadDir(binDir); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					subDir := filepath.Join(binDir, entry.Name())
					if subEntries, err := os.ReadDir(subDir); err == nil {
						for _, subEntry := range subEntries {
							if !subEntry.IsDir() && subEntry.Name() == "llama-tokenize" {
								tokenizePath := filepath.Join(subDir, subEntry.Name())
								os.Chmod(tokenizePath, 0755)
								llamaDebug("Found llama-tokenize at: %s", tokenizePath)
								return tokenizePath, nil
							}
						}
					}
				}
			}
		}
	}

	return "", fmt.Errorf("llama-tokenize not found")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func resolveModelPath(model string) (string, bool, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", false, fmt.Errorf("empty model name")
	}

	if fileExists(model) {
		return model, true, nil
	}

	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		godexModelPath := filepath.Join(homeDir, ".godex", "models", model)
		if fileExists(godexModelPath) {
			return godexModelPath, true, nil
		}

		entries, err := os.ReadDir(filepath.Join(homeDir, ".godex", "models"))
		if err == nil {
			for _, entry := range entries {
				if !entry.IsDir() && strings.Contains(strings.ToLower(entry.Name()), strings.ToLower(model)) {
					return filepath.Join(homeDir, ".godex", "models", entry.Name()), true, nil
				}
			}
		}
	}

	return model, false, nil
}

func resolveModelPathWithDownload(ctx context.Context, model string, downloadProgress chan<- DownloadProgress) (string, string, bool, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", "", false, fmt.Errorf("empty model name")
	}

	localPath, isLocal, err := resolveModelPath(model)
	if err != nil {
		return "", "", false, err
	}

	if isLocal {
		return localPath, model, true, nil
	}

	if !strings.Contains(model, "/") {
		return model, model, false, nil
	}

	modelID := model
	selectedQuant := ""

	if strings.Contains(model, ":") {
		parts := strings.SplitN(model, ":", 2)
		modelID = parts[0]
		selectedQuant = strings.ToUpper(parts[1])
	}

	ggufFiles, err := getGGUFFiles(ctx, modelID)
	if err != nil {
		return "", "", false, fmt.Errorf("failed to list GGUF files: %w", err)
	}

	if len(ggufFiles) == 0 {
		return model, model, false, nil
	}

	quantToFile := make(map[string]string)
	quantRe := regexp.MustCompile(`-([A-Za-z0-9_]+)\.gguf$`)
	for _, f := range ggufFiles {
		matches := quantRe.FindStringSubmatch(f)
		if len(matches) >= 2 {
			quantToFile[strings.ToUpper(matches[1])] = f
		}
	}

	if selectedQuant == "" {
		if cached, ok := getCachedQuantization(modelID); ok {
			if _, exists := quantToFile[cached]; exists {
				selectedQuant = cached
			}
		}
	}

	if selectedQuant == "" {
		fmt.Printf("\nAvailable quantizations for %s:\n", modelID)
		quants := SortQuantizationsKeys(quantToFile)
		for i, q := range quants {
			desc := GetQuantizationDescription(q)
			fmt.Printf("  %d. %s - %s\n", i+1, q, desc)
		}

		idx := slices.Index(quants, "Q4_K_M")
		defaultIdx := idx
		if defaultIdx < 0 {
			defaultIdx = 0
		}
		selectedIdx, err := promptQuantizationSelection(len(quants), defaultIdx)
		if err != nil {
			return "", "", false, err
		}
		selectedQuant = quants[selectedIdx]
	}

	selectedFile, ok := quantToFile[selectedQuant]
	if !ok {
		selectedFile = selectBestGGUF(ggufFiles)
		if selectedFile == "" {
			return "", "", false, fmt.Errorf("no suitable GGUF file found for %s", modelID)
		}
		fmt.Printf("Selected quantization %s not found, using %s\n", selectedQuant, selectedFile)
	}

	if selectedQuant != "" {
		setCachedQuantization(modelID, selectedQuant)
	}

	homeDir, _ := os.UserHomeDir()
	modelsDir := filepath.Join(homeDir, ".godex", "models")
	if err := os.MkdirAll(modelsDir, 0755); err != nil {
		return "", "", false, fmt.Errorf("failed to create models directory: %w", err)
	}

	filename := filepath.Base(selectedFile)
	destPath := filepath.Join(modelsDir, filename)

	modelWithQuant := modelID + ":" + selectedQuant

	if fileExists(destPath) {
		fmt.Printf("[llama.cpp] Using cached model: %s\n", filename)
		return destPath, modelWithQuant, true, nil
	}

	llamaDebug("Downloading model %s to %s", modelID, destPath)
	fmt.Printf("[llama.cpp] Downloading %s...\n", filename)

	downloadURL := getDownloadURL(modelID, selectedFile)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", "", false, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", false, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", "", false, fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	tmpPath := destPath + ".tmp"
	tmpFile, err := os.Create(tmpPath)
	if err != nil {
		return "", "", false, fmt.Errorf("failed to create temp file: %w", err)
	}

	var totalSize int64
	if resp.ContentLength > 0 {
		totalSize = resp.ContentLength
	}

	progressWriter := &progressWriter{
		w:          tmpFile,
		total:      totalSize,
		filename:   filename,
		progressCh: downloadProgress,
	}

	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := progressWriter.Write(buf[:n]); werr != nil {
				tmpFile.Close()
				os.Remove(tmpPath)
				return "", "", false, fmt.Errorf("failed to write: %w", werr)
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			tmpFile.Close()
			os.Remove(tmpPath)
			return "", "", false, fmt.Errorf("failed to read: %w", err)
		}
	}

	tmpFile.Close()
	if err := os.Rename(tmpPath, destPath); err != nil {
		return "", "", false, fmt.Errorf("failed to save model: %w", err)
	}

	return destPath, modelWithQuant, true, nil
}

func promptQuantizationSelection(maxOptions int, defaultIdx int) (int, error) {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Printf("\nSelect quantization (default: %d for Q4_K_M): ", defaultIdx+1)
		input, err := reader.ReadString('\n')
		if err != nil {
			return 0, err
		}
		input = strings.TrimSpace(input)
		if input == "" {
			return defaultIdx, nil
		}
		idx, err := strconv.Atoi(input)
		if err != nil || idx < 1 || idx > maxOptions {
			fmt.Println("Invalid selection, using default Q4_K_M")
			return defaultIdx, nil
		}
		return idx - 1, nil
	}
}

func getCachedQuantization(modelID string) (string, bool) {
	quantizationCacheMu.Lock()
	defer quantizationCacheMu.Unlock()
	q, ok := quantizationCache[modelID]
	return q, ok
}

func setCachedQuantization(modelID, quant string) {
	quantizationCacheMu.Lock()
	defer quantizationCacheMu.Unlock()
	quantizationCache[modelID] = quant
}

type progressWriter struct {
	w          io.Writer
	total      int64
	written    int64
	filename   string
	progressCh chan<- DownloadProgress
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.w.Write(p)
	if n > 0 {
		pw.written += int64(n)
		pw.printProgress()
	}
	return n, err
}

func (pw *progressWriter) printProgress() {
	if pw.total <= 0 {
		return
	}

	percent := float64(pw.written) * 100 / float64(pw.total)
	barWidth := 40
	filled := int(percent / 100 * float64(barWidth))
	bar := strings.Repeat("=", filled) + strings.Repeat(" ", barWidth-filled)

	fmt.Printf("\r[%s] %.1f%% (%s / %s)", bar, percent,
		formatBytes(pw.written), formatBytes(pw.total))

	if pw.written >= pw.total {
		fmt.Println()
	}

	if pw.progressCh != nil {
		select {
		case pw.progressCh <- DownloadProgress{
			Downloaded: pw.written,
			Total:      pw.total,
			Filename:   pw.filename,
		}:
		default:
		}
	}
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func newLlamaProvider(cfg *config.Provider) (Provider, error) {
	return newLlamaProviderWithProgress(cfg, nil)
}

type ModelDownloadState struct {
	ModelID      string
	Quantization string
	Progress     chan DownloadProgress
	Done         chan error
	ModelPath    string
}

func newLlamaProviderWithProgress(cfg *config.Provider, downloadProgress func(DownloadProgress)) (Provider, error) {
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = "Qwen/Qwen2.5-3B-Instruct-GGUF"
	}

	modelPath, isLocal, err := resolveModelPath(model)
	if err != nil {
		return nil, err
	}

	isHFModel := !isLocal && strings.Contains(model, "/")
	externalURL := strings.TrimSpace(cfg.LlamaServerURL)

	var baseURL string
	var serverPath string
	var tokenizePath string
	var port int
	var serverReady bool

	if externalURL != "" {
		baseURL = externalURL
		serverReady = true
		llamaDebug("Using external server at %s", baseURL)
	} else {
		serverPath, err = detectLlamaServer()
		if err != nil {
			return nil, fmt.Errorf("llama.cpp provider requires llama-server: %w", err)
		}

		tokenizePath, _ = detectLlamaTokenize()
		llamaDebug("tokenizePath detected: %s", tokenizePath)
		llamaDebug("newLlamaProvider: modelPath=%s", modelPath)

		port, err = findFreePort()
		if err != nil {
			return nil, fmt.Errorf("failed to find free port: %w", err)
		}
		baseURL = fmt.Sprintf("http://localhost:%d", port)
	}

	cfgCopy := &config.Provider{
		Type:          cfg.Type,
		Name:          cfg.Name,
		Model:         cfg.Model,
		Endpoint:      cfg.Endpoint,
		APIKey:        cfg.APIKey,
		Temperature:   cfg.Temperature,
		ContextLimit:  cfg.ContextLimit,
		MaxToolRounds: cfg.MaxToolRounds,
		ToolTimeout:   cfg.ToolTimeout,
	}

	p := &llamaProvider{
		baseURL:            baseURL,
		model:              modelPath,
		modelIsHF:          false,
		cfg:                cfgCopy,
		temperature:        cfg.Temperature,
		client:             &http.Client{Timeout: defaultLlamaServerTimeout},
		messages:           []map[string]interface{}{},
		contextLimit:       cfg.ContextLimit,
		pendingToolCalls:   make(map[string]string),
		serverPort:         port,
		serverPath:         serverPath,
		tokenizePath:       tokenizePath,
		externalServer:     externalURL != "",
		serverReady:        serverReady,
		serverStarted:      false,
		OnDownloadProgress: downloadProgress,
	}

	if isHFModel {
		p.model = ""
		modelPath, modelWithQuant, _, err := resolveModelPathWithDownload(context.Background(), model, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve model: %w", err)
		}
		p.model = modelPath
		p.cfg.Model = modelWithQuant
		llamaDebug("newLlamaProvider: HF model downloaded, final model=%s", modelPath)
		if downloadProgress != nil {
			downloadProgress(DownloadProgress{
				Filename: filepath.Base(modelPath),
				Total:    -1,
			})
		}
	}

	if externalURL == "" {
		p.startServerAsync()
	}

	return p, nil
}

func (p *llamaProvider) startServerAsync() {
	p.startMu.Lock()
	if p.serverStarted {
		p.startMu.Unlock()
		return
	}
	p.serverStarted = true
	p.startMu.Unlock()

	go func() {
		llamaDebug("Starting server on port %d with model: %s (hf: %v)", p.serverPort, p.model, p.modelIsHF)
		if err := p.startServerSync(); err != nil {
			llamaDebug("Server start failed: %v", err)
			p.startMu.Lock()
			p.serverStarted = false
			p.startMu.Unlock()
			return
		}
		p.serverReady = true
		//llamaDebug("Server ready at %s\n", p.baseURL)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if p.contextLimit == 0 {
			if err := p.fetchModelInfo(ctx); err != nil {
				fmt.Printf("[llama.cpp] Warning: could not fetch model info: %v\n", err)
			}
		}
	}()
}

func (p *llamaProvider) killServer() {
	llamaDebug("killServer() called, process=%v", p.serverProcess.Pid)
	p.serverMu.Lock()
	defer p.serverMu.Unlock()

	if p.serverProcess != nil {
		llamaDebug("Sending SIGTERM to pid %d", p.serverProcess.Pid)
		p.serverProcess.Signal(syscall.SIGTERM)
		ch := make(chan struct{})
		go func() {
			p.serverProcess.Wait()
			close(ch)
		}()
		select {
		case <-ch:
			llamaDebug("Process terminated gracefully")
		case <-time.After(2 * time.Second):
			llamaDebug("Process did not terminate, sending SIGKILL")
			p.serverProcess.Kill()
		}
		p.serverProcess = nil
	}
	if p.serverCmd != nil {
		p.serverCmd.Wait()
		p.serverCmd = nil
	}
}

func (p *llamaProvider) startServerSync() error {
	p.serverMu.Lock()
	defer p.serverMu.Unlock()

	args := []string{}

	if p.modelIsHF {
		args = append(args, "-hf", p.model)
	} else {
		args = append(args, "-m", p.model)
	}

	args = append(args,
		"--port", strconv.Itoa(p.serverPort),
		"--host", "127.0.0.1",
	)
	if p.contextLimit > 0 {
		args = append(args, "-c", strconv.Itoa(p.contextLimit))
	}
	args = append(args, "--log-disable", "--jinja")

	llamaDebug("Running: %s %v", p.serverPath, args)

	cmd := exec.Command(p.serverPath, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start llama-server: %w", err)
	}

	p.serverCmd = cmd
	p.serverProcess = cmd.Process

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.killServer()
			return fmt.Errorf("timeout waiting for llama-server to start")
		case <-ticker.C:
			if p.serverProcess == nil || p.serverProcess.Pid <= 0 {
				continue
			}
			resp, err := p.client.Get(p.baseURL + "/health")
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode < 500 {
					return nil
				}
			}
		}
	}
}

func (p *llamaProvider) fetchModelInfo(ctx context.Context) error {
	llamaDebug("fetchModelInfo: baseURL=%s model=%s", p.baseURL, p.model)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1/models", nil)
	if err != nil {
		return err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int    `json:"context_length"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}

	if len(result.Data) > 0 && result.Data[0].ContextLength > 0 {
		p.contextLimit = result.Data[0].ContextLength
		p.promptTokens = p.countTokensFromMessages()
		llamaDebug("fetchModelInfo: contextLimit=%d, promptTokens=%d", p.contextLimit, p.promptTokens)
		return nil
	}

	return p.fetchContextFromProps(ctx)
}

func (p *llamaProvider) fetchContextFromProps(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/props", nil)
	if err != nil {
		return err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}

	var result struct {
		DefaultGenerationSettings struct {
			NCtx int `json:"n_ctx"`
		} `json:"default_generation_settings"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}

	if result.DefaultGenerationSettings.NCtx > 0 {
		p.contextLimit = result.DefaultGenerationSettings.NCtx
	}

	return nil
}

func (p *llamaProvider) Send(ctx context.Context, prompt string) (string, error) {
	if err := p.ensureServerReady(ctx); err != nil {
		return "", fmt.Errorf("server not ready: %w", err)
	}

	p.sendMu.Lock()
	defer p.sendMu.Unlock()

	p.mu.Lock()
	p.messages = append(p.messages, map[string]interface{}{
		"role":    "user",
		"content": prompt,
	})
	messages := make([]map[string]interface{}, len(p.messages))
	copy(messages, p.messages)

	ctx, cancel := context.WithCancel(ctx)
	p.cancelGen++
	gen := p.cancelGen
	p.cancelFunc = cancel
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		if p.cancelGen == gen {
			p.cancelFunc = nil
		}
		p.mu.Unlock()
	}()

	var response string
	var totalPromptTokens, totalCompletionTokens int
	var lastErr error

	for attempt := 0; attempt <= llamaMaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(llamaRetryDelay):
			}
		}

		result, err := p.doSend(ctx, messages)
		if err != nil {
			lastErr = err
			continue
		}

		response = result.response
		totalPromptTokens = result.totalPromptTokens
		totalCompletionTokens = result.totalCompletionTokens
		lastErr = nil
		break
	}

	if lastErr != nil {
		return "", lastErr
	}

	p.mu.Lock()
	p.messages = append(p.messages, map[string]interface{}{
		"role":    "assistant",
		"content": response,
	})
	p.promptTokens += totalPromptTokens
	p.completionTokens = totalCompletionTokens
	p.mu.Unlock()

	return response, nil
}

func (p *llamaProvider) ensureServerReady(ctx context.Context) error {
	if p.externalServer {
		if err := p.checkHealth(ctx); err != nil {
			return fmt.Errorf("llama-server health check failed: %w", err)
		}
		p.serverReady = true
		return nil
	}

	if p.serverReady {
		if err := p.checkHealth(ctx); err == nil {
			return nil
		}
		llamaDebug("Server health check failed, restarting")
	}

	if p.serverStarted {
		if err := p.waitForHealth(ctx, 30*time.Second); err == nil {
			p.serverReady = true
			return nil
		}
		llamaDebug("Server failed to become healthy, restarting")
	}

	p.startMu.Lock()
	p.serverReady = false
	p.serverStarted = true
	p.startMu.Unlock()

	p.killServer()

	if p.serverPath == "" {
		serverPath, err := detectLlamaServer()
		if err != nil {
			return fmt.Errorf("failed to detect llama-server: %w", err)
		}
		p.serverPath = serverPath
	}

	llamaDebug("Starting server on port %d with model: %s (hf: %v)", p.serverPort, p.model, p.modelIsHF)
	if err := p.startServerSync(); err != nil {
		llamaDebug("Server start failed: %v", err)
		p.startMu.Lock()
		p.serverStarted = false
		p.startMu.Unlock()
		return err
	}
	p.serverReady = true
	return nil
}

func (p *llamaProvider) checkHealth(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func (p *llamaProvider) waitForHealth(ctx context.Context, timeout time.Duration) error {
	waitCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-waitCtx.Done():
			return waitCtx.Err()
		case <-ticker.C:
			if err := p.checkHealth(waitCtx); err == nil {
				return nil
			}
		}
	}
}

type llamaSendResult struct {
	response              string
	totalPromptTokens     int
	totalCompletionTokens int
}

func (p *llamaProvider) doSend(ctx context.Context, messages []map[string]interface{}) (llamaSendResult, error) {
	reqBody := map[string]interface{}{
		"model":    p.model,
		"messages": messages,
		"stream":   true,
	}

	if p.temperature != nil {
		reqBody["temperature"] = *p.temperature
	}

	if len(p.tools) > 0 {
		reqBody["tools"] = p.tools
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return llamaSendResult{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return llamaSendResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return llamaSendResult{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return llamaSendResult{}, fmt.Errorf("llama-server responded with %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var fullResponse strings.Builder
	var hasContent bool
	var totalPromptTokens, totalCompletionTokens int
	var reasoningContent string
	var toolCalls []llamaResponseToolCall

	reader := io.Reader(resp.Body)
	lineReader := newLineReader(reader)

	for {
		line, err := lineReader.ReadLine()
		if err != nil {
			break
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					Role             string `json:"role"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
			Message struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		}

		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if len(chunk.Choices) > 0 {
			choice := chunk.Choices[0]

			if choice.Delta.Content != "" {
				hasContent = true
				fullResponse.WriteString(choice.Delta.Content)
				if p.OnThink != nil {
					p.OnThink(choice.Delta.Content)
				}
			}

			if choice.Delta.ReasoningContent != "" {
				reasoningContent += choice.Delta.ReasoningContent
			}

			if choice.FinishReason == "tool_calls" && len(chunk.Message.ToolCalls) > 0 {
				for _, tc := range chunk.Message.ToolCalls {
					toolCalls = append(toolCalls, llamaResponseToolCall{
						ID:        tc.ID,
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					})
					p.pendingToolCalls[tc.ID] = tc.Function.Name
				}
			}
		}

		if totalPromptTokens == 0 && chunk.Usage.PromptTokens > 0 {
			totalPromptTokens = chunk.Usage.PromptTokens
		}
		if totalCompletionTokens == 0 && chunk.Usage.CompletionTokens > 0 {
			totalCompletionTokens = chunk.Usage.CompletionTokens
		}
	}

	if reasoningContent != "" && p.OnThink != nil {
		p.OnThink(reasoningContent)
	}

	if len(toolCalls) > 0 {
		var b strings.Builder
		for i, tc := range toolCalls {
			call := map[string]interface{}{
				"name": tc.Name,
			}
			if strings.TrimSpace(tc.Arguments) != "" {
				var args map[string]interface{}
				if err := json.Unmarshal([]byte(tc.Arguments), &args); err == nil {
					call["arguments"] = args
				} else {
					call["arguments"] = map[string]interface{}{"_raw": tc.Arguments}
				}
			} else {
				call["arguments"] = map[string]interface{}{}
			}
			payload, err := json.Marshal(call)
			if err != nil {
				continue
			}
			if i > 0 {
				b.WriteByte('\n')
			}
			b.Write(payload)
		}
		return llamaSendResult{response: b.String(), totalPromptTokens: totalPromptTokens, totalCompletionTokens: totalCompletionTokens}, nil
	}

	response := strings.TrimSpace(fullResponse.String())
	if response == "" && !hasContent {
		return llamaSendResult{}, fmt.Errorf("llama-server returned empty response")
	}

	return llamaSendResult{response: response, totalPromptTokens: totalPromptTokens, totalCompletionTokens: totalCompletionTokens}, nil
}

type lineReader struct {
	reader io.Reader
	buf    []byte
}

func newLineReader(r io.Reader) *lineReader {
	return &lineReader{reader: r, buf: make([]byte, 0, 4096)}
}

func (lr *lineReader) ReadLine() (string, error) {
	for {
		if len(lr.buf) > 0 {
			for i, b := range lr.buf {
				if b == '\n' {
					line := string(lr.buf[:i])
					lr.buf = lr.buf[i+1:]
					if len(line) > 0 && line[len(line)-1] == '\r' {
						line = line[:len(line)-1]
					}
					return line, nil
				}
			}
		}

		tmp := make([]byte, 4096)
		n, err := lr.reader.Read(tmp)
		if err != nil && err != io.EOF {
			return "", err
		}
		if n == 0 && err == io.EOF {
			if len(lr.buf) > 0 {
				line := string(lr.buf)
				lr.buf = lr.buf[:0]
				if len(line) > 0 && line[len(line)-1] == '\r' {
					line = line[:len(line)-1]
				}
				return line, nil
			}
			return "", io.EOF
		}
		lr.buf = append(lr.buf, tmp[:n]...)
		if err == io.EOF {
			continue
		}
	}
}

type llamaResponseToolCall struct {
	ID        string
	Name      string
	Arguments string
}

func (p *llamaProvider) Close() error {
	llamaDebug("Close() called")
	p.sendMu.Lock()
	defer p.sendMu.Unlock()

	p.killServer()

	p.mu.Lock()
	defer p.mu.Unlock()
	p.messages = nil
	return nil
}

func (p *llamaProvider) Tools() []Tool {
	return nil
}

func (p *llamaProvider) SetThinkCallback(fn func(string)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.OnThink = fn
}

func (p *llamaProvider) SetDownloadProgressCallback(fn func(DownloadProgress)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.OnDownloadProgress = fn
}

func (p *llamaProvider) SetStatusChannel(ch chan<- string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.statusCh = ch
}

func (p *llamaProvider) Cancel() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancelFunc != nil {
		p.cancelFunc()
	}
}

func (p *llamaProvider) CallTool(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	return "", fmt.Errorf("llama.cpp provider does not support direct tool calls, use MCP servers")
}

func (p *llamaProvider) ContextLimit() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.contextLimit
}

func (p *llamaProvider) TokenUsage() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	llamaDebug("TokenUsage: cached promptTokens=%d, completionTokens=%d", p.promptTokens, p.completionTokens)

	if p.promptTokens > 0 || p.completionTokens > 0 {
		return p.promptTokens, p.completionTokens
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	slots, err := p.getSlotInfo(ctx)
	if err != nil {
		llamaDebug("TokenUsage: getSlotInfo error: %v", err)
		promptTokens := p.countTokensFromMessages()
		llamaDebug("TokenUsage: using countTokensFromMessages: promptTokens=%d", promptTokens)
		return promptTokens, 0
	}

	promptTokens := 0
	var completionTokens int
	for _, slot := range slots {
		isActive := slot.IsProcessing || slot.IDTask != 0
		hasTokens := false
		for _, nt := range slot.NextToken {
			if nt.NDecoded > 0 {
				completionTokens += nt.NDecoded
				hasTokens = true
			}
		}
		llamaDebug("TokenUsage: slot id=%d is_processing=%v id_task=%d next_token=%v hasTokens=%v", slot.ID, slot.IsProcessing, slot.IDTask, slot.NextToken, hasTokens)
		if isActive || hasTokens {
			if p.contextLimit == 0 && slot.NCtx > 0 {
				p.contextLimit = slot.NCtx
			}
		}
	}

	if promptTokens == 0 {
		promptTokens = p.countTokensFromMessages()
		llamaDebug("TokenUsage: using countTokensFromMessages: promptTokens=%d", promptTokens)
	}

	llamaDebug("TokenUsage: returning promptTokens=%d, completionTokens=%d", promptTokens, completionTokens)
	return promptTokens, completionTokens
}

type slotInfo struct {
	ID           int  `json:"id"`
	IsProcessing bool `json:"is_processing"`
	NCtx         int  `json:"n_ctx"`
	IDTask       int  `json:"id_task"`
	NextToken    []struct {
		NDecoded int `json:"n_decoded"`
	} `json:"next_token"`
}

func (p *llamaProvider) countTokens(text string) int {
	llamaDebug("countTokens: tokenizePath=%s model=%s", p.tokenizePath, p.model)
	if p.tokenizePath == "" || p.model == "" {
		llamaDebug("countTokens: early return, tokenizePath or model empty")
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, p.tokenizePath, "-m", p.model, "-p", text, "--log-disable")
	output, err := cmd.Output()
	if err != nil {
		llamaDebug("tokenize error: %v", err)
		return 0
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	count := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "->") {
			count++
		}
	}

	llamaDebug("countTokens: text=%q count=%d output=%s", text, count, string(output))
	return count
}

func (p *llamaProvider) countTokensFromMessages() int {
	llamaDebug("countTokensFromMessages: tokenizePath=%s model=%s messagesCount=%d", p.tokenizePath, p.model, len(p.messages))
	if p.tokenizePath == "" || p.model == "" {
		return 0
	}

	var sb strings.Builder
	for _, msg := range p.messages {
		role, _ := msg["role"].(string)
		content, _ := msg["content"].(string)
		if role != "" && content != "" {
			sb.WriteString(role)
			sb.WriteString(": ")
			sb.WriteString(content)
			sb.WriteString("\n")
		}
	}

	if sb.Len() == 0 {
		return 0
	}

	return p.countTokens(sb.String())
}

func (p *llamaProvider) getSlotInfo(ctx context.Context) ([]slotInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/slots", nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("slots endpoint returned %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	llamaDebug("/slots response: %s", string(body))

	var slots []slotInfo
	if err := json.Unmarshal(body, &slots); err != nil {
		return nil, err
	}

	return slots, nil
}

func (p *llamaProvider) Reset() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.client = &http.Client{Timeout: defaultLlamaServerTimeout}
	p.messages = []map[string]interface{}{}
	p.promptTokens = 0
	p.completionTokens = 0
	p.pendingToolCalls = make(map[string]string)
	return nil
}

func (p *llamaProvider) SetMessages(messages []Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.messages = make([]map[string]interface{}, 0, len(messages))
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		content := msg.Content
		if role == "" || content == "" {
			continue
		}
		p.messages = append(p.messages, map[string]interface{}{
			"role":    role,
			"content": content,
		})
	}
	return nil
}

func (p *llamaProvider) AppendMessages(messages []Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	seen := make(map[string]struct{}, len(p.messages))
	for _, msg := range p.messages {
		role, _ := msg["role"].(string)
		content, _ := msg["content"].(string)
		if role == "" || content == "" {
			continue
		}
		seen[role+"\x00"+content] = struct{}{}
	}

	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		content := msg.Content
		if role == "" || content == "" {
			continue
		}
		key := role + "\x00" + content
		if _, ok := seen[key]; ok {
			continue
		}
		p.messages = append(p.messages, map[string]interface{}{
			"role":    role,
			"content": content,
		})
		seen[key] = struct{}{}
	}
	return nil
}

func (p *llamaProvider) SupportsNativeToolCalls() bool {
	return true
}

func (p *llamaProvider) SetModel(model string, contextLimit int) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	modelPath, isLocal, err := resolveModelPath(model)
	if err != nil {
		return fmt.Errorf("failed to resolve model path: %w", err)
	}

	if p.model != "" && p.model == modelPath {
		return nil
	}

	isHFModel := !isLocal && strings.Contains(model, "/")

	var downloadProgressCh chan DownloadProgress
	if p.OnDownloadProgress != nil && isHFModel {
		downloadProgressCh = make(chan DownloadProgress, 10)
		go func() {
			for prog := range downloadProgressCh {
				p.OnDownloadProgress(prog)
			}
		}()
	}

	if isHFModel {
		ctx := context.Background()
		downloadedPath, modelWithQuant, _, err := resolveModelPathWithDownload(ctx, model, downloadProgressCh)
		if err != nil {
			if downloadProgressCh != nil {
				close(downloadProgressCh)
			}
			return fmt.Errorf("failed to download model: %w", err)
		}
		modelPath = downloadedPath
		model = modelWithQuant
		isLocal = true
	}

	if downloadProgressCh != nil {
		close(downloadProgressCh)
	}

	p.model = modelPath
	if isHFModel && !isLocal {
		p.modelIsHF = true
	} else {
		p.modelIsHF = false
	}
	if contextLimit > 0 {
		p.contextLimit = contextLimit
	} else {
		p.contextLimit = 0
	}
	if p.cfg != nil {
		p.cfg.Model = model
	}

	needsRestart := p.serverProcess != nil
	if needsRestart {
		p.killServer()
		p.serverReady = false
		p.startMu.Lock()
		p.serverStarted = false
		p.startMu.Unlock()
		time.Sleep(100 * time.Millisecond)
	}

	if needsRestart {
		if p.serverPath == "" {
			serverPath, err := detectLlamaServer()
			if err != nil {
				return fmt.Errorf("failed to detect llama-server: %w", err)
			}
			p.serverPath = serverPath
		}
		p.startServerAsync()
	}

	return nil
}

func GetLlamaContextLimitFromModel(model string) (int, error) {
	return 0, fmt.Errorf("context limit detection from model name not implemented for llama.cpp")
}

func parseModelContext(model string) int {
	re := regexp.MustCompile(`-(\d+)k[_-]?ctx`)
	matches := re.FindStringSubmatch(strings.ToLower(model))
	if len(matches) >= 2 {
		if val, err := strconv.Atoi(matches[1]); err == nil {
			return val * 1000
		}
	}

	knownModels := map[string]int{
		"qwen2.5-coder":  8192,
		"qwen2.5":        8192,
		"qwen2":          8192,
		"llama3":         8192,
		"llama3.1":       128 * 1000,
		"llama3.2":       128 * 1000,
		"codellama":      16384,
		"mistral":        32768,
		"mixtral":        32768,
		"phi":            4096,
		"phi3":           4096,
		"gemma":          8192,
		"gemma2":         8192,
		"stablelm":       8192,
		"deepseek-coder": 16384,
		"deepseek":       16384,
	}

	modelLower := strings.ToLower(model)
	for prefix, ctx := range knownModels {
		if strings.Contains(modelLower, prefix) {
			return ctx
		}
	}

	return 4096
}
