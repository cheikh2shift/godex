package history

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type HistoryDB struct {
	db   *sql.DB
	path string
}

type Entry struct {
	ID        int64
	WD        string
	Command   string
	Timestamp time.Time
}

type Commit struct {
	ID        int64
	WD        string
	Ref       string
	Message   string
	CreatedAt time.Time
}

func New(dbPath string) (*HistoryDB, error) {
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

	h := &HistoryDB{db: db, path: dbPath}
	if err := h.initSchema(); err != nil {
		return nil, err
	}

	return h, nil
}

func getDefaultDBPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("LOCALAPPDATA"), "godex", "history.db")
	}
	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		tmpDir := os.TempDir()
		return filepath.Join(tmpDir, "godex-history.db")
	}
	return filepath.Join(homeDir, ".godex", "history.db")
}

func NewDefault() (*HistoryDB, error) {
	return New(getDefaultDBPath())
}

func (h *HistoryDB) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		wd TEXT NOT NULL,
		command TEXT NOT NULL,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_history_wd ON history(wd);
	CREATE INDEX IF NOT EXISTS idx_history_timestamp ON history(timestamp);
	CREATE TABLE IF NOT EXISTS commits (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		wd TEXT NOT NULL,
		ref TEXT NOT NULL,
		message TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_commits_wd_ref ON commits(wd, ref);
	CREATE INDEX IF NOT EXISTS idx_commits_wd_created_at ON commits(wd, created_at);
	`
	_, err := h.db.Exec(schema)
	return err
}

func (h *HistoryDB) Add(command string) error {
	return h.AddToWD("", command)
}

func (h *HistoryDB) AddToWD(wd, command string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}

	normalizedWD, err := normalizeWD(wd)
	if err != nil {
		return err
	}

	_, err = h.db.Exec(
		"INSERT INTO history (wd, command, timestamp) VALUES (?, ?, ?)",
		normalizedWD, command, time.Now(),
	)
	return err
}

func (h *HistoryDB) GetByWD(wd string, limit int) ([]string, error) {
	normalizedWD, err := normalizeWD(wd)
	if err != nil {
		return nil, err
	}

	if limit <= 0 {
		limit = 200
	}

	rows, err := h.db.Query(
		"SELECT DISTINCT command FROM history WHERE wd = ? ORDER BY id DESC LIMIT ?",
		normalizedWD, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var commands []string
	seen := make(map[string]bool)
	for rows.Next() {
		var cmd string
		if err := rows.Scan(&cmd); err != nil {
			return nil, err
		}
		if !seen[cmd] {
			seen[cmd] = true
			commands = append(commands, cmd)
		}
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return commands, nil
}

func (h *HistoryDB) Search(wd, query string, limit int) ([]string, error) {
	normalizedWD, err := normalizeWD(wd)
	if err != nil {
		return nil, err
	}

	if limit <= 0 {
		limit = 5
	}

	searchPattern := "%" + query + "%"
	rows, err := h.db.Query(
		"SELECT DISTINCT command FROM history WHERE wd = ? AND command LIKE ? ORDER BY id DESC LIMIT ?",
		normalizedWD, searchPattern, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var commands []string
	seen := make(map[string]bool)
	for rows.Next() {
		var cmd string
		if err := rows.Scan(&cmd); err != nil {
			return nil, err
		}
		if !seen[cmd] {
			seen[cmd] = true
			commands = append(commands, cmd)
		}
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return commands, nil
}

func (h *HistoryDB) GetAll(wd string, limit int) ([]string, error) {
	return h.GetByWD(wd, limit)
}

func (h *HistoryDB) Clear(wd string) error {
	normalizedWD, err := normalizeWD(wd)
	if err != nil {
		return err
	}

	_, err = h.db.Exec("DELETE FROM history WHERE wd = ?", normalizedWD)
	return err
}

func (h *HistoryDB) Close() error {
	return h.db.Close()
}

func (h *HistoryDB) BaseDir() string {
	if h == nil || h.path == "" {
		return ""
	}
	return filepath.Dir(h.path)
}

func (h *HistoryDB) CommitDir(wd string) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		return "", nil
	}
	baseDir := filepath.Join(homeDir, ".godex")
	normalizedWD, err := normalizeWD(wd)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(normalizedWD))
	return filepath.Join(baseDir, "commits", hex.EncodeToString(sum[:])), nil
}

func (h *HistoryDB) AddCommit(wd, ref, message string, createdAt time.Time) error {
	normalizedWD, err := normalizeWD(wd)
	if err != nil {
		return err
	}
	ref = strings.TrimSpace(ref)
	message = strings.TrimSpace(message)
	if ref == "" || message == "" {
		return nil
	}
	_, err = h.db.Exec(
		"INSERT OR IGNORE INTO commits (wd, ref, message, created_at) VALUES (?, ?, ?, ?)",
		normalizedWD, ref, message, createdAt,
	)
	return err
}

func (h *HistoryDB) ListCommits(wd string, limit int) ([]Commit, error) {
	normalizedWD, err := normalizeWD(wd)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 5
	}
	rows, err := h.db.Query(
		"SELECT id, wd, ref, message, created_at FROM commits WHERE wd = ? ORDER BY created_at DESC, id DESC LIMIT ?",
		normalizedWD, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var commits []Commit
	for rows.Next() {
		var c Commit
		if err := rows.Scan(&c.ID, &c.WD, &c.Ref, &c.Message, &c.CreatedAt); err != nil {
			return nil, err
		}
		commits = append(commits, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return commits, nil
}

func (h *HistoryDB) SearchCommits(wd, query string, limit int) ([]Commit, error) {
	normalizedWD, err := normalizeWD(wd)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 5
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return h.ListCommits(normalizedWD, limit)
	}
	searchPattern := "%" + query + "%"
	rows, err := h.db.Query(
		"SELECT id, wd, ref, message, created_at FROM commits WHERE wd = ? AND (ref LIKE ? OR message LIKE ?) ORDER BY created_at DESC, id DESC LIMIT ?",
		normalizedWD, searchPattern, searchPattern, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var commits []Commit
	for rows.Next() {
		var c Commit
		if err := rows.Scan(&c.ID, &c.WD, &c.Ref, &c.Message, &c.CreatedAt); err != nil {
			return nil, err
		}
		commits = append(commits, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return commits, nil
}

func (h *HistoryDB) FindCommitsByRefPrefix(wd, prefix string, limit int) ([]Commit, error) {
	normalizedWD, err := normalizeWD(wd)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 5
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return h.ListCommits(normalizedWD, limit)
	}
	searchPattern := prefix + "%"
	rows, err := h.db.Query(
		"SELECT id, wd, ref, message, created_at FROM commits WHERE wd = ? AND ref LIKE ? ORDER BY created_at DESC, id DESC LIMIT ?",
		normalizedWD, searchPattern, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var commits []Commit
	for rows.Next() {
		var c Commit
		if err := rows.Scan(&c.ID, &c.WD, &c.Ref, &c.Message, &c.CreatedAt); err != nil {
			return nil, err
		}
		commits = append(commits, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return commits, nil
}

func (h *HistoryDB) GetCommitByRef(wd, ref string) (*Commit, error) {
	normalizedWD, err := normalizeWD(wd)
	if err != nil {
		return nil, err
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, nil
	}
	row := h.db.QueryRow(
		"SELECT id, wd, ref, message, created_at FROM commits WHERE wd = ? AND ref = ? LIMIT 1",
		normalizedWD, ref,
	)
	var c Commit
	if err := row.Scan(&c.ID, &c.WD, &c.Ref, &c.Message, &c.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

func normalizeWD(wd string) (string, error) {
	wd = strings.TrimSpace(wd)
	if wd == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		wd = cwd
	}
	abs, err := filepath.Abs(wd)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}
