package agent

import (
	"fmt"
	"strings"

	projectctx "github.com/cheikh2shift/godex/internal/context"
)

const (
	toolIntroFmt = `You have access to MCP tools. Use them to accomplish tasks.
You can call multiple tools in a single response to be more efficient.
To call tools, you MUST respond with one or more JSON objects, each in its own markdown code block.

Example:
` + "```" + `json
{
  "name": "read_file",
  "arguments": {
    "path": "/absolute/path/to/file"
  }
}
` + "```" + `

IMPORTANT: Execute tools FIRST, then provide the final answer. Do NOT include any final answer, summary, or "FINAL_ANSWER:" until AFTER you have executed all necessary tools and received their results. If you need to run commands/tests to verify something, run them first before answering.
If you need to start a server/service or any long-running process, you MUST use background: true and continue.

Project tree:
%s

User prompt:
%s`
	toolFollowupFmt = "Tool results:\n%s\n\nPlease proceed or provide the FINAL_ANSWER if complete."
)

type ToolRequest struct {
	Reads       []string
	Greps       []projectctx.GrepRequest
	Tree        bool
	Description string
}

func ToolIntroPrompt(prompt, tree string) string {
	if strings.TrimSpace(tree) == "" {
		tree = "(tree unavailable)"
	}
	return fmt.Sprintf(toolIntroFmt, tree, prompt)
}

func ToolFollowupPrompt(excerpts string) string {
	return fmt.Sprintf(toolFollowupFmt, excerpts)
}

func ParseToolRequests(text string) (ToolRequest, bool) {
	lines := strings.Split(text, "\n")
	req := ToolRequest{}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		switch {
		case lower == "tree":
			req.Tree = true
		case strings.HasPrefix(lower, "read:") || strings.HasPrefix(lower, "request:"):
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) < 2 {
				continue
			}
			req.Reads = append(req.Reads, splitList(parts[1])...)
		case strings.HasPrefix(lower, "grep:"):
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) < 2 {
				continue
			}
			spec := strings.TrimSpace(parts[1])
			if pattern, paths, ok := parseGrepSpec(spec); ok {
				req.Greps = append(req.Greps, projectctx.GrepRequest{Pattern: pattern, Paths: paths})
				continue
			}
			if looksLikePath(spec) {
				req.Reads = append(req.Reads, spec)
			}
		}
	}

	if len(req.Reads) == 0 && len(req.Greps) == 0 && !req.Tree {
		return ToolRequest{}, false
	}

	var descParts []string
	if req.Tree {
		descParts = append(descParts, "TREE")
	}
	if len(req.Reads) > 0 {
		descParts = append(descParts, fmt.Sprintf("READ: %s", strings.Join(req.Reads, ", ")))
	}
	for _, grep := range req.Greps {
		descParts = append(descParts, fmt.Sprintf("GREP: %s | %s", grep.Pattern, strings.Join(grep.Paths, ", ")))
	}
	req.Description = strings.Join(descParts, " | ")
	return req, true
}

func parseGrepSpec(spec string) (string, []string, bool) {
	pieces := strings.SplitN(spec, "|", 2)
	if len(pieces) < 2 {
		return "", nil, false
	}
	left := strings.TrimSpace(pieces[0])
	right := strings.TrimSpace(pieces[1])
	if left == "" || right == "" {
		return "", nil, false
	}

	if looksLikePath(left) && looksLikeGrepCommand(right) {
		if extracted := extractGrepPattern(right); extracted != "" {
			return extracted, []string{left}, true
		}
	}

	pattern := left
	paths := filterPaths(splitList(right))
	if pattern == "" || len(paths) == 0 {
		return "", nil, false
	}
	if looksLikeGrepCommand(pattern) {
		return "", nil, false
	}
	return pattern, paths, true
}

func looksLikePath(value string) bool {
	if strings.ContainsAny(value, " \t") {
		return false
	}
	return strings.Contains(value, "/") || strings.Contains(value, ".")
}

func looksLikeGrepCommand(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "grep") || strings.Contains(lower, "rg ") || strings.Contains(lower, "ripgrep")
}

func extractGrepPattern(value string) string {
	if extracted := extractQuoted(value); extracted != "" {
		return extracted
	}
	lower := strings.ToLower(value)
	if idx := strings.Index(lower, "grep"); idx >= 0 {
		rest := strings.TrimSpace(value[idx+4:])
		rest = strings.TrimLeft(rest, "-n ")
		if rest != "" {
			return strings.Fields(rest)[0]
		}
	}
	return ""
}

func extractQuoted(value string) string {
	for _, quote := range []string{"'", `"`} {
		if start := strings.Index(value, quote); start >= 0 {
			if end := strings.Index(value[start+1:], quote); end >= 0 {
				return value[start+1 : start+1+end]
			}
		}
	}
	return ""
}

func filterPaths(paths []string) []string {
	var out []string
	for _, p := range paths {
		if p == "" {
			continue
		}
		if looksLikeGrepCommand(p) || strings.Contains(p, "|") {
			continue
		}
		out = append(out, p)
	}
	return out
}

func ExtractCommandsFromResponse(text string) []string {
	return extractCommandBlocks(text)
}

func ExtractCommandFromResponse(text string) (string, bool) {
	commands := extractCommandBlocks(text)
	if len(commands) == 0 {
		return "", false
	}
	return commands[0], true
}

func extractCommandBlocks(text string) []string {
	lines := strings.Split(text, "\n")
	var commands []string
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		lower := strings.ToLower(trimmed)
		prefix := ""
		for _, p := range []string{"run:", "command:", "execute:"} {
			if strings.HasPrefix(lower, p) {
				prefix = p
				break
			}
		}
		if prefix == "" {
			continue
		}
		command := strings.TrimSpace(trimmed[len(prefix):])
		if command == "" {
			continue
		}
		marker := heredocMarker(command)
		if marker == "" {
			commands = append(commands, command)
			continue
		}
		var block strings.Builder
		block.WriteString(command)
		block.WriteString("\n")
		for j := i + 1; j < len(lines); j++ {
			block.WriteString(lines[j])
			block.WriteString("\n")
			if strings.TrimSpace(lines[j]) == marker {
				i = j
				break
			}
		}
		commands = append(commands, strings.TrimRight(block.String(), "\n"))
	}
	return commands
}

func heredocMarker(command string) string {
	idx := strings.Index(command, "<<")
	if idx == -1 {
		return ""
	}
	rest := strings.TrimSpace(command[idx+2:])
	if rest == "" {
		return ""
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	marker := strings.Trim(fields[0], "'\"")
	if marker == "" {
		return ""
	}
	return marker
}

func splitList(raw string) []string {
	parts := strings.Split(raw, ",")
	var out []string
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}
