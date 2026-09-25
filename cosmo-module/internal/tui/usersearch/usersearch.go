// Package usersearch is a reusable nickname-search widget: a text box that runs
// GET /users/search and a scrolling results list to pick a match from. It is a
// sub-model, not a page - a parent embeds it, drives its focus, and reacts to
// the Action its Update returns (the user cancelled out, or picked a result).
// The profile page uses it to open another user's profile; the objekt send flow
// reuses it as a recipient picker.
//
// The widget owns two phases internally:
//
//	input   : the nickname box. enter runs the search, esc cancels (ActionCancel)
//	results : the matches. descend picks Selected() (ActionSelect), ascend edits
//
// Navigation follows the app-wide motions (see internal/tui/keynav).
package usersearch

import (
	"context"
	"fmt"
	"strings"

	"codeberg.org/djvu/cosmo-tui/internal/cosmo"
	"codeberg.org/djvu/cosmo-tui/internal/tui/keynav"
	"codeberg.org/djvu/cosmo-tui/internal/tui/style"
	"codeberg.org/djvu/cosmo-tui/internal/tui/textfmt"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

var (
	headStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	labelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	cursorStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
	selRowStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
)

// Action is what the widget asks the parent to do after an Update.
type Action int

const (
	ActionNone   Action = iota // stay in the widget
	ActionCancel               // the user backed out of the search box
	ActionSelect               // the user picked Selected()
)

// Config customizes the widget's chrome so different callers (profile search,
// recipient picker) can label it appropriately.
type Config struct {
	Title       string // heading line, e.g. "Search users"
	Subtitle    string // help line under the heading in the input phase (optional)
	Prompt      string // text-box prompt and results echo label, e.g. "search: "
	Placeholder string // text-box placeholder
	CancelHint  string // status hint for esc in the input phase, e.g. "esc: back"
}

type phase int

const (
	phaseInput phase = iota
	phaseResults
)

// SearchedMsg is the async result of a search, delivered back through the
// parent's Update (the parent forwards it to the embedded widget). It is
// exported so the parent can route it and tests can inject one.
type SearchedMsg struct {
	Query   string
	Results []cosmo.UserSearchResult
	Err     error
}

// Model is the search widget.
type Model struct {
	client *cosmo.Client
	cfg    Config

	phase phase
	input textinput.Model

	query     string
	results   []cosmo.UserSearchResult
	cursor    int
	offset    int // first visible result row (list scroll)
	searching bool
	err       error

	height int // body rows available, for results windowing
}

// New builds a search widget. It starts in the input phase, unfocused; call
// Start to focus it fresh.
func New(client *cosmo.Client, cfg Config) Model {
	if cfg.Prompt == "" {
		cfg.Prompt = "search: "
	}
	in := textinput.New()
	in.Placeholder = cfg.Placeholder
	in.Prompt = cfg.Prompt
	style.PlainInput(&in)
	return Model{client: client, cfg: cfg, input: in}
}

// Start enters the widget fresh: input phase, cleared query, focused. Returns
// the cursor-blink command.
func (m *Model) Start() tea.Cmd {
	m.phase = phaseInput
	m.query = ""
	m.results = nil
	m.cursor, m.offset = 0, 0
	m.searching = false
	m.err = nil
	m.input.SetValue("")
	m.input.Focus()
	return textinput.Blink
}

// Resume re-focuses the widget on its existing results without re-running the
// search (used when a parent backs out of a viewed result into the list).
func (m *Model) Resume() {
	m.phase = phaseResults
	m.input.Blur()
}

// SetSize sets the body area the widget may draw into and keeps the
// highlighted result on-screen. The width goes to the nickname box, which needs
// one to render its placeholder at all (see style.FitPlaceholder); the box has
// its line to itself, so padding to the full width costs nothing.
func (m *Model) SetSize(w, h int) {
	m.height = h
	if w > 0 {
		m.input.SetWidth(w)
	}
	m.clampOffset()
}

// AcceptsText reports whether the input box is capturing keystrokes, so the
// shell yields plain-letter keys to it.
func (m Model) AcceptsText() bool { return m.phase == phaseInput }

// Selected returns the highlighted result, if any.
func (m Model) Selected() (cosmo.UserSearchResult, bool) {
	if m.cursor < 0 || m.cursor >= len(m.results) {
		return cosmo.UserSearchResult{}, false
	}
	return m.results[m.cursor], true
}

// Update advances the widget and reports whether the parent should act.
func (m Model) Update(msg tea.Msg) (Model, Action, tea.Cmd) {
	switch msg := msg.(type) {
	case SearchedMsg:
		m.searching = false
		if msg.Query != m.query {
			return m, ActionNone, nil // a stale response for an earlier query
		}
		m.err = msg.Err
		m.results = msg.Results
		m.cursor, m.offset = 0, 0
		return m, ActionNone, nil

	case tea.PasteMsg:
		// Pasted text. The box may have been left while a ctrl+v read ran (a
		// search run, the widget cancelled), and then there is nowhere to put it.
		if m.phase != phaseInput {
			return m, ActionNone, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, ActionNone, cmd

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, ActionNone, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (Model, Action, tea.Cmd) {
	if m.phase == phaseInput {
		switch msg.String() {
		case "esc":
			return m, ActionCancel, nil
		case "enter":
			q := strings.TrimSpace(m.input.Value())
			if q == "" {
				return m, ActionNone, nil
			}
			m.phase = phaseResults
			m.input.Blur()
			m.query = q
			m.searching = true
			m.err = nil
			m.results = nil
			return m, ActionNone, m.search(q)
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, ActionNone, cmd
	}

	// results phase
	if keynav.Ascend(msg) {
		m.phase = phaseInput
		m.input.Focus()
		return m, ActionNone, textinput.Blink
	}
	if keynav.Descend(msg) {
		if len(m.results) == 0 {
			return m, ActionNone, nil
		}
		return m, ActionSelect, nil
	}
	switch msg.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.clampOffset()
		}
	case "down", "j":
		if m.cursor < len(m.results)-1 {
			m.cursor++
			m.clampOffset()
		}
	}
	return m, ActionNone, nil
}

func (m Model) search(q string) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		res, err := client.SearchUsers(context.Background(), q)
		return SearchedMsg{Query: q, Results: res, Err: err}
	}
}

// resultsCapacity is how many match rows fit below the heading/query/blank
// lines that precede the list.
func (m Model) resultsCapacity() int {
	if c := m.height - 3; c > 0 {
		return c
	}
	return 1
}

// clampOffset scrolls the result window so the highlighted row stays visible.
func (m *Model) clampOffset() {
	capacity := m.resultsCapacity()
	if m.cursor < m.offset {
		m.offset = m.cursor
	} else if m.cursor >= m.offset+capacity {
		m.offset = m.cursor - capacity + 1
	}
}

// View renders the widget body: the input box, or the query echo plus results.
func (m Model) View() string {
	var b strings.Builder
	b.WriteString(headStyle.Render(m.cfg.Title) + "\n")

	if m.phase == phaseInput {
		if m.cfg.Subtitle != "" {
			b.WriteString(labelStyle.Render(m.cfg.Subtitle) + "\n")
		}
		b.WriteString("\n" + m.input.View())
		return b.String()
	}

	b.WriteString(labelStyle.Render(m.cfg.Prompt) + textfmt.Line(textfmt.Normalize(m.query)) + "\n\n")
	switch {
	case m.searching:
		b.WriteString(statusStyle.Render("searching…"))
	case m.err != nil:
		b.WriteString(errStyle.Render("error: " + textfmt.Line(m.err.Error())))
	case len(m.results) == 0:
		b.WriteString(statusStyle.Render(fmt.Sprintf("no users found matching %q", m.query)))
	default:
		b.WriteString(m.renderResults())
	}
	return b.String()
}

// renderResults draws the match list as a scrolling window that keeps the
// highlighted row visible.
func (m Model) renderResults() string {
	capacity := m.resultsCapacity()
	offset := m.offset
	if offset > m.cursor {
		offset = m.cursor
	}

	var b strings.Builder
	end := offset + capacity
	if end > len(m.results) {
		end = len(m.results)
	}
	for i := offset; i < end; i++ {
		// Normalize as well as collapse: Line only folds whitespace, and a
		// nickname is arbitrary user text that can carry width-mismatched
		// characters the terminal draws wider than lipgloss measures (see
		// textfmt), which would overflow the row.
		name := textfmt.Line(textfmt.Normalize(m.results[i].Nickname))
		if i == m.cursor {
			b.WriteString(cursorStyle.Render("> ") + selRowStyle.Render(name))
		} else {
			b.WriteString("  " + name)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// StatusHint is the widget's one-line status hint for the parent's status slot.
func (m Model) StatusHint() string {
	if m.phase == phaseInput {
		hint := "type a nickname · enter: search"
		if m.cfg.CancelHint != "" {
			hint += " · " + m.cfg.CancelHint
		}
		return hint
	}
	if len(m.results) == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d", m.cursor+1, len(m.results))
}
