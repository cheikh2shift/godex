package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/cheikh2shift/godex/internal/mcp"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func handleMCPServeSubcommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: godex mcp serve filesystem [options]")
	}

	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "filesystem", "files":
		fs := flag.NewFlagSet("mcp serve filesystem", flag.ContinueOnError)
		var allowedPaths stringList
		var autoConfirm bool
		var useRoots bool
		fs.Var(&allowedPaths, "allowed-path", "allowed path (repeatable); if omitted, falls back to client roots or cwd")
		fs.BoolVar(&autoConfirm, "auto-confirm", false, "auto-confirm restricted paths (unsafe)")
		fs.BoolVar(&useRoots, "use-roots", true, "use client-provided MCP roots as additional allowed paths")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return serveFilesystemMCP([]string(allowedPaths), autoConfirm, useRoots)
	default:
		return fmt.Errorf("unknown server: %s (supported: filesystem)", args[0])
	}
}

func serveFilesystemMCP(baseAllowedPaths []string, autoConfirm bool, useRoots bool) error {
	s := server.NewMCPServer(
		"godex-filesystem",
		version,
		server.WithToolCapabilities(true),
		server.WithRoots(),
	)

	resolveAllowedPaths := func(ctx context.Context) []string {
		paths := append([]string{}, baseAllowedPaths...)
		if useRoots {
			if res, err := s.RequestRoots(ctx, mcplib.ListRootsRequest{}); err == nil {
				for _, r := range res.Roots {
					if strings.TrimSpace(r.URI) == "" {
						continue
					}
					// Roots are usually file:// URIs, but some clients may send plain paths.
					p := strings.TrimPrefix(r.URI, "file://")
					if strings.TrimSpace(p) == "" {
						continue
					}
					paths = append(paths, p)
				}
			}
		}
		if len(paths) == 0 {
			if wd, err := os.Getwd(); err == nil && strings.TrimSpace(wd) != "" {
				paths = []string{wd}
			}
		}
		return uniqueStrings(paths)
	}

	addProxyTool := func(name string, description string, schema mcplib.ToolInputSchema) {
		s.AddTool(mcplib.Tool{
			Name:        name,
			Description: description,
			InputSchema: schema,
		}, func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
			allowed := resolveAllowedPaths(ctx)
			fs := mcp.NewFileSystemServer(allowed, autoConfirm)

			args, _ := request.Params.Arguments.(map[string]any)
			argMap := make(map[string]any, len(args))
			for k, v := range args {
				argMap[k] = v
			}

			out, err := fs.CallTool(ctx, name, argMap)
			if err != nil {
				return mcplib.NewToolResultError(err.Error()), nil
			}
			return mcplib.NewToolResultText(out), nil
		})
	}

	// Permissions helpers (works well with other agents' root/permission prompts).
	s.AddTool(mcplib.Tool{
		Name:        "list_allowed_paths",
		Description: "List currently allowed filesystem paths (union of server flags and client-provided roots).",
		InputSchema: mcplib.ToolInputSchema{Type: "object", Properties: map[string]any{}},
	}, func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		paths := resolveAllowedPaths(ctx)
		return mcplib.NewToolResultText(strings.Join(paths, "\n")), nil
	})

	// Core filesystem tools (proxy to internal built-in implementation).
	addProxyTool("read_file", "Read contents of a file.", mcplib.ToolInputSchema{
		Type: "object",
		Properties: map[string]any{
			"path": map[string]any{"type": "string", "description": "Absolute path to the file."},
		},
		Required: []string{"path"},
	})
	addProxyTool("write_file", "Write content to a file.", mcplib.ToolInputSchema{
		Type: "object",
		Properties: map[string]any{
			"path":    map[string]any{"type": "string", "description": "Absolute path to the file."},
			"content": map[string]any{"type": "string", "description": "Content to write."},
		},
		Required: []string{"path", "content"},
	})
	addProxyTool("list_directory", "List files in a directory.", mcplib.ToolInputSchema{
		Type: "object",
		Properties: map[string]any{
			"path": map[string]any{"type": "string", "description": "Absolute path to directory."},
		},
		Required: []string{"path"},
	})
	addProxyTool("create_directory", "Create a new directory.", mcplib.ToolInputSchema{
		Type: "object",
		Properties: map[string]any{
			"path": map[string]any{"type": "string", "description": "Directory path to create."},
		},
		Required: []string{"path"},
	})
	addProxyTool("delete_file", "Delete a file or directory.", mcplib.ToolInputSchema{
		Type: "object",
		Properties: map[string]any{
			"path": map[string]any{"type": "string", "description": "File or directory path to delete."},
		},
		Required: []string{"path"},
	})
	addProxyTool("search_files", "Search for files matching a glob pattern.", mcplib.ToolInputSchema{
		Type: "object",
		Properties: map[string]any{
			"path":    map[string]any{"type": "string", "description": "Directory to search."},
			"pattern": map[string]any{"type": "string", "description": "File pattern (e.g., *.go)."},
		},
		Required: []string{"path", "pattern"},
	})
	addProxyTool("get_file_info", "Get information about a file.", mcplib.ToolInputSchema{
		Type: "object",
		Properties: map[string]any{
			"path": map[string]any{"type": "string", "description": "File path."},
		},
		Required: []string{"path"},
	})
	addProxyTool("read_file_line_range", "Read a specific range of lines from a file.", mcplib.ToolInputSchema{
		Type: "object",
		Properties: map[string]any{
			"path":  map[string]any{"type": "string", "description": "File path."},
			"start": map[string]any{"type": "number", "description": "Start line number (1-indexed)."},
			"end":   map[string]any{"type": "number", "description": "End line number (inclusive)."},
		},
		Required: []string{"path", "start", "end"},
	})
	addProxyTool("replace_first_in_file", "Replace the first occurrence of a string in a file.", mcplib.ToolInputSchema{
		Type: "object",
		Properties: map[string]any{
			"path":    map[string]any{"type": "string", "description": "File path."},
			"find":    map[string]any{"type": "string", "description": "Text to find (plain string)."},
			"replace": map[string]any{"type": "string", "description": "Replacement text."},
		},
		Required: []string{"path", "find", "replace"},
	})
	addProxyTool("insert_at_line", "Insert content at a specific line number.", mcplib.ToolInputSchema{
		Type: "object",
		Properties: map[string]any{
			"path":    map[string]any{"type": "string", "description": "File path."},
			"line":    map[string]any{"type": "number", "description": "Line number to insert at (1-indexed, 0 = append at end)."},
			"content": map[string]any{"type": "string", "description": "Content to insert."},
		},
		Required: []string{"path", "line", "content"},
	})
	addProxyTool("search_file_text", "Search for text content within files in a directory.", mcplib.ToolInputSchema{
		Type: "object",
		Properties: map[string]any{
			"path":           map[string]any{"type": "string", "description": "Directory to search."},
			"query":          map[string]any{"type": "string", "description": "Text to search for."},
			"case_sensitive": map[string]any{"type": "boolean", "description": "Enable case-sensitive matching."},
			"use_regex":      map[string]any{"type": "boolean", "description": "Treat query as a regular expression."},
		},
		Required: []string{"path", "query"},
	})
	addProxyTool("search_in_file", "Search for text within a specific file.", mcplib.ToolInputSchema{
		Type: "object",
		Properties: map[string]any{
			"path":           map[string]any{"type": "string", "description": "File path."},
			"query":          map[string]any{"type": "string", "description": "Text to search for (ignored if pattern provided)."},
			"pattern":        map[string]any{"type": "string", "description": "Regex pattern (takes precedence)."},
			"case_sensitive": map[string]any{"type": "boolean", "description": "Enable case-sensitive matching."},
			"use_regex":      map[string]any{"type": "boolean", "description": "Treat query as a regular expression."},
		},
		Required: []string{"path"},
	})
	addProxyTool("search_directory_text", "Search for text content within files in a directory.", mcplib.ToolInputSchema{
		Type: "object",
		Properties: map[string]any{
			"path":           map[string]any{"type": "string", "description": "Directory to search."},
			"query":          map[string]any{"type": "string", "description": "Text to search for (ignored if pattern is provided)."},
			"pattern":        map[string]any{"type": "string", "description": "Regex pattern to search for (takes precedence over query)."},
			"case_sensitive": map[string]any{"type": "boolean", "description": "Enable case-sensitive matching."},
		},
		Required: []string{"path"},
	})

	log.Printf("[MCP] Serving built-in filesystem MCP server over stdio (autoConfirm=%v, useRoots=%v, baseAllowedPaths=%v)", autoConfirm, useRoots, baseAllowedPaths)
	return server.ServeStdio(s)
}
