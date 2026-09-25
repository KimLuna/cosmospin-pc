// Package textfmt normalizes Cosmo API text for terminal display. Terminals
// disagree with Go's width/line handling in two ways that overflow TUI frames
// (a frame one row too tall scrolls the header off and desyncs Bubble Tea's
// renderer, showing duplicated/shifted content):
//
//   - Unicode line/paragraph separators (U+2028, U+2029, U+0085) that terminals
//     render as line breaks but Go's strings and lipgloss treat as in-line runes,
//     so a wrapped line silently gains an uncounted physical row.
//   - Emoji modifier sequences (skin tones, ZWJ joins): lipgloss measures the
//     whole grapheme cluster as width 2, but many terminals (st, kitty, and the
//     pyte reference emulator) render each codepoint as its own 2-wide glyph, so
//     the app under-measures the line, pads it short, and it overflows and wraps.
//   - THAI SARA AM (U+0E33) and LAO AM (U+0EB3): wcwidth terminals render them
//     as width-1 spacing marks, but uniseg folds them into the preceding
//     consonant's grapheme cluster (width 0), so a padded line containing one
//     renders a column wider than measured and can overflow and wrap.
//   - Invisible spacers: SOFT HYPHEN (width 0 to lipgloss, a width-1 glyph to
//     wcwidth terminals) and the Hangul fillers (width 1-2 to lipgloss,
//     invisible width 0 on wcwidth terminals), popular as blank spacers in
//     nicknames. Either direction of mismatch desyncs the renderer.
//
// It is a leaf package (imports only stdlib) so any page can use it.
package textfmt

import "strings"

// lineBreaks maps the vertical whitespace terminals break on to "\n".
var lineBreaks = strings.NewReplacer(
	"\r\n", "\n",
	"\r", "\n",
	"\u2028", "\n", // LINE SEPARATOR
	"\u2029", "\n", // PARAGRAPH SEPARATOR
	"\u0085", "\n", // NEL
	"\v", "\n", // vertical tab
	"\f", "\n", // form feed
)

// widthFixes rewrites characters whose width lipgloss and wcwidth terminals
// disagree on into visually equivalent text they measure identically:
//   - the two AM vowel signs decompose into their canonical mark + vowel pair,
//     so uniseg measures the width-1 vowel the terminal actually renders
//     instead of clustering the whole sign to width 0
//   - soft hyphens drop (an invisible hyphenation hint; terminals draw it)
//   - Hangul fillers become a regular space (keeps their spacer intent)
var widthFixes = strings.NewReplacer(
	"\u0e33", "\u0e4d\u0e32", // THAI SARA AM -> NIKHAHIT + SARA AA
	"\u0eb3", "\u0ecd\u0eb2", // LAO AM -> NIGGAHITA + AA
	"\u00ad", "", // SOFT HYPHEN
	"\u1160", " ", // HANGUL JUNGSEONG FILLER
	"\u3164", " ", // HANGUL FILLER
	"\uffa0", " ", // HALFWIDTH HANGUL FILLER
)

// Normalize prepares API text for terminal display: it converts vertical
// whitespace to "\n", rewrites width-mismatched characters (AM vowel signs,
// soft hyphens, Hangul fillers), and drops emoji modifiers whose width
// lipgloss and the terminal disagree on, leaving the base emoji (a consistent
// width everywhere).
func Normalize(s string) string {
	s = lineBreaks.Replace(s)
	s = widthFixes.Replace(s)
	if !hasModifier(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isEmojiModifier(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Line collapses s to a single line for one-line UI slots (status bars): runs
// of whitespace, including line breaks (Fields splits on unicode.IsSpace, which
// covers U+2028/U+2029/NEL too), become a single space. The renderer truncates
// over-wide lines to the terminal width, so only embedded line breaks can
// overflow a one-line slot.
func Line(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ShortAddr abbreviates a 0x wallet or contract address to 0xABCD…WXYZ. A full
// address is 42 characters, which crowds out everything it sits next to in a
// status line or a table row, and the ends are what a reader checks. Anything
// short enough to show whole is returned untouched.
func ShortAddr(addr string) string {
	if len(addr) <= 12 {
		return addr
	}
	return addr[:6] + "…" + addr[len(addr)-4:]
}

// isEmojiModifier reports whether r is an emoji modifier that terminals render
// as a separate glyph (skin tone) or use to fuse a sequence (ZWJ / variation
// selector) that lipgloss width-measures differently than the terminal draws it.
func isEmojiModifier(r rune) bool {
	switch {
	case r >= 0x1F3FB && r <= 0x1F3FF: // skin-tone modifiers
		return true
	case r == 0x200D: // zero-width joiner
		return true
	case r == 0xFE0E || r == 0xFE0F: // variation selectors 15/16
		return true
	}
	return false
}

func hasModifier(s string) bool {
	for _, r := range s {
		if isEmojiModifier(r) {
			return true
		}
	}
	return false
}
