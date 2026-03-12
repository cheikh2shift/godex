package wizard

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/cheikh-seck/godex/internal/config"
	"github.com/cheikh-seck/godex/internal/providers"
)

// RunWizard walks the user through creating a providers configuration YAML.
func RunWizard(destination string) error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("*** Provider configuration wizard ***")
	fmt.Println("This will create or overwrite:", destination)

	provider := config.Provider{}

	provider.Name = prompt(reader, "Provider name", "gemini")
	provider.Type = prompt(reader, "Provider type (gemini or ollama)", "gemini")
	modelDefault := providers.DefaultGeminiModel
	if provider.Type == "ollama" {
		modelDefault = providers.DefaultOllamaModel
	}
	provider.Model = prompt(reader, "Model", modelDefault)
	provider.Description = prompt(reader, "Description", "Gemini text model")
	backend := prompt(reader, "Backend (gemini, vertex, or ollama)", "gemini")
	if provider.Params == nil {
		provider.Params = map[string]string{}
	}
	provider.Params["backend"] = backend
	if backend == "ollama" || provider.Type == "ollama" {
		provider.Endpoint = prompt(reader, "Ollama base URL", "http://localhost:11434")
		if provider.Model == providers.DefaultGeminiModel {
			provider.Model = prompt(reader, "Ollama model", providers.DefaultOllamaModel)
		}
	} else if backend == "vertex" || backend == "vertexai" {
		provider.Params["project"] = prompt(reader, "Vertex project", "")
		provider.Params["location"] = prompt(reader, "Vertex location", "us-central1")
	} else {
		provider.APIKeyEnv = prompt(reader, "API key environment variable (leave blank to store in config)", "GEMINI_API_KEY")
	}

	if provider.APIKeyEnv == "" {
		provider.APIKey = prompt(reader, "API key (kept in YAML, not recommended)", "")
	}

	tempStr := prompt(reader, "Temperature (0.0-1.0)", "0.2")
	if tempStr != "" {
		if val, err := strconv.ParseFloat(tempStr, 64); err == nil {
			provider.Temperature = &val
		}
	}

	cfg := &config.Config{
		Providers:       []config.Provider{provider},
		DefaultProvider: provider.Name,
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
