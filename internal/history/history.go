package history

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type HistoryDB struct {
	db *sql.DB
}

type Entry struct {
	ID        int64
	WD        string
	Command   string
	Timestamp time.Time
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

	h := &HistoryDB{db: db}
	if err := h.initSchema(); err != nil {
		return nil, err
	}

	return h, nil
}

func getDefaultDBPath() string {
	tmpDir := os.TempDir()
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("LOCALAPPDATA"), "godex", "history.db")
	}
	return filepath.Join(tmpDir, "godex-history.db")
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
