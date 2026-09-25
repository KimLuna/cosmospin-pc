// Package talk is the talk page: a member-channel sidebar, a message viewport,
// and a reply box, with realtime delivery over the Talk SSE feed. It mirrors
// the cosmo-util cosmo-chat UX (dedup, thumbnail→original media upgrade,
// translate, mark-read, optimistic send) with a modal, vim-style interface.
//
// Navigation follows the app-wide motions (see internal/tui/keynav); only the
// keys unique to this page are listed here.
//
//	sidebar : channel list; descend opens a channel
//	normal  : a channel is open; j/k move the highlighted message (k on the
//	          oldest message pages older history in), i enters insert mode,
//	          t translates the highlighted message, T translates every
//	          on-screen artist message, a toggles auto-translation of incoming
//	          artist messages, o opens its attachment (media or sticker),
//	          D downloads it, m switches the pane to the media gallery
//
// The media gallery is the same pane over a different list: the channel's
// photos, videos and voice messages, from the media endpoint, with the text
// between them left out. Every motion, o and D work there unchanged; m goes
// back to the messages. It has no reply box — a reply belongs to a message —
// so i does nothing there and the row the box would take goes to the list.
//
// r refetches the channel list (and the open channel) from either mode; it is
// what recovers a page whose first load failed, since the realtime list stream
// only starts once a load has succeeded.
//
// Channel state (history, translations, selection) is cached in memory per
// member, so returning to a channel restores it and only backfills the gap.
//
//	insert  : composing a reply (esc back to normal, enter sends). ctrl+v pastes
//	          the system clipboard, here as in every text box (internal/tui/clip)
package talk

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"codeberg.org/djvu/cosmo-tui/internal/cosmo"
	"codeberg.org/djvu/cosmo-tui/internal/external"
	"codeberg.org/djvu/cosmo-tui/internal/tui/keynav"
	"codeberg.org/djvu/cosmo-tui/internal/tui/listfilter"
	"codeberg.org/djvu/cosmo-tui/internal/tui/style"
	"codeberg.org/djvu/cosmo-tui/internal/tui/textfmt"
	"codeberg.org/djvu/cosmo-tui/internal/tui/uimsg"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	sidebarWidth = 30
	pageSize     = 30 // messages per history fetch (initial load, backfill, older pages)
	// mediaPageSize is the media-endpoint equivalent. Both endpoints cap take at
	// 30 server-side and silently truncate anything larger, so asking for more
	// only misstates how far one call reaches; page with a cursor instead.
	mediaPageSize = 30
	// noticeTTL is how long an unprompted status-line notice stays up; see notice.
	noticeTTL = 2 * time.Second
)

var (
	artistNameStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
	userNameStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	timeStyle       = lipgloss.NewStyle().Faint(true)
	dimStyle        = lipgloss.NewStyle().Faint(true)
	transStyle      = lipgloss.NewStyle().Italic(true).Foreground(lipgloss.Color("14"))
	mediaStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	statusStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	errStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	selMarkerStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
	sidebarBox      = lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, true, false, false).
			BorderForeground(lipgloss.Color("8"))
)

type mode int

const (
	modeSidebar mode = iota
	modeNormal
	modeInsert
)

// gate is why the channel pane is intentionally empty, as opposed to erroring.
type gate int

const (
	gateNone           gate = iota
	gateNoTalk              // this artist has no Talk feature (CHANNEL_MESSAGE_INVALID_ARTIST)
	gateNoSubscription      // Talk exists but no channel is connected (no membership)
)

type (
	channelsLoadedMsg struct {
		channels []cosmo.Channel
		err      error
	}
	messagesLoadedMsg struct {
		memberID int
		initial  bool // the channel-open load, as opposed to a backfill
		messages []cosmo.Message
	}
	olderLoadedMsg struct {
		memberID int
		messages []cosmo.Message
		err      error
	}
	mediaUpgradeMsg struct {
		memberID  int
		originals map[string]string
	}
	// mediaLoadedMsg is a page of the media gallery. initial marks the newest
	// page (the one opening the gallery, or r, asks for) as opposed to a page
	// scrolled in above what is shown.
	mediaLoadedMsg struct {
		memberID int
		initial  bool
		items    []cosmo.Message
		err      error
	}
	sseMsg struct {
		memberID int // 0 for the channel-list stream
		ev       cosmo.SSEEvent
	}
	translatedMsg struct {
		memberID int
		id       string
		t        cosmo.Translation
	}
	transFailedMsg struct {
		memberID int
		id       string
		err      error
	}
	// transReq is one queued translation, tagged with its channel so background
	// translations land in the right member's state rather than the open one.
	transReq struct {
		memberID int
		id       string
	}
	// bgSyncMsg carries messages fetched for a backgrounded (non-open) channel
	// so their artist messages can be translated ahead of the user opening it.
	bgSyncMsg struct {
		memberID int
		messages []cosmo.Message
	}
	// sentMsg is a reply the API accepted. Like sendFailedMsg it names its
	// channel: a send that answers after the user has switched away must not be
	// appended to whichever channel is open by then. Dropping it loses nothing —
	// the reply is real history, so reopening the channel backfills it.
	sentMsg struct {
		memberID int
		msg      cosmo.Message
	}
	// sendFailedMsg is a reply that the API rejected. It carries the composed
	// text (and its channel) so the reply box can be refilled instead of the
	// message silently vanishing with the error.
	sendFailedMsg struct {
		memberID int
		content  string
		err      error
	}
	downloadDoneMsg struct {
		path    string
		skipped bool // the file already existed
	}
	// welcomeLoadedMsg is a channel's welcome message, fetched once its real
	// history has run out. absent reports a channel that has none, which is a
	// normal answer rather than a failure.
	welcomeLoadedMsg struct {
		memberID int
		msg      cosmo.Message
		absent   bool
		err      error
	}
	// limitsMsg is a channel's reply allowance. Like the send results it names
	// its channel, so an answer that arrives after the user has switched away is
	// dropped instead of being shown against the wrong member.
	limitsMsg struct {
		memberID int
		limits   cosmo.ReplyLimits
	}
	// statusExpiredMsg retires a notice set by notice(). It names the text it
	// expires so a tick can't cut short whatever replaced it since.
	statusExpiredMsg struct{ text string }
	errMsg           struct{ err error }
)

// transOrigin says what asked for a translation, which is what decides how it
// shows in the status line.
type transOrigin uint8

const (
	// transAuto is auto-translate or a background channel's sync: silent, by
	// design. It fires on every incoming artist message, so a notice per arrival
	// would leave the line churning at messages the user never asked about.
	transAuto   transOrigin = iota
	transSingle             // a t on one message: "translating…"
	transBatch              // one of a T over the screen: "translating… (N left)"
)

// transPend is an outstanding translation, queued or in flight: the channel it
// belongs to (the progress line counts only the open one) and what asked for it.
type transPend struct {
	memberID int
	origin   transOrigin
}

// channelItem is one sidebar row; name is the channel's display name (nickname
// or real name per the nicknames option), resolved by refreshSidebar.
type channelItem struct {
	ch   cosmo.Channel
	name string
}

func (i channelItem) Title() string {
	if i.ch.UnreadCount > 0 {
		return fmt.Sprintf("%s (%d)", i.name, i.ch.UnreadCount)
	}
	return i.name
}
func (i channelItem) Description() string {
	preview := channelPreview(i.ch.LastMessage, i.ch.LastMessageType)
	return strings.ReplaceAll(textfmt.Normalize(preview), "\n", " ")
}

func (i channelItem) FilterValue() string { return i.name }

// channelPreview is the sidebar's one-line summary of a channel's newest
// message. Attachment messages carry no content of their own, so they'd
// otherwise leave the line blank; mark them by type instead. Markers are kept
// terse because the sidebar is a narrow column and the member's name is
// already on the line above. "" when the channel has no messages at all.
//
// sticker_text messages carry both a sticker and text, and fall through to the
// text — the same thing the official app previews for them.
func channelPreview(last, msgType string) string {
	if last != "" {
		return last
	}
	switch msgType {
	case "sticker", "sticker_text":
		return "(sticker)"
	case "image":
		return "(photo)"
	case "audio":
		return "(audio)"
	case "video":
		// Confirmed alongside audio in the media-message update, but no artist
		// has sent one yet, so the preview wording is still unverified.
		return "(video)"
	}
	return ""
}

// channelState is everything the message pane is showing: its history and the
// view state over it. It is stashed when the user switches away, so returning
// to a channel restores it instead of refetching from the API, and again when
// the media gallery takes the pane over, so the two lists can trade places
// without either losing where it was.
type channelState struct {
	messages     []cosmo.Message
	seen         map[string]bool
	translations map[string]cosmo.Translation
	latestMs     int64
	selected     int
	yOffset      int
	historyEnd   bool
	welcomeDone  bool
}

// Model is the talk page.
type Model struct {
	client      *cosmo.Client
	group       string
	downloadDir string // base dir for talk media; see config.Options
	nickname    bool   // show artist-set nicknames; false = real names

	channels []cosmo.Channel
	sidebar  list.Model

	memberID   int
	memberName string // display name (nickname or real per nicknames)
	memberOrig string // real name; downloads dir under it so paths survive nickname changes

	messages      []cosmo.Message
	seen          map[string]bool
	translations  map[string]cosmo.Translation
	transQueue    []transReq           // batch translations awaiting their request
	transPending  map[string]transPend // queued or in-flight translations, by message id
	autoTranslate bool                 // translate new artist messages as they arrive
	latestMs      int64
	selected      int   // index of the highlighted message
	msgStart      []int // start line of each message in rendered content
	totalLines    int
	loadingOlder  bool
	historyEnd    bool // no more history above the oldest loaded message
	// welcomeDone records that the welcome message has been asked for, so
	// reaching the top again doesn't refetch it. It is set when the request goes
	// out, not when it lands: one attempt per channel visit, whatever the answer.
	welcomeDone bool

	cache map[int]*channelState // per-member state kept across channel switches

	// gallery says the pane is showing the channel's media instead of its
	// messages. The two share every buffer field above — messages, selection,
	// scroll, historyEnd — so the view, the motions and o/D act on whichever
	// list is loaded without knowing which one it is; m swaps the buffers in and
	// out of chatBuf/galleryBuf. What the gallery holds are media items, not
	// messages: they carry no text, so the translation keys find nothing to ask
	// about and stay silent there of their own accord.
	gallery bool
	// chatBuf is the message buffer parked while the gallery holds the pane
	// (nil otherwise). Live messages still land in it, so what arrives while the
	// gallery is up is already there on the way back.
	chatBuf *channelState
	// galleryBuf is the media buffer parked while the messages hold the pane, so
	// a second m returns to where the gallery was rather than to its top. It
	// belongs to the open channel and is dropped when another one is opened.
	galleryBuf *channelState

	// limits is the open channel's reply allowance, and hasLimits says whether
	// it has been fetched yet. It is deliberately not cached per channel the way
	// history is: the count moves on its own (the artist posting refills it), so
	// a stashed copy would go stale, and refetching on open is one small call.
	limits    cosmo.ReplyLimits
	hasLimits bool

	viewport viewport.Model
	reply    textinput.Model
	mode     mode

	msgStream  *cosmo.Stream
	listStream *cosmo.Stream

	width, height int
	ready         bool
	gate          gate
	err           error
	status        string
	// reloading is a manual refresh awaiting its channel list, shown in the
	// status line. A reload that changes nothing changes no pixel otherwise —
	// the sidebar rebuilds to identical rows and appendMessages dedupes the
	// backfill away — so without this the key looks like it does nothing.
	reloading bool
}

// New builds the talk page for a group. downloadDir is the base directory
// media messages are saved under; nickname selects artist-set nicknames over
// real names; autoTranslate seeds the auto-translate toggle on (all per
// config.Options).
func New(client *cosmo.Client, group, downloadDir string, nickname, autoTranslate bool) Model {
	sb := style.NewList("Channels", true, true)

	in := textinput.New()
	in.Placeholder = "reply to the latest message…"
	in.Prompt = "› "
	style.PlainInput(&in)

	return Model{
		client:        client,
		group:         group,
		downloadDir:   downloadDir,
		nickname:      nickname,
		autoTranslate: autoTranslate,
		sidebar:       sb,
		reply:         in,
		seen:          map[string]bool{},
		translations:  map[string]cosmo.Translation{},
		transPending:  map[string]transPend{},
		cache:         map[int]*channelState{},
	}
}

func (m Model) Title() string { return "Talk" }

// AcceptsText reports when the page is capturing typed characters (composing a
// reply, or filtering the channel list), so the shell yields plain-letter keys.
func (m Model) AcceptsText() bool {
	return m.mode == modeInsert || m.sidebar.FilterState() == list.Filtering
}

// notice sets a status-line message that retires itself after noticeTTL. The
// status line is otherwise sticky — only a reload, a channel switch or the next
// message replaces it — which suits a notice that answers a keypress, but parks
// an unprompted one (a translation that came back empty) on screen indefinitely.
func (m *Model) notice(text string) tea.Cmd {
	m.status = text
	return tea.Tick(noticeTTL, func(time.Time) tea.Msg {
		return statusExpiredMsg{text: text}
	})
}

// setMode switches the interaction mode and restyles the sidebar so its cursor
// is bright only while the sidebar itself holds focus (modeSidebar), matching
// the room/live panes.
func (m *Model) setMode(md mode) {
	m.mode = md
	m.sidebar.SetDelegate(style.ListDelegate(md == modeSidebar, true))
}

func (m Model) Init() tea.Cmd { return m.loadChannels() }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.resize(msg.Width, msg.Height), nil

	case uimsg.GroupChanged:
		return m.switchGroup(msg.Group)

	case channelsLoadedMsg:
		m.reloading = false // whether it succeeded or not, the refresh is over
		// Diff against the old channel list (before applyChannels overwrites it)
		// to translate backgrounded chats that gained messages.
		var bg tea.Cmd
		if msg.err == nil {
			bg = m.backgroundSyncCmds(msg.channels)
		}
		m.applyChannels(msg.channels, msg.err)
		var cmd tea.Cmd
		if msg.err == nil && m.listStream == nil {
			m.listStream = m.client.StreamChannelList(context.Background(), m.group)
			cmd = m.waitList()
		}
		return m, tea.Batch(cmd, bg)

	case messagesLoadedMsg:
		if msg.memberID != m.memberID || m.gallery {
			// A stale load: for a channel we've since left, or for the message
			// list at a moment the gallery holds the pane. Either way the channel
			// backfills again on the way back to it.
			return m, nil
		}
		if msg.initial && len(msg.messages) < pageSize {
			m.historyEnd = true // the channel has less history than one page
		}
		m.appendMessages(msg.messages, true)
		m.err = nil
		cmd := m.afterNew()
		// A channel with little or no history is already at its beginning, so
		// the welcome message is due now rather than after a scroll.
		return m, tea.Batch(cmd, m.welcomeCmd())

	case olderLoadedMsg:
		if msg.memberID != m.memberID || m.gallery {
			return m, nil
		}
		m.loadingOlder = false
		if msg.err != nil {
			m.err = msg.err
			m.status = "" // the load this was reporting on is over, failed or not
			return m, nil
		}
		m.err = nil
		added := m.prependOlder(msg.messages)
		if added == 0 {
			m.historyEnd = true
			// The page above was empty, so the welcome message is what's left of
			// the beginning. Its arrival replaces the notice with the message
			// itself; the notice still stands if there is none.
			if cmd := m.welcomeCmd(); cmd != nil {
				m.status = ""
				return m, cmd
			}
			return m, m.notice("beginning of history")
		}
		m.status = ""
		m.moveSelection(-1) // continue the k that triggered the load
		if hasMedia(m.messages[:added]) {
			// Cursor the originals fetch to the page's newest message; media
			// cursors are message ids, so this covers the whole page.
			return m, m.upgradeMedia(m.memberID, m.messages[added-1].ID)
		}
		return m, nil

	case mediaLoadedMsg:
		if msg.memberID != m.memberID || !m.gallery {
			return m, nil // a page for a channel, or a pane, we've since left
		}
		m.loadingOlder = false
		m.status = ""
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		if msg.initial {
			// A first page shorter than a full one is the whole list, so there is
			// nothing above it to scroll to.
			if len(msg.items) < mediaPageSize {
				m.historyEnd = true
			}
			m.appendMessages(msg.items, true)
			return m, nil
		}
		if m.prependOlder(msg.items) == 0 {
			m.historyEnd = true
			return m, m.notice("beginning of media")
		}
		m.moveSelection(-1) // continue the k that triggered the load
		return m, nil

	case mediaUpgradeMsg:
		if msg.memberID == m.memberID {
			m.applyOriginals(msg.originals)
			m.rerender()
			m.ensureVisible() // the swap can change a message's height; keep the selection in view
		}
		return m, nil

	case sseMsg:
		return m.handleSSE(msg)

	case translatedMsg:
		pend := m.transPending[msg.id]
		delete(m.transPending, msg.id)
		if msg.memberID != m.memberID {
			// A backgrounded channel's translation: stash it so it's ready when
			// the user opens that channel; nothing on screen changed.
			if st, ok := m.cache[msg.memberID]; ok {
				st.translations[msg.id] = msg.t
			}
			return m, m.nextTranslate()
		}
		m.translations[msg.id] = msg.t
		m.err = nil
		// The progress line is derived in render, so there is nothing to advance
		// here; only the end-of-work notice needs setting, and only for a
		// translation the user asked for (auto-translate stays silent either way).
		m.status = ""
		var expire tea.Cmd
		if n, _ := m.transProgress(); n == 0 && pend.origin != transAuto && msg.t.TranslatedContent == "" {
			expire = m.notice("no translation available")
		}
		m.rerender()
		m.ensureVisible() // a new translation line grows the block; keep the selection in view
		return m, tea.Batch(expire, m.nextTranslate())

	case statusExpiredMsg:
		if m.status == msg.text {
			m.status = ""
		}
		return m, nil

	case transFailedMsg:
		delete(m.transPending, msg.id)
		if msg.memberID == m.memberID {
			m.err = msg.err // a background failure must not surface on the open channel
		}
		return m, m.nextTranslate()

	case bgSyncMsg:
		return m, m.applyBackgroundSync(msg)

	case sentMsg:
		if msg.memberID != m.memberID {
			return m, nil // a reply that landed after we left its channel
		}
		// The feed doesn't echo our own sends, so the response is the only
		// source: append it and refresh the sidebar preview. With the gallery up
		// it goes to the buffer behind it — a reply is not media.
		if m.gallery {
			m.appendParked(msg.msg)
		} else {
			m.appendMessages([]cosmo.Message{msg.msg}, true)
		}
		m.updateChannelMeta(m.memberID, msg.msg, false)
		m.refreshSidebar()
		m.err = nil
		// The reply just spent one of the allowance; ask for the new count
		// rather than decrementing, so a sticker sent from the phone (which
		// spends from the same five) can't drift us out of step.
		return m, m.loadLimits(m.memberID)

	case welcomeLoadedMsg:
		if msg.memberID != m.memberID {
			return m, nil // a welcome message for a channel we've since left
		}
		if m.gallery {
			// The greeting belongs to the messages, and they are parked. Drop it
			// and let the buffer ask again when k next reaches its top.
			if m.chatBuf != nil {
				m.chatBuf.welcomeDone = false
			}
			return m, nil
		}
		if msg.err != nil {
			m.err = msg.err
			// Let k ask again: a greeting lost to a transient error shouldn't
			// stay lost for the rest of the visit.
			m.welcomeDone = false
			return m, nil
		}
		if msg.absent {
			// Nothing to add, but the top of history was still reached.
			return m, m.notice("beginning of history")
		}
		if m.prependOlder([]cosmo.Message{msg.msg}) == 0 {
			return m, nil // already sitting above the history
		}
		// prependOlder shifts the selection down by what it inserted, which puts
		// it out of range on a channel that had no messages to select at all.
		m.clampSelection()
		m.rerender()
		m.ensureVisible()
		// Deliberately not auto-translated. Auto-translate is for messages that
		// arrive while you are watching; loaded history is left alone, and the
		// greeting is the oldest history there is. t and T still reach it.
		return m, nil

	case limitsMsg:
		if msg.memberID != m.memberID {
			return m, nil // an allowance for a channel we've since left
		}
		m.limits = msg.limits
		m.hasLimits = true
		// A cap that arrived after the user started typing still applies.
		m.clampReply()
		return m, nil

	case sendFailedMsg:
		m.err = msg.err
		// The send may have failed *because* the allowance ran out elsewhere;
		// refetch so the line stops promising replies that aren't there.
		cmd := m.loadLimits(msg.memberID)
		// Put the failed reply back so it can be edited and resent — but only
		// into the same channel, and never over text typed since.
		if msg.memberID == m.memberID && m.reply.Value() == "" {
			m.reply.SetValue(msg.content)
		}
		return m, cmd

	case downloadDoneMsg:
		text := "saved " + msg.path
		if msg.skipped {
			text = "already saved: " + msg.path
		}
		m.err = nil
		return m, m.notice(text)

	case errMsg:
		m.err = msg.err
		return m, nil

	case tea.PasteMsg:
		// Pasted text goes to whichever box is capturing it: the reply, or the
		// sidebar's filter. Insert mode can have been left while a ctrl+v read
		// ran (esc, or the reply sent), and then there is nowhere to put it.
		if m.mode == modeInsert {
			return m.insert(msg)
		}
		return m, listfilter.Paste(msg, &m.sidebar)

	case list.FilterMatchesMsg:
		return m, listfilter.Route(msg, &m.sidebar)

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

// -- layout ----------------------------------------------------------------- //

func (m Model) resize(w, h int) Model {
	m.width, m.height = w, h
	sbW := sidebarWidth
	if sbW > w/2 {
		sbW = w / 2
	}
	chatW := w - sbW - 1
	m.sidebar.SetSize(sbW-1, h)

	vpH := m.paneRows()
	if !m.ready {
		m.viewport = viewport.New(viewport.WithWidth(chatW), viewport.WithHeight(vpH))
		m.ready = true
	} else {
		m.viewport.SetWidth(chatW)
		m.viewport.SetHeight(vpH)
	}
	m.reply.SetWidth(chatW - 4)
	m.rerender()
	m.ensureVisible()
	return m
}

// paneRows is the height of the message pane: the window less the status line,
// and less the reply box when there is one. The gallery has none, so the row it
// would take goes to the list.
func (m Model) paneRows() int {
	rows := m.height - 2
	if m.gallery {
		rows = m.height - 1
	}
	if rows < 1 {
		rows = 1
	}
	return rows
}

// setPaneRows resizes the pane after the reply box has come or gone. resize
// does its own sizing, on a viewport it may still have to build.
func (m *Model) setPaneRows() {
	if m.ready {
		m.viewport.SetHeight(m.paneRows())
	}
}

// -- sidebar ---------------------------------------------------------------- //

// applyChannels stores a channel-list load result: connected (membership-held)
// channels only, with the gate reflecting why the list is empty when it is.
// A success clears any shown error: errors are transient notices, and a
// working refresh is proof the API is reachable again.
func (m *Model) applyChannels(chs []cosmo.Channel, err error) {
	if err != nil {
		if cosmo.ErrorCode(err) == cosmo.CodeInvalidArtist {
			m.gate = gateNoTalk
			m.channels = nil
			m.refreshSidebar()
			return
		}
		m.err = err
		return
	}
	m.err = nil
	connected := make([]cosmo.Channel, 0, len(chs))
	for _, ch := range chs {
		if ch.IsConnected {
			connected = append(connected, ch)
		}
	}
	m.gate = gateNone
	if len(connected) == 0 {
		m.gate = gateNoSubscription
	}
	m.channels = connected
	m.refreshSidebar()
}

// channelName is ch's display name: the artist-set nickname (MemberName), or
// the real name when nicknames is off. Falls back to the nickname on API
// responses that carry no originalName. Normalized so the sidebar column (which
// pads the name to a bordered fixed width) and the chat header measure it the
// same way the terminal draws it; a stray VS16/emoji modifier in a nickname
// otherwise pads the row a column wide and shifts the divider (see textfmt).
func (m Model) channelName(ch cosmo.Channel) string {
	if !m.nickname && ch.OriginalName != "" {
		return textfmt.Normalize(ch.OriginalName)
	}
	return textfmt.Normalize(ch.MemberName)
}

// origName is ch's real name regardless of the nicknames option, falling
// back to MemberName on API responses that carry no originalName. Normalized
// for the same width reasons as channelName.
func origName(ch cosmo.Channel) string {
	if ch.OriginalName != "" {
		return textfmt.Normalize(ch.OriginalName)
	}
	return textfmt.Normalize(ch.MemberName)
}

// refreshSidebar rebuilds the sidebar rows. Resync re-runs an active filter
// over the new rows so a filtered sidebar doesn't render empty (see
// listfilter.Resync). While the user is inside the chat pane, the cursor
// re-locks onto the open channel's row: the list keeps its cursor by index, so
// a reorder (new activity resorts by recency) would silently move the highlight
// to a different member, and ascending back out would land somewhere
// unexpected. With the sidebar itself focused the index-keeping behavior stays,
// since a jumping cursor mid-navigation would be worse.
func (m *Model) refreshSidebar() {
	sort.SliceStable(m.channels, func(i, j int) bool {
		return m.channels[i].LastMessageAt > m.channels[j].LastMessageAt
	})
	items := make([]list.Item, len(m.channels))
	for i, ch := range m.channels {
		items[i] = channelItem{ch: ch, name: m.channelName(ch)}
	}
	listfilter.Resync(&m.sidebar, m.sidebar.SetItems(items))
	if m.mode != modeSidebar && m.memberID != 0 {
		for i, it := range m.sidebar.VisibleItems() {
			if ch, ok := it.(channelItem); ok && ch.ch.MemberID == m.memberID {
				m.sidebar.Select(i)
				break
			}
		}
	}
}

// updateChannelMeta points a channel's sidebar entry at its newest message.
// Both the content and the type are stored: an attachment message has no
// content, and only the type keeps its preview from going blank.
func (m *Model) updateChannelMeta(memberID int, last cosmo.Message, read bool) {
	for i := range m.channels {
		if m.channels[i].MemberID != memberID {
			continue
		}
		if read {
			m.channels[i].UnreadCount = 0
		}
		m.channels[i].LastMessage = last.Content
		m.channels[i].LastMessageType = last.Type
		return
	}
}

// -- messages --------------------------------------------------------------- //

// appendMessages adds new (deduped) messages. When follow is true and the
// highlight was already on the last message, it advances to the new last one
// and scrolls to the bottom; otherwise it keeps the user's current selection.
func (m *Model) appendMessages(msgs []cosmo.Message, follow bool) bool {
	atBottom := len(m.messages) == 0 || m.selected >= len(m.messages)-1
	added := false
	for _, msg := range msgs {
		if msg.ID == "" || m.seen[msg.ID] {
			continue
		}
		m.seen[msg.ID] = true
		m.messages = append(m.messages, msg)
		if ms := msg.CreatedMs(); ms > m.latestMs {
			m.latestMs = ms
		}
		added = true
	}
	if !added {
		return false
	}
	if follow && atBottom {
		m.selected = len(m.messages) - 1
	}
	m.clampSelection()
	m.rerender()
	m.ensureVisible()
	return true
}

func (m *Model) clampSelection() {
	if m.selected >= len(m.messages) {
		m.selected = len(m.messages) - 1
	}
	if m.selected < 0 {
		m.selected = 0
	}
}

func (m *Model) moveSelection(delta int) {
	if len(m.messages) == 0 {
		return
	}
	m.selected += delta
	m.clampSelection()
	m.rerender()
	m.ensureVisible()
}

// pageSelection moves the highlight a viewport-height of lines up or down,
// landing on the message that contains that far line — the chat-pane analogue
// of the page motion the bubbles lists give every other pane on d/u.
func (m *Model) pageSelection(dir int) {
	if len(m.messages) == 0 || len(m.msgStart) == 0 {
		return
	}
	target := m.msgStart[m.selected] + dir*m.viewport.Height()
	// The last message starting at or before the target line.
	next := sort.SearchInts(m.msgStart, target+1) - 1
	if next == m.selected {
		next += dir // a block taller than the viewport: still move one message
	}
	m.selected = next
	m.clampSelection()
	m.rerender()
	m.ensureVisible()
}

// prependOlder inserts an older (deduped) page above the current history and
// keeps the viewport anchored on the lines that were already visible, so the
// screen doesn't jump when a page loads in. Returns how many were added.
func (m *Model) prependOlder(msgs []cosmo.Message) int {
	var fresh []cosmo.Message
	for _, msg := range msgs {
		if msg.ID == "" || m.seen[msg.ID] {
			continue
		}
		m.seen[msg.ID] = true
		fresh = append(fresh, msg)
	}
	if len(fresh) == 0 {
		return 0
	}
	// The page order for beforeCursor fetches isn't guaranteed; keep ascending.
	sort.SliceStable(fresh, func(i, j int) bool {
		return fresh[i].CreatedMs() < fresh[j].CreatedMs()
	})
	m.messages = append(fresh, m.messages...)
	m.selected += len(fresh)
	oldLines := m.totalLines
	m.rerender()
	m.viewport.SetYOffset(m.viewport.YOffset() + m.totalLines - oldLines)
	return len(fresh)
}

func (m *Model) applyOriginals(originals map[string]string) {
	for i := range m.messages {
		if url, ok := originals[m.messages[i].ID]; ok && url != "" {
			m.messages[i].MediaURL = url
		}
	}
}

// rerender rebuilds the viewport content and records each message's start line.
func (m *Model) rerender() {
	if !m.ready {
		return
	}
	// Wrap each message to the viewport width (minus the 2-col marker) so long
	// messages and quoted replies wrap instead of being clipped.
	wrapW := m.wrapWidth()
	wrap := lipgloss.NewStyle().Width(wrapW)
	blocks := make([]string, len(m.messages))
	prevDate := ""
	for i, msg := range m.messages {
		block := wrap.Render(textfmt.Normalize(m.renderMessage(msg)))
		block = withMarker(block, i == m.selected)
		// Day separator above the first message of each local date, so scrolled
		// history stays anchored to a day (timestamps alone only show HH:MM).
		if d := cosmo.LocalDate(msg.CreatedAt); d != "" && d != prevDate {
			block = "  " + dimStyle.Render("── "+d+" ──") + "\n\n" + block
			prevDate = d
		}
		blocks[i] = block
	}
	m.msgStart = make([]int, len(blocks))
	line := 0
	for i, blk := range blocks {
		m.msgStart[i] = line
		line += strings.Count(blk, "\n") + 2 // block lines + blank separator
	}
	m.totalLines = line
	m.viewport.SetContent(strings.Join(blocks, "\n\n"))
}

// ensureVisible scrolls the viewport so the highlighted message is in view.
func (m *Model) ensureVisible() {
	if !m.ready || len(m.msgStart) == 0 {
		return
	}
	if m.selected >= len(m.msgStart)-1 {
		m.viewport.GotoBottom()
		return
	}
	start := m.msgStart[m.selected]
	end := m.msgStart[m.selected+1] - 2 // last content line of the selection
	if end < start {
		end = start
	}
	top := m.viewport.YOffset()
	h := m.viewport.Height()
	if start < top {
		m.viewport.SetYOffset(start)
	} else if end > top+h-1 {
		m.viewport.SetYOffset(end - h + 1)
	}
}

// wrapWidth is the column budget for a message block: the viewport width less
// the 2-col selection marker withMarker prepends to every line.
func (m Model) wrapWidth() int {
	w := m.viewport.Width() - 2
	if w < 10 {
		w = 10
	}
	return w
}

// wrapHang word-wraps s (already width-normalized) to total width w with a
// 2-col hanging indent: the first physical line gets head, and every other
// line — whether from an explicit newline or a wrap — gets cont, so a quoted
// reply stays aligned under its ↳ arrow when it wraps instead of restarting at
// column 0. Each line is rendered with style. The content is wrapped to w-2 so
// that prefix+content stays within w and the outer viewport wrap leaves it be.
func wrapHang(s string, w int, head, cont string, style lipgloss.Style) []string {
	inner := w - 2
	if inner < 1 {
		inner = 1
	}
	body := lipgloss.NewStyle().Width(inner).Render(s)
	lines := strings.Split(body, "\n")
	out := make([]string, len(lines))
	for i, ln := range lines {
		prefix := cont
		if i == 0 {
			prefix = head
		}
		out[i] = style.Render(prefix + ln)
	}
	return out
}

func withMarker(block string, selected bool) string {
	marker := "  "
	if selected {
		marker = selMarkerStyle.Render("▎ ")
	}
	lines := strings.Split(block, "\n")
	for i, ln := range lines {
		lines[i] = marker + ln
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderMessage(msg cosmo.Message) string {
	who := msg.SenderName
	nameStyle := userNameStyle
	if msg.IsArtist() {
		nameStyle = artistNameStyle
		// The messages endpoint's senderName only ever carries the nickname, so
		// with nicknames off the channel's name (real, via channelName) is
		// the only source of the real name.
		if who == "" || (!m.nickname && m.memberName != "") {
			who = m.memberName
		}
	} else if who == "" {
		who = "You"
	}
	tr, translated := m.translations[msg.ID]

	var lines []string
	lines = append(lines, nameStyle.Render(who)+" "+timeStyle.Render(fmtTime(msg.CreatedAt)))
	if msg.Reply != nil {
		wrapW := m.wrapWidth()
		reply := textfmt.Normalize(strings.TrimRight(msg.Reply.Content, "\n"))
		// A fan can reply with a sticker and no text; without this the quote is
		// a bare arrow. The sticker itself is not shown — it is not what the
		// open/save keys act on, so naming a file here would only mislead.
		if strings.TrimSpace(reply) == "" && msg.Reply.HasSticker {
			reply = "(sticker)"
		}
		lines = append(lines, wrapHang(reply, wrapW, "↳ ", "  ", dimStyle)...)
		// The quoted reply translates alongside the message (userReply in the
		// translate response), so show it right under the quote.
		if translated && tr.ReplyTranslatedContent != "" {
			trReply := textfmt.Normalize(tr.ReplyTranslatedContent)
			lines = append(lines, wrapHang(trReply, wrapW, "  ", "  ", transStyle)...)
		}
	}
	if msg.Content != "" {
		lines = append(lines, msg.Content)
	}
	// The attachment line is held back until after the translation. sticker_text
	// messages are the only kind carrying both a file and text, and slotting the
	// filename between the text and its translation splits the pair apart.
	var attachment string
	switch {
	case msg.IsMedia() && msg.MediaURL != "":
		icon := "📸"
		switch msg.Type {
		case "video":
			icon = "🎥"
		case "audio":
			// Voice messages arrive as an .mp4 muxing the recording with a
			// still cover image, so the extension says nothing about the type.
			icon = "🎵"
		}
		attachment = icon + " " + cosmo.Basename(msg.MediaURL)
		if d := fmtDuration(msg.DurationMs); d != "" {
			attachment += " (" + d + ")"
		}
	case msg.IsSticker():
		attachment = "🧸 " + cosmo.Basename(msg.StickerURL)
	case msg.Content == "":
		// Stands in for the missing text, so it keeps the text's position.
		lines = append(lines, dimStyle.Render("(no text)"))
	}
	if translated && tr.TranslatedContent != "" {
		lines = append(lines, transStyle.Render(tr.TranslatedContent))
	}
	if attachment != "" {
		lines = append(lines, mediaStyle.Render(attachment))
	}
	return strings.Join(lines, "\n")
}

// afterNew marks the channel read and refreshes the sidebar after new messages.
func (m *Model) afterNew() tea.Cmd {
	newest := m.newestMessage()
	if newest == nil {
		return nil
	}
	m.updateChannelMeta(m.memberID, *newest, true)
	m.refreshSidebar()
	memberID, msgID := m.memberID, newest.ID
	client := m.client
	return func() tea.Msg {
		_ = client.MarkRead(context.Background(), memberID, msgID)
		return nil
	}
}

func (m Model) newestMessage() *cosmo.Message {
	var best *cosmo.Message
	for i := range m.messages {
		if best == nil || m.messages[i].CreatedMs() > best.CreatedMs() {
			best = &m.messages[i]
		}
	}
	return best
}

// latestArtistMessage is the message a reply attaches to: the artist's newest.
//
// The welcome message counts. Its negative id is synthetic, but the reply
// endpoint accepts it (confirmed against the official app), so a channel whose
// only content is the greeting is repliable rather than dead. Being the oldest
// message in any channel, it wins only when there is nothing else to reply to.
//
// The pane's own list is the right one to search: the gallery, whose items are
// media rather than messages, refuses the key that would reach a send at all.
func (m Model) latestArtistMessage() *cosmo.Message {
	var best *cosmo.Message
	for i := range m.messages {
		if m.messages[i].IsArtist() && (best == nil || m.messages[i].CreatedMs() > best.CreatedMs()) {
			best = &m.messages[i]
		}
	}
	return best
}

// -- realtime --------------------------------------------------------------- //

func (m Model) handleSSE(msg sseMsg) (tea.Model, tea.Cmd) {
	// Channel-list stream (memberID 0): any real event => reload the sidebar.
	if msg.memberID == 0 {
		reissue := m.waitList()
		if isKeepalive(msg.ev.Event) {
			return m, reissue
		}
		return m, tea.Batch(reissue, m.loadChannels())
	}
	// Per-member stream; ignore events for a channel we've left.
	if msg.memberID != m.memberID {
		return m, nil
	}
	reissue := m.waitMessages(m.memberID)
	if isKeepalive(msg.ev.Event) {
		return m, reissue
	}

	if strings.HasPrefix(msg.ev.Event, "user-message.") {
		isArtist := strings.Contains(msg.ev.Event, "artist")
		if newMsg, ok := cosmo.MessageFromEvent(msg.ev.Data, isArtist); ok {
			if isArtist {
				newMsg.SenderName = m.memberName
			} else {
				newMsg.SenderName = "You"
			}
			if m.gallery {
				return m.galleryArrival(newMsg, isArtist, reissue)
			}
			if m.appendMessages([]cosmo.Message{newMsg}, true) {
				cmds := []tea.Cmd{reissue, m.afterNew()}
				if isArtist {
					// An artist message refills the allowance, so the count on
					// screen is stale the moment one lands.
					cmds = append(cmds, m.loadLimits(m.memberID))
				}
				if cmd := m.queueAutoTranslate(newMsg); cmd != nil {
					cmds = append(cmds, cmd)
				}
				if newMsg.IsMedia() {
					cmds = append(cmds, m.upgradeMedia(m.memberID, ""))
				}
				return m, tea.Batch(cmds...)
			}
			return m, reissue
		}
	}
	// reconnected / unrecognized: backfill anything newer than what we have.
	// With the gallery up the messages are parked and the backfill would be
	// dropped on arrival; leaving the gallery issues one of its own.
	if m.gallery {
		return m, reissue
	}
	return m, tea.Batch(reissue, m.backfill(m.memberID, m.latestMs))
}

// galleryArrival takes a live message while the gallery holds the pane. It
// goes to the parked message buffer whatever it is, and to the gallery as well
// when it is media. Marking read waits for the way out, where the backfill's
// afterNew does it: the pane isn't showing these messages yet.
func (m Model) galleryArrival(newMsg cosmo.Message, isArtist bool, reissue tea.Cmd) (tea.Model, tea.Cmd) {
	m.appendParked(newMsg)
	cmds := []tea.Cmd{reissue}
	if isArtist {
		// An artist message refills the allowance, so the count is stale the
		// moment one lands — the reply box reads it from the gallery too.
		cmds = append(cmds, m.loadLimits(m.memberID))
	}
	if cmd := m.queueAutoTranslate(newMsg); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if newMsg.IsMedia() && m.appendMessages([]cosmo.Message{newMsg}, true) {
		// The event carries the thumbnail; the gallery shows originals.
		cmds = append(cmds, m.upgradeMedia(m.memberID, ""))
	}
	return m, tea.Batch(cmds...)
}

func (m Model) waitMessages(memberID int) tea.Cmd {
	s := m.msgStream
	return func() tea.Msg {
		if s == nil {
			return nil
		}
		ev, ok := <-s.Events()
		if !ok {
			return nil
		}
		return sseMsg{memberID: memberID, ev: ev}
	}
}

func (m Model) waitList() tea.Cmd {
	s := m.listStream
	return func() tea.Msg {
		if s == nil {
			return nil
		}
		ev, ok := <-s.Events()
		if !ok {
			return nil
		}
		return sseMsg{memberID: 0, ev: ev}
	}
}

// -- keys ------------------------------------------------------------------- //

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Any keypress dismisses a shown error (the key still performs its action);
	// errors are transient status-line notices, not modal state.
	m.err = nil

	// 'r' refetches the channel list, and the open channel's new messages with
	// it, from any mode that isn't capturing text. It is also the only way back
	// from a failed channel-list load: the list stream is opened on a load that
	// succeeds, so a page whose first load failed has nothing left running that
	// would ever refetch, and would otherwise stay empty until the artist is
	// switched or the app restarted.
	if msg.String() == "r" && !m.AcceptsText() {
		m.status = ""
		m.reloading = true
		cmds := []tea.Cmd{m.loadChannels()}
		if m.memberID != 0 {
			cmds = append(cmds, m.loadLimits(m.memberID))
			// Refetch whichever list the pane is showing. The messages behind the
			// gallery are left to the backfill leaving it issues.
			if m.gallery {
				cmds = append(cmds, m.loadMedia(m.memberID))
			} else {
				cmds = append(cmds, m.backfill(m.memberID, m.latestMs))
			}
		}
		return m, tea.Batch(cmds...)
	}

	switch m.mode {
	case modeSidebar:
		if m.sidebar.FilterState() != list.Filtering {
			if keynav.Descend(msg) {
				return m.openSelectedChannel()
			}
			if keynav.Ascend(msg) {
				return m, nil // leftmost pane: swallow, or the list would page on h
			}
		}
		var cmd tea.Cmd
		m.sidebar, cmd = m.sidebar.Update(msg)
		return m, cmd

	case modeInsert:
		switch msg.String() {
		case "esc":
			m.setMode(modeNormal)
			m.reply.Blur()
			return m, nil
		case "enter":
			return m.send()
		}
		var cmd tea.Cmd
		m.reply, cmd = m.reply.Update(msg)
		m.clampReply()
		return m, cmd
	}

	// modeNormal
	if keynav.Ascend(msg) {
		m.setMode(modeSidebar)
		return m, nil
	}
	switch msg.String() {
	case "i":
		// Not from the gallery: a reply attaches to a message, and the gallery's
		// items are media rather than the messages that carry them. The hint line
		// doesn't offer the key there, so refusing it is silent — and it is what
		// lets latestArtistMessage read the pane's own list.
		if m.memberID != 0 && !m.gallery {
			m.setMode(modeInsert)
			m.reply.Focus()
		}
		return m, nil
	case "j", "down":
		m.moveSelection(1)
		return m, nil
	case "k", "up":
		if m.selected == 0 && len(m.messages) > 0 {
			return m.loadOlder()
		}
		m.moveSelection(-1)
		return m, nil
	case "d", "pgdown":
		m.pageSelection(1)
		return m, nil
	case "u", "pgup":
		if m.selected == 0 && len(m.messages) > 0 {
			return m.loadOlder()
		}
		m.pageSelection(-1)
		return m, nil
	case "g":
		m.selected = 0
		m.clampSelection()
		m.rerender()
		m.ensureVisible()
		return m, nil
	case "G":
		m.selected = len(m.messages) - 1
		m.clampSelection()
		m.rerender()
		m.ensureVisible()
		return m, nil
	case "t":
		return m.translateSelected()
	case "T":
		return m.translateVisible()
	case "a":
		m.autoTranslate = !m.autoTranslate
		return m, nil
	case "m":
		return m.toggleGallery()
	case "o":
		return m, m.openSelectedMedia()
	case "D":
		return m, m.downloadSelectedMedia()
	}
	return m, nil
}

func (m Model) openSelectedChannel() (tea.Model, tea.Cmd) {
	it, ok := m.sidebar.SelectedItem().(channelItem)
	if !ok {
		return m, nil
	}
	if it.ch.MemberID == m.memberID {
		// Already open: return to it without reloading or resetting the stream.
		m.setMode(modeNormal)
		return m, nil
	}
	return m.openChannel(it.ch.MemberID, it.name, origName(it.ch))
}

func (m Model) openChannel(memberID int, name, orig string) (tea.Model, tea.Cmd) {
	if m.msgStream != nil {
		m.msgStream.Close()
	}
	// Leave the gallery first, so what gets cached for the channel we're leaving
	// is its messages and not the media list that was over them. Both media
	// buffers belong to that channel and go with it.
	if m.gallery {
		m.gallery = false
		m.setPaneRows()
		m.restore(m.chatBuf)
		m.chatBuf = nil
	}
	m.galleryBuf = nil
	m.stashChannel()
	m.memberID = memberID
	m.memberName = name
	m.memberOrig = orig
	m.loadingOlder = false
	// Notices belong to the channel we're leaving. The translation queue does
	// not: its entries name their own channel and their results land in that
	// channel's cache, so a batch keeps draining across the switch.
	m.status = ""
	m.setMode(modeNormal)
	m.reply.SetValue("")
	m.reply.Blur()
	// The previous channel's allowance says nothing about this one, and showing
	// it until the fetch lands would be worse than showing nothing.
	m.limits = cosmo.ReplyLimits{}
	m.hasLimits = false

	load := m.loadMessages(memberID)
	restoreOffset := -1
	if st, ok := m.cache[memberID]; ok {
		// Restore the cached history and only backfill what arrived since.
		m.messages = st.messages
		m.seen = st.seen
		m.translations = st.translations
		m.latestMs = st.latestMs
		m.selected = st.selected
		m.historyEnd = st.historyEnd
		m.welcomeDone = st.welcomeDone
		m.clampSelection()
		restoreOffset = st.yOffset
		load = m.backfill(memberID, m.latestMs)
	} else {
		m.messages = nil
		m.seen = map[string]bool{}
		m.translations = map[string]cosmo.Translation{}
		m.latestMs = 0
		m.selected = 0
		m.historyEnd = false
		m.welcomeDone = false
	}
	m.rerender()
	if restoreOffset >= 0 {
		// Restore the scroll the channel had when we left it, so ensureVisible
		// positions from that baseline instead of the previous channel's offset.
		m.viewport.SetYOffset(restoreOffset)
	}
	m.ensureVisible()

	m.msgStream = m.client.StreamMessages(context.Background(), memberID)
	return m, tea.Batch(load, m.waitMessages(memberID), m.loadLimits(memberID))
}

// stashChannel saves the open channel's state into the cache so reopening it
// restores history from memory instead of refetching it.
func (m *Model) stashChannel() {
	if m.memberID == 0 || m.cache == nil {
		return
	}
	m.cache[m.memberID] = m.buffer()
}

// buffer captures what the pane is showing — the list and the view over it —
// so it can be parked; restore puts a parked one back. Between them they are
// how the message and media lists trade the pane, and how a channel's state
// survives a switch away from it.
func (m Model) buffer() *channelState {
	return &channelState{
		messages:     m.messages,
		seen:         m.seen,
		translations: m.translations,
		latestMs:     m.latestMs,
		selected:     m.selected,
		yOffset:      m.viewport.YOffset(),
		historyEnd:   m.historyEnd,
		welcomeDone:  m.welcomeDone,
	}
}

func (m *Model) restore(st *channelState) {
	m.messages = st.messages
	m.seen = st.seen
	m.translations = st.translations
	m.latestMs = st.latestMs
	m.selected = st.selected
	m.historyEnd = st.historyEnd
	m.welcomeDone = st.welcomeDone
	m.clampSelection()
	m.rerender()
	m.viewport.SetYOffset(st.yOffset)
	m.ensureVisible()
}

// toggleGallery swaps the pane between the channel's messages and its media.
func (m Model) toggleGallery() (tea.Model, tea.Cmd) {
	if m.memberID == 0 {
		return m, nil
	}
	if m.gallery {
		return m.closeGallery()
	}
	return m.openGallery()
}

// openGallery hands the pane to the media list, parking the messages behind it.
//
// The newest page is always fetched, even when a previous visit's list is being
// restored: it is one call, it costs the restored scroll nothing (the overlap
// dedupes and anything new appends below), and it is what puts media posted
// since on screen.
func (m Model) openGallery() (tea.Model, tea.Cmd) {
	m.chatBuf = m.buffer()
	m.gallery = true
	m.setPaneRows() // the reply box goes; the list takes its row
	if m.galleryBuf != nil {
		m.restore(m.galleryBuf)
		m.galleryBuf = nil
	} else {
		m.messages = nil
		m.seen = map[string]bool{}
		// The translations map stays shared with the messages behind it, so a
		// translation still in flight lands where it will be read. Nothing in the
		// gallery renders from it — media items carry no text.
		m.latestMs = 0
		m.selected = 0
		m.historyEnd = false
		m.rerender()
		m.viewport.SetYOffset(0)
	}
	m.loadingOlder = false
	m.status = "loading media…"
	return m, m.loadMedia(m.memberID)
}

// closeGallery gives the pane back to the messages. Live messages went to the
// parked buffer while the gallery was up, but a dropped stream or an
// unrecognized event could still have left a gap, so the return backfills the
// same way reopening a cached channel does — which also marks the channel read.
func (m Model) closeGallery() (tea.Model, tea.Cmd) {
	m.galleryBuf = m.buffer()
	m.gallery = false
	m.setPaneRows() // the reply box comes back, at the list's expense
	m.restore(m.chatBuf)
	m.chatBuf = nil
	m.loadingOlder = false
	m.status = ""
	return m, tea.Batch(m.backfill(m.memberID, m.latestMs), m.loadLimits(m.memberID))
}

// appendParked adds a message to the buffer parked behind the gallery, so what
// arrives while the media list is up isn't missed. Only the buffer is touched:
// the pane is showing something else, so there is nothing to rerender and
// nothing to mark read yet.
func (m *Model) appendParked(msg cosmo.Message) {
	st := m.chatBuf
	if st == nil || msg.ID == "" || st.seen[msg.ID] {
		return
	}
	st.seen[msg.ID] = true
	st.messages = append(st.messages, msg)
	if ms := msg.CreatedMs(); ms > st.latestMs {
		st.latestMs = ms
	}
}

// insert hands pasted text to the reply box, which drops it in at the cursor
// with tabs and newlines flattened to spaces — the box is one row, and a Talk
// reply is a single line of text either way.
func (m Model) insert(msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.reply, cmd = m.reply.Update(msg)
	m.clampReply()
	return m, cmd
}

// welcomeCmd fetches the welcome message once, at the moment the channel's real
// history runs out — which is when it becomes the only thing still missing
// above what's on screen. That moment is either an opening page shorter than a
// full one (a channel with little or no history shows it straight away) or an
// older page that came back empty.
//
// Returns nil when there is nothing to do, so callers can batch it
// unconditionally.
func (m *Model) welcomeCmd() tea.Cmd {
	// The greeting is the top of the message history; the gallery's list ends
	// at the oldest media and has nothing above it.
	if m.memberID == 0 || m.gallery || !m.historyEnd || m.welcomeDone {
		return nil
	}
	m.welcomeDone = true
	return m.loadWelcome(m.memberID)
}

func (m Model) loadWelcome(memberID int) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		msg, ok, err := client.WelcomeMessage(context.Background(), memberID)
		if err != nil {
			return welcomeLoadedMsg{memberID: memberID, err: err}
		}
		if !ok {
			return welcomeLoadedMsg{memberID: memberID, absent: true}
		}
		return welcomeLoadedMsg{memberID: memberID, msg: msg}
	}
}

// loadLimits fetches the channel's reply allowance. It is issued on opening a
// channel, after a send spends one, and when an artist message refills them.
func (m Model) loadLimits(memberID int) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		lim, err := client.ReplyCount(context.Background(), memberID)
		if err != nil {
			return errMsg{err: err}
		}
		return limitsMsg{memberID: memberID, limits: lim}
	}
}

// textMaxLength is the reply cap the channel reports, or 0 when the allowance
// hasn't been fetched (or the server named no cap), which means "don't clamp"
// and leaves the API as the only authority on length.
func (m Model) textMaxLength() int {
	if !m.hasLimits {
		return 0
	}
	return m.limits.TextMaxLength
}

// utf16Len is the length of s in UTF-16 code units.
//
// That is the unit the cap is counted in: the official app's reply box is a
// React Native TextInput whose maxLength counts code units, so an emoji outside
// the BMP spends two of the 200 there. Counting runes here would accept strings
// that box rejects, and quite likely that the API rejects too.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xFFFF {
			n++
		}
	}
	return n
}

// truncUTF16 cuts s down to at most limit UTF-16 code units, always on a rune
// boundary — a surrogate pair is dropped whole rather than half-spent.
func truncUTF16(s string, limit int) string {
	n := 0
	for i, r := range s {
		w := 1
		if r > 0xFFFF {
			w = 2
		}
		if n+w > limit {
			return s[:i]
		}
		n += w
	}
	return s
}

// clampReply trims the reply box back to the channel's cap after an edit.
// bubbles' own CharLimit counts runes, so it can't express this cap; the clamp
// is applied after every edit that can grow the value (typing and pasting).
func (m *Model) clampReply() {
	limit := m.textMaxLength()
	if limit <= 0 {
		return
	}
	v := m.reply.Value()
	if utf16Len(v) <= limit {
		return
	}
	m.reply.SetValue(truncUTF16(v, limit))
}

// replyCounter is the "chars [n/200]" the status line shows while composing, or
// "" when the channel never reported a cap.
func (m Model) replyCounter() string {
	limit := m.textMaxLength()
	if limit <= 0 {
		return ""
	}
	return fmt.Sprintf("chars [%d/%d]", utf16Len(m.reply.Value()), limit)
}

// replyAllowance is the "replies [n/5]" the status line shows while composing,
// or "" until the allowance is known. A channel with nothing left carries the
// recharge time inside the brackets — "when can I write again" is not a question
// until then, and it is the other half of an answer that would otherwise just
// read zero.
func (m Model) replyAllowance() string {
	if !m.hasLimits {
		return ""
	}
	inner := fmt.Sprintf("%d/%d", m.limits.RemainingCount, m.limits.MaxCount)
	if !m.limits.CanReply || m.limits.RemainingCount <= 0 {
		if when := fmtReset(m.limits.NextResetAt); when != "" {
			inner += ", recharges " + when
		}
	}
	return "replies [" + inner + "]"
}

// fmtReset renders an allowance reset timestamp as a local "Jan 2 15:04". The
// window runs 144 hours, so unlike message times it needs its date. Returns ""
// for the empty timestamp the API sends when no window is open.
func fmtReset(iso string) string {
	if iso == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return ""
	}
	return t.Local().Format("Jan 2 15:04")
}

func (m Model) send() (tea.Model, tea.Cmd) {
	content := strings.TrimSpace(m.reply.Value())
	if content == "" {
		return m, nil
	}
	// Spend a reply only when the channel says one is left, and say nothing
	// about it: the status line beside the box already reads replies [0/5,
	// recharges …], which is both the reason and the answer. Silent, the way
	// enter on an empty box is.
	if m.hasLimits && !m.limits.CanReply {
		return m, nil
	}
	target := m.latestArtistMessage()
	if target == nil {
		// Sends are replies to a specific message (SendReply), so a channel the
		// artist has never posted in has nothing to attach one to. The typed text
		// stays in the box: the artist's first message makes it sendable.
		return m, m.notice("nothing to reply to yet")
	}
	// The reply is on its way, so the box has served its purpose: empty it and
	// hand the keys back, the same exit esc gives. Only this path leaves insert
	// mode — enter on an empty box, or with nothing to reply to, keeps composing.
	m.reply.SetValue("")
	m.setMode(modeNormal)
	m.reply.Blur()
	memberID, msgID := m.memberID, target.ID
	client := m.client
	return m, func() tea.Msg {
		sent, err := client.SendReply(context.Background(), memberID, msgID, content)
		if err != nil {
			return sendFailedMsg{memberID: memberID, content: content, err: err}
		}
		return sentMsg{memberID: memberID, msg: sent}
	}
}

func (m Model) translateSelected() (tea.Model, tea.Cmd) {
	if m.selected < 0 || m.selected >= len(m.messages) {
		return m, nil
	}
	msg := m.messages[m.selected]
	// Nothing to ask the API for, and nothing worth saying about it: your own
	// messages are already in your language, and a photo or a lone sticker has no
	// text at all (the API answers those with an empty translation). T skips both
	// for the same reasons. A sticker_text message carries text alongside its
	// sticker and translates normally.
	if !msg.IsArtist() || msg.Content == "" {
		return m, nil
	}
	if t, ok := m.translations[msg.ID]; ok && t.TranslatedContent != "" {
		return m, nil // already translated
	}
	if _, pending := m.transPending[msg.ID]; pending {
		return m, nil // a batch request for it is already queued or in flight
	}
	memberID, msgID := m.memberID, msg.ID
	// Registered like a queued request, but sent straight away so a single t
	// stays snappy. The entry drives the progress line, and only a
	// translatedMsg/transFailedMsg clears it — hence no errMsg on failure.
	m.transPending[msgID] = transPend{memberID: memberID, origin: transSingle}
	m.status = ""
	client := m.client
	return m, func() tea.Msg {
		t, err := client.Translate(context.Background(), memberID, msgID)
		if err != nil {
			return transFailedMsg{memberID: memberID, id: msgID, err: err}
		}
		return translatedMsg{memberID: memberID, id: msgID, t: t}
	}
}

// translateVisible queues every untranslated on-screen artist message for
// translation. Scoping the batch to the viewport keeps it to a screenful no
// matter how much history is loaded; scroll and press T again for more.
func (m Model) translateVisible() (tea.Model, tea.Cmd) {
	if len(m.messages) == 0 || len(m.msgStart) == 0 {
		return m, nil
	}
	for _, i := range m.visibleIndices() {
		msg := m.messages[i]
		if !msg.IsArtist() || msg.Content == "" {
			continue
		}
		if _, done := m.translations[msg.ID]; done {
			continue
		}
		if _, pending := m.transPending[msg.ID]; pending {
			continue
		}
		m.transPending[msg.ID] = transPend{memberID: m.memberID, origin: transBatch}
		m.transQueue = append(m.transQueue, transReq{memberID: m.memberID, id: msg.ID})
	}
	// Queueing nothing means the screen is already translated. That needs no
	// notice: it is what a T that does nothing looks like, and a no-op t is
	// silent too.
	m.status = ""
	return m, m.nextTranslate()
}

// queueAutoTranslate enqueues a newly arrived artist message for translation
// when auto-translate is on. Returns nil when there is nothing to request or
// a running request will chain into the queue.
func (m *Model) queueAutoTranslate(msg cosmo.Message) tea.Cmd {
	if !m.autoTranslate || !msg.IsArtist() || msg.Content == "" {
		return nil
	}
	if _, done := m.translations[msg.ID]; done {
		return nil
	}
	if _, pending := m.transPending[msg.ID]; pending {
		return nil
	}
	m.transPending[msg.ID] = transPend{memberID: m.memberID}
	m.transQueue = append(m.transQueue, transReq{memberID: m.memberID, id: msg.ID})
	return m.nextTranslate()
}

// queueBackgroundTranslate is the background analogue of queueAutoTranslate: it
// enqueues a new artist message from a non-open channel, keyed to that channel's
// member so nextTranslate targets it and the result stashes into its cache entry.
func (m *Model) queueBackgroundTranslate(memberID int, msg cosmo.Message) tea.Cmd {
	if !msg.IsArtist() || msg.Content == "" {
		return nil
	}
	if st, ok := m.cache[memberID]; ok {
		if _, done := st.translations[msg.ID]; done {
			return nil
		}
	}
	if _, pending := m.transPending[msg.ID]; pending {
		return nil
	}
	m.transPending[msg.ID] = transPend{memberID: memberID}
	m.transQueue = append(m.transQueue, transReq{memberID: memberID, id: msg.ID})
	return m.nextTranslate()
}

// transProgress reports the open channel's outstanding user-asked-for
// translations — queued and in flight — and whether any of them belongs to a T
// batch. Auto-translate's are left out entirely: they're silent (see
// transAuto), so counting them would put a notice on the line anyway.
//
// The count is per channel rather than the length of the shared queue because
// batches keep draining across a channel switch, their results landing in the
// target's cache.
func (m Model) transProgress() (n int, batch bool) {
	for _, p := range m.transPending {
		if p.memberID != m.memberID || p.origin == transAuto {
			continue
		}
		n++
		batch = batch || p.origin == transBatch
	}
	return n, batch
}

// nextTranslate pops the next queued translation and requests it. The queue
// drains one request at a time so a T over a full screen doesn't burst
// concurrent calls at the API; each result chains the next request.
func (m *Model) nextTranslate() tea.Cmd {
	if len(m.transQueue) == 0 {
		return nil
	}
	// transPending counts queued plus in-flight requests, so any excess over the
	// queue means one is already running: let that one chain the next rather
	// than starting a second here.
	if len(m.transPending) > len(m.transQueue) {
		return nil
	}
	req := m.transQueue[0]
	m.transQueue = m.transQueue[1:]
	client := m.client
	return func() tea.Msg {
		t, err := client.Translate(context.Background(), req.memberID, req.id)
		if err != nil {
			return transFailedMsg{memberID: req.memberID, id: req.id, err: err}
		}
		return translatedMsg{memberID: req.memberID, id: req.id, t: t}
	}
}

// visibleIndices returns the indices of messages at least partly on screen.
func (m Model) visibleIndices() []int {
	top := m.viewport.YOffset()
	bottom := top + m.viewport.Height() - 1
	var idx []int
	for i, start := range m.msgStart {
		end := m.totalLines
		if i+1 < len(m.msgStart) {
			end = m.msgStart[i+1]
		}
		end -= 2 // drop the blank separator after the block
		if start <= bottom && end >= top {
			idx = append(idx, i)
		}
	}
	return idx
}

func (m Model) openSelectedMedia() tea.Cmd {
	if m.selected < 0 || m.selected >= len(m.messages) {
		return nil
	}
	msg := m.messages[m.selected]
	url := msg.Attachment()
	if url == "" {
		return nil
	}
	target := external.LocalOrURL(m.mediaPath(msg), url)
	return func() tea.Msg {
		_ = external.OpenURL(target)
		return nil
	}
}

// mediaPath is the on-disk destination a message's attachment downloads to.
// Open and download derive it identically so viewing an already-saved
// attachment reuses the local file instead of hitting the CDN again.
func (m Model) mediaPath(msg cosmo.Message) string {
	url := msg.Attachment()
	if url == "" {
		return ""
	}
	if msg.IsSticker() {
		return filepath.Join(m.downloadDir, "stickers", cosmo.Basename(url))
	}
	return filepath.Join(m.downloadDir, "cosmo-"+m.group, m.memberOrig, cosmo.Basename(url))
}

// downloadSelectedMedia saves the highlighted message's attachment, skipping it
// if already present (mirroring the room page's dedupe-by-basename behavior).
//
// Media is unique to the member who sent it and lands under
// <downloadDir>/cosmo-<group>/<member>; the member dir is always the real name
// (memberOrig), since nicknames change and download paths mustn't. Stickers are
// a global asset pool that every member of every group draws from, so they go
// to a shared <downloadDir>/stickers instead — filing them per member would
// save the same file once per member who ever sent it.
func (m Model) downloadSelectedMedia() tea.Cmd {
	if m.selected < 0 || m.selected >= len(m.messages) {
		return nil
	}
	msg := m.messages[m.selected]
	url := msg.Attachment()
	if url == "" {
		return nil
	}
	dest := m.mediaPath(msg)
	client := m.client
	return func() tea.Msg {
		if _, err := os.Stat(dest); err == nil {
			return downloadDoneMsg{path: dest, skipped: true}
		}
		if err := client.Download(context.Background(), url, dest); err != nil {
			return errMsg{err}
		}
		return downloadDoneMsg{path: dest}
	}
}

// -- commands --------------------------------------------------------------- //

func (m Model) loadChannels() tea.Cmd {
	client, group := m.client, m.group
	return func() tea.Msg {
		chs, err := client.ListChannels(context.Background(), group)
		return channelsLoadedMsg{channels: chs, err: err}
	}
}

// loadOlder pages one screenful of older history in, keyed off the oldest
// loaded message's cursor. Reached by pressing k on the oldest message.
func (m Model) loadOlder() (tea.Model, tea.Cmd) {
	if m.gallery {
		return m.loadOlderMedia()
	}
	if m.memberID == 0 || len(m.messages) == 0 || m.loadingOlder {
		return m, nil
	}
	if m.historyEnd {
		// History is exhausted, but the greeting above it may not have been
		// fetched yet — or its fetch failed. Either way k is what asks again.
		if cmd := m.welcomeCmd(); cmd != nil {
			m.status = ""
			return m, cmd
		}
		return m, m.notice("beginning of history")
	}
	m.loadingOlder = true
	m.status = "loading older messages…"
	before := m.messages[0].CreatedMs()
	if c, err := strconv.ParseInt(m.messages[0].Cursor, 10, 64); err == nil && c > 0 {
		before = c
	}
	memberID := m.memberID
	client := m.client
	return m, func() tea.Msg {
		msgs, err := client.FetchMessages(context.Background(), memberID, pageSize, before, 0)
		return olderLoadedMsg{memberID: memberID, messages: msgs, err: err}
	}
}

func (m Model) loadMessages(memberID int) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		msgs, err := client.FetchMessages(context.Background(), memberID, pageSize, 0, 0)
		if err != nil {
			return errMsg{err}
		}
		if hasMedia(msgs) {
			if originals, err := client.MediaOriginals(context.Background(), memberID, mediaPageSize, ""); err == nil {
				for i := range msgs {
					if url, ok := originals[msgs[i].ID]; ok && url != "" {
						msgs[i].MediaURL = url
					}
				}
			}
		}
		return messagesLoadedMsg{memberID: memberID, initial: true, messages: msgs}
	}
}

// loadMedia fetches the newest page of the gallery's list.
func (m Model) loadMedia(memberID int) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		items, err := client.MediaMessages(context.Background(), memberID, mediaPageSize, "")
		return mediaLoadedMsg{memberID: memberID, initial: true, items: items, err: err}
	}
}

// loadOlderMedia is loadOlder for the gallery: one page above what is shown,
// cursored on the oldest item. The endpoint's cursor is inclusive, so that item
// comes back with the page and prependOlder dedupes it away; a page of nothing
// but the repeat is how the end of the media announces itself (see
// cosmo.MediaMessages — the response's hasMoreAfter flag can't).
func (m Model) loadOlderMedia() (tea.Model, tea.Cmd) {
	if m.memberID == 0 || len(m.messages) == 0 || m.loadingOlder {
		return m, nil
	}
	if m.historyEnd {
		return m, m.notice("beginning of media")
	}
	m.loadingOlder = true
	m.status = "loading older media…"
	after := m.messages[0].ID
	if c := m.messages[0].Cursor; c != "" {
		after = c
	}
	memberID := m.memberID
	client := m.client
	return m, func() tea.Msg {
		items, err := client.MediaMessages(context.Background(), memberID, mediaPageSize, after)
		return mediaLoadedMsg{memberID: memberID, items: items, err: err}
	}
}

// upgradeMedia swaps thumbnail URLs for full-resolution originals. after ""
// covers the newest media; an older message page passes its newest message id
// so the fetch is cursored to exactly that page (plus older spillover).
func (m Model) upgradeMedia(memberID int, after string) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		originals, err := client.MediaOriginals(context.Background(), memberID, mediaPageSize, after)
		if err != nil {
			return nil
		}
		return mediaUpgradeMsg{memberID: memberID, originals: originals}
	}
}

// backgroundSyncCmds fetches new messages for backgrounded (already-opened)
// channels that gained activity since the last channel-list load, so their
// artist messages can be auto-translated ahead of the user switching to them.
// newChannels is the fresh list; m.channels still holds the previous one.
func (m *Model) backgroundSyncCmds(newChannels []cosmo.Channel) tea.Cmd {
	if !m.autoTranslate {
		return nil
	}
	// LastMessageAt is an ISO timestamp string, monotonic and lexicographically
	// sortable (the sidebar sorts on it directly), so string compare detects a
	// newer last message.
	prev := make(map[int]string, len(m.channels))
	for _, ch := range m.channels {
		prev[ch.MemberID] = ch.LastMessageAt
	}
	var cmds []tea.Cmd
	for _, ch := range newChannels {
		if !ch.IsConnected || ch.MemberID == m.memberID {
			continue // unsubscribed, or the open channel (handled by its live stream)
		}
		st, cached := m.cache[ch.MemberID]
		if !cached || ch.LastMessageAt <= prev[ch.MemberID] {
			continue // never opened this session, or no new messages
		}
		cmds = append(cmds, m.syncBackground(ch.MemberID, st.latestMs))
	}
	return tea.Batch(cmds...)
}

// syncBackground fetches a backgrounded channel's messages newer than afterMs.
// Like backfill, but yields a bgSyncMsg and swallows errors (it's background).
func (m Model) syncBackground(memberID int, afterMs int64) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		msgs, err := client.FetchMessages(context.Background(), memberID, pageSize, 0, afterMs)
		if err != nil {
			return nil
		}
		return bgSyncMsg{memberID: memberID, messages: msgs}
	}
}

// applyBackgroundSync merges a backgrounded channel's new messages into its
// cached state and queues their artist messages for translation. Merging (not
// just translating) keeps the cache complete so opening the channel restores the
// messages and their translations without a gap.
func (m *Model) applyBackgroundSync(msg bgSyncMsg) tea.Cmd {
	st, ok := m.cache[msg.memberID]
	if !ok || msg.memberID == m.memberID {
		return nil // opened since the fetch was issued; its live stream now owns it
	}
	var cmds []tea.Cmd
	for _, newMsg := range msg.messages {
		if newMsg.ID == "" || st.seen[newMsg.ID] {
			continue
		}
		st.seen[newMsg.ID] = true
		st.messages = append(st.messages, newMsg)
		if ms := newMsg.CreatedMs(); ms > st.latestMs {
			st.latestMs = ms
		}
		if cmd := m.queueBackgroundTranslate(msg.memberID, newMsg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

func (m Model) backfill(memberID int, afterMs int64) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		msgs, err := client.FetchMessages(context.Background(), memberID, pageSize, 0, afterMs)
		if err != nil {
			return nil
		}
		return messagesLoadedMsg{memberID: memberID, messages: msgs}
	}
}

func (m Model) switchGroup(group string) (tea.Model, tea.Cmd) {
	if m.msgStream != nil {
		m.msgStream.Close()
		m.msgStream = nil
	}
	if m.listStream != nil {
		m.listStream.Close()
		m.listStream = nil
	}
	m.group = group
	m.channels = nil
	m.gate = gateNone
	m.err = nil
	m.status = ""
	m.memberID = 0
	m.memberName = ""
	m.memberOrig = ""
	m.messages = nil
	m.seen = map[string]bool{}
	m.translations = map[string]cosmo.Translation{}
	m.transQueue = nil
	m.transPending = map[string]transPend{}
	m.latestMs = 0
	m.selected = 0
	m.loadingOlder = false
	m.reloading = false
	m.historyEnd = false
	m.gallery = false
	m.chatBuf = nil
	m.galleryBuf = nil
	m.cache = map[int]*channelState{}
	m.setPaneRows()
	m.setMode(modeSidebar)
	m.reply.Blur()
	listfilter.Resync(&m.sidebar, m.sidebar.SetItems(nil))
	m.rerender()
	return m, m.loadChannels()
}

// -- view ------------------------------------------------------------------- //

// View satisfies tea.Model. The layout itself is render; keeping it a plain
// string keeps the composing shell and this package's tests off tea.View.
func (m Model) View() tea.View { return tea.NewView(m.render()) }

func (m Model) render() string {
	if !m.ready {
		return "loading…"
	}
	if notice := m.gateNotice(); notice != "" {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
			dimStyle.Render(notice))
	}
	left := sidebarBox.Render(m.sidebar.View())

	var status string
	switch {
	case m.err != nil:
		// Line-collapse defensively: the layout budgets exactly one row for the
		// status, and a multi-line error would push the whole frame off-screen.
		status = errStyle.Render("error: " + textfmt.Line(m.err.Error()))
	case m.mode == modeInsert:
		// Text entry is the exception to the no-navigation-hints rule: while the
		// reply box has the keys, esc/enter aren't the uniform motions. It sits
		// above the reload notice: the keys the reply box needs matter more than
		// a refresh running behind it. A notice still takes the front of the line
		// — enter answering with "nothing to reply to yet" is the only sign the
		// send didn't happen, and it is raised from insert mode in the first place.
		hint := "esc: back · enter: send"
		// Both limits sit ahead of the keys: they are the reason to look at this
		// line while typing. They show only here, with the box open — neither
		// says anything actionable until there is a reply being written.
		if counter := m.replyCounter(); counter != "" {
			hint = counter + " · " + hint
		}
		if allowance := m.replyAllowance(); allowance != "" {
			hint = allowance + " · " + hint
		}
		if m.status != "" {
			hint = m.status + " · " + hint
		}
		status = statusStyle.Render(hint)
	case m.reloading:
		status = statusStyle.Render("reloading…")
	case m.memberID == 0:
		status = statusStyle.Render("r: reload · /: filter")
	case m.gallery:
		// The gallery names itself here: the pane holds the same message blocks
		// the chat does, so without the line there is nothing to say which list
		// is on screen. The translate keys are left out — media carries no text
		// for them to work on — and so is i, which the gallery refuses.
		hint := "media gallery · m: chat · o/D: open/save attachment · r: reload"
		notice := m.status
		if notice == "" && len(m.messages) == 0 {
			// An empty pane otherwise reads as a channel that failed to load.
			notice = "no media in this channel"
		}
		if notice != "" {
			hint = notice + " · " + hint
		}
		status = statusStyle.Render(hint)
	default:
		auto := "off"
		if m.autoTranslate {
			auto = "on"
		}
		hint := "i: reply · t/T: translate one/screen · a: auto [" + auto +
			"] · o/D: open/save attachment · m: media · r: reload"
		// Translation progress is derived from the pending set instead of stored
		// in status, so it can't outlive the requests it describes: leaving the
		// channel, or the batch finishing, drops it on the next frame.
		notice := m.status
		if n, batch := m.transProgress(); n > 0 {
			notice = "translating…"
			if batch {
				notice = fmt.Sprintf("translating… (%d left)", n)
			}
		}
		if notice != "" {
			hint = notice + " · " + hint
		}
		status = statusStyle.Render(hint)
	}

	// Cap the status (the only free-length line) to the chat width so no frame
	// row reaches the full terminal width: rows keep one column of slack, which
	// absorbs characters the terminal renders a column wider than lipgloss
	// measures (see textfmt) instead of wrapping and desyncing the renderer.
	status = lipgloss.NewStyle().MaxWidth(m.viewport.Width()).Render(status)

	// The gallery has no reply box, and its pane is a row taller for it.
	rows := []string{m.viewport.View()}
	if !m.gallery {
		replyView := dimStyle.Render("(select a channel)")
		if m.memberID != 0 {
			replyView = m.reply.View()
		}
		rows = append(rows, replyView)
	}
	right := lipgloss.JoinVertical(lipgloss.Left, append(rows, status)...)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

// gateNotice is the friendly full-pane text for an intentionally empty page.
func (m Model) gateNotice() string {
	switch m.gate {
	case gateNoTalk:
		return "Talk isn't available for " + m.group
	case gateNoSubscription:
		return "Talk requires a membership subscription\npurchase a member pass in the COSMO app to chat"
	}
	return ""
}

// -- helpers ---------------------------------------------------------------- //

func isKeepalive(event string) bool {
	switch event {
	case "connected", "ping", "keep-alive", "message":
		return true
	}
	return false
}

func hasMedia(msgs []cosmo.Message) bool {
	for _, m := range msgs {
		if m.IsMedia() {
			return true
		}
	}
	return false
}

// fmtDuration renders a media length in milliseconds as m:ss (h:mm:ss past an
// hour). Seconds truncate, the way media players show them, but a clip shorter
// than a second floors to 0:01 rather than 0:00. Returns "" for 0, which is
// both stills and any feed that omitted the metadata — the caller then shows
// the bare filename.
func fmtDuration(ms int) string {
	if ms <= 0 {
		return ""
	}
	secs := ms / 1000
	if secs == 0 {
		secs = 1
	}
	h, m, s := secs/3600, secs/60%60, secs%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func fmtTime(iso string) string {
	// "2026-07-08T07:49:02.439Z" -> "17:49" (API sends UTC; show local time)
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return ""
	}
	return t.Local().Format("15:04")
}
