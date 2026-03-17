package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var ErrPromptAborted = errors.New("prompt aborted")

var inputBoxStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("240")).
	Background(lipgloss.Color("236")).
	Padding(0, 1)

var commandTips = map[string]string{
	"/add-path":    "Add allowed MCP path: /add-path <filesys|url> <value>",
	"/remove-path": "Remove allowed MCP path: /remove-path <filesys|url> <value>",
	"/paths":       "List allowed MCP paths",
	"/tools":       "List available MCP tools",
	"/exit":        "Exit without saving",
	"/quit":        "Exit without saving",
	"/save":        "Save session and exit",
	"/save-exit":   "Save session and exit",
	"/kill":        "Kill a background process by PID or prune dead entries: /kill <pid> | /kill --prune",
	"/killbg":      "Kill all background processes",
	"/bg":          "List background processes",
	"/clear":       "Clear the screen",
	"/help":        "Show help for commands",
}

type promptModel struct {
	basePrompt      string
	contPrompt      string
	input           textinput.Model
	lines           []string
	multiline       bool
	history         []string
	historyIndex    int
	historyDraft    string
	submitted       bool
	aborted         bool
	completions     []string
	completeSeed    string
	completeIdx     int
	showCompletions bool
	width           int
	modelName       string
	contextUsage    int
	contextLimit    int
}

func newPromptModel(prompt string, history []string, modelName string, contextUsage int, contextLimit int) promptModel {
	ti := textinput.New()
	ti.Prompt = prompt
	ti.Placeholder = "Press ↵ Enter to submit"
	ti.PlaceholderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	ti.Focus()
	ti.CharLimit = 0

	return promptModel{
		basePrompt:   prompt,
		contPrompt:   "... ",
		input:        ti,
		history:      history,
		historyIndex: len(history),
		modelName:    modelName,
		contextUsage: contextUsage,
		contextLimit: contextLimit,
	}
}

func readPrompt(prompt string, history []string, modelName string, contextUsage int, contextLimit int) (string, error) {
	m := newPromptModel(prompt, history, modelName, contextUsage, contextLimit)
	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return "", err
	}
	result := finalModel.(promptModel)
	if result.aborted {
		return "", ErrPromptAborted
	}
	if !result.submitted {
		return "", nil
	}
	return result.value(), nil
}

func (m promptModel) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, tea.EnableBracketedPaste)
}

func (m promptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			m.aborted = true
			return m, tea.Quit
		}
		if msg.Type == tea.KeyTab {
			m.handleTab()
			return m, nil
		}
		if msg.Type == tea.KeyEnter {
			if !m.multiline && len(m.lines) == 0 {
				m.submitted = true
				return m, tea.Quit
			}
			if m.input.Value() == "" {
				m.submitted = true
				return m, tea.Quit
			}
			m.lines = append(m.lines, m.input.Value())
			m.input.SetValue("")
			m.input.SetCursor(0)
			m.multiline = true
			m.updatePrompt()
			return m, nil
		}

		if msg.Paste || (msg.Type == tea.KeyRunes && strings.ContainsRune(string(msg.Runes), '\n')) {
			m.handlePaste(string(msg.Runes))
			return m, nil
		}

		if msg.Type == tea.KeyRunes || msg.Type == tea.KeyBackspace || msg.Type == tea.KeyDelete {
			m.resetCompletion()
		}

		if !m.multiline && len(m.lines) == 0 {
			switch msg.Type {
			case tea.KeyUp:
				m.historyUp()
				m.updatePrompt()
				m.resetCompletion()
				return m, nil
			case tea.KeyDown:
				m.historyDown()
				m.updatePrompt()
				m.resetCompletion()
				return m, nil
			}
		}
	}
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = size.Width
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.updatePrompt()
	return m, cmd
}

func (m promptModel) View() string {
	var b strings.Builder
	if len(m.lines) > 0 {
		b.WriteString(m.basePrompt)
		b.WriteString(m.lines[0])
		b.WriteByte('\n')
		for _, line := range m.lines[1:] {
			b.WriteString(m.contPrompt)
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	boxStyle := inputBoxStyle
	if m.width > 0 {
		boxStyle = boxStyle.Width(max(0, m.width-4))
	}
	b.WriteString(boxStyle.Render(m.input.View()))

	if m.contextLimit > 0 || m.modelName != "" {
		b.WriteByte('\n')

		var leftContent, rightContent string

		if m.modelName != "" {
			leftContent = "Model: " + m.modelName
		}

		if m.contextLimit > 0 {
			usedPercent := float64(m.contextUsage) / float64(m.contextLimit)
			meterWidth := 10
			filled := int(usedPercent * float64(meterWidth))
			if filled > meterWidth {
				filled = meterWidth
			}
			var meter strings.Builder
			meter.WriteString("[")
			greenStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
			yellowStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("226"))
			redStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
			for i := 0; i < meterWidth; i++ {
				if i < filled {
					if usedPercent > 0.8 {
						meter.WriteString(redStyle.Render("="))
					} else if usedPercent > 0.5 {
						meter.WriteString(yellowStyle.Render("="))
					} else {
						meter.WriteString(greenStyle.Render("="))
					}
				} else {
					meter.WriteString("-")
				}
			}
			meter.WriteString("]")
			meter.WriteString(fmt.Sprintf(" %s/%s", formatNumber(m.contextUsage), formatNumber(m.contextLimit)))
			rightContent = meter.String()
		}

		grayStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

		if leftContent != "" {
			b.WriteString(grayStyle.Render(leftContent))
		}
		if rightContent != "" {
			if leftContent != "" {
				b.WriteString(" ")
			}
			b.WriteString(grayStyle.Render(rightContent))
		}
	}
	if m.showCompletions && len(m.completions) > 0 {
		var display []string
		for _, cmd := range m.completions {
			name := strings.TrimSpace(cmd)
			if tip := commandTips[name]; tip != "" {
				display = append(display, name+" - "+tip)
			} else {
				display = append(display, name)
			}
		}
		b.WriteByte('\n')
		b.WriteString(strings.Join(display, "\n"))
	}
	return b.String()
}

func (m promptModel) value() string {
	if len(m.lines) == 0 {
		return m.input.Value()
	}
	lines := append([]string{}, m.lines...)
	if m.input.Value() != "" {
		lines = append(lines, m.input.Value())
	}
	return strings.Join(lines, "\n")
}

func (m *promptModel) updatePrompt() {
	if len(m.lines) > 0 || m.multiline {
		m.input.Prompt = m.contPrompt
	} else {
		m.input.Prompt = m.basePrompt
	}
}

func (m *promptModel) resetCompletion() {
	m.completions = nil
	m.completeSeed = ""
	m.completeIdx = 0
	m.showCompletions = false
}

func (m *promptModel) handlePaste(paste string) {
	if paste == "" {
		return
	}
	paste = strings.ReplaceAll(paste, "\r\n", "\n")
	paste = strings.ReplaceAll(paste, "\r", "\n")

	if !strings.Contains(paste, "\n") {
		m.insertAtCursor(paste)
		return
	}

	parts := strings.Split(paste, "\n")
	left, right := splitAtRune(m.input.Value(), m.input.Position())

	first := left + parts[0]
	last := parts[len(parts)-1] + right

	m.lines = append(m.lines, first)
	if len(parts) > 2 {
		m.lines = append(m.lines, parts[1:len(parts)-1]...)
	}

	m.input.SetValue(last)
	m.input.SetCursor(utf8.RuneCountInString(parts[len(parts)-1]))
	m.multiline = true
	m.updatePrompt()
}

func (m *promptModel) insertAtCursor(s string) {
	if s == "" {
		return
	}
	left, right := splitAtRune(m.input.Value(), m.input.Position())
	m.input.SetValue(left + s + right)
	m.input.SetCursor(m.input.Position() + utf8.RuneCountInString(s))
}

func splitAtRune(s string, pos int) (string, string) {
	runes := []rune(s)
	if pos < 0 {
		pos = 0
	}
	if pos > len(runes) {
		pos = len(runes)
	}
	return string(runes[:pos]), string(runes[pos:])
}

func (m *promptModel) historyUp() {
	if len(m.history) == 0 {
		return
	}
	if m.historyIndex == len(m.history) {
		m.historyDraft = m.input.Value()
	}
	if m.historyIndex > 0 {
		m.historyIndex--
		m.input.SetValue(m.history[m.historyIndex])
		m.input.SetCursor(len([]rune(m.history[m.historyIndex])))
	}
}

func (m *promptModel) historyDown() {
	if len(m.history) == 0 {
		return
	}
	if m.historyIndex < len(m.history)-1 {
		m.historyIndex++
		m.input.SetValue(m.history[m.historyIndex])
		m.input.SetCursor(len([]rune(m.history[m.historyIndex])))
		return
	}
	if m.historyIndex == len(m.history)-1 {
		m.historyIndex = len(m.history)
		m.input.SetValue(m.historyDraft)
		m.input.SetCursor(len([]rune(m.historyDraft)))
	}
}

func (m *promptModel) handleTab() {
	value := m.input.Value()
	if value != "" && !strings.HasPrefix(value, "/") {
		return
	}
	if strings.Contains(value, " ") {
		return
	}
	if value != m.completeSeed {
		m.completeSeed = value
		m.completions = nil
		for _, cmd := range slashCommands {
			if value == "" || strings.HasPrefix(cmd, value) {
				m.completions = append(m.completions, cmd)
			}
		}
	}
	if len(m.completions) == 0 {
		m.showCompletions = false
		return
	}
	if len(m.completions) == 1 && value != "" && value != m.completions[0] {
		m.input.SetValue(m.completions[0])
		m.input.SetCursor(len([]rune(m.completions[0])))
		m.resetCompletion()
		return
	}
	m.showCompletions = true
}

func appendHistory(history []string, item string) []string {
	if len(history) == 0 {
		return append(history, item)
	}
	if history[len(history)-1] == item {
		return history
	}
	return append(history, item)
}

func formatNumber(n int) string {
	s := strconv.FormatInt(int64(n), 10)
	result := ""
	count := 0
	for i := len(s) - 1; i >= 0; i-- {
		if count > 0 && count%3 == 0 {
			result = "," + result
		}
		result = string(s[i]) + result
		count++
	}
	return result
}
