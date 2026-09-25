// Package clip reads the host clipboard for the app's text boxes. A paste the
// terminal performs itself — ctrl+shift+v, ⌘V, a middle click — needs nothing
// from us: bracketed paste is on by default in bubbletea v2, so the text
// arrives as a tea.PasteMsg. A ctrl+v typed inside the app is the case this
// package covers: that is just a key press, and the clipboard it should read
// belongs to the windowing system, not to the terminal.
//
// The read goes through the platform's own clipboard tooling — wl-paste under
// Wayland, xclip or xsel under X11, pbpaste on macOS, the Win32 API on Windows
// (and powershell.exe under WSL) — which works whatever terminal the app is
// running in, local or over ssh. The alternative, OSC 52, asks the terminal to
// report its clipboard back over the same channel; most emulators either never
// implemented that half of the sequence or refuse it as a security matter, and
// multiplexers need explicit configuration to pass it through, so a paste built
// on it silently does nothing on a lot of setups.
//
// Reading is a command rather than a call because the unix backends shell out
// to a helper binary, which has no business running on the update loop. The
// answer comes back as a Msg that the shell routes to the active page (see
// internal/tui.App.Update); a page holding a focused text box hands the text to
// it as a tea.PasteMsg, which is the same path the terminal's own paste takes,
// so both land at the cursor and obey the box's character limit.
package clip

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/atotto/clipboard"
)

// readTimeout caps a clipboard read. The unix backends exec a helper that asks
// the display server for the selection, and an unresponsive clipboard owner
// leaves that helper waiting indefinitely; without a cap the paste would simply
// never arrive, and never say why.
const readTimeout = 2 * time.Second

// Msg is the answer to a Read: the clipboard's text, or why it could not be
// read. Empty Text with a nil Err means the clipboard holds nothing.
type Msg struct {
	Text string
	Err  error
}

// Key reports whether a key press is the paste chord. ctrl+v is what reaches
// the application; the terminal keeps ctrl+shift+v and ⌘V for itself and
// answers those with a bracketed paste (tea.PasteMsg) instead.
//
// It matches on Keystroke rather than String: String answers with the key's
// literal text whenever it carries any, so a chord that arrived with its
// unmodified character attached would compare as a plain "v".
func Key(msg tea.KeyPressMsg) bool { return msg.Keystroke() == "ctrl+v" }

// Read is the command that reads the clipboard, answering with a Msg.
func Read() tea.Cmd {
	return func() tea.Msg {
		// Buffered so a read that finishes after the timeout can still hand its
		// result over and let the goroutine go, instead of blocking forever.
		done := make(chan Msg, 1)
		go func() {
			text, err := clipboard.ReadAll()
			done <- Msg{Text: text, Err: err}
		}()
		select {
		case msg := <-done:
			if msg.Err != nil {
				return Msg{Err: readErr(msg.Err)}
			}
			return msg
		case <-time.After(readTimeout):
			return Msg{Err: errors.New("clipboard read timed out")}
		}
	}
}

// readErr rewrites the backend's failure into something a one-line status can
// carry: a missing helper is reported as a full install sentence, and a helper
// that failed as nothing but its exit status — the reason it failed ("Can't
// open display", the usual one on a bare tty) goes to its stderr, which is
// where the useful half of the message is.
func readErr(err error) error {
	if clipboard.Unsupported {
		return errors.New("no clipboard tool found — install wl-clipboard, xclip or xsel")
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if stderr := strings.TrimSpace(string(exit.Stderr)); stderr != "" {
			return fmt.Errorf("clipboard: %s", stderr)
		}
	}
	return fmt.Errorf("clipboard: %w", err)
}
