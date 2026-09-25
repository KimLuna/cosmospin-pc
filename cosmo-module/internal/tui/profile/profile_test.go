package profile

import (
	"strings"
	"testing"

	"codeberg.org/djvu/cosmo-tui/internal/cosmo"
	"codeberg.org/djvu/cosmo-tui/internal/tui/usersearch"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// sized builds a sized page (no client needed: tests feed messages directly and
// never run the returned network commands).
func sized(t *testing.T) Model {
	t.Helper()
	m := New(nil, "tripleS")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return updated.(Model)
}

func key(m Model, k string) Model {
	var msg tea.KeyPressMsg
	switch k {
	case "enter":
		msg = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		msg = tea.KeyPressMsg{Code: tea.KeyEsc}
	default:
		msg = tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
	}
	updated, _ := m.Update(msg)
	return updated.(Model)
}

func typeText(m Model, s string) Model {
	for _, r := range s {
		m = key(m, string(r))
	}
	return m
}

// TestSearchCapturesText verifies "s" opens the nickname box and only then does
// the page capture plain-letter keys for the shell.
func TestSearchCapturesText(t *testing.T) {
	m := sized(t)
	if m.AcceptsText() {
		t.Fatal("should not capture text on the default profile")
	}
	m = key(m, "s")
	if m.mode != modeSearching || !m.AcceptsText() {
		t.Fatalf("expected searching mode capturing text, got mode=%d accepts=%v", m.mode, m.AcceptsText())
	}
	m = key(m, "esc")
	if m.mode != modeSelf || m.AcceptsText() {
		t.Fatal("esc should return to own profile and stop capturing text")
	}
}

// TestSearchToResults runs a query and confirms results render and the page
// stops capturing text once the search fires.
func TestSearchToResults(t *testing.T) {
	m := sized(t)
	m = key(m, "s")
	m = typeText(m, "cosmo")
	m = key(m, "enter")
	if m.mode != modeSearching {
		t.Fatalf("expected searching mode, got %d", m.mode)
	}
	if m.AcceptsText() {
		t.Fatal("results list should not capture text")
	}
	// Deliver the search response the fired command would have produced.
	updated, _ := m.Update(usersearch.SearchedMsg{Query: "cosmo", Results: []cosmo.UserSearchResult{
		{ID: 2851, Nickname: "cosmo"},
		{ID: 42, Nickname: "cosmoo"},
	}})
	m = updated.(Model)
	view := m.render()
	if !strings.Contains(view, "cosmo") || !strings.Contains(view, "cosmoo") {
		t.Fatalf("expected result nicknames in view:\n%s", view)
	}
}

// TestSearchNoResults confirms the not-found message for an empty result set.
func TestSearchNoResults(t *testing.T) {
	m := sized(t)
	m = key(m, "s")
	m = typeText(m, "zzznope")
	m = key(m, "enter")
	updated, _ := m.Update(usersearch.SearchedMsg{Query: "zzznope", Results: nil})
	m = updated.(Model)
	if got := m.render(); !strings.Contains(got, "no users found") {
		t.Fatalf("expected not-found message, got:\n%s", got)
	}
}

// TestViewUserProfile confirms selecting a result loads that user and the
// rendered card marks it as another user and omits the private COMO total.
func TestViewUserProfile(t *testing.T) {
	m := sized(t)
	m = key(m, "s")
	m = typeText(m, "cosmo")
	m = key(m, "enter")
	m, _ = mUpdate(m, usersearch.SearchedMsg{Query: "cosmo", Results: []cosmo.UserSearchResult{{ID: 2851, Nickname: "cosmo"}}})
	m = key(m, "enter") // open highlighted result
	if m.mode != modeUser {
		t.Fatalf("expected user mode, got %d", m.mode)
	}
	m, _ = mUpdate(m, userLoadedMsg{profile: cosmo.Profile{
		ID: 2851, Nickname: "cosmo", FandomName: "WAV", TotalComo: 999,
		Self: false, HasComo: false,
		Visibility: cosmo.ProfileVisibility{Overview: true},
		Stats:      cosmo.DailyStats{ObjektCount: 4},
	}})
	view := m.render()
	if !strings.Contains(view, "cosmo") {
		t.Fatalf("expected the viewed user's nickname, got:\n%s", view)
	}
	if strings.Contains(view, "COMO") {
		t.Fatalf("another user's profile must not show COMO, got:\n%s", view)
	}
	// h returns to the results list.
	m = key(m, "h")
	if m.mode != modeSearching {
		t.Fatalf("h from user should return to the results list, got %d", m.mode)
	}
	if m.AcceptsText() {
		t.Fatal("returning to the results list must not capture text")
	}
}

// TestForeignSearchResultIgnored checks that a search response belonging to
// another page is not taken. The shell broadcasts to every tab, and the objekt
// tab's recipient picker answers with the same message type; the widget's own
// stale guard is only the query string, so a recipient lookup repeating a
// nickname searched here would otherwise replace the results this page holds.
func TestForeignSearchResultIgnored(t *testing.T) {
	m := sized(t)
	m = key(m, "s")
	m = typeText(m, "cosmo")
	m = key(m, "enter")
	m, _ = mUpdate(m, usersearch.SearchedMsg{Query: "cosmo", Results: []cosmo.UserSearchResult{
		{ID: 2851, Nickname: "cosmo"},
		{ID: 42, Nickname: "cosmoo"},
	}})
	m = key(m, "enter") // open the highlighted result
	if m.mode != modeUser {
		t.Fatalf("expected user mode, got %d", m.mode)
	}

	// The objekt tab looks up the same nickname to send to.
	m, _ = mUpdate(m, usersearch.SearchedMsg{Query: "cosmo", Results: []cosmo.UserSearchResult{
		{ID: 7, Nickname: "cosmo"},
	}})

	// Backing out must show the results this page searched for, untouched.
	m = key(m, "h")
	if m.mode != modeSearching {
		t.Fatalf("h from user should return to the results list, got %d", m.mode)
	}
	if view := m.render(); !strings.Contains(view, "cosmoo") {
		t.Fatalf("the recipient lookup replaced this page's results:\n%s", view)
	}
}

// TestForeignTextNormalized checks that another user's nickname and bio — the
// only arbitrary text this page shows — are normalized and re-flowed before they
// reach the frame. A separator the terminal breaks on adds a row lipgloss never
// counted, and an over-long bio would otherwise be clipped at the pane edge
// rather than wrapped.
func TestForeignTextNormalized(t *testing.T) {
	const (
		width  = 100
		height = 30
	)
	m := New(nil, "tripleS")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = updated.(Model)

	m, _ = mUpdate(m, loadedMsg{profile: cosmo.Profile{
		Nickname:      "a b",                                // LINE SEPARATOR
		StatusMessage: strings.Repeat("long bio text ", 20), // wider than the pane
		Self:          true,
	}})

	view := m.render()
	if strings.ContainsAny(view, "  ") {
		t.Fatal("a line separator survived into the rendered profile")
	}
	if got := lipgloss.Height(view); got != height {
		t.Fatalf("frame is %d rows, want %d", got, height)
	}
	for i, ln := range strings.Split(view, "\n") {
		if w := lipgloss.Width(ln); w > width {
			t.Fatalf("row %d is %d cols, want <= %d", i, w, width)
		}
	}
	if !strings.Contains(view, "long bio text") {
		t.Fatalf("expected the bio on screen:\n%s", view)
	}
}

func mUpdate(m Model, msg tea.Msg) (Model, tea.Cmd) {
	updated, cmd := m.Update(msg)
	return updated.(Model), cmd
}
