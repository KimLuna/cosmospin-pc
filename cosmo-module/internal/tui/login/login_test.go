package login

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/djvu/cosmo-tui/internal/config"
	"codeberg.org/djvu/cosmo-tui/internal/tui/clip"
	"codeberg.org/djvu/cosmo-tui/internal/wallet"

	tea "charm.land/bubbletea/v2"
)

// TestDiscardForeignWalletKey checks the failed-provisioning path: a stored key
// that still signs for the account survives (provisioning fails for transient
// reasons, and the key can only be rebuilt at login), while one belonging to
// another account - or one we can no longer parse - is dropped.
func TestDiscardForeignWalletKey(t *testing.T) {
	key := bytes.Repeat([]byte{0xab}, 32)
	w, err := wallet.FromKeyBytes(key)
	if err != nil {
		t.Fatalf("FromKeyBytes: %v", err)
	}
	ours := w.EOA().Hex()
	const theirs = "0x0000000000000000000000000000000000000001"

	for _, tc := range []struct {
		name  string
		key   []byte // nil for "nothing stored"
		raw   string // written verbatim when set, in place of key
		eoa   string
		kept  bool
		fresh bool // no key file expected afterwards
	}{
		{name: "matching key kept", key: key, eoa: ours, kept: true},
		{name: "foreign key dropped", key: key, eoa: theirs},
		{name: "corrupt key dropped", raw: "not hex\n", eoa: ours},
		{name: "short key dropped", raw: "abcd\n", eoa: ours},
		{name: "unparseable eoa keeps key", key: key, eoa: "nonsense", kept: true},
		{name: "no key stored", eoa: ours, fresh: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", dir)
			p := filepath.Join(dir, "cosmo-tui", "wallet.key")
			if _, err := config.LoadWalletKey(); !errors.Is(err, config.ErrNoWalletKey) {
				t.Skipf("XDG_CONFIG_HOME override not honored here (err %v)", err)
			}
			switch {
			case tc.raw != "":
				if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(p, []byte(tc.raw), 0o600); err != nil {
					t.Fatalf("write: %v", err)
				}
			case tc.key != nil:
				if err := config.SaveWalletKey(tc.key); err != nil {
					t.Fatalf("SaveWalletKey: %v", err)
				}
			}

			discardForeignWalletKey(tc.eoa)

			got, err := config.LoadWalletKey()
			switch {
			case tc.kept:
				if err != nil {
					t.Fatalf("key should have been kept: %v", err)
				}
				if !bytes.Equal(got, tc.key) {
					t.Fatalf("kept key changed: got %x, want %x", got, tc.key)
				}
			default:
				if !errors.Is(err, config.ErrNoWalletKey) {
					t.Fatalf("key should be gone: got %x, err %v", got, err)
				}
				if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) && !tc.fresh {
					t.Fatalf("wallet.key still on disk: %v", err)
				}
			}
		})
	}
}

// TestPasteIntoPrompt checks the sign-in prompt takes pasted text (the
// terminal's own paste falls through to the input) and reports a clipboard
// that could not be read instead of dropping it.
func TestPasteIntoPrompt(t *testing.T) {
	m := New()
	updated, _ := m.Update(tea.PasteMsg{Content: "fan@example.com"})
	m = updated.(Model)
	if got := m.input.Value(); got != "fan@example.com" {
		t.Fatalf("prompt = %q, want the pasted address", got)
	}

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("ctrl+v should read the clipboard")
	}
	if got := m.input.Value(); got != "fan@example.com" {
		t.Fatalf("prompt = %q, want the chord itself not typed in", got)
	}

	updated, _ = m.Update(clip.Msg{Err: errors.New("no clipboard tool found")})
	m = updated.(Model)
	if !strings.Contains(m.render(), "no clipboard tool found") {
		t.Fatalf("clipboard failure not reported:\n%s", m.render())
	}
}
