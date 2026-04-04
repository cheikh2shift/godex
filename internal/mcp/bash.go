package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

func GetWorkingDir() (string, error) {
	return os.Getwd()
}

var runCmdMu sync.Mutex

// Job represents a running background job managed by a goroutine
type Job struct {
	ID       string
	Command  string
	PID      int
	Ctx      context.Context
	Cancel   context.CancelFunc
	Done     chan struct{}
	Output   strings.Builder
	Mu       sync.Mutex
	ExitCode int
	Exited   bool
	ExitedAt time.Time
	Started  time.Time
}

// JobTracker manages all background jobs
type JobTracker struct {
	jobs   map[string]*Job
	mu     sync.RWMutex
	nextID int
}

func NewJobTracker() *JobTracker {
	return &JobTracker{
		jobs:   make(map[string]*Job),
		nextID: 1,
	}
}

func (t *JobTracker) Add(job *Job) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	job.ID = fmt.Sprintf("job%d", t.nextID)
	t.nextID++
	t.jobs[job.ID] = job
	return job.ID
}

func (t *JobTracker) Get(id string) (*Job, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	job, ok := t.jobs[id]
	return job, ok
}

func (t *JobTracker) List() []*Job {
	t.mu.RLock()
	defer t.mu.RUnlock()
	jobs := make([]*Job, 0, len(t.jobs))
	for _, job := range t.jobs {
		jobs = append(jobs, job)
	}
	return jobs
}

func (t *JobTracker) Remove(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.jobs, id)
}

func (t *JobTracker) RemoveByPID(pid int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, job := range t.jobs {
		if job.PID == pid {
			delete(t.jobs, id)
			return id
		}
	}
	return ""
}

func (t *JobTracker) Kill(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	job, ok := t.jobs[id]
	if !ok {
		return false
	}
	job.Cancel()
	if job.PID > 0 {
		_, _ = killProcessGroup(job.PID, func() {
			delete(t.jobs, id)
		})
		if proc, err := os.FindProcess(job.PID); err == nil {
			_ = proc.Kill()
		}
	}
	job.Mu.Lock()
	job.Exited = true
	job.ExitedAt = time.Now()
	job.ExitCode = 1
	job.Mu.Unlock()
	return true
}

func (t *JobTracker) KillAll() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var killed []string
	for id, job := range t.jobs {
		job.Cancel()
		if job.PID > 0 {
			_, _ = killProcessGroup(job.PID, func() {
				delete(t.jobs, id)
			})
			if proc, err := os.FindProcess(job.PID); err == nil {
				_ = proc.Kill()
			}
		}
		job.Mu.Lock()
		job.Exited = true
		job.ExitedAt = time.Now()
		job.ExitCode = 1
		job.Mu.Unlock()
		killed = append(killed, id)
	}
	t.jobs = make(map[string]*Job)
	return killed
}

func (t *JobTracker) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.jobs)
}

func (t *JobTracker) JobStatus(id string) (string, bool) {
	t.mu.RLock()
	job, ok := t.jobs[id]
	if !ok {
		t.mu.RUnlock()
		return "", false
	}
	output := job.Output.String()
	exited := job.Exited
	exitCode := job.ExitCode
	t.mu.RUnlock()

	status := "Running"
	if exited {
		status = fmt.Sprintf("Exited (code: %d)", exitCode)
	}
	return status + "\\nOutput:\\n" + output, true
}

type BashServer struct {
	allowedPaths   []string
	tools          []Tool
	jobTracker     *JobTracker
	autoConfirm    bool
	mu             sync.RWMutex
	failedMu       sync.Mutex
	failedStarts   []string
}

var (
	cdRegex          = regexp.MustCompile(`(?i)^cd\s+(\S+)`)
	interpreterRegex = regexp.MustCompile(`(?i)(^|\s)(python[0-9.]*|pypy|ruby|perl|python|bash|sh|zsh|fish|r|lua|lua5|tcl|expect)(\s|$)`)
	pathRegex        = regexp.MustCompile(`(?:^|\s)([/\\]+[^\s/\\]+(?:/[^\s/\\]*)*|[\.]{1,2}[/\\]|(?:~|\$(?:HOME|\w+))[/\\][^\s/\\]+)`)
)

func extractPathsFromCommand(command string) []string {
	var paths []string
	matches := pathRegex.FindAllStringSubmatch(command, -1)
	for _, match := range matches {
		if len(match) > 1 {
			paths = append(paths, match[1])
		}
	}
	return paths
}

func resolvePath(p string) string {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "$") {
		if idx := strings.IndexAny(p, "/\\"); idx > 0 {
			envVar := p[:idx]
			envVal := os.Getenv(envVar[1:])
			if envVal != "" {
				p = envVal + p[idx:]
			}
		}
	}
	p = os.ExpandEnv(p)
	if strings.HasPrefix(p, "~/") || p == "~" {
		home, _ := os.UserHomeDir()
		if home != "" {
			if p == "~" {
				p = home
			} else {
				p = filepath.Join(home, p[2:])
			}
		}
	}
	p = filepath.Clean(p)
	return p
}

func (s *BashServer) extractAndCheckPaths(command string) (bool, string) {
	paths := extractPathsFromCommand(command)
	if len(paths) == 0 {
		return true, ""
	}
	p := paths[0]
	resolved := resolvePath(p)
	if _, err := os.Stat(resolved); err == nil {
		if !s.isPathAllowed(resolved) {
			return false, resolved
		}
	}
	return true, ""
}

func (s *BashServer) isCommandAllowed(command string) bool {
	allowed, _ := s.checkCommandPaths(command)
	return allowed
}

func (s *BashServer) checkCommandPaths(command string) (bool, string) {
	trimmed := strings.TrimSpace(command)

	if interpreterRegex.MatchString(command) {
		return false, ""
	}

	match := cdRegex.FindStringSubmatch(trimmed)
	if len(match) > 1 {
		targetPath := match[1]
		targetPath = strings.ReplaceAll(targetPath, "~", os.Getenv("HOME"))
		targetPath = filepath.Clean(targetPath)
		if !s.isPathAllowed(targetPath) {
			return false, targetPath
		}
	}

	return s.extractAndCheckPaths(command)
}

func NewBashServer(allowedPaths []string, autoConfirm bool) *BashServer {
	allowedPaths = sanitizeAllowedPaths(allowedPaths)
	allowedPaths = withDefaultCwd(allowedPaths)

	server := &BashServer{
		allowedPaths: allowedPaths,
		autoConfirm:  autoConfirm,
		jobTracker:   NewJobTracker(),
		tools: []Tool{
			{
				Name:        "run_command",
				Description: "Run a shell command and return its output. For servers/services/long-running processes, you MUST set background: true (killable via /kill or /killbg).",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"Shell command to run"},"timeout":{"type":"number","description":"Timeout in seconds (default 180)"},"background":{"type":"boolean","description":"Run in background as a goroutine (default false)"}},"required":["command"]}`),
			},
			{
				Name:        "kill_command",
				Description: "Kill a background job by ID or PID (returned from run_command with background: true)",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"job_id":{"type":"string","description":"Job ID to kill (e.g., job1)"},"pid":{"type":"number","description":"Process ID to kill"}},"required":["job_id"]}`),
			},
			{
				Name:        "run_python",
				Description: "Run a Python script and return its output",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"code":{"type":"string","description":"Python code to run"},"timeout":{"type":"number","description":"Timeout in seconds (default 30)"}},"required":["code"]}`),
			},
			{
				Name:        "run_node",
				Description: "Run a Node.js script and return its output",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"code":{"type":"string","description":"Node.js code to run"},"timeout":{"type":"number","description":"Timeout in seconds (default 30)"}},"required":["code"]}`),
			},
		},
	}

	// Add Unix-only tools (mac, linux)
	unixTools := GetUnixTools()
	server.tools = append(server.tools, unixTools...)

	return server
}

func (s *BashServer) Name() string {
	return "bash"
}

func (s *BashServer) Tools() []Tool {
	return s.tools
}

func (s *BashServer) CallTool(ctx context.Context, name string, arguments map[string]interface{}) (string, error) {
	switch name {
	case "run_command":
		runCmdMu.Lock()
		defer runCmdMu.Unlock()
		return s.runCommand(ctx, arguments)
	case "kill_command":
		return s.killCommand(arguments)
	case "kill_all_background":
		return s.killAllBackground(arguments)
	case "run_python":
		return s.runPython(ctx, arguments)
	case "run_node":
		return s.runNode(ctx, arguments)
	case "run_bash_script":
		return s.HandleRunBashScript(ctx, arguments)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

func (s *BashServer) isPathAllowed(path string) bool {
	if path == "" {
		return true
	}
	for _, allowed := range s.allowedPaths {
		if strings.HasPrefix(path, allowed) || path == allowed {
			return true
		}
	}
	return false
}

func (s *BashServer) runCommand(ctx context.Context, args map[string]interface{}) (string, error) {
	command, ok := args["command"].(string)
	if !ok {
		return "", fmt.Errorf("command is required")
	}

	if !s.isCommandAllowed(command) {
		if interpreterRegex.MatchString(command) {
			return "", fmt.Errorf("INTERPRETER_BLOCKED: scripting interpreters (python, ruby, perl, etc.) are not allowed in run_command. Use run_python or run_node instead.")
		}
		allowed, restrictedPath := s.checkCommandPaths(command)
		if !allowed && restrictedPath != "" {
			if s.autoConfirm {
				s.allowedPaths = append(s.allowedPaths, restrictedPath)
				log.Printf("[BASH] Auto-confirmed restricted path: %s", restrictedPath)
			} else {
				return "", fmt.Errorf("PATH_RESTRICTED: path not allowed: %s", restrictedPath)
			}
		} else if !allowed {
			if s.autoConfirm {
				log.Printf("[BASH] Auto-confirmed command execution")
			} else {
				return "", fmt.Errorf("PATH_RESTRICTED: command not allowed: must be run within allowed paths: %s", strings.Join(s.allowedPaths, ", "))
			}
		}
	}

	timeout := 180 // default 3 minutes
	if t, ok := args["timeout"].(float64); ok {
		timeout = int(t)
	}
	// Cap at 5 minutes max to prevent hanging
	if timeout > 300 {
		timeout = 300
	}

	background := false
	if b, ok := args["background"].(bool); ok {
		background = b
	}

	// Detect manual background operator (& at end of command)
	trimmed := strings.TrimSpace(command)
	if strings.HasSuffix(trimmed, "&") {
		background = true
		command = strings.TrimSuffix(trimmed, "&")
	}

	// Run in background with goroutine
	if background {
		return s.runBackgroundJob(ctx, command, timeout)
	}

	// Run synchronously with timeout
	return s.runSyncCommand(ctx, command, timeout)
}

func (s *BashServer) runBackgroundJob(ctx context.Context, command string, timeout int) (string, error) {
	if ctx.Err() != nil {
		s.recordFailedStart(command, "context canceled")
		return "", fmt.Errorf("failed to start background command: context canceled")
	}

	workingDir := ""
	if dir, rest, ok := splitLeadingCd(command); ok {
		workingDir = dir
		command = rest
	}

	// Detach background jobs from the request context to avoid accidental cancellation
	jobCtx, jobCancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)

	// Create a job struct to track this background command
	job := &Job{
		Command: command,
		Ctx:     jobCtx,
		Cancel:  jobCancel,
		Done:    make(chan struct{}),
		Started: time.Now(),
	}

	// Add to tracker and get job ID
	jobID := s.jobTracker.Add(job)

	cmd := exec.CommandContext(jobCtx, "sh", "-c", command)
	if runtime.GOOS != "windows" {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	if workingDir != "" {
		cmd.Dir = workingDir
	}
	cmd.Stdin = nil

	// Set up output capture with a pipe
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.jobTracker.Remove(jobID)
		jobCancel()
		s.recordFailedStart(command, fmt.Sprintf("stdout pipe error: %v", err))
		return "", fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		s.jobTracker.Remove(jobID)
		jobCancel()
		s.recordFailedStart(command, fmt.Sprintf("stderr pipe error: %v", err))
		return "", fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		s.jobTracker.Remove(jobID)
		jobCancel()
		s.recordFailedStart(command, err.Error())
		return "", fmt.Errorf("failed to start background command: %w", err)
	}

	// Record PID
	job.PID = cmd.Process.Pid

	// Monitor the command in a goroutine
	go func() {
		defer close(job.Done)
		defer jobCancel()

		// Copy output in separate goroutines
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			buf := make([]byte, 4096)
			for {
				select {
				case <-jobCtx.Done():
					return
				default:
					n, err := stdout.Read(buf)
					if n > 0 {
						job.Mu.Lock()
						job.Output.Write(buf[:n])
						job.Mu.Unlock()
					}
					if err != nil {
						return
					}
				}
			}
		}()

		go func() {
			defer wg.Done()
			buf := make([]byte, 4096)
			for {
				select {
				case <-jobCtx.Done():
					return
				default:
					n, err := stderr.Read(buf)
					if n > 0 {
						job.Mu.Lock()
						job.Output.Write(buf[:n])
						job.Mu.Unlock()
					}
					if err != nil {
						return
					}
				}
			}
		}()

		// Wait for output copy goroutines
		wg.Wait()

		// Wait for command to finish
		err = cmd.Wait()

		// Record exit status
		job.Mu.Lock()
		job.Exited = true
		job.ExitedAt = time.Now()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
					job.ExitCode = status.ExitStatus()
				} else {
					job.ExitCode = 1
				}
			} else {
				// Check if it was killed by context
				if jobCtx.Err() == context.DeadlineExceeded {
					job.Output.WriteString("\n[Job killed due to timeout]\n")
				} else if jobCtx.Err() == context.Canceled {
					job.Output.WriteString("\n[Job killed by user]\n")
				}
				job.ExitCode = 1
			}
		} else {
			job.ExitCode = 0
		}
		job.Mu.Unlock()

		// Clean up from tracker after a delay (to allow status checks)
		go func() {
			time.Sleep(5 * time.Minute)
			s.jobTracker.Remove(jobID)
		}()
	}()

	return fmt.Sprintf("Background job started: [%s] PID=%d\nCommand: %s\nTimeout: %ds\nCheck output with /bg command",
		jobID, job.PID, command, timeout), nil
}

func splitLeadingCd(command string) (string, string, bool) {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return "", "", false
	}
	if !strings.HasPrefix(strings.ToLower(trimmed), "cd ") {
		return "", "", false
	}

	rest := strings.TrimSpace(trimmed[2:])
	sepIdx := strings.Index(rest, "&&")
	sepLen := 2
	if sepIdx == -1 {
		sepIdx = strings.Index(rest, ";")
		sepLen = 1
	}
	if sepIdx == -1 {
		return "", "", false
	}

	dirPart := strings.TrimSpace(rest[:sepIdx])
	cmdPart := strings.TrimSpace(rest[sepIdx+sepLen:])
	if dirPart == "" || cmdPart == "" {
		return "", "", false
	}
	dirPart = strings.Trim(dirPart, `"'`)
	resolved := resolvePath(dirPart)
	return resolved, cmdPart, true
}

func (s *BashServer) runSyncCommand(ctx context.Context, command string, timeout int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stdin = nil // Close stdin to prevent interactive prompts from blocking

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	// Cancel after command completes (either success or error)
	cancel()

	stdoutStr := strings.TrimSpace(stdout.String())
	stderrStr := strings.TrimSpace(stderr.String())

	if err != nil {
		// Check if it was a timeout
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Sprintf("TIMEOUT: Command exceeded %d seconds and was killed", timeout), nil
		}
		// Return both stdout and stderr so model can self-correct
		result := ""
		if stdoutStr != "" {
			result += fmt.Sprintf("STDOUT:\n%s\n", stdoutStr)
		}
		if stderrStr != "" {
			result += fmt.Sprintf("STDERR:\n%s\n", stderrStr)
		}
		if result == "" {
			result = fmt.Sprintf("Error: %v", err)
		}
		return strings.TrimSpace(result), nil
	}

	// Command succeeded - still include stderr if non-empty (warnings, etc.)
	if stderrStr != "" {
		return fmt.Sprintf("STDOUT:\n%s\nSTDERR:\n%s", stdoutStr, stderrStr), nil
	}
	return stdoutStr, nil
}

func (s *BashServer) killCommand(args map[string]interface{}) (string, error) {
	// Try job_id first (new format)
	jobID, hasJobID := args["job_id"].(string)

	// Try pid (deprecated but still supported)
	if pidVal, ok := args["pid"].(float64); ok {
		pid := int(pidVal)
		foundID := s.jobTracker.RemoveByPID(pid)
		if foundID != "" {
			jobID = foundID
			hasJobID = true
		} else {
			// PID not found in tracker, try to kill process directly
			proc, err := os.FindProcess(pid)
			if err != nil {
				return fmt.Sprintf("Process %d not found", pid), nil
			}
			err = proc.Kill()
			if err != nil {
				if errors.Is(err, syscall.ESRCH) {
					return fmt.Sprintf("Process %d already terminated", pid), nil
				}
				return "", fmt.Errorf("failed to kill process %d: %v", pid, err)
			}
			return fmt.Sprintf("Killed process %d", pid), nil
		}
	}

	if !hasJobID || jobID == "" {
		return "", fmt.Errorf("job_id or pid is required")
	}

	// Kill the job
	if !s.jobTracker.Kill(jobID) {
		return fmt.Sprintf("Job %s not found", jobID), nil
	}

	return fmt.Sprintf("Killed job %s", jobID), nil
}

func (s *BashServer) killAllBackground(args map[string]interface{}) (string, error) {
	killed := s.jobTracker.KillAll()
	if len(killed) == 0 {
		return "No background jobs running", nil
	}
	return fmt.Sprintf("Killed %d jobs: %s", len(killed), strings.Join(killed, ", ")), nil
}

func (s *BashServer) KillAllBackground() (string, error) {
	return s.killAllBackground(nil)
}

func (s *BashServer) ListBackgroundJobs() string {
	jobs := s.jobTracker.List()
	failed := s.consumeFailedStarts()
	if len(jobs) == 0 && len(failed) == 0 {
		return "No background jobs running"
	}

	var lines []string
	if len(jobs) > 0 {
		lines = append(lines, fmt.Sprintf("Background Jobs (%d running):\n", len(jobs)))
	}

	for _, job := range jobs {
		job.Mu.Lock()
		status := "Running"
		if job.Exited {
			status = fmt.Sprintf("Exited (code: %d)", job.ExitCode)
		}
		elapsed := time.Since(job.Started).Round(time.Second)
		output := job.Output.String()
		if len(output) > 200 {
			output = output[:200] + "..."
		}
		lines = append(lines, fmt.Sprintf("[%s] PID=%d Status=%s Elapsed=%s\n  Command: %s\n  Output: %s",
			job.ID, job.PID, status, elapsed, job.Command, output))
		job.Mu.Unlock()
	}

	if len(failed) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "Failed Starts (showing once):")
		for _, entry := range failed {
			lines = append(lines, "  "+entry)
		}
	}

	return strings.Join(lines, "\n")
}

func (s *BashServer) recordFailedStart(command, reason string) {
	if command == "" {
		command = "(unknown command)"
	}
	if reason == "" {
		reason = "unknown error"
	}
	entry := fmt.Sprintf("%s — %s", command, reason)
	s.failedMu.Lock()
	s.failedStarts = append(s.failedStarts, entry)
	s.failedMu.Unlock()
}

func (s *BashServer) consumeFailedStarts() []string {
	s.failedMu.Lock()
	defer s.failedMu.Unlock()
	if len(s.failedStarts) == 0 {
		return nil
	}
	out := make([]string, len(s.failedStarts))
	copy(out, s.failedStarts)
	s.failedStarts = nil
	return out
}

func (s *BashServer) GetJobOutput(jobID string) (string, bool) {
	return s.jobTracker.JobStatus(jobID)
}

func (s *BashServer) GetJob(jobID string) (*Job, bool) {
	return s.jobTracker.Get(jobID)
}

func (s *BashServer) runPython(ctx context.Context, args map[string]interface{}) (string, error) {
	code, ok := args["code"]
	if !ok {
		return "", fmt.Errorf("code is required: args=%v", args)
	}

	timeout := 30
	if t, ok := args["timeout"].(float64); ok {
		timeout = int(t)
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	secureCode := s.pythonSecurityWrapper() + "\n" + fmt.Sprintf("%v", code)

	cmd := exec.CommandContext(ctx, "python3", "-c", secureCode)
	output, err := cmd.CombinedOutput()

	if err != nil {
		return fmt.Sprintf("Error: %v\nOutput: %s", err, string(output)), nil
	}

	return string(output), nil
}

func (s *BashServer) runNode(ctx context.Context, args map[string]interface{}) (string, error) {
	code, ok := args["code"]
	if !ok {
		return "", fmt.Errorf("code is required: args=%v", args)
	}

	timeout := 30
	if t, ok := args["timeout"].(float64); ok {
		timeout = int(t)
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	wrapper := s.nodeSecurityWrapper()
	userCode := fmt.Sprintf("%v", code)
	secureCode := wrapper + "\n" + userCode

	cmd := exec.CommandContext(ctx, "node", "-e", secureCode)
	output, err := cmd.CombinedOutput()

	if err != nil {
		return fmt.Sprintf("Error: %v\nOutput: %s", err, string(output)), nil
	}

	return string(output), nil
}

func (s *BashServer) AddPath(ctx context.Context, path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	for _, p := range s.allowedPaths {
		if p == path {
			return nil
		}
	}
	s.allowedPaths = append(s.allowedPaths, path)
	return nil
}

func (s *BashServer) TempAddPath(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	for _, p := range s.allowedPaths {
		if p == path {
			return
		}
	}
	s.allowedPaths = append(s.allowedPaths, path)
}

func (s *BashServer) AddURL(ctx context.Context, url string) error {
	return fmt.Errorf("bash server does not support URLs")
}

func (s *BashServer) RemovePath(ctx context.Context, path string) error {
	path = strings.TrimSpace(path)
	for i, p := range s.allowedPaths {
		if p == path {
			s.allowedPaths = append(s.allowedPaths[:i], s.allowedPaths[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("path not found: %s", path)
}

func (s *BashServer) RemoveURL(ctx context.Context, url string) error {
	return fmt.Errorf("bash server does not support URLs")
}

func (s *BashServer) AllowedPaths() []string {
	return s.allowedPaths
}

func (s *BashServer) Close() error {
	// Kill all background jobs on close
	s.KillAllBackground()
	return nil
}

func (s *BashServer) SetAllowedPaths(paths []string) {
	s.allowedPaths = sanitizeAllowedPaths(paths)
}

func (s *BashServer) AllowedPathsUpdated() {}


// Aliases for backwards compatibility with main.go
func (s *BashServer) ListBackground() (string, error) {
	return s.ListBackgroundJobs(), nil
}

func (s *BashServer) BackgroundCount() int {
	return s.jobTracker.Count()
}

func (s *BashServer) PruneBackground() ([]string, error) {
	return nil, nil // No-op, jobs auto-prune after 5 minutes
}
