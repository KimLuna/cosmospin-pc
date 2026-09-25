// Package gravity is the Gravity page: a category list on the left (Ongoing and
// Past, both from /v4/gravities - see cosmo.OngoingGravities for why the live
// one is not fetched with category=ongoing), the selected category's gravities
// on the right, and a full-page detail view for the highlighted gravity. It
// follows the news/room/live layout: the right pane follows the category cursor,
// and the app-wide motions walk categories → gravities → detail (see
// internal/tui/keynav).
//
// A gravity is a COMO-voting event. The detail view renders the event's
// description blocks, its poll candidates or ranked results, and the user's own
// COMO spend; from there v casts a vote (vote.go), which really does spend COMO,
// and a opens the gravity on apollo.cafe.
//
// What the detail shows, and whether v does anything, follow the gravity's
// cosmo.GravityState rather than its polls' Finalized flag, which is true for
// two states the page has to keep apart: a poll that has finalized without a
// result is still waiting on its reveal, and shows candidates like the three
// unrevealed states before it (upcoming, voting, counting). A result is what
// makes a gravity finished, and only StateVoting accepts a ballot.
package gravity

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"codeberg.org/djvu/cosmo-tui/internal/cosmo"
	"codeberg.org/djvu/cosmo-tui/internal/external"
	"codeberg.org/djvu/cosmo-tui/internal/tui/keynav"
	"codeberg.org/djvu/cosmo-tui/internal/tui/listfilter"
	"codeberg.org/djvu/cosmo-tui/internal/tui/style"
	"codeberg.org/djvu/cosmo-tui/internal/tui/textfmt"
	"codeberg.org/djvu/cosmo-tui/internal/tui/uimsg"
	"codeberg.org/djvu/cosmo-tui/internal/wallet"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const sidebarWidth = 22

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
	metaStyle    = lipgloss.NewStyle().Faint(true)
	statusStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	errStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	warnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	okStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	headingStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
)

type category int

const (
	categoryOngoing category = iota
	categoryPast
)

func (c category) name() string {
	if c == categoryPast {
		return "Past"
	}
	return "Ongoing"
}

type focusState int

const (
	focusCategories focusState = iota
	focusEntries
	focusDetail
)

type (
	ongoingLoadedMsg struct{ gravities []cosmo.Gravity }
	pastLoadedMsg    struct{ gravities []cosmo.Gravity }
	detailMsg        struct {
		gravity cosmo.Gravity
		status  cosmo.GravityStatus
	}
	errMsg struct{ err error }
)

// categoryItem is one sidebar row.
type categoryItem struct{ c category }

func (i categoryItem) Title() string       { return i.c.name() }
func (i categoryItem) Description() string { return "" }
func (i categoryItem) FilterValue() string { return i.c.name() }

// gravityItem is one gravity in the right-hand list.
type gravityItem struct{ g cosmo.Gravity }

func (i gravityItem) Title() string { return i.g.Title }

// Description summarizes the row by state: the voting window while a vote is
// open, the opening time for one still scheduled, the reveal time while the
// votes are being counted, or the date and winner once it is finished.
func (i gravityItem) Description() string {
	g := i.g
	switch g.State() {
	case cosmo.StateVoting:
		return fmt.Sprintf("voting · ends %s", cosmo.LocalDateTime(votingEnds(g)))
	case cosmo.StateUpcoming:
		return fmt.Sprintf("upcoming · opens %s", cosmo.LocalDateTime(votingOpens(g)))
	case cosmo.StateCounting:
		return fmt.Sprintf("counting votes · results %s", cosmo.LocalDateTime(resultsDue(g)))
	case cosmo.StateCounted:
		return fmt.Sprintf("votes counted · results %s", cosmo.LocalDateTime(resultsDue(g)))
	}
	if g.Result != nil && g.Result.ResultTitle != "" {
		return fmt.Sprintf("%s · winner: %s", cosmo.LocalDate(g.EndDate), g.Result.ResultTitle)
	}
	return cosmo.LocalDate(g.EndDate)
}

func (i gravityItem) FilterValue() string { return i.g.Title }

// Model is the gravity page.
type Model struct {
	client *cosmo.Client
	group  string

	category category
	focus    focusState

	categories list.Model
	ongoing    list.Model
	past       list.Model
	loadedOng  bool
	loadedPast bool

	viewport      viewport.Model
	loadingDetail bool
	detail        cosmo.Gravity // the gravity currently open in the detail view

	// wallet signs votes; nil when no key is provisioned, which disables voting.
	wallet *wallet.Wallet
	vote   voteState

	loading       bool
	err           error
	status        string
	width, height int
	ready         bool
}

// New builds the gravity page for a group and loads the ongoing gravities via
// Init. w is the vote-signing wallet, or nil when none is provisioned (voting
// is then disabled and the page stays read-only).
func New(client *cosmo.Client, group string, w *wallet.Wallet) Model {
	cl := style.NewList("Categories", true, false)
	cl.SetItems([]list.Item{
		categoryItem{c: categoryOngoing},
		categoryItem{c: categoryPast},
	})

	// An empty list renders its title and then "No <plural>.", so the phrasing
	// of the empty state is set through the item name. That keeps the pane's
	// header on screen when a category has nothing in it, which a hand-rolled
	// notice in place of the list would throw away.
	ongoing := style.NewList(categoryOngoing.name(), false, true)
	ongoing.SetStatusBarItemName("gravity", "gravities are ongoing right now")
	past := style.NewList(categoryPast.name(), false, true)
	past.SetStatusBarItemName("gravity", "past gravities")

	return Model{
		client:     client,
		group:      group,
		wallet:     w,
		categories: cl,
		ongoing:    ongoing,
		past:       past,
		loading:    true,
	}
}

func (m Model) Title() string { return "Gravity" }

// AcceptsText reports whether the page is capturing keystrokes, so the shell
// yields the plain-letter globals (q/A/?) to it. Only text entry claims them:
// the list filters and the vote flow's COMO amount box, where a typed "q" is a
// character rather than a quit. The rest of the vote flow deliberately leaves
// the globals working.
func (m Model) AcceptsText() bool {
	return m.vote.phase == voteAmount ||
		m.categories.FilterState() == list.Filtering ||
		m.ongoing.FilterState() == list.Filtering ||
		m.past.FilterState() == list.Filtering
}

func (m Model) Init() tea.Cmd { return m.loadCategory() }

// setFocus moves focus between panes and restyles the lists so only the focused
// pane shows a bright cursor.
func (m *Model) setFocus(f focusState) {
	m.focus = f
	m.categories.SetDelegate(style.ListDelegate(f == focusCategories, false))
	entries := style.ListDelegate(f == focusEntries, true)
	m.ongoing.SetDelegate(entries)
	m.past.SetDelegate(entries)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		m.ready = true
		return m, nil

	case uimsg.GroupChanged:
		m.group = msg.Group
		m.category = categoryOngoing
		m.categories.Select(0)
		m.setFocus(focusCategories)
		m.loadedOng, m.loadedPast = false, false
		listfilter.Resync(&m.ongoing, m.ongoing.SetItems(nil))
		listfilter.Resync(&m.past, m.past.SetItems(nil))
		m.err = nil
		m.loading = true
		m.updateStatus()
		return m, m.loadCategory()

	case ongoingLoadedMsg:
		m.loadedOng = true
		m.loading = false
		m.err = nil
		items := make([]list.Item, len(msg.gravities))
		for i, g := range msg.gravities {
			items[i] = gravityItem{g: g}
		}
		listfilter.Resync(&m.ongoing, m.ongoing.SetItems(items))
		m.updateStatus()
		return m, nil

	case pastLoadedMsg:
		m.loadedPast = true
		m.loading = false
		m.err = nil
		items := make([]list.Item, len(msg.gravities))
		for i, g := range msg.gravities {
			items[i] = gravityItem{g: g}
		}
		listfilter.Resync(&m.past, m.past.SetItems(items))
		m.updateStatus()
		return m, nil

	case detailMsg:
		m.loadingDetail = false
		m.err = nil
		m.detail = msg.gravity
		m.viewport.SetContent(m.renderDetail(msg.gravity, msg.status))
		m.viewport.GotoTop()
		return m, nil

	case voteBalanceMsg:
		if msg.err != nil {
			m.vote.err = msg.err
			return m, nil
		}
		m.vote.balance = msg.balance
		return m, nil

	case voteResultMsg:
		m.vote.phase = voteDone
		m.vote.hash = msg.hash
		m.vote.confirmed = msg.confirmed
		m.vote.err = msg.err
		return m, nil

	case errMsg:
		m.loading = false
		m.loadingDetail = false
		m.err = msg.err
		return m, nil

	case list.FilterMatchesMsg:
		cmd := listfilter.Route(msg, &m.categories, &m.ongoing, &m.past)
		m.updateStatus()
		return m, cmd

	case tea.PasteMsg:
		// Pasted text goes to whichever box is capturing it: the vote flow's COMO
		// amount, or a filtering list.
		if m.vote.phase == voteAmount {
			var cmd tea.Cmd
			m.vote.amount, cmd = m.vote.amount.Update(msg)
			style.FitInput(&m.vote.amount) // the box shares its line with the balance
			return m, cmd
		}
		return m, listfilter.Paste(msg, &m.categories, &m.ongoing, &m.past)

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Any keypress dismisses a shown error (the key still performs its action).
	m.err = nil

	// A vote in progress is modal over everything else, so a spend cannot be
	// interrupted by tab navigation or the detail's own keys.
	if updated, cmd, handled := m.handleVoteKey(msg); handled {
		return updated, cmd
	}

	// The detail pane is modal: it owns every key while open (esc returns to the
	// entry list).
	if m.focus == focusDetail {
		if msg.String() == "v" && m.canVote() {
			return m.startVote()
		}
		if keynav.Ascend(msg) {
			m.setFocus(focusEntries)
			return m, nil
		}
		switch msg.String() {
		case "a":
			if m.detail.ID != 0 {
				return m, openApollo(apolloURL(m.detail, m.group))
			}
			return m, nil
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

	// 'r' reloads the active category regardless of focus (unless typing a filter).
	if msg.String() == "r" && !m.AcceptsText() {
		m.loading = true
		if m.category == categoryPast {
			m.loadedPast = false
		} else {
			m.loadedOng = false
		}
		return m, m.loadCategory()
	}

	if m.focus == focusCategories {
		if m.categories.FilterState() != list.Filtering {
			if keynav.Descend(msg) {
				cmd := m.syncCategory()
				if _, ok := m.categories.SelectedItem().(categoryItem); ok {
					m.setFocus(focusEntries)
				}
				return m, cmd
			}
			if keynav.Ascend(msg) {
				return m, nil // leftmost pane: swallow, or the list would page on h
			}
		}
		var cmd tea.Cmd
		m.categories, cmd = m.categories.Update(msg)
		return m, tea.Batch(cmd, m.syncCategory())
	}

	// focusEntries
	if m.activeList().FilterState() != list.Filtering {
		if keynav.Ascend(msg) {
			m.setFocus(focusCategories)
			return m, nil
		}
		if keynav.Descend(msg) {
			return m.openDetail()
		}
	}
	var cmd tea.Cmd
	al := m.activeList()
	*al, cmd = al.Update(msg)
	m.updateStatus()
	return m, cmd
}

// syncCategory makes the entry pane follow the category cursor, fetching the
// newly selected category the first time it is shown.
func (m *Model) syncCategory() tea.Cmd {
	it, ok := m.categories.SelectedItem().(categoryItem)
	if !ok || it.c == m.category {
		return nil
	}
	m.category = it.c
	m.err = nil
	m.updateStatus()
	cmd := m.loadCategoryIfNeeded()
	if cmd != nil {
		m.loading = true
	}
	return cmd
}

// openDetail fetches the highlighted gravity and takes over the page with the
// detail viewport. A no-op if nothing is selected or the page hasn't been sized.
func (m Model) openDetail() (tea.Model, tea.Cmd) {
	it, ok := m.activeList().SelectedItem().(gravityItem)
	if !ok || !m.ready {
		return m, nil
	}
	m.setFocus(focusDetail)
	m.loadingDetail = true
	m.err = nil
	m.viewport.SetContent("")
	m.viewport.GotoTop()
	return m, m.loadDetail(it.g.ID)
}

func (m *Model) activeList() *list.Model {
	if m.category == categoryPast {
		return &m.past
	}
	return &m.ongoing
}

func (m Model) categoryLoaded() bool {
	if m.category == categoryPast {
		return m.loadedPast
	}
	return m.loadedOng
}

// updateStatus records the active category's row count, shown at the head of the
// entry pane's status line.
func (m *Model) updateStatus() {
	m.status = fmt.Sprintf("%d gravity(s)", len(m.activeList().VisibleItems()))
}

// listWidth is the entry pane's width: the page less the sidebar and its divider.
func (m Model) listWidth() int {
	if w := m.width - sidebarWidth - 1; w > 0 {
		return w
	}
	return 1
}

// layout sizes the sidebar, the entry lists and the detail viewport.
func (m *Model) layout() {
	m.categories.SetSize(sidebarWidth-2, m.height)

	listW := m.listWidth()
	listH := m.height - 1
	if listH < 1 {
		listH = 1
	}
	m.ongoing.SetSize(listW, listH)
	m.past.SetSize(listW, listH)

	if m.ready {
		m.viewport.SetWidth(m.width)
		m.viewport.SetHeight(listH)
	} else {
		m.viewport = viewport.New(viewport.WithWidth(m.width), viewport.WithHeight(listH))
	}
}

func (m Model) loadCategory() tea.Cmd {
	client, group := m.client, m.group
	if m.category == categoryPast {
		return func() tea.Msg {
			gs, err := client.AllPastGravities(context.Background(), group)
			if err != nil {
				return errMsg{err}
			}
			return pastLoadedMsg{gravities: gs}
		}
	}
	return func() tea.Msg {
		lists, err := client.OngoingGravities(context.Background(), group)
		if err != nil {
			return errMsg{err}
		}
		return ongoingLoadedMsg{gravities: mergeOngoing(lists)}
	}
}

// mergeOngoing orders the Ongoing category: the votes open now first, then the
// scheduled ones soonest-first. The API hands back upcoming newest-first, which
// would put the gravity that opens last at the top of the queue.
func mergeOngoing(lists cosmo.GravityLists) []cosmo.Gravity {
	upcoming := append([]cosmo.Gravity{}, lists.Upcoming...)
	// StartDate is ISO-8601 UTC, so lexical order is chronological order.
	sort.SliceStable(upcoming, func(i, j int) bool {
		return upcoming[i].StartDate < upcoming[j].StartDate
	})
	return append(append([]cosmo.Gravity{}, lists.Ongoing...), upcoming...)
}

func (m Model) loadCategoryIfNeeded() tea.Cmd {
	if m.categoryLoaded() {
		return nil
	}
	return m.loadCategory()
}

// loadDetail fetches a gravity's full record, the candidate list for any poll
// still open (the summary omits it), and the user's own vote status. The latter
// two are best-effort so a browse still renders if they fail.
func (m Model) loadDetail(id int64) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		ctx := context.Background()
		g, err := client.GravityDetail(ctx, id)
		if err != nil {
			return errMsg{err}
		}
		// Gated on the result rather than Finalized: a poll that has finalized
		// without one is still showing candidates, not a ranking, and /polls/{id}
		// keeps serving them there (verified on poll 235 after it finalized). Only
		// a revealed poll has something better to show than its candidate list.
		for i := range g.Polls {
			if g.Polls[i].Result != nil {
				continue
			}
			if pd, err := client.PollDetail(ctx, g.Polls[i].ID); err == nil {
				g.Polls[i].Choices = pd.Choices
			}
		}
		// The status record only fills in once a poll finalizes: while a vote is
		// open Cosmo reports zeros and no ballots even for one already settled
		// on-chain. Skip the request entirely until then, since there is nothing
		// it could return that we would show. Finalized is the right gate rather
		// than the result - poll 235 served the user's ballots as soon as it
		// finalized, an hour before its reveal - and it is checked per-poll so a
		// mixed event (one round counted, the next still open) fetches its history.
		var status cosmo.GravityStatus
		if anyFinalized(g) {
			status, _ = client.GravityStatus(ctx, id)
		}
		return detailMsg{gravity: g, status: status}
	}
}

// View satisfies tea.Model.
func (m Model) View() tea.View { return tea.NewView(m.render()) }

func (m Model) render() string {
	if m.vote.phase != voteOff {
		return m.renderVote()
	}
	if m.focus == focusDetail {
		return m.detailView()
	}

	left := style.PaneBorder(m.focus == focusCategories).Render(m.categories.View())

	var status string
	switch {
	case m.err != nil:
		status = errStyle.Render("error: " + textfmt.Line(m.err.Error()))
	case m.loading:
		status = statusStyle.Render("loading " + strings.ToLower(m.category.name()) + "…")
	case m.focus == focusCategories:
		status = statusStyle.Render("r: reload")
	default:
		status = statusStyle.Render(m.status + " · r: reload · /: filter")
	}
	status = lipgloss.NewStyle().MaxWidth(m.listWidth()).Render(status)

	// The list renders its own empty state under its title (see New), so an
	// empty category still shows which pane it is.
	right := lipgloss.JoinVertical(lipgloss.Left, m.activeList().View(), status)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

// detailView renders the full-page gravity viewport with a scroll status line.
func (m Model) detailView() string {
	var body string
	switch {
	case m.err != nil:
		body = errStyle.Render("error: " + textfmt.Line(m.err.Error()))
	case m.loadingDetail:
		body = statusStyle.Render("loading…")
	default:
		body = m.viewport.View()
	}
	body = lipgloss.Place(m.width, m.viewport.Height(), lipgloss.Left, lipgloss.Top, body)
	hint := fmt.Sprintf("%d%%", int(m.viewport.ScrollPercent()*100))
	if m.canVote() {
		hint += " · v: vote"
	}
	if m.detail.ID != 0 {
		hint += " · a: apollo.cafe"
	}
	status := statusStyle.Render(hint)
	status = lipgloss.NewStyle().MaxWidth(m.viewport.Width()).Render(status)
	return lipgloss.JoinVertical(lipgloss.Left, body, status)
}

// renderDetail lays out a gravity: heading/meta, its description blocks, then per
// poll either the live candidates (with the user's spend) or the ranked results,
// and finally the top-spender leaderboard once one exists.
func (m Model) renderDetail(g cosmo.Gravity, status cosmo.GravityStatus) string {
	w := m.viewport.Width()
	var b strings.Builder

	b.WriteString(titleStyle.Render(g.Title) + "\n")
	b.WriteString(metaStyle.Render(gravityMeta(g)) + "\n")
	// The user's standing across the whole gravity, once it is revealed.
	if status.Rank > 0 {
		b.WriteString(okStyle.Render(fmt.Sprintf("your rank: #%d · %s COMO spent",
			status.Rank, fmtComo(status.TotalComoUsed))) + "\n")
	}
	if strings.TrimSpace(g.Description) != "" {
		b.WriteString("\n" + style.Wrap(g.Description, w) + "\n")
	}

	for _, blk := range g.Body {
		if s := renderBlock(blk, w); s != "" {
			b.WriteString("\n" + s + "\n")
		}
	}

	for _, p := range g.Polls {
		b.WriteString("\n" + renderPoll(p, status, w) + "\n")
	}
	if s := leaderboardSection(g.Leaderboard, status, w); s != "" {
		b.WriteString("\n" + s + "\n")
	}
	return b.String()
}

// leaderboardSection renders the top COMO spenders, which Cosmo sends only once
// a gravity is revealed (arriving in the same payload as the result - see the
// lifecycle note in cosmo.GravityState). The user's own row is marked when the
// rank they were given matches one here, so the section reads against the "your
// rank" line at the top; the spend has to agree too, since ranks would otherwise
// point at the wrong person the moment two spenders tie.
//
// Addresses are decoded but not shown: 42 hex characters per row would crowd
// out the two columns anyone reads this for.
func leaderboardSection(entries []cosmo.LeaderboardEntry, status cosmo.GravityStatus, w int) string {
	if len(entries) == 0 {
		return ""
	}
	// Measure first, in display columns - nicknames are user-controlled text,
	// so they get the same normalization as any other API string and are padded
	// by rendered width rather than length.
	names := make([]string, len(entries))
	comos := make([]string, len(entries))
	var rankW, nameW, comoW int
	for i, e := range entries {
		names[i] = textfmt.Line(textfmt.Normalize(e.Nickname))
		comos[i] = fmtComo(e.ComoUsed)
		rankW = max(rankW, len(strconv.Itoa(e.Rank)))
		nameW = max(nameW, lipgloss.Width(names[i]))
		comoW = max(comoW, lipgloss.Width(comos[i]))
	}
	// "  12. name  9,001" - everything but the name column is fixed, so the
	// name absorbs whatever narrowing the viewport forces.
	if avail := w - (2 + rankW + 2 + 2 + comoW); avail > 0 && nameW > avail {
		nameW = avail
	}

	var b strings.Builder
	b.WriteString(headingStyle.Render("Leaderboard") + "\n")
	for i, e := range entries {
		name := names[i]
		if lipgloss.Width(name) > nameW {
			name = lipgloss.NewStyle().MaxWidth(nameW).Render(name)
		}
		if pad := nameW - lipgloss.Width(name); pad > 0 {
			name += strings.Repeat(" ", pad)
		}
		head := fmt.Sprintf("  %*d. %s  ", rankW, e.Rank, name)
		como := fmt.Sprintf("%*s", comoW, comos[i])
		if status.Rank == e.Rank && status.TotalComoUsed == e.ComoUsed {
			b.WriteString(okStyle.Render(head + como))
		} else {
			b.WriteString(head + comoStyle(como))
		}
		if i < len(entries)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// gravityMeta is the faint one-liner under the title: state, kind, and whatever
// deadline that state is waiting on, in local wall-clock time - when voting
// opens, when it closes, or when the result lands. A finished gravity has
// nothing pending, so it shows its plain dates instead.
func gravityMeta(g cosmo.Gravity) string {
	kind := "event"
	if g.Type == cosmo.GravityGrand {
		kind = "grand"
	}
	switch g.State() {
	case cosmo.StateVoting:
		return fmt.Sprintf("voting open · %s · voting ends %s", kind, cosmo.LocalDateTime(votingEnds(g)))
	case cosmo.StateUpcoming:
		return fmt.Sprintf("upcoming · %s · voting opens %s", kind, cosmo.LocalDateTime(votingOpens(g)))
	case cosmo.StateCounting:
		return fmt.Sprintf("counting votes · %s · results %s", kind, cosmo.LocalDateTime(resultsDue(g)))
	case cosmo.StateCounted:
		return fmt.Sprintf("votes counted · %s · results %s", kind, cosmo.LocalDateTime(resultsDue(g)))
	}
	return fmt.Sprintf("finished · %s · %s - %s", kind,
		cosmo.LocalDate(g.StartDate), cosmo.LocalDate(g.EndDate))
}

// votingEnds is when voting actually closes: the open poll's end date. That can
// precede the gravity's own end, which carries the later reveal time (poll 235
// closed at 01:00 UTC while its gravity ran to 04:00), so using the gravity end
// would overstate how long is left to vote. Keying on the open poll rather than
// the first unfinalized one also keeps a grand gravity honest: with one day
// counting and the next already open, the deadline that matters is the open
// day's, not the closed one's.
func votingEnds(g cosmo.Gravity) string {
	if p, ok := g.OpenPoll(); ok && p.EndDate != "" {
		return p.EndDate
	}
	return g.EndDate
}

// votingOpens is when voting starts on a gravity that has not begun: the next
// scheduled poll's start date, which can trail the gravity's own start the way
// votingEnds can precede its end. Falls back to the gravity's start.
func votingOpens(g cosmo.Gravity) string {
	if p, ok := g.NextPoll(); ok && p.StartDate != "" {
		return p.StartDate
	}
	return g.StartDate
}

// resultsDue is when a closed poll's result is expected: its reveal date. Cosmo
// leaves hours between the close and the reveal, so this is the only deadline
// that means anything while the votes are being counted. Falls back to the
// gravity's own end, which carries the later reveal time (see votingEnds).
func resultsDue(g cosmo.Gravity) string {
	if p, ok := g.CountingPoll(); ok && p.RevealDate != "" {
		return p.RevealDate
	}
	return g.EndDate
}

// renderBlock renders one description block. Image, video and spacing blocks
// are dropped: their URLs are the app's own layout graphics, which carry no
// information the text blocks and poll data do not already give us.
func renderBlock(b cosmo.BodyBlock, w int) string {
	switch b.Type {
	case "heading":
		return headingStyle.Render(style.Wrap(b.Text, w))
	case "text":
		return style.Wrap(b.Text, w)
	case "image", "video", "spacing":
		return ""
	default:
		return style.Wrap(b.Text, w)
	}
}

// renderPoll renders one poll: its title, then either the finalized ranked
// results or the live candidate list plus the user's own COMO spend.
func renderPoll(p cosmo.Poll, status cosmo.GravityStatus, w int) string {
	var b strings.Builder
	b.WriteString(headingStyle.Render(pollHeading(p)) + "\n")

	if p.Result != nil {
		for _, r := range p.Result.Results {
			b.WriteString(fmt.Sprintf("  %d. %s  %s\n",
				r.Rank, r.ChoiceName, comoStyle(fmtComo(r.ComoUsed))))
		}
		b.WriteString(metaStyle.Render(fmt.Sprintf("  total: %s COMO", fmtComo(p.Result.TotalComoUsed))))
		// The ballots are listed separately rather than marked against a
		// candidate: a user may vote repeatedly and spread those votes over
		// different candidates, so there is no single "their" choice to flag.
		if s := yourVoteLine(status, p.ID); s != "" {
			b.WriteString("\n" + s)
		}
		return b.String()
	}

	// Not revealed yet - open, scheduled, or counted: list candidates.
	if len(p.Choices) == 0 {
		b.WriteString(metaStyle.Render("  (candidates unavailable)"))
		return b.String()
	}
	for _, c := range p.Choices {
		line := "  - " + c.Title
		if c.Description != "" {
			line += metaStyle.Render(" (" + c.Description + ")")
		}
		b.WriteString(line + "\n")
	}
	// Only the positive case is stated. A zero from /gravities/{id}/status is
	// ambiguous: Cosmo reports 0 COMO and no votes for a poll that has been
	// voted in but not yet revealed (verified against a vote that succeeded
	// on-chain - receipt 0x1, TransferSingle on the COMO contract - while the
	// endpoint still said 0), so "you have not voted" would be a false claim.
	if s := yourVoteLine(status, p.ID); s != "" {
		b.WriteString(s)
	}
	return strings.TrimRight(b.String(), "\n")
}

// anyFinalized reports whether any of a gravity's polls has finalized, which is
// what makes the user's vote record worth fetching (see loadDetail).
func anyFinalized(g cosmo.Gravity) bool {
	for _, p := range g.Polls {
		if p.Finalized {
			return true
		}
	}
	return false
}

// pollStatus finds the user's record for one poll, or nil when absent.
func pollStatus(status cosmo.GravityStatus, pollID int64) *cosmo.PollVoteStatus {
	for i := range status.Votes {
		if status.Votes[i].PollID == pollID {
			return &status.Votes[i]
		}
	}
	return nil
}

// yourVoteLine describes the user's own ballots in a poll, or "" when the record
// is empty (which, mid-vote, does not mean they did not vote - see renderPoll).
func yourVoteLine(status cosmo.GravityStatus, pollID int64) string {
	ps := pollStatus(status, pollID)
	if ps == nil {
		return ""
	}
	if len(ps.Votes) == 0 {
		if ps.ComoUsed > 0 {
			return okStyle.Render(fmt.Sprintf("  you spent %s COMO here", fmtComo(ps.ComoUsed)))
		}
		return ""
	}
	var b strings.Builder
	for i, v := range ps.Votes {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(okStyle.Render(fmt.Sprintf("  your vote: %s (%s COMO)", v.ChoiceName, fmtComo(v.ComoUsed))))
		if v.At != "" {
			b.WriteString(metaStyle.Render(" " + cosmo.LocalDate(v.At)))
		}
	}
	return b.String()
}

// pollHeading names what the poll's section is about to show. It keys on the
// result, not Finalized: heading a finalized poll "Results" while the body
// still lists candidates - which is what a poll between its tally and its
// reveal does - would promise a ranking that is not there.
func pollHeading(p cosmo.Poll) string {
	switch {
	case p.Result != nil:
		return "Results"
	case p.Finalized:
		return "Candidates (votes counted)"
	case p.Started() && p.Ended():
		// The candidates are all we have until the reveal, but listing them
		// under a bare "Candidates" would read as an invitation to vote.
		return "Candidates (voting closed)"
	default:
		return "Candidates"
	}
}

// apolloURL is the gravity's page on apollo.cafe, the third-party Cosmo web
// client, which keys gravities on the same numeric id Cosmo does and matches
// the artist slug case-insensitively. The gravity's own Artist is preferred
// over the page's group so the link stays right if the two ever disagree.
func apolloURL(g cosmo.Gravity, group string) string {
	artist := g.Artist
	if artist == "" {
		artist = group
	}
	return fmt.Sprintf("https://apollo.cafe/gravity/%s/%d", artist, g.ID)
}

// openApollo hands a URL to the user's browser (or configured link handler).
// Failures are silent: the opener runs detached, so there is nothing useful to
// report back into the page.
func openApollo(url string) tea.Cmd {
	return func() tea.Msg {
		_ = external.OpenURL(url)
		return nil
	}
}

func comoStyle(s string) string { return metaStyle.Render(s) }

// fmtComo renders a COMO amount with thousands separators.
func fmtComo(n int64) string {
	s := fmt.Sprintf("%d", n)
	if n < 1000 {
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	return strings.Join(parts, ",")
}
