package usersearch

import (
	"strings"
	"testing"

	"codeberg.org/djvu/cosmo-tui/internal/cosmo"

	tea "charm.land/bubbletea/v2"
)

func newWidget() Model {
	m := New(nil, Config{Title: "Search users", Prompt: "search: ", CancelHint: "esc: back"})
	m.Start()
	m.SetSize(60, 20)
	return m
}

func key(m Model, k string) (Model, Action, tea.Cmd) {
	var msg tea.KeyPressMsg
	switch k {
	case "enter":
		msg = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		msg = tea.KeyPressMsg{Code: tea.KeyEsc}
	case "up":
		msg = tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		msg = tea.KeyPressMsg{Code: tea.KeyDown}
	default:
		msg = tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
	}
	return m.Update(msg)
}

func typeText(m Model, s string) Model {
	for _, r := range s {
		m, _, _ = key(m, string(r))
	}
	return m
}

// TestInputPhaseCapturesAndCancels checks the widget captures text in the input
// phase and reports ActionCancel when the user escapes out of the box.
func TestInputPhaseCapturesAndCancels(t *testing.T) {
	m := newWidget()
	if !m.AcceptsText() {
		t.Fatal("input phase should capture text")
	}
	m, act, _ := key(m, "esc")
	if act != ActionCancel {
		t.Fatalf("esc should cancel, got action %d", act)
	}
}

// TestEnterRunsSearch checks enter with a non-empty query leaves the input
// phase and fires a command; an empty query is a no-op.
func TestEnterRunsSearch(t *testing.T) {
	m := newWidget()
	// Empty query: no search.
	m, _, cmd := key(m, "enter")
	if cmd != nil || !m.AcceptsText() {
		t.Fatal("empty query should not fire a search or leave the input phase")
	}
	m = typeText(m, "cosmo")
	m, _, cmd = key(m, "enter")
	if cmd == nil {
		t.Fatal("a non-empty query should fire a search command")
	}
	if m.AcceptsText() {
		t.Fatal("after enter the widget should stop capturing text (results phase)")
	}
}

// TestResultsRenderAndSelect checks the async result renders, the cursor moves,
// and descending reports ActionSelect with the highlighted user.
func TestResultsRenderAndSelect(t *testing.T) {
	m := newWidget()
	m = typeText(m, "cosmo")
	m, _, _ = key(m, "enter")
	m, _, _ = m.Update(SearchedMsg{Query: "cosmo", Results: []cosmo.UserSearchResult{
		{ID: 1, Nickname: "cosmo", Address: "0xaaa"},
		{ID: 2, Nickname: "cosmoo", Address: "0xbbb"},
	}})

	view := m.View()
	if !strings.Contains(view, "cosmo") || !strings.Contains(view, "cosmoo") {
		t.Fatalf("results not rendered:\n%s", view)
	}
	// Move down, then select.
	m, _, _ = key(m, "j")
	sel, ok := m.Selected()
	if !ok || sel.ID != 2 {
		t.Fatalf("Selected after one down = %+v (ok=%v), want ID 2", sel, ok)
	}
	m, act, _ := key(m, "l")
	if act != ActionSelect {
		t.Fatalf("descend should select, got action %d", act)
	}
}

// TestStaleResponseIgnored checks a response for a superseded query is dropped.
func TestStaleResponseIgnored(t *testing.T) {
	m := newWidget()
	m = typeText(m, "current")
	m, _, _ = key(m, "enter")
	m, _, _ = m.Update(SearchedMsg{Query: "stale", Results: []cosmo.UserSearchResult{{ID: 9, Nickname: "ghost"}}})
	if _, ok := m.Selected(); ok {
		t.Fatal("a stale response must not populate results")
	}
}

// TestAscendReturnsToInput checks ascend from the results goes back to the
// (capturing) input phase rather than cancelling.
func TestAscendReturnsToInput(t *testing.T) {
	m := newWidget()
	m = typeText(m, "cosmo")
	m, _, _ = key(m, "enter")
	m, act, _ := key(m, "h")
	if act != ActionNone {
		t.Fatalf("ascend from results should not signal the parent, got %d", act)
	}
	if !m.AcceptsText() {
		t.Fatal("ascend should return to the capturing input phase")
	}
}

// TestResultsWindowing checks the result list scrolls to keep the cursor
// visible within a short body.
func TestResultsWindowing(t *testing.T) {
	m := New(nil, Config{Title: "Search", Prompt: "search: "})
	m.Start()
	m.SetSize(60, 3+3) // capacity = height-3 = 3 rows visible

	res := make([]cosmo.UserSearchResult, 12)
	for i := range res {
		res[i] = cosmo.UserSearchResult{ID: i, Nickname: string(rune('a' + i))}
	}
	m = typeText(m, "x")
	m, _, _ = key(m, "enter")
	m, _, _ = m.Update(SearchedMsg{Query: "x", Results: res})

	if got := m.resultsCapacity(); got != 3 {
		t.Fatalf("capacity = %d, want 3", got)
	}
	// Walk to the last result; the rendered window must contain it and hold at
	// most `capacity` rows.
	for range res {
		m, _, _ = key(m, "down")
	}
	rendered := m.renderResults()
	lines := strings.Split(rendered, "\n")
	if len(lines) > 3 {
		t.Fatalf("window shows %d rows, want <= 3:\n%s", len(lines), rendered)
	}
	if !strings.Contains(rendered, "l") { // 12th nickname == 'a'+11 == 'l'
		t.Fatalf("window should include the highlighted last row:\n%s", rendered)
	}
}

// TestStatusHint checks the hint differs by phase.
func TestStatusHint(t *testing.T) {
	m := newWidget()
	if got := m.StatusHint(); !strings.Contains(got, "enter: search") || !strings.Contains(got, "esc: back") {
		t.Fatalf("input hint = %q", got)
	}
	m = typeText(m, "cosmo")
	m, _, _ = key(m, "enter")
	m, _, _ = m.Update(SearchedMsg{Query: "cosmo", Results: []cosmo.UserSearchResult{{ID: 1, Nickname: "cosmo"}, {ID: 2, Nickname: "cosmoo"}}})
	if got := m.StatusHint(); got != "1/2" {
		t.Fatalf("results hint = %q, want 1/2", got)
	}
}

// TestPasteIntoNicknameBox checks pasted text lands in the box and is
// searchable like typed text. The chord that reads the clipboard belongs to the
// shell (see internal/tui/clip); the widget only ever sees the text.
func TestPasteIntoNicknameBox(t *testing.T) {
	m := newWidget()
	// The box is one row: a multi-line paste has to arrive flattened.
	m, _, _ = m.Update(tea.PasteMsg{Content: "cosmo\nfan"})
	if m.input.Value() != "cosmo fan" {
		t.Fatalf("box = %q, want the pasted text on one line", m.input.Value())
	}
	m, _, cmd := key(m, "enter")
	if cmd == nil || m.AcceptsText() {
		t.Fatal("enter after a paste should run the search")
	}
}

// TestPasteOnlyInInputPhase checks pasted text arriving after the box is gone
// (the read outlives the search that was run) is dropped rather than banked.
func TestPasteOnlyInInputPhase(t *testing.T) {
	m := newWidget()
	m = typeText(m, "cosmo")
	m, _, _ = key(m, "enter") // results phase; the box is blurred
	m, _, _ = m.Update(tea.PasteMsg{Content: "late"})
	if m.input.Value() != "cosmo" {
		t.Fatalf("box = %q, want the query untouched", m.input.Value())
	}
}
