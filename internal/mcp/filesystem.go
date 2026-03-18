package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type FileSystemServer struct {
	allowedPaths []string
	tools        []Tool
}

func NewFileSystemServer(allowedPaths []string) *FileSystemServer {
	allowedPaths = sanitizeAllowedPaths(allowedPaths)
	allowedPaths = withDefaultCwd(allowedPaths)

	server := &FileSystemServer{
		allowedPaths: allowedPaths,
		tools: []Tool{
			{
				Name:        "read_file",
				Description: "Read contents of a file",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Absolute path to the file"}},"required":["path"]}`),
			},
			{
				Name:        "write_file",
				Description: "Write content to a file",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Absolute path to the file"},"content":{"type":"string","description":"Content to write"}},"required":["path","content"]}`),
			},
			{
				Name:        "list_directory",
				Description: "List files in a directory",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Absolute path to directory"}},"required":["path"]}`),
			},
			{
				Name:        "create_directory",
				Description: "Create a new directory",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Absolute path for new directory"}},"required":["path"]}`),
			},
			{
				Name:        "delete_file",
				Description: "Delete a file",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Absolute path to file"}},"required":["path"]}`),
			},
			{
				Name:        "search_files",
				Description: "Search for files matching a pattern",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Directory to search"},"pattern":{"type":"string","description":"File pattern (e.g., *.go)"}},"required":["path","pattern"]}`),
			},
			{
				Name:        "get_file_info",
				Description: "Get information about a file",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Absolute path to file"}},"required":["path"]}`),
			},
			{
				Name:        "read_file_line_range",
				Description: "Read a specific range of lines from a file",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Absolute path to the file"},"start":{"type":"integer","description":"Start line number (1-indexed)"},"end":{"type":"integer","description":"End line number (inclusive)"}},"required":["path","start","end"]}`),
			},
			{
				Name:        "delete_line_range",
				Description: "Delete a range of lines from a file",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Absolute path to the file"},"start":{"type":"integer","description":"Start line number (1-indexed)"},"end":{"type":"integer","description":"End line number (inclusive)"}},"required":["path","start","end"]}`),
			},
			{
				Name:        "insert_at_line",
				Description: "Insert content at a specific line number",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Absolute path to the file"},"line":{"type":"integer","description":"Line number to insert at (1-indexed, 0 = append at end)"},"content":{"type":"string","description":"Content to insert"}},"required":["path","line","content"]}`),
			},
			{
				Name:        "search_file_text",
				Description: "Search for text content within files in a directory",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Directory to search"},"query":{"type":"string","description":"Text to search for"},"case_sensitive":{"type":"boolean","description":"Enable case-sensitive matching"},"use_regex":{"type":"boolean","description":"Treat query as a regular expression"}},"required":["path","query"]}`),
			},
			{
				Name:        "search_in_file",
				Description: "Search for text within a specific file, returns matching lines with line numbers",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Absolute path to the file"},"query":{"type":"string","description":"Text to search for"},"case_sensitive":{"type":"boolean","description":"Enable case-sensitive matching"},"use_regex":{"type":"boolean","description":"Treat query as a regular expression"}},"required":["path","query"]}`),
			},
		},
	}

	return server
}

func (s *FileSystemServer) Name() string {
	return "filesystem"
}

func (s *FileSystemServer) Tools() []Tool {
	return s.tools
}

func (s *FileSystemServer) CallTool(ctx context.Context, name string, arguments map[string]interface{}) (string, error) {
	switch name {
	case "read_file":
		return s.readFile(arguments)
	case "write_file":
		return s.writeFile(arguments)
	case "list_directory":
		return s.listDirectory(arguments)
	case "create_directory":
		return s.createDirectory(arguments)
	case "delete_file":
		return s.deleteFile(arguments)
	case "search_files":
		return s.searchFiles(arguments)
	case "get_file_info":
		return s.getFileInfo(arguments)
	case "read_file_line_range":
		return s.readFileLineRange(arguments)
	case "delete_line_range":
		return s.deleteLineRange(arguments)
	case "insert_at_line":
		return s.insertAtLine(arguments)
	case "search_file_text":
		return s.searchFileText(arguments)
	case "search_in_file":
		return s.searchInFile(arguments)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

func (s *FileSystemServer) isAllowed(path string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}

	for _, allowed := range s.allowedPaths {
		absAllowed, err := filepath.Abs(allowed)
		if err != nil {
			continue
		}
		if strings.HasPrefix(absPath, absAllowed+string(filepath.Separator)) || absPath == absAllowed {
			return true
		}
	}
	return false
}

func (s *FileSystemServer) readFile(args map[string]interface{}) (string, error) {
	path, ok := args["path"].(string)
	if !ok {
		return "", fmt.Errorf("path is required")
	}

	if !s.isAllowed(path) {
		return "", fmt.Errorf("path not allowed: %s", path)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	return string(content), nil
}

func (s *FileSystemServer) writeFile(args map[string]interface{}) (string, error) {
	path, ok := args["path"].(string)
	if !ok {
		return "", fmt.Errorf("path is required")
	}
	content, ok := args["content"].(string)
	if !ok {
		return "", fmt.Errorf("content is required")
	}

	if !s.isAllowed(path) {
		return "", fmt.Errorf("path not allowed: %s", path)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	return fmt.Sprintf("Written %d bytes to %s", len(content), path), nil
}

func (s *FileSystemServer) listDirectory(args map[string]interface{}) (string, error) {
	path, ok := args["path"].(string)
	if !ok {
		return "", fmt.Errorf("path is required")
	}
	path = strings.TrimSpace(path)
	if path == "." || path == "./" || path == ".\\" {
		if wd, err := os.Getwd(); err == nil {
			path = wd
		}
	}

	if !s.isAllowed(path) {
		return "", fmt.Errorf("path not allowed: %s", path)
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return "", fmt.Errorf("failed to read directory: %w", err)
	}

	var result strings.Builder
	for _, entry := range entries {
		info, _ := entry.Info()
		if entry.IsDir() {
			result.WriteString(fmt.Sprintf("d %s/\n", entry.Name()))
		} else {
			result.WriteString(fmt.Sprintf("- %s (%d bytes)\n", entry.Name(), info.Size()))
		}
	}

	return result.String(), nil
}

func (s *FileSystemServer) createDirectory(args map[string]interface{}) (string, error) {
	path, ok := args["path"].(string)
	if !ok {
		return "", fmt.Errorf("path is required")
	}

	if !s.isAllowed(path) {
		return "", fmt.Errorf("path not allowed: %s", path)
	}

	if err := os.MkdirAll(path, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}

	return fmt.Sprintf("Created directory: %s", path), nil
}

func (s *FileSystemServer) deleteFile(args map[string]interface{}) (string, error) {
	path, ok := args["path"].(string)
	if !ok {
		return "", fmt.Errorf("path is required")
	}

	if !s.isAllowed(path) {
		return "", fmt.Errorf("path not allowed: %s", path)
	}

	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("failed to stat: %w", err)
	}

	if info.IsDir() {
		if err := os.RemoveAll(path); err != nil {
			return "", fmt.Errorf("failed to delete directory: %w", err)
		}
		return fmt.Sprintf("Deleted directory: %s", path), nil
	}

	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("failed to delete file: %w", err)
	}

	return fmt.Sprintf("Deleted file: %s", path), nil
}

func (s *FileSystemServer) searchFiles(args map[string]interface{}) (string, error) {
	path, ok := args["path"].(string)
	if !ok {
		return "", fmt.Errorf("path is required")
	}
	pattern, ok := args["pattern"].(string)
	if !ok {
		return "", fmt.Errorf("pattern is required")
	}

	if !s.isAllowed(path) {
		return "", fmt.Errorf("path not allowed: %s", path)
	}

	var matches []string
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := filepath.Base(p)
		if matched, _ := filepath.Match(pattern, name); matched {
			rel, _ := filepath.Rel(path, p)
			matches = append(matches, rel)
		}
		return nil
	})

	if err != nil {
		return "", fmt.Errorf("search failed: %w", err)
	}

	if len(matches) == 0 {
		return "No matches found", nil
	}

	return strings.Join(matches, "\n"), nil
}

func (s *FileSystemServer) getFileInfo(args map[string]interface{}) (string, error) {
	path, ok := args["path"].(string)
	if !ok {
		return "", fmt.Errorf("path is required")
	}

	if !s.isAllowed(path) {
		return "", fmt.Errorf("path not allowed: %s", path)
	}

	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("failed to stat: %w", err)
	}

	return fmt.Sprintf("Name: %s\nSize: %d bytes\nIsDir: %t\nModTime: %s",
		info.Name(), info.Size(), info.IsDir(), info.ModTime().Format("2006-01-02 15:04:05")), nil
}

func (s *FileSystemServer) readFileLineRange(args map[string]interface{}) (string, error) {
	path, ok := args["path"].(string)
	if !ok {
		return "", fmt.Errorf("path is required")
	}

	if !s.isAllowed(path) {
		return "", fmt.Errorf("path not allowed: %s", path)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	start, ok := args["start"].(float64)
	if !ok {
		return "", fmt.Errorf("start is required")
	}
	end, ok := args["end"].(float64)
	if !ok {
		return "", fmt.Errorf("end is required")
	}

	startIdx := int(start) - 1
	endIdx := int(end)

	if startIdx < 0 || startIdx >= len(lines) {
		return "", fmt.Errorf("start line out of range")
	}
	if endIdx < startIdx || endIdx > len(lines) {
		return "", fmt.Errorf("end line out of range")
	}

	return strings.Join(lines[startIdx:endIdx], "\n"), nil
}

func (s *FileSystemServer) deleteLineRange(args map[string]interface{}) (string, error) {
	path, ok := args["path"].(string)
	if !ok {
		return "", fmt.Errorf("path is required")
	}

	if !s.isAllowed(path) {
		return "", fmt.Errorf("path not allowed: %s", path)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	start, ok := args["start"].(float64)
	if !ok {
		return "", fmt.Errorf("start is required")
	}
	end, ok := args["end"].(float64)
	if !ok {
		return "", fmt.Errorf("end is required")
	}

	startIdx := int(start) - 1
	endIdx := int(end)

	if startIdx < 0 || startIdx >= len(lines) {
		return "", fmt.Errorf("start line out of range")
	}
	if endIdx < startIdx || endIdx > len(lines) {
		return "", fmt.Errorf("end line out of range")
	}

	lines = append(lines[:startIdx], lines[endIdx:]...)

	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	return fmt.Sprintf("Deleted lines %d-%d from %s", int(start), int(end), path), nil
}

func (s *FileSystemServer) insertAtLine(args map[string]interface{}) (string, error) {
	path, ok := args["path"].(string)
	if !ok {
		return "", fmt.Errorf("path is required")
	}

	if !s.isAllowed(path) {
		return "", fmt.Errorf("path not allowed: %s", path)
	}

	content, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	lineNum, ok := args["line"].(float64)
	if !ok {
		return "", fmt.Errorf("line is required")
	}
	contentToInsert, ok := args["content"].(string)
	if !ok {
		return "", fmt.Errorf("content is required")
	}

	insertIdx := int(lineNum)
	if insertIdx < 0 || insertIdx > len(lines) {
		return "", fmt.Errorf("line number out of range")
	}

	lines = append(lines[:insertIdx], append([]string{contentToInsert}, lines[insertIdx:]...)...)

	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	return fmt.Sprintf("Inserted content at line %d in %s", int(lineNum), path), nil
}

func (s *FileSystemServer) searchFileText(args map[string]interface{}) (string, error) {
	path, ok := args["path"].(string)
	if !ok {
		return "", fmt.Errorf("path is required")
	}
	query, ok := args["query"].(string)
	if !ok {
		return "", fmt.Errorf("query is required")
	}
	caseSensitive, _ := args["case_sensitive"].(bool)
	useRegex, _ := args["use_regex"].(bool)

	if !s.isAllowed(path) {
		return "", fmt.Errorf("path not allowed: %s", path)
	}

	var regex *regexp.Regexp

	if useRegex {
		pattern := query
		if !caseSensitive {
			pattern = "(?i)" + pattern
		}
		var err error
		regex, err = regexp.Compile(pattern)
		if err != nil {
			return "", fmt.Errorf("invalid regex pattern: %w", err)
		}
	}

	var results []string
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		content, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		text := string(content)
		matched := false

		if useRegex {
			matched = regex.MatchString(text)
		} else {
			searchText := text
			searchQuery := query
			if !caseSensitive {
				searchText = strings.ToLower(text)
				searchQuery = strings.ToLower(query)
			}
			matched = strings.Contains(searchText, searchQuery)
		}

		if matched {
			rel, _ := filepath.Rel(path, p)
			results = append(results, rel)
		}
		return nil
	})

	if err != nil {
		return "", fmt.Errorf("search failed: %w", err)
	}

	if len(results) == 0 {
		return "No matches found", nil
	}

	return strings.Join(results, "\n"), nil
}

func (s *FileSystemServer) searchInFile(args map[string]interface{}) (string, error) {
	path, ok := args["path"].(string)
	if !ok {
		return "", fmt.Errorf("path is required")
	}
	query, ok := args["query"].(string)
	if !ok {
		return "", fmt.Errorf("query is required")
	}
	caseSensitive, _ := args["case_sensitive"].(bool)
	useRegex, _ := args["use_regex"].(bool)

	if !s.isAllowed(path) {
		return "", fmt.Errorf("path not allowed: %s", path)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	var regex *regexp.Regexp
	if useRegex {
		pattern := query
		if !caseSensitive {
			pattern = "(?i)" + pattern
		}
		regex, err = regexp.Compile(pattern)
		if err != nil {
			return "", fmt.Errorf("invalid regex pattern: %w", err)
		}
	}

	lines := strings.Split(string(content), "\n")
	var matches []string

	for i, line := range lines {
		matched := false
		if useRegex {
			matched = regex.MatchString(line)
		} else {
			searchLine := line
			searchQuery := query
			if !caseSensitive {
				searchLine = strings.ToLower(line)
				searchQuery = strings.ToLower(query)
			}
			matched = strings.Contains(searchLine, searchQuery)
		}
		if matched {
			matches = append(matches, fmt.Sprintf("%d: %s", i+1, line))
		}
	}

	if len(matches) == 0 {
		return "No matches found", nil
	}

	return strings.Join(matches, "\n"), nil
}

func (s *FileSystemServer) AddPath(ctx context.Context, path string) error {
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

func (s *FileSystemServer) TempAddPath(path string) {
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

func (s *FileSystemServer) AddURL(ctx context.Context, url string) error {
	return fmt.Errorf("filesystem server does not support URLs")
}

func (s *FileSystemServer) RemovePath(ctx context.Context, path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	for i, p := range s.allowedPaths {
		if p == path {
			s.allowedPaths = append(s.allowedPaths[:i], s.allowedPaths[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("path not found: %s", path)
}

func (s *FileSystemServer) RemoveURL(ctx context.Context, url string) error {
	return fmt.Errorf("filesystem server does not support URLs")
}

func (s *FileSystemServer) AllowedPaths() []string {
	return s.allowedPaths
}

func (s *FileSystemServer) Close() error {
	return nil
}
