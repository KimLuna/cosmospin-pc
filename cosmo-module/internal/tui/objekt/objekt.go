// Package objekt is the Objekt page: the user's owned objekt collection for the
// active group, laid out as a grid of per-copy cards beside a detail pane. Each
// card is one owned copy (every copy of a collection gets its own cell, so
// single-copy actions like sending are possible), tinted with the collection's
// accent color. A 2D cursor moves over the grid with h/j/k/l and the arrows, and
// the pane on the right redraws for whichever copy the cursor is on.
//
//	grid    : the card grid. /: filter, s: sort, p: pin (the selection, if any),
//	          r: reload, space: select, c: clear selection,
//	          t: send (the selection, if any)
//	detail  : the passive pane to its right, showing the highlighted copy. It
//	          takes no keys — there is nothing to descend into, so the whole page
//	          is one focusable pane
//	send    : modal over the page - pick a recipient, confirm the irreversible
//	          transfer, then watch the transfers run one by one.
//	          c drops the ones that have not started yet
//
// The detail pane is derived, not stored: it is rendered from the cursor at
// draw time, so nothing has to be kept in sync as the cursor, the filter or the
// loaded collections change. On a page too narrow to hold it beside a grid at
// least two cards wide it is dropped and the grid takes the whole width.
//
// Navigation follows the app-wide motions (see internal/tui/keynav); only the
// keys unique to this page are listed above. Because the grid is a 2D pane,
// h/l move the cursor horizontally rather than ascend/descend, and there is
// nowhere for enter to descend to — the detail is already on screen.
//
// Selection is separate from the cursor: any number of copies can be marked with
// space, and the marked set is what a bulk action reads. Both bulk actions
// consume it — p pins every marked copy's collection, and t sends every marked
// copy, falling back to the copy under the cursor when nothing is marked.
//
// Sending an objekt is an on-chain ERC-721 transfer signed by the user's
// embedded wallet (see internal/wallet); it is disabled when no signing key is
// provisioned. A send is always a queue of copies — one for a single send, many
// for a selection — worked through strictly one at a time, each transfer waiting
// for its receipt to record the move before the next is signed. A mined
// transaction is not proof on its own: Cosmo has reported token contracts that
// do not hold the copy, and transferFrom to an address with no matching code is
// a silent no-op that still mines successfully.
package objekt

import (
	"context"
	"fmt"
	"image/color"
	"math/big"
	"strconv"
	"strings"
	"time"

	"codeberg.org/djvu/cosmo-tui/internal/chain"
	"codeberg.org/djvu/cosmo-tui/internal/cosmo"
	"codeberg.org/djvu/cosmo-tui/internal/tui/keynav"
	"codeberg.org/djvu/cosmo-tui/internal/tui/style"
	"codeberg.org/djvu/cosmo-tui/internal/tui/textfmt"
	"codeberg.org/djvu/cosmo-tui/internal/tui/uimsg"
	"codeberg.org/djvu/cosmo-tui/internal/tui/usersearch"
	"codeberg.org/djvu/cosmo-tui/internal/wallet"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
	metaStyle   = lipgloss.NewStyle().Faint(true)
	labelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	warnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
)

// Card geometry. Cards are fixed width and height so the grid tiles cleanly and
// row windowing is a simple division; content is truncated to cardInnerW to
// keep each card exactly cardContentH lines (an over-long line would wrap and
// break the fixed height). Member names and collection codes are ASCII, so a
// rune-count truncation is safe here.
const (
	cardInnerW   = 13 // text width inside the border
	cardW        = cardInnerW + 2
	colGap       = 1
	cardContentH = 3
	cardH        = cardContentH + 2 // + top/bottom border
)

// Detail-pane geometry. The pane is a fixed width so the grid beside it keeps
// the same number of columns however the cursor moves; its rows are a label
// column and a value column, both truncated to fit.
const (
	detailW      = 30 // text width of the detail pane
	detailLabelW = 14 // label column inside it, wide enough for the longest label

	// minSplitWidth is the narrowest page that still splits: the pane, its
	// divider, and a grid at least two cards wide. Below it the pane is dropped
	// rather than squeezing the grid down to a single column — the grid is what
	// the page is for.
	minSplitWidth = detailW + 1 + 2*cardW + colGap
)

// sendPhase tracks the objekt-transfer flow, layered over the page: pick a
// recipient, confirm the irreversible transfers, watch them run, then read the
// results. sendOff means no transfer is in progress.
type sendPhase int

const (
	sendOff sendPhase = iota
	sendPicker
	sendConfirm
	sendSending
	sendDone
)

type (
	loadedMsg struct{ cols cosmo.ObjektCollections }
	errMsg    struct{ err error }

	// pinnedMsg is the result of a pin toggle. ci indexes the loaded
	// collections; pin is the state that was requested, so a failure can put
	// the collection back the way it was.
	pinnedMsg struct {
		ci  int
		pin bool
		err error
	}

	// sendResultMsg is the outcome of one queued transfer. idx is the position in
	// the send queue it answers, so a stale or duplicated result cannot advance
	// the queue twice. hash is set once the transfer is on the wire (even if the
	// receipt never arrived, hence confirmed).
	sendResultMsg struct {
		idx       int
		hash      string
		confirmed bool
		err       error
	}

	// sentReloadMsg carries a post-send collection reload. objektIDs are the
	// copies that were sent and attempt is which retry produced this (see
	// reloadAfterSend): the transfers index asynchronously, so a reload that
	// still shows one of them schedules another.
	sentReloadMsg struct {
		cols      cosmo.ObjektCollections
		objektIDs []int64
		attempt   int
		err       error
	}
)

// sendItem is one copy in a send queue: what to transfer, how to label it, and
// what became of it. The label is captured when the queue is built so the send
// screens never have to index back into the collections mid-flight.
type sendItem struct {
	objektID  int64
	label     string // "ChaeWon 101Z First #8001"
	hash      string // set once broadcast
	confirmed bool   // the chain showed the copy in the recipient's hands
	err       error
	done      bool
}

// cell is one owned copy in the grid: an index into the loaded collections plus
// which copy of that collection it is. Holding indices (rather than copies of
// the data) lets a collection-level pin toggle reach every sibling cell through
// the shared collection.
type cell struct {
	ci int // index into Model.collections
	oi int // index into that collection's Objekts
}

// Model is the objekt page.
type Model struct {
	client *cosmo.Client
	group  string
	wallet *wallet.Wallet // objekt-send signing key, or nil when unprovisioned

	sort string // cosmo.ObjektSortNewest | cosmo.ObjektSortOldest

	collections []cosmo.OwnedCollection // loaded source of truth (server order)
	meta        cosmo.ObjektCollections // counts (CollectionCount, FavoritedCount)

	cells  []cell // flattened per-copy view, after the filter
	cursor int    // index into cells
	topRow int    // first grid row rendered (vertical scroll)

	// selected is the marked set, keyed by objektId rather than by cell so a
	// filter, sort or reload — each of which rebuilds cells from scratch — does
	// not scatter it. It is the input a bulk action reads.
	selected map[int64]struct{}

	filterMode bool // the filter box is capturing keystrokes
	input      textinput.Model
	filter     string // applied filter text (kept while not typing)

	// objekt-send flow, layered over the whole page.
	send       sendPhase
	recipient  usersearch.Model // recipient nickname picker (send)
	sendTo     cosmo.UserSearchResult
	sendItems  []sendItem // the queue, captured when the flow starts
	sendIdx    int        // the transfer in flight; past the end once the queue is done
	sendTop    int        // first queue row rendered (send-list scroll)
	sendCancel bool       // stop after the transfer in flight, skipping the rest

	loaded bool
	err    error
	notice string // transient status-line message (pin limit, failed toggle)

	width, height int
}

// New builds the objekt page for a group and loads it via Init. w is the
// objekt-send signing wallet, or nil when none is provisioned (sending is then
// disabled).
func New(client *cosmo.Client, group string, w *wallet.Wallet) Model {
	in := textinput.New()
	in.Placeholder = "member / collection / class / season…"
	in.Prompt = "filter: "
	style.PlainInput(&in)
	style.FitInput(&in)
	recipient := usersearch.New(client, usersearch.Config{
		Title:       "Send objekt",
		Subtitle:    "Search a nickname to send to.",
		Prompt:      "recipient: ",
		Placeholder: "nickname…",
		CancelHint:  "esc: cancel send",
	})
	return Model{
		client:    client,
		group:     group,
		wallet:    w,
		sort:      cosmo.ObjektSortNewest,
		input:     in,
		recipient: recipient,
		selected:  map[int64]struct{}{},
	}
}

func (m Model) Title() string { return "Objekt" }

// AcceptsText reports whether the filter box or the send recipient picker is
// capturing keystrokes, so the shell yields plain-letter keys to it.
func (m Model) AcceptsText() bool {
	return m.filterMode || (m.send == sendPicker && m.recipient.AcceptsText())
}

func (m Model) Init() tea.Cmd { return m.load() }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.recipient.SetSize(m.width, m.bodyHeight())
		m.clampScroll()
		return m, nil

	case uimsg.GroupChanged:
		m.group = msg.Group
		m.send = sendOff
		m.loaded = false
		m.err = nil
		m.notice = ""
		m.resetFilter()
		m.clearSelection()
		m.collections = nil
		m.cells = nil
		m.cursor, m.topRow = 0, 0
		return m, m.load()

	case usersearch.SearchedMsg:
		// The async recipient-search result arrives via the shell broadcast;
		// hand it to the picker (which stale-guards by query).
		if m.send == sendPicker {
			var cmd tea.Cmd
			m.recipient, _, cmd = m.recipient.Update(msg)
			return m, cmd
		}
		return m, nil

	case tea.PasteMsg:
		// Pasted text goes to whichever box is capturing it: the recipient
		// picker while the send flow is on screen, or the filter box.
		if m.send == sendPicker {
			var cmd tea.Cmd
			m.recipient, _, cmd = m.recipient.Update(msg)
			return m, cmd
		}
		if m.filterMode {
			return m.filterPaste(msg)
		}
		return m, nil

	case sendResultMsg:
		return m.transferDone(msg)

	case loadedMsg:
		m.loaded = true
		m.err = nil
		m.meta = msg.cols
		m.collections = msg.cols.Collections
		m.send = sendOff
		m.cursor, m.topRow = 0, 0
		m.rebuildCells()
		m.pruneSelection()
		return m, nil

	case sentReloadMsg:
		if msg.err != nil {
			m.loaded = true
			m.err = msg.err
			return m, nil
		}
		// The transfers may not be indexed yet; if a sent copy is still present
		// and retries remain, wait and refetch rather than show the stale grid.
		if msg.attempt < postSendReloadTries && containsAnyObjekt(msg.cols, msg.objektIDs) {
			m.loaded = false
			return m, m.reloadAfterSend(msg.objektIDs, msg.attempt+1)
		}
		// Out of retries with a copy still listed: say so rather than leave the
		// user to notice it themselves. A send that reported success and left
		// the copy in place is exactly the failure the ownership check exists
		// to catch, so it must never pass silently here either.
		if n := stillPresent(msg.cols, msg.objektIDs); n > 0 {
			m.notice = fmt.Sprintf("%s still in your collection after sending · the transfer may not have gone through",
				countObjekts(n))
		}
		m.loaded = true
		m.err = nil
		m.meta = msg.cols
		m.collections = msg.cols.Collections
		m.cursor, m.topRow = 0, 0
		m.rebuildCells()
		m.pruneSelection()
		return m, nil

	case errMsg:
		m.loaded = true
		m.err = msg.err
		return m, nil

	case pinnedMsg:
		if msg.err != nil {
			m.notice = "pin failed: " + textfmt.Line(msg.err.Error())
			m.setPinned(msg.ci, !msg.pin) // put it back
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// The filter box owns every key while it is capturing text.
	if m.filterMode {
		return m.handleFilterKey(msg)
	}
	// The send flow, once started, is modal over the page.
	if m.send != sendOff {
		return m.handleSendKey(msg)
	}

	// Any keypress dismisses a shown notice (the key still performs its action).
	m.notice = ""

	return m.handleGridKey(msg)
}

// handleFilterKey drives the filter text box: enter applies and keeps the
// filter, esc clears it, anything else edits the query and re-filters live.
func (m Model) handleFilterKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.filterMode = false
		m.input.Blur()
		m.filter = strings.TrimSpace(m.input.Value())
		m.rebuildCells()
		return m, nil
	case "esc":
		m.filterMode = false
		m.input.Blur()
		m.resetFilter()
		m.rebuildCells()
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	style.FitInput(&m.input) // the box shares the status line with its hints
	m.filter = strings.TrimSpace(m.input.Value())
	m.rebuildCells()
	return m, cmd
}

// filterPaste drops pasted text into the filter box and re-filters the grid,
// the same work a typed character does — the filter is live, so the cells have
// to be rebuilt for the new query rather than at enter.
func (m Model) filterPaste(msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	style.FitInput(&m.input) // the box shares the status line with its hints
	m.filter = strings.TrimSpace(m.input.Value())
	m.rebuildCells()
	return m, cmd
}

// sendTargets returns the copies a send applies to, in grid order: the marked
// copies when anything is marked, else the one under the cursor. It is the send
// counterpart of pinTargets — but without the per-collection dedupe, since every
// copy is transferred in its own right.
func (m Model) sendTargets() []cell {
	if cells := m.selectedCells(); len(cells) > 0 {
		return cells
	}
	if c, ok := m.selectedCell(); ok {
		return []cell{c}
	}
	return nil
}

// startSendSelection queues every marked copy, or the cursor's when nothing is
// marked (see sendTargets).
func (m Model) startSendSelection() (tea.Model, tea.Cmd) {
	return m.beginSend(m.sendTargets())
}

// beginSend opens the recipient picker for a queue of copies, guarding against
// locked copies and a missing signing wallet. A locked copy anywhere in the
// queue refuses the whole thing rather than silently sending a subset: the user
// picked that copy, so dropping it quietly would be a surprise.
func (m Model) beginSend(cells []cell) (tea.Model, tea.Cmd) {
	if len(cells) == 0 {
		return m, nil
	}
	if locked := m.lockedCount(cells); locked > 0 {
		switch {
		case len(cells) == 1:
			m.notice = "this copy is locked (not transferable)"
		case locked == 1:
			m.notice = fmt.Sprintf("1 of the %d selected copies is locked · deselect it to send", len(cells))
		default:
			m.notice = fmt.Sprintf("%d of the %d selected copies are locked · deselect them to send",
				locked, len(cells))
		}
		return m, nil
	}
	if m.wallet == nil {
		m.notice = "sending unavailable: re-login with --no-auth to provision your wallet"
		return m, nil
	}
	m.sendItems = make([]sendItem, 0, len(cells))
	for _, c := range cells {
		col := m.collections[c.ci].Collection
		o := m.collections[c.ci].Objekts[c.oi]
		m.sendItems = append(m.sendItems, sendItem{
			objektID: o.ObjektID,
			label:    fmt.Sprintf("%s %s %s #%d", col.Member, col.CollectionNo, col.Class, o.ObjektNo),
		})
	}
	m.sendIdx, m.sendTop = 0, 0
	m.sendCancel = false
	m.send = sendPicker
	m.sendTo = cosmo.UserSearchResult{}
	cmd := m.recipient.Start()
	m.recipient.SetSize(m.width, m.bodyHeight())
	return m, cmd
}

// lockedCount is how many of the given copies cannot be transferred.
func (m Model) lockedCount(cells []cell) int {
	n := 0
	for _, c := range cells {
		if !m.collections[c.ci].Objekts[c.oi].Transferable {
			n++
		}
	}
	return n
}

// handleSendKey drives the modal send flow: pick a recipient, confirm the
// irreversible transfer, then dismiss the result.
func (m Model) handleSendKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.send {
	case sendPicker:
		var (
			act usersearch.Action
			cmd tea.Cmd
		)
		m.recipient, act, cmd = m.recipient.Update(msg)
		switch act {
		case usersearch.ActionCancel:
			m.send = sendOff
			return m, nil
		case usersearch.ActionSelect:
			if sel, ok := m.recipient.Selected(); ok {
				m.sendTo = sel
				m.send = sendConfirm
			}
			return m, nil
		}
		return m, cmd

	case sendConfirm:
		switch msg.String() {
		case "y", "enter":
			m.send = sendSending
			m.sendIdx, m.sendTop = 0, 0
			return m, m.doSend()
		case "n", "esc":
			// Backing out leaves the marked set alone, so the same selection can
			// be sent again to someone else.
			m.send = sendOff
			return m, nil
		}
		m.scrollSend(msg)
		return m, nil

	case sendSending:
		// The queue is running, so nothing the page owns leaves the flow before
		// every transfer has been processed. c is not an exception: it drops the
		// transfers that have not started, and the one in flight still runs to
		// its result. The shell's own globals (tab, q) still reach the shell —
		// switching away is harmless, since the queue keeps running and its
		// results land on return.
		if msg.String() == "c" && m.cancellable() {
			m.sendCancel = true // one way: there is no un-cancelling a queue
			return m, nil
		}
		m.scrollSend(msg)
		return m, nil

	case sendDone:
		// The list can outrun the screen, so the scroll keys keep working here.
		if m.scrollSend(msg) {
			return m, nil
		}
		// Leaving takes the uniform back motion, as the live page's finished
		// download panel does. Dismissing on any key was never true — the shell
		// takes q/H/L/A/? first, so "any key" quietly meant "any key except the
		// ones that quit or switch tab" — and it let a stray keystroke drop a
		// receipt list carrying transaction hashes before it had been read.
		if !keynav.Ascend(msg) {
			return m, nil
		}
		m.send = sendOff
		if ids := m.broadcastIDs(); len(ids) > 0 {
			// Those copies have left the collection: reload so they disappear
			// from the grid (and drop out of the marked set through
			// pruneSelection). Cosmo indexes the transfers asynchronously, so
			// the reload retries until they are actually gone.
			m.loaded = false
			m.err = nil
			return m, m.reloadAfterSend(ids, 1)
		}
		return m, nil // nothing left the wallet: stay put so the user can retry
	}
	return m, nil
}

// transferDone records one queued transfer's outcome and starts the next. The
// queue only ever advances on the result of the transfer in flight, which is
// what keeps the sends strictly serial: there is never a second one on the wire.
func (m Model) transferDone(msg sendResultMsg) (tea.Model, tea.Cmd) {
	if m.send != sendSending || msg.idx != m.sendIdx || msg.idx >= len(m.sendItems) {
		return m, nil // stale or duplicated; the queue has moved on
	}
	// sendItems is copied by header only, so this writes through to the model
	// being returned.
	m.sendItems[msg.idx].done = true
	m.sendItems[msg.idx].hash = msg.hash
	m.sendItems[msg.idx].confirmed = msg.confirmed
	m.sendItems[msg.idx].err = msg.err
	m.sendIdx++
	// A cancel takes effect here rather than at the keypress: the transfer that
	// was already on the wire cannot be recalled, so it is seen through to its
	// result and only the untouched rest are dropped.
	if m.sendIdx >= len(m.sendItems) || m.sendCancel {
		m.send = sendDone
	}
	m.followSend()
	if m.send == sendDone {
		return m, nil
	}
	return m, m.doSend()
}

// Receipt polling: a transfer is not finished when eth_sendRawTransaction
// answers. The nonce comes from the latest block (see cosmo.AbstractNonce), so
// signing the next transfer before this one is mined would reuse its nonce and
// fail. Waiting for the receipt also keeps the sends paced like a person rather
// than a burst. The cadence lives in internal/chain, alongside the loop.

// ownerReader is the chain read resolveToken probes candidate contracts with.
// Narrowing it here keeps the check testable without an HTTP client.
type ownerReader interface {
	AbstractOwnerOf(ctx context.Context, tokenAddress string, tokenID int64) (string, error)
}

// doSend broadcasts the transfer at the head of the queue and waits for the copy
// to actually change hands before reporting back.
func (m Model) doSend() tea.Cmd {
	idx := m.sendIdx
	objektID := m.sendItems[idx].objektID
	recipient := m.sendTo.Address
	candidates := m.tokenContracts()
	client, w := m.client, m.wallet
	return func() tea.Msg {
		ctx := context.Background()
		t, err := sendObjekt(ctx, client, w, objektID, recipient, candidates)
		if err != nil {
			return sendResultMsg{idx: idx, err: err}
		}
		confirmed, err := awaitTransfer(ctx, client, t, objektID, recipient)
		return sendResultMsg{idx: idx, hash: t.hash, confirmed: confirmed, err: err}
	}
}

// tokenContracts are the distinct token contracts the loaded collection reports,
// in the order they appear. They are the fallbacks for a copy whose own reported
// contract turns out not to hold it (see resolveToken).
//
// The main objekt contract is always among them, so no address has to be
// hardcoded here: every account is gifted a digital welcome objekt on signup
// which is not transferable, so it never leaves the collection — and digital
// copies report their contract correctly.
func (m Model) tokenContracts() []string {
	var out []string
	seen := map[string]bool{}
	for ci := range m.collections {
		for oi := range m.collections[ci].Objekts {
			addr := m.collections[ci].Objekts[oi].TokenAddress
			if addr == "" || seen[strings.ToLower(addr)] {
				continue
			}
			seen[strings.ToLower(addr)] = true
			out = append(out, addr)
		}
	}
	return out
}

// broadcastIDs are the copies that made it onto the wire, confirmed or not —
// the ones a post-send reload should wait out of the collection.
func (m Model) broadcastIDs() []int64 {
	var ids []int64
	for _, it := range m.sendItems {
		if it.hash != "" {
			ids = append(ids, it.objektID)
		}
	}
	return ids
}

// handleGridKey moves the 2D cursor and runs the grid-level actions. h/l move
// the cursor horizontally: the grid is a single 2D pane, with nothing to its
// left to back out to and nothing to descend into on its right — the detail
// pane is passive, and already shows the highlighted copy.
func (m Model) handleGridKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	cols := m.columns()
	switch msg.String() {
	case "left", "h":
		m.moveCursor(-1)
	case "right", "l":
		m.moveCursor(1)
	case "up", "k":
		m.moveCursor(-cols)
	case "down", "j":
		m.moveCursor(cols)
	case "pgup":
		m.moveCursor(-cols * m.visibleRows())
	case "pgdown":
		m.moveCursor(cols * m.visibleRows())
	case "g":
		m.cursor, m.topRow = 0, 0
	case "G":
		if len(m.cells) > 0 {
			m.cursor = len(m.cells) - 1
			m.clampScroll()
		}
	case "/":
		m.filterMode = true
		m.input.SetValue(m.filter)
		m.input.CursorEnd()
		m.input.Focus()
		return m, textinput.Blink
	case "esc":
		// The bubbles list clears an applied filter on esc without reopening
		// the filter box first; the grid matches that.
		if m.filter != "" {
			m.resetFilter()
			m.rebuildCells()
		}
	case "space":
		m.toggleSelected()
	case "c":
		m.clearSelection()
	case "p":
		return m.togglePin()
	case "t":
		return m.startSendSelection()
	case "s":
		if m.sort == cosmo.ObjektSortNewest {
			m.sort = cosmo.ObjektSortOldest
		} else {
			m.sort = cosmo.ObjektSortNewest
		}
		m.loaded = false
		m.err = nil
		return m, m.load()
	case "r":
		m.loaded = false
		m.err = nil
		return m, m.load()
	}
	return m, nil
}

// moveCursor shifts the cursor by delta cells, clamped to the grid, then
// scrolls to keep it visible. The detail pane follows on its own, being drawn
// from the cursor. A move that would leave the grid is swallowed (the edge is a
// wall, per keynav).
func (m *Model) moveCursor(delta int) {
	if len(m.cells) == 0 {
		return
	}
	next := m.cursor + delta
	if next < 0 || next >= len(m.cells) {
		return
	}
	m.cursor = next
	m.clampScroll()
}

// rebuildCells flattens the loaded collections into per-copy cells in server
// order, keeping only those matching the applied filter, and clamps the cursor
// into the new slice.
func (m *Model) rebuildCells() {
	m.cells = m.cells[:0]
	needle := strings.ToLower(m.filter)
	for ci := range m.collections {
		if needle != "" && !strings.Contains(collectionHaystack(m.collections[ci].Collection), needle) {
			continue
		}
		for oi := range m.collections[ci].Objekts {
			m.cells = append(m.cells, cell{ci: ci, oi: oi})
		}
	}
	if m.cursor >= len(m.cells) {
		m.cursor = len(m.cells) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	m.clampScroll()
}

// collectionHaystack is the lower-cased text the filter matches against.
func collectionHaystack(c cosmo.ObjektCollection) string {
	return strings.ToLower(c.Member + " " + c.CollectionNo + " " + c.Class + " " + c.Season)
}

func (m *Model) resetFilter() {
	m.filter = ""
	m.input.SetValue("")
	style.FitInput(&m.input) // back to placeholder width
}

// pinTargets returns the distinct collections a pin toggle applies to, in grid
// order: the marked copies' collections when anything is marked, else the one
// collection under the cursor. Marked copies are deduplicated by collection —
// pinning is a collection-level action, so two copies of the same collection are
// one target, not two requests.
func (m Model) pinTargets() []int {
	cells := m.selectedCells()
	if len(cells) == 0 {
		c, ok := m.selectedCell()
		if !ok {
			return nil
		}
		cells = []cell{c}
	}
	seen := make(map[int]bool, len(cells))
	out := make([]int, 0, len(cells))
	for _, c := range cells {
		if seen[c.ci] {
			continue
		}
		seen[c.ci] = true
		out = append(out, c.ci)
	}
	return out
}

// togglePin pins or unpins every target collection (see pinTargets). Pinning is
// a collection-level action, so it flips every sibling copy at once; the change
// is optimistic and reverted if the API rejects it. The list is deliberately
// not reloaded (a reload would reorder under the cursor).
//
// A selection toggles in one direction: it unpins only when every target is
// already pinned, and otherwise pins the unpinned targets and leaves the rest
// alone, so a mixed selection converges on pinned instead of half flipping each
// way. The whole batch is refused when it would pass the cap — the API would
// answer 201 and silently evict the oldest pins. The marks survive either way,
// so the same set can be toggled straight back off.
func (m Model) togglePin() (tea.Model, tea.Cmd) {
	targets := m.pinTargets()
	if len(targets) == 0 {
		return m, nil
	}
	pin := false
	for _, ci := range targets {
		if !m.collections[ci].Collection.Favorited() {
			pin = true
			break
		}
	}
	todo := make([]int, 0, len(targets))
	for _, ci := range targets {
		if m.collections[ci].Collection.Favorited() != pin {
			todo = append(todo, ci)
		}
	}
	if pin && m.pinnedCount()+len(todo) > cosmo.MaxFavoritedObjekts {
		m.notice = fmt.Sprintf("max pin limit reached (%d) · unpin one first", cosmo.MaxFavoritedObjekts)
		if len(todo) > 1 {
			m.notice = fmt.Sprintf("pinning %d would pass the max of %d (%d pinned) · unpin some first",
				len(todo), cosmo.MaxFavoritedObjekts, m.pinnedCount())
		}
		return m, nil
	}

	client := m.client
	cmds := make([]tea.Cmd, 0, len(todo))
	for _, ci := range todo {
		col := m.collections[ci].Collection
		cmds = append(cmds, func() tea.Msg {
			err := client.SetObjektFavorite(context.Background(), col, pin)
			return pinnedMsg{ci: ci, pin: pin, err: err}
		})
		m.setPinned(ci, pin)
	}
	// Sequence, not Batch: the user's favorite list takes one write at a time,
	// and Cosmo answers 409 to a second that overlaps it, so the requests go out
	// in order. Either way a single target collapses to the command itself,
	// which is the no-selection case.
	return m, tea.Sequence(cmds...)
}

// setPinned rewrites the pin state of the collection at ci (a stand-in
// favoritedAt until the next load).
func (m *Model) setPinned(ci int, pinned bool) {
	if ci < 0 || ci >= len(m.collections) {
		return
	}
	m.collections[ci].Collection.FavoritedAt = ""
	if pinned {
		m.collections[ci].Collection.FavoritedAt = time.Now().UTC().Format(time.RFC3339)
	}
}

// pinnedCount is how many distinct collections are pinned, counted from the
// loaded data so an optimistic toggle shows before the next load.
func (m Model) pinnedCount() int {
	n := 0
	for i := range m.collections {
		if m.collections[i].Collection.Favorited() {
			n++
		}
	}
	return n
}

// selectedCell returns the cell under the cursor.
func (m Model) selectedCell() (cell, bool) {
	if m.cursor < 0 || m.cursor >= len(m.cells) {
		return cell{}, false
	}
	return m.cells[m.cursor], true
}

// toggleSelected marks or unmarks the copy under the cursor.
func (m *Model) toggleSelected() {
	c, ok := m.selectedCell()
	if !ok {
		return
	}
	id := m.collections[c.ci].Objekts[c.oi].ObjektID
	if _, on := m.selected[id]; on {
		delete(m.selected, id)
		return
	}
	if m.selected == nil {
		m.selected = map[int64]struct{}{}
	}
	m.selected[id] = struct{}{}
}

// clearSelection unmarks everything, including copies the filter is hiding.
func (m *Model) clearSelection() { m.selected = map[int64]struct{}{} }

// isSelected reports whether a cell's copy is marked.
func (m Model) isSelected(c cell) bool {
	_, ok := m.selected[m.collections[c.ci].Objekts[c.oi].ObjektID]
	return ok
}

// selectedCells returns the marked copies in grid order — the order the user
// sees, and the order a bulk action should work through. It walks the
// collections rather than cells so copies the filter is hiding are included.
func (m Model) selectedCells() []cell {
	if len(m.selected) == 0 {
		return nil
	}
	out := make([]cell, 0, len(m.selected))
	for ci := range m.collections {
		for oi := range m.collections[ci].Objekts {
			if _, ok := m.selected[m.collections[ci].Objekts[oi].ObjektID]; ok {
				out = append(out, cell{ci: ci, oi: oi})
			}
		}
	}
	return out
}

// pruneSelection drops marks whose copy is no longer in the loaded collection,
// so a copy sent or traded away between reloads stops being counted.
func (m *Model) pruneSelection() {
	if len(m.selected) == 0 {
		return
	}
	kept := make(map[int64]struct{}, len(m.selected))
	for ci := range m.collections {
		for oi := range m.collections[ci].Objekts {
			id := m.collections[ci].Objekts[oi].ObjektID
			if _, ok := m.selected[id]; ok {
				kept[id] = struct{}{}
			}
		}
	}
	m.selected = kept
}

// columns is how many cards fit across the grid pane (at least one).
func (m Model) columns() int {
	if n := (m.gridWidth() + colGap) / (cardW + colGap); n > 0 {
		return n
	}
	return 1
}

// detailShown reports whether the page is wide enough to carry the detail pane
// beside the grid (see minSplitWidth).
func (m Model) detailShown() bool { return m.width >= minSplitWidth }

// gridWidth is the card grid's width: the page less the detail pane and its
// divider, or the whole page when the pane is dropped.
func (m Model) gridWidth() int {
	if !m.detailShown() {
		return m.width
	}
	return m.width - detailW - 1
}

// visibleRows is how many card rows fit in the grid body (at least one).
func (m Model) visibleRows() int {
	if n := m.bodyHeight() / cardH; n > 0 {
		return n
	}
	return 1
}

// bodyHeight is the grid/detail area, reserving one line for the status line.
func (m Model) bodyHeight() int {
	if h := m.height - 1; h > 0 {
		return h
	}
	return 1
}

// clampScroll scrolls the grid so the cursor's row stays within the visible
// window.
func (m *Model) clampScroll() {
	cols := m.columns()
	row := m.cursor / cols
	vis := m.visibleRows()
	if row < m.topRow {
		m.topRow = row
	} else if row >= m.topRow+vis {
		m.topRow = row - vis + 1
	}
	if m.topRow < 0 {
		m.topRow = 0
	}
}

func (m Model) load() tea.Cmd {
	client, group, order := m.client, m.group, m.sort
	return func() tea.Msg {
		cols, err := client.AllObjektCollections(context.Background(), group, order)
		if err != nil {
			return errMsg{err}
		}
		return loadedMsg{cols: cols}
	}
}

// Post-send reload pacing: Cosmo indexes the on-chain transfer a moment after
// the broadcast, so an immediate reload can still list the sent copy. Wait
// between reloads and retry a few times, stopping as soon as the copy is gone.
const (
	postSendReloadDelay = 1500 * time.Millisecond
	postSendReloadTries = 4
)

// reloadAfterSend waits, then refetches the collection, tagging the result so
// the handler can retry while a sent copy still lingers.
func (m Model) reloadAfterSend(objektIDs []int64, attempt int) tea.Cmd {
	client, group, order := m.client, m.group, m.sort
	return func() tea.Msg {
		time.Sleep(postSendReloadDelay)
		cols, err := client.AllObjektCollections(context.Background(), group, order)
		return sentReloadMsg{cols: cols, objektIDs: objektIDs, attempt: attempt, err: err}
	}
}

// containsAnyObjekt reports whether any of the given copies is still listed.
func containsAnyObjekt(cols cosmo.ObjektCollections, objektIDs []int64) bool {
	return stillPresent(cols, objektIDs) > 0
}

// stillPresent counts how many of the given copies the collection still lists.
func stillPresent(cols cosmo.ObjektCollections, objektIDs []int64) int {
	n := 0
	for _, id := range objektIDs {
		if containsObjekt(cols, id) {
			n++
		}
	}
	return n
}

// containsObjekt reports whether any copy in cols has the given objektId.
func containsObjekt(cols cosmo.ObjektCollections, objektID int64) bool {
	for i := range cols.Collections {
		for j := range cols.Collections[i].Objekts {
			if cols.Collections[i].Objekts[j].ObjektID == objektID {
				return true
			}
		}
	}
	return false
}

// transfer is one broadcast objekt transfer: the transaction hash and the
// contract it went to.
type transfer struct {
	hash  string
	token string
}

// sendObjekt runs the whole on-chain transfer: it re-checks live ownership and
// transferability, resolves the contract that actually holds the copy, builds the
// transferFrom calldata, and hands it to chain.Send to be priced, signed and
// broadcast.
//
// candidates are other token contracts worth trying if the one Cosmo reports
// turns out not to hold the copy (see resolveToken).
func sendObjekt(ctx context.Context, client *cosmo.Client, w *wallet.Wallet, objektID int64, recipient string, candidates []string) (transfer, error) {
	own, err := client.OwnedObjektDetail(ctx, objektID)
	if err != nil {
		return transfer{}, fmt.Errorf("re-check ownership: %w", err)
	}
	if !own.Transferable {
		return transfer{}, fmt.Errorf("objekt is no longer transferable")
	}
	token, err := resolveToken(ctx, client, own, objektID, candidates)
	if err != nil {
		return transfer{}, err
	}
	from, err := wallet.ParseAddress(own.Owner)
	if err != nil {
		return transfer{}, fmt.Errorf("owner address: %w", err)
	}
	to, err := wallet.ParseAddress(token)
	if err != nil {
		return transfer{}, fmt.Errorf("token address: %w", err)
	}
	rcpt, err := wallet.ParseAddress(recipient)
	if err != nil {
		return transfer{}, fmt.Errorf("recipient address: %w", err)
	}

	data := wallet.TransferCalldata(from, rcpt, big.NewInt(objektID))
	hash, err := chain.Send(ctx, client, w, from, to, data)
	if err != nil {
		return transfer{}, err
	}
	return transfer{hash: hash, token: token}, nil
}

// resolveToken picks the contract to send the copy through: the one Cosmo
// reports, if the chain agrees it holds the copy, else the first candidate that
// does.
//
// Cosmo's tokenAddress cannot be taken on trust: physical objekts (the ones
// whose collectionNo ends in A, carrying a QR that mints the digital copy) all
// report one contract that answers no ERC-721 call at all, while their token
// sits on the main objekt contract with every digital copy. Observed across two
// seasons, two classes and a year of mint dates. transferFrom to an address
// with no matching code is a silent no-op that still mines successfully, so an
// unchecked send reports success having moved nothing. Nothing here is guessed
// — a contract is only used once ownerOf names the sender as the owner.
func resolveToken(ctx context.Context, client ownerReader, own cosmo.ObjektOwnership, objektID int64, candidates []string) (string, error) {
	tried := make(map[string]bool)
	for _, addr := range append([]string{own.TokenAddress}, candidates...) {
		if addr == "" || tried[strings.ToLower(addr)] {
			continue
		}
		tried[strings.ToLower(addr)] = true
		holder, err := client.AbstractOwnerOf(ctx, addr, objektID)
		if err != nil {
			continue // reverted (no such token here), or a transient RPC error
		}
		if strings.EqualFold(holder, own.Owner) {
			return addr, nil
		}
	}
	return "", fmt.Errorf("no contract holds this copy: %s reports %s as owner of #%d, and the chain disagrees",
		textfmt.ShortAddr(own.TokenAddress), textfmt.ShortAddr(own.Owner), objektID)
}

// awaitTransfer waits for a broadcast transfer to be mined and then reads the
// receipt's logs to see whether the copy actually changed hands. It reports
// whether the move was confirmed; a transaction that lands without moving
// anything is an error, not a success.
//
// The proof is the receipt's Transfer event rather than a follow-up ownerOf
// call. Both failure modes this has to separate were seen for real on the same
// copy: a transaction that succeeded while moving nothing (a contract address
// that has no transferFrom), and a completed transfer whose ownerOf still named
// the sender seconds after the receipt, because reads through Cosmo's RPC proxy
// lag it. The receipt carries the truth at the moment it lands.
func awaitTransfer(ctx context.Context, client chain.ReceiptReader, t transfer, objektID int64, recipient string) (bool, error) {
	return chain.AwaitReceipt(ctx, client, t.hash, chain.Proof{
		Moved: func(rec cosmo.AbstractReceipt) bool {
			return rec.MovedToken(t.token, objektID, recipient)
		},
		Reverted: fmt.Errorf("transfer reverted on-chain"),
		Unproven: fmt.Errorf("the transaction was mined but recorded no transfer of this copy (%s)",
			textfmt.ShortAddr(t.token)),
	})
}

// View satisfies tea.Model. The layout itself is render; keeping it a plain
// string keeps the composing shell and this package's tests off tea.View.
func (m Model) View() tea.View { return tea.NewView(m.render()) }

func (m Model) render() string {
	if m.send != sendOff {
		body := lipgloss.Place(m.width, m.bodyHeight(), lipgloss.Left, lipgloss.Top, m.sendBody())
		return body + "\n" + statusStyle.Render(m.sendStatus())
	}

	var body string
	switch {
	case m.err != nil:
		body = errStyle.Render("error: "+textfmt.Line(m.err.Error())) + "\n\n" + statusStyle.Render("r: retry")
	case !m.loaded:
		body = statusStyle.Render("loading…")
	case len(m.collections) == 0:
		body = statusStyle.Render("no objekts in this collection")
	case len(m.cells) == 0:
		body = statusStyle.Render(fmt.Sprintf("no copies match %q", m.filter))
	default:
		// Only a populated grid has a copy to detail; the states above are a
		// message on an otherwise empty page, so they take the full width.
		return m.splitBody() + "\n" + m.status()
	}

	body = lipgloss.Place(m.width, m.bodyHeight(), lipgloss.Left, lipgloss.Top, body)
	return body + "\n" + m.status()
}

// status is the status line, capped to the page. It is the only free-length row
// here, and a hint list longer than the terminal is wide would wrap and push the
// frame off-screen (mirrors news/live).
func (m Model) status() string {
	return lipgloss.NewStyle().MaxWidth(m.width).Render(m.statusLine())
}

// splitBody lays the card grid beside the detail pane for the highlighted copy,
// with a divider between them. On a narrow page the pane is dropped and the grid
// takes the whole width (see detailShown).
func (m Model) splitBody() string {
	grid := lipgloss.Place(m.gridWidth(), m.bodyHeight(), lipgloss.Left, lipgloss.Top, m.renderGrid())
	if !m.detailShown() {
		return grid
	}
	// The grid pane draws the divider, so its color tracks the pane that holds
	// focus — which here is always the grid, the page's only focusable pane.
	left := style.PaneBorder(true).Render(grid)
	right := lipgloss.Place(detailW, m.bodyHeight(), lipgloss.Left, lipgloss.Top, m.detailPane())
	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

// detailPane renders the highlighted copy's detail, clipped to the body height
// so a short terminal cannot push the frame off-screen. It is derived from the
// cursor at draw time rather than cached, so it cannot fall out of step with the
// grid; rendering one copy is cheap enough for that to be free.
func (m Model) detailPane() string {
	c, ok := m.selectedCell()
	if !ok {
		return ""
	}
	lines := strings.Split(m.renderDetail(m.collections[c.ci], c.oi), "\n")
	if h := m.bodyHeight(); len(lines) > h {
		lines = lines[:h]
	}
	return strings.Join(lines, "\n")
}

// sendBody renders the current send-flow screen.
func (m Model) sendBody() string {
	if m.send == sendPicker {
		return m.recipient.View()
	}
	head, rows := m.sendScreen()
	lines := append(append([]string{}, head...), sendWindow(rows, m.sendTop, m.sendCapacity(len(head)))...)
	return textfmt.Normalize(strings.Join(lines, "\n"))
}

// sendStatus is the one-line status hint for the active send phase.
func (m Model) sendStatus() string {
	if m.send == sendPicker {
		return m.recipient.StatusHint()
	}
	// Scrolling is only worth mentioning once the queue outruns the screen.
	head, rows := m.sendScreen()
	scrolls := len(rows) > m.sendCapacity(len(head))
	switch m.send {
	case sendConfirm:
		hint := "y/enter: send · n/esc: cancel"
		if scrolls {
			hint += " · j/k: scroll"
		}
		return hint
	case sendSending:
		hint := "please wait…"
		switch {
		case m.sendCancel:
			hint = "cancelling…"
		case m.cancellable():
			hint = "please wait… · c: cancel remaining"
		}
		if scrolls {
			hint += " · j/k: scroll"
		}
		return hint
	case sendDone:
		if scrolls {
			return "j/k: scroll · h: back"
		}
		return "h: back"
	}
	return ""
}

// sendScreen splits a send screen into the header pinned to the top and the
// per-copy rows that scroll beneath it. The queue can be long, so the warning
// and the recipient live in the header where they stay readable however far the
// list is scrolled.
func (m Model) sendScreen() (head, rows []string) {
	to := m.sendTo.Nickname
	if m.sendTo.Address != "" {
		to += " " + metaStyle.Render(textfmt.ShortAddr(m.sendTo.Address))
	}
	recipient := labelStyle.Render("Recipient: ") + textfmt.Line(to)

	if m.send == sendConfirm {
		what := "This transfer is irreversible. The objekt leaves your wallet"
		if len(m.sendItems) > 1 {
			what = "These transfers are irreversible. The objekts leave your wallet"
		}
		head = []string{
			titleStyle.Render("Confirm send"),
			"",
			warnStyle.Render(what),
			warnStyle.Render("immediately and cannot be recalled."),
			"",
			recipient,
			"",
			labelStyle.Render(fmt.Sprintf("Sending %s:", countObjekts(len(m.sendItems)))),
		}
		for _, it := range m.sendItems {
			rows = append(rows, "  "+m.fit(it.label))
		}
		return head, rows
	}

	title, progress := titleStyle.Render("Sending objekts"), ""
	switch {
	case m.send == sendDone:
		title, progress = titleStyle.Render("Send complete"), m.sendSummary()
		if m.sendCancel {
			title = titleStyle.Render("Send cancelled")
		}
	case len(m.sendItems) == 0:
	case m.sendCancel:
		progress = statusStyle.Render(fmt.Sprintf("%d/%d", m.sendIdx+1, len(m.sendItems))) +
			statusStyle.Render(" · ") + warnStyle.Render("cancelling after this transfer")
	default:
		progress = statusStyle.Render(fmt.Sprintf("%d/%d", m.sendIdx+1, len(m.sendItems)))
	}
	head = []string{title, "", recipient, "", progress, ""}
	for i, it := range m.sendItems {
		rows = append(rows, m.sendRow(i, it))
	}
	return head, rows
}

// sendRow renders one queue entry: what happened to it, or that it is next up.
func (m Model) sendRow(i int, it sendItem) string {
	switch {
	case it.err != nil:
		// The hash stays on a failed row when there is one: a transfer that was
		// broadcast and then went wrong is exactly the case worth looking up.
		row := errStyle.Render("✗ " + m.fit(it.label))
		if it.hash != "" {
			row += "  " + metaStyle.Render(shortHash(it.hash))
		}
		return row + "  " + errStyle.Render(m.fit(textfmt.Line(it.err.Error())))
	case it.done && it.confirmed:
		return okStyle.Render("✓ "+m.fit(it.label)) + "  " + metaStyle.Render(shortHash(it.hash))
	case it.done:
		return warnStyle.Render("· "+m.fit(it.label)) + "  " + warnStyle.Render("sent, unconfirmed "+shortHash(it.hash))
	case m.send == sendSending && i == m.sendIdx:
		return statusStyle.Render("» "+m.fit(it.label)) + "  " + statusStyle.Render("sending…")
	case m.send == sendDone:
		// The queue stopped before reaching this one: it was never broadcast.
		return "  " + m.fit(it.label) + "  " + metaStyle.Render("not sent")
	default:
		return "  " + m.fit(it.label) + "  " + metaStyle.Render("waiting")
	}
}

// sendSummary counts the finished queue for the done screen. A copy that was
// broadcast but never seen to move is counted apart from the confirmed ones —
// calling it sent is exactly the claim that turned out to be wrong before the
// ownership check existed.
func (m Model) sendSummary() string {
	sent, unconfirmed, failed, skipped := 0, 0, 0, 0
	for _, it := range m.sendItems {
		switch {
		case !it.done:
			skipped++ // the queue stopped before this one (see cancellable)
		case it.err != nil:
			failed++
		case it.confirmed:
			sent++
		default:
			unconfirmed++
		}
	}
	out := okStyle.Render(fmt.Sprintf("%d sent", sent))
	if unconfirmed > 0 {
		out += statusStyle.Render(" · ") + warnStyle.Render(fmt.Sprintf("%d unconfirmed", unconfirmed))
	}
	if failed > 0 {
		out += statusStyle.Render(" · ") + errStyle.Render(fmt.Sprintf("%d failed", failed))
	}
	if skipped > 0 {
		out += statusStyle.Render(" · ") + metaStyle.Render(fmt.Sprintf("%d not sent", skipped))
	}
	return out
}

// cancellable reports whether there is anything left for a cancel to drop: a
// queue whose last transfer is already on the wire has nothing to stop, and a
// single-copy send never does.
func (m Model) cancellable() bool { return m.sendIdx < len(m.sendItems)-1 }

// countObjekts pluralizes a copy count ("1 objekt", "3 objekts").
func countObjekts(n int) string {
	if n == 1 {
		return "1 objekt"
	}
	return fmt.Sprintf("%d objekts", n)
}

// fit truncates a label to the body width, leaving room for the marker and the
// trailing status each row appends.
func (m Model) fit(s string) string {
	w := m.width - 24
	if w < 8 {
		w = 8
	}
	return truncate(s, w)
}

// shortHash abbreviates a transaction hash. Same shape as an address, so it is
// the same abbreviation; the name is what tells the queue rows apart.
func shortHash(h string) string { return textfmt.ShortAddr(h) }

// sendCapacity is how many queue rows fit below a header of headLines lines.
func (m Model) sendCapacity(headLines int) int {
	if c := m.bodyHeight() - headLines; c > 0 {
		return c
	}
	return 1
}

// sendWindow is the visible slice of the queue rows at the given scroll offset.
// It clamps rather than trusting top, so a shrinking list (or a resize) cannot
// scroll the view off the end.
func sendWindow(rows []string, top, capacity int) []string {
	if top > len(rows)-capacity {
		top = len(rows) - capacity
	}
	if top < 0 {
		top = 0
	}
	end := top + capacity
	if end > len(rows) {
		end = len(rows)
	}
	return rows[top:end]
}

// scrollSend runs the send list's scroll motions, reporting whether msg was one
// of them. The queue screens share it, so a long list stays readable while the
// transfers run.
func (m *Model) scrollSend(msg tea.KeyPressMsg) bool {
	head, rows := m.sendScreen()
	capacity := m.sendCapacity(len(head))
	switch msg.String() {
	case "down", "j":
		m.sendTop++
	case "up", "k":
		m.sendTop--
	case "pgdown":
		m.sendTop += capacity
	case "pgup":
		m.sendTop -= capacity
	case "g":
		m.sendTop = 0
	case "G":
		m.sendTop = len(rows) - capacity
	default:
		return false
	}
	m.clampSendScroll(len(rows), capacity)
	return true
}

// followSend scrolls the transfer in flight into view, so the queue tracks
// itself as it works down the list.
func (m *Model) followSend() {
	head, rows := m.sendScreen()
	capacity := m.sendCapacity(len(head))
	if m.sendIdx >= m.sendTop+capacity {
		m.sendTop = m.sendIdx - capacity + 1
	} else if m.sendIdx < m.sendTop {
		m.sendTop = m.sendIdx
	}
	m.clampSendScroll(len(rows), capacity)
}

// clampSendScroll keeps the scroll offset inside the list.
func (m *Model) clampSendScroll(total, capacity int) {
	if m.sendTop > total-capacity {
		m.sendTop = total - capacity
	}
	if m.sendTop < 0 {
		m.sendTop = 0
	}
}

// renderGrid draws the visible window of card rows.
func (m Model) renderGrid() string {
	cols := m.columns()
	vis := m.visibleRows()
	totalRows := (len(m.cells) + cols - 1) / cols

	var rows []string
	for r := m.topRow; r < m.topRow+vis && r < totalRows; r++ {
		var cards []string
		for c := 0; c < cols; c++ {
			idx := r*cols + c
			if idx >= len(m.cells) {
				break
			}
			cards = append(cards, m.renderCard(m.cells[idx], idx == m.cursor))
		}
		rows = append(rows, joinCards(cards))
	}
	return strings.Join(rows, "\n")
}

// joinCards lays out one grid row with a column gap between cards.
func joinCards(cards []string) string {
	if len(cards) == 0 {
		return ""
	}
	gap := strings.Repeat(" ", colGap)
	spaced := make([]string, 0, len(cards)*2-1)
	for i, c := range cards {
		if i > 0 {
			spaced = append(spaced, gap)
		}
		spaced = append(spaced, c)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, spaced...)
}

// renderCard draws one copy as a fixed-size card, tinted with the collection's
// accent color. The two states it shows are independent: the card under the
// cursor is filled with that same accent so it reads as a solid block at a
// glance, and a marked card is drawn with a thick border. Both border weights
// are one cell wide, so neither state changes the card's size.
func (m Model) renderCard(cl cell, cursor bool) string {
	c := m.collections[cl.ci].Collection
	o := m.collections[cl.ci].Objekts[cl.oi]

	member := c.Member
	if c.Favorited() {
		member = "*" + member // pin is a collection property
	}
	l2 := c.CollectionNo + " " + c.Class

	l3 := fmt.Sprintf("#%d", o.ObjektNo)
	if flags := copyFlags(o); flags != "" {
		l3 = padTo(l3, cardInnerW-len(flags)-1) + " " + flags
	}

	content := strings.Join([]string{
		truncate(textfmt.Line(member), cardInnerW),
		truncate(textfmt.Line(l2), cardInnerW),
		truncate(textfmt.Line(l3), cardInnerW),
	}, "\n")

	accent := lipgloss.Color("8")
	if c.AccentColor != "" {
		accent = lipgloss.Color(c.AccentColor)
	}
	border := lipgloss.NormalBorder()
	if m.isSelected(cl) {
		border = lipgloss.ThickBorder()
	}
	box := lipgloss.NewStyle().
		Border(border).
		BorderForeground(accent).
		Width(cardW) // lipgloss counts the border in Width, so this is the outer size
	if cursor {
		// Fill the card body only: the border cells keep the terminal's own
		// background, so the accent outline still rings the block.
		box = box.Background(accent).Foreground(cardInk(c.AccentColor)).Bold(true)
	}
	return box.Render(content)
}

// inkThreshold is the perceived luminance (0-255) above which a fill counts as
// light and wants dark text.
const inkThreshold = 150

// cardInk picks the text color for a card filled with accent: black on light
// fills, white on dark ones. Accent colors are arbitrary hex chosen per
// collection, so a fixed foreground is unreadable over half of them. The two
// inks are literal black and white rather than palette 0 and 15 because the
// luminance they are chosen against is the accent's real one — a theme that
// maps 0 to a dark gray would undo the contrast this computes.
func cardInk(accent string) color.Color {
	r, g, b, ok := parseHex(accent)
	if !ok {
		return lipgloss.Color("#FFFFFF") // the gray fallback fill is dark
	}
	if 0.2126*float64(r)+0.7152*float64(g)+0.0722*float64(b) > inkThreshold {
		return lipgloss.Color("#000000")
	}
	return lipgloss.Color("#FFFFFF")
}

// parseHex splits a "#RRGGBB" color into its components. Anything else reports
// false, leaving the caller its fallback.
func parseHex(s string) (r, g, b int, ok bool) {
	h := strings.TrimPrefix(s, "#")
	if len(h) != 6 {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return int(v>>16) & 0xff, int(v>>8) & 0xff, int(v) & 0xff, true
}

// copyFlags is the compact per-copy marker string: g (used for a grid), L
// (locked / not transferable).
func copyFlags(o cosmo.OwnedObjekt) string {
	var f []byte
	if o.UsedForGrid {
		f = append(f, 'g')
	}
	if !o.Transferable {
		f = append(f, 'L')
	}
	return string(f)
}

// statusLine summarizes the collection, then key hints, the filter, or a
// transient notice. Counts come from the loaded meta so an optimistic pin shows
// right away.
func (m Model) statusLine() string {
	if m.filterMode {
		return statusStyle.Render(m.input.View()) + statusStyle.Render("  ·  enter: apply · esc: clear")
	}

	summary := fmt.Sprintf("%d copies · %d types", m.ownedCount(), m.meta.CollectionCount)
	line := statusStyle.Render(summary + "  |  ")

	if m.notice != "" {
		return line + warnStyle.Render(textfmt.Line(m.notice))
	}

	sort := "newest"
	if m.sort == cosmo.ObjektSortOldest {
		sort = "oldest"
	}
	// c is only advertised once there is something for it to clear.
	sel := "space: select"
	if n := len(m.selected); n > 0 {
		sel = fmt.Sprintf("space: select [%d] · c: clear", n)
	}
	hints := fmt.Sprintf("s: sort [%s] · p: pin [%d/%d] · %s · r: reload · /: filter",
		sort, m.pinnedCount(), cosmo.MaxFavoritedObjekts, sel)
	// t is advertised whenever sending is possible at all (see sendable).
	if m.sendable() {
		hints = "t: send · " + hints
	}
	if m.filter != "" {
		hints = fmt.Sprintf("filter %q (%d) · %s", m.filter, len(m.cells), hints)
	}
	return line + statusStyle.Render(hints)
}

// sendable reports whether t is worth advertising: the wallet is provisioned and
// there is a copy under the cursor. Whether that particular copy is transferable
// is deliberately not part of this — the hint would otherwise flicker in and out
// as the cursor crosses locked copies. Pressing t on a locked copy says so (see
// beginSend), which is more useful than a vanishing hint.
func (m Model) sendable() bool {
	if m.wallet == nil {
		return false
	}
	_, ok := m.selectedCell()
	return ok
}

// ownedCount is the total number of owned copies across all collections.
func (m Model) ownedCount() int {
	n := 0
	for i := range m.collections {
		n += m.collections[i].Count
	}
	return n
}

// renderDetail builds the detail-pane text for one owned copy: the collection
// identity, then its properties, then this specific copy's provenance. Every
// row is laid out as a label column beside a value column and truncated to the
// pane, so no line can wrap and shift the rows below it out of the frame.
func (m Model) renderDetail(oc cosmo.OwnedCollection, oi int) string {
	c := oc.Collection
	o := oc.Objekts[oi]
	var b strings.Builder

	head := fmt.Sprintf("%s %s %s", c.Member, c.CollectionNo, c.Class)
	b.WriteString(titleStyle.Render(truncate(textfmt.Line(head), detailW)) + "\n")
	meta := fmt.Sprintf("#%d · %s · %s", o.ObjektNo, c.Season, c.ArtistName)
	b.WriteString(metaStyle.Render(truncate(textfmt.Line(meta), detailW)) + "\n\n")

	if c.GeneratesComo() {
		b.WriteString(detailRow("Generates", fmt.Sprintf("%d COMO / month", c.ComoAmount)))
		if o.MintedAtDay > 0 {
			// The drop day belongs to the line above; it only has to sit on its
			// own row because the pane is too narrow to carry both.
			b.WriteString(detailRow("", "on the "+ordinal(o.MintedAtDay)))
		}
	}
	b.WriteString(detailRow("Transferable", yesNo(o.Transferable)))
	if o.UsedForGrid {
		b.WriteString(detailRow("Used for grid", "yes"))
	}
	if c.AccentColor != "" {
		b.WriteString(detailRow("Accent", c.AccentColor))
	}
	if c.Favorited() {
		b.WriteString(detailRow("Pinned", cosmo.LocalDate(c.FavoritedAt)))
	}

	b.WriteString("\n" + labelStyle.Render("This copy") + "\n")
	b.WriteString(detailRow("Serial", fmt.Sprintf("#%d", o.ObjektNo)))
	b.WriteString(detailRow("Acquired", cosmo.LocalDate(o.AcquiredAt)))
	if o.Owner != "" {
		b.WriteString(detailRow("Owner", textfmt.ShortAddr(o.Owner)))
	}
	if o.TokenAddress != "" {
		b.WriteString(detailRow("Token", textfmt.ShortAddr(o.TokenAddress)))
		b.WriteString(detailRow("Objekt ID", fmt.Sprintf("%d", o.ObjektID)))
	}

	return textfmt.Normalize(b.String())
}

// detailRow is one "label  value" line of the detail pane. The label is padded
// to a fixed column so the values line up, and an empty label continues the row
// above it. Both halves are truncated before they are styled — a truncation of
// styled text would cut an escape sequence in half.
func detailRow(label, value string) string {
	head := padTo(truncate(label, detailLabelW-1), detailLabelW)
	return labelStyle.Render(head) + truncate(textfmt.Line(value), detailW-detailLabelW) + "\n"
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// truncate shortens s to at most w runes, marking a cut with an ellipsis. Card
// text is ASCII, so rune count matches display width.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return string(r[:w-1]) + "…"
}

// padTo right-pads s with spaces to at least w runes (used to right-align a
// card's flags).
func padTo(s string, w int) string {
	if n := w - len([]rune(s)); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// ordinal renders a day-of-month as "1st", "2nd", "9th", "21st", etc.
func ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("%d%s", n, suffix)
}
