// Package room is the room page: a member list on the left, that member's posts
// on the right, with external media viewing and downloading. The group's posts
// are fetched once and filtered client-side by the selected member (the app's
// own room scraper works the same way - one request, bucket by author). Each
// group's posts are cached for the session; r refetches.
package room

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"codeberg.org/djvu/cosmo-tui/internal/cosmo"
	"codeberg.org/djvu/cosmo-tui/internal/external"
	"codeberg.org/djvu/cosmo-tui/internal/members"
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
	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
	nameStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("13")) // comment authors; headerStyle (bold) marks artists
	metaStyle   = lipgloss.NewStyle().Faint(true)
	labelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	transStyle  = lipgloss.NewStyle().Italic(true).Foreground(lipgloss.Color("14")) // post translation, mirrors talk
)

type focusState int

const (
	focusMembers focusState = iota
	focusPosts
	focusDetail
)

type (
	postsLoadedMsg struct {
		group string
		posts []cosmo.Post
	}
	downloadDoneMsg struct {
		dir              string
		downloaded, skip int
	}
	commentsLoadedMsg struct {
		postID     string
		artistOnly bool
		comments   []cosmo.Comment
		err        error
	}
	postTranslatedMsg struct {
		postID string
		t      cosmo.Translation
		err    error
	}
	errMsg struct{ err error }
)

type memberItem struct{ name string }

func (i memberItem) Title() string       { return i.name }
func (i memberItem) Description() string { return "" }
func (i memberItem) FilterValue() string { return i.name }

type postItem struct {
	post       cosmo.Post
	showMember bool // the All list shows who wrote each post
}

func (i postItem) Title() string {
	title := cosmo.LocalDate(i.post.CreatedAt)
	if i.showMember {
		title += "  " + i.post.Author.Nickname
	}
	images, videos := 0, 0
	for _, md := range i.post.Media {
		if md.Kind == "video" {
			videos++
		} else {
			images++
		}
	}
	if images > 0 {
		title += fmt.Sprintf("  📷×%d", images)
	}
	if videos > 0 {
		title += fmt.Sprintf("  🎥×%d", videos)
	}
	return title
}
func (i postItem) Description() string {
	return strings.ReplaceAll(textfmt.Normalize(i.post.Content), "\n", " ")
}

// FilterValue leads with the row exactly as Title draws it — date, member —
// then spans the body, mirroring the notification list. The date matters
// because it is what a post list is usually filtered by, and leading with the
// drawn title is what puts the delegate's match highlight in the right place:
// it styles Title by indices taken from this string, so a divergence slides the
// underline away from the text the user typed. Body matches fall past the end
// of Title and simply go unhighlighted.
func (i postItem) FilterValue() string { return i.Title() + " " + i.Description() }

// Model is the room page.
type Model struct {
	client      *cosmo.Client
	group       string
	downloadDir string // base dir for post media; see config.Options

	all      []cosmo.Post
	selected string                  // selected member name, or "All"
	cache    map[string][]cosmo.Post // group -> posts, kept across group switches
	cursor   map[string]int          // "group/member" -> post cursor, kept across switches

	memberList list.Model
	postList   list.Model
	focus      focusState

	viewport    viewport.Model // full-post detail pane (focusDetail)
	detailReady bool           // viewport has been sized at least once

	// Detail-view comment state. Comments are fetched lazily when a post is
	// opened (the post's own content renders immediately). artistOnly toggles the
	// artist-comment filter and resets to false on each open. commentCache holds
	// results for the session, keyed "postID|filter".
	detailPostID    string
	artistOnly      bool
	commentsOldest  bool // sort comments oldest-first; resets to newest-first per open
	comments        []cosmo.Comment
	commentsLoading bool
	commentsErr     error
	commentCache    map[string][]cosmo.Comment

	// Post-text translations, mirroring the talk tab: fetched on 't', shown
	// beneath the original, cached per post for the session.
	translations   map[string]cosmo.Translation
	transPending   map[string]bool
	translateErr   error
	translateErrID string
	autoTranslate  bool // fetch a post's translation automatically on open

	loading       bool
	err           error
	status        string
	width, height int
}

// New builds the room page for a group and loads its posts via Init.
// downloadDir is the base directory post media is saved under; autoTranslate
// fetches a post's translation automatically when it is opened.
func New(client *cosmo.Client, group, downloadDir string, autoTranslate bool) Model {
	ml := style.NewList("Members", true, false)
	pl := style.NewList("Posts", false, true)

	m := Model{client: client, group: group, downloadDir: downloadDir,
		selected: "All", memberList: ml, postList: pl,
		cache: map[string][]cosmo.Post{}, cursor: map[string]int{},
		commentCache: map[string][]cosmo.Comment{},
		translations: map[string]cosmo.Translation{}, transPending: map[string]bool{},
		autoTranslate: autoTranslate}
	m.loadMemberItems()
	return m
}

func (m Model) Title() string { return "Room" }

// AcceptsText reports whether either list's filter is capturing keystrokes.
func (m Model) AcceptsText() bool {
	return m.memberList.FilterState() == list.Filtering ||
		m.postList.FilterState() == list.Filtering
}

func (m Model) Init() tea.Cmd { return m.load() }

// setFocus moves focus between panes and restyles both lists so only the
// focused pane shows a bright cursor.
func (m *Model) setFocus(f focusState) {
	m.focus = f
	m.memberList.SetDelegate(style.ListDelegate(f == focusMembers, false))
	m.postList.SetDelegate(style.ListDelegate(f == focusPosts, true))
}

func (m *Model) loadMemberItems() {
	items := []list.Item{memberItem{name: "All"}}
	for _, mem := range members.AllMembers(m.group) {
		items = append(items, memberItem{name: mem.Name})
	}
	// Resync so a filtered sidebar re-runs its matches over the new rows
	// instead of rendering empty (see listfilter.Resync).
	listfilter.Resync(&m.memberList, m.memberList.SetItems(items))
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.memberList.SetSize(sidebarWidth-2, msg.Height)
		m.postList.SetSize(msg.Width-sidebarWidth-1, msg.Height-1)
		// The detail pane fills the page, reserving one row for its status line.
		vh := msg.Height - 1
		if vh < 1 {
			vh = 1
		}
		if m.detailReady {
			m.viewport.SetWidth(msg.Width)
			m.viewport.SetHeight(vh)
		} else {
			m.viewport = viewport.New(viewport.WithWidth(msg.Width), viewport.WithHeight(vh))
			m.detailReady = true
		}
		if m.focus == focusDetail { // re-flow the shown post to the new width
			if post, ok := m.selectedPost(); ok {
				m.viewport.SetContent(m.renderPostDetail(post))
			}
		}
		return m, nil

	case uimsg.GroupChanged:
		m.group = msg.Group
		m.selected = "All"
		m.setFocus(focusMembers)
		m.loadMemberItems()
		m.memberList.Select(0) // keep the cursor on "All", matching m.selected
		m.err = nil
		m.commentCache = map[string][]cosmo.Comment{} // a new group's posts
		m.translations = map[string]cosmo.Translation{}
		m.transPending = map[string]bool{}
		m.translateErr, m.translateErrID = nil, ""
		if posts, ok := m.cache[m.group]; ok {
			// Already fetched this session; restore it (r refetches).
			m.all = posts
			m.loading = false
			m.applyFilter()
			return m, nil
		}
		m.all = nil
		listfilter.Resync(&m.postList, m.postList.SetItems(nil))
		m.loading = true
		return m, m.load()

	case postsLoadedMsg:
		m.cache[msg.group] = msg.posts
		if msg.group != m.group {
			return m, nil // a stale load for a group we've since left
		}
		m.loading = false
		m.err = nil
		m.all = msg.posts
		m.applyFilter()
		return m, nil

	case downloadDoneMsg:
		m.status = fmt.Sprintf("saved %d file(s) to %s (%d already present)",
			msg.downloaded, msg.dir, msg.skip)
		m.err = nil
		return m, nil

	case commentsLoadedMsg:
		// Ignore a load that finished after the user moved to another post or
		// flipped the filter; the key it was requested for no longer matches.
		if msg.postID != m.detailPostID || msg.artistOnly != m.artistOnly {
			return m, nil
		}
		m.commentsLoading = false
		m.commentsErr = msg.err
		m.comments = msg.comments
		if msg.err == nil {
			m.commentCache[commentKey(msg.postID, msg.artistOnly)] = msg.comments
		}
		if m.focus == focusDetail {
			if post, ok := m.selectedPost(); ok {
				m.viewport.SetContent(m.renderPostDetail(post)) // keep scroll offset
			}
		}
		return m, nil

	case postTranslatedMsg:
		delete(m.transPending, msg.postID)
		if msg.err != nil {
			m.translateErr, m.translateErrID = msg.err, msg.postID
		} else {
			m.translations[msg.postID] = msg.t
			if m.translateErrID == msg.postID {
				m.translateErr, m.translateErrID = nil, ""
			}
		}
		if m.focus == focusDetail && msg.postID == m.detailPostID {
			if post, ok := m.selectedPost(); ok {
				m.viewport.SetContent(m.renderPostDetail(post)) // keep scroll offset
			}
		}
		return m, nil

	case errMsg:
		m.loading = false
		m.err = msg.err
		return m, nil

	case list.FilterMatchesMsg:
		return m, listfilter.Route(msg, &m.memberList, &m.postList)

	case tea.PasteMsg:
		// Pasted text goes to whichever list is filtering, if any.
		return m, listfilter.Paste(msg, &m.memberList, &m.postList)

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
	// post list), so handle it before the reload/list routing below.
	if m.focus == focusDetail {
		if keynav.Ascend(msg) {
			m.setFocus(focusPosts)
			return m, nil
		}
		switch msg.String() {
		case "g":
			m.viewport.GotoTop()
			return m, nil
		case "G":
			m.viewport.GotoBottom()
			return m, nil
		case "a":
			m.artistOnly = !m.artistOnly
			return m.loadComments(m.detailPostID, m.artistOnly)
		case "s":
			m.commentsOldest = !m.commentsOldest
			if post, ok := m.selectedPost(); ok {
				m.viewport.SetContent(m.renderPostDetail(post)) // keep scroll offset
			}
			return m, nil
		case "t":
			return m.translatePost()
		case "o":
			return m, m.openSelected()
		case "D":
			return m, m.downloadSelected()
		}
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}

	// 'r' reloads regardless of focus (unless typing a filter). A refresh refetches
	// the whole group at once, so forget every member's cached cursor for it and
	// let each list start at the top.
	if msg.String() == "r" && !m.AcceptsText() {
		m.loading, m.err = true, nil
		m.resetCursors()
		return m, m.load()
	}

	if m.focus == focusMembers {
		if m.memberList.FilterState() != list.Filtering {
			if keynav.Descend(msg) {
				m.syncSelected() // normally a no-op: the pane already follows the cursor
				if _, ok := m.memberList.SelectedItem().(memberItem); ok {
					m.setFocus(focusPosts)
				}
				return m, nil
			}
			if keynav.Ascend(msg) {
				return m, nil // leftmost pane: swallow, or the list would page on h
			}
		}
		var cmd tea.Cmd
		m.memberList, cmd = m.memberList.Update(msg)
		m.syncSelected()
		return m, cmd
	}

	// focusPosts
	if m.postList.FilterState() != list.Filtering {
		if keynav.Ascend(msg) {
			m.setFocus(focusMembers)
			return m, nil
		}
		if keynav.Descend(msg) {
			return m.openDetail()
		}
		switch msg.String() {
		case "o":
			return m, m.openSelected()
		case "D":
			return m, m.downloadSelected()
		}
	}
	var cmd tea.Cmd
	m.postList, cmd = m.postList.Update(msg)
	return m, cmd
}

// cursorKey identifies the selected member's post list within the current group.
func (m Model) cursorKey() string { return m.group + "/" + m.selected }

// resetCursors forgets every remembered cursor in the current group, used when a
// refresh refetches the whole group's posts.
func (m *Model) resetCursors() {
	prefix := m.group + "/"
	for k := range m.cursor {
		if strings.HasPrefix(k, prefix) {
			delete(m.cursor, k)
		}
	}
}

// syncSelected makes the post pane follow the member cursor: if the selected
// member changed, stash the old member's post cursor and re-filter. All posts
// are already client-side, so this is instant.
func (m *Model) syncSelected() {
	it, ok := m.memberList.SelectedItem().(memberItem)
	if !ok || it.name == m.selected {
		return
	}
	m.stashCursor() // remember where we left the current member
	m.selected = it.name
	m.applyFilter()
}

// stashCursor records the current member's post cursor so re-entering their list
// returns to the same spot. SetItems keeps the raw cursor across a swap, so
// without this a shorter list would inherit a stale position.
func (m *Model) stashCursor() {
	if m.cursor != nil {
		m.cursor[m.cursorKey()] = m.postList.Index()
	}
}

// applyFilter rebuilds the post list for the selected member and restores that
// member's remembered cursor (0 on a first visit or after a manual refresh).
func (m *Model) applyFilter() {
	showMember := m.selected == "All"
	var items []list.Item
	for _, p := range m.all {
		if showMember || strings.EqualFold(p.Author.Nickname, m.selected) {
			items = append(items, postItem{post: p, showMember: showMember})
		}
	}
	listfilter.Resync(&m.postList, m.postList.SetItems(items))
	m.postList.Title = "Posts - " + m.selected
	m.status = fmt.Sprintf("%d post(s)", len(items))
	idx := m.cursor[m.cursorKey()]
	if idx < 0 || idx >= len(items) {
		idx = 0
	}
	m.postList.Select(idx)
}

func (m Model) selectedPost() (cosmo.Post, bool) {
	it, ok := m.postList.SelectedItem().(postItem)
	if !ok {
		return cosmo.Post{}, false
	}
	return it.post, true
}

// commentKey identifies a post's comment thread under a filter in commentCache.
func commentKey(postID string, artistOnly bool) string {
	if artistOnly {
		return postID + "|artist"
	}
	return postID + "|all"
}

// openDetail focuses the detail viewport on the selected post: its content and
// media render immediately from memory; the comment thread is fetched lazily.
// The artist-only filter resets to off on every open. A no-op if nothing is
// selected or the viewport hasn't been sized yet.
func (m Model) openDetail() (tea.Model, tea.Cmd) {
	post, ok := m.selectedPost()
	if !ok || !m.detailReady {
		return m, nil
	}
	m.detailPostID = string(post.ID)
	m.artistOnly = false
	m.commentsOldest = false
	m.translateErr, m.translateErrID = nil, ""
	m.setFocus(focusDetail)
	m.viewport.GotoTop()
	model, cmd := m.loadComments(m.detailPostID, m.artistOnly)
	if m.autoTranslate {
		tm, tcmd := model.(Model).translatePost()
		return tm, tea.Batch(cmd, tcmd)
	}
	return model, cmd
}

// translatePost fetches the open post's translation (shown beneath its text) the
// first time 't' is pressed. It mirrors the talk tab: cached per post, and a
// no-op if already translated, in flight, or the post has no text.
func (m Model) translatePost() (tea.Model, tea.Cmd) {
	post, ok := m.selectedPost()
	if !ok || post.Content == "" {
		return m, nil
	}
	id := string(post.ID)
	if _, done := m.translations[id]; done {
		return m, nil
	}
	if m.transPending[id] {
		return m, nil
	}
	m.transPending[id] = true
	m.translateErr, m.translateErrID = nil, ""
	m.viewport.SetContent(m.renderPostDetail(post)) // show the "translating…" line
	client := m.client
	return m, func() tea.Msg {
		t, err := client.RoomPostTranslation(context.Background(), id)
		return postTranslatedMsg{postID: id, t: t, err: err}
	}
}

// loadComments points the detail view at a post's comments for the given filter.
// A cached thread renders synchronously; otherwise it shows a loading state and
// returns a command that fetches the thread. Either way the viewport is
// re-rendered so the header/content stay visible.
func (m Model) loadComments(postID string, artistOnly bool) (tea.Model, tea.Cmd) {
	post, ok := m.selectedPost()
	if !ok {
		return m, nil
	}
	if cached, hit := m.commentCache[commentKey(postID, artistOnly)]; hit {
		m.comments = cached
		m.commentsLoading = false
		m.commentsErr = nil
		m.viewport.SetContent(m.renderPostDetail(post))
		return m, nil
	}
	m.comments = nil
	m.commentsLoading = true
	m.commentsErr = nil
	m.viewport.SetContent(m.renderPostDetail(post))
	client := m.client
	return m, func() tea.Msg {
		comments, err := client.RoomPostComments(context.Background(), postID, artistOnly)
		return commentsLoadedMsg{postID: postID, artistOnly: artistOnly, comments: comments, err: err}
	}
}

func (m Model) openSelected() tea.Cmd {
	post, ok := m.selectedPost()
	if !ok || len(post.Media) == 0 {
		return nil
	}
	dir := m.postDir(post)
	var targets []string
	for _, md := range post.Media {
		if md.URL != "" {
			targets = append(targets, external.LocalOrURL(filepath.Join(dir, cosmo.Basename(md.URL)), md.URL))
		}
	}
	return func() tea.Msg {
		for _, t := range targets {
			_ = external.OpenURL(t)
		}
		return nil
	}
}

// postDir is the folder a post's media downloads into. Open and download derive
// it identically so viewing already-saved media reuses the local files.
func (m Model) postDir(post cosmo.Post) string {
	return filepath.Join(m.downloadDir, "cosmo-"+m.group, post.Author.Nickname)
}

func (m Model) downloadSelected() tea.Cmd {
	post, ok := m.selectedPost()
	if !ok || len(post.Media) == 0 {
		return nil
	}
	dir := m.postDir(post)
	client := m.client
	return func() tea.Msg {
		d, s, err := client.DownloadPost(context.Background(), post, dir)
		if err != nil {
			return errMsg{err}
		}
		return downloadDoneMsg{dir: dir, downloaded: d, skip: s}
	}
}

func (m Model) load() tea.Cmd {
	client, group := m.client, m.group
	return func() tea.Msg {
		posts, err := client.RoomPosts(context.Background(), group)
		if err != nil {
			return errMsg{err}
		}
		return postsLoadedMsg{group: group, posts: posts}
	}
}

// View satisfies tea.Model. The layout itself is render; keeping it a plain
// string keeps the composing shell and this package's tests off tea.View.
func (m Model) View() tea.View { return tea.NewView(m.render()) }

func (m Model) render() string {
	if m.focus == focusDetail {
		return m.detailView()
	}

	left := style.PaneBorder(m.focus == focusMembers).Render(m.memberList.View())

	var status string
	switch {
	case m.err != nil:
		// Line-collapse defensively: the layout budgets exactly one row for the
		// status, and a multi-line error would push the whole frame off-screen.
		status = errStyle.Render("error: " + textfmt.Line(m.err.Error()))
	case m.loading:
		status = statusStyle.Render("loading posts…")
	case m.focus == focusMembers:
		status = statusStyle.Render("r: reload · /: filter")
	default:
		status = statusStyle.Render(m.status +
			" · o: open · D: download · r: reload · /: filter")
	}
	// Cap the status (the only free-length row) to the post pane's width: a hint
	// or error longer than the pane would widen the joined block past the
	// terminal, wrapping the frame and desyncing the renderer (mirrors talk).
	status = lipgloss.NewStyle().MaxWidth(m.listWidth()).Render(status)

	right := lipgloss.JoinVertical(lipgloss.Left, m.postList.View(), status)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

// listWidth is the post pane's width: the page less the sidebar and its divider.
func (m Model) listWidth() int {
	if w := m.width - sidebarWidth - 1; w > 0 {
		return w
	}
	return 1
}

// detailView renders the full-post viewport with a scroll/help status line.
func (m Model) detailView() string {
	artist := "off"
	if m.artistOnly {
		artist = "on"
	}
	sort := "newest"
	if m.commentsOldest {
		sort = "oldest"
	}
	status := statusStyle.Render(fmt.Sprintf("%d%% · t: translate · a: artist-only [%s] · s: sort [%s] · o: open · D: download",
		int(m.viewport.ScrollPercent()*100), artist, sort))
	// Cap to the viewport so a long hint truncates instead of wrapping the frame.
	status = lipgloss.NewStyle().MaxWidth(m.viewport.Width()).Render(status)
	return lipgloss.JoinVertical(lipgloss.Left, m.viewport.View(), status)
}

// renderTranslation renders the post-text translation line shown beneath the
// original, reflecting the pending/available/empty/failed states (mirrors talk).
func (m Model) renderTranslation(postID string) string {
	if tr, ok := m.translations[postID]; ok {
		if tr.TranslatedContent == "" {
			return transStyle.Render("(no translation available)") + "\n"
		}
		return transStyle.Render(tr.TranslatedContent) + "\n"
	}
	if m.transPending[postID] {
		return transStyle.Render("translating…") + "\n"
	}
	if m.translateErrID == postID && m.translateErr != nil {
		return transStyle.Render("(translation failed)") + "\n"
	}
	return ""
}

// renderPostDetail builds the scrollable body for one post: a header, the full
// (untruncated, multi-line) content, a media summary, and the comment thread
// (fetched separately, held on the model). Text is normalized and re-flowed to
// the viewport width.
func (m Model) renderPostDetail(p cosmo.Post) string {
	var b strings.Builder

	b.WriteString(headerStyle.Render(cosmo.LocalDateTime(p.CreatedAt)+"  ·  "+p.Author.Nickname) + "\n\n")
	if p.Content != "" {
		b.WriteString(p.Content + "\n")
	}
	b.WriteString(m.renderTranslation(string(p.ID)))

	if len(p.Media) > 0 {
		images, videos := 0, 0
		for _, md := range p.Media {
			if md.Kind == "video" {
				videos++
			} else {
				images++
			}
		}
		var parts []string
		if images > 0 {
			parts = append(parts, fmt.Sprintf("%d image(s)", images))
		}
		if videos > 0 {
			parts = append(parts, fmt.Sprintf("%d video(s)", videos))
		}
		if p.MediaAspectRatio != "" {
			parts = append(parts, p.MediaAspectRatio)
		}
		b.WriteString("\n" + labelStyle.Render("["+strings.Join(parts, " · ")+"]") + "\n")
	}

	b.WriteString("\n" + m.renderComments())

	return style.Wrap(b.String(), m.viewport.Width())
}

// renderComments renders the comment-thread section from the model's fetched
// comments, reflecting the loading/error/empty and artist-only states.
func (m Model) renderComments() string {
	noun := "Comments"
	if m.artistOnly {
		noun = "Artist comments"
	}
	switch {
	case m.commentsLoading:
		return labelStyle.Render("── "+noun+" · loading… ──") + "\n"
	case m.commentsErr != nil:
		return labelStyle.Render("── "+noun+" · error ──") + "\n"
	case len(m.comments) == 0:
		return labelStyle.Render("── No "+strings.ToLower(noun)+" ──") + "\n"
	}

	members := m.memberNames() // to tag artist replies (replies carry no isArtist)
	// name styles an author, bolding artists (their username in place of a tag).
	name := func(nick string, artist bool) string {
		if artist {
			return headerStyle.Render(nick)
		}
		return nameStyle.Render(nick)
	}
	var b strings.Builder
	b.WriteString(labelStyle.Render(fmt.Sprintf("── %s (%d) ──", noun, len(m.comments))) + "\n")
	for i := range m.comments {
		c := m.comments[i]
		if m.commentsOldest { // reverse the newest-first slice; replies keep their order
			c = m.comments[len(m.comments)-1-i]
		}
		if c.IsDeleted || c.IsBlinded {
			b.WriteString("\n" + metaStyle.Render("  [removed]") + "\n")
			continue
		}
		b.WriteString("\n" + name(c.Author.Nickname, c.IsArtist) + metaStyle.Render("  "+cosmo.LocalDateTime(c.CreatedAt)) + "\n")
		b.WriteString("  " + c.Content + "\n")
		for _, r := range c.Replies {
			artist := members[strings.ToLower(r.Author.Nickname)]
			b.WriteString("  └ " + name(r.Author.Nickname, artist) + metaStyle.Render("  "+cosmo.LocalDateTime(r.CreatedAt)) + "\n")
			b.WriteString("      " + r.Content + "\n")
		}
	}
	return b.String()
}

// memberNames is the lowercase set of the current group's member names, used to
// recognise an artist reply by its author nickname.
func (m Model) memberNames() map[string]bool {
	set := map[string]bool{}
	for _, mem := range members.AllMembers(m.group) {
		set[strings.ToLower(mem.Name)] = true
	}
	return set
}
