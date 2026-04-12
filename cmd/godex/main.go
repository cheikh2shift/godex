//go:build !linux || (linux && !noclipboard)

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"

	"github.com/cheikh2shift/godex/internal/agent"
	"github.com/cheikh2shift/godex/internal/config"
	godexcontext "github.com/cheikh2shift/godex/internal/context"
	"github.com/cheikh2shift/godex/internal/history"
	"github.com/cheikh2shift/godex/internal/hive"
	"github.com/cheikh2shift/godex/internal/mcp"
	"github.com/cheikh2shift/godex/internal/ml"
	"github.com/cheikh2shift/godex/internal/providers"
	"github.com/cheikh2shift/godex/internal/wizard"
	"github.com/cheikh2shift/godex/modelquery"
)

var clipboardAvailable = true

const (
	defaultConfigFile = "providers.yaml"
	sessionFileName   = "session.txt"
	historyFileName   = "history.db"
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

var slashCommands = []string{"/add-path ", "/remove-path ", "/paths", "/tools", "/clear-context", "/commit ", "/commit-pull ", "/commit-merge ", "/commit-search ", "/exit", "/quit", "/q", "/save", "/save-exit", "/kill ", "/killbg", "/bg", "/clear", "/help", "/model", "/model-persist"}

var (
	greenOrb         = lipgloss.NewStyle().Foreground(lipgloss.Color("82")).Render("●")
	orangeOrb        = lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Render("●")
	purpleOrb        = lipgloss.NewStyle().Foreground(lipgloss.Color("135")).Render("●")
	muted            = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	debugMode        bool
	pathPromptMu     sync.Mutex
	hivePendingStop  chan struct{}
	summarisingStart chan struct{}
)

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
	generateComp string
)

func main() {
	var (
		configPath     string
		providerName   string
		runWizard      bool
		prompt         string
		autoConfirm    bool
		modelOverride  string
		hiveCode       string
		llamaServerURL string
		showHelp       bool
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
	flag.StringVar(&modelOverride, "model", "", "override provider model")
	flag.StringVar(&hiveCode, "hive", "", "enable hive mode with a shared secret")
	flag.BoolVar(&printVersion, "version", false, "print version information")
	flag.BoolVar(&debugMode, "debug", false, "enable debug mode to log MCP requests")
	flag.BoolVar(&showHelp, "help", false, "show help")
	flag.StringVar(&generateComp, "completion", "", "generate shell completion (bash|zsh|fish)")
	flag.StringVar(&llamaServerURL, "llama-server", "", "external llama.cpp server URL (e.g. http://localhost:9090). If set, won't launch inline server")
	flag.Parse()

	if os.Getenv("GODEX_COMPLETE") != "" {
		runCompletion(strings.Fields(os.Getenv("GODEX_COMPLETE")))
		os.Exit(0)
	}

	if generateComp != "" {
		runCompletion(append([]string{generateComp}, flag.Args()...))
		os.Exit(0)
	}

	if showHelp {
		printCLIHelp()
		return
	}

	if args := flag.Args(); len(args) > 0 && args[0] == "mcp" {
		if err := handleMCPSubcommand(args[1:], configPath); err != nil {
			log.Fatalf("mcp: %v", err)
		}
		return
	}

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
			fmt.Printf("No config found at %s. Launching wizard to create one...\n", configPath)
			if err := wizard.RunWizard(configPath); err != nil {
				log.Fatalf("wizard failed: %v", err)
			}
			fmt.Println("Config created. Please run godex again.")
			return
		}
		log.Fatalf("unable to load config: %v", err)
	}

	if len(cfg.Providers) == 0 {
		fmt.Println("No providers configured. Launching wizard...")
		if err := wizard.RunWizard(configPath); err != nil {
			log.Fatalf("wizard failed: %v", err)
		}
		fmt.Println("Config updated. Please run godex again.")
		return
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
	if strings.TrimSpace(modelOverride) != "" {
		provider.Model = strings.TrimSpace(modelOverride)
	}
	if strings.TrimSpace(llamaServerURL) != "" {
		provider.LlamaServerURL = strings.TrimSpace(llamaServerURL)
	}

	if cfg.VisionEnabled == nil {
		fmt.Print("Enable vision features (image analysis)? (y/N): ")
		var response string
		fmt.Scanln(&response)
		enabled := strings.ToLower(strings.TrimSpace(response)) == "y"
		cfg.VisionEnabled = &enabled
		if err := config.Save(configPath, cfg); err != nil {
			log.Printf("failed to save vision preference: %v", err)
		}
	}

	if cfg.VisionEnabled != nil && *cfg.VisionEnabled {
		if !cfg.VisionCLIDownloaded {
			fmt.Println("Vision/OCR requires Tesseract to be installed.")
			fmt.Println("")

			var installCmd string
			switch runtime.GOOS {
			case "darwin":
				installCmd = "brew install tesseract"
			case "linux":
				installCmd = "sudo apt-get update && sudo apt-get install -y tesseract-ocr"
			case "windows":
				installCmd = "Download from: https://github.com/UB-Mannheim/tesseract/wiki"
			default:
				installCmd = "Install tesseract-ocr via your package manager"
			}

			fmt.Printf("To install, run:\n  %s\n", installCmd)
			fmt.Println("")
			fmt.Print("Install tesseract now? (y/N): ")

			var response string
			fmt.Scanln(&response)
			if strings.ToLower(strings.TrimSpace(response)) == "y" {
				cfg.VisionCLIDownloaded = true
				if err := ml.DownloadVisionServer(context.Background()); err != nil {
					log.Printf("[Vision] Setup failed: %v", err)
					cfg.VisionCLIDownloaded = false
				}
				if err := config.Save(configPath, cfg); err != nil {
					log.Printf("failed to save vision preference: %v", err)
				}
			} else {
				fmt.Println("Vision disabled. You can enable it later in config.")
				*cfg.VisionEnabled = false
				if err := config.Save(configPath, cfg); err != nil {
					log.Printf("failed to save vision preference: %v", err)
				}
			}
		}

		if cfg.VisionCLIDownloaded {
			if err := ml.EnsureVisionModel(context.Background()); err != nil {
				log.Printf("[Vision] Model setup failed: %v", err)
			} else {
				if err := ml.StartVisionServer(context.Background()); err != nil {
					log.Printf("[Vision] Server start failed: %v", err)
				}
			}
		}
	}

	llmProvider, err := agent.GetProvider(provider)
	if err != nil {
		log.Fatalf("failed to create provider: %v", err)
	}
	providers.DebugMode = debugMode
	ml.DebugMode = debugMode
	ml.PromptInstall = func(tool string) bool {
		fmt.Printf("Tool '%s' is needed. Install now? (y/N): ", tool)
		var response string
		fmt.Scanln(&response)
		return strings.ToLower(strings.TrimSpace(response)) == "y"
	}

	if provider == nil {
		log.Fatalf("no provider configured; use --wizard to create one")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer ml.StopVisionServer()
	initClipboard()

	servers, mcpLogs := initMCPServers(ctx, provider, autoConfirm)
	statusCh := make(chan string, 8)

	var providerTools []providers.Tool
	for _, server := range servers {
		for _, tool := range server.Tools() {
			var inputSchema map[string]interface{}
			if err := json.Unmarshal(tool.InputSchema, &inputSchema); err != nil {
				inputSchema = map[string]interface{}{}
			}
			providerTools = append(providerTools, providers.Tool{
				Name:        tool.Name,
				Description: tool.Description,
				InputSchema: inputSchema,
			})
		}
	}
	agent.SetProviderTools(provider, providerTools)
	if setter, ok := llmProvider.(interface{ SetStatusChannel(chan<- string) }); ok {
		setter.SetStatusChannel(statusCh)
	}

	fmt.Println()

	if prompt != "" {
		err := runSinglePrompt(ctx, provider, prompt, autoConfirm, servers)
		cleanup(servers)
		if err != nil {
			log.Fatalf("prompt failed: %v", err)
		}
		return
	}

	// Get working directory for session files
	wd, _ := os.Getwd()

	// Initialize history database (stored in tmp directory)
	var historyDB *history.HistoryDB
	historyDB, err = history.NewDefault()
	if err != nil {
		fmt.Printf("Warning: Could not initialize history database: %v\n", err)
	} else {
		fmt.Println(muted.Render("[History] Command history loaded"))
	}

	var hiveMgr *hive.Manager
	if strings.TrimSpace(hiveCode) != "" {
		baseDir := ""
		if historyDB != nil {
			baseDir = historyDB.BaseDir()
		}
		if baseDir == "" {
			if homeDir, err := os.UserHomeDir(); err == nil {
				baseDir = filepath.Join(homeDir, ".godex")
			}
		}
		maxTokens := 0
		if llmProvider != nil {
			maxTokens = llmProvider.ContextLimit()
		}
		hiveMgr, err = hive.NewManager(hiveCode, baseDir, provider.Model, maxTokens, getServerNames(servers), statusCh, func(ctx context.Context, prompt string) (string, error) {
			native := llmProvider != nil && llmProvider.SupportsNativeToolCalls()
			toolsDesc := ""
			if !native {
				toolsDesc = getToolsDescription(servers)
			}
			fullPrompt := buildFullPrompt(native, toolsDesc, "", wd, prompt, "", hiveCode)
			maxRounds := 10
			if provider.MaxToolRounds != nil && *provider.MaxToolRounds > 0 {
				maxRounds = *provider.MaxToolRounds
			}
			toolTimeout := 180
			if provider.ToolTimeout != nil && *provider.ToolTimeout > 0 {
				toolTimeout = *provider.ToolTimeout
			}
			result, err := runToolLoop(ctx, provider, servers, fullPrompt, prompt, maxRounds, toolTimeout, llmProvider, false, debugMode, true, false, func(toolName string) {
				hiveMgr.Status(fmt.Sprintf("Hive: %s", toolName))
			})
			if llmProvider != nil {
				inputTokens, outputTokens := llmProvider.TokenUsage()
				hiveMgr.SetTokenUsage(inputTokens, outputTokens)
			}
			currentWd, _ := os.Getwd()
			hiveMgr.SetWorkingDir(currentWd)
			return result, err
		})
		if err != nil {
			fmt.Printf("[Hive] Failed to start hive manager: %v\n", err)
		} else {
			servers = append(servers, mcp.NewHiveServer(hiveMgr))
			var providerTools []providers.Tool
			for _, server := range servers {
				for _, tool := range server.Tools() {
					var inputSchema map[string]interface{}
					if err := json.Unmarshal(tool.InputSchema, &inputSchema); err != nil {
						inputSchema = map[string]interface{}{}
					}
					providerTools = append(providerTools, providers.Tool{
						Name:        tool.Name,
						Description: tool.Description,
						InputSchema: inputSchema,
					})
				}
			}
			agent.SetProviderTools(provider, providerTools)
		}
	}

	printStartupBanner(provider, servers, mcpLogs, hiveMgr)

	fmt.Println()

	// Load previous session summary from ./.godex
	prevSession := loadPreviousSession(wd)
	if prevSession != "" {
		fmt.Println()
		fmt.Println(muted.Render("[Session] Loaded previous session context"))
	}

	// Load AGENTS.md if present
	agentsContext := loadAgentsFile(wd)
	if agentsContext != "" {
		fmt.Println()
		fmt.Println(muted.Render("[Agents] Loaded AGENTS.md"))
	}

	var commitContextPath string
	var commitContextRef string

	var hiveResultMu sync.Mutex
	var pendingHiveResults []*hive.HiveResult
	hiveResultReady := make(chan struct{}, 1)
	hivePendingStop = make(chan struct{}, 1)
	summarisingStart = make(chan struct{}, 1)

	if hiveMgr != nil {
		go func() {
			for res := range hiveMgr.Results() {
				hiveResultMu.Lock()
				pendingHiveResults = append(pendingHiveResults, &res)
				hiveResultMu.Unlock()
				select {
				case hiveResultReady <- struct{}{}:
				default:
				}
				select {
				case hivePendingStop <- struct{}{}:
				default:
				}
				select {
				case summarisingStart <- struct{}{}:
				default:
				}
			}
		}()
	}

	go func() {
		var ticker *time.Ticker
		var start time.Time
		active := false
		for {
			if active && ticker != nil {
				select {
				case <-summarisingStart:
					ticker.Stop()
					start = time.Now()
					ticker = time.NewTicker(1 * time.Second)
				case <-ticker.C:
					elapsed := time.Since(start)
					minutes := int(elapsed.Seconds()) / 60
					seconds := int(elapsed.Seconds()) % 60
					var timeStr string
					if minutes > 0 {
						timeStr = fmt.Sprintf("%dm%02ds", minutes, seconds)
					} else {
						timeStr = fmt.Sprintf("%ds", seconds)
					}
					fmt.Printf("\r%s - Summarising - %s", purpleOrb, timeStr)
				case <-hivePendingStop:
					ticker.Stop()
					ticker = nil
					active = false
					fmt.Print("\r               \r")
				}
			} else {
				select {
				case <-summarisingStart:
					start = time.Now()
					ticker = time.NewTicker(1 * time.Second)
					active = true
				case <-hivePendingStop:
				}
			}
		}
	}()

	// Track session for summary
	var sessionEntries []sessionEntry
	var history []string

	// Load history from database (newest first, reverse for navigation)
	if historyDB != nil {
		dbHistory, _ := historyDB.GetByWD(wd, 200)
		for i := len(dbHistory) - 1; i >= 0; i-- {
			history = append(history, dbHistory[i])
		}
	}

promptLoop:
	for {
		bgCount := getBgCount(servers)
		promptStr := "> "
		if bgCount > 0 {
			promptStr = fmt.Sprintf("[%d bg] >", bgCount)
		}

		contextLimit := llmProvider.ContextLimit()
		inputTokens, outputTokens := llmProvider.TokenUsage()

		// log.Println("[DEBUG] Context limit:", contextLimit, "Input tokens:", inputTokens, "Output tokens:", outputTokens)

		modelLabel := provider.Model
		if llmProvider != nil {
			if vs, ok := llmProvider.(interface {
				SupportsVision(context.Context) (bool, error)
			}); ok {
				visionCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
				supported, err := vs.SupportsVision(visionCtx)
				cancel()
				if err == nil && supported {
					modelLabel = modelLabel + " [V]"
				}
			}
		}

		var delegateCh <-chan hive.HiveStats
		if hiveMgr != nil {
			delegateCh = hiveMgr.Stats()
		}

		type promptResult struct {
			input string
			err   error
		}
		promptCancelCh := make(chan struct{}, 1)
		promptResultCh := make(chan promptResult, 1)

		go func() {
			input, err := readPrompt(promptStr, history, modelLabel, inputTokens+outputTokens, contextLimit, historyDB, wd, statusCh, delegateCh, promptCancelCh)
			promptResultCh <- promptResult{input: input, err: err}
		}()

		var input string
		var err error

		select {
		case result := <-promptResultCh:
			input = result.input
			err = result.err
		case <-hiveResultReady:
			hiveResultMu.Lock()
			if len(pendingHiveResults) == 0 {
				hiveResultMu.Unlock()
				continue
			}
			res := pendingHiveResults[0]
			pendingHiveResults = pendingHiveResults[1:]
			hiveResultMu.Unlock()
			select {
			case promptCancelCh <- struct{}{}:
			default:
			}
			if res != nil {
				workerLabel := res.FromName
				if strings.TrimSpace(workerLabel) == "" {
					workerLabel = res.FromID
				}
				payload := res.Result
				if res.Error != "" {
					payload = res.Error
				}
				completionMsg := fmt.Sprintf("Hive worker %s has completed and sent back the following result:\n%s", workerLabel, payload)
				sessionContext := buildSessionContext(prevSession, agentsContext, commitContextPath, commitContextRef, wd)
				native := llmProvider != nil && llmProvider.SupportsNativeToolCalls()
				toolsDesc := ""
				if !native {
					toolsDesc = getToolsDescription(servers)
				}
				var hiveInstanceID string
				if hiveMgr != nil {
					hiveInstanceID = hiveMgr.Instance().ID
				}
				fullPrompt := buildFullPrompt(native, toolsDesc, sessionContext, wd, completionMsg, "", hiveInstanceID)
				maxRounds := 10
				if provider.MaxToolRounds != nil && *provider.MaxToolRounds > 0 {
					maxRounds = *provider.MaxToolRounds
				}
				toolTimeout := 180
				if provider.ToolTimeout != nil && *provider.ToolTimeout > 0 {
					toolTimeout = *provider.ToolTimeout
				}

				_, _ = runToolLoop(ctx, provider, servers, fullPrompt, completionMsg, maxRounds, toolTimeout, llmProvider, true, false, true, true, nil)
				if debugMode {
					fmt.Printf("\n[%s] Hive worker completed:\n%s\n\n", workerLabel, renderMarkdown(payload))
				}
				hiveMgr.Status("Hive: idle")
				continue
			}
		}

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

		// Save to history database
		if historyDB != nil {
			historyDB.AddToWD(wd, input)
		}

		if input == "/exit" || input == "/quit" || input == "/q" {
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
		if input == "/model" {
			handleModelSwitch(provider, llmProvider, servers)
			continue
		}
		if input == "/model-persist" {
			handleModelPersist(provider, llmProvider, configPath, servers)
			continue
		}
		if strings.EqualFold(input, "who's at work") || strings.EqualFold(input, "whos at work") {
			if hiveMgr == nil {
				fmt.Println("[Hive] No hive instances (hive disabled)")
			} else {
				printHiveInstances(hiveMgr)
			}
			continue
		}
		if strings.HasPrefix(input, "/commit") {
			switch {
			case strings.HasPrefix(input, "/commit-pull"):
				if historyDB == nil {
					fmt.Println("[Commit] History database not available")
					continue
				}
				ref := strings.TrimSpace(strings.TrimPrefix(input, "/commit-pull"))
				if ref == "" {
					fmt.Println("[Commit] Usage: /commit-pull <commit-ref>")
					printCommitList(historyDB, wd, "")
					continue
				}
				commit, matches, err := resolveCommitRef(historyDB, wd, ref)
				if err != nil {
					fmt.Printf("[Commit] %v\n", err)
					continue
				}
				if commit == nil {
					if len(matches) > 1 {
						fmt.Printf("[Commit] Multiple matches for ref: %s\n", ref)
						printCommitListFrom(matches)
						continue
					}
					fmt.Printf("[Commit] No commit found for ref: %s\n", ref)
					continue
				}
				path, err := commitFilePath(historyDB, wd, commit.Ref)
				if err != nil {
					fmt.Printf("[Commit] Failed to resolve commit file path: %v\n", err)
					continue
				}
				entries, err := loadCommitEntries(path)
				if err != nil {
					fmt.Printf("[Commit] Failed to load commit file: %v\n", err)
					continue
				}
				applyCommitRestore(llmProvider, entries)
				fmt.Printf("[Commit] Restored %s (%s)\n", commit.Ref, formatCommitDate(commit.CreatedAt))
				continue
			case strings.HasPrefix(input, "/commit-merge"):
				if historyDB == nil {
					fmt.Println("[Commit] History database not available")
					continue
				}
				ref := strings.TrimSpace(strings.TrimPrefix(input, "/commit-merge"))
				if ref == "" {
					fmt.Println("[Commit] Usage: /commit-merge <commit-ref>")
					printCommitList(historyDB, wd, "")
					continue
				}
				commit, matches, err := resolveCommitRef(historyDB, wd, ref)
				if err != nil {
					fmt.Printf("[Commit] %v\n", err)
					continue
				}
				if commit == nil {
					if len(matches) > 1 {
						fmt.Printf("[Commit] Multiple matches for ref: %s\n", ref)
						printCommitListFrom(matches)
						continue
					}
					fmt.Printf("[Commit] No commit found for ref: %s\n", ref)
					continue
				}
				path, err := commitFilePath(historyDB, wd, commit.Ref)
				if err != nil {
					fmt.Printf("[Commit] Failed to resolve commit file path: %v\n", err)
					continue
				}
				entries, err := loadCommitEntries(path)
				if err != nil {
					fmt.Printf("[Commit] Failed to load commit file: %v\n", err)
					continue
				}
				appendCommitMessages(llmProvider, entries)
				fmt.Printf("[Commit] Merged %s (%s)\n", commit.Ref, formatCommitDate(commit.CreatedAt))
				continue
			case strings.HasPrefix(input, "/commit-search"):
				if historyDB == nil {
					fmt.Println("[Commit] History database not available")
					continue
				}
				query := strings.TrimSpace(strings.TrimPrefix(input, "/commit-search"))
				commits, err := historyDB.SearchCommits(wd, query, 5)
				if err != nil {
					fmt.Printf("[Commit] Failed to search commits: %v\n", err)
					continue
				}
				if len(commits) == 0 {
					fmt.Println("[Commit] No commits found")
					continue
				}
				rows := make([]commitRow, 0, len(commits))
				for _, c := range commits {
					msg := truncateRunes(c.Message, 129)
					date := formatCommitDate(c.CreatedAt)
					primary := fmt.Sprintf("%s  %s", date, msg)
					rows = append(rows, commitRow{primary: primary, secondary: c.Ref})
				}
				selected := commitSelectPrompt(rows)
				if selected < 0 || selected >= len(commits) {
					fmt.Println("[Commit] Cancelled")
					continue
				}
				commit := commits[selected]
				path, err := commitFilePath(historyDB, wd, commit.Ref)
				if err != nil {
					fmt.Printf("[Commit] Failed to resolve commit file path: %v\n", err)
					continue
				}
				entries, err := loadCommitEntries(path)
				if err != nil {
					fmt.Printf("[Commit] Failed to load commit file: %v\n", err)
					continue
				}
				applyCommitRestore(llmProvider, entries)
				fmt.Printf("[Commit] Restored %s (%s)\n", commit.Ref, formatCommitDate(commit.CreatedAt))
				continue
			case strings.HasPrefix(input, "/commit "):
				if historyDB == nil {
					fmt.Println("[Commit] History database not available")
					continue
				}
				message := strings.TrimSpace(strings.TrimPrefix(input, "/commit"))
				if message == "" {
					fmt.Println("[Commit] Usage: /commit <message>")
					continue
				}
				if len(sessionEntries) == 0 {
					fmt.Println("[Commit] No chat history to commit")
					continue
				}
				ref, data, err := buildCommitRef(sessionEntries)
				if err != nil {
					fmt.Printf("[Commit] Failed to build commit: %v\n", err)
					continue
				}
				path, err := commitFilePath(historyDB, wd, ref)
				if err != nil {
					fmt.Printf("[Commit] Failed to resolve commit file path: %v\n", err)
					continue
				}
				if err := writeCommitFile(path, data); err != nil {
					fmt.Printf("[Commit] Failed to write commit file: %v\n", err)
					continue
				}
				if err := historyDB.AddCommit(wd, ref, message, time.Now()); err != nil {
					fmt.Printf("[Commit] Failed to store commit: %v\n", err)
					continue
				}
				fmt.Printf("[Commit] Saved %s\n", ref)
				continue
			}
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
				fmt.Printf("Error resetting context\n")
			} else {
				fmt.Println("Context cleared.")
			}
			continue
		}

		nativeToolCalls := llmProvider.SupportsNativeToolCalls()
		var toolsSection string
		if nativeToolCalls {
			toolsSection = ""
		} else {
			toolsSection = fmt.Sprintf("\nYou have access to these tools:\n%s\n", getToolsDescription(servers))
		}

		sessionContext := buildSessionContext(prevSession, agentsContext, commitContextPath, commitContextRef, wd)

		fullPrompt := buildFullPrompt(nativeToolCalls, toolsSection, sessionContext, wd, input, "", "")

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

		// Track tool calls across rounds to detect infinite loops
		prevRoundToolCalls := make(map[string]bool)
		prevNoTool := false
		for round := 0; round < maxToolRounds; round++ {
			var streamed strings.Builder
			printedStreamed := false

			// Create a cancellable context for this round
			roundCtx, roundCancel := context.WithCancel(ctx)

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
						fmt.Printf("\r%s - Thinking - %s", orangeOrb, formatElapsed(elapsed))
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

			llmProvider.SetThinkCallback(func(think string) {
				streamed.WriteString(think)
			})
			resp, err := llmProvider.Send(roundCtx, fullPrompt)
			llmProvider.SetThinkCallback(nil)

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

			if len(toolCalls) > 0 && isToolCallResponse {
				prevNoTool = false
			}
			if !isToolCallResponse || len(toolCalls) == 0 {

				if !looksLikeToolCall(resp) {
					go playSound()
					fmt.Printf("\n\n%s\n", renderMarkdown(resp))
					break
				}
				// No valid tool calls - print thinking text and final response, then stop
				if !printedStreamed && !prevNoTool {
					thinkingText := extractThinkingText(preResp)
					if thinkingText != "" {
						fmt.Println(renderThinking(thinkingText))
					}
				}

				if prevNoTool {
					go playSound()
					fmt.Printf("\n\n%s\n", renderMarkdown(resp))
					break
				}

				prevNoTool = true
				output := resp
				if hasFinal {
					output = finalResp
				} else {
					cprompt := fmt.Sprintf("The assistant gave the following response:\n%s\n\nSince there are no tool calls to execute, please provide a final answer based on this response. Don't mention you already provided a previous answer", resp)
					fullPrompt = buildContinuePrompt(llmProvider, servers, fullPrompt, cprompt, round+1, maxToolRounds)
					continue
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
			// if round == 0 {
			// 	fmt.Print("\033[90m")
			// 	fmt.Printf("> %s\n", input)
			// 	fmt.Printf("%s\n", resp)
			// 	fmt.Print("\033[0m")
			// }
			fmt.Printf("[Executing %d tool(s)] (round %d/%d)\n", len(toolCalls), round+1, maxToolRounds)
			var toolResults []string
			var toolExecError bool

			if len(toolCalls) > 1 && llmProvider != nil {
				toolResults, toolExecError = executeToolCallsInParallel(roundCtx, servers, toolCalls, toolTimeout, llmProvider.SupportsNativeToolCalls(), true, false, input)

			} else {
				hasError := false
				for _, tc := range toolCalls {
					toolName := tc["name"].(string)
					args := tc["arguments"].(map[string]interface{})

					if toolName == "read_image" && input != "" {
						if _, ok := args["prompt"]; !ok {
							args["prompt"] = input
						}
					}

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

					if raw, ok := args["_raw"]; ok {
						log.Printf("_raw found, unwrapping: type=%T", raw)
						if rawStr, ok := raw.(string); ok {
							log.Printf("_raw is string, length=%d", len(rawStr))
							var rawMap map[string]interface{}
							if err := json.Unmarshal([]byte(rawStr), &rawMap); err == nil {
								log.Printf("_raw unwrapped successfully: %+v", rawMap)
								args = rawMap
							} else {
								log.Printf("_raw json unmarshal failed: %v", err)
							}
						}
					}

					fmt.Printf("[%s] %s\n", toolName, argsStr)

					if llmProvider.SupportsNativeToolCalls() && toolName == "run_command" {
						if cmd, ok := args["command"].(string); ok {
							cmd = fixDriveLetterPath(cmd)
							args["command"] = cmd
						}
					}

					result, err := callTool(roundCtx, servers, toolName, args, toolTimeout, false)
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
						if toolName == "read_image" || toolName == "read_text" {
							toolResults = append(toolResults, result)
						} else if toolName == "read_pdf" {
							toolResults = append(toolResults, truncate(result, 5000))
						} else {
							toolResults = append(toolResults, truncate(result, 2500))
						}
					}
				}
				toolExecError = hasError
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

			if toolExecError {
				fmt.Printf("\n[Some tools had errors, asking LLM to handle...]\n")
			}

			// Ask for final answer with all tool results (including errors)
			// Include tools description so model knows available tools for follow-up (only if not using native tool calls)
			// Check for repeated tool calls across rounds to prevent infinite loops
			currentToolCalls := make(map[string]bool)
			for _, tc := range toolCalls {
				name := tc["name"].(string)
				args := tc["arguments"]
				argsJson, _ := json.Marshal(args)
				sig := fmt.Sprintf("%s:%s", name, string(argsJson))
				currentToolCalls[sig] = true
			}

			// Check if these tool calls were also called in the previous round
			hasRepeatedCalls := false
			for sig := range currentToolCalls {
				if prevRoundToolCalls[sig] {
					hasRepeatedCalls = true
					break
				}
			}

			duplicateWarning := ""
			if hasRepeatedCalls && round > 0 {
				duplicateWarning = "\n\nWARNING: You are calling the same tool(s) with the same arguments as the previous round. These tools are not producing new results. Do NOT call them again. Provide your FINAL_ANSWER based on the information you already have."
			}

			// Update previous round tool calls for next iteration
			prevRoundToolCalls = currentToolCalls

			// Add urgency based on round number
			roundUrgency := ""
			if round >= 10 {
				roundUrgency = "\n\nSTOP: You have reached round " + fmt.Sprintf("%d", round+1) + ". You MUST provide your FINAL_ANSWER now based on the tool results you have. Do NOT call any more tools."
			} else if round >= 2 {
				roundUrgency = "\n\nNOTE: You are on round " + fmt.Sprintf("%d", round+1) + ". Only call more tools if absolutely necessary to complete the task."
			}

			if llmProvider.SupportsNativeToolCalls() {
				fullPrompt = fmt.Sprintf("User asked: %s\n\nTool results:\n%s%s%s\n\nProvide your FINAL_ANSWER now.", input, strings.Join(toolResults, "\n---\n"), duplicateWarning, roundUrgency)
			} else {
				toolsDesc := getToolsDescription(servers)
				toolCallFormat := "To call tools, respond with JSON in markdown code blocks:\n```json\n{\n  \"name\": \"tool_name\",\n  \"arguments\": {\n    \"arg1\": \"value1\"\n  }\n}\n```"
				fullPrompt = fmt.Sprintf("You have access to these tools:%s\n\n%s\n\nUser asked: %s\n\nTool results:\n%s%s%s\n\nProvide your FINAL_ANSWER now.", toolsDesc, toolCallFormat, input, strings.Join(toolResults, "\n---\n"), duplicateWarning, roundUrgency)
			}
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

		// Process all pending hive results after the prompt completes
		if hiveMgr != nil {
			hiveResultMu.Lock()
			results := pendingHiveResults
			pendingHiveResults = nil
			hiveResultMu.Unlock()
			for _, res := range results {
				if res != nil {
					select {
					case promptCancelCh <- struct{}{}:
					default:
					}
					workerLabel := res.FromName
					if strings.TrimSpace(workerLabel) == "" {
						workerLabel = res.FromID
					}

					if debugMode {
						fmt.Printf("\n[%s] Hive worker completed:\n%s\n\n", workerLabel, renderMarkdown(res.Result))
						if res.Error != "" {
							fmt.Printf("[%s] Hive worker error: %s\n\n", workerLabel, res.Error)
						}
					}
					fmt.Printf("%s %s\n\n", purpleOrb, muted.Render("Sending output to LLM..."))
					payload := res.Result
					if res.Error != "" {
						payload = res.Error
					}
					completionMsg := fmt.Sprintf("Hive worker %s has completed and sent back the following result:\n%s", workerLabel, payload)
					sessionContext := buildSessionContext(prevSession, agentsContext, commitContextPath, commitContextRef, wd)
					native := llmProvider != nil && llmProvider.SupportsNativeToolCalls()
					toolsDesc := ""
					if !native {
						toolsDesc = getToolsDescription(servers)
					}
					var hiveInstanceID string
					if hiveMgr != nil {
						hiveInstanceID = hiveMgr.Instance().ID
					}
					fullPrompt := buildFullPrompt(native, toolsDesc, sessionContext, wd, completionMsg, "", hiveInstanceID)
					maxRounds := 10
					if provider.MaxToolRounds != nil && *provider.MaxToolRounds > 0 {
						maxRounds = *provider.MaxToolRounds
					}
					toolTimeout := 180
					if provider.ToolTimeout != nil && *provider.ToolTimeout > 0 {
						toolTimeout = *provider.ToolTimeout
					}
					_, _ = runToolLoop(ctx, provider, servers, fullPrompt, completionMsg, maxRounds, toolTimeout, llmProvider, true, false, true, true, nil)
					hiveMgr.Status("Hive: idle")
				}
			}
		}
	}

	cancel()
	cleanup(servers)
	agent.CloseProvider(provider)
	if hiveMgr != nil {
		hiveMgr.Close()
	}
	if historyDB != nil {
		historyDB.Close()
	}
	fmt.Println("Goodbye!")
}

func printCLIHelp() {
	fmt.Println(`godex - AI coding agent (TUI)

Usage:
  godex [flags]
  godex mcp <add|remove> [options]

Flags:
  --config         Provider configuration YAML
  --provider       Provider name to use
  --model          Override provider model
  --hive           Enable hive mode with a shared secret
  --wizard         Launch provider configuration wizard
  --prompt         Run a single prompt non-interactively
  --auto-confirm   Auto-run suggested commands
  --version        Print version information
  --debug          Enable debug mode
  --completion     Generate shell completion (bash|zsh|fish)
  --llama-server   External llama.cpp server URL
  --help           Show this help

MCP subcommands:
  godex mcp add --provider <name> --name <server> [options]
  godex mcp remove --provider <name> --name <server>

MCP add options:
  --name           MCP server name (filesystem, bash, webscraper, or external name)
  --command        Command for external MCP server (required for external)
  --args           MCP server arg (repeatable)
  --env            MCP server env (repeatable, KEY=VALUE)
  --transport      MCP transport (e.g., stdio)
  --allowed-path   Allowed path (repeatable)
  --allowed-url    Allowed URL (repeatable)`)
}

type stringList []string

func (s *stringList) String() string {
	return strings.Join(*s, ",")
}

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func handleMCPSubcommand(args []string, configPath string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: godex mcp <add|remove> [options]")
	}

	isFilesystem := func(name string) bool {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "filesystem", "files":
			return true
		default:
			return false
		}
	}
	isBash := func(name string) bool {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "bash", "shell", "exec":
			return true
		default:
			return false
		}
	}
	isWeb := func(name string) bool {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "webscraper", "web", "browser":
			return true
		default:
			return false
		}
	}

	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("mcp add", flag.ContinueOnError)
		var providerName string
		var serverName string
		var command string
		var transport string
		var rawArgs stringList
		var rawEnv stringList
		var allowedPaths stringList
		var allowedURLs stringList
		fs.StringVar(&providerName, "provider", "", "provider name to update (defaults to configured default)")
		fs.StringVar(&serverName, "name", "", "MCP server name (e.g., filesystem, bash, webscraper, or external name)")
		fs.StringVar(&command, "command", "", "command for external MCP server")
		fs.Var(&rawArgs, "args", "MCP server arg (repeatable)")
		fs.Var(&rawEnv, "env", "MCP server env (repeatable, KEY=VALUE)")
		fs.StringVar(&transport, "transport", "", "MCP transport (e.g., stdio)")
		fs.Var(&allowedPaths, "allowed-path", "allowed path for MCP server (repeatable)")
		fs.Var(&allowedURLs, "allowed-url", "allowed URL for MCP server (repeatable)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(serverName) == "" {
			return fmt.Errorf("missing --name")
		}

		if !isFilesystem(serverName) && !isBash(serverName) && !isWeb(serverName) {
			if strings.TrimSpace(command) == "" {
				return fmt.Errorf("--command is required for external MCP servers")
			}
		}

		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		provider := cfg.DefaultOrFirst()
		if strings.TrimSpace(providerName) != "" {
			if p := cfg.ProviderByName(providerName); p != nil {
				provider = p
			} else {
				return fmt.Errorf("provider %q not found", providerName)
			}
		}
		if provider == nil {
			return fmt.Errorf("no providers configured")
		}

		for _, s := range provider.MCPServers {
			if strings.EqualFold(s.Name, serverName) {
				return fmt.Errorf("MCP server %q already exists on provider %q", serverName, provider.Name)
			}
		}

		allows := append([]string{}, allowedPaths...)
		allows = append(allows, allowedURLs...)

		provider.MCPServers = append(provider.MCPServers, config.MCPServer{
			Name:         strings.TrimSpace(serverName),
			Command:      strings.TrimSpace(command),
			Args:         []string(rawArgs),
			Env:          []string(rawEnv),
			Transport:    strings.TrimSpace(transport),
			AllowedPaths: allows,
		})

		if err := config.Save(configPath, cfg); err != nil {
			return err
		}
		fmt.Printf("Added MCP server %q to provider %q\n", serverName, provider.Name)
		return nil
	case "remove":
		fs := flag.NewFlagSet("mcp remove", flag.ContinueOnError)
		var providerName string
		var serverName string
		fs.StringVar(&providerName, "provider", "", "provider name to update (defaults to configured default)")
		fs.StringVar(&serverName, "name", "", "MCP server name to remove")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(serverName) == "" {
			return fmt.Errorf("missing --name")
		}

		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		provider := cfg.DefaultOrFirst()
		if strings.TrimSpace(providerName) != "" {
			if p := cfg.ProviderByName(providerName); p != nil {
				provider = p
			} else {
				return fmt.Errorf("provider %q not found", providerName)
			}
		}
		if provider == nil {
			return fmt.Errorf("no providers configured")
		}

		found := false
		before := len(provider.MCPServers)
		var kept []config.MCPServer
		for _, s := range provider.MCPServers {
			if strings.EqualFold(s.Name, serverName) {
				found = true
				continue
			}
			if s.Name != serverName {
				kept = append(kept, s)
			}
		}
		provider.MCPServers = kept
		if !found || len(provider.MCPServers) == before {
			return fmt.Errorf("no MCP server named %q found on provider %q", serverName, provider.Name)
		}

		if err := config.Save(configPath, cfg); err != nil {
			return err
		}
		fmt.Printf("Removed MCP server %q from provider %q\n", serverName, provider.Name)
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q (expected add or remove)", args[0])
	}
}

type mcpLog struct {
	lines []string
}

func (m *mcpLog) Printf(format string, args ...interface{}) {
	m.lines = append(m.lines, fmt.Sprintf(format, args...))
}

func (m *mcpLog) Println(args ...interface{}) {
	m.lines = append(m.lines, fmt.Sprint(args...))
}

func (m *mcpLog) String() string {
	return strings.Join(m.lines, "\n")
}

func initMCPServers(ctx context.Context, provider *config.Provider, autoConfirm bool) ([]MCPServer, string) {
	logs := &mcpLog{}

	if len(provider.MCPServers) == 0 {
		logs.Println("[MCP] No MCP servers configured")
		return nil, logs.String()
	}

	logs.Printf("[MCP] Initializing %d MCP server(s)...", len(provider.MCPServers))

	wd, _ := os.Getwd()
	logs.Printf("[MCP] Working directory: %s", wd)

	var servers []MCPServer

	for i, serverConfig := range provider.MCPServers {
		if strings.TrimSpace(serverConfig.Name) == "" {
			logs.Printf("[MCP] [!] Skipping unnamed MCP server entry")
			continue
		}
		logs.Printf("[MCP] [%d/%d] Connecting to server: %s", i+1, len(provider.MCPServers), serverConfig.Name)

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
			logs.Printf("[MCP]   Type: inline (Go)")
			logs.Printf("[MCP]   Allowed paths: %v", paths)
			server = mcp.NewFileSystemServer(paths, autoConfirm)
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
			logs.Printf("[MCP]   Type: inline (Go - command execution)")
			logs.Printf("[MCP]   Allowed paths: %v", paths)
			server = mcp.NewBashServer(paths, autoConfirm)
		} else if serverConfig.Name == "webscraper" || serverConfig.Name == "web" || serverConfig.Name == "browser" {
			paths = uniqueStrings(paths)
			logs.Printf("[MCP]   Type: inline (Go - web scraper with JS rendering)")
			logs.Printf("[MCP]   Allowed URLs: %v", paths)
			server = mcp.NewWebScraperServer(paths, autoConfirm)
		} else {
			logs.Printf("[MCP]   Type: external")
			logs.Printf("[MCP]   Command: %s %v", serverConfig.Command, serverConfig.Args)
			logs.Printf("[MCP]   Allowed paths: %v", serverConfig.AllowedPaths)

			if strings.TrimSpace(serverConfig.Command) == "" {
				logs.Printf("[MCP] [!] Skipping external MCP server %q: missing command", serverConfig.Name)
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
				logs.Printf("[MCP] [!] Failed to connect to %s: %v", serverConfig.Name, err)
				continue
			}
			server = executor
			agent.RegisterMCPExecutor(provider, executor)
		}

		servers = append(servers, server)

		toolCount := len(server.Tools())
		logs.Printf("[MCP] [+] Connected to %s with %d tool(s)", serverConfig.Name, toolCount)

		if toolCount > 0 {
			toolNames := make([]string, toolCount)
			for i, t := range server.Tools() {
				toolNames[i] = t.Name
			}
			logs.Printf("[MCP]   Tools: %s", strings.Join(toolNames, ", "))
		}
	}

	if len(servers) == 0 {
		logs.Println("[MCP] No MCP servers connected")
		return servers, logs.String()
	}

	logs.Printf("[MCP] Connected to %d MCP server(s)", len(servers))
	return servers, logs.String()
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

func buildMQProvider(provider *config.Provider) modelquery.Provider {
	mqProvider := modelquery.Provider{
		Endpoint: provider.Endpoint,
		APIKey:   provider.APIKey,
	}
	switch provider.Type {
	case "ollama":
		mqProvider.Type = modelquery.ProviderOllama
	case "llama", "llamacpp":
		mqProvider.Type = modelquery.ProviderHuggingFace
	case "gemini":
		mqProvider.Type = modelquery.ProviderGemini
	case "openrouter":
		mqProvider.Type = modelquery.ProviderOpenRouter
	default:
		fmt.Println("[Model] Unknown provider type")
	}
	return mqProvider
}

func selectNewModel(provider *config.Provider, llmProv providers.Provider) (bool, string, int) {
	mqProvider := buildMQProvider(provider)
	if mqProvider.Type == "" {
		return false, "", 0
	}

	selected, contextLen := wizard.ModelSelectPrompt(mqProvider, provider.Model)
	if selected == "" || selected == provider.Model {
		return false, "", 0
	}

	if contextLen == 0 && provider.Type == "ollama" {
		if limit, err := providers.GetOllamaContextLimit(selected); err == nil {
			contextLen = limit
			fmt.Printf("[Model] Fetched context limit from web: %d\n", contextLen)
		}
	}

	provider.Model = selected
	if contextLen > 0 {
		provider.ContextLimit = contextLen
	}

	if err := llmProv.SetModel(selected, contextLen); err != nil {
		fmt.Printf("[Model] Warning: failed to update model: %v\n", err)
	}

	return true, selected, contextLen
}

func handleModelSwitch(provider *config.Provider, llmProv providers.Provider, servers []MCPServer) {
	changed, model, contextLen := selectNewModel(provider, llmProv)
	if changed {
		fmt.Printf("[Model] Switched to %s (context: %d)\n", model, contextLen)
		setProviderToolsOnInstance(llmProv, servers)
	}
}

func handleModelPersist(provider *config.Provider, llmProv providers.Provider, configPath string, servers []MCPServer) {
	changed, model, contextLen := selectNewModel(provider, llmProv)
	if !changed {
		return
	}

	fmt.Printf("[Model] Switched to %s (context: %d)\n", model, contextLen)
	setProviderToolsOnInstance(llmProv, servers)

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Printf("[Model] Failed to load config: %v\n", err)
		return
	}

	for i := range cfg.Providers {
		if cfg.Providers[i].Name == provider.Name {
			cfg.Providers[i].Model = provider.Model
			cfg.Providers[i].ContextLimit = provider.ContextLimit
			break
		}
	}

	if err := config.Save(configPath, cfg); err != nil {
		fmt.Printf("[Model] Failed to save config: %v\n", err)
		return
	}
	fmt.Printf("[Model] Saved to %s\n", configPath)
}

func setProviderToolsOnInstance(llmProv providers.Provider, servers []MCPServer) {
	if llmProv == nil {
		return
	}
	pt, ok := llmProv.(interface{ SetTools([]providers.Tool) })
	if !ok {
		return
	}
	pt.SetTools(buildProviderTools(servers))
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
  /commit <message> - Commit current chat history
  /commit-pull <ref> - Restore committed chat history
  /commit-merge <ref> - Merge committed history into current state
  /commit-search <query> - Search commits by ref or message
  /clear-context    - Reset context counter and LLM client
  /save, /save-exit - Save session and exit
  /kill <pid>       - Kill a background process by PID
  /killbg           - Kill all background processes
  /bg               - List background processes
  /clear            - Clear the terminal
  /exit, /quit     - Exit the program
  /help            - Show this help
  /model           - Switch LLM model
  /model-persist   - Switch LLM model and save to config

Tips:
  - Paste multiline text - waits for more input automatically
  - Up/Down arrows for command history
  - Ctrl+R to search past prompts
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

func getServerNames(servers []MCPServer) []string {
	names := make([]string, 0, len(servers))
	for _, s := range servers {
		names = append(names, s.Name())
	}
	return names
}

func isOllamaPromptTooLong(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "ollama responded with 400") &&
		strings.Contains(msg, "prompt too long") &&
		strings.Contains(msg, "max context length")
}

func runSinglePrompt(ctx context.Context, provider *config.Provider, prompt string, autoConfirm bool, servers []MCPServer) error {
	tree, _ := godexcontext.BuildTree(".")
	wd, _ := os.Getwd()

	llmProvider, _ := agent.GetProvider(provider)
	nativeToolCalls := llmProvider != nil && llmProvider.SupportsNativeToolCalls()

	toolsDesc := ""
	if !nativeToolCalls {
		toolsDesc = getToolsDescription(servers)
	}
	fullPrompt := buildFullPrompt(nativeToolCalls, toolsDesc, "", wd, prompt, tree, "")

	// Get tool settings
	maxToolRounds := 10
	if provider.MaxToolRounds != nil {
		maxToolRounds = *provider.MaxToolRounds
	}
	toolTimeout := 180
	if provider.ToolTimeout != nil {
		toolTimeout = *provider.ToolTimeout
	}

	output, err := runToolLoop(ctx, provider, servers, fullPrompt, prompt, maxToolRounds, toolTimeout, llmProvider, true, false, false, false, nil)
	if err != nil {
		return err
	}
	if strings.TrimSpace(output) != "" {
		fmt.Printf("\n\n%s\n%s\n", renderSuccessBar(), renderMarkdown(output))
	}

	return nil
}

func buildFullPrompt(nativeToolCalls bool, toolsSection, sessionContext, wd, input, tree, hiveInstanceID string) string {
	base := ""
	if sessionContext != "" {
		base = sessionContext + "\n\n"
	}
	hiveInfo := ""
	if hiveInstanceID != "" {
		hiveInfo = fmt.Sprintf(`- Hive Instance ID: %s. You can't assign a task to yourself, but you can ask other hive workers to perform tasks for you.
`, hiveInstanceID)
	}
	base += fmt.Sprintf(`CRITICAL INFORMATION:
- Operating System: %s (%s)
- Current working directory: %s
%sUse this path when the user asks about "this folder", "current directory", or similar.

IMPORTANT: Execute tools FIRST, perform any action asked for by the user, then provide the final answer. Do NOT include any final answer, summary, or "FINAL_ANSWER:" until AFTER you have executed all necessary tools and received their results. If you need to run commands/tests to verify something, run them first before answering.
`, runtime.GOOS, runtime.GOARCH, wd, hiveInfo)

	if !nativeToolCalls {
		base = fmt.Sprintf(`You have access to these tools:
%s

%s

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
You can call multiple tools at once to be more efficient. If tools are independent of each other, call them in parallel for faster execution.
When planning parallel tool calls, ensure there is no conflict - tools that modify the same resource (file, directory, process, etc.) should NOT be called in parallel; run them sequentially instead.

Example:
`+"```json"+`
{
  "name": "read_file",
  "arguments": {
    "path": "/absolute/path/to/file"
  }
}
`+"```"+`

IMPORTANT: Execute tools FIRST, perform any action asked for by the user, then provide the final answer. Do NOT include any final answer, summary, or "FINAL_ANSWER:" until AFTER you have executed all necessary tools and received their results. If you need to run commands/tests to verify something, run them first before answering.
`, toolsSection, base, wd)
	}

	if strings.TrimSpace(tree) != "" {
		base += fmt.Sprintf("\nProject tree:\n%s\n", tree)
	}

	return fmt.Sprintf(`%s
User request: %s`, strings.TrimSpace(base), input)
}

func runToolLoop(ctx context.Context, provider *config.Provider, servers []MCPServer, fullPrompt, input string, maxToolRounds int, toolTimeout int, llmProvider providers.Provider, verbose, debug, autoDenyRestrictedPaths bool, nocont bool, onToolCall func(string)) (string, error) {
	prevRoundToolCalls := make(map[string]bool)
	toolsDesc := ""
	if llmProvider == nil || !llmProvider.SupportsNativeToolCalls() {
		toolsDesc = getToolsDescription(servers)
	}

	prevNoTool := false
	for round := 0; round < maxToolRounds; round++ {
		if nocont {
			if hivePendingStop != nil {
				select {
				case hivePendingStop <- struct{}{}:
				default:
				}
				select {
				case summarisingStart <- struct{}{}:
				default:
				}

			}
		}

		var resp string
		var err error
		if verbose {
			llmProvider.SetThinkCallback(func(think string) {
				fmt.Print(think)
			})
			resp, err = llmProvider.Send(ctx, fullPrompt)
			llmProvider.SetThinkCallback(nil)
		} else {
			resp, err = llmProvider.Send(ctx, fullPrompt)
		}
		select {
		case hivePendingStop <- struct{}{}:
		default:
		}
		if err != nil {
			if isOllamaPromptTooLong(err) {
				fmt.Println("\n[Ollama] Prompt exceeded the model context limit.")
				fmt.Println("Switch to a larger model to continue, or run `/commit`, then `/clear-context`, then `/commit-pull` to resume with a clean context window.")
			}
			return "", err
		}

		preResp, finalResp, hasFinal := splitFinalAnswer(resp)
		toolCalls, isToolCallResponse := shouldExecuteToolCall(preResp)
		toolCalls, missingTools := filterToolCallsByAvailability(servers, toolCalls)
		if verbose && len(missingTools) > 0 {
			fmt.Printf("\n[Ignored unknown tool(s): %s]\n", strings.Join(missingTools, ", "))
		}

		if verbose {
			fmt.Printf("[TL Round %d/%d] Got %d tool calls, isToolCallResponse=%v\n", round+1, maxToolRounds, len(toolCalls), isToolCallResponse)
		}
		if debug {
			for _, tc := range toolCalls {
				name := tc["name"].(string)
				args := tc["arguments"]
				argsJSON, _ := json.Marshal(args)
				fmt.Printf("[DEBUG] Tool call: %s(%s)\n", name, string(argsJSON))
			}
		}

		if !isToolCallResponse || len(toolCalls) == 0 {
			if !hasFinal {

				if !looksLikeToolCall(resp) {
					fmt.Printf("\n\n%s\n", renderMarkdown(resp))
					return "", nil
				}

				if strings.TrimSpace(resp) != "" && (nocont || prevNoTool) {
					if nocont {
						fmt.Printf("\n\n%s\n", renderMarkdown(resp))
					}
					return resp, nil
				}

				prevNoTool = true
				if verbose {
					thinkingText := extractThinkingText(resp)
					if thinkingText != "" {
						fmt.Println(renderThinking(thinkingText))
					}
					fmt.Printf("\n[No valid tool calls detected, asking for final answer...]\n")
				}
				continuePrompt := fmt.Sprintf("You provided the following response:\n%s\n\nHowever, I couldn't find any valid tool calls in it. Please provide your FINAL_ANSWER now based on the information you have. Do NOT call any tools, just provide the final answer.Don't mention you already provided a previous answer", resp)

				fullPrompt = buildContinuePrompt(llmProvider, servers, input, continuePrompt, round+1, maxToolRounds)

				continue
			}
			return finalResp, nil
		}

		prevNoTool = false
		if verbose {
			fmt.Printf("\n")

			fmt.Printf("[Executing %d tool(s)] (round %d/%d)\n", len(toolCalls), round+1, maxToolRounds)
		}

		var toolResults []string
		var toolExecError bool
		if len(toolCalls) > 1 && llmProvider != nil {
			toolResults, toolExecError = executeToolCallsInParallel(ctx, servers, toolCalls, toolTimeout, llmProvider.SupportsNativeToolCalls(), verbose, autoDenyRestrictedPaths, input)
		} else {
			hasError := false
			for _, tc := range toolCalls {
				toolName := tc["name"].(string)
				args := tc["arguments"].(map[string]interface{})

				if toolName == "read_image" && input != "" {
					if _, ok := args["prompt"]; !ok {
						args["prompt"] = input
					}
				}

				if onToolCall != nil {
					onToolCall(toolName)
				}

				if verbose {
					toolDesc := getToolDescription(servers, toolName)
					if toolDesc != "" {
						fmt.Print("\033[90m")
						fmt.Printf("  %s\n", toolDesc)
						fmt.Print("\033[0m")
					}
				}

				if raw, ok := args["_raw"]; ok {
					if rawStr, ok := raw.(string); ok {
						var rawMap map[string]interface{}
						if err := json.Unmarshal([]byte(rawStr), &rawMap); err == nil {
							args = rawMap
						}
					}
				}

				if llmProvider != nil && llmProvider.SupportsNativeToolCalls() && toolName == "run_command" {
					if cmd, ok := args["command"].(string); ok {
						cmd = fixDriveLetterPath(cmd)
						args["command"] = cmd
					}
				}

				result, err := callTool(ctx, servers, toolName, args, toolTimeout, autoDenyRestrictedPaths)
				if err != nil {
					errMsg := fmt.Sprintf("ERROR: %v", err)
					if verbose {
						fmt.Printf("  Error: %v\n", err)
					}
					toolResults = append(toolResults, errMsg)
					hasError = true
				} else {
					if toolName == "read_image" || toolName == "read_text" {
						toolResults = append(toolResults, result)
					} else if toolName == "read_pdf" {
						toolResults = append(toolResults, truncate(result, 5000))
					} else {
						toolResults = append(toolResults, truncate(result, 2500))
					}
				}
			}
			toolExecError = hasError
		}

		if hasFinal {
			return finalResp, nil
		}

		if toolExecError && verbose {
			fmt.Printf("\n[Some tools had errors, asking LLM to handle...]\n")
		}

		currentToolCalls := make(map[string]bool)
		for _, tc := range toolCalls {
			name := tc["name"].(string)
			args := tc["arguments"]
			argsJson, _ := json.Marshal(args)
			sig := fmt.Sprintf("%s:%s", name, string(argsJson))
			currentToolCalls[sig] = true
		}

		hasRepeatedCalls := false
		for sig := range currentToolCalls {
			if prevRoundToolCalls[sig] {
				hasRepeatedCalls = true
				break
			}
		}

		duplicateWarning := ""
		if hasRepeatedCalls && round > 0 {
			duplicateWarning = "\n\nWARNING: You are calling the same tool(s) with the same arguments as the previous round. These tools are not producing new results. Do NOT call them again. Provide your FINAL_ANSWER based on the information you already have."
		}

		prevRoundToolCalls = currentToolCalls

		roundUrgency := ""
		if round >= 10 {
			roundUrgency = "\n\nSTOP: You have reached round " + fmt.Sprintf("%d", round+1) + ". You MUST provide your FINAL_ANSWER now based on the tool results you have. Do NOT call any more tools."
		} else if round >= 2 {
			roundUrgency = "\n\nNOTE: You are on round " + fmt.Sprintf("%d", round+1) + ". Only call more tools if absolutely necessary to complete the task."
		}

		if llmProvider != nil && llmProvider.SupportsNativeToolCalls() {
			fullPrompt = fmt.Sprintf("User asked: %s\n\nTool results:\n%s%s%s\n\nProvide your FINAL_ANSWER now.", input, strings.Join(toolResults, "\n---\n"), duplicateWarning, roundUrgency)
		} else {
			toolCallFormat := "To call tools, respond with JSON in markdown code blocks:\n```json\n{\n  \"name\": \"tool_name\",\n  \"arguments\": {\n    \"arg1\": \"value1\"\n  }\n}\n```"
			fullPrompt = fmt.Sprintf("You have access to these tools:%s\n\n%s\n\nUser asked: %s\n\nTool results:\n%s%s%s\n\nProvide your FINAL_ANSWER now.", toolsDesc, toolCallFormat, input, strings.Join(toolResults, "\n---\n"), duplicateWarning, roundUrgency)
		}
	}

	return "Max tool rounds reached", nil
}

func buildContinuePrompt(llmProvider providers.Provider, servers []MCPServer, originalInput, continuePrompt string, currentRound, maxRounds int) string {
	if llmProvider != nil && llmProvider.SupportsNativeToolCalls() {
		return fmt.Sprintf("Continue from where you left off. %s\n\nProvide your FINAL_ANSWER now.", continuePrompt)
	}
	toolsDesc := getToolsDescription(servers)
	toolCallFormat := "To call tools, respond with JSON in markdown code blocks:\n```json\n{\n  \"name\": \"tool_name\",\n  \"arguments\": {\n    \"arg1\": \"value1\"\n  }\n}\n```"
	roundInfo := ""
	if currentRound > 0 {
		roundInfo = fmt.Sprintf("\n\nNOTE: You are on round %d/%d. Only call more tools if absolutely necessary.", currentRound, maxRounds)
	}
	return fmt.Sprintf("You have access to these tools:%s\n\n%s\n\nContinue from where you left off. %s\n\nProvide your FINAL_ANSWER now.%s", toolsDesc, toolCallFormat, continuePrompt, roundInfo)
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

func buildProviderTools(servers []MCPServer) []providers.Tool {
	var providerTools []providers.Tool
	for _, server := range servers {
		for _, tool := range server.Tools() {
			var inputSchema map[string]interface{}
			if err := json.Unmarshal(tool.InputSchema, &inputSchema); err != nil {
				inputSchema = map[string]interface{}{}
			}
			providerTools = append(providerTools, providers.Tool{
				Name:        tool.Name,
				Description: tool.Description,
				InputSchema: inputSchema,
			})
		}
	}
	return providerTools
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

func looksLikeToolCall(s string) bool {
	lower := strings.ToLower(s)
	if s == "" {
		return true
	}

	if !strings.Contains(lower, "toolcall") {
		return false
	}
	return strings.Contains(s, "{") && strings.Contains(s, "}")
}

func callTool(ctx context.Context, servers []MCPServer, name string, args map[string]interface{}, timeoutSecs int, autoDenyRestrictedPaths bool) (string, error) {

	//log.Println("args", args)
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
					if autoDenyRestrictedPaths {
						log.Printf("[Hive] Auto-denied restricted path access: %s (allowed paths: %v)\n", path, allowedPaths)
						return "", fmt.Errorf("PATH_RESTRICTED: '%s' is not in allowed paths. Do NOT try to access this path. Find an alternative solution that does not require this file/path. If no alternative exists, respond with FINAL_ANSWER: and explain the restriction.", path)
					}
					pathPromptMu.Lock()
					selected := showPathRestrictionPrompt(path, allowedPaths)
					pathPromptMu.Unlock()
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

func executeToolCallsInParallel(ctx context.Context, servers []MCPServer, toolCalls []map[string]interface{}, timeoutSecs int, supportsNativeToolCalls bool, verbose bool, autoDenyRestrictedPaths bool, promptContext string) ([]string, bool) {
	if len(toolCalls) == 0 {
		return nil, false
	}

	green := "\033[1;32m"
	red := "\033[1;31m"
	reset := "\033[0m"

	if verbose {
		fmt.Printf("\n")
		fmt.Printf("  %s🚀 Launching %d tools in parallel%s\n", green, len(toolCalls), reset)

		for i, tc := range toolCalls {
			toolName := tc["name"].(string)
			args := tc["arguments"].(map[string]interface{})
			var argsStr []string
			for k, v := range args {
				argsStr = append(argsStr, fmt.Sprintf("%s=%v", k, v))
			}
			fmt.Printf("  %s[%d]%s %s %s\n", green, i+1, reset, toolName, strings.Join(argsStr, ", "))
		}
		fmt.Println()
	}

	var wg sync.WaitGroup
	results := make([]string, len(toolCalls))
	resultCh := make(chan struct {
		index  int
		result string
		err    error
	}, len(toolCalls))

	startTime := time.Now()

	for i, tc := range toolCalls {
		wg.Add(1)
		go func(index int, call map[string]interface{}, autoDeny bool) {
			defer wg.Done()

			toolName := call["name"].(string)
			args := call["arguments"].(map[string]interface{})

			if toolName == "read_image" && promptContext != "" {
				if _, ok := args["prompt"]; !ok {
					args["prompt"] = promptContext
				}
			}

			if supportsNativeToolCalls && toolName == "run_command" {
				if cmd, ok := args["command"].(string); ok {
					cmd = fixDriveLetterPath(cmd)
					args["command"] = cmd
				}
			}

			select {
			case <-ctx.Done():
				resultCh <- struct {
					index  int
					result string
					err    error
				}{index, "", ctx.Err()}
				return
			default:
			}

			result, err := callTool(ctx, servers, toolName, args, timeoutSecs, autoDeny)

			select {
			case resultCh <- struct {
				index  int
				result string
				err    error
			}{index, result, err}:
			default:
			}
		}(i, tc, autoDenyRestrictedPaths)
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	hasError := false
	for result := range resultCh {
		if result.err != nil {
			if errors.Is(result.err, ErrUserAborted) {
				fmt.Println("\nUser aborted.")
				return nil, true
			}
			results[result.index] = fmt.Sprintf("ERROR: %v", result.err)
			hasError = true
		} else {
			if toolCalls[result.index]["name"].(string) == "read_image" {
				results[result.index] = result.result
			} else if toolCalls[result.index]["name"].(string) == "read_pdf" {
				results[result.index] = truncate(result.result, 5000)
			} else {
				results[result.index] = truncate(result.result, 2500)
			}
		}
	}

	elapsed := time.Since(startTime)

	if verbose {
		fmt.Printf("  %s✓ Completed in %v%s\n", green, elapsed, reset)

		for i, tc := range toolCalls {
			statusIcon := "✓"
			statusColor := green
			if strings.HasPrefix(results[i], "ERROR:") {
				statusColor = red
				statusIcon = "✗"
			}
			toolName := tc["name"].(string)
			fmt.Printf("  %s%s [%d]%s %s\n", statusIcon, statusColor, i+1, reset, toolName)
		}
		fmt.Println()
	}

	if verbose && ctx.Err() != nil {
		fmt.Printf("  ⚠ %s\n", ctx.Err())
	}

	return results, hasError
}

func isPathRestrictionError(err error) bool {
	if err == nil {
		return false
	}
	errMsg := err.Error()
	return strings.Contains(errMsg, "PATH_RESTRICTED") ||
		strings.Contains(errMsg, "path not allowed") ||
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
	case "read_file", "read_image", "write_file", "list_directory", "create_directory", "delete_file", "search_files", "get_file_info":
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
		// First, try to find markdown code blocks using proper extraction
		codeBlocks := extractCodeBlockContent(candidate)

		for _, content := range codeBlocks {
			content = strings.TrimSpace(content)
			// Try to parse the whole block as one JSON object
			var data map[string]interface{}
			if err := json.Unmarshal([]byte(content), &data); err == nil {
				if _, ok := data["name"].(string); ok {
					results = append(results, processToolData(data))
					continue
				}
			}
			// Try to parse the block as a JSON array of tool calls
			if arrayCalls := extractToolCallsFromJSONArray(content); len(arrayCalls) > 0 {
				results = append(results, arrayCalls...)
				continue
			}
			// If not a single object, try sanitizing multi-line strings
			sanitized := sanitizeMultilineJson(content)
			var sanitizedData map[string]interface{}
			if err := json.Unmarshal([]byte(sanitized), &sanitizedData); err == nil {
				if _, ok := sanitizedData["name"].(string); ok {
					results = append(results, processToolData(sanitizedData))
					continue
				}
			}
			if arrayCalls := extractToolCallsFromJSONArray(sanitized); len(arrayCalls) > 0 {
				results = append(results, arrayCalls...)
				continue
			}
			// Fall back to extracting individual JSON objects
			extracted := extractJsonObjects(sanitized)
			if len(extracted) > 0 {
				results = append(results, extracted...)
			}
		}

		// If no tools found in code blocks, search the whole text for { ... } blobs
		if len(results) == 0 {
			results = append(results, extractJsonObjects(candidate)...)
		}
		if len(results) == 0 {
			results = append(results, extractToolCallsFromJSONArray(candidate)...)
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

		// Parse native tool call format: [TOOL_CALL: name | {"arg": "value"}]
		if len(results) == 0 {
			nativeMatches := extractNativeToolCalls(candidate)
			for _, m := range nativeMatches {
				name := m[0]
				argsStr := m[1]
				var args map[string]interface{}
				if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
					args = map[string]interface{}{"_raw": argsStr}
				}
				results = append(results, map[string]interface{}{"name": name, "arguments": args})
			}
		}

		if len(results) > 0 {
			break
		}
	}

	return results
}

func extractToolCallsFromJSONArray(text string) []map[string]interface{} {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	var items []map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &items); err != nil {
		return nil
	}
	var results []map[string]interface{}
	for _, item := range items {
		if item == nil {
			continue
		}
		if _, ok := item["name"].(string); ok {
			results = append(results, processToolData(item))
			continue
		}
		if tool, ok := item["tool"].(string); ok {
			args := parseToolArgs(item)
			if len(args) == 0 {
				if params, ok := item["parameters"].(map[string]interface{}); ok {
					args = params
				}
			}
			results = append(results, map[string]interface{}{
				"name":      tool,
				"arguments": args,
			})
		}
	}
	return results
}

func extractNativeToolCalls(text string) [][2]string {
	var results [][2]string
	re := regexp.MustCompile(`\[TOOL_CALL:\s*([^\s|]+)\s*\|`)
	matches := re.FindAllStringSubmatchIndex(text, -1)
	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		name := text[m[2]:m[3]]
		jsonStart := m[1]
		for jsonStart < len(text) && (text[jsonStart] == ' ' || text[jsonStart] == '\t') {
			jsonStart++
		}
		var jsonEnd int
		inString := false
		escapeNext := false
		braceDepth := 0
		for i := jsonStart; i < len(text); i++ {
			c := text[i]
			if escapeNext {
				escapeNext = false
				continue
			}
			switch c {
			case '\\':
				escapeNext = true
			case '"':
				inString = !inString
			case '{', '[':
				if !inString {
					braceDepth++
				}
			case '}', ']':
				if !inString {
					braceDepth--
					if braceDepth == 0 {
						jsonEnd = i + 1
						break
					}
				}
			}
		}
		if jsonEnd > jsonStart {
			results = append(results, [2]string{name, text[jsonStart:jsonEnd]})
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

// fixDriveLetterPath converts commands like ":/path" to "cd /path" for Windows compatibility
// Some providers return :/home/user instead of cd /home/user
// Only replaces the first occurrence at the start of the command
func fixDriveLetterPath(cmd string) string {
	// Match :/<path> at start and convert to cd /<path>
	re := regexp.MustCompile(`(?i)^:\/(.+)`)
	match := re.FindStringSubmatch(cmd)
	if match == nil {
		return cmd
	}
	// match[1] is the path (e.g., "home/cheikh-seck/godex")
	// match[0] is the full match (e.g., ":/home/cheikh-seck/godex")
	return "cd /" + match[1] + cmd[len(match[0]):]
}

// sanitizeMultilineJson attempts to fix JSON with multi-line string values by escaping newlines within quoted strings
func sanitizeMultilineJson(text string) string {
	var result strings.Builder
	inString := false
	escapeNext := false
	lines := strings.Split(text, "\n")

	for lineIdx, line := range lines {
		for i := 0; i < len(line); i++ {
			char := rune(line[i])

			if escapeNext {
				if char == 'n' {
					result.WriteString("\\n")
				} else {
					result.WriteRune('\\')
					result.WriteRune(char)
				}
				escapeNext = false
				continue
			}

			if char == '\\' && inString {
				escapeNext = true
				continue
			}

			if char == '"' {
				if !inString {
					inString = true
				} else {
					backslashCount := 0
					j := i - 1
					for j >= 0 && line[j] == '\\' {
						backslashCount++
						j--
					}
					if backslashCount%2 == 0 {
						inString = false
					}
				}
			}

			result.WriteRune(char)
		}

		if inString && lineIdx < len(lines)-1 {
			result.WriteString("\\n")
		} else if !inString {
			result.WriteRune('\n')
		}
	}

	return result.String()
}

// extractCodeBlockContent extracts content from markdown code blocks, handling multi-line JSON
func extractCodeBlockContent(text string) []string {
	var results []string
	lines := strings.Split(text, "\n")
	inBlock := false
	blockStart := -1

	for i, line := range lines {
		if !inBlock {
			// Look for opening ```
			if strings.Contains(line, "```") {
				inBlock = true
				blockStart = i + 1
			}
		} else {
			// Look for closing ``` on its own line (with optional whitespace)
			if strings.TrimSpace(line) == "```" {
				content := strings.Join(lines[blockStart:i], "\n")
				results = append(results, content)
				inBlock = false
				blockStart = -1
			}
		}
	}
	return results
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
	args := parseToolArgs(data)

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

func parseToolArgs(data map[string]interface{}) map[string]interface{} {
	if a, ok := data["arguments"].(map[string]interface{}); ok {
		return a
	}
	if a, ok := data["args"].(map[string]interface{}); ok {
		return a
	}
	if a, ok := data["parameters"].(map[string]interface{}); ok {
		return a
	}
	if a, ok := data["params"].(map[string]interface{}); ok {
		return a
	}
	if raw, ok := data["parameters"].(string); ok && strings.TrimSpace(raw) != "" {
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
			return parsed
		}
	}
	if raw, ok := data["arguments"].(string); ok && strings.TrimSpace(raw) != "" {
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
			return parsed
		}
	}
	return make(map[string]interface{})
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

func buildSessionContext(prevSession, agentsContext, commitContextPath, commitContextRef, wd string) string {
	sessionContext := ""
	if strings.TrimSpace(prevSession) != "" {
		sessionPath := filepath.Join(wd, ".godex", sessionFileName)
		sessionContext = fmt.Sprintf("\n\nPrevious session available at: %s\nYou can read this file if you need context from previous sessions.\n", sessionPath)
	}
	if strings.TrimSpace(commitContextPath) != "" {
		sessionContext += fmt.Sprintf("\n\nCommitted history restored (%s): %s\nYou can read this file if you need restored context.\n", commitContextRef, commitContextPath)
	}
	if strings.TrimSpace(agentsContext) != "" {
		sessionContext += fmt.Sprintf("\n\nAGENTS.md instructions:\n%s\n", agentsContext)
	}
	return sessionContext
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

type commitEntry struct {
	Prompt   string `json:"prompt"`
	Response string `json:"response"`
}

type commitFile struct {
	Entries []commitEntry `json:"entries"`
}

func buildCommitRef(entries []sessionEntry) (string, []byte, error) {
	commitEntries := make([]commitEntry, 0, len(entries))
	for _, e := range entries {
		commitEntries = append(commitEntries, commitEntry{
			Prompt:   e.Prompt,
			Response: e.Response,
		})
	}
	payload, err := json.Marshal(commitFile{Entries: commitEntries})
	if err != nil {
		return "", nil, err
	}
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:]), payload, nil
}

func writeCommitFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func loadCommitEntries(path string) ([]sessionEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cf commitFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return nil, err
	}
	entries := make([]sessionEntry, 0, len(cf.Entries))
	for _, e := range cf.Entries {
		entries = append(entries, sessionEntry{
			Prompt:   e.Prompt,
			Response: e.Response,
		})
	}
	return entries, nil
}

func formatCommitDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04")
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}

func printCommitList(db *history.HistoryDB, wd, query string) {
	commits, err := db.SearchCommits(wd, query, 5)
	if err != nil {
		fmt.Printf("[Commit] Failed to search commits: %v\n", err)
		return
	}
	printCommitListFrom(commits)
}

func printCommitListFrom(commits []history.Commit) {
	if len(commits) == 0 {
		fmt.Println("[Commit] No commits found")
		return
	}
	for _, c := range commits {
		msg := truncateRunes(c.Message, 129)
		date := formatCommitDate(c.CreatedAt)
		fmt.Printf("[Commit] %s  %s  %s\n", c.Ref, date, msg)
	}
}

func applyCommitRestore(provider providers.Provider, entries []sessionEntry) {
	if provider == nil {
		fmt.Println("[Commit] Warning: provider not available for restore")
		return
	}
	if err := provider.Reset(); err != nil {
		fmt.Println("[Commit] Warning: failed to reset provider context")
	}
	messages := make([]providers.Message, 0, len(entries)*2)
	for _, e := range entries {
		if strings.TrimSpace(e.Prompt) != "" {
			messages = append(messages, providers.Message{
				Role:    "user",
				Content: e.Prompt,
			})
		}
		if strings.TrimSpace(e.Response) != "" {
			messages = append(messages, providers.Message{
				Role:    "assistant",
				Content: e.Response,
			})
		}
	}
	if err := provider.SetMessages(messages); err != nil {
		fmt.Println("[Commit] Warning: failed to set provider messages")
	}
}

func appendCommitMessages(provider providers.Provider, entries []sessionEntry) {
	if provider == nil {
		fmt.Println("[Commit] Warning: provider not available for merge")
		return
	}
	messages := make([]providers.Message, 0, len(entries)*2)
	for _, e := range entries {
		if strings.TrimSpace(e.Prompt) != "" {
			messages = append(messages, providers.Message{
				Role:    "user",
				Content: e.Prompt,
			})
		}
		if strings.TrimSpace(e.Response) != "" {
			messages = append(messages, providers.Message{
				Role:    "assistant",
				Content: e.Response,
			})
		}
	}
	if err := provider.AppendMessages(messages); err != nil {
		fmt.Println("[Commit] Warning: failed to append provider messages")
	}
}

func resolveCommitRef(db *history.HistoryDB, wd, ref string) (*history.Commit, []history.Commit, error) {
	if db == nil {
		return nil, nil, errors.New("history database not available")
	}
	commit, err := db.GetCommitByRef(wd, ref)
	if err != nil {
		return nil, nil, err
	}
	if commit != nil {
		return commit, nil, nil
	}
	matches, err := db.FindCommitsByRefPrefix(wd, ref, 5)
	if err != nil {
		return nil, nil, err
	}
	if len(matches) == 1 {
		return &matches[0], nil, nil
	}
	if len(matches) > 1 {
		return nil, matches, nil
	}
	return nil, nil, nil
}

func printHiveInstances(mgr *hive.Manager) {
	instances, err := mgr.Instances()
	if err != nil {
		fmt.Printf("[Hive] Failed to list instances: %v\n", err)
		return
	}
	if len(instances) == 0 {
		fmt.Println("[Hive] No instances found")
		return
	}
	for _, inst := range instances {
		fmt.Printf("[Hive] %s  model=%s  max_tokens=%d  port=%d\n", inst.ID, inst.Model, inst.MaxTokens, inst.Port)
	}
}

func commitFilePath(db *history.HistoryDB, wd, ref string) (string, error) {
	if db == nil {
		return "", errors.New("history database not available")
	}
	dir, err := db.CommitDir(wd)
	if err != nil {
		return "", err
	}
	if dir == "" {
		dir = "."
	}
	return filepath.Join(dir, ref), nil
}

func printStartupBanner(provider *config.Provider, servers []MCPServer, mcpLogs string, hiveMgr *hive.Manager) {
	leftContent := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86")).Render("  GoDex  ") + "\n"

	info := []string{
		fmt.Sprintf("Provider: %s (%s)", provider.Name, provider.Model),
		fmt.Sprintf("MCP Servers: %d", len(servers)),
	}
	if hiveMgr != nil {
		inst := hiveMgr.Instance()
		info = append(info, fmt.Sprintf("Hive: enabled (%s)", inst.Name))
	}
	info = append(info,
		"",
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("75")).Render("Commands:"),
		"  /commit <message> - Commit chat history",
		"  /commit-search <query> - Search commits",
		"  /clear-context - Reset context",
		"  /help - Show all commands",
		"  /model - Switch LLM model",
		"  /model-persist - Switch LLM model and save to config",
		"",
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("75")).Render("Tips:"),
		"  Ctrl+C - Cancel prompt",
		"  Up/Down - Command history",
		"  Ctrl+R - Search history",
		"  Tab - Autocomplete",
		"  Multiline: paste with newlines",
		"  Enter on empty line to submit",
	)

	for _, line := range info {
		leftContent += "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(line)
	}

	panel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("252")).
		Padding(1, 2)

	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		target := int(float64(w) * 0.8)
		if target < 40 {
			target = 40
		}
		panel = panel.Width(target)
	} else {
		panel = panel.Width(40)
	}

	fmt.Println(panel.Render(leftContent))

	if debugMode {
		logLines := strings.Split(mcpLogs, "\n")
		for _, line := range logLines {
			if strings.Contains(line, "[+] Connected") {
				fmt.Println(greenOrb + " " + muted.Render(line))
			} else {
				fmt.Println(muted.Render(line))
			}
		}
	} else {
		for _, line := range strings.Split(mcpLogs, "\n") {
			if strings.Contains(line, "[+] Connected") {
				fmt.Println(greenOrb + " " + muted.Render(line))
			}
		}
	}
}
