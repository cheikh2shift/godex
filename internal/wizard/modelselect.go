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

type modelSelectModel struct {
	textInput      textinput.Model
	results        []modelquery.Model
	cursor         int
	loading        bool
	provider       modelquery.Provider
	searchDebounce time.Duration
	done           bool
	result         string
	defaultValue   string
	resultsCh      chan []modelquery.Model
	searchCancel   context.CancelFunc
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
		searchDebounce: 300 * time.Millisecond,
		defaultValue:   defaultValue,
		resultsCh:      make(chan []modelquery.Model),
	}
}

func (m *modelSelectModel) Init() tea.Cmd {
	if m.defaultValue != "" {
		go m.searchDebounced(m.defaultValue)
	}
	return textinput.Blink
}

func (m *modelSelectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case []modelquery.Model:
		m.loading = false
		m.results = msg
		if len(m.results) > 6 {
			m.results = m.results[:6]
		}
		if len(m.results) > 0 {
			m.cursor = 0
		} else {
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
			go m.searchDebounced(m.textInput.Value())
			return m, cmd
		default:
			var textCmd tea.Cmd
			m.textInput, textCmd = m.textInput.Update(msg)
			return m, textCmd
		}
	}

	return m, cmd
}

func (m *modelSelectModel) searchDebounced(query string) {
	if strings.TrimSpace(query) == "" {
		select {
		case m.resultsCh <- nil:
		case <-time.After(time.Second):
		}
		return
	}

	time.Sleep(m.searchDebounce)

	ctx, cancel := context.WithCancel(context.Background())
	m.searchCancel = cancel
	defer cancel()

	models, err := modelquery.SearchModels(ctx, m.provider, query)
	if err != nil {
		select {
		case m.resultsCh <- nil:
		case <-ctx.Done():
		}
		return
	}

	select {
	case m.resultsCh <- models:
	case <-ctx.Done():
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

	if m.loading && state.results == nil {
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

	searchDone := make(chan struct{})
	go func() {
		for models := range m.resultsCh {
			m.Update(models)
		}
		close(searchDone)
	}()

	p := tea.NewProgram(&m)
	finalModel, err := p.Run()
	if err != nil {
		return defaultValue, 0
	}

	if m.searchCancel != nil {
		m.searchCancel()
	}
	close(m.resultsCh)
	<-searchDone

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
