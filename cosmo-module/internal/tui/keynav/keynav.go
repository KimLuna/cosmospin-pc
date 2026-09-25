// Package keynav defines the app's uniform navigation keys.
//
// Every tab is a stack of panes — sidebar → list → detail — and moves through
// it the same way:
//
//	j/k, ↑/↓        move within the focused pane
//	l, →, enter     descend one level (open the highlighted row)
//	h, ←            ascend one level (back out)
//
// Because the motions never change between tabs, status lines do not advertise
// them; hints are reserved for what a tab alone can do (translate, download,
// sort). A motion with nowhere to go must be swallowed rather than passed to the
// underlying bubbles list, whose defaults would otherwise page on h/l.
//
// esc is deliberately not an ascend key: it belongs to the bubbles list, whose
// native binding clears an applied filter. Intercepting it here left filters
// permanently stuck, since nothing else clears one. It is safe to hand esc to
// the lists because every one of them is built with DisableQuitKeybindings —
// without that, esc is a quit key in bubbles' default keymap.
//
// Text-entry modes are the deliberate exception: while composing a talk reply or
// typing a profile search, enter submits and esc leaves. Those modes match esc
// themselves, ahead of these motions, and their hints stay.
package keynav

import tea "charm.land/bubbletea/v2"

// Descend reports whether msg means "go one level deeper": open the highlighted
// row, or move focus into the pane on the right.
func Descend(msg tea.KeyPressMsg) bool {
	switch msg.String() {
	case "l", "right", "enter":
		return true
	}
	return false
}

// Ascend reports whether msg means "go one level back": leave a detail view, or
// move focus to the pane on the left. esc is not included; see the package doc.
func Ascend(msg tea.KeyPressMsg) bool {
	switch msg.String() {
	case "h", "left":
		return true
	}
	return false
}
