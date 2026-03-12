package context

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	ignore "github.com/sabhiram/go-gitignore"
)

const (
	maxFileSizeBytes = 200 * 1024
	maxTreeLines     = 500
	maxSnippetBytes  = 8 * 1024
	maxTotalBytes    = 50 * 1024
	maxGrepHits      = 200
	treeCacheName    = ".gtree"
)

var (
	defaultIgnores = []string{
		"gtext/",
		".git/",
		treeCacheName,
	}
)

type GrepRequest struct {
	Pattern string
	Paths   []string
}

// BuildTree returns a newline-delimited list of files and directories.
func BuildTree(root string) (string, error) {
	ig := loadGitignore(root)
	if err := ensureGitignore(root); err != nil {
		return "", err
	}
	var lines []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		slashRel := filepath.ToSlash(rel)
		if shouldIgnorePath(ig, slashRel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if len(lines) >= maxTreeLines {
			return fs.SkipAll
		}
		if d.IsDir() {
			lines = append(lines, slashRel+"/")
			return nil
		}
		lines = append(lines, slashRel)
		return nil
	})
	if err != nil && err != fs.SkipAll {
		return "", err
	}

	if len(lines) == 0 {
		return "(no files found)", nil
	}

	sort.Strings(lines)
	if len(lines) >= maxTreeLines {
		lines = append(lines, "... (truncated)")
	}
	tree := strings.Join(lines, "\n")
	_ = writeTreeCache(root, tree)
	return tree, nil
}

// ResolvePaths expands requested paths/globs against the project root.
func ResolvePaths(root string, requested []string) ([]string, error) {
	ig := loadGitignore(root)
	wanted := map[string]struct{}{}
	patterns := make([]string, 0, len(requested))
	for _, entry := range requested {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		patterns = append(patterns, filepath.ToSlash(entry))
	}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		slashRel := filepath.ToSlash(rel)
		if shouldIgnorePath(ig, slashRel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}

		for _, pattern := range patterns {
			if pattern == slashRel {
				wanted[slashRel] = struct{}{}
				continue
			}
			if strings.ContainsAny(pattern, "*?[") {
				if ok, _ := filepath.Match(pattern, slashRel); ok {
					wanted[slashRel] = struct{}{}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	results := make([]string, 0, len(wanted))
	for path := range wanted {
		results = append(results, path)
	}
	sort.Strings(results)
	return results, nil
}

// ReadFiles returns file excerpts for the requested paths.
func ReadFiles(root string, paths []string) (string, error) {
	var buf strings.Builder
	total := 0
	for _, rel := range paths {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() {
			continue
		}
		if info.Size() > maxFileSizeBytes {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil || isBinary(data) {
			continue
		}
		content := string(data)
		if len(content) > maxSnippetBytes {
			content = content[:maxSnippetBytes]
		}
		entry := fmt.Sprintf("FILE: %s\n%s\n\n", rel, content)
		if total+len(entry) > maxTotalBytes {
			break
		}
		buf.WriteString(entry)
		total += len(entry)
	}
	return strings.TrimSpace(buf.String()), nil
}

// GrepFiles searches for patterns in requested files.
func GrepFiles(root string, req GrepRequest) (string, error) {
	pattern := strings.TrimSpace(req.Pattern)
	if pattern == "" {
		return "", nil
	}
	paths := req.Paths
	if len(paths) == 0 {
		lower := strings.ToLower(pattern)
		if strings.Contains(lower, "grep") || strings.Contains(lower, "rg ") || strings.Contains(pattern, "|") {
			return "", nil
		}
		return grepAll(root, pattern)
	}
	var buf strings.Builder
	count := 0
	for _, rel := range paths {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() {
			continue
		}
		if info.Size() > maxFileSizeBytes {
			continue
		}
		file, err := os.Open(abs)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			if strings.Contains(line, pattern) {
				buf.WriteString(fmt.Sprintf("GREP: %s:%d: %s\n", rel, lineNo, line))
				count++
				if count >= maxGrepHits {
					break
				}
			}
		}
		_ = file.Close()
		if count >= maxGrepHits {
			break
		}
	}
	return strings.TrimSpace(buf.String()), nil
}

func loadGitignore(root string) *ignore.GitIgnore {
	path := filepath.Join(root, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	return ignore.CompileIgnoreLines(lines...)
}

func shouldIgnorePath(ig *ignore.GitIgnore, path string) bool {
	for _, pattern := range defaultIgnores {
		if ok, _ := filepath.Match(pattern, path); ok {
			return true
		}
		if strings.HasPrefix(path, strings.TrimSuffix(pattern, "/")+"/") {
			return true
		}
	}
	if ig == nil {
		return false
	}
	return ig.MatchesPath(path)
}

func ensureGitignore(root string) error {
	path := filepath.Join(root, ".gitignore")
	existing, _ := os.ReadFile(path)
	content := string(existing)
	needsWrite := false
	for _, pattern := range defaultIgnores {
		if !strings.Contains(content, pattern) {
			content = strings.TrimRight(content, "\n") + "\n" + pattern + "\n"
			needsWrite = true
		}
	}
	if !needsWrite {
		return nil
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func writeTreeCache(root, tree string) error {
	path := filepath.Join(root, treeCacheName)
	return os.WriteFile(path, []byte(tree), 0o644)
}

func isBinary(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return false
}

func grepAll(root, pattern string) (string, error) {
	ig := loadGitignore(root)
	var buf strings.Builder
	count := 0

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		slashRel := filepath.ToSlash(rel)
		if shouldIgnorePath(ig, slashRel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil || info.IsDir() {
			return nil
		}
		if info.Size() > maxFileSizeBytes {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		scanner := bufio.NewScanner(file)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			if strings.Contains(line, pattern) {
				buf.WriteString(fmt.Sprintf("GREP: %s:%d: %s\n", slashRel, lineNo, line))
				count++
				if count >= maxGrepHits {
					break
				}
			}
		}
		_ = file.Close()
		if count >= maxGrepHits {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil && err != fs.SkipAll {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}
