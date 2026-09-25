package listfilter

import (
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

type item string

func (i item) FilterValue() string { return string(i) }

func newList() list.Model {
	l := list.New([]list.Item{item("chaewon"), item("yooyeon")},
		list.NewDefaultDelegate(), 40, 10)
	return l
}

// startFiltering opens a list's filter box, the way "/" does on a page.
func startFiltering(t *testing.T, l list.Model) list.Model {
	t.Helper()
	l, _ = l.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	if l.FilterState() != list.Filtering {
		t.Fatalf("filter state = %v, want Filtering", l.FilterState())
	}
	return l
}

// TestPasteGoesToTheFilteringList checks pasted text reaches the one list whose
// filter box is open and no other — a page hands Paste every list it owns, and
// only the focused pane can be filtering.
func TestPasteGoesToTheFilteringList(t *testing.T) {
	filtering, idle := startFiltering(t, newList()), newList()

	cmd := Paste(tea.PasteMsg{Content: "chae"}, &filtering, &idle)

	if got := filtering.FilterInput.Value(); got != "chae" {
		t.Fatalf("filtering list's box = %q, want the pasted text", got)
	}
	if got := idle.FilterInput.Value(); got != "" {
		t.Fatalf("idle list's box = %q, want it untouched", got)
	}
	if cmd == nil {
		t.Fatal("a changed filter should ask for the matches")
	}
}

// TestPasteWithNoFilterOpen checks a paste that arrives with every box closed
// (the read outlived the filter) is dropped rather than banked.
func TestPasteWithNoFilterOpen(t *testing.T) {
	idle := newList()
	if cmd := Paste(tea.PasteMsg{Content: "chae"}, &idle); cmd != nil {
		t.Fatal("a paste with no filter open should do nothing")
	}
	if got := idle.FilterInput.Value(); got != "" {
		t.Fatalf("idle list's box = %q, want it untouched", got)
	}
}
