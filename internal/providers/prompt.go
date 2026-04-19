package providers

import "strings"

const (
	userRequestMarker = "User request:"
	userAskedMarker   = "User asked:"
)

// SplitSystemUserPrompt splits a prompt into system and user parts
func SplitSystemUserPrompt(prompt string) (string, string) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", ""
	}

	idx := -1
	marker := ""
	if i := strings.Index(prompt, userRequestMarker); i != -1 {
		idx = i
		marker = userRequestMarker
	}
	if i := strings.Index(prompt, userAskedMarker); i != -1 && (idx == -1 || i < idx) {
		idx = i
		marker = userAskedMarker
	}
	if idx == -1 {
		return "", prompt
	}

	system := strings.TrimSpace(prompt[:idx])
	user := strings.TrimSpace(prompt[idx+len(marker):])
	return system, user
}
