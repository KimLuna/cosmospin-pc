// Package listfilter routes the bubbles list filter's async result back to the
// list that asked for it. It is a leaf package (imports nothing from
// internal/tui) so pages can use it without an import cycle.
//
// Filtering is not synchronous: each keystroke in a list's filter makes
// list.Update return a command that computes the matches off to the side and
// reports them as a list.FilterMatchesMsg. A page whose Update only forwards
// tea.KeyPressMsg never delivers that message, and the failure is silent and
// counter-intuitive — the list's filteredItems stays empty, so VisibleItems
// reports nothing, and on enter the list decides the filter matched nothing and
// quietly clears it. The filter looks like it does nothing at all.
//
// Pages therefore have to handle the message explicitly:
//
//	case list.FilterMatchesMsg:
//		return m, listfilter.Route(msg, &m.sidebar, &m.entries)
//
// Paste is the same routing for text pasted into a filter box (the shell turns
// both a ctrl+v and the terminal's own paste into a tea.PasteMsg; see
// internal/tui/clip):
//
//	case tea.PasteMsg:
//		return m, listfilter.Paste(msg, &m.sidebar, &m.entries)
package listfilter

import (
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

// Route delivers msg to whichever of lists is currently filtering, and returns
// the resulting command. Only the focused pane can be filtering, so at most one
// list takes the message; passing every list a page owns is the simplest way to
// stay correct as focus moves.
func Route(msg list.FilterMatchesMsg, lists ...*list.Model) tea.Cmd {
	return deliver(msg, lists)
}

// Paste drops pasted text into whichever of lists is currently filtering, the
// same way Route delivers a match set. A filtering list hands every message it
// is given to its filter box and re-runs the filter whenever the query changes,
// so the insertion and the re-filter both come out of the one Update — the
// command that comes back is the match computation, which the page returns to
// the shell like any other and gets back as the list.FilterMatchesMsg Route
// handles.
func Paste(msg tea.PasteMsg, lists ...*list.Model) tea.Cmd {
	return deliver(msg, lists)
}

func deliver(msg tea.Msg, lists []*list.Model) tea.Cmd {
	var cmds []tea.Cmd
	for _, l := range lists {
		if l.FilterState() != list.Filtering {
			continue
		}
		var cmd tea.Cmd
		*l, cmd = l.Update(msg)
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

// Resync completes a programmatic item swap on a list that may be filtered:
// call it with the command SetItems or SetItem returned. That command
// recomputes the filter's matches (SetItems drops the current ones, blanking a
// filtered list until they are rebuilt), and it cannot go through the normal
// async path: its FilterMatchesMsg comes back as a broadcast that Route only
// delivers while a filter is being typed — an *applied* filter never receives
// it — and the message doesn't name its list, so routing it to every filtered
// list could cross-deliver between two filtered panes. Running the command
// inline and handing the message straight back to the owning list closes both
// holes; the filter is an in-memory fuzzy match, so this is cheap.
func Resync(l *list.Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if msg := cmd(); msg != nil {
		// The FilterMatchesMsg branch of list.Update returns no follow-up cmd.
		*l, _ = l.Update(msg)
	}
}
