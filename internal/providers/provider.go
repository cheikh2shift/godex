package providers

import (
	"context"
	"fmt"
	"strings"

	"github.com/cheikh2shift/godex/internal/config"
)

var DebugMode bool

// Tool represents an executable tool.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
}

type Message struct {
	Role    string
	Content string
}

// Provider defines a pluggable LLM provider implementation.
type Provider interface {
	Send(ctx context.Context, prompt string) (string, error)
	SetThinkCallback(func(string))
	Cancel()
	Tools() []Tool
	CallTool(ctx context.Context, name string, args map[string]any) (string, error)
	Close() error
	ContextLimit() int
	TokenUsage() (input int, output int)
	Reset() error
	SetMessages(messages []Message) error
	AppendMessages(messages []Message) error
	SupportsNativeToolCalls() bool
	SetStatusChannel(chan<- string)
	SetModel(model string, contextLimit int) error
}

type factory func(cfg *config.Provider) (Provider, error)

var registry = map[string]factory{}

// Register registers a provider factory for the given type.
func Register(kind string, fn factory) {
	registry[strings.ToLower(strings.TrimSpace(kind))] = fn
}

// NewProvider builds a provider from configuration.
func NewProvider(cfg *config.Provider) (Provider, error) {
	kind := strings.ToLower(strings.TrimSpace(cfg.Type))
	if kind == "" {
		kind = "gemini"
	}
	fn, ok := registry[kind]
	if !ok {
		return nil, fmt.Errorf("unsupported provider type %q", cfg.Type)
	}
	return fn(cfg)
}
