package cosmo

import (
	"testing"
	"time"
)

// TestLocalDateTime checks that an ISO-8601 instant is rendered as local
// "YYYY-MM-DD HH:MM", and that unparseable input falls back to the raw string.
func TestLocalDateTime(t *testing.T) {
	iso := "2026-07-13T05:41:30.170Z"
	want := time.Date(2026, 7, 13, 5, 41, 0, 0, time.UTC).Local().Format("2006-01-02 15:04")
	if got := LocalDateTime(iso); got != want {
		t.Fatalf("LocalDateTime(%q) = %q, want %q", iso, got, want)
	}

	if got := LocalDateTime("not-a-date"); got != "not-a-date" {
		t.Fatalf("LocalDateTime fallback = %q, want the raw input", got)
	}
}
