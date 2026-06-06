package wizard

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/cheikh2shift/godex/internal/config"
	"github.com/cheikh2shift/godex/internal/providers"
	"github.com/cheikh2shift/godex/modelquery"
)

func RunWizard(destination string) error {
	reader := bufio.NewReader(os.Stdin)

	// Try to load existing config for defaults
	var existingCfg *config.Config
	var existingProvider *config.Provider
	if cfg, err := config.Load(destination); err == nil {
		existingCfg = cfg
		if len(cfg.Providers) > 0 {
			existingProvider = &cfg.Providers[0]
		}
	}

	fmt.Println("*** Provider configuration wizard ***")
	fmt.Println("This will create or overwrite:", destination)
	appendProvider := false
	setAsDefault := false
	if existingCfg != nil && len(existingCfg.Providers) > 0 {
		fmt.Println("Loaded defaults from existing config")
		appendProvider = promptYesNo(reader, "Append to existing providers? (y/N)", false)
		if appendProvider {
			setAsDefault = promptYesNo(reader, "Set as default provider? (y/N)", false)
		}
	}

	provider := config.Provider{}

	// Helper to get existing value or default
	getDefault := func(field string, defaultVal string) string {
		if existingProvider == nil {
			return defaultVal
		}
		if existingProvider.Type != "" && provider.Type != "" &&
			!strings.EqualFold(existingProvider.Type, provider.Type) {
			switch field {
			case "model", "description", "endpoint", "api_key_env":
				return defaultVal
			}
		}
		switch field {
		case "name":
			return existingProvider.Name
		case "type":
			return existingProvider.Type
		case "model":
			return existingProvider.Model
		case "description":
			return existingProvider.Description
		case "endpoint":
			return existingProvider.Endpoint
		case "api_key_env":
			return defaultVal
		case "temperature":
			if existingProvider.Temperature != nil {
				return fmt.Sprintf("%v", *existingProvider.Temperature)
			}
			return defaultVal
		case "max_tool_rounds":
			if existingProvider.MaxToolRounds != nil {
				return fmt.Sprintf("%v", *existingProvider.MaxToolRounds)
			}
			return defaultVal
		case "tool_timeout":
			if existingProvider.ToolTimeout != nil {
				return fmt.Sprintf("%v", *existingProvider.ToolTimeout)
			}
			return defaultVal
		}
		return defaultVal
	}

	providerName, err := prompt(reader, "Provider name", getDefault("name", "my-local-identifier"))
	if err != nil || providerName == "" {
		return nil
	}
	provider.Name = providerName
	providerType, err := selectPrompt("Provider type", []selectOption{
		{label: "ollama", desc: "LLM via Ollama"},
		{label: "llama", desc: "LLM via llama.cpp (local, no server needed)"},
		{label: "gemini", desc: "Google Gemini API"},
		{label: "openrouter", desc: "OpenRouter (OpenAI, Anthropic, Meta models)"},
	}, getDefault("type", "ollama"), true)
	if err != nil || providerType == "" {
		fmt.Println("\nSetup cancelled.")
		return nil
	}
	provider.Type = providerType

	if provider.Params == nil {
		provider.Params = map[string]string{}
	}

	if provider.Type == "openrouter" {
		authMethod, _ := selectPrompt("OpenRouter authentication", []selectOption{
			{label: "oauth", desc: "Login via browser (OAuth PKCE) - opens browser"},
			{label: "manual", desc: "Enter API key manually"},
			{label: "env", desc: "Use OPENROUTER_API_KEY from environment"},
			{label: "skip", desc: "Skip for now (use OPENROUTER_API_KEY at runtime)"},
		}, "oauth")
		if authMethod == "oauth" {
			apiKey, err := doOpenRouterOAuth()
			if err != nil {
				fmt.Printf("OAuth failed: %v. Falling back to manual entry.\n", err)
				provider.APIKey, _ = prompt(reader, "OpenRouter API key", "")
			} else {
				provider.APIKey = apiKey
				fmt.Println("Successfully authenticated with OpenRouter!")
			}
		} else if authMethod == "manual" {
			provider.APIKey, _ = prompt(reader, "OpenRouter API key", "")
		} else if authMethod == "env" {
			provider.APIKeyEnv = "OPENROUTER_API_KEY"
		}
		provider.Endpoint, _ = prompt(reader, "OpenRouter base URL", getDefault("endpoint", "https://openrouter.ai/api/v1"))

	} else if provider.Type == "gemini" {
		backend, _ := selectPrompt("Backend", []selectOption{
			{label: "gemini", desc: "Use Google Gemini API directly"},
			{label: "vertex", desc: "Use Google Cloud Vertex AI (requires GCP setup)"},
		}, getDefault("backend", "gemini"))
		provider.Params["backend"] = backend
		if backend == "vertex" || backend == "vertexai" {
			provider.Params["project"], _ = prompt(reader, "Vertex project", getDefault("project", ""))
			provider.Params["location"], _ = prompt(reader, "Vertex location", "us-central1")
		} else {
			provider.APIKeyEnv, _ = prompt(reader, "API key environment variable", getDefault("api_key_env", "GEMINI_API_KEY"))
		}
	} else if provider.Type == "ollama" {
		provider.Endpoint, _ = prompt(reader, "Ollama base URL", getDefault("endpoint", "http://localhost:11434"))
		provider.Params["backend"] = "ollama"
	} else if provider.Type == "llama" {
		provider.Params["backend"] = "llama"
		if err := providers.CheckOrInstallLlamaServer(reader); err != nil {
			fmt.Printf("Warning: %v\n", err)
			fmt.Println("You can still configure the provider, but it may not work until llama-server is available.")
		}
	}

	modelDefault := providers.DefaultGeminiModel
	if provider.Type == "ollama" {
		modelDefault = providers.DefaultOllamaModel
	} else if provider.Type == "llama" {
		modelDefault = "Qwen/Qwen2.5-3B-Instruct-GGUF"
	} else if provider.Type == "openrouter" {
		modelDefault = "moonshotai/kimi-k2.5"
	}

	mqProvider := modelquery.Provider{}
	switch provider.Type {
	case "ollama":
		mqProvider.Type = modelquery.ProviderOllama
	case "llama":
		mqProvider.Type = modelquery.ProviderHuggingFace
	case "gemini":
		mqProvider.Type = modelquery.ProviderGemini
	case "openrouter":
		mqProvider.Type = modelquery.ProviderOpenRouter
	}
	mqProvider.Endpoint = provider.Endpoint
	mqProvider.APIKey = provider.APIKey

	modelID, _ := ModelSelectPrompt(mqProvider, getDefault("model", modelDefault))
	provider.Model = modelID

	provider.Description, _ = prompt(reader, "Description", getDefault("description", "This model is red"))

	tempStr, _ := prompt(reader, "Temperature (0.0-1.0). The higher the value, the more random the output.", getDefault("temperature", "0.2"))
	if tempStr != "" {
		if val, err := strconv.ParseFloat(tempStr, 64); err == nil {
			provider.Temperature = &val
		}
	}

	maxRoundsStr, _ := prompt(reader, "Max tool rounds (10)", getDefault("max_tool_rounds", "10"))
	if maxRoundsStr != "" {
		if val, err := strconv.Atoi(maxRoundsStr); err == nil {
			provider.MaxToolRounds = &val
		}
	}

	toolTimeoutStr, _ := prompt(reader, "Tool timeout in seconds (180)", getDefault("tool_timeout", "180"))
	if toolTimeoutStr != "" {
		if val, err := strconv.Atoi(toolTimeoutStr); err == nil {
			provider.ToolTimeout = &val
		}
	}

	// MCP servers
	fmt.Println()
	selectedMCP := multiSelectPrompt("Select MCP servers", []selectOption{
		{label: "filesystem", desc: "Read, write, and manage files"},
		{label: "bash", desc: "Run shell commands"},
		{label: "webscraper", desc: "Fetch and scrape web pages"},
	})
	if len(selectedMCP) > 0 {
		var mcpServers []config.MCPServer
		for _, name := range selectedMCP {
			mcpServers = append(mcpServers, config.MCPServer{Name: name})
		}
		provider.MCPServers = mcpServers
	}

	var cfg *config.Config
	if appendProvider && existingCfg != nil {
		existingCfg.Providers = append(existingCfg.Providers, provider)
		if existingCfg.DefaultProvider == "" || setAsDefault {
			existingCfg.DefaultProvider = provider.Name
		}
		cfg = existingCfg
	} else {
		cfg = &config.Config{
			Providers:       []config.Provider{provider},
			DefaultProvider: provider.Name,
		}
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return fmt.Errorf("unable to create config directory: %w", err)
	}

	if err := config.Save(destination, cfg); err != nil {
		return fmt.Errorf("unable to save config: %w", err)
	}

	fmt.Println()
	if appendProvider {
		fmt.Println("Updated providers file at", destination)
	} else {
		fmt.Println("Created providers file at", destination)
	}
	fmt.Println("You can add more providers by editing the YAML and adding entries to the providers list.")
	fmt.Println("Launch with this provider:")
	fmt.Printf("  godex --provider %s\n", provider.Name)
	fmt.Println("Launch the CLI without --wizard to start the agent.")

	return nil
}

func prompt(reader *bufio.Reader, question, def string) (string, error) {
	for attempts := 0; attempts < 3; attempts++ {
		if def != "" {
			fmt.Printf("%s [%s]: ", question, def)
		} else {
			fmt.Printf("%s: ", question)
		}
		input, _ := reader.ReadString('\n')
		if len(input) > 0 && input[0] == 27 {
			fmt.Println("\nSetup cancelled.")
			return "", nil
		}
		input = strings.TrimSpace(input)
		if input == "" {
			return def, nil
		}
		if looksLikePromptEcho(input) {
			fmt.Println("Input looked like a prompt echo. Please enter a value.")
			continue
		}
		return input, nil
	}
	return def, nil
}

func promptYesNo(reader *bufio.Reader, question string, def bool) bool {
	defLabel := "y/N"
	if def {
		defLabel = "Y/n"
	}
	for attempts := 0; attempts < 3; attempts++ {
		fmt.Printf("%s [%s]: ", question, defLabel)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(strings.ToLower(input))
		if input == "" {
			return def
		}
		if looksLikePromptEcho(input) {
			fmt.Println("Input looked like a prompt echo. Please answer y/n.")
			continue
		}
		return input == "y" || input == "yes"
	}
	return def
}

func looksLikePromptEcho(input string) bool {
	lower := strings.ToLower(input)
	if strings.Contains(lower, "providers.yaml") {
		return true
	}
	promptMarkers := []string{
		"append to existing providers",
		"set as default provider",
		"provider name",
		"provider type",
		"model",
		"description",
		"base url",
		"api key environment variable",
		"temperature",
		"max tool rounds",
		"tool timeout",
		"vertex project",
		"vertex location",
		"mcp servers",
	}
	for _, marker := range promptMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return strings.Contains(input, "]:")
}

type selectOption struct {
	label string
	desc  string
}

type selectModel struct {
	result      string
	options     []selectOption
	cursor      int
	done        bool
	allowCancel bool
}

func newSelectModel(options []selectOption) selectModel {
	return selectModel{
		options:     options,
		cursor:      0,
		done:        false,
		result:      "",
		allowCancel: true,
	}
}

func (m selectModel) Init() tea.Cmd {
	return nil
}

func (m selectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyUp:
			if m.cursor > 0 {
				m.cursor--
			}
		case tea.KeyDown:
			if m.cursor < len(m.options)-1 {
				m.cursor++
			}
		case tea.KeyEnter:
			m.result = m.options[m.cursor].label
			m.done = true
			return m, tea.Quit
		case tea.KeyCtrlC, tea.KeyEsc:
			if m.allowCancel {
				m.done = true
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m selectModel) View() string {
	var b strings.Builder
	b.WriteString("\n")
	for i, opt := range m.options {
		if i == m.cursor {
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Bold(true).Render("  > " + opt.label))
		} else {
			b.WriteString("    " + opt.label)
		}
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("    "+opt.desc) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("  ↑↓ select  ↵ confirm  esc cancel"))
	return b.String()
}

func selectPrompt(title string, options []selectOption, defaultVal string, allowCancel ...bool) (string, error) {
	m := newSelectModel(options)
	if len(allowCancel) > 0 && !allowCancel[0] {
		m.allowCancel = false
	}
	for i, opt := range options {
		if opt.label == defaultVal {
			m.cursor = i
			break
		}
	}
	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return defaultVal, err
	}
	result := finalModel.(selectModel)
	if result.result == "" {
		return defaultVal, nil
	}
	return result.result, nil
}

type multiSelectModel struct {
	selected    map[int]bool
	options     []selectOption
	cursor      int
	done        bool
	allowCancel bool
}

func newMultiSelectModel(options []selectOption) multiSelectModel {
	return multiSelectModel{
		options:     options,
		selected:    make(map[int]bool),
		cursor:      0,
		done:        false,
		allowCancel: true,
	}
}

func (m multiSelectModel) Init() tea.Cmd {
	return nil
}

func (m multiSelectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyUp:
			if m.cursor > 0 {
				m.cursor--
			}
		case tea.KeyDown:
			if m.cursor < len(m.options)-1 {
				m.cursor++
			}
		case tea.KeySpace:
			m.selected[m.cursor] = !m.selected[m.cursor]
		case tea.KeyEnter:
			m.done = true
			return m, tea.Quit
		case tea.KeyCtrlC, tea.KeyEsc:
			if m.allowCancel {
				m.selected = make(map[int]bool)
				m.done = true
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m multiSelectModel) View() string {
	var b strings.Builder
	b.WriteString("\n")
	for i, opt := range m.options {
		prefix := "[ ]"
		style := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
		if m.selected[i] {
			prefix = "[x]"
			style = lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Bold(true)
		}
		if i == m.cursor {
			b.WriteString(style.Render("  > " + prefix + " " + opt.label))
		} else {
			b.WriteString(style.Render("    " + prefix + " " + opt.label))
		}
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("      "+opt.desc) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("  ↑↓ navigate  ␣ toggle  ↵ confirm  esc cancel"))
	return b.String()
}

func multiSelectPrompt(title string, options []selectOption) []string {
	m := newMultiSelectModel(options)
	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return nil
	}
	result := finalModel.(multiSelectModel)
	if len(result.selected) == 0 {
		return nil
	}
	var selected []string
	for i := range result.selected {
		selected = append(selected, result.options[i].label)
	}
	return selected
}

func doOpenRouterOAuth() (string, error) {
	codeVerifier := generateCodeVerifier(64)
	codeChallenge := generateCodeChallenge(codeVerifier)

	port := 3000
	callbackURL := fmt.Sprintf("http://localhost:%d/callback", port)

	authURL := fmt.Sprintf("https://openrouter.ai/auth?callback_url=%s&code_challenge=%s&code_challenge_method=S256",
		url.QueryEscape(callbackURL), url.QueryEscape(codeChallenge))

	fmt.Printf("\n  Opening browser for OAuth login...\n")
	fmt.Printf("  If browser doesn't open, visit:\n  %s\n\n", authURL)

	if err := openBrowser(authURL); err != nil {
		return "", fmt.Errorf("failed to open browser: %w", err)
	}

	code := waitForCallback(port, 5*time.Minute)
	if code == "" {
		return "", fmt.Errorf("OAuth timeout - no callback received")
	}

	apiKey, err := exchangeCodeForKey(code, codeVerifier)
	if err != nil {
		return "", fmt.Errorf("failed to exchange code: %w", err)
	}

	return apiKey, nil
}

func generateCodeVerifier(length int) string {
	bytes := make([]byte, length)
	rand.Read(bytes)
	return base64.RawURLEncoding.EncodeToString(bytes)
}

func generateCodeChallenge(verifier string) string {
	hash := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

func openBrowser(urlStr string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", urlStr).Run()
	case "linux":
		if _, err := os.Stat("/usr/bin/xdg-open"); err == nil {
			return exec.Command("xdg-open", urlStr).Run()
		}
		return exec.Command("gio", "open", urlStr).Run()
	case "windows":
		return exec.Command("cmd", "/c", "start", urlStr).Run()
	}
	return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
}

type oauthCallback struct {
	err      error
	done     chan struct{}
	code     string
	received bool
}

var callbackResult *oauthCallback

func waitForCallback(port int, timeout time.Duration) string {
	callbackResult = &oauthCallback{done: make(chan struct{})}

	server := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: http.HandlerFunc(handleOAuthCallback)}

	go func() {
		server.ListenAndServe()
	}()

	defer server.Close()

	select {
	case <-callbackResult.done:
		return callbackResult.code
	case <-time.After(timeout):
		return ""
	}
}

func handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if callbackResult == nil || callbackResult.received {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<html><body><p>Already processed.</p></body></html>")
		return
	}

	if err := r.ParseForm(); err != nil {
		callbackResult.err = err
		http.Error(w, "Parse error", 400)
		return
	}

	if code := r.Form.Get("code"); code != "" {
		callbackResult.code = code
		callbackResult.received = true
		callbackResult.done <- struct{}{}
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<html><body><h1>Authentication successful!</h1><p>You can close this window and return to the terminal.</p><script>window.close()</script></body></html>")
		return
	}

	http.Error(w, "No code received", 400)
}

func exchangeCodeForKey(code, codeVerifier string) (string, error) {
	reqBody := map[string]string{
		"code":                  code,
		"code_verifier":         codeVerifier,
		"code_challenge_method": "S256",
	}

	jsonBody, _ := json.Marshal(reqBody)
	req, err := http.NewRequest("POST", "https://openrouter.ai/api/v1/auth/keys", strings.NewReader(string(jsonBody)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("auth failed: HTTP %d - %s", resp.StatusCode, string(body))
	}

	var result struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w - body: %s", err, string(body))
	}

	return result.Key, nil
}
