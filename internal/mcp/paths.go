package mcp

import (
	"os"
	"strings"
)

func sanitizeAllowedPaths(paths []string) []string {
	seen := make(map[string]bool)
	var cleaned []string
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		cleaned = append(cleaned, p)
	}
	return cleaned
}

func withDefaultCwd(paths []string) []string {
	if len(paths) > 0 {
		return paths
	}
	if wd, err := os.Getwd(); err == nil && strings.TrimSpace(wd) != "" {
		return []string{wd}
	}
	return paths
}
