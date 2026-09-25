// Package login is the first-run Privy sign-in flow as a Bubble Tea model.
//
// It drives two steps with a single text input: enter email (Cosmo emails a
// one-time code), then enter the code. On success it persists the credentials
// and reports done to the caller (standalone via Run, or embedded in the app
// shell). Bubble Tea's async pattern is used throughout: each network step is a
// tea.Cmd returning a typed message.
package login

import (
	"context"
	"errors"
	"strings"
	"time"

	"codeberg.org/djvu/cosmo-tui/internal/config"
	"codeberg.org/djvu/cosmo-tui/internal/cosmo"
	"codeberg.org/djvu/cosmo-tui/internal/tui/clip"
	"codeberg.org/djvu/cosmo-tui/internal/tui/style"
	"codeberg.org/djvu/cosmo-tui/internal/tui/textfmt"
	"codeberg.org/djvu/cosmo-tui/internal/wallet"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true)
	dimStyle    = lipgloss.NewStyle().Faint(true)
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
)

type (
	codeSentMsg struct{}
	doneMsg     struct{ creds config.Credentials }
	errMsg      struct{ err error }
)

// Model is the sign-in model.
type Model struct {
	input    textinput.Model
	onCode   bool // false: awaiting email, true: awaiting code
	busy     bool
	email    string
	status   string
	err      error
	creds    config.Credentials
	done     bool
	quitting bool
}

// New builds a fresh sign-in model focused on the email prompt.
func New() Model {
	in := textinput.New()
	in.Placeholder = "you@example.com"
	in.Focus()
	in.Prompt = "email: "
	in.SetWidth(48) // the form never learns the frame size; wide enough for an email
	style.PlainInput(&in)
	return Model{input: in}
}

// Done reports whether sign-in completed, with the resulting credentials.
func (m Model) Done() (config.Credentials, bool) { return m.creds, m.done }

// Err returns a terminal error (e.g. the user quit), if any.
func (m Model) Err() error {
	if m.quitting && !m.done {
		return errors.New("sign-in cancelled")
	}
	return nil
}

func (m Model) Init() tea.Cmd { return textinput.Blink }

// Update advances the flow. It satisfies tea.Model; when embedding the login
// step the root app type-asserts the result back to login.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.quitting = true
			return m, tea.Quit
		case "enter":
			nm, cmd := m.submit()
			return nm, cmd
		}
		// The text input binds ctrl+v itself, but answers a failed read by
		// stashing the error where nothing here renders it. Read the clipboard
		// ourselves so a host that cannot be read says so.
		if clip.Key(msg) {
			return m, clip.Read()
		}

	case clip.Msg:
		if msg.Err != nil {
			m.err = msg.Err
			m.status = ""
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(tea.PasteMsg{Content: msg.Text})
		return m, cmd
	case codeSentMsg:
		m.busy = false
		m.onCode = true
		m.err = nil
		m.status = "code sent to " + m.email
		m.input.SetValue("")
		m.input.Prompt = "code: "
		m.input.Placeholder = "6-digit code"
		return m, nil
	case doneMsg:
		m.busy = false
		m.creds = msg.creds
		m.done = true
		return m, tea.Quit
	case errMsg:
		m.busy = false
		m.err = msg.err
		m.status = ""
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// submit handles Enter in whichever phase we're in.
func (m Model) submit() (Model, tea.Cmd) {
	if m.busy {
		return m, nil
	}
	value := strings.TrimSpace(m.input.Value())
	if value == "" {
		return m, nil
	}
	m.busy = true
	m.err = nil
	if !m.onCode {
		m.email = value
		m.status = "sending code…"
		return m, sendCode(value)
	}
	m.status = "signing in…"
	return m, doLogin(m.email, value)
}

// View satisfies tea.Model. The layout itself is render; keeping it a plain
// string keeps the composing shell and this package's tests off tea.View.
func (m Model) View() tea.View { return tea.NewView(m.render()) }

func (m Model) render() string {
	if m.done {
		return statusStyle.Render("signed in ✓") + "\n"
	}
	var b strings.Builder
	b.WriteString(titleStyle.Render("cosmo-tui - sign in") + "\n\n")
	if m.err != nil {
		b.WriteString(errStyle.Render("error: "+textfmt.Line(m.err.Error())) + "\n\n")
	} else if m.status != "" {
		b.WriteString(statusStyle.Render(m.status) + "\n\n")
	}
	b.WriteString(m.input.View() + "\n\n")
	b.WriteString(dimStyle.Render("enter to continue · esc to quit"))
	return b.String()
}

// -- commands --------------------------------------------------------------- //

func sendCode(email string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := cosmo.SendCode(ctx, email); err != nil {
			return errMsg{err}
		}
		return codeSentMsg{}
	}
}

func doLogin(email, code string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		creds, session, err := cosmo.Login(ctx, email, code)
		if err != nil {
			return errMsg{err}
		}
		if err := config.Save(creds); err != nil {
			return errMsg{err}
		}
		// Provision the objekt-send signing key while we still hold a Privy
		// session (the only moment we do); persist it for later launches. This
		// is best-effort: a failure here must not block sign-in - sending stays
		// disabled until the key is provisioned, everything else works.
		provisionWallet(ctx, session)
		return doneMsg{creds}
	}
}

// provisionWallet reconstructs the embedded-wallet signing key from the Privy
// session and caches it to disk. Errors are swallowed by design (see doLogin).
//
// Whenever it cannot write a key it makes sure a stale one is not left behind:
// signing in as a different account, or with an account that has no embedded
// wallet, would otherwise leave the previous user's key on disk, still loaded
// at startup and still used to sign sends.
func provisionWallet(ctx context.Context, session cosmo.PrivySession) {
	if session.EOA == "" {
		// No embedded wallet on this account, so any stored key is not ours.
		_ = config.RemoveWalletKey()
		return
	}
	w, err := wallet.Provision(ctx, session.AccessToken, session.EOA)
	if err != nil {
		discardForeignWalletKey(session.EOA)
		return
	}
	_ = config.SaveWalletKey(w.KeyBytes())
}

// discardForeignWalletKey removes the stored key unless it already signs for
// eoa. Provisioning fails for transient reasons too, and the key can only be
// rebuilt during a login, so a key that still matches the account is worth
// keeping; one that does not (or that no longer parses) is dead weight.
func discardForeignWalletKey(eoa string) {
	want, err := wallet.ParseAddress(eoa)
	if err != nil {
		return // unreadable address: no grounds to call the stored key stale
	}
	keyBytes, err := config.LoadWalletKey()
	if err != nil {
		if errors.Is(err, config.ErrNoWalletKey) {
			return
		}
		_ = config.RemoveWalletKey() // corrupt, and we cannot replace it
		return
	}
	if w, err := wallet.FromKeyBytes(keyBytes); err != nil || w.EOA() != want {
		_ = config.RemoveWalletKey()
	}
}

// Run executes the sign-in flow as a standalone program, returning the saved
// credentials. Used at startup before the main app has a token.
func Run() (config.Credentials, error) {
	p := tea.NewProgram(New())
	final, err := p.Run()
	if err != nil {
		return config.Credentials{}, err
	}
	m := final.(Model)
	if creds, ok := m.Done(); ok {
		return creds, nil
	}
	if e := m.Err(); e != nil {
		return config.Credentials{}, e
	}
	return config.Credentials{}, errors.New("sign-in did not complete")
}
