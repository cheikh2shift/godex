package toolcalls

import (
	"encoding/json"
	"strings"

	"github.com/cheikh2shift/godex/internal/rxcache"
)

// ExtractAllToolCalls extracts tool calls from a model response in a variety of common formats.
// It returns items shaped as: {"name": string, "arguments": map[string]interface{}}.
func ExtractAllToolCalls(text string) []map[string]interface{} {
	var results []map[string]interface{}

	candidates := []string{text}
	if normalized := normalizeToolCallText(text); normalized != text {
		candidates = append(candidates, normalized)
	}

	for _, candidate := range candidates {
		// First, try to find markdown code blocks using proper extraction
		codeBlocks := extractCodeBlockContent(candidate)

		for _, content := range codeBlocks {
			content = strings.TrimSpace(content)

			// Try to parse the whole block as one JSON object
			var data map[string]interface{}
			if err := json.Unmarshal([]byte(content), &data); err == nil {
				if _, ok := data["name"].(string); ok {
					results = append(results, processToolData(data))
					continue
				}
			}

			// Try to parse the block as a JSON array of tool calls
			if arrayCalls := extractToolCallsFromJSONArray(content); len(arrayCalls) > 0 {
				results = append(results, arrayCalls...)
				continue
			}

			// If not a single object, try sanitizing multi-line strings
			sanitized := sanitizeMultilineJson(content)
			var sanitizedData map[string]interface{}
			if err := json.Unmarshal([]byte(sanitized), &sanitizedData); err == nil {
				if _, ok := sanitizedData["name"].(string); ok {
					results = append(results, processToolData(sanitizedData))
					continue
				}
			}
			if arrayCalls := extractToolCallsFromJSONArray(sanitized); len(arrayCalls) > 0 {
				results = append(results, arrayCalls...)
				continue
			}

			// Fall back to extracting individual JSON objects
			extracted := extractJsonObjects(sanitized)
			if len(extracted) > 0 {
				results = append(results, extracted...)
			}
		}

		// If no tools found in code blocks, search the whole text for { ... } blobs
		if len(results) == 0 {
			results = append(results, extractJsonObjects(candidate)...)
		}
		if len(results) == 0 {
			results = append(results, extractToolCallsFromJSONArray(candidate)...)
		}

		// Safety fallback for the specific [TOOL_CALL] format
		if len(results) == 0 {
			toolCallRe := rxcache.MustCompile(`(?s)\[TOOL_CALL\]\s*(.*?)\s*\[/TOOL_CALL\]`)
			tcMatches := toolCallRe.FindAllStringSubmatch(candidate, -1)
			for _, m := range tcMatches {
				if len(m) > 1 {
					nameRe := rxcache.MustCompile(`tool\s*=>\s*"([^"]+)"`)
					if nameMatch := nameRe.FindStringSubmatch(m[1]); len(nameMatch) > 1 {
						results = append(results, map[string]interface{}{"name": nameMatch[1], "arguments": map[string]interface{}{}})
					}
				}
			}
		}

		// Parse native tool call format: [TOOL_CALL: name | {"arg": "value"}]
		if len(results) == 0 {
			nativeMatches := extractNativeToolCalls(candidate)
			for _, m := range nativeMatches {
				name := m[0]
				argsStr := m[1]
				var args map[string]interface{}
				if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
					args = map[string]interface{}{"_raw": argsStr}
				}
				results = append(results, map[string]interface{}{"name": name, "arguments": args})
			}
		}

		if len(results) > 0 {
			break
		}
	}

	return results
}

func extractToolCallsFromJSONArray(text string) []map[string]interface{} {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	var items []map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &items); err != nil {
		return nil
	}
	var results []map[string]interface{}
	for _, item := range items {
		if item == nil {
			continue
		}
		if _, ok := item["name"].(string); ok {
			results = append(results, processToolData(item))
			continue
		}
		if tool, ok := item["tool"].(string); ok {
			args := parseToolArgs(item)
			if len(args) == 0 {
				if params, ok := item["parameters"].(map[string]interface{}); ok {
					args = params
				}
			}
			results = append(results, map[string]interface{}{
				"name":      tool,
				"arguments": args,
			})
		}
	}
	return results
}

func extractNativeToolCalls(text string) [][2]string {
	var results [][2]string
	re := rxcache.MustCompile(`\[TOOL_CALL:\s*([^\s|]+)\s*\|`)
	matches := re.FindAllStringSubmatchIndex(text, -1)
	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		name := text[m[2]:m[3]]
		jsonStart := m[1]
		for jsonStart < len(text) && (text[jsonStart] == ' ' || text[jsonStart] == '\t') {
			jsonStart++
		}
		var jsonEnd int
		inString := false
		escapeNext := false
		braceDepth := 0
		for i := jsonStart; i < len(text); i++ {
			c := text[i]
			if escapeNext {
				escapeNext = false
				continue
			}
			switch c {
			case '\\':
				escapeNext = true
			case '"':
				inString = !inString
			case '{', '[':
				if !inString {
					braceDepth++
				}
			case '}', ']':
				if !inString {
					braceDepth--
					if braceDepth == 0 {
						jsonEnd = i + 1
						break
					}
				}
			}
		}
		if jsonEnd > jsonStart {
			results = append(results, [2]string{name, text[jsonStart:jsonEnd]})
		}
	}
	return results
}

func normalizeToolCallText(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 {
		return text
	}

	nonEmpty := 0
	withMargin := 0
	marginRe := rxcache.MustCompile(`^\s*│`)
	stripRe := rxcache.MustCompile(`^\s*│\s?`)

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		nonEmpty++
		if marginRe.MatchString(line) {
			withMargin++
		}
	}
	if nonEmpty == 0 || withMargin*2 < nonEmpty {
		return text
	}

	for i, line := range lines {
		lines[i] = stripRe.ReplaceAllString(line, "")
	}
	return strings.Join(lines, "\n")
}

func sanitizeMultilineJson(text string) string {
	var result strings.Builder
	inString := false
	escapeNext := false
	lines := strings.Split(text, "\n")

	for lineIdx, line := range lines {
		for i := 0; i < len(line); i++ {
			char := rune(line[i])

			if escapeNext {
				if char == 'n' {
					result.WriteString("\\n")
				} else {
					result.WriteRune('\\')
					result.WriteRune(char)
				}
				escapeNext = false
				continue
			}

			if char == '\\' && inString {
				escapeNext = true
				continue
			}

			if char == '"' {
				if !inString {
					inString = true
				} else {
					backslashCount := 0
					j := i - 1
					for j >= 0 && line[j] == '\\' {
						backslashCount++
						j--
					}
					if backslashCount%2 == 0 {
						inString = false
					}
				}
			}

			result.WriteRune(char)
		}

		if inString && lineIdx < len(lines)-1 {
			result.WriteString("\\n")
		} else if !inString {
			result.WriteRune('\n')
		}
	}

	return result.String()
}

func extractCodeBlockContent(text string) []string {
	var results []string
	lines := strings.Split(text, "\n")
	inBlock := false
	blockStart := -1

	for i, line := range lines {
		if !inBlock {
			if strings.Contains(line, "```") {
				inBlock = true
				blockStart = i + 1
			}
		} else {
			if strings.TrimSpace(line) == "```" {
				content := strings.Join(lines[blockStart:i], "\n")
				results = append(results, content)
				inBlock = false
				blockStart = -1
			}
		}
	}
	return results
}

func extractJsonObjects(text string) []map[string]interface{} {
	var results []map[string]interface{}
	startIdx := -1
	braceCount := 0

	for i, char := range text {
		if char == '{' {
			if braceCount == 0 {
				startIdx = i
			}
			braceCount++
		} else if char == '}' {
			if braceCount > 0 {
				braceCount--
				if braceCount == 0 && startIdx != -1 {
					potentialJSON := text[startIdx : i+1]
					var data map[string]interface{}
					if err := json.Unmarshal([]byte(potentialJSON), &data); err == nil {
						if _, ok := data["name"].(string); ok {
							results = append(results, processToolData(data))
						}
					}
				}
			}
		}
	}
	return results
}

func processToolData(data map[string]interface{}) map[string]interface{} {
	name, _ := data["name"].(string)
	args := parseToolArgs(data)

	// Clean up string arguments
	for k, v := range args {
		if s, ok := v.(string); ok {
			s = strings.ReplaceAll(s, "\\u0026", "&")
			s = strings.ReplaceAll(s, "&#38;", "&")
			s = strings.ReplaceAll(s, "&amp;", "&")
			args[k] = s
		}
	}
	return map[string]interface{}{"name": name, "arguments": args}
}

func parseToolArgs(data map[string]interface{}) map[string]interface{} {
	if a, ok := data["arguments"].(map[string]interface{}); ok {
		return a
	}
	if a, ok := data["args"].(map[string]interface{}); ok {
		return a
	}
	if a, ok := data["parameters"].(map[string]interface{}); ok {
		return a
	}
	if a, ok := data["params"].(map[string]interface{}); ok {
		return a
	}
	if raw, ok := data["parameters"].(string); ok && strings.TrimSpace(raw) != "" {
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
			return parsed
		}
	}
	if raw, ok := data["arguments"].(string); ok && strings.TrimSpace(raw) != "" {
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
			return parsed
		}
	}
	return make(map[string]interface{})
}
