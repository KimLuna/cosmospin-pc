package style

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestWrapNormalizesAndReflows every detail view runs its body through Wrap, so
// both halves matter: the normalization keeps a separator the terminal breaks on
// out of the frame, and the reflow keeps long text inside the pane instead of
// clipped at its edge.
func TestWrapNormalizesAndReflows(t *testing.T) {
	const w = 20

	got := Wrap("a b", w)
	if strings.ContainsRune(got, ' ') {
		t.Error("Wrap left a line separator in the text")
	}
	// The reflow pads each line out to the width, so compare the trimmed rows.
	var rows []string
	for _, ln := range strings.Split(got, "\n") {
		rows = append(rows, strings.TrimRight(ln, " "))
	}
	if strings.Join(rows, "|") != "a|b" {
		t.Errorf("the separator should have become a newline, got %q", got)
	}

	long := strings.Repeat("wrap me ", 12)
	for i, ln := range strings.Split(Wrap(long, w), "\n") {
		if lw := lipgloss.Width(ln); lw > w {
			t.Errorf("row %d is %d cols, want <= %d", i, lw, w)
		}
	}
}

// TestWrapUnsizedPaneStillNormalizes a pane that has not been sized yet must not
// lose the normalization, which is the half that protects the frame.
func TestWrapUnsizedPaneStillNormalizes(t *testing.T) {
	got := Wrap("a b", 0)
	if got != "a\nb" {
		t.Fatalf("Wrap(_, 0) = %q, want the normalized text unwrapped", got)
	}
}
