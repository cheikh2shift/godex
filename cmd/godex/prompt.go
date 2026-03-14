package main

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

var ErrPromptAborted = errors.New("prompt aborted")

type promptModel struct {
	basePrompt   string
	contPrompt   string
	input        textinput.Model
	lines        []string
	multiline    bool
	history      []string
	historyIndex int
	historyDraft string
	submitted    bool
	aborted      bool
	completions  []string
	completeSeed string
	completeIdx  int
}

func newPromptModel(prompt string, history []string) promptModel {
	ti := textinput.New()
	ti.Prompt = prompt
	ti.Focus()
	ti.CharLimit = 0

	return promptModel{
		basePrompt:   prompt,
		contPrompt:   "... ",
		input:        ti,
		history:      history,
		historyIndex: len(history),
	}
}

func readPrompt(prompt string, history []string) (string, error) {
	m := newPromptModel(prompt, history)
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
			if m.applyCompletion() {
				return m, nil
			}
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
				return m, nil
			case tea.KeyDown:
				m.historyDown()
				m.updatePrompt()
				return m, nil
			}
		}
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
	b.WriteString(m.input.View())
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

func (m *promptModel) applyCompletion() bool {
	value := m.input.Value()
	if !strings.HasPrefix(value, "/") {
		return false
	}
	if strings.Contains(value, " ") {
		return false
	}
	if value != m.completeSeed || len(m.completions) == 0 {
		m.completeSeed = value
		m.completions = nil
		for _, cmd := range slashCommands {
			if strings.HasPrefix(cmd, value) {
				m.completions = append(m.completions, cmd)
			}
		}
		m.completeIdx = 0
	}
	if len(m.completions) == 0 {
		return false
	}
	if m.completeIdx >= len(m.completions) {
		m.completeIdx = 0
	}
	next := m.completions[m.completeIdx]
	m.completeIdx++
	m.input.SetValue(next)
	m.input.SetCursor(len([]rune(next)))
	return true
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
