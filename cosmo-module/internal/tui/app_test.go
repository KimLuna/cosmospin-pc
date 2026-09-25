package tui

import (
	"errors"
	"strings"
	"testing"

	"codeberg.org/djvu/cosmo-tui/internal/config"
	"codeberg.org/djvu/cosmo-tui/internal/tui/clip"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestShellNavigation drives the shell without a live client to confirm the
// tab bar, tab switching, and group cycling behave and View never panics.
func TestShellNavigation(t *testing.T) {
	a := New(nil, []string{"tripleS", "artms", "idntt"}, config.DefaultOptions(), nil)
	// give it a size so pages render in place
	m, _ := a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	a = m.(*App)

	view := a.render()
	for _, want := range []string{"cosmo-tui", "tripleS", "Profile", "News"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	if a.active != 0 {
		t.Fatalf("expected info active, got %d", a.active)
	}

	// tab → next page
	m, _ = a.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	a = m.(*App)
	if a.active != 1 {
		t.Fatalf("expected news active after tab, got %d", a.active)
	}

	// A → next group
	m, _ = a.Update(tea.KeyPressMsg{Code: 'A', Text: "A"})
	a = m.(*App)
	if a.group != "artms" {
		t.Fatalf("expected artms after group switch, got %q", a.group)
	}
}

// TestTabsSelection checks that opts.Tabs picks which tabs are built and their
// order, opening on the first, and that the full default list constructs every
// tab (guarding the New factory against drift from config.Tabs).
func TestTabsSelection(t *testing.T) {
	opts := config.DefaultOptions()
	opts.Tabs = []string{"talk", "room", "live", "news"}
	a := New(nil, []string{"tripleS"}, opts, nil)
	m, _ := a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	a = m.(*App)

	if len(a.pages) != 4 {
		t.Fatalf("expected 4 tabs, got %d", len(a.pages))
	}
	for i, want := range []string{"Talk", "Room", "Live", "News"} {
		if got := a.pages[i].Title(); got != want {
			t.Fatalf("tab %d = %q, want %q", i, got, want)
		}
	}
	if a.active != 0 {
		t.Fatalf("expected the first configured tab (Talk) active, got %d", a.active)
	}
	if view := a.render(); !strings.Contains(view, "Talk") || strings.Contains(view, "Profile") {
		t.Fatalf("view should show only the configured tabs:\n%s", view)
	}

	// The full default list must construct every tab; a config.Tabs id with no
	// factory entry would call a nil closure and panic here.
	all := config.DefaultOptions()
	all.Tabs = config.Tabs
	full := New(nil, []string{"tripleS"}, all, nil)
	if len(full.pages) != len(config.Tabs) {
		t.Fatalf("expected %d tabs, got %d", len(config.Tabs), len(full.pages))
	}
}

// TestFrameHeight checks that the rendered frame is exactly the height of the
// terminal in both help modes. Full help is as tall as its longest column, and
// that column's length depends on how many bindings are enabled, so a fixed
// reservation under-counts it and pushes the frame past the last row.
func TestFrameHeight(t *testing.T) {
	const height = 24
	for _, groups := range [][]string{{"tripleS", "artms", "idntt"}, {"tripleS"}} {
		a := New(nil, groups, config.DefaultOptions(), nil)
		m, _ := a.Update(tea.WindowSizeMsg{Width: 100, Height: height})
		a = m.(*App)
		if got := lipgloss.Height(a.render()); got != height {
			t.Fatalf("%d artist(s), short help: frame %d rows, want %d", len(groups), got, height)
		}
		m, _ = a.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
		a = m.(*App)
		if got := lipgloss.Height(a.render()); got != height {
			t.Fatalf("%d artist(s), full help: frame %d rows, want %d", len(groups), got, height)
		}
	}
}

// TestGroupCarousel checks that the configured artist list drives the A key:
// a two-artist carousel wraps in order, and a single artist neither moves nor
// advertises the binding.
func TestGroupCarousel(t *testing.T) {
	a := New(nil, []string{"idntt", "artms"}, config.DefaultOptions(), nil)
	if a.group != "idntt" {
		t.Fatalf("expected startup on idntt, got %q", a.group)
	}
	for _, want := range []string{"artms", "idntt", "artms"} {
		m, _ := a.Update(tea.KeyPressMsg{Code: 'A', Text: "A"})
		a = m.(*App)
		if a.group != want {
			t.Fatalf("expected %q after group switch, got %q", want, a.group)
		}
	}

	solo := New(nil, []string{"tripleS"}, config.DefaultOptions(), nil)
	m, _ := solo.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	solo = m.(*App)
	m, _ = solo.Update(tea.KeyPressMsg{Code: 'A', Text: "A"})
	solo = m.(*App)
	if solo.group != "tripleS" {
		t.Fatalf("single-artist carousel moved to %q", solo.group)
	}
	if solo.keys.Group.Enabled() {
		t.Fatal("switch-artist binding should be hidden with one artist")
	}
	if strings.Contains(solo.render(), "switch artist") {
		t.Fatal("footer still advertises switch artist")
	}
}

// TestPasteReachesOnlyTheActivePage checks pasted text goes to the tab on
// screen and nowhere else. Unlike a load result, a paste is aimed at whatever
// box has the cursor, so broadcasting it would drop a copy into every page
// holding a focused text box — including one left open on another tab.
func TestPasteReachesOnlyTheActivePage(t *testing.T) {
	opts := config.DefaultOptions()
	opts.Tabs = []string{"profile", "objekt"}
	a := New(nil, []string{"tripleS"}, opts, nil)
	m, _ := a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	a = m.(*App)

	// Open the profile tab's nickname search and paste into it.
	m, _ = a.Update(tea.KeyPressMsg{Code: 's', Text: "s"})
	a = m.(*App)
	m, _ = a.Update(tea.PasteMsg{Content: "cosmo"})
	a = m.(*App)
	if view := ansi.Strip(a.render()); !strings.Contains(view, "cosmo") {
		t.Fatalf("pasted text missing from the search box:\n%s", view)
	}

	// Now paste with another tab active: the search box, still open behind it,
	// must not take a second copy.
	m, _ = a.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	a = m.(*App)
	m, _ = a.Update(tea.PasteMsg{Content: "cosmo"})
	a = m.(*App)
	m, _ = a.Update(clip.Msg{Text: "cosmo"})
	a = m.(*App)
	m, _ = a.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	a = m.(*App)
	if view := ansi.Strip(a.render()); strings.Contains(view, "cosmocosmo") {
		t.Fatalf("a paste aimed at another tab landed in the search box:\n%s", view)
	}
}

// TestClipboardChord checks the shell owns ctrl+v: it reads the clipboard only
// while a box is capturing text, and the chord never reaches the page as a
// keystroke (a bubbles filter box binds ctrl+v itself, to a paste whose answer
// nothing here could deliver back to it).
func TestClipboardChord(t *testing.T) {
	opts := config.DefaultOptions()
	opts.Tabs = []string{"profile"}
	a := New(nil, []string{"tripleS"}, opts, nil)
	m, _ := a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	a = m.(*App)

	// Nothing is capturing text: ctrl+v is the page's business, not a read.
	if _, cmd := a.Update(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl}); cmd != nil {
		t.Fatal("ctrl+v outside a text box should not read the clipboard")
	}

	m, _ = a.Update(tea.KeyPressMsg{Code: 's', Text: "s"}) // open the search box
	a = m.(*App)
	m, cmd := a.Update(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})
	a = m.(*App)
	if cmd == nil {
		t.Fatal("ctrl+v in a text box should read the clipboard")
	}
	if _, ok := cmd().(clip.Msg); !ok {
		t.Fatal("the dispatched command should answer with a clip.Msg")
	}
	if view := ansi.Strip(a.render()); strings.Contains(view, "v") && strings.Contains(view, "search: v") {
		t.Fatalf("the chord was typed into the box as a character:\n%s", view)
	}
}

// TestClipboardErrorInFooter checks a failed read is reported by the shell,
// since a page's box has no way to know why the host could not be read. The
// notice replaces the footer's first line rather than adding one: the body
// height pages were given assumes the footer's row count (see pageSize).
func TestClipboardErrorInFooter(t *testing.T) {
	opts := config.DefaultOptions()
	opts.Tabs = []string{"profile"}
	a := New(nil, []string{"tripleS"}, opts, nil)
	m, _ := a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	a = m.(*App)
	before := lipgloss.Height(a.render())

	m, _ = a.Update(clip.Msg{Err: errors.New("no clipboard tool found")})
	a = m.(*App)
	view := ansi.Strip(a.render())
	if !strings.Contains(view, "no clipboard tool found") {
		t.Fatalf("clipboard failure not reported:\n%s", view)
	}
	if got := lipgloss.Height(a.render()); got != before {
		t.Fatalf("frame height = %d with the notice, want %d", got, before)
	}

	// The next keystroke retires it.
	m, _ = a.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	a = m.(*App)
	if view := ansi.Strip(a.render()); strings.Contains(view, "no clipboard tool found") {
		t.Fatalf("the notice outlived the next keystroke:\n%s", view)
	}
}
