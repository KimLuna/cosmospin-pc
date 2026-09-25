// Package tui is the cosmo-tui app shell: a tab bar over a set of page models,
// with group switching, help, and quit. Each page is a self-contained
// tea.Model (plus a Title); the shell routes input to the active page and
// broadcasts window-size and group-change events.
package tui

import (
	"strings"

	"codeberg.org/djvu/cosmo-tui/internal/config"
	"codeberg.org/djvu/cosmo-tui/internal/cosmo"
	"codeberg.org/djvu/cosmo-tui/internal/tui/clip"
	"codeberg.org/djvu/cosmo-tui/internal/tui/gravity"
	"codeberg.org/djvu/cosmo-tui/internal/tui/info"
	"codeberg.org/djvu/cosmo-tui/internal/tui/live"
	"codeberg.org/djvu/cosmo-tui/internal/tui/news"
	"codeberg.org/djvu/cosmo-tui/internal/tui/objekt"
	"codeberg.org/djvu/cosmo-tui/internal/tui/profile"
	"codeberg.org/djvu/cosmo-tui/internal/tui/room"
	"codeberg.org/djvu/cosmo-tui/internal/tui/talk"
	"codeberg.org/djvu/cosmo-tui/internal/tui/uimsg"
	"codeberg.org/djvu/cosmo-tui/internal/wallet"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// page is any tab in the shell: a tea.Model with a display Title. It is
// satisfied structurally, so page packages need not import this one.
type page interface {
	tea.Model
	Title() string
}

// chromeHeight is the number of lines the header + tab bar + footer occupy,
// reserved from the window so pages get an accurate body size.
const chromeHeight = 4

type keyMap struct {
	Left    key.Binding
	Right   key.Binding
	Group   key.Binding
	Refresh key.Binding
	Help    key.Binding
	Quit    key.Binding
}

func defaultKeys() keyMap {
	return keyMap{
		// Only tab/shift+tab and H/L switch tabs, leaving the arrow keys free
		// for the active page's own navigation (lists, etc).
		Left:    key.NewBinding(key.WithKeys("shift+tab", "H"), key.WithHelp("⇤/H", "prev tab")),
		Right:   key.NewBinding(key.WithKeys("tab", "L"), key.WithHelp("⇥/L", "next tab")),
		Group:   key.NewBinding(key.WithKeys("A"), key.WithHelp("A", "switch artist")),
		Refresh: key.NewBinding(key.WithKeys("ctrl+l"), key.WithHelp("^L", "redraw")),
		Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Left, k.Right, k.Group, k.Refresh, k.Help, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Left, k.Right}, {k.Group, k.Refresh, k.Help, k.Quit}}
}

// App is the root model.
type App struct {
	client *cosmo.Client
	groups []string // the A-key carousel, in display order
	group  string   // the current group

	pages  []page
	active int

	width  int
	height int

	// clipErr is the last clipboard read that failed, shown in place of the
	// help footer until the next keystroke. It is reported here rather than by
	// the page that wanted the text because a read fails for reasons that have
	// nothing to do with the box — the host has no clipboard tool, or no
	// display to ask — and every text box in the app would otherwise need its
	// own way to say so.
	clipErr string

	help help.Model
	keys keyMap
}

// New builds the app shell for an authenticated client, configured by the
// user's runtime options. groups is the artist carousel (must be non-empty);
// the app opens on the first entry. w is the objekt-send signing wallet, or nil
// when none is provisioned (sending is then disabled).
func New(client *cosmo.Client, groups []string, opts config.Options, w *wallet.Wallet) *App {
	group := groups[0]
	keys := defaultKeys()
	// With a single artist there is nothing to switch to; hide the binding
	// from the help footer and let cycleGroup no-op.
	keys.Group.SetEnabled(len(groups) > 1)

	// Each tab is built on demand, so an omitted tab is never constructed and
	// its Init() (startup API call, plus talk's SSE stream) never runs. Keyed
	// by the same canonical ids config validates against (config.Tabs).
	newPage := map[string]func() page{
		"info": func() page { return info.New(client, group) },
		"news": func() page { return news.New(client, group) },
		"room": func() page { return room.New(client, group, opts.PostDownloadDir, opts.AutoTranslate) },
		"live": func() page {
			return live.New(client, group, opts.ReplayDownloadDir, opts.ReplayFragments)
		},
		"talk": func() page {
			return talk.New(client, group, opts.TalkDownloadDir, opts.Nicknames, opts.AutoTranslate)
		},
		"objekt":  func() page { return objekt.New(client, group, w) },
		"gravity": func() page { return gravity.New(client, group, w) },
		"profile": func() page { return profile.New(client, group) },
	}
	// Unset tabs means all of them in the default order; the first entry is
	// the startup tab (active stays 0).
	order := opts.Tabs
	if len(order) == 0 {
		order = config.Tabs
	}
	pages := make([]page, 0, len(order))
	for _, id := range order {
		pages = append(pages, newPage[id]())
	}

	return &App{
		client: client,
		groups: groups,
		group:  group,
		pages:  pages,
		help:   help.New(),
		keys:   keys,
	}
}

func (a *App) Init() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(a.pages))
	for _, p := range a.pages {
		cmds = append(cmds, p.Init())
	}
	return tea.Batch(cmds...)
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.help.SetWidth(msg.Width)
		return a, a.broadcast(a.pageSize())

	case tea.KeyPressMsg:
		// ctrl+c and tab navigation always work. But when the active page is
		// capturing text (a reply box or list filter), the plain-letter globals
		// (q/A/?) must reach the page as typed characters instead of firing.
		if msg.String() == "ctrl+c" {
			return a, tea.Quit
		}
		a.clipErr = "" // whatever the key does, it retires a clipboard complaint
		// ctrl+l forces a full repaint, useful when stray terminal output has
		// corrupted the screen. As a control key it never conflicts with text
		// input, so it fires even while a page is capturing text.
		if key.Matches(msg, a.keys.Refresh) {
			return a, tea.ClearScreen
		}
		// H and L are plain letters, so while typing only tab/shift+tab switch.
		typing := a.activeAcceptsText()
		if key.Matches(msg, a.keys.Right) && (msg.String() == "tab" || !typing) {
			a.active = (a.active + 1) % len(a.pages)
			return a, a.updateActive(uimsg.Activated{})
		}
		if key.Matches(msg, a.keys.Left) && (msg.String() == "shift+tab" || !typing) {
			a.active = (a.active - 1 + len(a.pages)) % len(a.pages)
			return a, a.updateActive(uimsg.Activated{})
		}
		// ctrl+v reads the clipboard for whichever box is capturing text. The
		// shell starts the read so no page has to know the chord, and so the key
		// never reaches a bubbles filter box: its text input binds ctrl+v to a
		// paste of its own, whose answer is a package-private message nothing
		// here could deliver back to it — an invisible clipboard read that
		// pastes nothing.
		if typing && clip.Key(msg) {
			return a, clip.Read()
		}
		if !typing {
			switch {
			case key.Matches(msg, a.keys.Quit):
				return a, tea.Quit
			case key.Matches(msg, a.keys.Help):
				a.help.ShowAll = !a.help.ShowAll
				return a, a.broadcast(a.pageSize())
			case key.Matches(msg, a.keys.Group):
				return a, a.cycleGroup()
			}
		}
		// Keys go only to the active page.
		return a, a.updateActive(msg)

	case tea.PasteMsg:
		// A paste the terminal performed itself (bracketed paste). It is text
		// aimed at whatever box has the cursor, so unlike the async messages
		// further down it goes to the active page alone — broadcast, every page
		// holding a focused box would take a copy.
		return a, a.updateActive(msg)

	case clip.Msg:
		// The answer to a ctrl+v. A failed read is the shell's to report; a
		// successful one carries on as an ordinary paste, so pages never have to
		// handle two kinds of pasted text (or know this package exists).
		if msg.Err != nil {
			a.clipErr = msg.Err.Error()
			return a, nil
		}
		return a, a.updateActive(tea.PasteMsg{Content: msg.Text})
	}

	// Everything else (async load results, SSE events, ticks) is broadcast to
	// every page, so a background load reaches the page that issued it even
	// when that page isn't the active tab. Messages are package-private types,
	// so only the owning page reacts.
	return a, a.broadcast(msg)
}

// activeAcceptsText reports whether the active page is currently capturing text
// (reply box, list filter), in which case the shell yields plain-letter keys.
func (a *App) activeAcceptsText() bool {
	if t, ok := a.pages[a.active].(interface{ AcceptsText() bool }); ok {
		return t.AcceptsText()
	}
	return false
}

// clipFooter puts a failed clipboard read where the help footer's first line
// is. It replaces that line rather than adding one: the body height pages were
// given is the window minus however many rows the help view occupies (see
// pageSize), so a footer that grew by a row would push the frame past the
// bottom of the terminal and desync the renderer. The text is capped to the
// window width for the same reason — a line that wraps is a row too many.
func (a *App) clipFooter(help string) string {
	if a.clipErr == "" {
		return help
	}
	notice := lipgloss.NewStyle().MaxWidth(a.width).Render(footerErrStyle.Render(a.clipErr))
	lines := strings.Split(help, "\n")
	lines[0] = notice
	return strings.Join(lines, "\n")
}

// pageSize is the window-size message pages receive, minus the shell chrome.
// chromeHeight budgets one row for the footer, so anything the help view spends
// beyond that first row comes out of the body. Full help is not a fixed two
// rows: it renders as columns joined horizontally, making it as tall as its
// longest column, which changes with how many bindings are enabled (the
// artist-switch one is hidden for a single artist). Measuring it is the only way
// to keep the frame the height of the terminal; a frame one row too tall scrolls
// the header off and desyncs the renderer.
func (a *App) pageSize() tea.WindowSizeMsg {
	h := a.height - chromeHeight - (lipgloss.Height(a.help.View(a.keys)) - 1)
	if h < 1 {
		h = 1
	}
	return tea.WindowSizeMsg{Width: a.width, Height: h}
}

// broadcast sends a message to every page, collecting their commands.
func (a *App) broadcast(msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd
	for i, p := range a.pages {
		updated, cmd := p.Update(msg)
		a.pages[i] = updated.(page)
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

// updateActive routes a message to the active page only.
func (a *App) updateActive(msg tea.Msg) tea.Cmd {
	updated, cmd := a.pages[a.active].Update(msg)
	a.pages[a.active] = updated.(page)
	return cmd
}

// cycleGroup advances to the next group in the configured carousel and tells
// pages to reload. The page on screen is re-activated after the reset so it
// can load its initial view.
func (a *App) cycleGroup() tea.Cmd {
	if len(a.groups) < 2 {
		return nil // a single artist has nowhere to cycle
	}
	i := 0
	for j, g := range a.groups {
		if g == a.group {
			i = j
			break
		}
	}
	a.group = a.groups[(i+1)%len(a.groups)]
	return tea.Batch(
		a.broadcast(uimsg.GroupChanged{Group: a.group}),
		a.updateActive(uimsg.Activated{}),
	)
}

// View satisfies tea.Model. It also declares the terminal state the shell
// wants: v2 has no WithAltScreen program option, so the alt screen is a
// property of every frame instead of a one-shot command at startup.
func (a *App) View() tea.View {
	v := tea.NewView(a.render())
	v.AltScreen = true
	return v
}

func (a *App) render() string {
	header := headerStyle.Render("cosmo-tui") + "  " + groupStyle.Render(a.group)

	tabs := make([]string, len(a.pages))
	for i, p := range a.pages {
		if i == a.active {
			tabs[i] = tabActiveStyle.Render(p.Title())
		} else {
			tabs[i] = tabInactiveStyle.Render(p.Title())
		}
	}
	tabBar := tabBarStyle.Render(lipgloss.JoinHorizontal(lipgloss.Top, tabs...))

	body := a.pages[a.active].View().Content
	footer := a.clipFooter(a.help.View(a.keys))

	return strings.Join([]string{header, tabBar, body, footer}, "\n")
}
