package hive

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	StatusHidePrefix = "\x00hive_hide:"
	StatusShowPrefix = "\x00hive_show:"
)

type Instance struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Model      string    `json:"model"`
	MaxTokens  int       `json:"max_tokens"`
	Port       int       `json:"port"`
	StartedAt  time.Time `json:"started_at"`
	PID        int       `json:"pid"`
	MCPServers []string  `json:"mcp_servers"`
}

type Manager struct {
	codeHash  string
	code      string
	baseDir   string
	instance  Instance
	statusCh  chan<- string
	handler   func(context.Context, string) (string, error)
	srv       *http.Server
	listener  net.Listener
	closeOnce sync.Once
	closed    chan struct{}
	results   chan HiveResult
	statsMu   sync.Mutex
	statsCh   chan HiveStats
	stats     HiveStats
}

type HiveResult struct {
	FromID   string
	FromName string
	Result   string
	Error    string
}

type HiveStats struct {
	DelegatedCount int
	LatestCommand  string
}

func (m *Manager) Status(msg string) {
	if m == nil || m.statusCh == nil {
		return
	}
	select {
	case m.statusCh <- msg:
	default:
	}
}

func NewManager(code string, baseDir string, model string, maxTokens int, mcpServers []string, statusCh chan<- string, handler func(context.Context, string) (string, error)) (*Manager, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, errors.New("hive code is required")
	}
	if baseDir == "" {
		return nil, errors.New("base dir required")
	}
	if handler == nil {
		return nil, errors.New("handler required")
	}

	id, err := newUUID()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	codeHash := hashString(code)
	inst := Instance{
		ID:         id,
		Name:       randomHumanName(),
		Model:      model,
		MaxTokens:  maxTokens,
		Port:       port,
		StartedAt:  time.Now(),
		PID:        os.Getpid(),
		MCPServers: mcpServers,
	}

	m := &Manager{
		code:     code,
		codeHash: codeHash,
		baseDir:  baseDir,
		instance: inst,
		statusCh: statusCh,
		handler:  handler,
		listener: ln,
		closed:   make(chan struct{}),
		results:  make(chan HiveResult, 16),
		statsCh:  make(chan HiveStats, 8),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", m.handleWS)
	m.srv = &http.Server{Handler: mux}

	if err := m.writeInstanceFile(); err != nil {
		_ = ln.Close()
		return nil, err
	}

	if instances, err := m.Instances(); err == nil {
		m.cleanupDeadInstances(instances)
	}

	go func() {
		_ = m.srv.Serve(ln)
		close(m.closed)
	}()

	return m, nil
}

func (m *Manager) Instance() Instance {
	return m.instance
}

func (m *Manager) Results() <-chan HiveResult {
	if m == nil {
		return nil
	}
	return m.results
}

func (m *Manager) Code() string {
	return m.code
}

func (m *Manager) Stats() <-chan HiveStats {
	if m == nil {
		return nil
	}
	return m.statsCh
}

func (m *Manager) RecordDelegate(prompt string) {
	if m == nil {
		return
	}
	m.statsMu.Lock()
	m.stats.DelegatedCount++
	m.stats.LatestCommand = strings.TrimSpace(prompt)
	stats := m.stats
	m.statsMu.Unlock()

	select {
	case m.statsCh <- stats:
	default:
	}
}

func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if m.srv != nil {
			_ = m.srv.Shutdown(ctx)
		}
		m.removeInstanceFile()
		close(m.results)
		close(m.statsCh)
	})
}

func (m *Manager) WaitClosed() {
	<-m.closed
}

func (m *Manager) Instances() ([]Instance, error) {
	instances, err := listInstances(m.instancesDir())
	if err != nil {
		return nil, err
	}
	m.cleanupDeadInstances(instances)
	return instances, nil
}

func (m *Manager) cleanupDeadInstances(instances []Instance) {
	for _, inst := range instances {
		if inst.ID == m.instance.ID {
			continue
		}
		addr := fmt.Sprintf("127.0.0.1:%d", inst.Port)
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err != nil {
			_ = os.Remove(filepath.Join(m.instancesDir(), inst.ID+".json"))
			continue
		}
		conn.Close()
	}
}

func (m *Manager) Delegate(ctx context.Context, targetID, prompt string) (string, error) {
	inst, err := findInstance(m.instancesDir(), targetID)
	if err != nil {
		return "", err
	}
	return delegateToInstance(ctx, inst, prompt, m.code)
}

func (m *Manager) DelegateAsync(ctx context.Context, targetID, prompt string) error {
	inst, err := findInstance(m.instancesDir(), targetID)
	if err != nil {
		return err
	}
	go func() {
		res, err := delegateToInstance(ctx, inst, prompt, m.code)
		h := HiveResult{
			FromID:   inst.ID,
			FromName: inst.Name,
			Result:   res,
		}
		if err != nil {
			h.Error = err.Error()
		}
		select {
		case m.results <- h:
		default:
		}
	}()
	return nil
}

func (m *Manager) handleWS(w http.ResponseWriter, r *http.Request) {
	if !validateHiveToken(r, m.code) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := acceptWebSocket(w, r)
	if err != nil {
		return
	}
	go m.handleConn(context.Background(), conn)
}

func (m *Manager) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	msg, err := readWSMessage(conn)
	if err != nil {
		return
	}
	var req wireMessage
	if err := json.Unmarshal([]byte(msg), &req); err != nil {
		return
	}
	if req.Type != "delegate" {
		return
	}
	taskCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-taskCtx.Done():
		}
	}()

	if m.statusCh != nil {
		msg := fmt.Sprintf("%s%s", StatusHidePrefix, fmt.Sprintf("Hive: working on %s", truncatePrompt(req.Prompt)))
		select {
		case m.statusCh <- msg:
		default:
		}
	}

	res, err := m.handler(taskCtx, req.Prompt)
	resp := wireMessage{Type: "result", ID: req.ID, Result: res}
	if err != nil {
		resp.Error = err.Error()
	}
	payload, _ := json.Marshal(resp)
	_ = writeWSMessage(conn, string(payload))

	if m.statusCh != nil {
		msg := fmt.Sprintf("%sHive: idle", StatusShowPrefix)
		select {
		case m.statusCh <- msg:
		default:
		}
	}
}

func (m *Manager) instancesDir() string {
	return filepath.Join(m.baseDir, "instances", m.codeHash)
}

func (m *Manager) instancePath() string {
	return filepath.Join(m.instancesDir(), m.instance.ID+".json")
}

func (m *Manager) writeInstanceFile() error {
	if err := os.MkdirAll(m.instancesDir(), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m.instance, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.instancePath(), data, 0644)
}

func (m *Manager) removeInstanceFile() {
	_ = os.Remove(m.instancePath())
}

func listInstances(dir string) ([]Instance, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Instance
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var inst Instance
		if err := json.Unmarshal(data, &inst); err != nil {
			continue
		}
		out = append(out, inst)
	}
	return out, nil
}

func findInstance(dir, id string) (Instance, error) {
	instances, err := listInstances(dir)
	if err != nil {
		return Instance{}, err
	}
	for _, inst := range instances {
		if inst.ID == id {
			return inst, nil
		}
	}
	return Instance{}, fmt.Errorf("instance not found: %s", id)
}

func delegateToInstance(ctx context.Context, inst Instance, prompt, code string) (string, error) {
	addr := fmt.Sprintf("127.0.0.1:%d", inst.Port)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	if err := clientWebSocketHandshake(conn, addr, code); err != nil {
		return "", err
	}

	req := wireMessage{Type: "delegate", ID: newID(), Prompt: prompt}
	payload, _ := json.Marshal(req)
	if err := writeWSMessage(conn, string(payload)); err != nil {
		return "", err
	}

	respRaw, err := readWSMessage(conn)
	if err != nil {
		return "", err
	}
	var resp wireMessage
	if err := json.Unmarshal([]byte(respRaw), &resp); err != nil {
		return "", err
	}
	if resp.Error != "" {
		return "", errors.New(resp.Error)
	}
	return resp.Result, nil
}

func truncatePrompt(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 80 {
		return s
	}
	return s[:77] + "..."
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

type wireMessage struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	Prompt string `json:"prompt,omitempty"`
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}
