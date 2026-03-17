package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"

	"github.com/cheikh-seck/godex/internal/agent"
	"github.com/cheikh-seck/godex/internal/config"
	godexcontext "github.com/cheikh-seck/godex/internal/context"
	"github.com/cheikh-seck/godex/internal/mcp"
	"github.com/cheikh-seck/godex/internal/wizard"
)

const (
	defaultConfigFile = "providers.yaml"
	sessionFileName   = "session.txt"
)

var ErrUserAborted = errors.New("user aborted")

type MCPServer interface {
	Name() string
	Tools() []mcp.Tool
	CallTool(ctx context.Context, name string, args map[string]interface{}) (string, error)
	AllowedPaths() []string
	AddPath(ctx context.Context, path string) error
	TempAddPath(path string)
	AddURL(ctx context.Context, url string) error
	RemovePath(ctx context.Context, path string) error
	RemoveURL(ctx context.Context, url string) error
	Close() error
}

var slashCommands = []string{"/add-path ", "/remove-path ", "/paths", "/tools", "/clear-context", "/exit", "/quit", "/save", "/save-exit", "/kill ", "/killbg", "/bg", "/clear", "/help"}

var thinkingStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("239")).
	Foreground(lipgloss.Color("245")).
	Background(lipgloss.Color("235")).
	Padding(0, 1)

var successBarStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("34")).
	Background(lipgloss.Color("34")).
	Foreground(lipgloss.Color("34")).
	Padding(0, 1)

type sessionEntry struct {
	Prompt    string
	Response  string
	Timestamp string
}

var (
	version      string
	buildTime    string
	printVersion bool
)

func main() {
	var (
		configPath   string
		providerName string
		runWizard    bool
		prompt       string
		autoConfirm  bool
	)

	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("unable to determine home directory: %v", err)
	}

	flag.StringVar(&configPath, "config", filepath.Join(home, ".godex", defaultConfigFile), "provider configuration YAML")
	flag.StringVar(&providerName, "provider", "", "provider name to use (defaults to configured default or first provider)")
	flag.BoolVar(&runWizard, "wizard", false, "launch the provider configuration wizard")
	flag.StringVar(&prompt, "prompt", "", "run a single prompt non-interactively")
	flag.BoolVar(&autoConfirm, "auto-confirm", false, "auto-run suggested commands in non-interactive mode")
	flag.BoolVar(&printVersion, "version", false, "print version information")
	flag.Parse()

	// Handle version flag
	if printVersion {
		fmt.Printf("godex version %s\n", version)
		if buildTime != "" {
			fmt.Printf("built at: %s\n", buildTime)
		}
		os.Exit(0)
	}

	if runWizard {
		if err := wizard.RunWizard(configPath); err != nil {
			log.Fatalf("wizard failed: %v", err)
		}
		return
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			log.Fatalf("config %s does not exist; run with --wizard to create it", configPath)
		}
		log.Fatalf("unable to load config: %v", err)
	}

	provider := cfg.DefaultOrFirst()
	if providerName != "" {
		if p := cfg.ProviderByName(providerName); p != nil {
			provider = p
		} else {
			var available []string
			for _, p := range cfg.Providers {
				available = append(available, p.Name)
			}
			log.Fatalf("provider %q not found. Available: %v", providerName, available)
		}
	}
	llmProvider, err := agent.GetProvider(provider)
	if err != nil {
		log.Fatalf("failed to create provider: %v", err)
	}

	if provider == nil {
		log.Fatalf("no provider configured; use --wizard to create one")
	}

	ctx, cancel := context.WithCancel(context.Background())

	servers, err := initMCPServers(ctx, provider)
	if err != nil {
		fmt.Printf("MCP init warning: %v\n", err)
	}

	if prompt != "" {
		err := runSinglePrompt(ctx, provider, prompt, autoConfirm, servers)
		cleanup(servers)
		if err != nil {
			log.Fatalf("prompt failed: %v", err)
		}
		return
	}

	fmt.Printf("GoDex - Connected to %s (%s)\n", provider.Name, provider.Model)
	fmt.Printf("MCP Servers: %d\n", len(servers))
	fmt.Println("Commands: /paths, /add-path <path>, /exit, Up/Down for history, Ctrl+C to cancel")
	fmt.Println("Type your prompt or /help for more options.")
	fmt.Println("Multiline: paste with newlines to enter multiline, Enter on empty line to submit")

	// Get working directory for session files
	wd, _ := os.Getwd()

	// Load previous session summary from ./.godex
	prevSession := loadPreviousSession(wd)
	if prevSession != "" {
		fmt.Println("[Session] Loaded previous session context")
	}

	// Load AGENTS.md if present
	agentsContext := loadAgentsFile(wd)
	if agentsContext != "" {
		fmt.Println("[Agents] Loaded AGENTS.md")
	}

	// Track session for summary
	var sessionEntries []sessionEntry
	var history []string

promptLoop:
	for {
		// Show prompt with background process count
		bgCount := getBgCount(servers)
		prompt := "> "
		if bgCount > 0 {
			prompt = fmt.Sprintf("[%d bg] >", bgCount)
		}

		contextLimit := llmProvider.ContextLimit()
		inputTokens, _ := llmProvider.TokenUsage()
		input, err := readPrompt(prompt, history, provider.Model, inputTokens, contextLimit)
		if err != nil {
			if err == ErrPromptAborted {
				fmt.Println("\n[Cancelled] Use /quit to exit or /save to save and exit.")
				agent.CancelPrompt(provider)
				continue
			}
			break
		}

		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}

		history = appendHistory(history, input)

		if input == "/exit" || input == "/quit" {
			break
		}
		if input == "/save" || input == "/save-exit" {
			saveSessionSync(wd, sessionEntries, provider)
			break
		}
		if input == "/killbg" {
			handleKillBg(servers)
			continue
		}
		if strings.HasPrefix(input, "/kill ") || input == "/kill" {
			handleKill(servers, input)
			continue
		}
		if input == "/bg" {
			handleListBg(servers)
			continue
		}
		if input == "/clear" {
			fmt.Print("\033[2J\033[H")
			continue
		}
		if input == "/help" {
			printHelp()
			continue
		}
		if strings.HasPrefix(input, "/add-path") {
			handleAddPath(servers, input, ctx)
			continue
		}
		if strings.HasPrefix(input, "/remove-path") {
			handleRemovePath(servers, input, ctx)
			continue
		}
		if input == "/paths" {
			handlePaths(servers)
			continue
		}
		if input == "/tools" {
			handleTools(servers)
			continue
		}
		if input == "/clear-context" {
			if err := llmProvider.Reset(); err != nil {
				fmt.Printf("Error resetting context: %v\n", err)
			} else {
				fmt.Println("Context cleared.")
			}
			continue
		}

		toolsDesc := getToolsDescription(servers)

		sessionContext := ""
		if prevSession != "" {
			sessionContext = fmt.Sprintf("\n\nPrevious session summary:\n%s\n", prevSession)
		}
		if agentsContext != "" {
			sessionContext += fmt.Sprintf("\n\nAGENTS.md instructions:\n%s\n", agentsContext)
		}

		fullPrompt := fmt.Sprintf(`You have access to these tools:
%s%s

CRITICAL: The current working directory is: %s
Use this path when the user asks about "this folder", "current directory", or similar.

IMPORTANT: When you need to read files, search, or get directory contents, you MUST call the appropriate tool with the CORRECT path.
Do NOT use example paths like "/path/to/directory" - use the actual path: %s

BASH TOOL LIMITATIONS:
- Shell variables like $HOME, $PATH are NOT expanded - use absolute paths instead
- Interactive commands (vim, less, top) will NOT work - use non-interactive alternatives
- Shell aliases are NOT expanded - use full command names
- NEVER run servers or long-running programs in foreground - they will hang. ALWAYS use background: true
- ALWAYS use background: true for any server, daemon, or program that doesn't exit immediately
- ALWAYS use background: true when starting a webserver or a long-running background task
- After starting a background process, use sleep before making requests to it
- Use kill_command with the PID to stop background processes when done

To call tools, respond with one or more JSON objects, each in its own markdown code block.
You can call multiple tools at once to be more efficient.

Example:
`+"```json"+`
{
  "name": "read_file",
  "arguments": {
    "path": "/absolute/path/to/file"
  }
}
`+"```"+`

IMPORTANT: Execute tools FIRST, then provide the final answer. Do NOT include any final answer, summary, or "FINAL_ANSWER:" until AFTER you have executed all necessary tools and received their results. If you need to run commands/tests to verify something, run them first before answering.

User request: %s`, toolsDesc, sessionContext, wd, wd, input)

		maxToolRounds := 10
		if provider.MaxToolRounds != nil && *provider.MaxToolRounds > 0 {
			maxToolRounds = *provider.MaxToolRounds
		}
		toolTimeout := 180 // default 3 minutes
		if provider.ToolTimeout != nil && *provider.ToolTimeout > 0 {
			toolTimeout = *provider.ToolTimeout
		}

		// Setup signal handling for cancellation during AI round
		stopSignal := make(chan os.Signal, 1)
		signal.Notify(stopSignal, os.Interrupt, syscall.SIGTERM)

		for round := 0; round < maxToolRounds; round++ {
			var streamed strings.Builder
			printedStreamed := false

			// Create a cancellable context for this round
			roundCtx, roundCancel := context.WithCancel(ctx)

			// Show loading indicator with timer
			stopSpinner := make(chan bool)
			go func() {
				elapsed := 0
				pulse := false
				ticker := time.NewTicker(1 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ticker.C:
						elapsed++
						pulse = !pulse
						icon := "o"
						if pulse {
							icon = "O"
						}
						fmt.Printf("\r\033[33m%s\033[0m - Thinking - %s", icon, formatElapsed(elapsed))
					case <-stopSpinner:
						return
					}
				}
			}()

			// Listen for interrupt in background
			go func() {
				select {
				case <-stopSignal:
					agent.CancelPrompt(provider)
					roundCancel()
				case <-roundCtx.Done():
				}
			}()

			resp, err := agent.SendPromptWithThink(roundCtx, provider, fullPrompt, func(think string) {
				streamed.WriteString(think)
			})

			// Stop spinner and clear line
			stopSpinner <- true
			fmt.Print("\r               \r")

			// Check if cancelled
			if roundCtx.Err() == context.Canceled {
				fmt.Println("\n[Cancelled]")
				agent.CancelPrompt(provider)
				break
			}

			if err != nil {
				fmt.Printf("\nError: %v\n", err)
				break
			}

			// Print thinking only once at the start
			if round == 0 && streamed.Len() > 0 {
				fmt.Println(renderThinking(streamed.String()))
				printedStreamed = true
			}

			preResp, finalResp, hasFinal := splitFinalAnswer(resp)

			// Only execute tool calls if response is primarily tool calls (JSON format)
			// If there's substantial explanatory text, don't treat as tool call
			toolCalls, isToolCallResponse := shouldExecuteToolCall(preResp)
			toolCalls, missingTools := filterToolCallsByAvailability(servers, toolCalls)
			if len(missingTools) > 0 {
				fmt.Printf("\n[Ignored unknown tool(s): %s]\n", strings.Join(missingTools, ", "))
			}

			fmt.Printf("[Round %d/%d] Got %d tool calls, isToolCallResponse=%v\n", round+1, maxToolRounds, len(toolCalls), isToolCallResponse)

			if !isToolCallResponse || len(toolCalls) == 0 {
				// No valid tool calls - print thinking text and final response, then stop
				if !printedStreamed {
					thinkingText := extractThinkingText(preResp)
					if thinkingText != "" {
						fmt.Println(renderThinking(thinkingText))
					}
				}
				output := resp
				if hasFinal {
					output = finalResp
				}
				if strings.TrimSpace(output) != "" {
					go playSound()
					fmt.Printf("\n\n%s\n%s\n", renderSuccessBar(), renderMarkdown(output))
				}
				// Save to session
				sessionEntries = append(sessionEntries, sessionEntry{
					Prompt:   input,
					Response: output,
				})
				break
			}

			// Extract and print thinking text (non-JSON parts) in muted color
			if !printedStreamed {
				thinkingText := extractThinkingText(preResp)
				if thinkingText != "" {
					fmt.Println(renderThinking(thinkingText))
				}
			}

			// Execute all tool calls
			fmt.Printf("\n")
			if round == 0 {
				fmt.Print("\033[90m")
				fmt.Printf("> %s\n", input)
				fmt.Printf("%s\n", resp)
				fmt.Print("\033[0m")
			}
			fmt.Printf("[Executing %d tool(s)] (round %d/%d)\n", len(toolCalls), round+1, maxToolRounds)
			var toolResults []string
			hasError := false
			for _, tc := range toolCalls {
				toolName := tc["name"].(string)
				args := tc["arguments"].(map[string]interface{})

				// Get tool description
				toolDesc := getToolDescription(servers, toolName)
				if toolDesc != "" {
					fmt.Print("\033[90m")
					fmt.Printf("  %s\n", toolDesc)
					fmt.Print("\033[0m")
				}

				var argsStr string
				for k, v := range args {
					if argsStr != "" {
						argsStr += ", "
					}
					argsStr += fmt.Sprintf("%s=%v", k, v)
				}
				fmt.Printf("[%s] %s\n", toolName, argsStr)
				result, err := callTool(roundCtx, servers, toolName, args, toolTimeout)
				if errors.Is(err, ErrUserAborted) {
					fmt.Println("\nUser aborted.")
					goto promptLoop
				}
				if err != nil {
					errMsg := fmt.Sprintf("ERROR: %v", err)
					fmt.Printf("  Error: %v\n", err)
					toolResults = append(toolResults, errMsg)
					hasError = true
				} else {
					toolResults = append(toolResults, truncate(result, 500))
				}
			}

			if hasFinal {
				if strings.TrimSpace(finalResp) != "" {
					go playSound()
					fmt.Printf("\n\n%s\n%s\n", renderSuccessBar(), renderMarkdown(finalResp))
				}
				sessionEntries = append(sessionEntries, sessionEntry{
					Prompt:   input,
					Response: finalResp,
				})
				break
			}

			// Continue even if there were errors - send results to LLM
			if hasError {
				fmt.Printf("\n[Some tools had errors, asking LLM to handle...]\n")
			}

			// Ask for final answer with all tool results (including errors)
			fullPrompt = fmt.Sprintf("User asked: %s\n\nTool results:\n%s\n\nYou MUST now provide the FINAL answer. Do NOT make any more tool calls. If there were errors, explain them. Start your response with 'FINAL_ANSWER:'", input, strings.Join(toolResults, "\n---\n"))
		}

		signal.Stop(stopSignal)
		close(stopSignal)

		// Save session entry for max rounds case
		if len(sessionEntries) == 0 || sessionEntries[len(sessionEntries)-1].Prompt != input {
			sessionEntries = append(sessionEntries, sessionEntry{
				Prompt:   input,
				Response: "Max tool rounds reached",
			})
		}
	}

	cancel()
	cleanup(servers)
	fmt.Println("Goodbye!")
}

func initMCPServers(ctx context.Context, provider *config.Provider) ([]MCPServer, error) {
	if len(provider.MCPServers) == 0 {
		fmt.Println("[MCP] No MCP servers configured")
		return nil, nil
	}

	fmt.Printf("[MCP] Initializing %d MCP server(s)...\n", len(provider.MCPServers))

	wd, _ := os.Getwd()
	fmt.Printf("[MCP] Working directory: %s\n", wd)

	var servers []MCPServer

	for i, serverConfig := range provider.MCPServers {
		if strings.TrimSpace(serverConfig.Name) == "" {
			fmt.Printf("[MCP] [!] Skipping unnamed MCP server entry\n")
			continue
		}
		fmt.Printf("[MCP] [%d/%d] Connecting to server: %s\n", i+1, len(provider.MCPServers), serverConfig.Name)

		var server MCPServer

		paths := serverConfig.AllowedPaths

		if serverConfig.Name == "filesystem" || serverConfig.Name == "files" {
			if len(paths) == 0 {
				paths = []string{wd}
			} else {
				hasWd := false
				for _, p := range paths {
					if p == wd || p == "." || p == "./" {
						hasWd = true
						break
					}
				}
				if !hasWd {
					paths = append(paths, wd)
				}
			}
			paths = uniqueStrings(paths)
			fmt.Printf("[MCP]   Type: inline (Go)\n")
			fmt.Printf("[MCP]   Allowed paths: %v\n", paths)
			server = mcp.NewFileSystemServer(paths)
		} else if serverConfig.Name == "bash" || serverConfig.Name == "shell" || serverConfig.Name == "exec" {
			if len(paths) == 0 {
				paths = []string{wd}
			} else {
				hasWd := false
				for _, p := range paths {
					if p == wd || p == "." || p == "./" {
						hasWd = true
						break
					}
				}
				if !hasWd {
					paths = append(paths, wd)
				}
			}
			paths = uniqueStrings(paths)
			fmt.Printf("[MCP]   Type: inline (Go - command execution)\n")
			fmt.Printf("[MCP]   Allowed paths: %v\n", paths)
			server = mcp.NewBashServer(paths)
		} else if serverConfig.Name == "webscraper" || serverConfig.Name == "web" || serverConfig.Name == "browser" {
			paths = uniqueStrings(paths)
			fmt.Printf("[MCP]   Type: inline (Go - web scraper with JS rendering)\n")
			fmt.Printf("[MCP]   Allowed URLs: %v\n", paths)
			server = mcp.NewWebScraperServer(paths)
		} else {
			fmt.Printf("[MCP]   Type: external\n")
			fmt.Printf("[MCP]   Command: %s %v\n", serverConfig.Command, serverConfig.Args)
			fmt.Printf("[MCP]   Allowed paths: %v\n", serverConfig.AllowedPaths)

			if strings.TrimSpace(serverConfig.Command) == "" {
				fmt.Printf("[MCP] [!] Skipping external MCP server %q: missing command\n", serverConfig.Name)
				continue
			}

			executor, err := mcp.NewMCPServer(ctx, mcp.MCPServer{
				Name:         serverConfig.Name,
				Command:      serverConfig.Command,
				Args:         serverConfig.Args,
				Env:          serverConfig.Env,
				Transport:    serverConfig.Transport,
				AllowedPaths: serverConfig.AllowedPaths,
			}, wd)
			if err != nil {
				fmt.Printf("[MCP] [!] Failed to connect to %s: %v\n", serverConfig.Name, err)
				continue
			}
			server = executor
		}

		servers = append(servers, server)

		toolCount := len(server.Tools())
		fmt.Printf("[MCP] [+] Connected to %s with %d tool(s)\n", serverConfig.Name, toolCount)

		if toolCount > 0 {
			toolNames := make([]string, toolCount)
			for i, t := range server.Tools() {
				toolNames[i] = t.Name
			}
			fmt.Printf("[MCP]   Tools: %s\n", strings.Join(toolNames, ", "))
		}
	}

	if len(servers) == 0 {
		fmt.Println("[MCP] No MCP servers connected")
		return servers, nil
	}

	fmt.Printf("[MCP] Connected to %d MCP server(s)\n", len(servers))
	return servers, nil
}

func cleanup(servers []MCPServer) {
	for _, s := range servers {
		s.Close()
	}
}

func handleAddPath(servers []MCPServer, input string, ctx context.Context) {
	parts := strings.Fields(input)
	if len(parts) < 3 {
		fmt.Println("Usage: /add-path <filesys|url> <value>")
		fmt.Println("  filesys <path> - Add allowed path to filesystem/bash server")
		fmt.Println("  url <url>      - Add allowed URL to web scraper server")
		return
	}
	pathType := strings.ToLower(parts[1])
	value := strings.TrimSpace(strings.Join(parts[2:], " "))
	if value == "" {
		fmt.Println("Usage: /add-path <filesys|url> <value>")
		return
	}
	if len(servers) == 0 {
		fmt.Println("No MCP servers configured")
		return
	}

	isURL := pathType == "url"
	if !isURL {
		abs, err := resolveUserPath(value)
		if err != nil {
			fmt.Printf("Error resolving path: %v\n", err)
			return
		}
		value = abs
	}
	added := false

	for _, server := range servers {
		if isURL {
			if strings.Contains(server.Tools()[0].Name, "web") || strings.Contains(server.Tools()[0].Name, "scraper") || strings.Contains(server.Tools()[0].Name, "fetch") {
				if err := server.AddURL(ctx, value); err != nil {
					fmt.Printf("Error adding URL: %v\n", err)
					return
				}
				fmt.Printf("Added URL '%s' to %s\n", value, server.Name())
				added = true
			}
		} else {
			if strings.Contains(server.Tools()[0].Name, "file") || strings.Contains(server.Tools()[0].Name, "bash") || strings.Contains(server.Tools()[0].Name, "command") {
				if err := server.AddPath(ctx, value); err != nil {
					fmt.Printf("Error adding path: %v\n", err)
					return
				}
				fmt.Printf("Added path '%s' to  %s\n", value, server.Name())
				added = true
			}
		}
	}
	if isURL && !added {
		fmt.Println("No web scraper server found")
	} else if !isURL && !added {
		fmt.Println("No filesystem server found")
	}
}

func handleRemovePath(servers []MCPServer, input string, ctx context.Context) {
	parts := strings.Fields(input)
	if len(parts) < 3 {
		fmt.Println("Usage: /remove-path <filesys|url> <value>")
		fmt.Println("  filesys <path> - Remove allowed path from filesystem/bash server")
		fmt.Println("  url <url>      - Remove allowed URL from web scraper server")
		return
	}
	pathType := strings.ToLower(parts[1])
	value := strings.TrimSpace(strings.Join(parts[2:], " "))
	if value == "" {
		fmt.Println("Usage: /remove-path <filesys|url> <value>")
		return
	}
	if len(servers) == 0 {
		fmt.Println("No MCP servers configured")
		return
	}

	isURL := pathType == "url"
	if !isURL {
		abs, err := resolveUserPath(value)
		if err != nil {
			fmt.Printf("Error resolving path: %v\n", err)
			return
		}
		value = abs
	}
	removed := false

	for _, server := range servers {
		if isURL {
			if strings.Contains(server.Tools()[0].Name, "web") || strings.Contains(server.Tools()[0].Name, "scraper") || strings.Contains(server.Tools()[0].Name, "fetch") {
				if err := server.RemoveURL(ctx, value); err != nil {
					fmt.Printf("Error removing URL: %v\n", err)
					return
				}
				fmt.Printf("Removed URL '%s' from %s\n", value, server.Tools()[0].Name)
				removed = true
			}
		} else {
			if strings.Contains(server.Tools()[0].Name, "file") || strings.Contains(server.Tools()[0].Name, "bash") || strings.Contains(server.Tools()[0].Name, "command") {
				if err := server.RemovePath(ctx, value); err != nil {
					fmt.Printf("Error removing path: %v\n", err)
					return
				}
				fmt.Printf("Removed path '%s' from %s (all file/command operations)\n", value, server.Tools()[0].Name)
				removed = true
			}
		}
	}
	if isURL && !removed {
		fmt.Println("No web scraper server found")
	} else if !isURL && !removed {
		fmt.Println("No filesystem server found")
	}
}

func handlePaths(servers []MCPServer) {
	if len(servers) == 0 {
		fmt.Println("No MCP servers configured")
		return
	}

	typePaths := make(map[string][]string)
	for _, server := range servers {
		paths := server.AllowedPaths()
		serverType := server.Name()
		if len(server.Tools()) > 0 {
			if strings.Contains(server.Tools()[0].Name, "web") || strings.Contains(server.Tools()[0].Name, "fetch") {
				serverType = "url"
			}
		}
		for _, p := range paths {
			found := false
			for _, existing := range typePaths[serverType] {
				if existing == p {
					found = true
					break
				}
			}
			if !found {
				typePaths[serverType] = append(typePaths[serverType], p)
			}
		}
	}

	for serverType, paths := range typePaths {
		fmt.Printf("%s: %s\n", serverType, strings.Join(paths, ", "))
	}
}

func handleTools(servers []MCPServer) {
	if len(servers) == 0 {
		fmt.Println("No MCP servers configured")
		return
	}
	for _, server := range servers {
		fmt.Printf("Tools for %s:\n", server.Tools()[0].Name)
		for _, tool := range server.Tools() {
			fmt.Printf("  - %s: %s\n", tool.Name, tool.Description)
		}
	}
}

func handleKillBg(servers []MCPServer) {
	for _, server := range servers {
		if bs, ok := server.(*mcp.BashServer); ok {
			result, err := bs.KillAllBackground()
			if err != nil {
				fmt.Printf("Error: %v\n", err)
			} else {
				fmt.Println(result)
			}
			return
		}
	}
	fmt.Println("No bash server found")
}

func handleKill(servers []MCPServer, input string) {
	fields := strings.Fields(input)
	if len(fields) != 2 {
		fmt.Println("Usage: /kill <pid> | /kill --prune")
		return
	}
	if fields[1] == "--prune" {
		for _, server := range servers {
			if bs, ok := server.(*mcp.BashServer); ok {
				removed, err := bs.PruneBackground()
				if err != nil {
					fmt.Printf("Error: %v\n", err)
				} else if len(removed) == 0 {
					fmt.Println("No dead background entries to prune")
				} else {
					fmt.Printf("Removed dead background entries: %v\n", removed)
				}
				return
			}
		}
		fmt.Println("No bash server found")
		return
	}
	pid, err := strconv.Atoi(fields[1])
	if err != nil || pid <= 0 {
		fmt.Println("Usage: /kill <pid> | /kill --prune")
		return
	}

	for _, server := range servers {
		if bs, ok := server.(*mcp.BashServer); ok {
			result, err := bs.CallTool(context.Background(), "kill_command", map[string]interface{}{
				"pid": float64(pid),
			})
			if err != nil {
				fmt.Printf("Error: %v\n", err)
			} else {
				fmt.Println(result)
			}
			return
		}
	}
	fmt.Println("No bash server found")
}

func getToolDescription(servers []MCPServer, toolName string) string {
	for _, server := range servers {
		for _, tool := range server.Tools() {
			if tool.Name == toolName {
				return tool.Description
			}
		}
	}
	return ""
}

func handleListBg(servers []MCPServer) {
	for _, server := range servers {
		if bs, ok := server.(*mcp.BashServer); ok {
			result, err := bs.ListBackground()
			if err != nil {
				fmt.Printf("Error: %v\n", err)
			} else {
				fmt.Println(result)
			}
			return
		}
	}
	fmt.Println("No bash server found")
}

func getBgCount(servers []MCPServer) int {
	for _, server := range servers {
		if bs, ok := server.(*mcp.BashServer); ok {
			return bs.BackgroundCount()
		}
	}
	return 0
}

func printHelp() {
	fmt.Println(`
Multiline: Enter to add new line, Enter again on empty line to submit

Commands:
  /add-path <filesys|url> <value> - Add allowed path/URL to MCP server
                                    filesys <path> - Add to filesystem/bash
                                    url <url>      - Add to web scraper
  /remove-path <filesys|url> <value> - Remove allowed path/URL from MCP server
                                    filesys <path> - Remove from filesystem/bash
                                    url <url>      - Remove from web scraper
  /paths            - Show current allowed paths
  /tools            - Show available MCP tools
  /clear-context    - Reset context counter and LLM client
  /save, /save-exit - Save session and exit
  /kill <pid>       - Kill a background process by PID
  /killbg           - Kill all background processes
  /bg               - List background processes
  /clear            - Clear the terminal
  /exit, /quit     - Exit the program
  /help            - Show this help

Tips:
  - Paste multiline text - waits for more input automatically
  - Up/Down arrows for command history
  - Tab to autocomplete / commands

Available MCP tools:
  filesystem:
    read_file(path)       - Read a file
    write_file(path, content) - Write to a file
    list_directory(path) - List directory contents
    create_directory(path) - Create a directory
    delete_file(path)    - Delete a file
    search_files(path, pattern) - Search for files
    get_file_info(path)  - Get file information

  bash:
    run_command(command)  - Run a shell command

  webscraper:
    fetch_url(url)       - Fetch URL with JavaScript rendering
    search_html(html, selector, text) - Search HTML content
    get_links(html)      - Extract all links from HTML

Examples:
  > read_file /home/user/project/main.go
  > write_file /home/user/project/test.txt "Hello World"
  > run_command "ls -la"
  > fetch_url "https://example.com"`)
}

func uniqueStrings(input []string) []string {
	seen := make(map[string]bool)
	result := []string{}
	for _, s := range input {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}

func runSinglePrompt(ctx context.Context, provider *config.Provider, prompt string, autoConfirm bool, servers []MCPServer) error {
	tree, _ := godexcontext.BuildTree(".")
	toolsDesc := getToolsDescription(servers)
	wd, _ := os.Getwd()
	fullPrompt := fmt.Sprintf(`You have access to these tools:
%s

CRITICAL: The current working directory is: %s
Use this path when the user asks about "this folder", "current directory", or similar.

IMPORTANT: When you need to read files, search, or get directory contents, you MUST call the appropriate tool with the CORRECT path.
Do NOT use example paths like "/path/to/directory" - use the actual path: %s

BASH TOOL LIMITATIONS:
- Shell variables like $HOME, $PATH are NOT expanded - use absolute paths instead
- Interactive commands (vim, less, top) will NOT work - use non-interactive alternatives
- Shell aliases are NOT expanded - use full command names
- NEVER run servers or long-running programs in foreground - they will hang. ALWAYS use background: true
- ALWAYS use background: true for any server, daemon, or program that doesn't exit immediately
- ALWAYS use background: true when starting a webserver or a long-running background task
- After starting a background process, use sleep before making requests to it
- Use kill_command with the PID to stop background processes when done

To call tools, respond with one or more JSON objects, each in its own markdown code block.
You can call multiple tools at once to be more efficient.

Example:
`+"```json"+`
{
  "name": "read_file",
  "arguments": {
    "path": "/absolute/path/to/file"
  }
}
`+"```"+`

IMPORTANT: Execute tools FIRST, then provide the final answer. Do NOT include any final answer, summary, or "FINAL_ANSWER:" until AFTER you have executed all necessary tools and received their results. If you need to run commands/tests to verify something, run them first before answering.

Project tree:
%s

User request: %s`, toolsDesc, wd, wd, tree, prompt)

	// Get tool settings
	maxToolRounds := 10
	if provider.MaxToolRounds != nil {
		maxToolRounds = *provider.MaxToolRounds
	}
	toolTimeout := 180
	if provider.ToolTimeout != nil {
		toolTimeout = *provider.ToolTimeout
	}

	input := prompt

	for round := 0; round < maxToolRounds; round++ {
		resp, err := agent.SendPromptWithThink(ctx, provider, fullPrompt, func(think string) {
			fmt.Print(think)
		})
		if err != nil {
			return err
		}

		preResp, finalResp, hasFinal := splitFinalAnswer(resp)

		// Only execute tool calls if response is primarily tool calls (JSON format)
		toolCalls, isToolCallResponse := shouldExecuteToolCall(preResp)
		toolCalls, missingTools := filterToolCallsByAvailability(servers, toolCalls)
		if len(missingTools) > 0 {
			fmt.Printf("\n[Ignored unknown tool(s): %s]\n", strings.Join(missingTools, ", "))
		}

		fmt.Printf("[Round %d/%d] Got %d tool calls, isToolCallResponse=%v\n", round+1, maxToolRounds, len(toolCalls), isToolCallResponse)

		if !isToolCallResponse || len(toolCalls) == 0 {
			// No tool calls - print final response and stop
			output := resp
			if hasFinal {
				output = finalResp
			}
			if strings.TrimSpace(output) != "" {
				fmt.Printf("\n\n%s\n%s\n", renderSuccessBar(), renderMarkdown(output))
			}
			break
		}

		// Execute all tool calls
		fmt.Printf("\n")
		if round == 0 {
			fmt.Print("\033[90m")
			fmt.Printf("> %s\n", input)
			fmt.Printf("%s\n", resp)
			fmt.Print("\033[0m")
		}
		fmt.Printf("[Executing %d tool(s)] (round %d/%d)\n", len(toolCalls), round+1, maxToolRounds)
		var toolResults []string
		hasError := false
		for _, tc := range toolCalls {
			toolName := tc["name"].(string)
			args := tc["arguments"].(map[string]interface{})

			// Get tool description
			toolDesc := getToolDescription(servers, toolName)
			if toolDesc != "" {
				fmt.Print("\033[90m")
				fmt.Printf("  %s\n", toolDesc)
				fmt.Print("\033[0m")
			}

			var argsStr string
			for k, v := range args {
				if argsStr != "" {
					argsStr += ", "
				}
				argsStr += fmt.Sprintf("%s=%v", k, v)
			}
			fmt.Printf("[%s] %s\n", toolName, argsStr)
			result, err := callTool(ctx, servers, toolName, args, toolTimeout)
			if err != nil {
				errMsg := fmt.Sprintf("ERROR: %v", err)
				fmt.Printf("  Error: %v\n", err)
				toolResults = append(toolResults, errMsg)
				hasError = true
			} else {
				toolResults = append(toolResults, truncate(result, 500))
			}
		}

		if hasFinal {
			if strings.TrimSpace(finalResp) != "" {
				fmt.Printf("\n\n%s\n%s\n", renderSuccessBar(), renderMarkdown(finalResp))
			}
			break
		}

		// Continue even if there were errors - send results to LLM
		if hasError {
			fmt.Printf("\n[Some tools had errors, asking LLM to handle...]\n")
		}

		// Ask for final answer with all tool results (including errors)
		fullPrompt = fmt.Sprintf("User asked: %s\n\nTool results:\n%s\n\nYou MUST now provide the FINAL answer. Do NOT make any more tool calls. If there were errors, explain them. Start your response with 'FINAL_ANSWER:", input, strings.Join(toolResults, "\n---\n"))
	}

	return nil
}

func getToolsDescription(servers []MCPServer) string {
	if len(servers) == 0 {
		return "No tools available."
	}

	var desc strings.Builder
	for _, server := range servers {
		tools := server.Tools()
		if len(tools) == 0 {
			continue
		}
		serverName := server.Name()
		desc.WriteString(fmt.Sprintf("\n[%s]\n", serverName))
		for _, tool := range tools {
			desc.WriteString(fmt.Sprintf("  - %s: %s\n", tool.Name, tool.Description))
		}
	}
	return desc.String()
}

func renderThinking(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	width := guessTerminalWidth()
	style := thinkingStyle
	if width > 0 {
		style = style.Width(max(0, width-4))
	}
	return style.Render(text)
}

func renderSuccessBar() string {
	width := guessTerminalWidth()
	style := successBarStyle
	if width > 0 {
		target := int(float64(width) * 0.35)
		style = style.Width(max(10, target))
	}
	return style.Render(" OK ")
}

func renderMarkdown(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	width := guessTerminalWidth()
	renderer, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return text
	}
	out, err := renderer.Render(text)
	if err != nil {
		return text
	}
	return strings.TrimRight(out, "\n")
}

func guessTerminalWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		return w
	}
	return 0
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func formatElapsed(totalSeconds int) string {
	if totalSeconds >= 60 {
		minutes := totalSeconds / 60
		seconds := totalSeconds % 60
		return fmt.Sprintf("%dm%02d", minutes, seconds)
	}
	return fmt.Sprintf("%ds", totalSeconds)
}

func parseToolCall(text string) (string, string, map[string]interface{}, bool) {
	text = strings.TrimSpace(text)

	if strings.HasPrefix(text, "{") {
		var toolData map[string]interface{}
		if err := json.Unmarshal([]byte(text), &toolData); err == nil {
			if name, ok := toolData["name"].(string); ok {
				args := make(map[string]interface{})
				if toolArgs, ok := toolData["arguments"].(map[string]interface{}); ok {
					args = toolArgs
				} else if toolArgs, ok := toolData["args"].(map[string]interface{}); ok {
					args = toolArgs
				}
				return name, name, args, true
			}
		}
	}

	lines := strings.Split(text, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "call:") || strings.HasPrefix(lower, "tool_call:") || strings.HasPrefix(lower, "execute:") {
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) < 2 {
				continue
			}
			toolPart := strings.TrimSpace(parts[1])
			name := ""
			args := make(map[string]interface{})
			if idx := strings.Index(toolPart, "("); idx > 0 {
				name = strings.TrimSpace(toolPart[:idx])
				argsStr := strings.Trim(toolPart[idx:], "()")
				args = parseArgs(argsStr)
			}
			if name != "" {
				return name, name, args, true
			}
		}
	}

	return "", "", nil, false
}

func parseArgs(argsStr string) map[string]interface{} {
	args := make(map[string]interface{})
	if argsStr == "" {
		return args
	}
	parts := strings.Split(argsStr, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if idx := strings.Index(part, "="); idx > 0 {
			key := strings.TrimSpace(part[:idx])
			value := strings.TrimSpace(part[idx+1:])
			value = strings.Trim(value, "\"'")
			args[key] = value
		}
	}
	return args
}

func callTool(ctx context.Context, servers []MCPServer, name string, args map[string]interface{}, timeoutSecs int) (string, error) {
	normalizeToolPathArgs(name, args)
	for _, server := range servers {
		for _, tool := range server.Tools() {
			if tool.Name == name {
				ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSecs)*time.Second)
				defer cancel()
				result, err := server.CallTool(ctx, name, args)
				if isPathRestrictionError(err) {
					path := extractPathFromError(err)
					allowedPaths := server.AllowedPaths()
					selected := showPathRestrictionPrompt(path, allowedPaths)
					switch selected {
					case 0:
						server.AddPath(ctx, path)
						result, err = server.CallTool(ctx, name, args)
						if err != nil {
							return "", err
						}
						return result, nil
					case 1:
						server.TempAddPath(path)
						result, err = server.CallTool(ctx, name, args)
						server.RemovePath(ctx, path)
						if err != nil {
							return "", err
						}
						return result, nil
					case 2:
						return "", ErrUserAborted
					case 3:
						return "", fmt.Errorf("PATH_RESTRICTED: '%s' is not in allowed paths. Do NOT try to access this path. Find an alternative solution that does not require this file/path. If no alternative exists, respond with FINAL_ANSWER: and explain the restriction.", path)
					}
					return "", err
				}
				return result, err
			}
		}
	}
	return "", fmt.Errorf("tool %s not found", name)
}

func isPathRestrictionError(err error) bool {
	if err == nil {
		return false
	}
	errMsg := err.Error()
	return strings.Contains(errMsg, "path not allowed") ||
		strings.Contains(errMsg, "command not allowed") ||
		strings.Contains(errMsg, "code not allowed")
}

func extractPathFromError(err error) string {
	if err == nil {
		return ""
	}
	errMsg := err.Error()
	parts := strings.Split(errMsg, ":")
	if len(parts) > 1 {
		return strings.TrimSpace(parts[len(parts)-1])
	}
	return ""
}

func showPathRestrictionPrompt(path string, allowedPaths []string) int {
	options := []selectOption{
		{
			label: "Allow path",
			desc:  fmt.Sprintf("Add '%s' to allowed paths permanently", path),
		},
		{
			label: "Allow once",
			desc:  fmt.Sprintf("Allow '%s' for this command only", path),
		},
		{
			label: "Stop",
			desc:  "Tell LLM to stop immediately",
		},
		{
			label: "Find another way",
			desc:  "Tell LLM path is restricted - find alternative",
		},
	}
	return selectOptionPrompt("Path Restricted", options)
}

func normalizeToolPathArgs(toolName string, args map[string]interface{}) {
	if !isFilesystemTool(toolName) {
		return
	}
	raw, ok := args["path"].(string)
	if !ok {
		return
	}
	path := strings.TrimSpace(raw)
	if path == "" {
		return
	}
	resolved, err := resolveUserPath(path)
	if err == nil {
		args["path"] = resolved
		return
	}
}

func isFilesystemTool(toolName string) bool {
	switch toolName {
	case "read_file", "write_file", "list_directory", "create_directory", "delete_file", "search_files", "get_file_info":
		return true
	default:
		return false
	}
}

func resolveUserPath(input string) (string, error) {
	path := strings.TrimSpace(input)
	if path == "" {
		return "", fmt.Errorf("path is empty")
	}
	path = os.ExpandEnv(path)
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", fmt.Errorf("unable to resolve home directory")
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	if wd, err := os.Getwd(); err == nil && strings.TrimSpace(wd) != "" {
		return filepath.Clean(filepath.Join(wd, path)), nil
	}
	if pwd := strings.TrimSpace(os.Getenv("PWD")); pwd != "" {
		return filepath.Clean(filepath.Join(pwd, path)), nil
	}
	return "", fmt.Errorf("unable to resolve relative path: %s", input)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func extractAllToolCalls(text string) []map[string]interface{} {
	var results []map[string]interface{}

	candidates := []string{text}
	if normalized := normalizeToolCallText(text); normalized != text {
		candidates = append(candidates, normalized)
	}

	for _, candidate := range candidates {
		// First, try to find markdown code blocks (```json ... ``` or ``` ... ```)
		codeBlockRe := regexp.MustCompile("(?s)```(?:json)?\n(.*?)\n```")
		blocks := codeBlockRe.FindAllStringSubmatch(candidate, -1)

		for _, block := range blocks {
			content := strings.TrimSpace(block[1])
			// Try to parse the whole block as one JSON object
			var data map[string]interface{}
			if err := json.Unmarshal([]byte(content), &data); err == nil {
				if _, ok := data["name"].(string); ok {
					results = append(results, processToolData(data))
				}
			} else {
				// If not a single object, maybe multiple objects in the block?
				results = append(results, extractJsonObjects(content)...)
			}
		}

		// If no tools found in code blocks, search the whole text for { ... } blobs
		if len(results) == 0 {
			results = append(results, extractJsonObjects(candidate)...)
		}

		// Safety fallback for the specific [TOOL_CALL] format
		if len(results) == 0 {
			toolCallRe := regexp.MustCompile(`(?s)\[TOOL_CALL\]\s*(.*?)\s*\[/TOOL_CALL\]`)
			tcMatches := toolCallRe.FindAllStringSubmatch(candidate, -1)
			for _, m := range tcMatches {
				if len(m) > 1 {
					nameRe := regexp.MustCompile(`tool\s*=>\s*"([^"]+)"`)
					if nameMatch := nameRe.FindStringSubmatch(m[1]); len(nameMatch) > 1 {
						results = append(results, map[string]interface{}{"name": nameMatch[1], "arguments": map[string]interface{}{}})
					}
				}
			}
		}

		if len(results) > 0 {
			break
		}
	}

	return results
}

func normalizeToolCallText(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 {
		return text
	}

	nonEmpty := 0
	withMargin := 0
	marginRe := regexp.MustCompile(`^\s*│`)
	stripRe := regexp.MustCompile(`^\s*│\s?`)

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		nonEmpty++
		if marginRe.MatchString(line) {
			withMargin++
		}
	}
	if nonEmpty == 0 || withMargin*2 < nonEmpty {
		return text
	}

	for i, line := range lines {
		lines[i] = stripRe.ReplaceAllString(line, "")
	}
	return strings.Join(lines, "\n")
}

// extractJsonObjects finds all balanced { ... } strings and tries to parse them as tool calls
func extractJsonObjects(text string) []map[string]interface{} {
	var results []map[string]interface{}
	startIdx := -1
	braceCount := 0

	for i, char := range text {
		if char == '{' {
			if braceCount == 0 {
				startIdx = i
			}
			braceCount++
		} else if char == '}' {
			if braceCount > 0 {
				braceCount--
				if braceCount == 0 && startIdx != -1 {
					potentialJson := text[startIdx : i+1]
					var data map[string]interface{}
					if err := json.Unmarshal([]byte(potentialJson), &data); err == nil {
						if _, ok := data["name"].(string); ok {
							results = append(results, processToolData(data))
						}
					}
				}
			}
		}
	}
	return results
}

func extractThinkingText(text string) string {
	codeBlockRe := regexp.MustCompile("(?s)```(?:json)?\\n.*?\\n```")
	thinking := codeBlockRe.ReplaceAllString(text, "")
	thinking = strings.TrimSpace(thinking)
	return thinking
}

func splitFinalAnswer(text string) (string, string, bool) {
	idx := strings.Index(text, "FINAL_ANSWER:")
	if idx < 0 {
		return text, "", false
	}
	pre := strings.TrimSpace(text[:idx])
	final := strings.TrimSpace(text[idx+len("FINAL_ANSWER:"):])
	return pre, final, true
}

func processToolData(data map[string]interface{}) map[string]interface{} {
	name, _ := data["name"].(string)
	args := make(map[string]interface{})
	if a, ok := data["arguments"].(map[string]interface{}); ok {
		args = a
	} else if a, ok := data["args"].(map[string]interface{}); ok {
		args = a
	}

	// Clean up string arguments
	for k, v := range args {
		if s, ok := v.(string); ok {
			s = strings.ReplaceAll(s, "\\u0026", "&")
			s = strings.ReplaceAll(s, "&#38;", "&")
			s = strings.ReplaceAll(s, "&amp;", "&")
			args[k] = s
		}
	}
	return map[string]interface{}{"name": name, "arguments": args}
}

func shouldExecuteToolCall(text string) ([]map[string]interface{}, bool) {
	toolCalls := extractAllToolCalls(text)
	if len(toolCalls) == 0 {
		return nil, false
	}
	return toolCalls, true
}

func filterToolCallsByAvailability(servers []MCPServer, toolCalls []map[string]interface{}) ([]map[string]interface{}, []string) {
	if len(toolCalls) == 0 {
		return nil, nil
	}
	available := make(map[string]struct{})
	for _, server := range servers {
		for _, tool := range server.Tools() {
			available[tool.Name] = struct{}{}
		}
	}
	var filtered []map[string]interface{}
	var missing []string
	for _, tc := range toolCalls {
		name, _ := tc["name"].(string)
		if name == "" {
			continue
		}
		if _, ok := available[name]; ok {
			filtered = append(filtered, tc)
		} else {
			missing = append(missing, name)
		}
	}
	return filtered, uniqueStrings(missing)
}

func loadPreviousSession(cwd string) string {
	sessionPath := filepath.Join(cwd, ".godex", sessionFileName)
	data, err := os.ReadFile(sessionPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func loadAgentsFile(cwd string) string {
	agentsPath := filepath.Join(cwd, "AGENTS.md")
	data, err := os.ReadFile(agentsPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func playSound() {
	var cmd *exec.Cmd
	// Try different sound playback methods based on OS
	if _, err := exec.LookPath("paplay"); err == nil {
		cmd = exec.Command("paplay", "/usr/share/sounds/gnome/default/alerts/glass.ogg")
	} else if _, err := exec.LookPath("afplay"); err == nil {
		cmd = exec.Command("afplay", "/System/Library/Sounds/Glass.aiff")
	} else if _, err := exec.LookPath("espeak"); err == nil {
		cmd = exec.Command("espeak", "-s 150", "ding")
	} else {
		// Fallback to terminal bell
		fmt.Print("\a")
		return
	}
	cmd.Run()
}

func saveSessionSync(cwd string, entries []sessionEntry, provider *config.Provider) {
	if len(entries) == 0 {
		fmt.Println("[Session] No entries to save")
		return
	}

	fmt.Printf("[Session] Saving %d entries...\n", len(entries))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var promptBuilder strings.Builder
	promptBuilder.WriteString("Create a comprehensive summary of this session. Include:\n")
	promptBuilder.WriteString("1. What the user asked for\n")
	promptBuilder.WriteString("2. What tools were used and what they revealed\n")
	promptBuilder.WriteString("3. Key findings, decisions, or code that was written\n")
	promptBuilder.WriteString("4. Any errors encountered and how they were resolved\n\n")
	promptBuilder.WriteString("Be thorough - this will be used as context for future sessions.\n\n")
	promptBuilder.WriteString("Session transcript:\n\n")
	for _, e := range entries {
		promptBuilder.WriteString(fmt.Sprintf("User: %s\n", e.Prompt))
		promptBuilder.WriteString(fmt.Sprintf("Response: %s\n\n", e.Response))
	}
	promptBuilder.WriteString("\nProvide a detailed summary covering all the above points.")

	summary, err := agent.SendPrompt(ctx, provider, promptBuilder.String())
	if err != nil {
		fmt.Printf("[Session] Failed to get summary: %v\n", err)
		return
	}

	sessionPath := filepath.Join(cwd, ".godex", sessionFileName)
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0755); err != nil {
		fmt.Printf("[Session] Failed to create directory: %v\n", err)
		return
	}
	err = os.WriteFile(sessionPath, []byte(summary), 0644)
	if err != nil {
		fmt.Printf("[Session] Failed to save session: %v\n", err)
	} else {
		fmt.Printf("[Session] Saved to %s\n", sessionPath)
	}
}
