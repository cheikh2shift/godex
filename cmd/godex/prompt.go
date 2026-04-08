//go:build !linux || (linux && !noclipboard)

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cheikh2shift/godex/internal/history"
	"github.com/cheikh2shift/godex/internal/hive"
)

var ErrPromptAborted = errors.New("prompt aborted")

var inputBoxStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("240")).
	Background(lipgloss.Color("236")).
	Padding(0, 1)

var commandTips = map[string]string{
	"/add-path":      "Add allowed MCP path: /add-path <filesys|url> <value>",
	"/remove-path":   "Remove allowed MCP path: /remove-path <filesys|url> <value>",
	"/paths":         "List allowed MCP paths",
	"/tools":         "List available MCP tools",
	"/clear-context": "Reset context counter and re-init LLM client",
	"/exit":          "Exit without saving",
	"/quit":          "Exit without saving",
	"/save":          "Save session and exit",
	"/save-exit":     "Save session and exit",
	"/kill":          "Kill a background process by PID or prune dead entries: /kill <pid> | /kill --prune",
	"/killbg":        "Kill all background processes",
	"/bg":            "List background processes",
	"/clear":         "Clear the screen",
	"/help":          "Show help for commands",
	"/commit":        "Commit chat history: /commit <message>",
	"/commit-pull":   "Restore committed history: /commit-pull <commit-ref>",
	"/commit-merge":  "Merge committed history: /commit-merge <commit-ref>",
	"/commit-search": "Search commits by ref/message: /commit-search <query>",
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
	historyDB       *history.HistoryDB
	wd              string
	searchMode      bool
	searchInput     textinput.Model
	searchIndex     int
	searchResults   []string
	statusMessage   string
	commitList      []string
	statusCh        <-chan string
	delegateCh      <-chan hive.HiveStats
	delegateCount   int
	delegateLatest  string
	statusHidden    bool
	Cancelled       chan struct{}
}

func newPromptModel(prompt string, history []string, modelName string, contextUsage int, contextLimit int, historyDB *history.HistoryDB, wd string, statusCh <-chan string, delegateCh <-chan hive.HiveStats) promptModel {
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
		historyDB:    historyDB,
		wd:           wd,
		statusCh:     statusCh,
		delegateCh:   delegateCh,
		Cancelled:    make(chan struct{}),
	}
}

func readPrompt(prompt string, history []string, modelName string, contextUsage int, contextLimit int, historyDB *history.HistoryDB, wd string, statusCh <-chan string, delegateCh <-chan hive.HiveStats, cancelCh <-chan struct{}) (string, error) {
	m := newPromptModel(prompt, history, modelName, contextUsage, contextLimit, historyDB, wd, statusCh, delegateCh)
	p := tea.NewProgram(m)

	type result struct {
		model tea.Model
		err   error
	}
	resultCh := make(chan result, 1)

	go func() {
		finalModel, err := p.Run()
		resultCh <- result{model: finalModel, err: err}
	}()

	select {
	case r := <-resultCh:
		if r.err != nil {
			return "", r.err
		}
		result := r.model.(promptModel)
		if result.aborted {
			return "", ErrPromptAborted
		}
		if !result.submitted {
			return "", nil
		}
		return result.value(), nil
	case <-cancelCh:
		p.Kill()
		return "", ErrPromptAborted
	}
}

func (m promptModel) Init() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink, tea.EnableBracketedPaste}
	if m.statusCh != nil {
		cmds = append(cmds, waitStatusCmd(m.statusCh))
	}
	if m.delegateCh != nil {
		cmds = append(cmds, waitDelegateCmd(m.delegateCh))
	}
	return tea.Batch(cmds...)
}

func (m promptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			if m.searchMode {
				m.exitSearchMode()
				return m, nil
			}
			m.aborted = true
			return m, tea.Quit
		}
		if m.statusHidden {
			return m, nil
		}
		if (m.statusMessage != "" || len(m.commitList) > 0) && msg.Type != tea.KeyTab {
			m.statusMessage = ""
			m.commitList = nil
		}
		if msg.Type == tea.KeyCtrlR {
			if m.searchMode {
				m.searchNext()
			} else {
				m.enterSearchMode()
			}
			return m, nil
		}
		if msg.Type == tea.KeyEsc {
			if m.searchMode {
				m.exitSearchMode()
				return m, nil
			}
		}
		if msg.Type == tea.KeyTab {
			m.handleTab()
			return m, nil
		}
		if msg.Type == tea.KeyEnter {
			if m.searchMode {
				m.selectSearchResult()
				return m, nil
			}
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
			if m.searchMode {
				var cmd tea.Cmd
				m.searchInput, cmd = m.searchInput.Update(msg)
				m.performSearch()
				return m, cmd
			}
		}

		if !m.multiline && len(m.lines) == 0 && !m.searchMode {
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

		if m.searchMode {
			switch msg.Type {
			case tea.KeyUp:
				m.searchPrev()
				return m, nil
			case tea.KeyDown:
				m.searchNext()
				return m, nil
			}
		}
	}
	if msg, ok := msg.(statusMsg); ok {
		m.applyStatusMessage(msg)
		return m, waitStatusCmd(m.statusCh)
	}
	if msg, ok := msg.(delegateMsg); ok {
		m.delegateCount = msg.Count
		m.delegateLatest = msg.Latest
		return m, waitDelegateCmd(m.delegateCh)
	}
	if msg, ok := msg.(tea.KeyMsg); ok {
		if msg.Type == tea.KeyCtrlV || (msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == 22) {
			m, cmd := m.handleImagePaste("")
			return m, cmd
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
	if m.statusHidden {
		b.WriteString("\n")
	} else {
		b.WriteString(boxStyle.Render(m.input.View()))
	}

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
		} else if m.contextUsage > 0 {
			rightContent = fmt.Sprintf("tokens: %s", formatNumber(m.contextUsage))
		}

		grayStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

		if leftContent != "" {
			b.WriteString(grayStyle.Render(leftContent))
		}
		if rightContent != "" {
			if leftContent != "" {
				b.WriteString(" ")
			}
			delegateInfo := ""
			if m.delegateCount > 0 || strings.TrimSpace(m.delegateLatest) != "" {
				latest := promptTruncateRunes(m.delegateLatest, 42)
				if latest != "" {
					delegateInfo = fmt.Sprintf("Hive:%d %s", m.delegateCount, latest)
				} else {
					delegateInfo = fmt.Sprintf("Hive:%d", m.delegateCount)
				}
			}
			if delegateInfo != "" {
				rightContent = rightContent + " | " + delegateInfo
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
	if m.searchMode {
		b.WriteByte('\n')
		searchPrompt := lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Bold(true).Render("(reverse-i-search)`': ")
		b.WriteString(boxStyle.Width(max(0, m.width-4)).Render(searchPrompt + m.searchInput.View()))
		b.WriteByte('\n')
		if len(m.searchResults) > 0 {
			searchStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("75"))
			selectedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("82")).Bold(true)
			for i, result := range m.searchResults {
				display := result
				if len(display) > 200 {
					display = display[:200] + "..."
				}
				prefix := "  "
				if i == m.searchIndex {
					prefix = " > "
					b.WriteString(selectedStyle.Render(prefix + display))
				} else {
					b.WriteString(searchStyle.Render(prefix + display))
				}
				b.WriteByte('\n')
			}
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("  ↑↓ navigate  ↵ select  esc cancel"))
		} else if m.searchInput.Value() != "" {
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("  No matches found"))
		}
	}
	if len(m.commitList) > 0 {
		b.WriteByte('\n')
		listStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("75"))
		for _, line := range m.commitList {
			b.WriteString(listStyle.Render(line))
			b.WriteByte('\n')
		}
	}
	if m.statusMessage != "" {
		b.WriteByte('\n')
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(m.statusMessage))
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

func (m *promptModel) handleImagePaste(_ string) (*promptModel, tea.Cmd) {
	imgData := readClipboardImage()
	if len(imgData) == 0 {
		textData := readClipboardText()
		if len(textData) > 0 {
			m.handlePaste(string(textData))
		}
		return m, nil
	}

	tmpDir := os.TempDir()
	tmpFile := filepath.Join(tmpDir, "godex-paste-"+fmt.Sprintf("%d", time.Now().UnixNano())+".png")
	err := os.WriteFile(tmpFile, imgData, 0644)
	if err != nil {
		return m, nil
	}

	m.insertAtCursor(tmpFile)
	return m, nil
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
	if strings.HasPrefix(value, "/commit-pull") {
		if m.handleCommitPullTab(value) {
			return
		}
	}
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
	common := commonPrefix(m.completions)
	if common != "" && common != value {
		m.input.SetValue(common)
		m.input.SetCursor(len([]rune(common)))
		if len(m.completions) == 1 {
			m.resetCompletion()
			return
		}
		m.showCompletions = true
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

func (m *promptModel) handleCommitPullTab(value string) bool {
	if m.historyDB == nil {
		return false
	}
	prefix := strings.TrimSpace(strings.TrimPrefix(value, "/commit-pull"))
	if strings.Contains(value, " ") || value == "/commit-pull" {
		commits, err := m.historyDB.FindCommitsByRefPrefix(m.wd, prefix, 5)
		if err != nil || len(commits) == 0 {
			return false
		}
		if len(commits) == 1 && prefix != "" {
			m.input.SetValue("/commit-pull " + commits[0].Ref)
			m.input.SetCursor(len([]rune(m.input.Value())))
			m.resetCompletion()
			return true
		}
		m.commitList = nil
		for _, c := range commits {
			ref := c.Ref
			if len(ref) > 15 {
				ref = ref[:15]
			}
			line := fmt.Sprintf("%s  %s", ref, promptTruncateRunes(c.Message, 129))
			m.commitList = append(m.commitList, line)
		}
		m.resetCompletion()
		return true
	}
	return false
}

func promptTruncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}

func promptFormatCommitDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04")
}

func commonPrefix(values []string) string {
	if len(values) == 0 {
		return ""
	}
	prefix := values[0]
	for _, v := range values[1:] {
		prefix = commonPrefixPair(prefix, v)
		if prefix == "" {
			return ""
		}
	}
	return prefix
}

func commonPrefixPair(a, b string) string {
	ar := []rune(a)
	br := []rune(b)
	n := len(ar)
	if len(br) < n {
		n = len(br)
	}
	i := 0
	for i < n && ar[i] == br[i] {
		i++
	}
	return string(ar[:i])
}

func (m *promptModel) enterSearchMode() {
	m.searchMode = true
	m.searchIndex = 0
	m.searchResults = nil
	m.historyDraft = m.input.Value()

	ti := textinput.New()
	ti.Prompt = ""
	ti.Focus()
	ti.CharLimit = 0
	m.searchInput = ti
}

func (m *promptModel) exitSearchMode() {
	m.searchMode = false
	m.searchResults = nil
	m.searchIndex = 0
	m.searchInput = textinput.Model{}
	m.input.SetValue(m.historyDraft)
	m.input.SetCursor(len([]rune(m.historyDraft)))
}

func (m *promptModel) performSearch() {
	query := m.searchInput.Value()
	if m.historyDB == nil || query == "" {
		m.searchResults = nil
		return
	}

	results, err := m.historyDB.Search(m.wd, query, 5)
	if err != nil {
		m.searchResults = nil
		return
	}

	m.searchResults = results
	if m.searchIndex >= len(m.searchResults) {
		m.searchIndex = 0
	}
}

func (m *promptModel) searchNext() {
	if len(m.searchResults) == 0 {
		return
	}
	m.searchIndex++
	if m.searchIndex >= len(m.searchResults) {
		m.searchIndex = 0
	}
}

func (m *promptModel) searchPrev() {
	if len(m.searchResults) == 0 {
		return
	}
	m.searchIndex--
	if m.searchIndex < 0 {
		m.searchIndex = len(m.searchResults) - 1
	}
}

func (m *promptModel) selectSearchResult() {
	if len(m.searchResults) > 0 && m.searchIndex < len(m.searchResults) {
		m.input.SetValue(m.searchResults[m.searchIndex])
		m.input.SetCursor(len([]rune(m.searchResults[m.searchIndex])))
	}
	m.searchMode = false
	m.searchResults = nil
	m.searchIndex = 0
	m.searchInput = textinput.Model{}
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

type selectOption struct {
	label string
	desc  string
}

type statusMsg string

type delegateMsg struct {
	Count  int
	Latest string
}

func (m *promptModel) applyStatusMessage(msg statusMsg) {
	text := string(msg)
	switch {
	case strings.HasPrefix(text, hive.StatusHidePrefix):
		m.statusHidden = true
		m.statusMessage = strings.TrimPrefix(text, hive.StatusHidePrefix)
	case strings.HasPrefix(text, hive.StatusShowPrefix):
		m.statusHidden = false
		m.statusMessage = strings.TrimPrefix(text, hive.StatusShowPrefix)
	default:
		m.statusHidden = false
		m.statusMessage = text
	}
}

type selectModel struct {
	options []selectOption
	cursor  int
	done    bool
	result  int
}

func newSelectModel(options []selectOption) selectModel {
	return selectModel{
		options: options,
		cursor:  0,
		done:    false,
		result:  -1,
	}
}

func (m selectModel) Init() tea.Cmd {
	return nil
}

func (m selectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyUp:
			if m.cursor > 0 {
				m.cursor--
			}
		case tea.KeyDown:
			if m.cursor < len(m.options)-1 {
				m.cursor++
			}
		case tea.KeyEnter:
			m.result = m.cursor
			m.done = true
			return m, tea.Quit
		case tea.KeyCtrlC, tea.KeyEsc:
			m.result = -1
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m selectModel) View() string {
	var b strings.Builder
	b.WriteString("\n")
	for i, opt := range m.options {
		if i == m.cursor {
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Bold(true).Render("  > " + opt.label))
		} else {
			b.WriteString("    " + opt.label)
		}
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("    "+opt.desc) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("  ↑↓ select  ↵ confirm  esc cancel"))
	return b.String()
}

func selectOptionPrompt(title string, options []selectOption) int {
	m := newSelectModel(options)
	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return -1
	}
	result := finalModel.(selectModel)
	return result.result
}

func waitStatusCmd(ch <-chan string) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return statusMsg(msg)
	}
}

func waitDelegateCmd(ch <-chan hive.HiveStats) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return delegateMsg{Count: msg.DelegatedCount, Latest: msg.LatestCommand}
	}
}

type commitRow struct {
	primary   string
	secondary string
}

type commitSelectModel struct {
	rows   []commitRow
	cursor int
	done   bool
	result int
}

func newCommitSelectModel(rows []commitRow) commitSelectModel {
	return commitSelectModel{
		rows:   rows,
		cursor: 0,
		done:   false,
		result: -1,
	}
}

func (m commitSelectModel) Init() tea.Cmd {
	return nil
}

func (m commitSelectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyUp:
			if m.cursor > 0 {
				m.cursor--
			}
		case tea.KeyDown:
			if m.cursor < len(m.rows)-1 {
				m.cursor++
			}
		case tea.KeyEnter:
			m.result = m.cursor
			m.done = true
			return m, tea.Quit
		case tea.KeyCtrlC, tea.KeyEsc:
			m.result = -1
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m commitSelectModel) View() string {
	var b strings.Builder
	b.WriteString("\n")
	if len(m.rows) > 0 {
		searchStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("75"))
		selectedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("82")).Bold(true)
		subStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
		for i, row := range m.rows {
			prefix := "  "
			if i == m.cursor {
				prefix = " > "
				b.WriteString(selectedStyle.Render(prefix + row.primary))
			} else {
				b.WriteString(searchStyle.Render(prefix + row.primary))
			}
			b.WriteByte('\n')
			if row.secondary != "" {
				b.WriteString(subStyle.Render("    " + row.secondary))
				b.WriteByte('\n')
			}
		}
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("  ↑↓ navigate  ↵ restore  esc cancel"))
	} else {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("  No matches found"))
	}
	return b.String()
}

func commitSelectPrompt(rows []commitRow) int {
	m := newCommitSelectModel(rows)
	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return -1
	}
	result := finalModel.(commitSelectModel)
	return result.result
}
