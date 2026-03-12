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
	var existingProvider *config.Provider
	if existingCfg, err := config.Load(destination); err == nil && len(existingCfg.Providers) > 0 {
		existingProvider = &existingCfg.Providers[0]
	}

	fmt.Println("*** Provider configuration wizard ***")
	fmt.Println("This will create or overwrite:", destination)
	if existingProvider != nil {
		fmt.Println("Loaded defaults from existing config")
	}

	provider := config.Provider{}

	// Helper to get existing value or default
	getDefault := func(field string, defaultVal string) string {
		if existingProvider == nil {
			return defaultVal
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
			return existingProvider.APIKeyEnv
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

	provider.Name = prompt(reader, "Provider name", getDefault("name", "ollama"))
	provider.Type = prompt(reader, "Provider type (gemini or ollama)", getDefault("type", "ollama"))

	modelDefault := providers.DefaultGeminiModel
	if provider.Type == "ollama" {
		modelDefault = providers.DefaultOllamaModel
	}
	provider.Model = prompt(reader, "Model", getDefault("model", modelDefault))
	provider.Description = prompt(reader, "Description", getDefault("description", "Ollama model"))
	backend := prompt(reader, "Backend (gemini, vertex, or ollama)", getDefault("backend", "ollama"))

	if provider.Params == nil {
		provider.Params = map[string]string{}
	}
	provider.Params["backend"] = backend

	if backend == "ollama" || provider.Type == "ollama" {
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

	tempStr := prompt(reader, "Temperature (0.0-1.0)", getDefault("temperature", "0.2"))
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

	cfg := &config.Config{
		Providers:       []config.Provider{provider},
		DefaultProvider: provider.Name,
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return fmt.Errorf("unable to create config directory: %w", err)
	}

	if err := config.Save(destination, cfg); err != nil {
		return fmt.Errorf("unable to save config: %w", err)
	}

	fmt.Println()
	fmt.Println("Created providers file at", destination)
	fmt.Println("You can add more providers by editing the YAML and adding entries to the providers list.")
	fmt.Println("Launch the CLI without --wizard to start the agent.")

	return nil
}

func prompt(reader *bufio.Reader, question, def string) string {
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
	return input
}
