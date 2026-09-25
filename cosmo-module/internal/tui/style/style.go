// Package style holds shared TUI rendering helpers used by both the app shell
// and the individual page packages. It is a leaf package (imports nothing from
// internal/tui) so pages can use it without creating an import cycle.
package style

import (
	"codeberg.org/djvu/cosmo-tui/internal/tui/textfmt"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
)

// Wrap prepares API text for a pane: it normalizes the text for terminal
// display (see textfmt) and re-flows it to w columns. Every detail view runs its
// body through this — the normalization is what keeps a stray separator or
// width-mismatched character from adding a row the frame never budgeted for, and
// the reflow is what keeps long prose on screen instead of clipped at the pane
// edge. A width below 1 (an unsized pane) skips the reflow but still normalizes.
func Wrap(s string, w int) string {
	s = textfmt.Normalize(s)
	if w < 1 {
		return s
	}
	return lipgloss.NewStyle().Width(w).Render(s)
}

// NewList builds a page list with the setup every list here wants: a title, no
// help or status bar (the shell renders those), no quit keybindings, a
// focus-tracking delegate, and a filter box styled to match the plain
// terminal-palette look of the profile page's search box.
func NewList(title string, focused, showDesc bool) list.Model {
	l := list.New(nil, ListDelegate(focused, showDesc), 0, 0)
	l.Title = title
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	l.DisableQuitKeybindings()

	l.FilterInput.Prompt = "filter: "
	PlainInput(&l.FilterInput)
	return l
}

// FitInput sizes a text input to whatever it is currently showing: its value,
// or its placeholder while empty. Every input needs a width from somewhere —
// bubbles copies the placeholder into a buffer of Width+1 runes, so an input
// left at the zero width renders only the first character of it — but bubbles
// also pads the box out to that width. An input sharing its line with other
// text therefore has to be re-fitted after every keystroke, or the padding
// shoves whatever follows it across the row. Inputs with a line to themselves
// can just take the width of their frame, where the padding falls off the end.
func FitInput(in *textinput.Model) {
	if w := lipgloss.Width(in.Value()); w > 0 {
		// The visible window has to hold the cursor sitting past the last
		// character, or it scrolls and eats the first one.
		in.SetWidth(w + 1)
		return
	}
	// While empty the cursor sits over the placeholder's first character, so
	// the box needs room for the rest of it and nothing more.
	if w := lipgloss.Width(in.Placeholder) - 1; w > 0 {
		in.SetWidth(w)
	}
}

// PlainInput strips the palette picks bubbles bakes into a text input, leaving
// the prompt, the blurred text and the cursor in the terminal's own colors.
// Without it an input reads as a second theme sitting inside the app's chrome:
// the defaults paint the prompt and cursor a fixed gray and dim the value
// whenever the input loses focus.
func PlainInput(in *textinput.Model) {
	s := in.Styles()
	s.Focused.Prompt = lipgloss.NewStyle()
	s.Blurred.Prompt = lipgloss.NewStyle()
	s.Blurred.Text = lipgloss.NewStyle()
	s.Cursor.Color = nil
	in.SetStyles(s)
}

// PaneBorder styles a pane's right-hand divider, bright magenta when the pane
// holds focus and dim gray when it does not.
func PaneBorder(focused bool) lipgloss.Style {
	c := lipgloss.Color("8")
	if focused {
		c = lipgloss.Color("13")
	}
	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, true, false, false).
		BorderForeground(c)
}

// ListDelegate builds a bubbles list delegate whose selected-row highlight
// tracks pane focus: bright magenta when the pane is focused, a muted gray when
// it is not, so only the pane the user is driving shows a loud cursor while the
// other still shows a quiet marker of where its cursor rests. showDesc keeps or
// hides the second (description) line per list.
func ListDelegate(focused, showDesc bool) list.DefaultDelegate {
	d := list.NewDefaultDelegate()
	d.ShowDescription = showDesc

	titleC, descC := lipgloss.Color("8"), lipgloss.Color("8")
	borderC := lipgloss.Color("8")
	if focused {
		titleC, descC = lipgloss.Color("13"), lipgloss.Color("5")
		borderC = lipgloss.Color("13")
	} else {
		titleC = lipgloss.Color("7")
	}
	d.Styles.SelectedTitle = d.Styles.SelectedTitle.
		BorderForeground(borderC).Foreground(titleC).Bold(focused)
	d.Styles.SelectedDesc = d.Styles.SelectedDesc.
		BorderForeground(borderC).Foreground(descC)
	return d
}
