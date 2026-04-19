package context

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	ignore "github.com/sabhiram/go-gitignore"
)

const (
	maxTreeLines  = 500
	treeCacheName = ".gtree"
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
