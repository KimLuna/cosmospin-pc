// Package news is the news page: a category list on the left (Announcements from
// /notices, Schedule from /artist-schedules, Notifications from
// /notification-center), the selected category's entries on the right, and a
// full-page detail view for the highlighted entry. It follows the room/live
// layout: the right pane follows the category cursor, and the app-wide motions
// walk categories → entries → detail (see internal/tui/keynav). Notifications
// stop at the entry list: the endpoint has no detail call and each row already
// carries the whole notification.
package news

import (
	"context"
	"fmt"
	"strings"
	"time"

	"codeberg.org/djvu/cosmo-tui/internal/cosmo"
	"codeberg.org/djvu/cosmo-tui/internal/tui/keynav"
	"codeberg.org/djvu/cosmo-tui/internal/tui/listfilter"
	"codeberg.org/djvu/cosmo-tui/internal/tui/style"
	"codeberg.org/djvu/cosmo-tui/internal/tui/textfmt"
	"codeberg.org/djvu/cosmo-tui/internal/tui/uimsg"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const sidebarWidth = 22

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
	metaStyle   = lipgloss.NewStyle().Faint(true)
	labelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

type category int

const (
	categoryAnnouncements category = iota
	categorySchedule
	categoryNotifications
)

// name is the category's display name, used for both the sidebar row and the
// entry list's title.
func (c category) name() string {
	switch c {
	case categorySchedule:
		return "Schedule"
	case categoryNotifications:
		return "Notifications"
	default:
		return "Announcements"
	}
}

type focusState int

const (
	focusCategories focusState = iota
	focusEntries
	focusDetail
)

type (
	noticesLoadedMsg       struct{ notices []cosmo.Notice }
	schedulesLoadedMsg     struct{ schedules []cosmo.Schedule }
	notificationsLoadedMsg struct{ notifications []cosmo.Notification }
	noticeDetailMsg        struct{ detail cosmo.NoticeDetail }
	scheduleDetailMsg      struct{ detail cosmo.ScheduleDetail }
	errMsg                 struct{ err error }
)

// categoryItem is one sidebar row.
type categoryItem struct{ c category }

func (i categoryItem) Title() string       { return i.c.name() }
func (i categoryItem) Description() string { return "" }
func (i categoryItem) FilterValue() string { return i.c.name() }

type noticeItem struct{ n cosmo.Notice }

func (i noticeItem) Title() string { return i.n.Title }
func (i noticeItem) Description() string {
	return fmt.Sprintf("%s · %s", i.n.Category, fmtDate(i.n.ActiveAt))
}
func (i noticeItem) FilterValue() string { return i.n.Title }

type scheduleItem struct{ s cosmo.Schedule }

func (i scheduleItem) Title() string       { return i.s.Title }
func (i scheduleItem) Description() string { return fmtRange(i.s.StartAt, i.s.EndAt) }
func (i scheduleItem) FilterValue() string { return i.s.Title }

type notificationItem struct{ n cosmo.Notification }

func (i notificationItem) Title() string { return i.n.Title }

// Description carries the category, the time and the whole body: no detail view
// sits behind the row, so everything has to show here. Time-of-day rather than
// the bare date the other categories use, since a group sends several a day.
func (i notificationItem) Description() string {
	return fmt.Sprintf("%s · %s · %s", i.n.Category, cosmo.LocalDateTime(i.n.SentAt), i.n.Content)
}

// FilterValue spans the body because titles repeat heavily ("Cosmo Live:
// tripleS" over and over): only the content tells two rows apart.
func (i notificationItem) FilterValue() string { return i.n.Title + " " + i.n.Content }

// Model is the news page.
type Model struct {
	client *cosmo.Client
	group  string

	category category
	focus    focusState

	categories    list.Model
	announcements list.Model
	schedule      list.Model
	notifications list.Model
	loadedAnn     bool
	loadedSched   bool
	loadedNotif   bool

	viewport      viewport.Model
	loadingDetail bool

	loading       bool
	err           error
	status        string
	width, height int
	ready         bool
}

// categoryLoaded reports whether the active category's list has been fetched.
func (m Model) categoryLoaded() bool {
	switch m.category {
	case categorySchedule:
		return m.loadedSched
	case categoryNotifications:
		return m.loadedNotif
	default:
		return m.loadedAnn
	}
}

// New builds the news page for a group and loads the announcements via Init.
func New(client *cosmo.Client, group string) Model {
	newList := func(title string) list.Model {
		return style.NewList(title, false, true)
	}

	cl := style.NewList("Categories", true, false)
	cl.SetItems([]list.Item{
		categoryItem{c: categoryAnnouncements},
		categoryItem{c: categorySchedule},
		categoryItem{c: categoryNotifications},
	})

	return Model{
		client:        client,
		group:         group,
		categories:    cl,
		announcements: newList(categoryAnnouncements.name()),
		schedule:      newList(categorySchedule.name()),
		notifications: newList(categoryNotifications.name()),
		loading:       true,
	}
}

func (m Model) Title() string { return "News" }

// AcceptsText reports whether any of the page's list filters is capturing
// keystrokes.
func (m Model) AcceptsText() bool {
	return m.categories.FilterState() == list.Filtering ||
		m.announcements.FilterState() == list.Filtering ||
		m.schedule.FilterState() == list.Filtering ||
		m.notifications.FilterState() == list.Filtering
}

func (m Model) Init() tea.Cmd { return m.loadCategory() }

// setFocus moves focus between panes and restyles the lists so only the focused
// pane shows a bright cursor.
func (m *Model) setFocus(f focusState) {
	m.focus = f
	m.categories.SetDelegate(style.ListDelegate(f == focusCategories, false))
	entries := style.ListDelegate(f == focusEntries, true)
	m.announcements.SetDelegate(entries)
	m.schedule.SetDelegate(entries)
	m.notifications.SetDelegate(entries)
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
		m.category = categoryAnnouncements
		m.categories.Select(0) // keep the cursor on Announcements, matching m.category
		m.setFocus(focusCategories)
		m.loadedAnn, m.loadedSched, m.loadedNotif = false, false, false
		// Resync each swap so a list with an applied filter re-runs its matches
		// instead of rendering empty (see listfilter.Resync).
		listfilter.Resync(&m.announcements, m.announcements.SetItems(nil))
		listfilter.Resync(&m.schedule, m.schedule.SetItems(nil))
		listfilter.Resync(&m.notifications, m.notifications.SetItems(nil))
		m.err = nil
		m.loading = true
		m.updateStatus()
		return m, m.loadCategory()

	case noticesLoadedMsg:
		m.loadedAnn = true
		m.loading = false
		m.err = nil
		items := make([]list.Item, len(msg.notices))
		for i, n := range msg.notices {
			items[i] = noticeItem{n: n}
		}
		listfilter.Resync(&m.announcements, m.announcements.SetItems(items))
		m.updateStatus()
		return m, nil

	case schedulesLoadedMsg:
		m.loadedSched = true
		m.loading = false
		m.err = nil
		items := make([]list.Item, len(msg.schedules))
		for i, s := range msg.schedules {
			items[i] = scheduleItem{s: s}
		}
		listfilter.Resync(&m.schedule, m.schedule.SetItems(items))
		m.updateStatus()
		return m, nil

	case notificationsLoadedMsg:
		m.loadedNotif = true
		m.loading = false
		m.err = nil
		items := make([]list.Item, len(msg.notifications))
		for i, n := range msg.notifications {
			items[i] = notificationItem{n: n}
		}
		listfilter.Resync(&m.notifications, m.notifications.SetItems(items))
		m.updateStatus()
		return m, nil

	case noticeDetailMsg:
		m.loadingDetail = false
		m.err = nil
		m.viewport.SetContent(m.renderNotice(msg.detail))
		m.viewport.GotoTop()
		return m, nil

	case scheduleDetailMsg:
		m.loadingDetail = false
		m.err = nil
		m.viewport.SetContent(m.renderSchedule(msg.detail))
		m.viewport.GotoTop()
		return m, nil

	case errMsg:
		m.loading = false
		m.loadingDetail = false
		m.err = msg.err
		return m, nil

	case list.FilterMatchesMsg:
		cmd := listfilter.Route(msg, &m.categories, &m.announcements, &m.schedule, &m.notifications)
		m.updateStatus() // the filter changed how many rows are on screen
		return m, cmd

	case tea.PasteMsg:
		// Pasted text goes to whichever list is filtering, if any. The row count
		// is refreshed off the match set this asks for, not here.
		return m, listfilter.Paste(msg, &m.categories, &m.announcements, &m.schedule, &m.notifications)

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Any keypress dismisses a shown error (the key still performs its action);
	// errors are transient status-line notices, not modal state.
	m.err = nil

	// The detail pane is modal: it owns every key while open (esc returns to the
	// entry list), so handle it before the reload/list routing below.
	if m.focus == focusDetail {
		if keynav.Ascend(msg) {
			m.setFocus(focusEntries)
			return m, nil
		}
		switch msg.String() {
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
		return m, m.loadCategory()
	}

	if m.focus == focusCategories {
		if m.categories.FilterState() != list.Filtering {
			if keynav.Descend(msg) {
				cmd := m.syncCategory() // normally a no-op: the pane already follows the cursor
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
	al := m.activeList() // m is this call's own copy, so this writes through to it
	*al, cmd = al.Update(msg)
	m.updateStatus() // a filter changes how many rows are on screen
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

// openDetail fetches the highlighted entry and takes over the page with the
// detail viewport. A no-op if nothing is selected or the page hasn't been sized.
func (m Model) openDetail() (tea.Model, tea.Cmd) {
	cmd := m.loadDetail()
	if cmd == nil || !m.ready {
		return m, nil
	}
	m.setFocus(focusDetail)
	m.loadingDetail = true
	m.err = nil
	m.viewport.SetContent("") // drop the previous entry while this one loads
	m.viewport.GotoTop()
	return m, cmd
}

func (m *Model) activeList() *list.Model {
	switch m.category {
	case categorySchedule:
		return &m.schedule
	case categoryNotifications:
		return &m.notifications
	default:
		return &m.announcements
	}
}

// updateStatus records the active category's row count, shown at the head of the
// entry pane's status line (mirroring room's post count).
func (m *Model) updateStatus() {
	noun := "announcement"
	switch m.category {
	case categorySchedule:
		noun = "event"
	case categoryNotifications:
		noun = "notification"
	}
	// VisibleItems, not Items: with a filter applied the count has to describe
	// what is actually on screen.
	m.status = fmt.Sprintf("%d %s(s)", len(m.activeList().VisibleItems()), noun)
}

// listWidth is the entry pane's width: the page less the sidebar and its divider.
func (m Model) listWidth() int {
	if w := m.width - sidebarWidth - 1; w > 0 {
		return w
	}
	return 1
}

// layout sizes the sidebar, the entry lists and the detail viewport. The entry
// pane reserves one row for its status line; the detail viewport fills the page
// less the same row.
func (m *Model) layout() {
	m.categories.SetSize(sidebarWidth-2, m.height)

	listW := m.listWidth()
	listH := m.height - 1
	if listH < 1 {
		listH = 1
	}
	m.announcements.SetSize(listW, listH)
	m.schedule.SetSize(listW, listH)
	m.notifications.SetSize(listW, listH)

	if m.ready {
		m.viewport.SetWidth(m.width)
		m.viewport.SetHeight(listH)
	} else {
		m.viewport = viewport.New(viewport.WithWidth(m.width), viewport.WithHeight(listH))
	}
}

// loadDetail returns a command to fetch the highlighted entry's detail, or nil
// when nothing is selected (so the caller can skip the switch to detail mode).
func (m Model) loadDetail() tea.Cmd {
	client := m.client
	switch m.category {
	case categorySchedule:
		it, ok := m.schedule.SelectedItem().(scheduleItem)
		if !ok {
			return nil
		}
		id := it.s.ID
		return func() tea.Msg {
			d, err := client.ScheduleDetail(context.Background(), id)
			if err != nil {
				return errMsg{err}
			}
			return scheduleDetailMsg{detail: d}
		}
	case categoryNotifications:
		// /notification-center has no by-id call and the row already shows the
		// whole notification, so there is nothing to descend into: returning nil
		// leaves openDetail a no-op rather than opening an empty viewport.
		return nil
	default:
		it, ok := m.announcements.SelectedItem().(noticeItem)
		if !ok {
			return nil
		}
		id := it.n.ID
		return func() tea.Msg {
			d, err := client.NoticeDetail(context.Background(), id)
			if err != nil {
				return errMsg{err}
			}
			return noticeDetailMsg{detail: d}
		}
	}
}

func (m Model) loadCategory() tea.Cmd {
	client, group := m.client, m.group
	switch m.category {
	case categorySchedule:
		return func() tea.Msg {
			ss, err := client.Schedules(context.Background(), group)
			if err != nil {
				return errMsg{err}
			}
			return schedulesLoadedMsg{schedules: ss}
		}
	case categoryNotifications:
		return func() tea.Msg {
			ns, err := client.Notifications(context.Background(), group)
			if err != nil {
				return errMsg{err}
			}
			return notificationsLoadedMsg{notifications: ns}
		}
	default:
		return func() tea.Msg {
			ns, err := client.Notices(context.Background(), group)
			if err != nil {
				return errMsg{err}
			}
			return noticesLoadedMsg{notices: ns}
		}
	}
}

// loadCategoryIfNeeded returns a load command only when the active category has
// not been fetched yet.
func (m Model) loadCategoryIfNeeded() tea.Cmd {
	if m.categoryLoaded() {
		return nil
	}
	return m.loadCategory()
}

// View satisfies tea.Model. The layout itself is render; keeping it a plain
// string keeps the composing shell and this package's tests off tea.View.
func (m Model) View() tea.View { return tea.NewView(m.render()) }

func (m Model) render() string {
	if m.focus == focusDetail {
		return m.detailView()
	}

	left := style.PaneBorder(m.focus == focusCategories).Render(m.categories.View())

	var status string
	switch {
	case m.err != nil:
		// Line-collapse defensively: the layout budgets exactly one row for the
		// status, and a multi-line error would push the whole frame off-screen.
		status = errStyle.Render("error: " + textfmt.Line(m.err.Error()))
	case m.loading:
		status = statusStyle.Render("loading " + strings.ToLower(m.category.name()) + "…")
	case m.focus == focusCategories:
		status = statusStyle.Render("r: reload")
	default:
		status = statusStyle.Render(m.status + " · r: reload · /: filter")
	}
	// Cap the status (the only free-length row) to the entry pane's width: a hint
	// or error longer than the pane would widen the joined block past the
	// terminal, wrapping the frame and desyncing the renderer (mirrors talk).
	status = lipgloss.NewStyle().MaxWidth(m.listWidth()).Render(status)

	right := lipgloss.JoinVertical(lipgloss.Left, m.activeList().View(), status)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

// detailView renders the full-page entry viewport with a scroll/help status line.
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
	// Pad a short body (loading/error) to the viewport's height so the status
	// line stays pinned to the bottom of the frame instead of floating up.
	body = lipgloss.Place(m.width, m.viewport.Height(), lipgloss.Left, lipgloss.Top, body)
	status := statusStyle.Render(fmt.Sprintf("%d%%", int(m.viewport.ScrollPercent()*100)))
	status = lipgloss.NewStyle().MaxWidth(m.viewport.Width()).Render(status)
	return lipgloss.JoinVertical(lipgloss.Left, body, status)
}

func (m Model) renderNotice(d cosmo.NoticeDetail) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(d.Title) + "\n")
	b.WriteString(metaStyle.Render(d.Category+" · "+fmtDate(d.ActiveAt)) + "\n\n")
	b.WriteString(d.Content)
	if len(d.ImageURLList) > 0 {
		b.WriteString("\n\n" + labelStyle.Render(fmt.Sprintf("[%d image(s)]", len(d.ImageURLList))))
	}
	return style.Wrap(b.String(), m.viewport.Width())
}

func (m Model) renderSchedule(d cosmo.ScheduleDetail) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(d.Title) + "\n")
	b.WriteString(metaStyle.Render(fmtRangeTime(d.StartAt, d.EndAt)) + "\n")
	if d.Place != "" {
		b.WriteString(labelStyle.Render("place: ") + d.Place + "\n")
	}
	if len(d.Members) > 0 {
		names := make([]string, len(d.Members))
		for i, mem := range d.Members {
			names[i] = mem.Name
		}
		b.WriteString(labelStyle.Render("members: ") + strings.Join(names, ", ") + "\n")
	}
	if strings.TrimSpace(d.Content) != "" {
		b.WriteString("\n" + d.Content)
	}
	return style.Wrap(b.String(), m.viewport.Width())
}

// fmtDate renders the YYYY-MM-DD of an ISO-8601 timestamp in local time.
func fmtDate(iso string) string {
	return cosmo.LocalDate(iso)
}

// fmtRange renders a start-end date range (dates only), collapsing to a single
// date when both fall on the same day.
func fmtRange(startISO, endISO string) string {
	s, e := fmtDate(startISO), fmtDate(endISO)
	if s == e {
		return s
	}
	return s + " - " + e
}

// fmtRangeTime is fmtRange with wall-clock times, converted to the system's
// local timezone (the API sends schedule times with a KST +09:00 offset).
func fmtRangeTime(startISO, endISO string) string {
	st, serr := time.Parse(time.RFC3339, startISO)
	et, eerr := time.Parse(time.RFC3339, endISO)
	if serr != nil || eerr != nil {
		return fmtRange(startISO, endISO)
	}
	st, et = st.Local(), et.Local()
	if st.Format("2006-01-02") == et.Format("2006-01-02") {
		return st.Format("2006-01-02 15:04") + " - " + et.Format("15:04")
	}
	return st.Format("2006-01-02 15:04") + " - " + et.Format("2006-01-02 15:04")
}
