package scheduler

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cheikh2shift/godex/internal/mcp"
	"github.com/cheikh2shift/godex/internal/providers"
	"github.com/cheikh2shift/godex/internal/toolcalls"
	_ "modernc.org/sqlite"
)

type ScheduledTask struct {
	ID            string    `json:"id"`
	Prompt        string    `json:"prompt"`
	IntervalSec   int       `json:"interval_sec"`
	RunAt         string    `json:"run_at"`
	IsRepeating   bool      `json:"is_repeating"`
	WorkingDir    string    `json:"working_dir"`
	CreatedAt     time.Time `json:"created_at"`
	LastRun       time.Time `json:"last_run"`
	RunCount      int       `json:"run_count"`
	LastError     string    `json:"last_error"`
	LastOutput    string    `json:"last_output"`
	ProviderType  string    `json:"provider_type"`
	ProviderName  string    `json:"provider_name"`
	ProviderModel string    `json:"provider_model"`
}

type Scheduler struct {
	db             *sql.DB
	tasks          map[string]*ScheduledTask
	taskMu         sync.RWMutex
	running        map[string]context.CancelFunc
	runMu          sync.RWMutex
	stopCh         chan struct{}
	wg             sync.WaitGroup
	provider       ProviderGetter
	servers        []ToolServer
	onTaskFinished func(*ScheduledTask)
	maxRounds      int
	toolTimeout    int
	wd             string
	os             string
	statusCh       chan string
}

type ProviderGetter interface {
	GetProvider(cfg any) (any, error)
}

type ToolServer interface {
	Name() string
	Tools() []mcp.Tool
	CallTool(ctx context.Context, name string, args map[string]any) (string, error)
}

func getDefaultDBPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("LOCALAPPDATA"), "godex", "scheduler.db")
	}
	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		tmpDir := os.TempDir()
		return filepath.Join(tmpDir, "godex-scheduler.db")
	}
	return filepath.Join(homeDir, ".godex", "scheduler.db")
}

func New(dbPath string) (*Scheduler, error) {
	if dbPath == "" {
		dbPath = getDefaultDBPath()
	}

	wd, _ := os.Getwd()

	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	if err := db.Ping(); err != nil {
		return nil, err
	}

	s := &Scheduler{
		db:       db,
		tasks:    make(map[string]*ScheduledTask),
		running:  make(map[string]context.CancelFunc),
		stopCh:   make(chan struct{}),
		wd:       wd,
		statusCh: make(chan string, 8),
	}

	if err := s.initSchema(); err != nil {
		db.Close()
		return nil, err
	}

	if err := s.loadTasks(); err != nil {
		db.Close()
		return nil, err
	}

	s.startLoadedTasks()

	return s, nil
}

func (s *Scheduler) startLoadedTasks() {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	for _, task := range s.tasks {
		if task.IsRepeating || (task.LastRun.IsZero() && task.IntervalSec > 0) {
			s.startTask(task)
		}
	}
}

func NewDefault() (*Scheduler, error) {
	return New("")
}

func (s *Scheduler) SetStatusChannel(ch chan string) {
	s.statusCh = ch
}

func (s *Scheduler) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS scheduled_tasks (
		id TEXT PRIMARY KEY,
		prompt TEXT NOT NULL,
		interval_sec INTEGER NOT NULL,
		run_at TEXT NOT NULL,
		is_repeating INTEGER NOT NULL,
		working_dir TEXT NOT NULL,
		created_at TEXT NOT NULL,
		last_run TEXT,
		run_count INTEGER DEFAULT 0,
		last_error TEXT,
		last_output TEXT,
		provider_type TEXT NOT NULL,
		provider_name TEXT NOT NULL,
		provider_model TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_scheduled_tasks_created_at ON scheduled_tasks(created_at);
	CREATE INDEX IF NOT EXISTS idx_scheduled_tasks_working_dir ON scheduled_tasks(working_dir);
	`
	_, err := s.db.Exec(schema)
	if err != nil {
		return err
	}

	// Migrate older DBs which predate working_dir.
	if _, err := s.db.Exec(`ALTER TABLE scheduled_tasks ADD COLUMN working_dir TEXT`); err != nil {
		// ignore "duplicate column name" errors (SQLite reports this when column already exists)
		if !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return err
		}
	}
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_scheduled_tasks_working_dir ON scheduled_tasks(working_dir)`)
	if strings.TrimSpace(s.wd) != "" {
		_, _ = s.db.Exec(`UPDATE scheduled_tasks SET working_dir = ? WHERE working_dir IS NULL OR working_dir = ''`, s.wd)
	}

	return nil
}

func (s *Scheduler) loadTasks() error {
	query := `
		SELECT id, prompt, interval_sec, run_at, is_repeating, working_dir, created_at,
		       last_run, run_count, last_error, last_output, provider_type, provider_name, provider_model
		FROM scheduled_tasks
	`
	if strings.TrimSpace(s.wd) != "" {
		query += ` WHERE working_dir = ?`
	}
	query += ` ORDER BY created_at ASC`

	var (
		rows *sql.Rows
		err  error
	)
	if strings.TrimSpace(s.wd) != "" {
		rows, err = s.db.Query(query, s.wd)
	} else {
		rows, err = s.db.Query(query)
	}
	if err != nil {
		return err
	}
	defer rows.Close()

	s.taskMu.Lock()
	defer s.taskMu.Unlock()

	for rows.Next() {
		var task ScheduledTask
		var (
			lastRun, createdAt, runAt, lastOutput sql.NullString
			workingDir                            sql.NullString
			isRepeatingInt                        sql.NullInt64
		)
		err := rows.Scan(
			&task.ID, &task.Prompt, &task.IntervalSec, &runAt, &isRepeatingInt, &workingDir,
			&createdAt, &lastRun, &task.RunCount, &task.LastError, &lastOutput,
			&task.ProviderType, &task.ProviderName, &task.ProviderModel,
		)
		if err != nil {
			return err
		}
		if isRepeatingInt.Valid && isRepeatingInt.Int64 != 0 {
			task.IsRepeating = true
		}
		if createdAt.Valid {
			task.CreatedAt, _ = time.Parse(time.RFC3339, createdAt.String)
		}
		if lastRun.Valid {
			task.LastRun, _ = time.Parse(time.RFC3339, lastRun.String)
		}
		if runAt.Valid {
			task.RunAt = runAt.String
		}
		if lastOutput.Valid {
			task.LastOutput = lastOutput.String
		}
		if workingDir.Valid {
			task.WorkingDir = workingDir.String
		} else {
			task.WorkingDir = s.wd
		}
		s.tasks[task.ID] = &task
	}
	return rows.Err()
}

func (s *Scheduler) SetProviderGetter(pg ProviderGetter) {
	s.provider = pg
}

func (s *Scheduler) SetServers(servers []any) {
	s.servers = nil
	for _, srv := range servers {
		ts, ok := srv.(ToolServer)
		if !ok {
			continue
		}
		s.servers = append(s.servers, ts)
	}
}

func (s *Scheduler) SetOnTaskFinished(cb func(*ScheduledTask)) {
	s.onTaskFinished = cb
}

func (s *Scheduler) SetConfig(maxRounds, toolTimeout int, wd, os string) {
	if maxRounds > 0 {
		s.maxRounds = maxRounds
	}
	if toolTimeout > 0 {
		s.toolTimeout = toolTimeout
	}
	if strings.TrimSpace(wd) != "" && s.wd != wd {
		// Re-scope tasks to the configured working directory.
		s.wd = wd
		s.StopAllTasks()
		s.taskMu.Lock()
		s.tasks = make(map[string]*ScheduledTask)
		s.taskMu.Unlock()
		_ = s.initSchema()
		_ = s.loadTasks()
		s.startLoadedTasks()
	} else if strings.TrimSpace(wd) != "" {
		s.wd = wd
	}
	if os != "" {
		s.os = os
	}
}

func (s *Scheduler) generateID() (string, error) {
	charset := "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	bytes := make([]byte, 4)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	result := make([]byte, 4)
	for i, b := range bytes {
		result[i] = charset[int(b)%len(charset)]
	}
	return string(result), nil
}

type SchedulerInterface interface {
	AddTask(prompt string, intervalSec int, runAt string, providerType, providerName, providerModel string) (any, error)
	AddTaskWithRepeat(prompt string, intervalSec int, runAt string, isRepeating bool, providerType, providerName, providerModel string) (any, error)
	StopTask(id string) bool
	RemoveTask(id string) bool
	ListTasks() []any
	GetTask(id string) any
}

func (s *Scheduler) AddTask(prompt string, intervalSec int, runAt string, providerType, providerName, providerModel string) (any, error) {
	isRepeating := intervalSec > 0
	return s.AddTaskWithRepeat(prompt, intervalSec, runAt, isRepeating, providerType, providerName, providerModel)
}

func (s *Scheduler) AddTaskWithRepeat(prompt string, intervalSec int, runAt string, isRepeating bool, providerType, providerName, providerModel string) (any, error) {
	if intervalSec <= 0 && runAt == "" {
		return nil, fmt.Errorf("must specify either interval or run_at time")
	}

	id, err := s.generateID()
	if err != nil {
		return nil, err
	}

	task := &ScheduledTask{
		ID:            id,
		Prompt:        prompt,
		IntervalSec:   intervalSec,
		RunAt:         runAt,
		IsRepeating:   isRepeating,
		WorkingDir:    s.wd,
		CreatedAt:     time.Now(),
		RunCount:      0,
		ProviderType:  providerType,
		ProviderName:  providerName,
		ProviderModel: providerModel,
	}

	s.taskMu.Lock()
	s.tasks[id] = task
	s.taskMu.Unlock()

	if err := s.saveTask(task); err != nil {
		return nil, err
	}

	s.startTask(task)

	return task, nil
}

func (s *Scheduler) saveTask(task *ScheduledTask) error {
	var lastRunStr string
	if task.LastRun.IsZero() {
		lastRunStr = ""
	} else {
		lastRunStr = task.LastRun.Format(time.RFC3339)
	}
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO scheduled_tasks 
		(id, prompt, interval_sec, run_at, is_repeating, working_dir, created_at, last_run, run_count, last_error, last_output, provider_type, provider_name, provider_model)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, task.ID, task.Prompt, task.IntervalSec, task.RunAt, task.IsRepeating, task.WorkingDir,
		task.CreatedAt.Format(time.RFC3339), lastRunStr,
		task.RunCount, task.LastError, task.LastOutput, task.ProviderType, task.ProviderName, task.ProviderModel)
	return err
}

func (s *Scheduler) startTask(task *ScheduledTask) {
	s.runMu.Lock()
	if _, exists := s.running[task.ID]; exists {
		s.runMu.Unlock()
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.running[task.ID] = cancel
	s.runMu.Unlock()

	s.wg.Add(1)
	go s.runLoop(ctx, task)
}

func (s *Scheduler) runLoop(ctx context.Context, task *ScheduledTask) {
	defer s.wg.Done()

	sleepDuration := func() time.Duration {
		if strings.ToLower(strings.TrimSpace(task.RunAt)) == "now" {
			return 0
		}
		if task.IntervalSec > 0 {
			return time.Duration(task.IntervalSec) * time.Second
		}
		if task.RunAt != "" {
			now := time.Now()
			target, err := time.Parse("15:04", task.RunAt)
			if err != nil {
				return 24 * time.Hour
			}
			target = time.Date(now.Year(), now.Month(), now.Day(), target.Hour(), target.Minute(), 0, 0, now.Location())
			if target.Before(now) {
				target = target.Add(24 * time.Hour)
			}
			return target.Sub(now)
		}
		return 24 * time.Hour
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(sleepDuration()):
			s.executeTask(task)
			if !task.IsRepeating {
				return
			}
		}
	}
}

func (s *Scheduler) executeTask(task *ScheduledTask) {
	taskCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if len(s.servers) > 0 {
		result, err := s.executeWithTools(taskCtx, task)
		if err != nil {
			task.LastError = fmt.Sprintf("execution error: %v", err)
			task.LastOutput = ""
		} else {
			task.LastError = ""
			task.LastOutput = result
		}
	} else if s.provider != nil {
		var cfg any
		if task.ProviderType == "unknown" || task.ProviderType == "" || task.ProviderType == "current" {
			cfg = map[string]any{
				"type":  "current",
				"name":  "current",
				"model": "current",
			}
		} else {
			cfg = map[string]any{
				"type":  task.ProviderType,
				"name":  task.ProviderName,
				"model": task.ProviderModel,
			}
		}

		prov, err := s.provider.GetProvider(cfg)
		if err != nil {
			task.LastError = fmt.Sprintf("failed to get provider: %v", err)
			task.LastRun = time.Now()
			task.RunCount++
			s.updateTask(task)
			return
		}

		provInterface, ok := prov.(interface {
			Send(ctx context.Context, prompt string) (string, error)
		})
		if !ok {
			task.LastError = "provider does not support Send method"
			task.LastRun = time.Now()
			task.RunCount++
			s.updateTask(task)
			return
		}

		result, err := provInterface.Send(taskCtx, task.Prompt)
		if err != nil {
			task.LastError = fmt.Sprintf("execution error: %v", err)
			task.LastOutput = ""
		} else {
			task.LastError = ""
			task.LastOutput = result
		}
	} else {
		task.LastError = "no provider or servers available"
	}

	task.LastRun = time.Now()
	task.RunCount++
	s.updateTask(task)

	if s.onTaskFinished != nil {
		func() {
			defer func() { _ = recover() }()
			s.onTaskFinished(task)
		}()
	}
}

func (s *Scheduler) executeWithTools(ctx context.Context, task *ScheduledTask) (string, error) {
	maxRounds := s.maxRounds
	if maxRounds <= 0 {
		maxRounds = 10
	}
	toolTimeout := s.toolTimeout
	if toolTimeout <= 0 {
		toolTimeout = 180
	}

	if s.provider == nil {
		return "", fmt.Errorf("no provider available")
	}

	cfg := map[string]any{
		"type":  "current",
		"name":  "current",
		"model": "current",
	}
	prov, err := s.provider.GetProvider(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to get provider: %v", err)
	}
	llmProvider, ok := prov.(interface {
		Send(ctx context.Context, prompt string) (string, error)
		SetThinkCallback(cb func(string))
		SupportsNativeToolCalls() bool
	})
	if !ok {
		return "", fmt.Errorf("provider does not support required interface")
	}

	toolsDesc := s.getToolsDescription()
	toolCallFormat := ""
	if !llmProvider.SupportsNativeToolCalls() {
		toolCallFormat = "\nTo call tools, respond with JSON in markdown code blocks:\n```json\n{\n  \"name\": \"tool_name\",\n  \"arguments\": {\n    \"arg1\": \"value1\"\n  }\n}\n```"
	}

	input := task.Prompt
	fullPrompt := s.buildInitialPrompt(input, toolsDesc, toolCallFormat, llmProvider.SupportsNativeToolCalls())

	prevRoundToolCalls := make(map[string]bool)
	prevNoTool := false

	for round := 0; round < maxRounds; round++ {
		var resp string
		resp, err = llmProvider.Send(ctx, fullPrompt)
		if err != nil {
			return "", fmt.Errorf("provider error: %v", err)
		}

		preResp, finalResp, hasFinal := s.splitFinalAnswer(resp)
		toolCalls := toolcalls.ExtractAllToolCalls(preResp)
		isToolCallResponse := len(toolCalls) > 0

		if !isToolCallResponse || len(toolCalls) == 0 {
			if !hasFinal {
				if !s.looksLikeToolCall(resp) && round > 0 {
					return resp, nil
				}
				if strings.TrimSpace(resp) != "" && prevNoTool {
					return resp, nil
				}
				prevNoTool = true
				continuePrompt := fmt.Sprintf("You provided the following response:\n%s\n\nHowever, I couldn't find any valid tool calls in it. Please provide your FINAL_ANSWER now based on the information you have.", resp)
				fullPrompt = s.buildContinuePrompt(continuePrompt, toolsDesc, toolCallFormat, llmProvider.SupportsNativeToolCalls(), round+1, maxRounds)
				continue
			}
			return finalResp, nil
		}

		prevNoTool = false

		var toolResults []string
		seenThisRound := make(map[string]bool)
		for _, tc := range toolCalls {
			toolName, ok := tc["name"].(string)
			if !ok {
				continue
			}
			args, ok := tc["arguments"].(map[string]any)
			if !ok {
				continue
			}
			argsJSON, _ := json.Marshal(args)
			sig := fmt.Sprintf("%s:%s", toolName, string(argsJSON))
			if seenThisRound[sig] {
				continue
			}
			seenThisRound[sig] = true

			if s.statusCh != nil {
				select {
				case s.statusCh <- fmt.Sprintf("[%s] Calling tool: %s", task.ID, toolName):
					//fmt.Printf("DEBUG: Sent status: [%s] Calling tool: %s\n", task.ID, toolName)
				default:
				}
			}

			result, err := s.callTool(ctx, toolName, args, toolTimeout)
			if err != nil {
				errStr := strings.ToLower(err.Error())
				if strings.Contains(errStr, "path_restricted") ||
					strings.Contains(errStr, "not allowed") ||
					strings.Contains(errStr, "denied") ||
					strings.Contains(errStr, "_blocked") ||
					strings.Contains(errStr, "permission") {
					return fmt.Sprintf("PERMISSION_DENIED: %v", err), nil
				}
				toolResults = append(toolResults, fmt.Sprintf("ERROR: %v", err))
			} else {
				toolResults = append(toolResults, s.truncate(result, 2500))
			}
		}

		if hasFinal {
			return finalResp, nil
		}

		currentToolCalls := make(map[string]bool)
		for _, tc := range toolCalls {
			name, ok := tc["name"].(string)
			if !ok {
				continue
			}
			args := tc["arguments"]
			argsJSON, _ := json.Marshal(args)
			currentToolCalls[fmt.Sprintf("%s:%s", name, string(argsJSON))] = true
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
			duplicateWarning = "\n\nWARNING: You are calling the same tool(s) with the same arguments as the previous round. Do NOT call them again."
		}

		roundUrgency := ""
		if round >= 10 {
			roundUrgency = "\n\nSTOP: You have reached round " + fmt.Sprintf("%d", round+1) + ". You MUST provide your FINAL_ANSWER now."
		} else if round >= 2 {
			roundUrgency = "\n\nNOTE: You are on round " + fmt.Sprintf("%d", round+1) + ". Only call more tools if absolutely necessary."
		}

		prevRoundToolCalls = currentToolCalls

		if llmProvider.SupportsNativeToolCalls() {
			fullPrompt = fmt.Sprintf("User asked: %s\n\nTool results:\n%s%s%s\n\nProvide your FINAL_ANSWER now.", input, strings.Join(toolResults, "\n---\n"), duplicateWarning, roundUrgency)
		} else {
			fullPrompt = fmt.Sprintf("You have access to these tools:%s\n\n%s\n\nUser asked: %s\n\nTool results:\n%s%s%s\n\nProvide your FINAL_ANSWER now.", toolsDesc, toolCallFormat, input, strings.Join(toolResults, "\n---\n"), duplicateWarning, roundUrgency)
		}
	}

	return "Max tool rounds reached", nil
}

func (s *Scheduler) buildInitialPrompt(input, toolsDesc string, toolCallFormat string, nativeToolCalls bool) string {
	osInfo := "linux"
	if s.os != "" {
		osInfo = s.os
	}
	arch := runtime.GOARCH
	wdInfo := ""
	if s.wd != "" {
		wdInfo = s.wd
	}

	base := fmt.Sprintf(`CRITICAL INFORMATION:
- Operating System: %s (%s)
- Current working directory: %s
Use this path when the user asks about "this folder", "current directory", or similar.

IMPORTANT: Execute tools FIRST, perform any action asked for by the user, then provide the final answer. Do NOT include any final answer, summary, or "FINAL_ANSWER:" until AFTER you have executed all necessary tools and received their results.
`, osInfo, arch, wdInfo)

	if nativeToolCalls {
		return base + "\n\n" + input
	}
	return fmt.Sprintf("%s\n\nYou have access to these tools:\n%s\n\n%s\n\nUser asked: %s", base, toolsDesc, toolCallFormat, input)
}

func (s *Scheduler) buildContinuePrompt(continuePrompt, toolsDesc string, toolCallFormat string, nativeToolCalls bool, currentRound, maxRounds int) string {
	roundInfo := ""
	if currentRound > 0 {
		roundInfo = fmt.Sprintf("\n\nNOTE: You are on round %d/%d. Only call more tools if absolutely necessary.", currentRound, maxRounds)
	}
	osInfo := "linux"
	if s.os != "" {
		osInfo = s.os
	}
	arch := runtime.GOARCH
	wdInfo := ""
	if s.wd != "" {
		wdInfo = s.wd
	}
	extraInfo := fmt.Sprintf(`CRITICAL INFORMATION:
- Operating System: %s (%s)
- Current working directory: %s
Use this path when the user asks about "this folder", "current directory", or similar.
`, osInfo, arch, wdInfo)

	systemPrompt, _ := providers.SplitSystemUserPrompt(continuePrompt)
	if systemPrompt != "" {
		systemPrompt += "\n\n"
	}
	userPromptWithContext := fmt.Sprintf("\n\n%s%sContinue from where you left off. %s\n\nProvide your FINAL_ANSWER now.%s", extraInfo, systemPrompt, continuePrompt, roundInfo)

	if nativeToolCalls {
		return userPromptWithContext
	}

	return fmt.Sprintf("%s\n\nYou have access to these tools:\n%s\n\n%s\n\n%s", extraInfo, toolsDesc, toolCallFormat, userPromptWithContext)
}

func (s *Scheduler) splitFinalAnswer(text string) (string, string, bool) {
	parts := strings.Split(text, "FINAL_ANSWER:")
	if len(parts) > 1 {
		return parts[0], strings.TrimSpace(parts[1]), true
	}
	parts = strings.Split(text, "final answer:")
	if len(parts) > 1 {
		return parts[0], strings.TrimSpace(parts[1]), true
	}
	return text, "", false
}

func (s *Scheduler) looksLikeToolCall(text string) bool {
	return strings.Contains(text, "tool") || strings.Contains(text, "call") ||
		strings.Contains(text, "function") || strings.Contains(text, "(")
}

func (s *Scheduler) callTool(ctx context.Context, name string, args map[string]any, timeoutSecs int) (string, error) {
	for _, server := range s.servers {
		for _, t := range server.Tools() {
			if t.Name == name {
				toolCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSecs)*time.Second)
				defer cancel()
				return server.CallTool(toolCtx, name, args)
			}
		}
	}

	// Compatibility: some models incorrectly use the server name (e.g. "bash") as the tool name.
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, server := range s.servers {
		if strings.ToLower(server.Name()) != lower {
			continue
		}
		for _, t := range server.Tools() {
			if t.Name == "run_command" {
				toolCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSecs)*time.Second)
				defer cancel()
				return server.CallTool(toolCtx, t.Name, args)
			}
		}
	}

	return "", fmt.Errorf("tool %s not found", name)
}

func (s *Scheduler) truncate(str string, maxLen int) string {
	if len(str) <= maxLen {
		return str
	}
	return str[:maxLen] + "..."
}

func (s *Scheduler) getToolsDescription() string {
	if len(s.servers) == 0 {
		return "No tools available."
	}

	var desc strings.Builder
	for _, server := range s.servers {
		tools := server.Tools()
		if len(tools) == 0 {
			continue
		}
		desc.WriteString(fmt.Sprintf("\n[%s]\n", server.Name()))
		for _, tool := range tools {
			desc.WriteString(fmt.Sprintf("  - %s: %s\n", tool.Name, tool.Description))
		}
	}
	return desc.String()
}

func (s *Scheduler) updateTask(task *ScheduledTask) {
	s.taskMu.Lock()
	s.tasks[task.ID] = task
	s.taskMu.Unlock()
	s.saveTask(task)
}

func (s *Scheduler) StopTask(id string) bool {
	s.runMu.Lock()
	if cancel, exists := s.running[id]; exists {
		cancel()
		delete(s.running, id)
		s.runMu.Unlock()
		return true
	}
	s.runMu.Unlock()
	return false
}

func (s *Scheduler) StopAllTasks() {
	s.runMu.Lock()
	for id, cancel := range s.running {
		cancel()
		delete(s.running, id)
	}
	s.runMu.Unlock()
}

func (s *Scheduler) RemoveTask(id string) bool {
	s.StopTask(id)
	s.taskMu.Lock()
	if _, exists := s.tasks[id]; exists {
		delete(s.tasks, id)
		s.taskMu.Unlock()
		if strings.TrimSpace(s.wd) != "" {
			_, err := s.db.Exec("DELETE FROM scheduled_tasks WHERE id = ? AND working_dir = ?", id, s.wd)
			return err == nil
		}
		_, err := s.db.Exec("DELETE FROM scheduled_tasks WHERE id = ?", id)
		return err == nil
	}
	s.taskMu.Unlock()
	return false
}

func (s *Scheduler) ListTasks() []any {
	s.taskMu.RLock()
	defer s.taskMu.RUnlock()

	tasks := make([]*ScheduledTask, 0, len(s.tasks))
	for _, task := range s.tasks {
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
	})
	result := make([]any, len(tasks))
	for i, t := range tasks {
		result[i] = t
	}
	return result
}

func (s *Scheduler) GetTask(id string) any {
	s.taskMu.RLock()
	defer s.taskMu.RUnlock()
	return s.tasks[id]
}

func (s *Scheduler) Close() {
	close(s.stopCh)
	s.StopAllTasks()
	s.wg.Wait()
	s.db.Close()
}

type providerConfig struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Model string `json:"model"`
}
