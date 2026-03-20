package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// MCPServer describes an MCP server configuration.
type MCPServer struct {
	Name         string   `yaml:"name"`
	Command      string   `yaml:"command"`
	Args         []string `yaml:"args,omitempty"`
	Env          []string `yaml:"env,omitempty"`
	Transport    string   `yaml:"transport,omitempty"`
	AllowedPaths []string `yaml:"allowed_paths,omitempty"`
}

// Provider describes a single LLM provider, referenced from the TUI agent.
type Provider struct {
	Name          string            `yaml:"name"`
	Type          string            `yaml:"type"`
	Endpoint      string            `yaml:"endpoint"`
	Model         string            `yaml:"model,omitempty"`
	Description   string            `yaml:"description,omitempty"`
	APIKey        string            `yaml:"api_key,omitempty"`
	APIKeyEnv     string            `yaml:"api_key_env,omitempty"`
	Temperature   *float64          `yaml:"temperature,omitempty"`
	MaxToolRounds *int              `yaml:"max_tool_rounds,omitempty"`
	ToolTimeout   *int              `yaml:"tool_timeout,omitempty"` // in seconds, default 180
	ContextLimit  int               `yaml:"context_limit,omitempty"`
	Params        map[string]string `yaml:"params,omitempty"`
	MCPServers    []MCPServer       `yaml:"mcp_servers,omitempty"`
}

// Config is a list of providers, with a default provider name for the CLI.
type Config struct {
	Providers       []Provider `yaml:"providers"`
	DefaultProvider string     `yaml:"default_provider,omitempty"`
}

// Load reads the YAML configuration from the provided path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Save persists the configuration to path, creating directories as needed.
func Save(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0o600)
}

// ProviderByName returns the provider that matches the provided name.
func (c *Config) ProviderByName(name string) *Provider {
	for i := range c.Providers {
		if c.Providers[i].Name == name {
			return &c.Providers[i]
		}
	}
	return nil
}

// DefaultOrFirst returns the configured default provider or the first entry.
func (c *Config) DefaultOrFirst() *Provider {
	if p := c.ProviderByName(c.DefaultProvider); p != nil {
		return p
	}
	if len(c.Providers) > 0 {
		return &c.Providers[0]
	}
	return nil
}
