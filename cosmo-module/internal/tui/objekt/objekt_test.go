package objekt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"codeberg.org/djvu/cosmo-tui/internal/chain"
	"codeberg.org/djvu/cosmo-tui/internal/cosmo"
	"codeberg.org/djvu/cosmo-tui/internal/tui/uimsg"
	"codeberg.org/djvu/cosmo-tui/internal/tui/usersearch"
	"codeberg.org/djvu/cosmo-tui/internal/wallet"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// plain drops the styling from rendered output. lipgloss v2 always emits color
// escapes — unlike v1, which stripped them when stdout was not a terminal — so
// an assertion that spans a style boundary has to compare against plain text.
func plain(s string) string { return ansi.Strip(s) }

func sampleCols() cosmo.ObjektCollections {
	return cosmo.ObjektCollections{
		CollectionCount: 2,
		FavoritedCount:  1,
		Collections: []cosmo.OwnedCollection{
			{
				Collection: cosmo.ObjektCollection{
					CollectionNo: "101Z", Season: "Binary02", Class: "First",
					Member: "ChaeWon", ArtistName: "tripleS", ComoAmount: 1,
					Transferable: true, Gridable: true, AccentColor: "#75FB4C",
					FavoritedAt: "2026-03-15T05:13:59.000Z",
				},
				Count: 2,
				Objekts: []cosmo.OwnedObjekt{
					{ObjektNo: 4194, ObjektID: 21208645, TokenID: 21208645, Transferable: false, UsedForGrid: true, MintedAtDay: 8, AcquiredAt: "2026-03-08T07:12:20.000Z", TokenAddress: "0x99Bb83AE9bb0C0A6be865CaCF67760947f91Cb70"},
					{ObjektNo: 8001, ObjektID: 21999999, TokenID: 21999999, Transferable: true, MintedAtDay: 12, AcquiredAt: "2026-04-01T00:00:00.000Z"},
				},
			},
			{
				Collection: cosmo.ObjektCollection{
					CollectionNo: "201Z", Season: "Binary02", Class: "Special",
					Member: "YooYeon", ArtistName: "tripleS", ComoAmount: 1,
				},
				Count:   1,
				Objekts: []cosmo.OwnedObjekt{{ObjektNo: 570, ObjektID: 22000570, MintedAtDay: 9, AcquiredAt: "2026-03-09T05:05:06.000Z", Transferable: true}},
			},
		},
	}
}

// loadedModel builds a sized, loaded model.
func loadedModel(t *testing.T, cols cosmo.ObjektCollections) Model {
	t.Helper()
	m := New(nil, "tripleS", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	updated, _ = m.Update(loadedMsg{cols: cols})
	return updated.(Model)
}

func press(t *testing.T, m Model, s string) (Model, tea.Cmd) {
	t.Helper()
	var msg tea.KeyPressMsg
	switch s {
	case "enter":
		msg = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "up":
		msg = tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		msg = tea.KeyPressMsg{Code: tea.KeyDown}
	case "esc":
		msg = tea.KeyPressMsg{Code: tea.KeyEsc}
	case "space":
		// Space carries " " as its text, which Key.String() skips in favour of
		// the "space" keystroke name — so it must be built by code, not rune.
		msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	default:
		msg = tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
	}
	updated, cmd := m.Update(msg)
	return updated.(Model), cmd
}

// TestFlattenToPerCopy checks every owned copy becomes its own grid cell, in
// server order.
func TestFlattenToPerCopy(t *testing.T) {
	m := loadedModel(t, sampleCols())
	if len(m.cells) != 3 {
		t.Fatalf("expected 3 per-copy cells, got %d", len(m.cells))
	}
	// First two cells are the two copies of collection 0, the third is
	// collection 1's single copy.
	want := []struct{ ci, oi int }{{0, 0}, {0, 1}, {1, 0}}
	for i, w := range want {
		if m.cells[i].ci != w.ci || m.cells[i].oi != w.oi {
			t.Fatalf("cell %d = %+v, want %+v", i, m.cells[i], w)
		}
	}
}

// TestOwnedAndTypeCounts checks the status summary counts copies and types.
func TestOwnedAndTypeCounts(t *testing.T) {
	m := loadedModel(t, sampleCols())
	status := m.statusLine()
	for _, want := range []string{"3 copies", "2 types", "p: pin [1/9]", "s: sort [newest]", "space: select"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status %q missing %q", status, want)
		}
	}
}

// TestGridColumnsAndCursor checks 2D motion: right/left step one cell, down/up
// step a full row (columns), and edges are walls.
func TestGridColumnsAndCursor(t *testing.T) {
	// A narrow window forces a single column so down/up walk the whole list.
	m := New(nil, "tripleS", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: cardW + 1, Height: 40})
	m = updated.(Model)
	updated, _ = m.Update(loadedMsg{cols: sampleCols()})
	m = updated.(Model)

	if got := m.columns(); got != 1 {
		t.Fatalf("columns = %d, want 1 in a one-card-wide window", got)
	}
	// Left at the top edge is a wall.
	m, _ = press(t, m, "h")
	if m.cursor != 0 {
		t.Fatalf("left at cell 0 should not move, cursor = %d", m.cursor)
	}
	// Down walks one row (== one cell here).
	m, _ = press(t, m, "j")
	m, _ = press(t, m, "j")
	if m.cursor != 2 {
		t.Fatalf("two downs should reach cell 2, cursor = %d", m.cursor)
	}
	// Down at the bottom edge is a wall.
	m, _ = press(t, m, "j")
	if m.cursor != 2 {
		t.Fatalf("down at last cell should not move, cursor = %d", m.cursor)
	}
	// Up returns.
	m, _ = press(t, m, "k")
	if m.cursor != 1 {
		t.Fatalf("up should reach cell 1, cursor = %d", m.cursor)
	}
}

// TestHorizontalMotionWraps checks that in a multi-column grid, right moves
// across columns and down moves by a full row.
func TestHorizontalMotionWraps(t *testing.T) {
	m := loadedModel(t, sampleCols()) // width 100 -> multiple columns
	if m.columns() < 2 {
		t.Fatalf("test needs a multi-column grid, got %d columns", m.columns())
	}
	m, _ = press(t, m, "l")
	if m.cursor != 1 {
		t.Fatalf("right should move to cell 1, cursor = %d", m.cursor)
	}
	m, _ = press(t, m, "h")
	if m.cursor != 0 {
		t.Fatalf("left should move back to cell 0, cursor = %d", m.cursor)
	}
}

// TestGGotoEnds checks g/G jump to the first and last cell.
func TestGGotoEnds(t *testing.T) {
	m := loadedModel(t, sampleCols())
	m, _ = press(t, m, "G")
	if m.cursor != 2 {
		t.Fatalf("G should select the last cell, cursor = %d", m.cursor)
	}
	m, _ = press(t, m, "g")
	if m.cursor != 0 || m.topRow != 0 {
		t.Fatalf("g should reset to cell 0 / top row, cursor = %d topRow = %d", m.cursor, m.topRow)
	}
}

// TestRowWindowing checks the grid scrolls so the cursor's row stays visible.
func TestRowWindowing(t *testing.T) {
	// One column, a short window: only a couple of rows are visible at a time.
	cols := manyCopies(20)
	m := New(nil, "tripleS", nil)
	// Height leaves room for ~2 card rows in the body.
	updated, _ := m.Update(tea.WindowSizeMsg{Width: cardW + 1, Height: 2*cardH + 1 + 1})
	m = updated.(Model)
	updated, _ = m.Update(loadedMsg{cols: cols})
	m = updated.(Model)

	if m.columns() != 1 {
		t.Fatalf("columns = %d, want 1", m.columns())
	}
	vis := m.visibleRows()
	if vis < 1 {
		t.Fatalf("visibleRows = %d", vis)
	}
	if m.topRow != 0 {
		t.Fatalf("topRow should start at 0, got %d", m.topRow)
	}
	// Walk to the end; the window must have scrolled to keep the cursor in view.
	m, _ = press(t, m, "G")
	row := m.cursor / m.columns()
	if row < m.topRow || row >= m.topRow+vis {
		t.Fatalf("cursor row %d out of window [%d,%d)", row, m.topRow, m.topRow+vis)
	}
	if m.topRow == 0 {
		t.Fatal("expected the window to scroll for a 20-cell single column")
	}
}

// TestDetailPaneBesideGrid checks the detail pane is drawn beside the grid
// rather than in place of it: both the cards and the highlighted copy's detail
// are on screen at once.
func TestDetailPaneBesideGrid(t *testing.T) {
	m := loadedModel(t, sampleCols())
	if !m.detailShown() {
		t.Fatalf("width %d should carry the detail pane", m.width)
	}
	body := plain(m.render())
	for _, want := range []string{"101Z", "#4194", "This copy", "Transferable"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
	// The grid is still there: the second collection's card is drawn even though
	// the pane details the first.
	if !strings.Contains(body, "YooYeon") {
		t.Fatalf("grid cards missing from the split body:\n%s", body)
	}
	// The first row carries both panes side by side, not one above the other.
	if first := strings.SplitN(body, "\n", 2)[0]; !strings.Contains(first, "ChaeWon 101Z First") {
		t.Fatalf("detail head not on the grid's first row: %q", first)
	}
}

// TestEnterIsSwallowed checks enter no longer opens anything: the detail is
// already on screen, so descend has nowhere to go and must not disturb the grid.
func TestEnterIsSwallowed(t *testing.T) {
	m := loadedModel(t, sampleCols())
	m, _ = press(t, m, "l")
	before := m.cursor
	m, cmd := press(t, m, "enter")
	if m.cursor != before || cmd != nil {
		t.Fatalf("enter moved the page: cursor %d -> %d, cmd %v", before, m.cursor, cmd)
	}
}

// detailValue reads back the value beside a label in a rendered detail pane, so
// an assertion does not have to know how wide the label column is padded to.
// Returns "" when the label is not on screen.
func detailValue(pane, label string) string {
	for _, ln := range strings.Split(plain(pane), "\n") {
		if rest, ok := strings.CutPrefix(ln, label); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// TestDetailFollowsCursor checks the detail pane tracks the highlighted copy as
// the cursor moves, and shows per-copy provenance.
func TestDetailFollowsCursor(t *testing.T) {
	m := loadedModel(t, sampleCols())
	// Cell 0 is copy #4194, the locked one.
	got := plain(m.detailPane())
	if !strings.Contains(got, "#4194") || detailValue(got, "Transferable") != "no" {
		t.Fatalf("copy 0 detail wrong:\n%s", got)
	}
	// Move to the third cell (collection 1, copy #570).
	m, _ = press(t, m, "l")
	m, _ = press(t, m, "l")
	detail := plain(m.detailPane())
	if !strings.Contains(detail, "201Z") || !strings.Contains(detail, "#570") {
		t.Fatalf("detail did not follow to 201Z/#570:\n%s", detail)
	}
	if strings.Contains(detail, "#4194") {
		t.Fatalf("detail still shows the previous copy:\n%s", detail)
	}
}

// TestDetailPaneFitsItsFrame checks the pane never overflows the box it is
// placed in — a row wider than the pane or a body taller than the page would
// wrap the joined frame and desync the renderer.
func TestDetailPaneFitsItsFrame(t *testing.T) {
	cols := sampleCols()
	// A member name and an artist name far longer than the pane is wide.
	cols.Collections[0].Collection.Member = strings.Repeat("Chae", 20)
	cols.Collections[0].Collection.ArtistName = strings.Repeat("triple", 20)

	m := New(nil, "tripleS", nil)
	// A body only a few rows tall, so the clip has something to cut.
	updated, _ := m.Update(tea.WindowSizeMsg{Width: minSplitWidth, Height: 6})
	m = updated.(Model)
	updated, _ = m.Update(loadedMsg{cols: cols})
	m = updated.(Model)

	lines := strings.Split(plain(m.detailPane()), "\n")
	if len(lines) > m.bodyHeight() {
		t.Fatalf("detail pane is %d lines, body is %d", len(lines), m.bodyHeight())
	}
	for i, ln := range lines {
		if w := lipgloss.Width(ln); w > detailW {
			t.Fatalf("detail line %d is %d wide, pane is %d: %q", i, w, detailW, ln)
		}
	}
	// The frame the page hands the shell has to hold its own width too.
	for i, ln := range strings.Split(plain(m.render()), "\n") {
		if w := lipgloss.Width(ln); w > m.width {
			t.Fatalf("rendered line %d is %d wide, page is %d: %q", i, w, m.width, ln)
		}
	}
}

// TestNarrowPageDropsDetailPane checks a page too narrow to hold the pane beside
// a two-card grid gives the whole width to the grid instead of squeezing it.
func TestNarrowPageDropsDetailPane(t *testing.T) {
	m := New(nil, "tripleS", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: minSplitWidth - 1, Height: 30})
	m = updated.(Model)
	updated, _ = m.Update(loadedMsg{cols: sampleCols()})
	m = updated.(Model)

	if m.detailShown() {
		t.Fatalf("width %d should drop the detail pane", m.width)
	}
	if m.gridWidth() != m.width {
		t.Fatalf("gridWidth = %d, want the whole page (%d)", m.gridWidth(), m.width)
	}
	if body := plain(m.render()); strings.Contains(body, "This copy") {
		t.Fatalf("narrow page still drew the detail pane:\n%s", body)
	}
	// One card wider and the pane is back, with the grid still two cards across.
	updated, _ = m.Update(tea.WindowSizeMsg{Width: minSplitWidth, Height: 30})
	m = updated.(Model)
	if !m.detailShown() {
		t.Fatalf("width %d should carry the detail pane", m.width)
	}
	if got := m.columns(); got != 2 {
		t.Fatalf("columns = %d at the split threshold, want 2", got)
	}
}

// TestDetailComoGeneration checks the copy detail shows generation + drop day
// for a generating collection and omits it otherwise.
func TestDetailComoGeneration(t *testing.T) {
	m := loadedModel(t, sampleCols())
	first := plain(m.renderDetail(m.collections[0], 0)) // First class
	if strings.Contains(first, "Generates") {
		t.Fatalf("First-class copy should not mention COMO generation:\n%s", first)
	}
	special := plain(m.renderDetail(m.collections[1], 0)) // Special class
	if !strings.Contains(special, "Generates") || !strings.Contains(special, "on the 9th") {
		t.Fatalf("Special copy missing generation/drop-day:\n%s", special)
	}
}

// TestFilterNarrowsCells checks "/" filters copies by collection metadata and
// esc clears it. The filter matches collection fields, so it keeps every copy
// of a matched collection.
func TestFilterNarrowsCells(t *testing.T) {
	m := loadedModel(t, sampleCols())
	// Enter filter mode and type a member name matching only collection 1.
	m, _ = press(t, m, "/")
	if !m.AcceptsText() {
		t.Fatal("/ should start capturing text")
	}
	for _, r := range "YooYeon" {
		m, _ = press(t, m, string(r))
	}
	if len(m.cells) != 1 || m.cells[0].ci != 1 {
		t.Fatalf("filter should leave collection 1's single copy, got %d cells", len(m.cells))
	}
	// Enter applies and keeps the filter (no longer capturing text).
	m, _ = press(t, m, "enter")
	if m.AcceptsText() {
		t.Fatal("enter should stop capturing text")
	}
	if m.filter != "YooYeon" || len(m.cells) != 1 {
		t.Fatalf("applied filter not kept: filter=%q cells=%d", m.filter, len(m.cells))
	}
	// esc clears the applied filter straight from the grid, as on the list tabs.
	m, _ = press(t, m, "esc")
	if m.filter != "" || len(m.cells) != 3 {
		t.Fatalf("esc should clear the filter: filter=%q cells=%d", m.filter, len(m.cells))
	}
}

// TestPinFromCopyIsCollectionLevel checks pinning from a copy flips the whole
// collection (every sibling copy reads pinned) and counts once.
func TestPinFromCopyIsCollectionLevel(t *testing.T) {
	m := loadedModel(t, sampleCols())
	// Cursor on cell 0 (collection 0, currently pinned). Unpin it.
	if !m.collections[0].Collection.Favorited() {
		t.Fatal("precondition: collection 0 is pinned")
	}
	m, cmd := press(t, m, "p")
	if cmd == nil {
		t.Fatal("unpin should send a request")
	}
	if m.collections[0].Collection.Favorited() {
		t.Fatal("collection 0 should be optimistically unpinned")
	}
	// Cell 1 is the sibling copy of the same collection: it reads unpinned too.
	if m.collections[m.cells[1].ci].Collection.Favorited() {
		t.Fatal("sibling copy should reflect the collection-level unpin")
	}
	if got := m.statusLine(); !strings.Contains(got, "p: pin [0/9]") {
		t.Fatalf("status %q should count 0 pins", got)
	}
}

// TestPinLimitBlocks checks pinning past the cap is refused with a notice.
func TestPinLimitBlocks(t *testing.T) {
	cols := pinnedSample(10, cosmo.MaxFavoritedObjekts) // 9 pinned, cursor on the 10th
	m := loadedModel(t, cols)
	// Move the cursor to the last (unpinned) collection's copy.
	m, _ = press(t, m, "G")
	c, _ := m.selectedCell()
	if m.collections[c.ci].Collection.Favorited() {
		t.Fatal("precondition: last collection is unpinned")
	}
	m, cmd := press(t, m, "p")
	if cmd != nil {
		t.Fatal("must not send a request once the pin limit is reached")
	}
	if m.collections[c.ci].Collection.Favorited() {
		t.Fatal("the collection must stay unpinned")
	}
	if !strings.Contains(m.notice, "max pin limit reached") {
		t.Fatalf("notice = %q, want the pin-limit warning", m.notice)
	}
	// The next keypress dismisses the notice.
	m, _ = press(t, m, "j")
	if m.notice != "" {
		t.Fatalf("notice should clear on the next key, got %q", m.notice)
	}
}

// TestPinFailureReverts checks a rejected toggle puts the collection back.
func TestPinFailureReverts(t *testing.T) {
	m := loadedModel(t, pinnedSample(3, 0)) // none pinned
	m, _ = press(t, m, "p")
	if !m.collections[0].Collection.Favorited() {
		t.Fatal("precondition: collection 0 pinned optimistically")
	}
	updated, _ := m.Update(pinnedMsg{ci: 0, pin: true, err: errors.New("boom")})
	m = updated.(Model)
	if m.collections[0].Collection.Favorited() {
		t.Fatal("a failed pin must revert the collection")
	}
	if !strings.Contains(m.notice, "pin failed") {
		t.Fatalf("notice = %q, want a failure message", m.notice)
	}
}

// --- pinning the selection ---

// markCells marks the copies at the given cell indices, leaving the cursor on
// the last of them. It sets the cursor directly rather than walking it with
// motion keys: only m.cells[m.cursor] matters to a mark.
func markCells(t *testing.T, m Model, idx ...int) Model {
	t.Helper()
	for _, i := range idx {
		if i >= len(m.cells) {
			t.Fatalf("cell %d out of range (%d cells)", i, len(m.cells))
		}
		m.cursor = i
		m, _ = press(t, m, "space")
	}
	return m
}

// pinRequests is how many requests cmd fans out to, and asserts they are
// sequenced rather than concurrent. It runs cmd itself — the message tea.Sequence
// produces carries the inner commands without running them — so it may only be
// used where more than one request is expected: a lone command collapses to
// itself, and running that one would call the (nil) API client.
//
// tea.Sequence's message type is unexported, so its commands are counted
// reflectively rather than through a type assertion; what matters for the
// assertion is that it is *not* a tea.BatchMsg, which would run the writes
// concurrently and draw a 409 from the API.
func pinRequests(t *testing.T, cmd tea.Cmd) int {
	t.Helper()
	if cmd == nil {
		return 0
	}
	msg := cmd()
	if _, batched := msg.(tea.BatchMsg); batched {
		t.Fatal("pin requests must be sequenced, not batched: overlapping favorite writes 409")
	}
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice {
		t.Fatalf("expected a sequence of pin requests, got %T", msg)
	}
	return v.Len()
}

// pinnedSet lists the indices of the pinned collections, for comparing whole
// pin states in one assertion.
func pinnedSet(m Model) []int {
	var out []int
	for i := range m.collections {
		if m.collections[i].Collection.Favorited() {
			out = append(out, i)
		}
	}
	return out
}

// TestPinTargetsDedupe checks the pin targets are distinct collections: two
// marked copies of one collection are one target, not two requests.
func TestPinTargetsDedupe(t *testing.T) {
	m := loadedModel(t, sampleCols())

	// Nothing marked: the target is the collection under the cursor.
	m.cursor = 2 // collection 1's only copy
	if got := m.pinTargets(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("pinTargets with no selection = %v, want [1]", got)
	}

	// Cells 0 and 1 are the two copies of collection 0.
	m = markCells(t, m, 0, 1)
	if got := m.pinTargets(); len(got) != 1 || got[0] != 0 {
		t.Fatalf("pinTargets = %v, want the shared collection once", got)
	}
	// Marking collection 1's copy too adds it, in grid order.
	m = markCells(t, m, 2)
	if got := m.pinTargets(); len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Fatalf("pinTargets = %v, want [0 1]", got)
	}
}

// TestPinAppliesToSelection checks p pins every marked copy's collection rather
// than the cursor's, and leaves the marks in place afterwards.
func TestPinAppliesToSelection(t *testing.T) {
	m := loadedModel(t, pinnedSample(4, 0)) // one copy each, none pinned
	m = markCells(t, m, 0, 2)
	m.cursor = 3 // the cursor is not marked: it must not be pinned

	m, cmd := press(t, m, "p")
	if got := pinRequests(t, cmd); got != 2 {
		t.Fatalf("batched %d pin requests, want 2", got)
	}
	if got := pinnedSet(m); len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Fatalf("pinned = %v, want the two marked collections", got)
	}
	if len(m.selected) != 2 {
		t.Fatalf("pinning must keep the selection, got %d marks", len(m.selected))
	}
	if got := m.statusLine(); !strings.Contains(got, "p: pin [2/9]") {
		t.Fatalf("status %q should count 2 pins", got)
	}
}

// TestPinSelectionUnpinsWhenAllPinned checks the toggle reverses once every
// target is pinned, so the same marked set turns its own pins back off.
func TestPinSelectionUnpinsWhenAllPinned(t *testing.T) {
	m := loadedModel(t, pinnedSample(4, 2)) // collections 0 and 1 pinned
	m = markCells(t, m, 0, 1)

	m, cmd := press(t, m, "p")
	if got := pinRequests(t, cmd); got != 2 {
		t.Fatalf("batched %d unpin requests, want 2", got)
	}
	if got := pinnedSet(m); got != nil {
		t.Fatalf("pinned = %v, want everything unpinned", got)
	}
	if len(m.selected) != 2 {
		t.Fatalf("unpinning must keep the selection, got %d marks", len(m.selected))
	}
}

// TestPinSelectionSkipsAlreadyPinned checks a mixed selection converges on
// pinned: the unpinned targets are pinned and the pinned one is left alone
// rather than flipped off.
func TestPinSelectionSkipsAlreadyPinned(t *testing.T) {
	m := loadedModel(t, pinnedSample(4, 1)) // collection 0 pinned
	m = markCells(t, m, 0, 1, 2)

	m, cmd := press(t, m, "p")
	if got := pinRequests(t, cmd); got != 2 {
		t.Fatalf("batched %d requests, want 2 (the already-pinned target is skipped)", got)
	}
	if got := pinnedSet(m); len(got) != 3 || got[2] != 2 {
		t.Fatalf("pinned = %v, want collections 0, 1 and 2", got)
	}
}

// TestPinSelectionLimitBlocks checks a selection that would pass the cap pins
// nothing at all — the API would answer 201 and silently evict the oldest pins.
func TestPinSelectionLimitBlocks(t *testing.T) {
	m := loadedModel(t, pinnedSample(12, 8)) // 8 pinned, room for one more
	m = markCells(t, m, 8, 9, 10)

	before := pinnedSet(m)
	m, cmd := press(t, m, "p")
	if cmd != nil {
		t.Fatal("must not send anything when the batch would pass the cap")
	}
	if got := pinnedSet(m); len(got) != len(before) {
		t.Fatalf("pinned = %v, want the %d pins unchanged", got, len(before))
	}
	if !strings.Contains(m.notice, "would pass the max of 9") {
		t.Fatalf("notice = %q, want the bulk pin-limit warning", m.notice)
	}
	if len(m.selected) != 3 {
		t.Fatalf("a refused pin must keep the selection, got %d marks", len(m.selected))
	}

	// Trimming the selection to what fits goes through.
	m = markCells(t, m, 9, 10) // unmark two
	m, cmd = press(t, m, "p")
	if cmd == nil {
		t.Fatal("one more pin fits and should be sent")
	}
	if got := pinnedSet(m); len(got) != 9 {
		t.Fatalf("pinned %d collections, want 9", len(got))
	}
}

// TestSortToggleReloads checks "s" flips the order and issues a reload command.
func TestSortToggleReloads(t *testing.T) {
	m := loadedModel(t, sampleCols())
	if m.sort != cosmo.ObjektSortNewest {
		t.Fatalf("initial sort = %q", m.sort)
	}
	m, cmd := press(t, m, "s")
	if m.sort != cosmo.ObjektSortOldest {
		t.Fatalf("after toggle sort = %q, want oldest", m.sort)
	}
	if cmd == nil {
		t.Fatal("sort toggle should issue a reload command")
	}
	if m.loaded {
		t.Fatal("loaded should reset while the re-sorted page loads")
	}
}

// TestGroupChangeReloads checks a group switch clears cells and fetches.
func TestGroupChangeReloads(t *testing.T) {
	m := loadedModel(t, sampleCols())
	updated, cmd := m.Update(uimsg.GroupChanged{Group: "artms"})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("group change should issue a load command")
	}
	if len(m.cells) != 0 || m.loaded {
		t.Fatal("cells should clear and loaded reset on group change")
	}
	if m.group != "artms" {
		t.Fatalf("group = %q, want artms", m.group)
	}
}

// TestGeneratesComo checks the class allowlist gate.
func TestGeneratesComo(t *testing.T) {
	cols := sampleCols()
	if cols.Collections[0].Collection.GeneratesComo() {
		t.Fatal("First class must not generate COMO")
	}
	if !cols.Collections[1].Collection.GeneratesComo() {
		t.Fatal("Special class must generate COMO")
	}
}

// TestOrdinal checks day-of-month suffixes, including the 11-13 exception.
func TestOrdinal(t *testing.T) {
	cases := map[int]string{1: "1st", 2: "2nd", 3: "3rd", 4: "4th", 9: "9th",
		11: "11th", 12: "12th", 13: "13th", 21: "21st", 22: "22nd", 23: "23rd", 31: "31st"}
	for n, want := range cases {
		if got := ordinal(n); got != want {
			t.Errorf("ordinal(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestTruncate checks the card-text truncation and ellipsis.
func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		w    int
		want string
	}{
		{"ChaeWon", 13, "ChaeWon"},
		{"ChaeWon", 4, "Cha…"},
		{"abc", 0, ""},
		{"abc", 1, "…"},
	}
	for _, tt := range tests {
		if got := truncate(tt.in, tt.w); got != tt.want {
			t.Errorf("truncate(%q,%d) = %q, want %q", tt.in, tt.w, got, tt.want)
		}
	}
}

// TestCardInk checks the highlighted card's text flips to black over a light
// accent and white over a dark one, and falls back to white when the
// collection has no accent to measure.
func TestCardInk(t *testing.T) {
	tests := []struct {
		accent string
		want   string
	}{
		{"#75FB4C", "#000000"}, // bright green
		{"#FFD166", "#000000"}, // pale yellow
		{"#34495E", "#FFFFFF"}, // dark slate
		{"#2B6CB0", "#FFFFFF"}, // mid blue
		{"", "#FFFFFF"},        // no accent: the gray fallback fill is dark
		{"#GGGGGG", "#FFFFFF"}, // unparseable
	}
	for _, tt := range tests {
		if got := cardInk(tt.accent); got != lipgloss.Color(tt.want) {
			t.Errorf("cardInk(%q) = %v, want %v", tt.accent, got, lipgloss.Color(tt.want))
		}
	}
}

// TestCursorCardFilled checks the cursor's card is painted with the
// collection's accent as a background while its neighbours are not, and that
// the fill leaves the card's size alone.
func TestCursorCardFilled(t *testing.T) {
	m := loadedModel(t, sampleCols())

	sel := m.renderCard(m.cells[0], true)
	unsel := m.renderCard(m.cells[0], false)

	// "48;2;117;251;76" is an SGR true-color background of #75FB4C.
	const fill = "48;2;117;251;76"
	if !strings.Contains(sel, fill) {
		t.Errorf("selected card is not filled with the accent background:\n%q", sel)
	}
	if strings.Contains(unsel, fill) {
		t.Errorf("unselected card should not be filled:\n%q", unsel)
	}
	if got, want := lipgloss.Width(sel), lipgloss.Width(unsel); got != want {
		t.Errorf("selection changed the card width: %d, want %d", got, want)
	}
	if got, want := lipgloss.Height(sel), lipgloss.Height(unsel); got != want {
		t.Errorf("selection changed the card height: %d, want %d", got, want)
	}
	if got, want := plain(sel), plain(unsel); got != want {
		t.Errorf("selection changed the card text:\n got %q\nwant %q", got, want)
	}
}

// --- selection ---

// TestSelectToggleAndClear checks space marks and unmarks the copy under the
// cursor, that marks accumulate across cells, and that c drops all of them.
func TestSelectToggleAndClear(t *testing.T) {
	m := loadedModel(t, sampleCols())
	first := sampleCols().Collections[0].Objekts[0].ObjektID

	m, _ = press(t, m, "space")
	if _, ok := m.selected[first]; !ok || len(m.selected) != 1 {
		t.Fatalf("space should mark the cursor's copy, selected = %v", m.selected)
	}
	// The cursor is unmoved by a toggle.
	if m.cursor != 0 {
		t.Fatalf("space moved the cursor to %d", m.cursor)
	}
	m, _ = press(t, m, "space")
	if len(m.selected) != 0 {
		t.Fatalf("space again should unmark, selected = %v", m.selected)
	}

	// Mark two different copies, then clear.
	m, _ = press(t, m, "space")
	m, _ = press(t, m, "l")
	m, _ = press(t, m, "space")
	if len(m.selected) != 2 {
		t.Fatalf("expected 2 marks, got %d", len(m.selected))
	}
	if got := m.statusLine(); !strings.Contains(got, "space: select [2]") || !strings.Contains(got, "c: clear") {
		t.Fatalf("status %q should count the marks and offer c", got)
	}
	m, _ = press(t, m, "c")
	if len(m.selected) != 0 {
		t.Fatalf("c should clear every mark, got %d", len(m.selected))
	}
	if got := m.statusLine(); strings.Contains(got, "c: clear") {
		t.Fatalf("status %q should stop offering c with nothing marked", got)
	}
}

// TestSelectionSurvivesFilter checks marks are keyed by copy, not by cell: a
// filter that hides a marked copy keeps the mark, and marks made under two
// different filters union together.
func TestSelectionSurvivesFilter(t *testing.T) {
	m := loadedModel(t, sampleCols())
	m, _ = press(t, m, "space") // mark the ChaeWon copy #4194

	// Filter down to the YooYeon collection: the marked copy is now hidden.
	m, _ = press(t, m, "/")
	for _, r := range "YooYeon" {
		m, _ = press(t, m, string(r))
	}
	m, _ = press(t, m, "enter")
	if len(m.cells) != 1 {
		t.Fatalf("filter should leave 1 cell, got %d", len(m.cells))
	}
	if len(m.selected) != 1 {
		t.Fatalf("filtering must not drop the hidden mark, selected = %d", len(m.selected))
	}
	// Mark the visible copy too.
	m, _ = press(t, m, "space")
	if len(m.selected) != 2 {
		t.Fatalf("expected the marks to union to 2, got %d", len(m.selected))
	}

	// Clearing the filter brings every cell back, both still marked.
	m, _ = press(t, m, "esc")
	if len(m.cells) != 3 || len(m.selected) != 2 {
		t.Fatalf("after clearing the filter: cells=%d selected=%d, want 3/2", len(m.cells), len(m.selected))
	}
	if got := m.selectedCells(); len(got) != 2 || got[0] != (cell{0, 0}) || got[1] != (cell{1, 0}) {
		t.Fatalf("selectedCells = %+v, want the two marked copies in grid order", got)
	}
}

// TestSelectionSurvivesSort checks a reload (sort toggle, r) keeps the marks:
// the same copies are still owned, so the same copies stay marked.
func TestSelectionSurvivesSort(t *testing.T) {
	m := loadedModel(t, sampleCols())
	m, _ = press(t, m, "space")
	m, _ = press(t, m, "s") // issues a reload; the result arrives as loadedMsg

	// The re-sorted payload lists the collections in the other order.
	reordered := sampleCols()
	reordered.Collections[0], reordered.Collections[1] = reordered.Collections[1], reordered.Collections[0]
	updated, _ := m.Update(loadedMsg{cols: reordered})
	m = updated.(Model)

	if len(m.selected) != 1 {
		t.Fatalf("a reload should keep the mark, selected = %d", len(m.selected))
	}
	// The marked copy has moved to the end of the grid; the mark moved with it.
	if got := m.selectedCells(); len(got) != 1 || got[0] != (cell{1, 0}) {
		t.Fatalf("selectedCells = %+v, want the marked copy at its new position", got)
	}
}

// TestSelectionPrunedOnReload checks a copy that leaves the collection (sent or
// traded away) stops being counted.
func TestSelectionPrunedOnReload(t *testing.T) {
	m := loadedModel(t, sampleCols())
	sentID := sampleCols().Collections[0].Objekts[1].ObjektID

	m, _ = press(t, m, "space") // cell 0
	m, _ = press(t, m, "l")
	m, _ = press(t, m, "space") // cell 1, the copy about to leave
	if len(m.selected) != 2 {
		t.Fatalf("precondition: 2 marks, got %d", len(m.selected))
	}

	updated, _ := m.Update(sentReloadMsg{cols: withoutObjekt(sampleCols(), sentID), objektIDs: []int64{sentID}, attempt: 1})
	m = updated.(Model)
	if len(m.selected) != 1 {
		t.Fatalf("the departed copy should be pruned, selected = %d", len(m.selected))
	}
	if _, ok := m.selected[sentID]; ok {
		t.Fatal("the sent copy is still marked")
	}
}

// TestSelectionClearedOnGroupChange checks marks do not leak across artists.
func TestSelectionClearedOnGroupChange(t *testing.T) {
	m := loadedModel(t, sampleCols())
	m, _ = press(t, m, "space")
	updated, _ := m.Update(uimsg.GroupChanged{Group: "artms"})
	m = updated.(Model)
	if len(m.selected) != 0 {
		t.Fatalf("a group change should clear the selection, got %d", len(m.selected))
	}
}

// TestSelectedCardThickBorder checks a marked card is drawn with a thick border
// and an unmarked one with the normal border, without changing the card's size
// or text (the grid tiles on a fixed card geometry).
func TestSelectedCardThickBorder(t *testing.T) {
	m := loadedModel(t, sampleCols())

	unmarked := m.renderCard(m.cells[0], false)
	if !strings.Contains(unmarked, "┌") {
		t.Fatalf("unmarked card should use the normal border:\n%s", plain(unmarked))
	}

	m.toggleSelected() // cursor is on cell 0
	marked := m.renderCard(m.cells[0], false)
	for _, want := range []string{"┏", "┓", "┃", "┗"} {
		if !strings.Contains(marked, want) {
			t.Fatalf("marked card missing %q:\n%s", want, plain(marked))
		}
	}
	if strings.Contains(marked, "┌") {
		t.Fatalf("marked card still uses the normal border:\n%s", plain(marked))
	}
	if got, want := lipgloss.Width(marked), lipgloss.Width(unmarked); got != want {
		t.Errorf("selection changed the card width: %d, want %d", got, want)
	}
	if got, want := lipgloss.Height(marked), lipgloss.Height(unmarked); got != want {
		t.Errorf("selection changed the card height: %d, want %d", got, want)
	}

	// Marking is independent of the cursor fill: a marked card under the cursor
	// keeps both the accent fill and the thick border.
	both := m.renderCard(m.cells[0], true)
	if !strings.Contains(both, "┏") || !strings.Contains(both, "48;2;117;251;76") {
		t.Errorf("a marked card under the cursor should show both states:\n%q", both)
	}
}

// TestGridNoOverflow checks the rendered grid never exceeds the terminal width
// and packs the expected number of cards per row (the textfmt width hazard).
func TestGridNoOverflow(t *testing.T) {
	for _, w := range []int{40, 60, 100, 137} {
		m := New(nil, "tripleS", nil)
		updated, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: 30})
		m = updated.(Model)
		updated, _ = m.Update(loadedMsg{cols: manyCopies(25)})
		m = updated.(Model)

		grid := m.renderGrid()
		for _, line := range strings.Split(grid, "\n") {
			if got := lipgloss.Width(line); got > w {
				t.Fatalf("width %d: grid line overflows (%d > %d): %q", w, got, w, line)
			}
		}
		// The full view (body + status) must also fit and not exceed the height.
		view := m.render()
		if h := lipgloss.Height(view); h > 30 {
			t.Fatalf("width %d: view is %d rows, want <= 30", w, h)
		}
	}
}

// manyCopies builds one collection with n copies, for grid-windowing tests.
// Copies get distinct objektIds: that is the selection key, and leaving them all
// zero would make every copy share one mark.
func manyCopies(n int) cosmo.ObjektCollections {
	objs := make([]cosmo.OwnedObjekt, n)
	for i := range objs {
		objs[i] = cosmo.OwnedObjekt{
			ObjektNo: int64(i + 1), ObjektID: int64(30000000 + i), Transferable: true,
		}
	}
	return cosmo.ObjektCollections{
		CollectionCount: 1,
		Collections: []cosmo.OwnedCollection{{
			Collection: cosmo.ObjektCollection{
				CollectionNo: "101Z", Season: "Binary02", Class: "First", Member: "ChaeWon", ArtistName: "tripleS",
			},
			Count:   n,
			Objekts: objs,
		}},
	}
}

// pinnedSample returns n single-copy collections, the first `pinned` of which
// are pinned; each gets a distinct collectionNo.
func pinnedSample(n, pinned int) cosmo.ObjektCollections {
	cols := cosmo.ObjektCollections{CollectionCount: n}
	for i := range n {
		c := cosmo.ObjektCollection{
			CollectionNo: fmt.Sprintf("%03dZ", 101+i), Season: "Binary02",
			Class: "First", Member: "ChaeWon", ArtistName: "tripleS",
		}
		if i < pinned {
			c.FavoritedAt = "2026-03-15T05:13:59.000Z"
			cols.FavoritedCount++
		}
		cols.Collections = append(cols.Collections, cosmo.OwnedCollection{
			Collection: c, Count: 1,
			Objekts: []cosmo.OwnedObjekt{{ObjektNo: int64(i), ObjektID: int64(40000000 + i), Transferable: true}},
		})
	}
	return cols
}

// --- objekt send flow ---

// testWallet builds a throwaway signing wallet (a fixed 32-byte key) so the
// send flow's wallet != nil guard is satisfied in unit tests.
func testWallet(t *testing.T) *wallet.Wallet {
	t.Helper()
	w, err := wallet.FromKeyBytes(bytes.Repeat([]byte{0x11}, 32))
	if err != nil {
		t.Fatalf("test wallet: %v", err)
	}
	return w
}

// loadedModelW is loadedModel with a provisioned wallet.
func loadedModelW(t *testing.T, cols cosmo.ObjektCollections, w *wallet.Wallet) Model {
	t.Helper()
	m := New(nil, "tripleS", w)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	updated, _ = m.Update(loadedMsg{cols: cols})
	return updated.(Model)
}

// queuedIDs is the send queue's copies, in queue order.
func queuedIDs(m Model) []int64 {
	var out []int64
	for _, it := range m.sendItems {
		out = append(out, it.objektID)
	}
	return out
}

// confirmModel drives a started send flow (already in sendPicker) through the
// recipient search to the confirmation screen.
func confirmModel(t *testing.T, m Model) Model {
	t.Helper()
	if m.send != sendPicker {
		t.Fatalf("send phase = %d, want sendPicker before picking a recipient", m.send)
	}
	m, _ = press(t, m, "bob")
	m, _ = press(t, m, "enter") // run the search (the command is not executed here)
	updated, _ := m.Update(usersearch.SearchedMsg{
		Query:   "bob",
		Results: []cosmo.UserSearchResult{{Nickname: "bob", Address: "0x9CCCFc221a3CcE9090E60950EF07517bFCCc840d"}},
	})
	m = updated.(Model)
	m, _ = press(t, m, "enter") // descend picks the recipient
	if m.send != sendConfirm {
		t.Fatalf("send phase = %d, want sendConfirm", m.send)
	}
	return m
}

// sendingModel takes a model with a copy under the cursor all the way into the
// sending phase, with the first transfer nominally in flight (its command is
// returned rather than run).
func sendingModel(t *testing.T, m Model) Model {
	t.Helper()
	m, _ = press(t, m, "t")
	m = confirmModel(t, m)
	m, _ = press(t, m, "y")
	if m.send != sendSending {
		t.Fatalf("send phase = %d, want sendSending", m.send)
	}
	return m
}

// TestStartSendNoWallet: without a provisioned wallet, t on a transferable copy
// declines and explains, rather than opening the picker.
func TestStartSendNoWallet(t *testing.T) {
	m := loadedModel(t, sampleCols()) // wallet nil
	m, _ = press(t, m, "l")           // cursor -> cell 1 (transferable)
	m, _ = press(t, m, "t")
	if m.send != sendOff {
		t.Fatalf("send phase = %d, want sendOff", m.send)
	}
	if !strings.Contains(m.notice, "provision") {
		t.Fatalf("notice = %q, want a provisioning hint", m.notice)
	}
}

// TestStartSendLockedCopy: t on a locked (non-transferable) copy declines.
func TestStartSendLockedCopy(t *testing.T) {
	m := loadedModelW(t, sampleCols(), testWallet(t)) // cursor starts on the locked cell 0
	m, _ = press(t, m, "t")
	if m.send != sendOff {
		t.Fatalf("send phase = %d, want sendOff (locked copy)", m.send)
	}
	if !strings.Contains(m.notice, "locked") {
		t.Fatalf("notice = %q, want a locked hint", m.notice)
	}
}

// TestSendIsModalOverGrid: t captures the copy under the cursor, and the flow it
// opens is modal over the grid — esc backs out of the send, not out of a filter
// or the page.
func TestSendIsModalOverGrid(t *testing.T) {
	m := loadedModelW(t, sampleCols(), testWallet(t))
	m, _ = press(t, m, "l") // cursor -> cell 1 (transferable)
	m, _ = press(t, m, "t")
	if m.send != sendPicker {
		t.Fatalf("send phase = %d, want sendPicker", m.send)
	}
	if got := queuedIDs(m); !reflect.DeepEqual(got, []int64{21999999}) {
		t.Fatalf("queue = %v, want just the cursor's copy", got)
	}
	m, _ = press(t, m, "esc")
	if m.send != sendOff {
		t.Fatalf("send phase = %d, want sendOff after esc", m.send)
	}
}

// TestGridSendHint: the grid status line advertises t whenever a wallet is
// provisioned — including over a locked copy, so the hint does not flicker as
// the cursor moves.
func TestGridSendHint(t *testing.T) {
	m := loadedModelW(t, sampleCols(), testWallet(t))
	if got := m.statusLine(); !strings.Contains(got, "t: send") {
		t.Fatalf("status = %q, want a send hint on the locked copy too", got)
	}
	m, _ = press(t, m, "l") // transferable copy
	if got := m.statusLine(); !strings.Contains(got, "t: send") {
		t.Fatalf("status = %q, want a send hint on a transferable copy", got)
	}

	// No wallet, no hint, whatever the copy.
	nw := loadedModel(t, sampleCols())
	nw, _ = press(t, nw, "l")
	if got := nw.statusLine(); strings.Contains(got, "t: send") {
		t.Fatalf("status = %q, want no send hint without a wallet", got)
	}
}

// TestSendPickerToConfirm drives the flow from t through picking a recipient to
// the confirmation step, then cancels.
func TestSendPickerToConfirm(t *testing.T) {
	m := loadedModelW(t, sampleCols(), testWallet(t))
	m, _ = press(t, m, "l") // transferable cell 1
	m, _ = press(t, m, "t") // start send
	if m.send != sendPicker {
		t.Fatalf("send phase = %d, want sendPicker", m.send)
	}
	if !m.AcceptsText() {
		t.Fatal("recipient input should capture text")
	}

	m, _ = press(t, m, "bob")   // type a nickname
	m, _ = press(t, m, "enter") // run search (cmd not executed here)
	updated, _ := m.Update(usersearch.SearchedMsg{
		Query:   "bob",
		Results: []cosmo.UserSearchResult{{Nickname: "bob", Address: "0x9CCCFc221a3CcE9090E60950EF07517bFCCc840d"}},
	})
	m = updated.(Model)
	m, _ = press(t, m, "enter") // descend selects the recipient
	if m.send != sendConfirm {
		t.Fatalf("send phase = %d, want sendConfirm", m.send)
	}
	if m.sendTo.Nickname != "bob" {
		t.Fatalf("recipient = %q, want bob", m.sendTo.Nickname)
	}
	if body := m.sendBody(); !strings.Contains(body, "irreversible") {
		t.Fatalf("confirm body missing the irreversible warning: %q", body)
	}

	m, _ = press(t, m, "n") // decline
	if m.send != sendOff {
		t.Fatalf("send phase = %d, want sendOff after cancel", m.send)
	}
}

// TestSendPickerCancel: esc from the recipient input backs out of the flow.
func TestSendPickerCancel(t *testing.T) {
	m := loadedModelW(t, sampleCols(), testWallet(t))
	m, _ = press(t, m, "l")
	m, _ = press(t, m, "t")
	m, _ = press(t, m, "esc")
	if m.send != sendOff {
		t.Fatalf("send phase = %d, want sendOff after esc", m.send)
	}
}

// TestSendResultAndDismiss: a broadcast result moves to sendDone and the hash
// renders; dismissing a success returns to the grid and reloads the collection
// (so the sent copy disappears).
func TestSendResultAndDismiss(t *testing.T) {
	m := loadedModelW(t, sampleCols(), testWallet(t))
	m, _ = press(t, m, "l")
	m = sendingModel(t, m)
	updated, _ := m.Update(sendResultMsg{hash: "0xabc123", confirmed: true})
	m = updated.(Model)
	if m.send != sendDone || m.sendItems[0].hash != "0xabc123" {
		t.Fatalf("phase=%d hash=%q, want sendDone + 0xabc123", m.send, m.sendItems[0].hash)
	}
	if !strings.Contains(m.render(), "0xabc123") {
		t.Fatal("result view should show the tx hash")
	}
	m, cmd := press(t, m, "h") // the back motion dismisses
	if m.send != sendOff {
		t.Fatalf("send phase = %d, want sendOff after dismiss", m.send)
	}
	if m.loaded {
		t.Fatal("dismissing a success should trigger a reload (loaded=false)")
	}
	if cmd == nil {
		t.Fatal("dismissing a success should return the reload command")
	}
}

// TestSendDoneOnlyLeavesOnBack: the receipt list carries transaction hashes, so
// only the uniform back motion dismisses it (matching the live page's finished
// download panel). Any-key dismissal was never true anyway — the shell takes
// q/H/L/A/? before the page sees them.
func TestSendDoneOnlyLeavesOnBack(t *testing.T) {
	m := loadedModelW(t, sampleCols(), testWallet(t))
	m, _ = press(t, m, "l")
	m = sendingModel(t, m)
	updated, _ := m.Update(sendResultMsg{hash: "0xabc123", confirmed: true})
	m = updated.(Model)

	for _, k := range []string{"x", "enter", "esc", " ", "y"} {
		m, _ = press(t, m, k)
		if m.send != sendDone {
			t.Fatalf("%q dismissed the receipt list; only the back motion should", k)
		}
	}
	if !strings.Contains(m.render(), "h: back") {
		t.Fatalf("the status line should name the one exit:\n%s", m.render())
	}
	m, _ = press(t, m, "h")
	if m.send != sendOff {
		t.Fatalf("send phase = %d, want sendOff after h", m.send)
	}
}

// TestPostSendReloadRetries: a post-send reload that still lists the sent copy
// (Cosmo not yet indexed) keeps loading and schedules another reload; once the
// copy is gone it finalizes onto the grid.
func TestPostSendReloadRetries(t *testing.T) {
	m := loadedModelW(t, sampleCols(), testWallet(t))
	sentID := sampleCols().Collections[0].Objekts[1].ObjektID // 21999999, a transferable copy

	// Still present -> retry (loading, non-nil command, data not yet applied).
	updated, cmd := m.Update(sentReloadMsg{cols: sampleCols(), objektIDs: []int64{sentID}, attempt: 1})
	m = updated.(Model)
	if m.loaded {
		t.Fatal("copy still present should keep the page loading")
	}
	if cmd == nil {
		t.Fatal("copy still present should schedule another reload")
	}

	// Gone -> finalize: apply data, land on the grid, drop the copy's cell.
	updated, cmd = m.Update(sentReloadMsg{cols: withoutObjekt(sampleCols(), sentID), objektIDs: []int64{sentID}, attempt: 2})
	m = updated.(Model)
	if !m.loaded || cmd != nil {
		t.Fatalf("copy gone should finalize (loaded=%v, cmd!=nil=%v)", m.loaded, cmd != nil)
	}
	if len(m.cells) != 2 {
		t.Fatalf("cells = %d, want 2 after the sent copy is gone", len(m.cells))
	}
	if containsObjekt(cosmo.ObjektCollections{Collections: m.collections}, sentID) {
		t.Fatal("sent copy should no longer be in the collection")
	}
}

// TestPostSendReloadGivesUp: after the last attempt the reload finalizes even if
// the copy still lingers, rather than retrying forever.
func TestPostSendReloadGivesUp(t *testing.T) {
	m := loadedModelW(t, sampleCols(), testWallet(t))
	sentID := sampleCols().Collections[0].Objekts[1].ObjektID
	updated, cmd := m.Update(sentReloadMsg{cols: sampleCols(), objektIDs: []int64{sentID}, attempt: postSendReloadTries})
	m = updated.(Model)
	if !m.loaded || cmd != nil {
		t.Fatalf("final attempt should finalize (loaded=%v, cmd!=nil=%v)", m.loaded, cmd != nil)
	}
}

// withoutObjekt returns cols with the copy of the given objektId removed.
func withoutObjekt(cols cosmo.ObjektCollections, objektID int64) cosmo.ObjektCollections {
	out := cols
	out.Collections = nil
	for _, c := range cols.Collections {
		kept := c.Objekts[:0:0]
		for _, o := range c.Objekts {
			if o.ObjektID != objektID {
				kept = append(kept, o)
			}
		}
		c.Objekts = kept
		c.Count = len(kept)
		out.Collections = append(out.Collections, c)
	}
	return out
}

// TestSendErrorResult: a failed broadcast surfaces the error, and dismissing it
// stays on the copy detail for a retry (no reload).
func TestSendErrorResult(t *testing.T) {
	m := loadedModelW(t, sampleCols(), testWallet(t))
	m, _ = press(t, m, "l")
	m = sendingModel(t, m)
	updated, _ := m.Update(sendResultMsg{err: errors.New("nonce too low")})
	m = updated.(Model)
	if m.send != sendDone || m.sendItems[0].err == nil {
		t.Fatalf("phase=%d err=%v, want sendDone + error", m.send, m.sendItems[0].err)
	}
	if !strings.Contains(m.render(), "nonce too low") {
		t.Fatal("result view should show the error")
	}
	m, cmd := press(t, m, "h")
	if m.send != sendOff {
		t.Fatalf("after error dismiss: phase=%d, want sendOff", m.send)
	}
	if !m.loaded || cmd != nil {
		t.Fatal("error dismiss should not reload the collection")
	}
}

// --- bulk send (the grid selection) ---

// selectAll marks every owned copy, the way space would over the whole grid.
func selectAll(m *Model) {
	for ci := range m.collections {
		for oi := range m.collections[ci].Objekts {
			m.selected[m.collections[ci].Objekts[oi].ObjektID] = struct{}{}
		}
	}
}

// TestBulkSendQueuesSelection: t on the grid queues every marked copy in grid
// order, whatever order the marks were made in.
func TestBulkSendQueuesSelection(t *testing.T) {
	m := loadedModelW(t, sampleCols(), testWallet(t))
	m, _ = press(t, m, "G")     // last cell (22000570)
	m, _ = press(t, m, "space") // mark it first
	m, _ = press(t, m, "h")     // back to cell 1 (21999999)
	m, _ = press(t, m, "space")
	m, _ = press(t, m, "t")
	if m.send != sendPicker {
		t.Fatalf("send phase = %d, want sendPicker", m.send)
	}
	if got := queuedIDs(m); !reflect.DeepEqual(got, []int64{21999999, 22000570}) {
		t.Fatalf("queue = %v, want both marked copies in grid order", got)
	}
}

// TestBulkSendLockedCopyRefused: a locked copy anywhere in the selection stops
// the whole send with a notice, rather than sending the transferable rest.
func TestBulkSendLockedCopyRefused(t *testing.T) {
	m := loadedModelW(t, sampleCols(), testWallet(t))
	m, _ = press(t, m, "space") // cell 0 is the locked copy
	m, _ = press(t, m, "l")
	m, _ = press(t, m, "space") // plus a transferable one
	m, _ = press(t, m, "t")
	if m.send != sendOff {
		t.Fatalf("send phase = %d, want sendOff (locked copy in the selection)", m.send)
	}
	if !strings.Contains(m.notice, "locked") {
		t.Fatalf("notice = %q, want a locked hint", m.notice)
	}
	if len(m.sendItems) != 0 {
		t.Fatalf("queue = %v, want nothing queued", queuedIDs(m))
	}
	// The marks are untouched, so the user can just deselect the locked one.
	if len(m.selected) != 2 {
		t.Fatalf("selected = %d, want the marks left alone", len(m.selected))
	}
}

// TestGridSendNoSelectionUsesCursor: with nothing marked, t on the grid is still
// the single-copy send of the copy under the cursor.
func TestGridSendNoSelectionUsesCursor(t *testing.T) {
	m := loadedModelW(t, sampleCols(), testWallet(t))
	m, _ = press(t, m, "l") // cell 1, transferable
	m, _ = press(t, m, "t")
	if got := queuedIDs(m); !reflect.DeepEqual(got, []int64{21999999}) {
		t.Fatalf("queue = %v, want just the cursor's copy", got)
	}
}

// TestConfirmScreenLayout: the warning sits above the recipient, the recipient
// is listed once however many copies are queued, and every copy is listed.
func TestConfirmScreenLayout(t *testing.T) {
	m := loadedModelW(t, sampleCols(), testWallet(t))
	m, _ = press(t, m, "l")
	m, _ = press(t, m, "space")
	m, _ = press(t, m, "l")
	m, _ = press(t, m, "space")
	m, _ = press(t, m, "t")
	m = confirmModel(t, m)

	body := plain(m.sendBody())
	warn, rcpt := strings.Index(body, "irreversible"), strings.Index(body, "Recipient:")
	if warn < 0 || rcpt < 0 || warn > rcpt {
		t.Fatalf("want the warning above the recipient, got warn=%d rcpt=%d in:\n%s", warn, rcpt, body)
	}
	if n := strings.Count(body, "Recipient:"); n != 1 {
		t.Fatalf("recipient listed %d times, want once:\n%s", n, body)
	}
	if !strings.Contains(body, "2 objekts") {
		t.Fatalf("want the queue count in:\n%s", body)
	}
	for _, want := range []string{"#8001", "#570"} {
		if !strings.Contains(body, want) {
			t.Fatalf("confirm body missing %q:\n%s", want, body)
		}
	}
}

// TestConfirmScrolls: a queue longer than the screen scrolls under a pinned
// header, so the warning and the recipient stay put.
func TestConfirmScrolls(t *testing.T) {
	m := New(nil, "tripleS", testWallet(t))
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 12})
	m = updated.(Model)
	updated, _ = m.Update(loadedMsg{cols: pinnedSample(12, 0)})
	m = updated.(Model)
	selectAll(&m)
	m, _ = press(t, m, "t")
	m = confirmModel(t, m)

	head, rows := m.sendScreen()
	capacity := m.sendCapacity(len(head))
	if len(rows) <= capacity {
		t.Fatalf("test wants an overflowing queue: %d rows, capacity %d", len(rows), capacity)
	}
	first := plain(m.sendBody())
	if !strings.Contains(first, "101Z") { // the first copy's collectionNo
		t.Fatalf("want the top of the queue before scrolling:\n%s", first)
	}
	if !strings.Contains(m.sendStatus(), "j/k: scroll") {
		t.Fatalf("status %q should advertise scrolling on an overflowing queue", m.sendStatus())
	}

	m, _ = press(t, m, "j")
	if m.sendTop != 1 {
		t.Fatalf("sendTop = %d after j, want 1", m.sendTop)
	}
	after := plain(m.sendBody())
	if strings.Contains(after, "101Z") {
		t.Fatalf("first row should have scrolled off:\n%s", after)
	}
	if !strings.Contains(after, "irreversible") || !strings.Contains(after, "Recipient:") {
		t.Fatalf("header should stay pinned while scrolling:\n%s", after)
	}
	m, _ = press(t, m, "G")
	if want := len(rows) - capacity; m.sendTop != want {
		t.Fatalf("sendTop = %d after G, want %d", m.sendTop, want)
	}
	m, _ = press(t, m, "k")
	m, _ = press(t, m, "g")
	if m.sendTop != 0 {
		t.Fatalf("sendTop = %d after g, want 0", m.sendTop)
	}
}

// TestSendRunsOneAtATime: the queue only ever has one transfer on the wire — the
// next command is issued by the result of the previous one, and a stale result
// cannot advance it.
func TestSendRunsOneAtATime(t *testing.T) {
	m := loadedModelW(t, sampleCols(), testWallet(t))
	m, _ = press(t, m, "l")
	m, _ = press(t, m, "space")
	m, _ = press(t, m, "l")
	m, _ = press(t, m, "space")
	m, _ = press(t, m, "t")
	m = confirmModel(t, m)

	m, cmd := press(t, m, "y")
	if m.send != sendSending || cmd == nil {
		t.Fatalf("confirming should start the first transfer (phase=%d cmd!=nil=%v)", m.send, cmd != nil)
	}
	if m.sendIdx != 0 {
		t.Fatalf("sendIdx = %d, want the first transfer in flight", m.sendIdx)
	}

	// A result for a transfer that is not the one in flight is ignored.
	updated, cmd := m.Update(sendResultMsg{idx: 1, hash: "0xlater"})
	if stale := updated.(Model); cmd != nil || stale.sendIdx != 0 || stale.sendItems[1].done {
		t.Fatal("a result for a queued-but-not-running transfer should be ignored")
	}

	updated, cmd = m.Update(sendResultMsg{idx: 0, hash: "0xone", confirmed: true})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("finishing a transfer should start the next one")
	}
	if m.sendIdx != 1 || !m.sendItems[0].done || m.send != sendSending {
		t.Fatalf("after the first result: idx=%d done=%v phase=%d", m.sendIdx, m.sendItems[0].done, m.send)
	}
	// The same result again is stale now: the queue has moved on.
	updated, cmd = m.Update(sendResultMsg{idx: 0, hash: "0xone", confirmed: true})
	if dup := updated.(Model); cmd != nil || dup.sendIdx != 1 {
		t.Fatal("a duplicated result should not advance the queue twice")
	}

	updated, cmd = m.Update(sendResultMsg{idx: 1, hash: "0xtwo", confirmed: true})
	m = updated.(Model)
	if m.send != sendDone || cmd != nil {
		t.Fatalf("the last result should finish the queue (phase=%d cmd!=nil=%v)", m.send, cmd != nil)
	}
	if body := plain(m.sendBody()); !strings.Contains(body, "2 sent") {
		t.Fatalf("done screen should summarize the queue:\n%s", body)
	}
}

// TestSendContinuesAfterFailure: one failed transfer does not stop the rest, and
// the done screen counts both outcomes.
func TestSendContinuesAfterFailure(t *testing.T) {
	m := loadedModelW(t, sampleCols(), testWallet(t))
	m, _ = press(t, m, "l")
	m, _ = press(t, m, "space")
	m, _ = press(t, m, "l")
	m, _ = press(t, m, "space")
	m, _ = press(t, m, "t")
	m = confirmModel(t, m)
	m, _ = press(t, m, "y")

	updated, cmd := m.Update(sendResultMsg{idx: 0, err: errors.New("nonce too low")})
	m = updated.(Model)
	if cmd == nil || m.send != sendSending {
		t.Fatalf("a failure should not stop the queue (cmd!=nil=%v phase=%d)", cmd != nil, m.send)
	}
	updated, _ = m.Update(sendResultMsg{idx: 1, hash: "0xtwo", confirmed: true})
	m = updated.(Model)

	body := plain(m.sendBody())
	if !strings.Contains(body, "1 sent") || !strings.Contains(body, "1 failed") {
		t.Fatalf("done screen should count both outcomes:\n%s", body)
	}
	if !strings.Contains(body, "nonce too low") {
		t.Fatalf("done screen should show the failure reason:\n%s", body)
	}
}

// TestSendingLocksTheView: while the queue is running, no key the page owns
// leaves it — only the list scrolls. (tab and q belong to the shell, which
// routes them before the page ever sees them.)
func TestSendingLocksTheView(t *testing.T) {
	m := loadedModelW(t, sampleCols(), testWallet(t))
	m, _ = press(t, m, "l")
	m = sendingModel(t, m)
	for _, k := range []string{"esc", "enter", "n", "x", "y"} {
		var cmd tea.Cmd
		m, cmd = press(t, m, k)
		if m.send != sendSending {
			t.Fatalf("%q left the sending view (phase=%d)", k, m.send)
		}
		if cmd != nil {
			t.Fatalf("%q should do nothing while sending", k)
		}
	}
}

// TestDoneDismissReloadsBroadcastCopies: dismissing waits every copy that made it
// onto the wire out of the collection; a queue that sent nothing does not reload.
func TestDoneDismissReloadsBroadcastCopies(t *testing.T) {
	m := loadedModelW(t, sampleCols(), testWallet(t))
	m, _ = press(t, m, "l")
	m, _ = press(t, m, "space")
	m, _ = press(t, m, "l")
	m, _ = press(t, m, "space")
	m, _ = press(t, m, "t")
	m = confirmModel(t, m)
	m, _ = press(t, m, "y")
	updated, _ := m.Update(sendResultMsg{idx: 0, err: errors.New("gas station down")})
	m = updated.(Model)
	updated, _ = m.Update(sendResultMsg{idx: 1, hash: "0xtwo", confirmed: true})
	m = updated.(Model)

	if got := m.broadcastIDs(); !reflect.DeepEqual(got, []int64{22000570}) {
		t.Fatalf("broadcast ids = %v, want only the copy that was sent", got)
	}
	m, cmd := press(t, m, "h")
	if m.send != sendOff || m.loaded || cmd == nil {
		t.Fatalf("dismiss should reload the grid (phase=%d loaded=%v cmd!=nil=%v)",
			m.send, m.loaded, cmd != nil)
	}

	// A queue where nothing was broadcast stays put instead.
	f := loadedModelW(t, sampleCols(), testWallet(t))
	f, _ = press(t, f, "l")
	f = sendingModel(t, f)
	updated, _ = f.Update(sendResultMsg{idx: 0, err: errors.New("sign: bad key")})
	f = updated.(Model)
	f, cmd = press(t, f, "x")
	if !f.loaded || cmd != nil {
		t.Fatal("a queue that sent nothing should not reload")
	}
}

// TestSelectionSurvivesSendCancel: backing out at either send step leaves the
// marked set intact, so the same selection can be sent to someone else.
func TestSelectionSurvivesSendCancel(t *testing.T) {
	m := loadedModelW(t, sampleCols(), testWallet(t))
	m, _ = press(t, m, "l")
	m, _ = press(t, m, "space")
	m, _ = press(t, m, "l")
	m, _ = press(t, m, "space")

	m, _ = press(t, m, "t")
	m, _ = press(t, m, "esc") // out of the recipient box
	if m.send != sendOff || len(m.selected) != 2 {
		t.Fatalf("after picker cancel: phase=%d selected=%d, want sendOff + 2 marks", m.send, len(m.selected))
	}

	m, _ = press(t, m, "t")
	m = confirmModel(t, m)
	m, _ = press(t, m, "n") // out of the confirmation
	if m.send != sendOff || len(m.selected) != 2 {
		t.Fatalf("after confirm cancel: phase=%d selected=%d, want sendOff + 2 marks", m.send, len(m.selected))
	}
	// And the marks still drive a fresh send.
	m, _ = press(t, m, "t")
	if got := queuedIDs(m); len(got) != 2 {
		t.Fatalf("queue = %v, want both marks again", got)
	}
}

// --- contract resolution and transfer verification ---

// fakeChain stands in for the Cosmo client's chain reads: ownerOf per contract
// and a scripted sequence of receipts.
type fakeChain struct {
	owners   map[string]string // lower-cased token address -> holder
	errs     map[string]error  // lower-cased token address -> eth_call error
	receipts []cosmo.AbstractReceipt
	calls    int
}

func (f *fakeChain) AbstractOwnerOf(_ context.Context, token string, _ int64) (string, error) {
	k := strings.ToLower(token)
	if err, ok := f.errs[k]; ok {
		return "", err
	}
	return f.owners[k], nil
}

func (f *fakeChain) AbstractTxState(context.Context, string) (cosmo.AbstractReceipt, error) {
	if f.calls >= len(f.receipts) {
		return cosmo.AbstractReceipt{}, nil // TxPending
	}
	r := f.receipts[f.calls]
	f.calls++
	return r, nil
}

// topic renders a value as a 32-byte log topic word.
func topic(v string) string {
	v = strings.TrimPrefix(v, "0x")
	return "0x" + strings.Repeat("0", 64-len(v)) + v
}

// transferLog is the ERC-721 Transfer event a real move emits.
func transferLog(token, from, to string, id int64) cosmo.AbstractLog {
	return cosmo.AbstractLog{Address: token, Topics: []string{
		cosmo.TransferTopic, topic(from), topic(to), topic(fmt.Sprintf("%x", id)),
	}}
}

// feeLog is the gas payment every transaction on this chain logs, transfer or
// not — the reason an empty Transfer search is meaningful.
func feeLog() cosmo.AbstractLog {
	return cosmo.AbstractLog{
		Address: "0x000000000000000000000000000000000000800A",
		Topics:  []string{cosmo.TransferTopic, topic(holder), topic("0x1111")},
	}
}

// fastPolls shrinks the polling loop so a test does not wait on real timings.
func fastPolls(t *testing.T) {
	t.Helper()
	interval, tries := chain.PollInterval, chain.PollTries
	chain.PollInterval, chain.PollTries = 0, 3
	t.Cleanup(func() { chain.PollInterval, chain.PollTries = interval, tries })
}

const (
	holder    = "0x3a6E4EFfEb030f950870C98834eb40f5BE9c7f2A"
	recipient = "0x9CCCFc221a3CcE9090E60950EF07517bFCCc840d"
	badToken  = "0xA4B37bE40F7b231Ee9574c4b16b7DDb7EAcDC99B" // reports nothing
	mainToken = "0x99Bb83AE9bb0C0A6be865CaCF67760947f91Cb70"
)

// TestResolveTokenTrustsTheChain: the contract Cosmo reports is used when the
// chain agrees it holds the copy, and skipped when it does not — a Unit objekt
// was reported against a contract that answers no ERC-721 call at all, while the
// token really sat on the main objekt contract.
func TestResolveTokenTrustsTheChain(t *testing.T) {
	own := cosmo.ObjektOwnership{Owner: holder, TokenAddress: badToken}
	cases := []struct {
		name       string
		chain      *fakeChain
		candidates []string
		want       string
		wantErr    bool
	}{
		{
			name:  "reported contract holds it",
			chain: &fakeChain{owners: map[string]string{strings.ToLower(badToken): holder}},
			want:  badToken,
		},
		{
			name: "reported answers nothing, a candidate holds it",
			chain: &fakeChain{owners: map[string]string{
				strings.ToLower(badToken):  "", // no ownerOf on this address
				strings.ToLower(mainToken): holder,
			}},
			candidates: []string{badToken, mainToken},
			want:       mainToken,
		},
		{
			name: "reported reverts, a candidate holds it",
			chain: &fakeChain{
				owners: map[string]string{strings.ToLower(mainToken): holder},
				errs:   map[string]error{strings.ToLower(badToken): errors.New("execution reverted")},
			},
			candidates: []string{mainToken},
			want:       mainToken,
		},
		{
			name: "somebody else holds it everywhere",
			chain: &fakeChain{owners: map[string]string{
				strings.ToLower(badToken):  recipient,
				strings.ToLower(mainToken): recipient,
			}},
			candidates: []string{mainToken},
			wantErr:    true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveToken(context.Background(), tc.chain, own, 23881050, tc.candidates)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got contract %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveToken: %v", err)
			}
			if !strings.EqualFold(got, tc.want) {
				t.Fatalf("contract = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestAwaitTransferReadsTheReceipt: the receipt's Transfer log is the proof a
// copy moved. Both real failure modes hang off this — a transaction that mined
// while moving nothing, and a completed transfer whose ownerOf still named the
// sender seconds later because reads lag the receipt.
func TestAwaitTransferReadsTheReceipt(t *testing.T) {
	fastPolls(t)
	tr := transfer{hash: "0xabc", token: mainToken}
	const id = 23881050

	t.Run("mined with the transfer logged", func(t *testing.T) {
		c := &fakeChain{
			// ownerOf still names the sender: the state read lags, and must not
			// be allowed to contradict the receipt.
			owners: map[string]string{strings.ToLower(mainToken): holder},
			receipts: []cosmo.AbstractReceipt{{Status: cosmo.TxSuccess, Logs: []cosmo.AbstractLog{
				feeLog(), transferLog(mainToken, holder, recipient, id), feeLog(),
			}}},
		}
		confirmed, err := awaitTransfer(context.Background(), c, tr, id, recipient)
		if err != nil || !confirmed {
			t.Fatalf("confirmed=%v err=%v, want a confirmed move", confirmed, err)
		}
	})

	t.Run("mined with no transfer logged", func(t *testing.T) {
		// The Unit-objekt no-op: the gas was paid, nothing else happened.
		c := &fakeChain{receipts: []cosmo.AbstractReceipt{
			{Status: cosmo.TxSuccess, Logs: []cosmo.AbstractLog{feeLog()}},
		}}
		confirmed, err := awaitTransfer(context.Background(), c, tr, id, recipient)
		if confirmed || err == nil {
			t.Fatalf("confirmed=%v err=%v, want a failure", confirmed, err)
		}
		if !strings.Contains(err.Error(), "recorded no transfer") {
			t.Fatalf("err = %v, want it to say nothing moved", err)
		}
	})

	t.Run("transfer logged for another copy", func(t *testing.T) {
		c := &fakeChain{receipts: []cosmo.AbstractReceipt{
			{Status: cosmo.TxSuccess, Logs: []cosmo.AbstractLog{
				feeLog(), transferLog(mainToken, holder, recipient, id+1),
			}},
		}}
		if _, err := awaitTransfer(context.Background(), c, tr, id, recipient); err == nil {
			t.Fatal("a Transfer for a different tokenId should not confirm this one")
		}
	})

	t.Run("reverted", func(t *testing.T) {
		c := &fakeChain{receipts: []cosmo.AbstractReceipt{{Status: cosmo.TxReverted}}}
		if _, err := awaitTransfer(context.Background(), c, tr, id, recipient); err == nil ||
			!strings.Contains(err.Error(), "reverted") {
			t.Fatalf("err = %v, want a revert", err)
		}
	})

	t.Run("never mined", func(t *testing.T) {
		// Broadcast but no receipt: unconfirmed, not a failure.
		confirmed, err := awaitTransfer(context.Background(), &fakeChain{}, tr, id, recipient)
		if confirmed || err != nil {
			t.Fatalf("confirmed=%v err=%v, want unconfirmed without an error", confirmed, err)
		}
	})

	t.Run("receipt without logs is inconclusive", func(t *testing.T) {
		// A proxy that strips logs must not turn every send into a failure.
		c := &fakeChain{receipts: []cosmo.AbstractReceipt{{Status: cosmo.TxSuccess}}}
		confirmed, err := awaitTransfer(context.Background(), c, tr, id, recipient)
		if confirmed || err != nil {
			t.Fatalf("confirmed=%v err=%v, want unconfirmed without an error", confirmed, err)
		}
	})
}

// TestTokenContractsAreDistinct: the fallback contracts are the distinct ones
// the loaded collection reports, in load order.
func TestTokenContractsAreDistinct(t *testing.T) {
	cols := sampleCols()
	cols.Collections[0].Objekts[1].TokenAddress = mainToken // same as [0][0]
	cols.Collections[1].Objekts[0].TokenAddress = badToken
	cols.Collections[0].Objekts[0].TokenAddress = strings.ToLower(mainToken)
	m := loadedModel(t, cols)
	got := m.tokenContracts()
	if len(got) != 2 || !strings.EqualFold(got[0], mainToken) || !strings.EqualFold(got[1], badToken) {
		t.Fatalf("contracts = %v, want the two distinct ones in load order", got)
	}
}

// TestSummaryCountsUnconfirmedApart: a copy that was broadcast but never seen to
// move is not counted as sent — calling it sent is the claim that was wrong.
func TestSummaryCountsUnconfirmedApart(t *testing.T) {
	m := loadedModelW(t, sampleCols(), testWallet(t))
	m.sendItems = []sendItem{
		{label: "a", hash: "0x1", confirmed: true, done: true},
		{label: "b", hash: "0x2", done: true},
		{label: "c", err: errors.New("nope"), done: true},
	}
	m.send, m.sendIdx = sendDone, 3
	got := plain(m.sendSummary())
	for _, want := range []string{"1 sent", "1 unconfirmed", "1 failed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary %q missing %q", got, want)
		}
	}
}

// TestReloadFlagsCopiesThatStayed: when the post-send reload runs out of retries
// with a sent copy still listed, the status line says so rather than leaving the
// user to spot it.
func TestReloadFlagsCopiesThatStayed(t *testing.T) {
	m := loadedModelW(t, sampleCols(), testWallet(t))
	sentID := sampleCols().Collections[0].Objekts[1].ObjektID
	updated, _ := m.Update(sentReloadMsg{cols: sampleCols(), objektIDs: []int64{sentID}, attempt: postSendReloadTries})
	m = updated.(Model)
	if !strings.Contains(m.notice, "still in your collection") {
		t.Fatalf("notice = %q, want a warning that the copy stayed", m.notice)
	}
	// A reload that finds them all gone says nothing.
	clean := loadedModelW(t, sampleCols(), testWallet(t))
	updated, _ = clean.Update(sentReloadMsg{
		cols: withoutObjekt(sampleCols(), sentID), objektIDs: []int64{sentID}, attempt: postSendReloadTries})
	if got := updated.(Model).notice; got != "" {
		t.Fatalf("notice = %q, want none when the copy left", got)
	}
}

// --- cancelling a running queue ---

// startQueue drives a two-copy selection all the way into the sending phase.
func startQueue(t *testing.T, n int) Model {
	t.Helper()
	m := loadedModelW(t, pinnedSample(n, 0), testWallet(t))
	selectAll(&m)
	m, _ = press(t, m, "t")
	m = confirmModel(t, m)
	m, _ = press(t, m, "y")
	if m.send != sendSending || len(m.sendItems) != n {
		t.Fatalf("phase=%d queue=%d, want a running queue of %d", m.send, len(m.sendItems), n)
	}
	return m
}

// TestCancelStopsAfterTheTransferInFlight: c drops the transfers that have not
// started, but the one already on the wire is seen through to its result — it
// cannot be recalled, so pretending otherwise would be a lie.
func TestCancelStopsAfterTheTransferInFlight(t *testing.T) {
	m := startQueue(t, 3)
	m, cmd := press(t, m, "c")
	if !m.sendCancel {
		t.Fatal("c should arm the cancel")
	}
	if cmd != nil || m.send != sendSending {
		t.Fatalf("c must not itself stop anything (cmd!=nil=%v phase=%d)", cmd != nil, m.send)
	}

	// The transfer that was already running still reports, and is recorded.
	updated, cmd := m.Update(sendResultMsg{idx: 0, hash: "0xone", confirmed: true})
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("a cancelled queue should not start the next transfer")
	}
	if m.send != sendDone {
		t.Fatalf("phase = %d, want sendDone once the in-flight transfer returned", m.send)
	}
	if !m.sendItems[0].done || m.sendItems[0].hash != "0xone" {
		t.Fatal("the in-flight transfer's result should still be recorded")
	}
	if m.sendItems[1].done || m.sendItems[2].done {
		t.Fatal("the untouched transfers should not be marked done")
	}

	body := plain(m.sendBody())
	for _, want := range []string{"Send cancelled", "1 sent", "2 not sent", "not sent"} {
		if !strings.Contains(body, want) {
			t.Fatalf("done screen missing %q:\n%s", want, body)
		}
	}
	// Nothing was broadcast for the skipped copies, so the reload only waits on
	// the one that actually left.
	if got := m.broadcastIDs(); len(got) != 1 {
		t.Fatalf("broadcast ids = %v, want only the sent copy", got)
	}
}

// TestCancelIsOneWay: c commits — pressing it again does not put the dropped
// transfers back.
func TestCancelIsOneWay(t *testing.T) {
	m := startQueue(t, 3)
	m, _ = press(t, m, "c")
	m, _ = press(t, m, "c")
	if !m.sendCancel {
		t.Fatal("a second c must not undo the cancel")
	}
	updated, cmd := m.Update(sendResultMsg{idx: 0, hash: "0xone", confirmed: true})
	m = updated.(Model)
	if cmd != nil || m.send != sendDone {
		t.Fatalf("the queue should still stop (cmd!=nil=%v phase=%d)", cmd != nil, m.send)
	}
}

// TestCancelPendingIsVisible: an armed cancel says so while the queue is still
// working, so the wait is not mistaken for the key having done nothing.
func TestCancelPendingIsVisible(t *testing.T) {
	m := startQueue(t, 3)
	if got := m.sendStatus(); !strings.Contains(got, "c: cancel remaining") {
		t.Fatalf("status %q should offer the cancel", got)
	}
	m, _ = press(t, m, "c")
	if got := plain(m.sendBody()); !strings.Contains(got, "cancelling after this transfer") {
		t.Fatalf("body should show the pending cancel:\n%s", got)
	}
	got := m.sendStatus()
	if !strings.Contains(got, "cancelling") {
		t.Fatalf("status %q should say the cancel is pending", got)
	}
	if strings.Contains(got, "c: ") {
		t.Fatalf("status %q should not offer c once cancelled", got)
	}
}

// TestCancelNeedsSomethingToDrop: on the last transfer of a queue — and so on
// any single-copy send — there is nothing left to cancel, and c is inert.
func TestCancelNeedsSomethingToDrop(t *testing.T) {
	one := startQueue(t, 1)
	one, _ = press(t, one, "c")
	if one.sendCancel {
		t.Fatal("a single-copy send has nothing to cancel")
	}
	if got := one.sendStatus(); strings.Contains(got, "c: cancel remaining") {
		t.Fatalf("status %q should not offer a cancel", got)
	}

	// Same once the queue reaches its final transfer.
	last := startQueue(t, 2)
	updated, _ := last.Update(sendResultMsg{idx: 0, hash: "0xone", confirmed: true})
	last = updated.(Model)
	last, _ = press(t, last, "c")
	if last.sendCancel || last.send != sendSending {
		t.Fatalf("cancel = %v phase = %d, want an inert c on the last transfer", last.sendCancel, last.send)
	}
}

// TestCancelClearedByANewSend: the flag does not leak into the next send.
func TestCancelClearedByANewSend(t *testing.T) {
	m := startQueue(t, 2)
	m, _ = press(t, m, "c")
	updated, _ := m.Update(sendResultMsg{idx: 0, hash: "0xone", confirmed: true})
	m = updated.(Model)
	m, _ = press(t, m, "h") // dismiss
	m, _ = press(t, m, "t") // start another send
	if m.sendCancel {
		t.Fatal("a new send should start uncancelled")
	}
}

// TestPasteIntoRecipientBox checks a pasted nickname lands in the recipient
// picker, and only while the picker is the thing on screen.
func TestPasteIntoRecipientBox(t *testing.T) {
	m := loadedModelW(t, sampleCols(), testWallet(t))

	// Before the flow starts there is no recipient box to paste into.
	updated, _ := m.Update(tea.PasteMsg{Content: "bob"})
	m = updated.(Model)

	m, _ = press(t, m, "l")
	m, _ = press(t, m, "enter")
	m, _ = press(t, m, "t") // sendPicker
	updated, _ = m.Update(tea.PasteMsg{Content: "bob"})
	m = updated.(Model)

	if body := m.sendBody(); !strings.Contains(body, "bob") {
		t.Fatalf("pasted nickname missing from the recipient box: %q", body)
	}
	// A pasted nickname searches like a typed one.
	m, cmd := press(t, m, "enter")
	if cmd == nil {
		t.Fatal("enter after a paste should run the recipient search")
	}
}

// TestPasteIntoFilterBox checks a paste into the grid filter applies live, the
// way a typed character does.
func TestPasteIntoFilterBox(t *testing.T) {
	m := loadedModel(t, sampleCols())
	all := len(m.cells)

	m, _ = press(t, m, "/")
	if !m.filterMode {
		t.Fatal("/ should open the filter box")
	}
	updated, _ := m.Update(tea.PasteMsg{Content: "YooYeon"})
	m = updated.(Model)

	if m.filter != "YooYeon" {
		t.Fatalf("filter = %q, want the pasted text applied", m.filter)
	}
	if len(m.cells) != 1 || m.cells[0].ci != 1 {
		t.Fatalf("cells = %d of %d, want only the pasted member's copy", len(m.cells), all)
	}
}
