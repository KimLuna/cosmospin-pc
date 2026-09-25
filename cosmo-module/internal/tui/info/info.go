// Package info is the artist info page: the current group's public card
// (title, fandom name), the member roster with aliases, official colors and
// units, and the group's SNS links. It is read-only; the group selection picks
// the artist shown.
package info

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"codeberg.org/djvu/cosmo-tui/internal/cosmo"
	"codeberg.org/djvu/cosmo-tui/internal/tui/textfmt"
	"codeberg.org/djvu/cosmo-tui/internal/tui/uimsg"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
	labelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	headStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

// snsOrder fixes the platform display order; unknown platforms follow after,
// alphabetically.
var snsOrder = []string{"twitter", "instagram", "youtube", "tiktok", "discord"}

type (
	loadedMsg struct{ artist cosmo.Artist }
	errMsg    struct{ err error }
)

// Model is the info page.
type Model struct {
	client *cosmo.Client
	group  string

	artist  cosmo.Artist
	loaded  bool
	loading bool
	err     error

	viewport viewport.Model
	ready    bool

	width, height int
}

// New builds the info page for a group and loads it via Init.
func New(client *cosmo.Client, group string) Model {
	return Model{client: client, group: group, loading: true}
}

func (m Model) Title() string { return "Info" }

func (m Model) Init() tea.Cmd { return m.load() }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		m.ready = true
		if m.loaded {
			m.viewport.SetContent(m.renderInfo())
		}
		return m, nil

	case uimsg.GroupChanged:
		m.group = msg.Group
		m.loaded, m.loading, m.err = false, true, nil
		return m, m.load()

	case loadedMsg:
		m.loading = false
		m.loaded = true
		m.err = nil
		m.artist = msg.artist
		if m.ready {
			m.viewport.SetContent(m.renderInfo())
			m.viewport.GotoTop()
		}
		return m, nil

	case errMsg:
		m.loading = false
		m.err = msg.err
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "r":
			m.loading, m.err = true, nil
			return m, m.load()
		case "g":
			m.viewport.GotoTop()
			return m, nil
		case "G":
			m.viewport.GotoBottom()
			return m, nil
		}
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}
	return m, nil
}

// layout sizes the scrollable viewport to the body area, reserving one line for
// the status/help line pinned at the bottom.
func (m *Model) layout() {
	bodyH := m.height - 1
	if bodyH < 1 {
		bodyH = 1
	}
	if m.ready {
		m.viewport.SetWidth(m.width)
		m.viewport.SetHeight(bodyH)
	} else {
		m.viewport = viewport.New(viewport.WithWidth(m.width), viewport.WithHeight(bodyH))
	}
}

func (m Model) load() tea.Cmd {
	client, group := m.client, m.group
	return func() tea.Msg {
		a, err := client.Artist(context.Background(), group)
		if err != nil {
			return errMsg{err}
		}
		return loadedMsg{artist: a}
	}
}

// View satisfies tea.Model. The layout itself is render; keeping it a plain
// string keeps the composing shell and this package's tests off tea.View.
func (m Model) View() tea.View { return tea.NewView(m.render()) }

func (m Model) render() string {
	if !m.ready {
		return statusStyle.Render("loading info…")
	}

	var body, status string
	switch {
	case m.err != nil:
		body = errStyle.Render("error: " + textfmt.Line(m.err.Error()))
		status = statusStyle.Render("r: retry")
	case !m.loaded:
		body = statusStyle.Render("loading info…")
	default:
		body = m.viewport.View()
		status = statusStyle.Render(m.statusLine())
	}

	bodyH := m.height - 1
	if bodyH < 1 {
		bodyH = 1
	}
	body = lipgloss.Place(m.width, bodyH, lipgloss.Left, lipgloss.Top, body)
	return body + "\n" + status
}

// statusLine is the pinned footer, prefixed with a scroll indicator only when
// the info actually overflows the viewport.
func (m Model) statusLine() string {
	hint := "r: reload"
	if m.viewport.TotalLineCount() > m.viewport.Height() {
		return fmt.Sprintf("%d%% · %s", int(m.viewport.ScrollPercent()*100), hint)
	}
	return hint
}

func (m Model) renderInfo() string {
	a := m.artist
	var b strings.Builder

	b.WriteString(titleStyle.Render(a.Title))
	b.WriteString("\n")
	b.WriteString(field("fandom", a.FandomName))

	members := append([]cosmo.ArtistMember(nil), a.Members...)
	sort.Slice(members, func(i, j int) bool { return members[i].Order < members[j].Order })

	b.WriteString("\n" + headStyle.Render("Members") + "\n")
	aliasW, nameW := columnWidths(members)
	for _, mem := range members {
		alias := mem.Alias
		if alias == mem.Name {
			alias = ""
		}
		line := "  "
		if aliasW > 0 {
			line += pad(alias, aliasW) + " "
		}
		line += pad(mem.Name, nameW) + " " + labelStyle.Render(mem.ColorHex)
		if mem.Units != "" {
			line += "  " + labelStyle.Render(mem.Units)
		}
		b.WriteString(line + "\n")
	}

	b.WriteString("\n" + headStyle.Render("Links") + "\n")
	if len(a.SNSLink) == 0 {
		b.WriteString("  " + labelStyle.Render("none") + "\n")
	}
	for _, platform := range snsPlatforms(a.SNSLink) {
		b.WriteString(fmt.Sprintf("  %-11s %s\n",
			platform, a.SNSLink[platform].Address))
	}

	return b.String()
}

// columnWidths sizes the alias and name columns to their longest entries,
// measured in display cells so a non-ASCII name can't skew the table (len
// counts bytes); aliasW is 0 when no member has a distinct alias, hiding the
// column.
func columnWidths(members []cosmo.ArtistMember) (aliasW, nameW int) {
	for _, mem := range members {
		if w := lipgloss.Width(mem.Alias); mem.Alias != mem.Name && w > aliasW {
			aliasW = w
		}
		if w := lipgloss.Width(mem.Name); w > nameW {
			nameW = w
		}
	}
	return aliasW, nameW
}

// pad right-pads s with spaces to w display cells. fmt's %-*s pads by rune
// count, which under-pads next to double-width characters.
func pad(s string, w int) string {
	if n := w - lipgloss.Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// snsPlatforms orders the link keys: known platforms first in snsOrder, then
// any others alphabetically.
func snsPlatforms(links map[string]cosmo.SNSLink) []string {
	var out []string
	for _, p := range snsOrder {
		if _, ok := links[p]; ok {
			out = append(out, p)
		}
	}
	var rest []string
	for p := range links {
		if !slices.Contains(snsOrder, p) {
			rest = append(rest, p)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// field renders one "label: value" metadata line, hiding empty values.
func field(label, value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return labelStyle.Render(label+": ") + value + "\n"
}
