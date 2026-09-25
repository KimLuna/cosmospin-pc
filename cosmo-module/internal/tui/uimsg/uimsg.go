// Package uimsg holds tea.Msg types shared between the app shell and page
// packages. It is a leaf package (imports nothing from internal/tui) so pages
// can consume these messages without an import cycle.
package uimsg

// GroupChanged is broadcast by the shell when the active group changes. Pages
// that show group-specific data should reload on receipt.
type GroupChanged struct {
	Group string
}

// Activated is sent to a page (never broadcast) when it becomes the active
// tab, so a page can defer loading until it is actually looked at.
type Activated struct{}
