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
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type ScheduledTask struct {
	ID            string    `json:"id"`
	Prompt        string    `json:"prompt"`
	IntervalSec   int       `json:"interval_sec"`
	RunAt         string    `json:"run_at"`
	IsRepeating   bool      `json:"is_repeating"`
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
	db       *sql.DB
	tasks    map[string]*ScheduledTask
	taskMu   sync.RWMutex
	running  map[string]context.CancelFunc
	runMu    sync.RWMutex
	stopCh   chan struct{}
	wg       sync.WaitGroup
	provider ProviderGetter
}

type ProviderGetter interface {
	GetProvider(cfg interface{}) (interface{}, error)
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
		db:      db,
		tasks:   make(map[string]*ScheduledTask),
		running: make(map[string]context.CancelFunc),
		stopCh:  make(chan struct{}),
	}

	if err := s.initSchema(); err != nil {
		db.Close()
		return nil, err
	}

	if err := s.loadTasks(); err != nil {
		db.Close()
		return nil, err
	}

	return s, nil
}

func NewDefault() (*Scheduler, error) {
	return New("")
}

func (s *Scheduler) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS scheduled_tasks (
		id TEXT PRIMARY KEY,
		prompt TEXT NOT NULL,
		interval_sec INTEGER NOT NULL,
		run_at TEXT NOT NULL,
		is_repeating INTEGER NOT NULL,
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
	`
	_, err := s.db.Exec(schema)
	return err
}

func (s *Scheduler) loadTasks() error {
	rows, err := s.db.Query(`
		SELECT id, prompt, interval_sec, run_at, is_repeating, created_at, 
		       last_run, run_count, last_error, last_output, provider_type, provider_name, provider_model
		FROM scheduled_tasks
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var task ScheduledTask
		var lastRun, createdAt, runAt, lastOutput sql.NullString
		err := rows.Scan(
			&task.ID, &task.Prompt, &task.IntervalSec, &runAt, &task.IsRepeating,
			&createdAt, &lastRun, &task.RunCount, &task.LastError, &lastOutput,
			&task.ProviderType, &task.ProviderName, &task.ProviderModel,
		)
		if err != nil {
			return err
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
		s.tasks[task.ID] = &task
	}
	return rows.Err()
}

func (s *Scheduler) SetProviderGetter(pg ProviderGetter) {
	s.provider = pg
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
	AddTask(prompt string, intervalSec int, runAt string, providerType, providerName, providerModel string) (interface{}, error)
	RunNow(prompt string, providerType, providerName, providerModel string) (string, error)
	StopTask(id string) bool
	RemoveTask(id string) bool
	ListTasks() []interface{}
	GetTask(id string) interface{}
}

func (s *Scheduler) AddTask(prompt string, intervalSec int, runAt string, providerType, providerName, providerModel string) (interface{}, error) {
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
		IsRepeating:   intervalSec > 0,
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

func (s *Scheduler) RunNow(prompt string, providerType, providerName, providerModel string) (string, error) {
	if s.provider == nil {
		return "", fmt.Errorf("no provider getter set")
	}

	cfg := map[string]interface{}{
		"type":  providerType,
		"name":  providerName,
		"model": providerModel,
	}

	prov, err := s.provider.GetProvider(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to get provider: %v", err)
	}

	provInterface, ok := prov.(interface {
		Send(ctx context.Context, prompt string) (string, error)
	})
	if !ok {
		return "", fmt.Errorf("provider does not support Send method")
	}

	taskCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	result, err := provInterface.Send(taskCtx, prompt)
	if err != nil {
		return "", fmt.Errorf("execution error: %v", err)
	}

	return result, nil
}

func (s *Scheduler) saveTask(task *ScheduledTask) error {
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO scheduled_tasks 
		(id, prompt, interval_sec, run_at, is_repeating, created_at, last_run, run_count, last_error, last_output, provider_type, provider_name, provider_model)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, task.ID, task.Prompt, task.IntervalSec, task.RunAt, task.IsRepeating,
		task.CreatedAt.Format(time.RFC3339), task.LastRun.Format(time.RFC3339),
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
	if s.provider == nil {
		task.LastError = "no provider getter set"
		return
	}

	var cfg interface{}
	if task.ProviderType == "unknown" || task.ProviderType == "" {
		cfg = map[string]interface{}{
			"type":  "current",
			"name":  "current",
			"model": "current",
		}
	} else {
		cfg = map[string]interface{}{
			"type":  task.ProviderType,
			"name":  task.ProviderName,
			"model": task.ProviderModel,
		}
	}

	prov, err := s.provider.GetProvider(cfg)
	if err != nil {
		task.LastError = fmt.Sprintf("failed to get provider: %v", err)
		task.RunCount++
		s.updateTask(task)
		return
	}

	provInterface, ok := prov.(interface {
		Send(ctx context.Context, prompt string) (string, error)
	})
	if !ok {
		task.LastError = "provider does not support Send method"
		task.RunCount++
		s.updateTask(task)
		return
	}

	taskCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	result, err := provInterface.Send(taskCtx, task.Prompt)
	if err != nil {
		task.LastError = fmt.Sprintf("execution error: %v", err)
		task.LastOutput = ""
	} else {
		task.LastError = ""
		task.LastOutput = result
		fmt.Printf("[Scheduler %s] Result: %s\n", task.ID, truncate(result, 200))
	}

	task.LastRun = time.Now()
	task.RunCount++
	s.updateTask(task)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
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

func (s *Scheduler) RemoveTask(id string) bool {
	s.StopTask(id)
	s.taskMu.Lock()
	if _, exists := s.tasks[id]; exists {
		delete(s.tasks, id)
		s.taskMu.Unlock()
		_, err := s.db.Exec("DELETE FROM scheduled_tasks WHERE id = ?", id)
		return err == nil
	}
	s.taskMu.Unlock()
	return false
}

func (s *Scheduler) ListTasks() []interface{} {
	s.taskMu.RLock()
	defer s.taskMu.RUnlock()

	tasks := make([]interface{}, 0, len(s.tasks))
	for _, task := range s.tasks {
		tasks = append(tasks, task)
	}
	return tasks
}

func (s *Scheduler) GetTask(id string) interface{} {
	s.taskMu.RLock()
	defer s.taskMu.RUnlock()
	return s.tasks[id]
}

func (s *Scheduler) Close() {
	close(s.stopCh)
	s.runMu.Lock()
	for _, cancel := range s.running {
		cancel()
	}
	s.runMu.Unlock()
	s.wg.Wait()
	s.db.Close()
}

type providerConfig struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Model string `json:"model"`
}

func GetProviderFromConfig(cfg *providerConfig) (interface{}, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}

	cfgJSON, _ := json.Marshal(cfg)
	return string(cfgJSON), nil
}
