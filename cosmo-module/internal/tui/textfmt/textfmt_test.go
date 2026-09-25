package textfmt

import "testing"

func TestNormalizeLineBreaks(t *testing.T) {
	got := Normalize("a\u2028b\u2029c\u0085d\r\ne")
	if want := "a\nb\nc\nd\ne"; got != want {
		t.Fatalf("Normalize = %q, want %q", got, want)
	}
}

// TestNormalizeWidthFixes checks the rewrites for characters lipgloss and
// wcwidth terminals measure differently: the Thai and Lao AM vowel signs
// decompose into the mark + vowel pair, soft hyphens drop, and Hangul filler
// spacers become regular spaces.
func TestNormalizeWidthFixes(t *testing.T) {
	tests := []struct{ in, want string }{
		{"ทำไมยังไม่นอนอีกอะะ", "ทําไมยังไม่นอนอีกอะะ"}, // U+0E33 -> U+0E4D U+0E32
		{"ຄຳ", "ຄໍາ"},               // U+0EB3 -> U+0ECD U+0EB2
		{"co­operate", "cooperate"}, // soft hyphen dropped
		{"별ㅤ하늘", "별 하늘"},            // HANGUL FILLER -> space
		{"aᅠbﾠc", "a b c"},          // jungseong + halfwidth fillers -> space
		{"no thai here", "no thai here"},
	}
	for _, tt := range tests {
		if got := Normalize(tt.in); got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestLine checks that multi-line text (e.g. an HTML gateway-error page) is
// collapsed to one status-bar line.
func TestLine(t *testing.T) {
	tests := []struct{ in, want string }{
		{"<html>\n<body>\n  504 Gateway Time-out\n</body>\n</html>", "<html> <body> 504 Gateway Time-out </body> </html>"},
		{"one\u2028two\u2029three", "one two three"},
		{"  padded\t\ttabs  ", "padded tabs"},
		{"already one line", "already one line"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := Line(tt.in); got != tt.want {
			t.Errorf("Line(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestShortAddr an address long enough to crowd its row is abbreviated to its
// two ends; anything already short enough is left alone.
func TestShortAddr(t *testing.T) {
	tests := []struct{ in, want string }{
		{"0x3a6E4EFfEb030f950870C98834eb40f5BE9c7f2A", "0x3a6E…7f2A"}, // a real 42-char address
		{"0xd0ee3ba23a384a8eefd43f33a957ded60ed12706", "0xd0ee…2706"},
		{"0x1234567890", "0x1234567890"}, // exactly 12 chars: shown whole
		{"0x12345678901", "0x1234…8901"}, // one longer: abbreviated
		{"", ""},
		{"short", "short"},
	}
	for _, tc := range tests {
		if got := ShortAddr(tc.in); got != tc.want {
			t.Errorf("ShortAddr(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
