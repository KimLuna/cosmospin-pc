// Package profile is the account profile page. By default it shows the current
// user's per-artist card (nickname, bio, fandom, wallet, streak) plus headline
// activity stats and an owned-objekt breakdown. Pressing "s" opens a nickname
// search: type a nickname, Enter runs the search, then pick a match from the
// results list to view that user's public profile (the same card, minus the
// private wallet totals and any sections that user has hidden). It is read-only;
// the group selection picks the artist the profile is shown for.
//
// Navigation follows the app-wide motions (see internal/tui/keynav); only the
// keys unique to this page are listed here.
//
//	self      : the logged-in user's own profile (default). s: search, r: reload
//	searching : the shared usersearch widget (nickname box -> matches). descend
//	            opens the highlighted user; esc from the box returns to self
//	user      : another user's profile. s: new search, r: reload, ascend: results
package profile

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"codeberg.org/djvu/cosmo-tui/internal/cosmo"
	"codeberg.org/djvu/cosmo-tui/internal/external"
	"codeberg.org/djvu/cosmo-tui/internal/tui/keynav"
	"codeberg.org/djvu/cosmo-tui/internal/tui/style"
	"codeberg.org/djvu/cosmo-tui/internal/tui/textfmt"
	"codeberg.org/djvu/cosmo-tui/internal/tui/uimsg"
	"codeberg.org/djvu/cosmo-tui/internal/tui/usersearch"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
	labelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	bioStyle    = lipgloss.NewStyle().Italic(true)
	headStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

// mode is the page's current interaction state (see the package doc).
type mode int

const (
	modeSelf      mode = iota
	modeSearching      // the embedded usersearch widget is driving
	modeUser
)

type (
	loadedMsg     struct{ profile cosmo.Profile }
	errMsg        struct{ err error }
	userLoadedMsg struct{ profile cosmo.Profile }
	userErrMsg    struct{ err error }
)

// Model is the profile page.
type Model struct {
	client *cosmo.Client
	group  string

	mode mode

	// own profile
	profile cosmo.Profile
	loaded  bool
	loading bool
	err     error

	// nickname search -> other user (shared widget)
	search usersearch.Model

	// viewed user
	user        cosmo.Profile
	userID      int
	userLoaded  bool
	userLoading bool
	userErr     error

	viewport viewport.Model
	ready    bool

	width, height int
}

// New builds the profile page for a group and loads it via Init.
func New(client *cosmo.Client, group string) Model {
	search := usersearch.New(client, usersearch.Config{
		Title:       "Search users",
		Subtitle:    "Find another user's profile.",
		Prompt:      "search: ",
		Placeholder: "nickname…",
		CancelHint:  "esc: back",
	})
	return Model{client: client, group: group, loading: true, search: search}
}

func (m Model) Title() string { return "Profile" }

func (m Model) Init() tea.Cmd { return m.load() }

// AcceptsText reports whether the nickname search box is capturing keystrokes,
// so the shell yields plain-letter keys to it.
func (m Model) AcceptsText() bool { return m.mode == modeSearching && m.search.AcceptsText() }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		m.ready = true
		m.search.SetSize(m.width, m.bodyHeight()) // a shorter window can hide the highlighted result
		m.refreshViewport()
		return m, nil

	case uimsg.GroupChanged:
		// A viewed user's profile is group-specific, so drop back to our own.
		m.group = msg.Group
		m.mode = modeSelf
		m.resetSearch()
		m.loaded, m.loading, m.err = false, true, nil
		return m, m.load()

	case usersearch.SearchedMsg:
		// The async search result arrives via the shell broadcast, which reaches
		// every page — and the objekt tab's recipient picker is the same widget
		// answering with the same message type. Only take it while this page's
		// own search is the one running: the widget's stale guard is the query
		// string, so a recipient lookup for a nickname searched here earlier
		// would otherwise land in this widget and replace the results the user
		// backs out to (see Resume).
		if m.mode != modeSearching {
			return m, nil
		}
		var cmd tea.Cmd
		m.search, _, cmd = m.search.Update(msg)
		return m, cmd

	case tea.PasteMsg:
		// Pasted text belongs to the nickname box, and only while that box is
		// what the page is showing.
		if m.mode != modeSearching {
			return m, nil
		}
		var cmd tea.Cmd
		m.search, _, cmd = m.search.Update(msg)
		return m, cmd

	case loadedMsg:
		m.loading = false
		m.loaded = true
		m.err = nil
		m.profile = msg.profile
		if m.ready && m.mode == modeSelf {
			m.showProfile(m.profile)
		}
		return m, nil

	case errMsg:
		m.loading = false
		m.err = msg.err
		return m, nil

	case userLoadedMsg:
		m.userLoading = false
		m.userLoaded = true
		m.userErr = nil
		m.user = msg.profile
		if m.ready && m.mode == modeUser {
			m.showProfile(m.user)
		}
		return m, nil

	case userErrMsg:
		m.userLoading = false
		m.userErr = msg.err
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeSearching:
		var (
			act usersearch.Action
			cmd tea.Cmd
		)
		m.search, act, cmd = m.search.Update(msg)
		switch act {
		case usersearch.ActionCancel:
			m.mode = modeSelf
			m.showProfile(m.profile)
			return m, nil
		case usersearch.ActionSelect:
			if sel, ok := m.search.Selected(); ok {
				m.mode = modeUser
				m.userID = sel.ID
				m.userLoaded, m.userLoading, m.userErr = false, true, nil
				return m, m.loadUser(sel.ID)
			}
		}
		return m, cmd

	case modeUser:
		if keynav.Ascend(msg) {
			// Back out to the results list the pick came from.
			m.mode = modeSearching
			m.search.Resume()
			return m, nil
		}
		switch msg.String() {
		case "s":
			return m, m.enterSearch()
		case "r":
			if !m.userLoading {
				m.userLoaded, m.userLoading, m.userErr = false, true, nil
				return m, m.loadUser(m.userID)
			}
			return m, nil
		case "a":
			return m, openWebProfile("apollo.cafe", m.user.Nickname)
		case "o":
			return m, openWebProfile("objekt.top", m.user.Nickname)
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

	default: // modeSelf
		switch msg.String() {
		case "s":
			return m, m.enterSearch()
		case "r":
			m.loading, m.err = true, nil
			return m, m.load()
		case "a":
			return m, openWebProfile("apollo.cafe", m.profile.Nickname)
		case "o":
			return m, openWebProfile("objekt.top", m.profile.Nickname)
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
}

// enterSearch switches into the nickname search box with a cleared query.
func (m *Model) enterSearch() tea.Cmd {
	m.mode = modeSearching
	m.userLoaded, m.userLoading, m.userErr = false, false, nil
	cmd := m.search.Start()
	m.search.SetSize(m.width, m.bodyHeight())
	return cmd
}

// resetSearch drops back out of any search/viewed-user state to our own
// profile (used on a group change).
func (m *Model) resetSearch() {
	m.search.Start()
	m.userLoaded, m.userLoading, m.userErr = false, false, nil
}

// layout sizes the scrollable viewport to the body area, reserving one line for
// the status/help line pinned at the bottom.
func (m *Model) layout() {
	bodyH := m.bodyHeight()
	if m.ready {
		m.viewport.SetWidth(m.width)
		m.viewport.SetHeight(bodyH)
	} else {
		m.viewport = viewport.New(viewport.WithWidth(m.width), viewport.WithHeight(bodyH))
	}
}

func (m Model) bodyHeight() int {
	if h := m.height - 1; h > 0 {
		return h
	}
	return 1
}

// refreshViewport re-renders whichever profile occupies the viewport after a
// resize.
func (m *Model) refreshViewport() {
	switch {
	case m.mode == modeUser && m.userLoaded:
		m.showProfile(m.user)
	case m.mode == modeSelf && m.loaded:
		m.showProfile(m.profile)
	}
}

// showProfile loads a profile into the scrollable viewport, scrolled to top.
func (m *Model) showProfile(p cosmo.Profile) {
	if !m.ready {
		return
	}
	m.viewport.SetContent(m.renderProfile(p))
	m.viewport.GotoTop()
}

func (m Model) load() tea.Cmd {
	client, group := m.client, m.group
	return func() tea.Msg {
		p, err := client.Profile(context.Background(), group)
		if err != nil {
			return errMsg{err}
		}
		return loadedMsg{profile: p}
	}
}

func (m Model) loadUser(id int) tea.Cmd {
	client, group := m.client, m.group
	return func() tea.Msg {
		p, err := client.UserProfile(context.Background(), group, id)
		if err != nil {
			return userErrMsg{err}
		}
		return userLoadedMsg{profile: p}
	}
}

// openWebProfile opens nickname's page on a profile-mirror site via the
// user's link handler (or OS opener). No-op until the profile has loaded
// and the nickname is known.
func openWebProfile(host, nickname string) tea.Cmd {
	if nickname == "" {
		return nil
	}
	return func() tea.Msg {
		_ = external.OpenURL("https://" + host + "/@" + nickname)
		return nil
	}
}

// View satisfies tea.Model. The layout itself is render; keeping it a plain
// string keeps the composing shell and this package's tests off tea.View.
func (m Model) View() tea.View { return tea.NewView(m.render()) }

func (m Model) render() string {
	if !m.ready {
		return statusStyle.Render("loading profile…")
	}

	var body, status string
	switch m.mode {
	case modeSearching:
		body, status = m.search.View(), m.search.StatusHint()
	case modeUser:
		body, status = m.userBody()
	default:
		body, status = m.selfBody()
	}

	bodyH := m.bodyHeight()
	body = lipgloss.Place(m.width, bodyH, lipgloss.Left, lipgloss.Top, body)
	return body + "\n" + statusStyle.Render(status)
}

// selfBody renders the logged-in user's own profile.
func (m Model) selfBody() (string, string) {
	switch {
	case m.err != nil:
		return errStyle.Render("error: " + textfmt.Line(m.err.Error())), "r: retry · s: search users"
	case !m.loaded:
		return statusStyle.Render("loading profile…"), "s: search users"
	default:
		return m.viewport.View(), m.scrollHint("s: search users · r: reload · a: apollo.cafe · o: objekt.top")
	}
}

// userBody renders a viewed user's profile.
func (m Model) userBody() (string, string) {
	switch {
	case m.userErr != nil:
		return errStyle.Render("error: " + textfmt.Line(m.userErr.Error())), "r: retry · s: search"
	case !m.userLoaded:
		return statusStyle.Render("loading profile…"), ""
	default:
		return m.viewport.View(), m.scrollHint("s: search · r: reload · a: apollo.cafe · o: objekt.top")
	}
}

// scrollHint prefixes hint with a scroll indicator when the profile overflows
// the viewport.
func (m Model) scrollHint(hint string) string {
	if m.viewport.TotalLineCount() > m.viewport.Height() {
		return fmt.Sprintf("%d%% · %s", int(m.viewport.ScrollPercent()*100), hint)
	}
	return hint
}

func (m Model) renderProfile(p cosmo.Profile) string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(p.Nickname))
	b.WriteString("\n")

	bio := p.StatusMessage
	if strings.TrimSpace(bio) == "" {
		bio = labelStyle.Render("(no bio)")
	} else {
		bio = bioStyle.Render(bio)
	}
	b.WriteString(bio + "\n\n")

	b.WriteString(field("fandom", p.FandomName))
	b.WriteString(field("streak", plural(p.CurrentStreak, "day", "days")))
	b.WriteString(field("following", plural(p.FollowDurationDays, "day", "days")))
	b.WriteString(field("wallet", textfmt.ShortAddr(p.Address)))
	b.WriteString(field("member since", cosmo.LocalDate(p.CreatedAt)))

	if p.ShowOverview() {
		b.WriteString("\n" + headStyle.Render("Stats - "+m.group) + "\n")
		if p.HasComo {
			b.WriteString(stat("COMO", p.TotalComo))
		}
		b.WriteString(stat("Objekts", p.TotalObjekt))
		b.WriteString(stat("Live joined", p.Stats.JoinedLiveCount))
		b.WriteString(stat("Gravity votes", p.Stats.JoinedGravityCount))
		b.WriteString(stat("Grids completed", p.Stats.CompletedGridCount))
		b.WriteString(stat("Offline badges", p.Stats.OfflineBadgeCount))
	} else {
		b.WriteString("\n" + headStyle.Render("Stats") + "\n  " + labelStyle.Render("private") + "\n")
	}

	if p.ShowObjektStats() {
		if len(p.ObjektsByClass) > 0 {
			b.WriteString("\n" + headStyle.Render("Objekts by class") + "\n  ")
			parts := make([]string, 0, len(p.ObjektsByClass))
			for _, g := range p.ObjektsByClass {
				parts = append(parts, fmt.Sprintf("%s %d", g.Name, g.Count))
			}
			b.WriteString(strings.Join(parts, labelStyle.Render(" · ")))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n" + headStyle.Render("Joined channels") + "\n")
	if !p.ShowChannels() {
		b.WriteString("  " + labelStyle.Render("private") + "\n")
	} else if connected := p.ConnectedChannels(); len(connected) == 0 {
		b.WriteString("  " + labelStyle.Render("none") + "\n")
	} else {
		for _, ch := range connected {
			b.WriteString(fmt.Sprintf("  %-12s %s\n",
				ch.Name, labelStyle.Render(plural(ch.DaysTogether, "day", "days"))))
		}
	}

	return style.Wrap(b.String(), m.viewport.Width())
}

// field renders one "label: value" metadata line, hiding empty values.
func field(label, value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return labelStyle.Render(label+": ") + value + "\n"
}

// stat renders one right-aligned counter line under the Stats heading.
// stat renders one "label  count" row. The label is padded before it is styled:
// padding the styled string instead counts the color escapes as characters, so
// the column drifts whenever the escape sequences change length.
func stat(label string, n int) string {
	return fmt.Sprintf("  %s %s\n", labelStyle.Render(fmt.Sprintf("%-16s", label)), strconv.Itoa(n))
}

// plural formats a count with a singular/plural unit, or "" for a zero-less
// unset value so field can omit it.
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
