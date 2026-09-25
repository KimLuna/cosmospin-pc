// Package live is the live page: a member list on the left (with an "All"
// entry like the room page), that member's live-stream VOD history on the
// right, with view (mpv) via an external process and download (a native
// parallel HLS fetch muxed with ffmpeg). The group's whole replay history is
// fetched in one request at startup and
// bucketed per member client-side, cached for the session; r refetches. Clips
// gated behind a membership the account lacks are marked with a lock. Members
// streaming right now are marked in the member list, fetched alongside the
// replays and refreshed by r (never polled, so it can go stale mid-session).
package live

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"codeberg.org/djvu/cosmo-tui/internal/cosmo"
	"codeberg.org/djvu/cosmo-tui/internal/external"
	"codeberg.org/djvu/cosmo-tui/internal/hls"
	"codeberg.org/djvu/cosmo-tui/internal/members"
	"codeberg.org/djvu/cosmo-tui/internal/tui/keynav"
	"codeberg.org/djvu/cosmo-tui/internal/tui/listfilter"
	"codeberg.org/djvu/cosmo-tui/internal/tui/style"
	"codeberg.org/djvu/cosmo-tui/internal/tui/textfmt"
	"codeberg.org/djvu/cosmo-tui/internal/tui/uimsg"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/progress"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const sidebarWidth = 22

var (
	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
	labelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	unsafeChars = regexp.MustCompile(`[<>:"/\\|?*]`)
)

// allMember is the synthetic "All" roster entry; its APIID lands on the
// group-wide VodsByMember bucket.
var allMember = members.Member{APIID: cosmo.AllMembersID, Name: "All"}

type focusState int

const (
	focusMembers focusState = iota
	focusVods
	focusDownload // the modal download view has taken over the page
)

// dlPhase tracks where a download is in its lifecycle, driving the download
// view's rendering and which keys are accepted.
type dlPhase int

const (
	phaseResolving   dlPhase = iota // fetching the membership-gated stream URL
	phaseDownloading                // pulling fragments (progress bar active)
	phaseMuxing                     // ffmpeg combining the two tracks
	phaseSubtitles                  // video saved; fetching its subtitles
	phaseDone                       // finished; file and subtitles settled
	phaseError                      // failed; err holds why
)

// dlState is the in-flight (or just-finished) download shown by focusDownload.
type dlState struct {
	vod       cosmo.Vod
	member    string // folder/name member (author for the "All" list)
	phase     dlPhase
	done      int
	total     int
	savedPath string             // set at phaseDone; full output path
	subtitles string             // set at phaseDone; the subtitle outcome, worded
	err       error              // set at phaseError
	cancel    context.CancelFunc // cancels an in-flight fetch on 'c'
}

type (
	vodsLoadedMsg struct {
		group string
		vods  map[int][]cosmo.Vod
	}
	// liveSessionsLoadedMsg carries the set of member APIIDs streaming right
	// now; a nil set means "none, or the fetch failed" (see loadLive).
	liveSessionsLoadedMsg struct {
		group string
		ids   map[int]bool
	}
	resolvedMsg struct {
		vod      cosmo.Vod
		clip     cosmo.Clip
		download bool
	}
	// resolveFailedMsg is a download resolve that errored. It carries the vod so
	// a stale failure (from a download the user dismissed mid-resolve) can be
	// told apart from the one on screen; view resolves report plain errMsg.
	resolveFailedMsg struct {
		vod cosmo.Vod
		err error
	}
	// downloadProgressMsg reports a download phase change and doubles as the
	// element type of the Model's progress channel: muxing marks the switch from
	// fragment-fetching to the ffmpeg mux, otherwise done/total is the count.
	downloadProgressMsg struct {
		done, total int
		muxing      bool
		subtitles   bool
	}
	execDoneMsg struct {
		err       error
		savedPath string // set when a download finished; "" for mpv/view exits
		subtitles string // the subtitle outcome, worded; "" for mpv/view exits
	}
	errMsg struct{ err error }
)

type memberItem struct {
	member members.Member
	live   bool // streaming right now
}

// Title carries the live marker: this list's delegate draws no description row,
// so unlike the replay list's badges it has nowhere else to go. The marker is a
// bare base emoji (no variation selector, no ZWJ) so lipgloss and the terminal
// agree on its width; see the note on vodItem.Title.
func (i memberItem) Title() string {
	if i.live {
		return i.member.Name + " 🔴"
	}
	return i.member.Name
}
func (i memberItem) Description() string { return "" }
func (i memberItem) FilterValue() string { return i.member.Name }

type vodItem struct {
	vod        cosmo.Vod
	showMember bool // the All list shows who streamed each replay
	downloaded bool // a copy already exists in the download folder
}

func (i vodItem) Title() string {
	// Normalize the member nickname and post-content title: both are arbitrary
	// user text and can carry width-mismatched characters (ZWJ/variation-selector
	// emoji, AM vowel signs, Hangul fillers) that overflow the row and desync the
	// list renderer if drawn raw, exactly like the room/talk lists guard against.
	title := textfmt.Normalize(i.vod.Title)
	if i.showMember {
		title = textfmt.Normalize(i.vod.Member) + ": " + title
	}
	return fmt.Sprintf("%s  %s", i.vod.Date, title)
}

// Description is the muted second row: the clip's duration followed by any
// status badges. The badges live here, worded, rather than in the title so they
// read as app-supplied status and can't be mistaken for part of a member's
// stream title (which is otherwise unlabelled on a single-member list). The two
// can co-occur — a clip downloaded while a membership was active may now read as
// gated.
func (i vodItem) Description() string {
	desc := formatDuration(i.vod.Duration)
	if !i.vod.Playable {
		desc += " · 🔒"
	}
	if i.downloaded {
		desc += " · 💾"
	}
	return desc
}

// FilterValue is the row exactly as Title draws it, date included: the date is
// the most common thing to filter a replay list by, and matching the drawn text
// rune-for-rune is also what puts the delegate's match highlight in the right
// place — it styles Title by indices taken from this string, so any divergence
// slides the underline away from the text the user typed.
func (i vodItem) FilterValue() string { return i.Title() }

// Model is the live page.
type Model struct {
	client      *cosmo.Client
	group       string
	downloadDir string // base dir for vod downloads; see config.Options
	fragments   int    // parallel fragment fetches; 0 uses hls.DefaultConcurrency

	memberList list.Model
	vodList    list.Model
	focus      focusState
	current    members.Member // selected member, or allMember

	liveIDs map[int]bool // member api ids streaming now, for the current group

	all    map[int][]cosmo.Vod            // member api id -> replay list (nil until loaded)
	cache  map[string]map[int][]cosmo.Vod // group -> buckets, kept across switches
	cursor map[string]int                 // "group/member" -> replay cursor, kept across switches

	loading       bool
	err           error
	status        string
	dl            dlState                  // the download shown by focusDownload
	dlProgress    chan downloadProgressMsg // non-nil while a download runs
	prog          progress.Model           // the download view's progress bar
	width, height int
}

// New builds the live page for a group and loads its replays via Init.
// downloadDir is the base directory vods are saved under; fragments is the
// parallel fragment-fetch count (0 uses hls.DefaultConcurrency).
func New(client *cosmo.Client, group, downloadDir string, fragments int) Model {
	ml := style.NewList("Members", true, false)
	vl := style.NewList("Replays", false, true)

	m := Model{client: client, group: group, downloadDir: downloadDir, fragments: fragments,
		current: allMember, memberList: ml, vodList: vl,
		// Magenta to match the focused header, and a solid fill: bubbles
		// defaults to a half block, which draws the bar as separated stripes.
		prog: progress.New(
			progress.WithColors(lipgloss.Color("13")),
			progress.WithFillCharacters(progress.DefaultFullCharFullBlock, progress.DefaultEmptyCharBlock),
		),
		cache: map[string]map[int][]cosmo.Vod{}, cursor: map[string]int{}}
	m.loadMemberItems()
	return m
}

func (m Model) Title() string { return "Live" }

// AcceptsText reports whether either list's filter is capturing keystrokes.
func (m Model) AcceptsText() bool {
	return m.memberList.FilterState() == list.Filtering ||
		m.vodList.FilterState() == list.Filtering
}

func (m Model) Init() tea.Cmd { return tea.Batch(m.load(), m.loadLive()) }

// setFocus moves focus between panes and restyles both lists so only the
// focused pane shows a bright cursor.
func (m *Model) setFocus(f focusState) {
	m.focus = f
	m.memberList.SetDelegate(style.ListDelegate(f == focusMembers, false))
	m.vodList.SetDelegate(style.ListDelegate(f == focusVods, true))
}

// loadMemberItems rebuilds the roster rows, marking members streaming now. The
// "All" row stays unmarked: it's a bucket, not a member. Resync re-runs an
// active filter over the new rows so a filtered sidebar doesn't render empty
// (see listfilter.Resync).
func (m *Model) loadMemberItems() {
	items := []list.Item{memberItem{member: allMember}}
	for _, mem := range members.AllMembers(m.group) {
		items = append(items, memberItem{member: mem, live: m.liveIDs[mem.APIID]})
	}
	listfilter.Resync(&m.memberList, m.memberList.SetItems(items))
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.memberList.SetSize(sidebarWidth-2, msg.Height)
		m.vodList.SetSize(msg.Width-sidebarWidth-1, msg.Height-1)
		// Fit the progress bar inside the download panel (border + padding).
		if w := msg.Width - 10; w > 0 {
			m.prog.SetWidth(w)
		}
		return m, nil

	case uimsg.GroupChanged:
		m.group = msg.Group
		m.current = allMember
		m.setFocus(focusMembers)
		// Live status is per-group and too volatile to cache, so it's always
		// refetched, even when the replays come back from the cache below.
		m.liveIDs = nil
		m.loadMemberItems()
		m.memberList.Select(0) // keep the cursor on "All", matching m.current
		m.err = nil
		if vods, ok := m.cache[m.group]; ok {
			// Already fetched this session; restore it (r refetches).
			m.all = vods
			m.loading = false
			m.setVods(m.all[m.current.APIID])
			return m, m.loadLive()
		}
		m.all = nil
		listfilter.Resync(&m.vodList, m.vodList.SetItems(nil))
		m.loading = true
		return m, tea.Batch(m.load(), m.loadLive())

	case liveSessionsLoadedMsg:
		if msg.group != m.group {
			return m, nil // a stale load for a group we've since left
		}
		m.liveIDs = msg.ids
		m.loadMemberItems()
		return m, nil

	case vodsLoadedMsg:
		m.cache[msg.group] = msg.vods
		if msg.group != m.group {
			return m, nil // a stale load for a group we've since left
		}
		m.loading = false
		m.err = nil
		m.all = msg.vods
		m.setVods(m.all[m.current.APIID])
		return m, nil

	case resolvedMsg:
		m.err = nil
		if msg.download {
			// Only start the download this view is showing: the user may have
			// dismissed it during the resolve, or dismissed it and begun another
			// (whose own resolve is still in flight) — starting a stale resolve
			// would fetch the wrong replay and orphan the new one's state.
			if m.focus != focusDownload || m.dl.phase != phaseResolving ||
				msg.vod.VideoID != m.dl.vod.VideoID {
				return m, nil
			}
			cmd := m.startDownload(msg) // mutates m.dl/dlProgress first
			return m, cmd
		}
		return m, openVideo(msg.clip.VideoURL)

	case downloadProgressMsg:
		if m.focus != focusDownload || m.dl.phase == phaseDone || m.dl.phase == phaseError {
			return m, nil // cancelled/finished; ignore a trailing update
		}
		switch {
		case msg.subtitles:
			m.dl.phase = phaseSubtitles
		case msg.muxing:
			m.dl.phase = phaseMuxing
		default:
			m.dl.phase = phaseDownloading
			m.dl.done, m.dl.total = msg.done, msg.total
		}
		return m, waitProgress(m.dlProgress)

	case execDoneMsg:
		if m.focus != focusDownload {
			// An mpv/view process exited; context.Canceled is a download the
			// user already dismissed, so it is not an error to surface.
			if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
				m.err = msg.err
			}
			return m, nil
		}
		m.dlProgress = nil
		switch {
		case errors.Is(msg.err, context.Canceled):
			return m.dismissDownload() // shouldn't happen ('c' dismisses first)
		case msg.err != nil:
			m.dl.phase, m.dl.err = phaseError, msg.err
		default:
			m.dl.phase = phaseDone
			m.dl.savedPath = msg.savedPath
			m.dl.subtitles = msg.subtitles
			// The download view is modal, so the list cursor is still on the clip
			// we just saved; flip its badge on so it reads as downloaded the moment
			// the user returns, without waiting for a reload. GlobalIndex, not
			// Index: while a filter is applied Index counts the visible rows, but
			// SetItem writes the unfiltered slice; Resync then re-runs the
			// filter's matches over the updated rows (see listfilter.Resync).
			if it, ok := m.vodList.SelectedItem().(vodItem); ok {
				it.downloaded = true
				listfilter.Resync(&m.vodList, m.vodList.SetItem(m.vodList.GlobalIndex(), it))
			}
		}
		return m, nil

	case resolveFailedMsg:
		// Same staleness rules as resolvedMsg: only fail the download this view
		// is showing, not one the user dismissed mid-resolve.
		if m.focus != focusDownload || m.dl.phase != phaseResolving ||
			msg.vod.VideoID != m.dl.vod.VideoID {
			return m, nil
		}
		m.dl.phase = phaseError
		m.dl.err = msg.err
		if errors.Is(msg.err, cosmo.ErrNoMembership) {
			m.dl.err = errors.New("membership required to download this replay")
		}
		return m, nil

	case errMsg:
		// Only view resolves and the group loads report errMsg, so this never
		// belongs to the download view (its errors arrive as resolveFailedMsg or
		// execDoneMsg); a stale load error keeps to the list status line.
		m.loading = false
		if errors.Is(msg.err, cosmo.ErrNoMembership) {
			m.status = "membership required to watch this replay"
			return m, nil
		}
		m.err = msg.err
		return m, nil

	case list.FilterMatchesMsg:
		return m, listfilter.Route(msg, &m.memberList, &m.vodList)

	case tea.PasteMsg:
		// Pasted text goes to whichever list is filtering, if any.
		return m, listfilter.Paste(msg, &m.memberList, &m.vodList)

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// The download view is modal: every other key is swallowed so the list can't
	// be driven (and a second download can't be started) underneath it.
	if m.focus == focusDownload {
		// A finished download is just a panel to leave, so the uniform back
		// motion dismisses it. While one is in flight, leaving would throw the
		// work away, so the motions are blocked and cancelling takes a key of its
		// own — 'h' must not silently bin a download the user is mid-way through.
		if m.dl.phase == phaseDone || m.dl.phase == phaseError {
			if keynav.Ascend(msg) {
				return m.dismissDownload()
			}
			return m, nil
		}
		if msg.String() == "c" {
			return m.dismissDownload()
		}
		return m, nil
	}

	// Any keypress dismisses a shown error (the key still performs its action);
	// errors are transient status-line notices, not modal state.
	m.err = nil

	// 'r' reloads regardless of focus (unless typing a filter). A refresh
	// refetches the whole group at once, so forget every member's cached cursor
	// for it and let each list start at the top.
	if msg.String() == "r" && !m.AcceptsText() {
		m.loading, m.err = true, nil
		m.resetCursors()
		return m, tea.Batch(m.load(), m.loadLive())
	}

	if m.focus == focusMembers {
		if m.memberList.FilterState() != list.Filtering {
			if keynav.Descend(msg) {
				m.syncCurrent() // normally a no-op: the pane already follows the cursor
				if _, ok := m.memberList.SelectedItem().(memberItem); ok {
					m.setFocus(focusVods)
				}
				return m, nil
			}
			if keynav.Ascend(msg) {
				return m, nil // leftmost pane: swallow, or the list would page on h
			}
		}
		var cmd tea.Cmd
		m.memberList, cmd = m.memberList.Update(msg)
		m.syncCurrent()
		return m, cmd
	}

	// focusVods
	if m.vodList.FilterState() != list.Filtering {
		if keynav.Ascend(msg) {
			m.setFocus(focusMembers)
			return m, nil
		}
		if keynav.Descend(msg) {
			// Swallowed: a replay has nothing to descend into yet (v plays it).
			// Reserved for a detail view; falling through would page the list.
			return m, nil
		}
		switch msg.String() {
		case "v":
			return m, m.viewSelected()
		case "D":
			return m.beginDownload()
		case "t":
			return m, m.openThumbnail()
		}
	}
	var cmd tea.Cmd
	m.vodList, cmd = m.vodList.Update(msg)
	return m, cmd
}

// openThumbnail opens the selected replay's thumbnail in the default image
// handler. Thumbnails come straight off the list response's public CDN URL, so
// this works even for membership-gated clips.
func (m Model) openThumbnail() tea.Cmd {
	it, ok := m.vodList.SelectedItem().(vodItem)
	if !ok || it.vod.ThumbnailURL == "" {
		return nil
	}
	url := it.vod.ThumbnailURL
	return func() tea.Msg {
		_ = external.OpenURL(url)
		return nil
	}
}

// viewSelected opens the selected replay in mpv (or the configured link
// handler). If the replay has already been downloaded to the output folder, its
// local file is opened directly, skipping the stream-URL resolve so playback
// starts immediately; otherwise it falls back to resolving the live stream URL.
func (m Model) viewSelected() tea.Cmd {
	it, ok := m.vodList.SelectedItem().(vodItem)
	if !ok {
		return nil
	}
	if path := m.vodPath(m.vodMember(it.vod), it.vod); fileExists(path) {
		return openVideo(path)
	}
	return m.resolveSelected(false)
}

// vodMember is the folder/name member a replay files under: always the
// broadcasting author, so a replay lands in the same folder no matter which
// list it was opened from.
func (m Model) vodMember(vod cosmo.Vod) string {
	return vod.Member
}

// vodPath is the on-disk destination a replay download is saved to. View and
// download derive it identically so a viewed replay can reuse a completed one.
func (m Model) vodPath(member string, vod cosmo.Vod) string {
	name := sanitize(fmt.Sprintf("%s_%s_%s", vod.Date, member, vod.Title)) + ".mp4"
	return filepath.Join(m.downloadDir, "cosmo-"+m.group, member, name)
}

// captionPath is where a replay's subtitles are saved: the video's own path
// with the language code and .vtt in place of .mp4, so players pick the track
// up off the matching stem. The language is whichever COSMO served (see
// cosmo.Caption), which keeps a later switch from overwriting what is here.
//
// Sidecar rather than muxed in: a sixth of the cues overlap the one before
// them, which no subtitle track an mp4 can carry is able to represent.
func captionPath(videoPath, lang string) string {
	return strings.TrimSuffix(videoPath, ".mp4") + "." + lang + ".vtt"
}

// subtitleOutcome saves a replay's subtitles beside its video and reports the
// result as the line the download view shows.
//
// It returns words rather than an error because it cannot be allowed to fail a
// download: the replay is what the user asked for, and it is already on disk by
// the time this runs. Every way of coming back empty is worth distinguishing,
// though - "COSMO made none for this live" and "it made some and you need a
// membership to read them" are different problems, and only the second is worth
// acting on.
func subtitleOutcome(ctx context.Context, client *cosmo.Client, clip cosmo.Clip, vod cosmo.Vod, videoPath string) string {
	if !clip.HasCaption {
		return "none for this replay"
	}
	caption, err := client.Caption(ctx, vod.VideoID)
	if err != nil {
		return "unavailable (" + textfmt.Line(err.Error()) + ")"
	}
	if !caption.Available() {
		if !clip.Connected {
			return "membership required"
		}
		return "none returned"
	}
	dest := captionPath(videoPath, caption.Lang)
	if fileExists(dest) {
		return caption.Lang + " (already saved)"
	}
	if err := client.Download(ctx, caption.URL, dest); err != nil {
		return "failed (" + textfmt.Line(err.Error()) + ")"
	}
	return caption.Lang
}

// openVideo hands a video target (a stream URL or a local file path) to the
// configured link handler, or to in-terminal mpv when none is set.
func openVideo(target string) tea.Cmd {
	if external.LinkHandlerSet() {
		// The handler launches its own (windowed) player, so fire it detached
		// instead of suspending the TUI for in-terminal mpv.
		return func() tea.Msg {
			_ = external.OpenURL(target)
			return nil
		}
	}
	return tea.ExecProcess(external.MPVCommand(target),
		func(err error) tea.Msg { return execDoneMsg{err: err} })
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (m Model) resolveSelected(download bool) tea.Cmd {
	it, ok := m.vodList.SelectedItem().(vodItem)
	if !ok {
		return nil
	}
	client := m.client
	vod := it.vod
	return func() tea.Msg {
		clip, err := client.Clip(context.Background(), vod.VideoID)
		if err != nil {
			if download {
				return resolveFailedMsg{vod: vod, err: err}
			}
			return errMsg{err}
		}
		return resolvedMsg{vod: vod, clip: clip, download: download}
	}
}

// beginDownload opens the modal download view for the selected replay and
// starts resolving its stream URL. Entering the view immediately (before the
// resolve returns) blocks further navigation, so the same replay can't be
// queued twice. It mutates m, so the caller must return what it hands back.
//
// The receiver is a pointer to mutate, but the model is returned by value: the
// shell stores whatever Update yields, and every other page hands back a Model,
// so returning the pointer here would leave the tab held as a *Model.
func (m *Model) beginDownload() (tea.Model, tea.Cmd) {
	it, ok := m.vodList.SelectedItem().(vodItem)
	if !ok {
		return *m, nil
	}
	m.dl = dlState{vod: it.vod, member: m.vodMember(it.vod), phase: phaseResolving}
	m.setFocus(focusDownload)
	cmd := m.resolveSelected(true)
	return *m, cmd
}

// startDownload kicks off the native, parallel HLS download for the replay the
// download view is showing, muxing the demuxed video+audio tracks into one mp4
// with ffmpeg. It runs in the background, feeding phase updates through
// m.dlProgress. It mutates m, so the caller must return the receiver.
func (m *Model) startDownload(r resolvedMsg) tea.Cmd {
	outPath := m.vodPath(m.dl.member, r.vod)
	dir := filepath.Dir(outPath)

	// Skip the whole download (and the ffmpeg requirement) if it's already
	// saved, matching the room/talk download flows. The subtitles are still
	// worth a look: this is the path a replay saved before they existed comes
	// back through, and the one that collects a second language after switching
	// it in the app, so pressing d again fetches whatever is missing.
	if fileExists(outPath) {
		// Not phaseDone yet: the view must not offer its exit while the
		// subtitle fetch is still running, or the user is invited to leave
		// during the one window where there is still something to wait for.
		m.dl.phase, m.dl.savedPath = phaseSubtitles, outPath
		ctx, cancel := context.WithCancel(context.Background())
		m.dl.cancel = cancel
		client, clip, vod := m.client, r.clip, r.vod
		return func() tea.Msg {
			return execDoneMsg{
				savedPath: outPath,
				subtitles: subtitleOutcome(ctx, client, clip, vod, outPath),
			}
		}
	}
	if !external.Available("ffmpeg") {
		m.dl.phase = phaseError
		m.dl.err = errors.New("ffmpeg required to download replays")
		return nil
	}
	_ = os.MkdirAll(dir, 0o755)

	ch := make(chan downloadProgressMsg, 1)
	ctx, cancel := context.WithCancel(context.Background())
	m.dlProgress = ch
	m.dl.phase = phaseDownloading
	m.dl.cancel = cancel
	url := r.clip.VideoURL
	client, clip, vod := m.client, r.clip, r.vod

	run := func() tea.Msg {
		defer close(ch)
		// Work in a temp dir on the destination's filesystem so the final
		// rename into place is atomic, echoing cosmo.Download's .part convention.
		tmp, err := os.MkdirTemp(dir, ".cosmo-dl-*")
		if err != nil {
			return execDoneMsg{err: err}
		}
		defer os.RemoveAll(tmp)

		video, audio, err := hls.Fetch(ctx, url, tmp,
			hls.Options{Concurrency: m.fragments, UserAgent: cosmo.UserAgent},
			func(done, total int) {
				// Coalesce: drop updates the UI hasn't drained yet rather than
				// stall the fetch workers.
				select {
				case ch <- downloadProgressMsg{done: done, total: total}:
				default:
				}
			})
		if err != nil {
			return execDoneMsg{err: err}
		}

		// Produce the finished file inside tmp (a .mp4 name so ffmpeg picks the
		// container from the extension), then rename it into place: the
		// destination only ever sees a complete file, and tmp shares its
		// filesystem so the rename is atomic.
		muxed := video // already a complete mp4 for an already-muxed stream
		if audio != "" {
			// Best-effort like the fragment updates: never block a possibly
			// reader-less channel (the UI may have been dismissed).
			select {
			case ch <- downloadProgressMsg{muxing: true}:
			default:
			}
			muxed = filepath.Join(tmp, "muxed.mp4")
			if err := external.FFmpegMuxCommand(video, audio, muxed).Run(); err != nil {
				return execDoneMsg{err: fmt.Errorf("ffmpeg mux: %w", err)}
			}
		}
		if err := os.Rename(muxed, outPath); err != nil {
			return execDoneMsg{err: err}
		}
		// Only now, with the video in place: a cancelled or failed download
		// should leave no lone .vtt behind. ctx still applies, so dismissing
		// mid-fetch drops the subtitles rather than the saved replay.
		select {
		case ch <- downloadProgressMsg{subtitles: true}:
		default:
		}
		return execDoneMsg{
			savedPath: outPath,
			subtitles: subtitleOutcome(ctx, client, clip, vod, outPath),
		}
	}
	return tea.Batch(run, waitProgress(ch))
}

// dismissDownload leaves the download view: it cancels an in-flight fetch, or
// simply returns to the list once the download has finished. It mutates m, so
// the caller must return what it hands back. See beginDownload for why the
// model comes back by value.
func (m *Model) dismissDownload() (tea.Model, tea.Cmd) {
	if m.dl.phase != phaseDone && m.dl.phase != phaseError && m.dl.cancel != nil {
		m.dl.cancel() // stop an in-flight fetch; run() cleans up its temp dir
	}
	m.dlProgress = nil
	m.dl = dlState{}
	m.setFocus(focusVods)
	return *m, nil
}

// waitProgress blocks for the next download update, returning nil once the
// channel closes (download finished). Re-issued after each update to keep the
// view live.
func waitProgress(ch chan downloadProgressMsg) tea.Cmd {
	return func() tea.Msg {
		if p, ok := <-ch; ok {
			return p
		}
		return nil
	}
}

// syncCurrent makes the replay pane follow the member cursor: if the selected
// member changed, stash the old member's replay cursor and re-filter. The
// group's replays are already client-side, so this is instant; while the group
// fetch is in flight the pane fills when it lands.
func (m *Model) syncCurrent() {
	it, ok := m.memberList.SelectedItem().(memberItem)
	if !ok || it.member.Name == m.current.Name {
		return
	}
	m.stashCursor() // remember where we left the current member
	m.current = it.member
	m.err = nil
	if m.all == nil {
		return
	}
	m.setVods(m.all[it.member.APIID])
}

// cursorKey identifies the selected member's replay list within the current group.
func (m Model) cursorKey() string { return m.group + "/" + m.current.Name }

// resetCursors forgets every remembered cursor in the current group, used when
// a refresh refetches the whole group's replays.
func (m *Model) resetCursors() {
	prefix := m.group + "/"
	for k := range m.cursor {
		if strings.HasPrefix(k, prefix) {
			delete(m.cursor, k)
		}
	}
}

// stashCursor records the current member's replay cursor so re-entering their
// list returns to the same spot. SetItems keeps the raw cursor across a swap, so
// without this a shorter list would inherit a stale position.
func (m *Model) stashCursor() {
	if m.cursor != nil {
		m.cursor[m.cursorKey()] = m.vodList.Index()
	}
}

// setVods fills the right pane with a member's replay list and restores that
// member's remembered cursor (0 on a first visit or after a manual refresh).
func (m *Model) setVods(vods []cosmo.Vod) {
	showMember := m.current.Name == allMember.Name
	items := make([]list.Item, len(vods))
	for i, v := range vods {
		downloaded := fileExists(m.vodPath(m.vodMember(v), v))
		items[i] = vodItem{vod: v, showMember: showMember, downloaded: downloaded}
	}
	listfilter.Resync(&m.vodList, m.vodList.SetItems(items))
	m.vodList.Title = "Replays - " + m.current.Name
	m.status = fmt.Sprintf("%d replay(s)", len(items))
	idx := m.cursor[m.cursorKey()]
	if idx < 0 || idx >= len(items) {
		idx = 0
	}
	m.vodList.Select(idx)
}

func (m Model) load() tea.Cmd {
	client, group := m.client, m.group
	return func() tea.Msg {
		vods, err := client.VodsByMember(context.Background(), group)
		if err != nil {
			return errMsg{err}
		}
		return vodsLoadedMsg{group: group, vods: vods}
	}
}

// loadLive fetches who's streaming now. A failure yields an empty set rather
// than an errMsg: the marker is decorative, and it must not blank the replay
// list or take over the status line that playback errors need.
func (m Model) loadLive() tea.Cmd {
	client, group := m.client, m.group
	return func() tea.Msg {
		ids, err := client.LiveMemberIDs(context.Background(), group)
		if err != nil {
			return liveSessionsLoadedMsg{group: group}
		}
		return liveSessionsLoadedMsg{group: group, ids: ids}
	}
}

// View satisfies tea.Model. The layout itself is render; keeping it a plain
// string keeps the composing shell and this package's tests off tea.View.
func (m Model) View() tea.View { return tea.NewView(m.render()) }

func (m Model) render() string {
	if m.focus == focusDownload {
		return m.downloadView()
	}

	left := style.PaneBorder(m.focus == focusMembers).Render(m.memberList.View())

	var status string
	switch {
	case m.err != nil:
		// Line-collapse defensively: the layout budgets exactly one row for the
		// status, and a multi-line error would push the whole frame off-screen.
		status = errStyle.Render("error: " + textfmt.Line(m.err.Error()))
	case m.loading:
		status = statusStyle.Render("loading replays…")
	case m.focus == focusMembers:
		status = statusStyle.Render("r: reload · /: filter")
	default:
		status = statusStyle.Render(m.status +
			" · v: view · D: download · t: thumb · r: reload · /: filter")
	}
	// Cap the status (the only free-length row) to the replay pane's width: a hint
	// or error longer than the pane would widen the joined block past the
	// terminal, wrapping the frame and desyncing the renderer (mirrors talk).
	status = lipgloss.NewStyle().MaxWidth(m.listWidth()).Render(status)

	right := lipgloss.JoinVertical(lipgloss.Left, m.vodList.View(), status)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

// listWidth is the replay pane's width: the page less the sidebar and its divider.
func (m Model) listWidth() int {
	if w := m.width - sidebarWidth - 1; w > 0 {
		return w
	}
	return 1
}

// downloadView renders the modal download panel: the replay's details, a
// progress bar tracking the current phase, and a footer whose hint flips
// between cancel (in flight) and back (finished).
func (m Model) downloadView() string {
	d := m.dl
	field := func(k, v string) string {
		return labelStyle.Render(fmt.Sprintf("%-8s", k)) + v
	}
	lines := []string{
		titleStyle.Render("Downloading replay"),
		"",
		field("member", d.member),
		field("date", d.vod.Date),
		field("title", textfmt.Line(textfmt.Normalize(d.vod.Title))),
		field("length", formatDuration(d.vod.Duration)),
		"",
	}

	switch d.phase {
	case phaseResolving:
		lines = append(lines, statusStyle.Render("resolving stream…"))
	case phaseDownloading:
		pct := 0.0
		if d.total > 0 {
			pct = float64(d.done) / float64(d.total)
		}
		lines = append(lines, m.prog.ViewAs(pct), "",
			statusStyle.Render(fmt.Sprintf("%d / %d fragments", d.done, d.total)))
	case phaseMuxing:
		lines = append(lines, m.prog.ViewAs(1), "", statusStyle.Render("muxing tracks…"))
	case phaseSubtitles:
		lines = append(lines, m.prog.ViewAs(1), "", statusStyle.Render("fetching subtitles…"))
	case phaseDone:
		lines = append(lines, m.prog.ViewAs(1), "", statusStyle.Render("saved: "+d.savedPath))
		if d.subtitles != "" {
			lines = append(lines, statusStyle.Render("subtitles: "+d.subtitles))
		}
	case phaseError:
		lines = append(lines, errStyle.Render("error: "+textfmt.Line(d.err.Error())))
	}

	// This modal always advertises its one exit, where the rest of the app leaves
	// the uniform motions unhinted (see internal/tui/keynav). Both phases have
	// earned it: a running download is left by a key used nowhere else, and a
	// settled one is left by a motion the view spent the whole download
	// swallowing — having just taught the reader that h does nothing here, it has
	// to say when that stops being true.
	if d.phase == phaseDone || d.phase == phaseError {
		lines = append(lines, "", labelStyle.Render("h: back"))
	} else {
		lines = append(lines, "", labelStyle.Render("c: cancel"))
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("13")).
		Padding(1, 3)
	if m.width > 2 {
		box = box.Width(m.width) // lipgloss counts the border in Width/Height
	}
	if m.height > 2 {
		box = box.Height(m.height)
	}
	return box.Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
}

// -- helpers ---------------------------------------------------------------- //

func formatDuration(seconds int) string {
	h := seconds / 3600
	mn := (seconds % 3600) / 60
	s := seconds % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, mn, s)
	}
	return fmt.Sprintf("%d:%02d", mn, s)
}

func sanitize(name string) string {
	return strings.Trim(unsafeChars.ReplaceAllString(name, "_"), ". ")
}
