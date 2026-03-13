package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/cheikh-seck/godex/internal/agent"
	"github.com/cheikh-seck/godex/internal/config"
	projectctx "github.com/cheikh-seck/godex/internal/context"
	"github.com/cheikh-seck/godex/internal/mcp"

	"github.com/jesseduffield/gocui"
)

type App struct {
	provider       *config.Provider
	g              *gocui.Gui
	messages       []string
	sideContent    string
	status         string
	loading        bool
	projectRoot    string
	projectTree    string
	baseCtx        context.Context
	baseCancel     context.CancelFunc
	mcpExecutors   []*mcp.MCPToolExecutor
	toolRound      int
	lastToolData   string
	pendingCommand string
	toolRunning    bool
	inputBuffer    string
}

func NewApp(provider *config.Provider) *App {
	wd, _ := os.Getwd()
	baseCtx, baseCancel := context.WithCancel(context.Background())

	app := &App{
		provider:    provider,
		projectRoot: wd,
		baseCtx:     baseCtx,
		baseCancel:  baseCancel,
		messages:    []string{},
		toolRound:   0,
		toolRunning: false,
		status:      "ready",
	}

	app.g = gocui.NewGui()

	app.setupLayout()
	app.setupKeybindings()

	go app.initMCPServers()
	go app.loadTree()

	go app.runApp()

	return app
}

func (a *App) runApp() {
	if err := a.g.MainLoop(); err != nil && err != gocui.ErrQuit {
		fmt.Println("Error:", err)
	}
}

func (a *App) setupLayout() {
	g := a.g

	g.SetLayout(func(g *gocui.Gui) error {
		maxX, maxY := g.Size()

		// Messages view (left side)
		v, err := g.SetView("messages", 0, 0, maxX-40, maxY-4)
		if err != nil {
			if err != gocui.ErrUnknownView {
				return err
			}
			v.Title = "Messages"
			v.Wrap = true
			v.Autoscroll = true
		}
		fmt.Fprintln(v, fmt.Sprintf("[System] Connected to %s (%s)", a.provider.Name, a.provider.Model))

		// Side view (right side)
		v, err = g.SetView("side", maxX-39, 0, maxX-1, maxY-4)
		if err != nil {
			if err != gocui.ErrUnknownView {
				return err
			}
			v.Title = "Context"
			v.Wrap = true
			v.Autoscroll = true
		}
		fmt.Fprintln(v, a.sideContent)

		// Input view (bottom)
		v, err = g.SetView("input", 0, maxY-3, maxX-1, maxY-1)
		if err != nil {
			if err != gocui.ErrUnknownView {
				return err
			}
			v.Title = "Input"
			v.Editable = true
		}

		// Status bar
		v, err = g.SetView("status", 0, maxY-4, maxX-1, maxY-3)
		if err != nil {
			if err != gocui.ErrUnknownView {
				return err
			}
			v.Title = "Status"
			fmt.Fprintf(v, "Status: %s | MCP: %d servers | /confirm /cancel /add-path /paths | Ctrl+C quit", a.status, len(a.mcpExecutors))
		}

		return nil
	})
}

func (a *App) setupKeybindings() {
	g := a.g

	g.SetKeybinding("", gocui.KeyCtrlC, gocui.ModNone, func(g *gocui.Gui, v *gocui.View) error {
		a.Stop()
		return gocui.ErrQuit
	})

	g.SetKeybinding("input", gocui.KeyEnter, gocui.ModNone, func(g *gocui.Gui, v *gocui.View) error {
		input := strings.TrimSpace(v.Buffer())
		v.Clear()
		if input != "" {
			a.handleInput(input)
		}
		return nil
	})

	g.SetKeybinding("input", gocui.KeyCtrlD, gocui.ModNone, func(g *gocui.Gui, v *gocui.View) error {
		a.Stop()
		return gocui.ErrQuit
	})
}

func (a *App) handleInput(input string) {
	input = strings.TrimSpace(input)

	if strings.EqualFold(input, "/copy") {
		a.addMessage(fmt.Sprintf("[System] Copy not implemented"))
		return
	}

	if strings.EqualFold(input, "/save") {
		a.addMessage(fmt.Sprintf("[System] Save not implemented"))
		return
	}

	if strings.HasPrefix(strings.ToLower(input), "/add-path") {
		parts := strings.SplitN(input, " ", 2)
		path := ""
		if len(parts) > 1 {
			path = strings.TrimSpace(parts[1])
		}
		if path == "" {
			a.addMessage(fmt.Sprintf("[System] Usage: /add-path <path>"))
			return
		}
		a.addMCPPath(path)
		return
	}

	if strings.EqualFold(input, "/paths") {
		var pathList []string
		for _, executor := range a.mcpExecutors {
			paths := executor.AllowedPaths()
			pathList = append(pathList, fmt.Sprintf("%s: %s", executor.Name, strings.Join(paths, ", ")))
		}
		if len(pathList) == 0 {
			a.addMessage(fmt.Sprintf("[System] No MCP servers configured"))
		} else {
			a.addMessage(fmt.Sprintf("[System] Allowed paths:\n%s", strings.Join(pathList, "\n")))
		}
		return
	}

	if strings.EqualFold(input, "/confirm") && a.pendingCommand != "" {
		a.runShellCommand(a.pendingCommand)
		a.pendingCommand = ""
		return
	}

	if strings.EqualFold(input, "/cancel") {
		a.addMessage(fmt.Sprintf("[System] Command '%s' cancelled", a.pendingCommand))
		a.pendingCommand = ""
		return
	}

	if a.pendingCommand != "" {
		a.addMessage(fmt.Sprintf("[System] Command '%s' awaiting /confirm or /cancel", a.pendingCommand))
		return
	}

	a.addMessage(fmt.Sprintf("[You] %s", input))
	a.setLoading(true)
	a.toolRound = 0
	a.toolRunning = false
	go a.callProvider(input)
}

func (a *App) callProvider(prompt string) {
	ctx, cancel := context.WithTimeout(a.baseCtx, 5*time.Minute)
	defer cancel()

	fullPrompt := prompt
	if !a.toolRunning && a.toolRound == 0 {
		fullPrompt = agent.ToolIntroPrompt(prompt, a.projectTree)
	}

	resp, err := agent.SendPrompt(ctx, a.provider, fullPrompt)
	a.setLoading(false)

	if err != nil {
		a.addMessage(fmt.Sprintf("[Agent] Error: %v", err))
		a.updateStatus("error")
		return
	}

	if toolCall, ok := parseMCPToolCall(resp); ok && a.toolRound < 15 && len(a.mcpExecutors) > 0 {
		a.toolRound++
		a.toolRunning = true
		a.addMessage(fmt.Sprintf("[System] Calling MCP tool: %s(%v)", toolCall.name, toolCall.args))
		a.executeMCPTool(toolCall.name, toolCall.args)
		return
	}

	a.addMessage(fmt.Sprintf("[Agent] %s", resp))
	a.updateSideContext(resp)
	a.updateStatus("ready")
}

func (a *App) executeMCPTool(toolName string, args map[string]interface{}) {
	a.setLoading(true)
	a.updateStatus("executing MCP tool...")

	go func() {
		for _, executor := range a.mcpExecutors {
			for _, tool := range executor.Tools() {
				if tool.Name == toolName {
					result, err := executor.CallTool(a.baseCtx, toolName, args)
					a.setLoading(false)
					if err != nil {
						a.addMessage(fmt.Sprintf("[System] MCP tool %s failed: %v", toolName, err))
					} else {
						a.addMessage(fmt.Sprintf("[System] MCP tool %s result:", toolName))
						a.addMessage(fmt.Sprintf("[Agent] %s", result))
						a.lastToolData = fmt.Sprintf("Tool %s returned: %s", toolName, result)
						go a.callProvider(a.lastToolData)
					}
					return
				}
			}
		}
		a.setLoading(false)
		a.addMessage(fmt.Sprintf("[System] Tool %s not found", toolName))
	}()
}

func (a *App) addMCPPath(path string) {
	if len(a.mcpExecutors) == 0 {
		a.addMessage(fmt.Sprintf("[System] No MCP servers configured"))
		return
	}

	for _, executor := range a.mcpExecutors {
		if executor.Name == "filesystem" || strings.Contains(executor.Name, "file") {
			if err := executor.AddPath(a.baseCtx, path); err != nil {
				a.addMessage(fmt.Sprintf("[System] Failed to add path: %v", err))
				return
			}
			a.addMessage(fmt.Sprintf("[System] Added path '%s' to MCP server", path))
			return
		}
	}
	a.addMessage(fmt.Sprintf("[System] No filesystem MCP server found"))
}

func (a *App) initMCPServers() {
	for _, serverConfig := range a.provider.MCPServers {
		executor, err := mcp.NewMCPServer(a.baseCtx, mcp.MCPServer{
			Name:         serverConfig.Name,
			Command:      serverConfig.Command,
			Args:         serverConfig.Args,
			Env:          serverConfig.Env,
			Transport:    serverConfig.Transport,
			AllowedPaths: serverConfig.AllowedPaths,
		}, a.projectRoot)
		if err != nil {
			a.addMessage(fmt.Sprintf("[System] Failed to connect to MCP server %s: %v", serverConfig.Name, err))
			continue
		}
		a.mcpExecutors = append(a.mcpExecutors, executor)
		if len(executor.Tools()) > 0 {
			toolNames := make([]string, len(executor.Tools()))
			for i, t := range executor.Tools() {
				toolNames[i] = t.Name
			}
			a.addMessage(fmt.Sprintf("[System] Connected to MCP server %s with tools: %s", serverConfig.Name, strings.Join(toolNames, ", ")))
		}
	}
}

func (a *App) loadTree() {
	tree, err := projectctx.BuildTree(a.projectRoot)
	if err != nil {
		a.addMessage(fmt.Sprintf("[System] Failed to load tree: %v", err))
		return
	}
	a.projectTree = tree
	a.addMessage(fmt.Sprintf("[System] Project tree loaded"))
}

func (a *App) runShellCommand(command string) {
	a.addMessage(fmt.Sprintf("[System] Running: %s", command))
	a.setLoading(true)

	go func() {
		ctx, cancel := context.WithTimeout(a.baseCtx, 2*time.Minute)
		defer cancel()

		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		var buf strings.Builder
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		err := cmd.Run()

		a.setLoading(false)
		output := strings.TrimSpace(buf.String())

		if err != nil {
			a.addMessage(fmt.Sprintf("[System] Command failed: %v", err))
			if output != "" {
				a.addMessage(output)
			}
		} else {
			a.addMessage(fmt.Sprintf("[System] Command finished: %s", command))
			if output != "" {
				a.addMessage(output)
			}
		}
	}()
}

func (a *App) addMessage(msg string) {
	const maxMessages = 150
	if len(a.messages) >= maxMessages {
		a.messages = append(a.messages[1:], msg)
	} else {
		a.messages = append(a.messages, msg)
	}
	if a.g != nil {
		a.g.Execute(func(g *gocui.Gui) error {
			v, err := g.View("messages")
			if err != nil {
				return err
			}
			fmt.Fprintln(v, msg)
			return nil
		})
	}
}

func (a *App) updateSideContext(data string) {
	var sb strings.Builder
	sb.WriteString("Project Tree:\n")
	if a.projectTree != "" {
		lines := strings.Split(a.projectTree, "\n")
		if len(lines) > 20 {
			lines = lines[:20]
			lines = append(lines, "...")
		}
		sb.WriteString(strings.Join(lines, "\n"))
	}
	sb.WriteString("\n\nLast Tool Data:\n")
	sb.WriteString(a.lastToolData)

	a.sideContent = sb.String()
	if a.g != nil {
		a.g.Execute(func(g *gocui.Gui) error {
			v, err := g.View("side")
			if err != nil {
				return err
			}
			v.Clear()
			fmt.Fprintln(v, a.sideContent)
			return nil
		})
	}
}

func (a *App) setLoading(loading bool) {
	a.loading = loading
}

func (a *App) updateStatus(status string) {
	a.status = status
	if a.g != nil {
		a.g.Execute(func(g *gocui.Gui) error {
			v, err := g.View("status")
			if err != nil {
				return err
			}
			v.Clear()
			fmt.Fprintf(v, "Status: %s | MCP: %d servers | /confirm /cancel /add-path /paths | Ctrl+C quit", status, len(a.mcpExecutors))
			return nil
		})
	}
}

func (a *App) Stop() {
	if a.baseCancel != nil {
		a.baseCancel()
	}
	for _, executor := range a.mcpExecutors {
		_ = executor.Close()
	}
	agent.CloseProvider(a.provider)
	if a.g != nil {
		a.g.Close()
	}
}

type toolCall struct {
	name string
	args map[string]interface{}
}

func parseMCPToolCall(text string) (toolCall, bool) {
	text = strings.TrimSpace(text)

	startIdx := strings.Index(text, "{")
	if startIdx == -1 {
		return parseMCPToolCallSimple(text)
	}

	var toolData map[string]interface{}
	if err := json.Unmarshal([]byte(text[startIdx:]), &toolData); err == nil {
		if name, ok := toolData["name"].(string); ok {
			args := make(map[string]interface{})
			if toolArgs, ok := toolData["arguments"].(map[string]interface{}); ok {
				args = toolArgs
			} else if toolArgs, ok := toolData["args"].(map[string]interface{}); ok {
				args = toolArgs
			}
			return toolCall{name: name, args: args}, true
		}
	}

	return parseMCPToolCallSimple(text)
}

func parseMCPToolCallSimple(text string) (toolCall, bool) {
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)

		if strings.HasPrefix(lower, "tool_call:") || strings.HasPrefix(lower, "call:") || strings.HasPrefix(lower, "execute:") {
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) < 2 {
				continue
			}
			toolPart := strings.TrimSpace(parts[1])

			if idx := strings.Index(toolPart, "("); idx > 0 {
				name := strings.TrimSpace(toolPart[:idx])
				argsStr := strings.Trim(toolPart[idx:], "()")

				args := parseArgs(argsStr)
				return toolCall{name: name, args: args}, true
			}

			if idx := strings.Index(toolPart, " "); idx > 0 {
				name := strings.TrimSpace(toolPart[:idx])
				rest := strings.TrimSpace(toolPart[idx:])
				args := parseArgs(rest)
				return toolCall{name: name, args: args}, true
			}
		}
	}

	return toolCall{}, false
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
