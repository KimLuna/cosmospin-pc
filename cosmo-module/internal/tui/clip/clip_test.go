package clip

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// TestKey checks the paste chord is recognised however the key event was
// assembled, and that a plain v (the one that types a character) is not it.
func TestKey(t *testing.T) {
	cases := []struct {
		name string
		msg  tea.KeyPressMsg
		want bool
	}{
		{"ctrl+v", tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl}, true},
		// Some inputs carry the unmodified character alongside the chord, which
		// is what the key's String would answer with.
		{"ctrl+v with text", tea.KeyPressMsg{Code: 'v', Text: "v", Mod: tea.ModCtrl}, true},
		{"plain v", tea.KeyPressMsg{Code: 'v', Text: "v"}, false},
		{"ctrl+c", tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, false},
	}
	for _, tc := range cases {
		if got := Key(tc.msg); got != tc.want {
			t.Errorf("Key(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestReadAnswers checks the read command answers with a Msg either way — the
// machine running this may have no clipboard tooling at all, and a paste that
// cannot happen still has to come back and say so rather than hang.
func TestReadAnswers(t *testing.T) {
	done := make(chan tea.Msg, 1)
	go func() { done <- Read()() }()

	select {
	case msg := <-done:
		got, ok := msg.(Msg)
		if !ok {
			t.Fatalf("Read answered with %T, want a clip.Msg", msg)
		}
		if got.Err != nil && got.Text != "" {
			t.Fatalf("a failed read carried text as well: %+v", got)
		}
	case <-time.After(readTimeout + time.Second):
		t.Fatal("Read never answered, so its own timeout did not fire either")
	}
}
