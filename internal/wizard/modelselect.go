package wizard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cheikh2shift/godex/modelquery"
)

type searchResultMsg struct {
	models []modelquery.Model
}

type modelSelectModel struct {
	searchCancel   context.CancelFunc
	provider       modelquery.Provider
	result         string
	defaultValue   string
	currentQuery   string
	results        []modelquery.Model
	textInput      textinput.Model
	cursor         int
	searchDebounce time.Duration
	loading        bool
	done           bool
	pendingSearch  bool
}

type modelSelectState struct {
	results []modelquery.Model
	cursor  int
	loading bool
}

func (m *modelSelectModel) getState() modelSelectState {
	return modelSelectState{
		results: m.results,
		cursor:  m.cursor,
		loading: m.loading,
	}
}

func newModelSelectModel(provider modelquery.Provider, defaultValue string) modelSelectModel {
	ti := textinput.New()
	ti.Placeholder = "Type to search models..."
	ti.Focus()
	if defaultValue != "" {
		ti.Prompt = fmt.Sprintf("Model [%s]: ", defaultValue)
	} else {
		ti.Prompt = "Model: "
	}

	return modelSelectModel{
		textInput:      ti,
		results:        nil,
		cursor:         -1,
		provider:       provider,
		searchDebounce: 200 * time.Millisecond,
		defaultValue:   defaultValue,
	}
}

func (m *modelSelectModel) Init() tea.Cmd {
	return tea.Batch(
		textinput.Blink,
		searchModelsCmd(m.provider, ""),
	)
}

func (m *modelSelectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case searchResultMsg:
		m.loading = false
		m.pendingSearch = false
		m.results = msg.models
		if len(m.results) > 6 {
			m.results = m.results[:6]
		}
		if len(m.results) > 0 && m.cursor < 0 {
			m.cursor = 0
		} else if len(m.results) == 0 {
			m.cursor = -1
		}

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyUp:
			if len(m.results) > 0 && m.cursor > 0 {
				m.cursor--
			}
		case tea.KeyDown:
			if len(m.results) > 0 && m.cursor < len(m.results)-1 {
				m.cursor++
			}
		case tea.KeyEnter:
			if m.cursor >= 0 && m.cursor < len(m.results) {
				m.result = m.results[m.cursor].ID
				m.done = true
				return m, tea.Quit
			}
			m.result = m.textInput.Value()
			if m.result == "" {
				m.result = m.defaultValue
			}
			m.done = true
			return m, tea.Quit
		case tea.KeyEsc:
			m.result = m.defaultValue
			m.done = true
			return m, tea.Quit
		case tea.KeyCtrlC:
			m.result = ""
			m.done = true
			return m, tea.Quit
		case tea.KeyRunes:
			m.textInput, cmd = m.textInput.Update(msg)
			m.cursor = -1
			m.loading = true
			m.results = nil
			if m.searchCancel != nil {
				m.searchCancel()
			}
			m.currentQuery = m.textInput.Value()
			m.pendingSearch = true
			return m, tea.Batch(cmd, searchModelsCmd(m.provider, m.currentQuery))
		default:
			var textCmd tea.Cmd
			m.textInput, textCmd = m.textInput.Update(msg)
			return m, textCmd
		}
	}

	return m, cmd
}

func searchModelsCmd(provider modelquery.Provider, query string) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(200 * time.Millisecond)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		models, err := modelquery.SearchModels(ctx, provider, query)
		if err != nil || len(models) == 0 {
			return searchResultMsg{models: nil}
		}
		return searchResultMsg{models: models}
	}
}

func formatContextLen(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%dK", n/1_000)
	}
	if n == 0 {
		return "?"
	}
	return fmt.Sprintf("%d", n)
}

func (m *modelSelectModel) View() string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(m.textInput.View())
	b.WriteString("\n")

	state := m.getState()

	if m.loading {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("  Loading..."))
		b.WriteString("\n")
	}

	if len(state.results) > 0 {
		for i, model := range state.results {
			prefix := "  "
			if i == state.cursor {
				prefix = " > "
				b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Bold(true).Render(prefix + model.ID))
			} else {
				b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(prefix + model.ID))
			}
			b.WriteString(" ")
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("(" + formatContextLen(model.ContextLen) + ")"))
			b.WriteString("\n")
			desc := model.Description
			if len(desc) > 60 {
				desc = desc[:57] + "..."
			}
			descStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
			if i != state.cursor {
				descStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("237"))
			}
			b.WriteString(descStyle.Render("    " + desc))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("  ↑↓ navigate  ↵ select  Esc cancel"))
	return b.String()
}

func ModelSelectPrompt(provider modelquery.Provider, defaultValue string) (string, int) {
	m := newModelSelectModel(provider, defaultValue)
	p := tea.NewProgram(&m)
	finalModel, err := p.Run()
	if err != nil {
		return defaultValue, 0
	}

	result := finalModel.(*modelSelectModel)
	if result.result == "" {
		return defaultValue, 0
	}

	state := result.getState()
	for _, model := range state.results {
		if model.ID == result.result {
			return model.ID, model.ContextLen
		}
	}
	return result.result, 0
}
