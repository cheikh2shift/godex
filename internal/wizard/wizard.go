package wizard

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cheikh-seck/godex/internal/config"
	"github.com/cheikh-seck/godex/internal/providers"
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
			strings.ToLower(existingProvider.Type) != strings.ToLower(provider.Type) {
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

	provider.Name = prompt(reader, "Provider name", getDefault("name", "my-local-identifier"))
	provider.Type = prompt(reader, "Provider type (gemini, ollama, huggingface, or openrouter)", getDefault("type", "ollama"))

	modelDefault := providers.DefaultGeminiModel
	if provider.Type == "ollama" {
		modelDefault = providers.DefaultOllamaModel
	} else if provider.Type == "huggingface" {
		modelDefault = "deepseek-ai/DeepSeek-R1:fastest"
	} else if provider.Type == "openrouter" {
		modelDefault = "nvidia/nemotron-3-nano-30b-a3b:free"
	}
	modelQuestion := "Model"
	if provider.Type == "huggingface" {
		modelQuestion = "Model (supports :fastest or :provider)"
	}
	provider.Model = prompt(reader, modelQuestion, getDefault("model", modelDefault))
	provider.Description = prompt(reader, "Description", getDefault("description", "This model is red"))
	backend := ""
	if provider.Type == "gemini" {
		backend = prompt(reader, "Backend (gemini or vertex)", getDefault("backend", "gemini"))
	} else if provider.Type == "huggingface" {
		backend = "huggingface"
	} else if provider.Type == "openrouter" {
		backend = "openrouter"
	} else {
		backend = "ollama"
	}

	if provider.Params == nil {
		provider.Params = map[string]string{}
	}
	provider.Params["backend"] = backend

	if provider.Type == "huggingface" {
		provider.Endpoint = prompt(reader, "Hugging Face base URL", getDefault("endpoint", "https://router.huggingface.co/v1"))
		provider.APIKeyEnv = prompt(reader, "API key environment variable", getDefault("api_key_env", "HF_TOKEN"))
	} else if provider.Type == "openrouter" {
		provider.Endpoint = prompt(reader, "OpenRouter base URL", getDefault("endpoint", "https://openrouter.ai/api/v1"))
		provider.APIKeyEnv = prompt(reader, "API key environment variable", getDefault("api_key_env", "OPENROUTER_API_KEY"))
	} else if backend == "ollama" || provider.Type == "ollama" {
		provider.Endpoint = prompt(reader, "Ollama base URL", getDefault("endpoint", "http://localhost:11434"))
	} else if backend == "vertex" || backend == "vertexai" {
		provider.Params["project"] = prompt(reader, "Vertex project", getDefault("project", ""))
		provider.Params["location"] = prompt(reader, "Vertex location", "us-central1")
	} else {
		provider.APIKeyEnv = prompt(reader, "API key environment variable", getDefault("api_key_env", "GEMINI_API_KEY"))
	}

	if provider.APIKeyEnv == "" {
		provider.APIKey = prompt(reader, "API key (kept in YAML, not recommended)", "")
	}

	tempStr := prompt(reader, "Temperature (0.0-1.0). The higher the value, the more random the output.", getDefault("temperature", "0.5"))
	if tempStr != "" {
		if val, err := strconv.ParseFloat(tempStr, 64); err == nil {
			provider.Temperature = &val
		}
	}

	maxRoundsStr := prompt(reader, "Max tool rounds (10)", getDefault("max_tool_rounds", "10"))
	if maxRoundsStr != "" {
		if val, err := strconv.Atoi(maxRoundsStr); err == nil {
			provider.MaxToolRounds = &val
		}
	}

	toolTimeoutStr := prompt(reader, "Tool timeout in seconds (180)", getDefault("tool_timeout", "180"))
	if toolTimeoutStr != "" {
		if val, err := strconv.Atoi(toolTimeoutStr); err == nil {
			provider.ToolTimeout = &val
		}
	}

	// MCP servers
	fmt.Println("\nMCP Servers (comma separated, or Enter for none):")
	fmt.Println("Available: filesystem, bash, webscraper")
	mcpInput := prompt(reader, "MCP servers", "")
	if mcpInput != "" {
		var mcpServers []config.MCPServer
		for _, name := range strings.Split(mcpInput, ",") {
			name = strings.TrimSpace(name)
			if name != "" {
				mcpServers = append(mcpServers, config.MCPServer{Name: name})
			}
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

func prompt(reader *bufio.Reader, question, def string) string {
	for attempts := 0; attempts < 3; attempts++ {
		if def != "" {
			fmt.Printf("%s [%s]: ", question, def)
		} else {
			fmt.Printf("%s: ", question)
		}
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)
		if input == "" {
			return def
		}
		if looksLikePromptEcho(input) {
			fmt.Println("Input looked like a prompt echo. Please enter a value.")
			continue
		}
		return input
	}
	return def
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
	if strings.Contains(input, "]:") {
		return true
	}
	return false
}
