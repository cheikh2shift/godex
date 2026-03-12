package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cheikh-seck/godex/internal/agent"
	"github.com/cheikh-seck/godex/internal/config"
	godexcontext "github.com/cheikh-seck/godex/internal/context"
	"github.com/cheikh-seck/godex/internal/mcp"

	"github.com/peterh/liner"
)

const (
	defaultConfigFile = "providers.yaml"
	sessionFileName   = "session.txt"
)

type MCPServer interface {
	Tools() []mcp.Tool
	CallTool(ctx context.Context, name string, args map[string]interface{}) (string, error)
	AllowedPaths() []string
	AddPath(ctx context.Context, path string) error
	Close() error
}

type sessionEntry struct {
	Prompt    string
	Response  string
	Timestamp string
}

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
	flag.StringVar(&providerName, "provider", "", "provider entry to use (defaults to configured default or first provider)")
	flag.BoolVar(&runWizard, "wizard", false, "launch the provider configuration wizard")
	flag.StringVar(&prompt, "prompt", "", "run a single prompt non-interactively")
	flag.BoolVar(&autoConfirm, "auto-confirm", false, "auto-run suggested commands in non-interactive mode")
	flag.Parse()

	if runWizard {
		fmt.Println("Run with: godex --wizard")
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
			log.Fatalf("provider %q not found", providerName)
		}
	}
	if provider == nil {
		log.Fatalf("no provider configured; use --wizard to create one")
	}

	ctx := context.Background()

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
	fmt.Println("Commands: /paths, /add-path <path>, /exit, Up/Down for history, Escape to cancel")
	fmt.Println("Type your prompt or /help for more options.")

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

	rl := NewLiner()
	defer rl.Close()

	// Track session for summary
	var sessionEntries []sessionEntry

	for {
		input, err := rl.Prompt("> ")
		if err != nil {
			if err == liner.ErrPromptAborted {
				fmt.Println("\n[Cancelled]")
				agent.CancelPrompt(provider)
				continue
			}
			break
		}

		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}

		rl.AppendHistory(input)

		if input == "/exit" || input == "/quit" {
		}
		if input == "/exit" || input == "/quit" {
			break
		}
		if input == "/save" || input == "/save-exit" {
			saveSessionSync(wd, sessionEntries, provider)
			break
		}
		if input == "/help" {
			printHelp()
			continue
		}
		if strings.HasPrefix(input, "/add-path") {
			handleAddPath(servers, input, ctx)
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
The user is asking about: %s

IMPORTANT: When you need to read files, search, or get directory contents, you MUST call the appropriate tool with the CORRECT path.
Do NOT use example paths like "/path/to/directory" - use the actual path: %s

To call a tool, respond with ONLY a JSON object like:
{"name": "tool_name", "arguments": {"arg1": "value1"}}

When you have completed the task and want to provide the final answer, start your response with:
FINAL_ANSWER:

User request: %s`, toolsDesc, sessionContext, wd, input, wd, input)

		maxToolRounds := 10
		if provider.MaxToolRounds != nil && *provider.MaxToolRounds > 0 {
			maxToolRounds = *provider.MaxToolRounds
		}
		toolTimeout := 180 // default 3 minutes
		if provider.ToolTimeout != nil && *provider.ToolTimeout > 0 {
			toolTimeout = *provider.ToolTimeout
		}
		for round := 0; round < maxToolRounds; round++ {
			var streamed strings.Builder

			// Show loading indicator with timer
			stopSpinner := make(chan bool)
			go func() {
				elapsed := 0
				ticker := time.NewTicker(1 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ticker.C:
						elapsed++
						fmt.Printf("\r\033[90m[%ds]...\033[0m", elapsed)
					case <-stopSpinner:
						return
					}
				}
			}()

			resp, err := agent.SendPromptWithThink(ctx, provider, fullPrompt, func(think string) {
				streamed.WriteString(think)
			})

			// Stop spinner and clear line
			stopSpinner <- true
			fmt.Print("\r               \r")

			if err != nil {
				fmt.Printf("\nError: %v\n", err)
				break
			}

			// Print thinking only once at the start
			if round == 0 && streamed.Len() > 0 {
				fmt.Print("\033[90m")
				fmt.Print(streamed.String())
				fmt.Print("\033[0m\n")
			}

			// Only execute tool calls if response is primarily tool calls (JSON format)
			// If there's substantial explanatory text, don't treat as tool call
			toolCalls, isToolCallResponse := shouldExecuteToolCall(resp)

			fmt.Printf("[Round %d/%d] Got %d tool calls, isToolCallResponse=%v\n", round+1, maxToolRounds, len(toolCalls), isToolCallResponse)

			if !isToolCallResponse || len(toolCalls) == 0 {
				// No tool calls - print final response and stop
				if strings.TrimSpace(resp) != "" {
					// Check for FINAL_ANSWER marker
					finalResp := resp
					if idx := strings.Index(resp, "FINAL_ANSWER:"); idx >= 0 {
						finalResp = strings.TrimSpace(resp[idx+len("FINAL_ANSWER:"):])
						fmt.Printf("\n\n========================================\n%s\n", finalResp)
					} else {
						fmt.Printf("%s\n", resp)
					}
				}
				// Save to session
				sessionEntries = append(sessionEntries, sessionEntry{
					Prompt:   input,
					Response: resp,
				})
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

				var argsStr string
				for k, v := range args {
					if argsStr != "" {
						argsStr += ", "
					}
					argsStr += fmt.Sprintf("%s=%v", k, v)
				}
				fmt.Printf("[%s] %s\n", toolName, argsStr)
				result, err := callTool(servers, toolName, args, toolTimeout)
				if err != nil {
					errMsg := fmt.Sprintf("ERROR: %v", err)
					fmt.Printf("  Error: %v\n", err)
					toolResults = append(toolResults, errMsg)
					hasError = true
				} else {
					toolResults = append(toolResults, truncate(result, 500))
				}
			}

			// Continue even if there were errors - send results to LLM
			if hasError {
				fmt.Printf("\n[Some tools had errors, asking LLM to handle...]\n")
			}

			// Ask for final answer with all tool results (including errors)
			fullPrompt = fmt.Sprintf("User asked: %s\n\nTool results:\n%s\n\nYou MUST now provide the FINAL answer. Do NOT make any more tool calls. If there were errors, explain them. Start your response with 'FINAL_ANSWER:'", input, strings.Join(toolResults, "\n---\n"))
		}

		// Save session entry for max rounds case
		if len(sessionEntries) == 0 || sessionEntries[len(sessionEntries)-1].Prompt != input {
			sessionEntries = append(sessionEntries, sessionEntry{
				Prompt:   input,
				Response: "Max tool rounds reached",
			})
		}
	}

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
	parts := strings.SplitN(input, " ", 2)
	path := ""
	if len(parts) > 1 {
		path = strings.TrimSpace(parts[1])
	}
	if path == "" {
		fmt.Println("Usage: /add-path <path>")
		return
	}
	if len(servers) == 0 {
		fmt.Println("No MCP servers configured")
		return
	}
	for _, server := range servers {
		if err := server.AddPath(ctx, path); err != nil {
			fmt.Printf("Error adding path: %v\n", err)
			return
		}
		fmt.Printf("Added path '%s' to %s\n", path, server.Tools()[0].Name)
		//return
	}
}

func handlePaths(servers []MCPServer) {
	if len(servers) == 0 {
		fmt.Println("No MCP servers configured")
		return
	}
	for _, server := range servers {
		paths := server.AllowedPaths()
		fmt.Printf("%s: %s\n", "filesystem", strings.Join(paths, ", "))
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

func printHelp() {
	fmt.Println(`
Commands:
  /add-path <path>  - Add allowed path to MCP filesystem server
  /paths            - Show current allowed paths
  /tools            - Show available MCP tools
  /save, /save-exit - Save session and exit
  /exit, /quit      - Exit the program
  /help             - Show this help

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
	fullPrompt := agent.ToolIntroPrompt(prompt, tree)

	resp, err := agent.SendPromptWithThink(ctx, provider, fullPrompt, func(think string) {
		fmt.Print(think)
	})
	if err != nil {
		return err
	}

	fmt.Println(resp)
	return nil
}

var slashCommands = []string{"/add-path ", "/paths", "/tools", "/exit", "/quit", "/save", "/save-exit", "/help"}

func NewLiner() *liner.State {
	l := liner.NewLiner()
	l.SetCompleter(func(line string) []string {
		if !strings.HasPrefix(line, "/") {
			return nil
		}
		var matches []string
		for _, cmd := range slashCommands {
			if strings.HasPrefix(cmd, line) {
				matches = append(matches, cmd)
			}
		}
		return matches
	})
	return l
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
		desc.WriteString(fmt.Sprintf("\n%s tools:\n", server.Tools()[0].Name))
		for _, tool := range tools {
			desc.WriteString(fmt.Sprintf("  - %s: %s\n", tool.Name, tool.Description))
		}
	}
	return desc.String()
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

func callTool(servers []MCPServer, name string, args map[string]interface{}, timeoutSecs int) (string, error) {
	for _, server := range servers {
		for _, tool := range server.Tools() {
			if tool.Name == name {
				ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSecs)*time.Second)
				defer cancel()
				return server.CallTool(ctx, name, args)
			}
		}
	}
	return "", fmt.Errorf("tool %s not found", name)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func extractAllToolCalls(text string) []map[string]interface{} {
	var results []map[string]interface{}

	// Try JSON format: {"name": "...", "arguments": {...}}
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "{") && strings.HasSuffix(line, "}") {
			var data map[string]interface{}
			if err := json.Unmarshal([]byte(line), &data); err == nil {
				if name, ok := data["name"].(string); ok {
					args := make(map[string]interface{})
					if a, ok := data["arguments"].(map[string]interface{}); ok {
						args = a
						// Unescape HTML-encoded entities in string arguments
						for k, v := range args {
							if s, ok := v.(string); ok {
								s = strings.ReplaceAll(s, "\\u0026", "&")
								s = strings.ReplaceAll(s, "&#38;", "&")
								s = strings.ReplaceAll(s, "&amp;", "&")
								args[k] = s
							}
						}
					}
					results = append(results, map[string]interface{}{"name": name, "arguments": args})
				}
			}
		}
	}

	// Try <tool_code>name</tool_code> format
	re := `<tool_code>\s*(\w+)\s*</tool_code>`
	if r, err := regexp.Compile(re); err == nil {
		matches := r.FindAllStringSubmatch(text, -1)
		for _, m := range matches {
			if len(m) > 1 {
				results = append(results, map[string]interface{}{"name": m[1], "arguments": map[string]interface{}{}})
			}
		}
	}

	// Try "call: tool_name" or "execute: tool_name" format
	for _, line := range lines {
		lower := strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(lower, "call:") || strings.HasPrefix(lower, "execute:") || strings.HasPrefix(lower, "tool_call:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) > 1 {
				name := strings.TrimSpace(parts[1])
				name = strings.Trim(name, "() ")
				if name != "" {
					results = append(results, map[string]interface{}{"name": name, "arguments": map[string]interface{}{}})
				}
			}
		}
	}

	return results
}

func shouldExecuteToolCall(text string) ([]map[string]interface{}, bool) {
	toolCalls := extractAllToolCalls(text)
	if len(toolCalls) == 0 {
		return nil, false
	}

	// Check if the response is primarily tool calls (JSON format at start)
	lines := strings.Split(text, "\n")
	nonToolText := 0

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "{") && strings.Contains(line, "\"name\":") {
			continue
		}
		if strings.HasPrefix(line, "[") || strings.HasPrefix(line, "]") {
			continue
		}
		nonToolText++
	}

	// If there's a lot of explanatory text, don't execute tool calls
	if nonToolText > 3 && !strings.HasPrefix(strings.TrimSpace(text), "{") {
		return toolCalls, false
	}

	return toolCalls, true
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

func saveSessionSync(cwd string, entries []sessionEntry, provider *config.Provider) {
	if len(entries) == 0 {
		fmt.Println("[Session] No entries to save")
		return
	}

	fmt.Printf("[Session] Saving %d entries...\n", len(entries))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var promptBuilder strings.Builder
	promptBuilder.WriteString("Create the shortest possible summary (1-2 sentences max) of what was accomplished in this session:\n\n")
	for _, e := range entries {
		promptBuilder.WriteString(fmt.Sprintf("Q: %s\nA: %s\n\n", e.Prompt, truncate(e.Response, 150)))
	}
	promptBuilder.WriteString("\nRespond with ONLY the summary sentence(s), no preamble.")

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
