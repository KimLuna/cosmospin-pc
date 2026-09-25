package cosmo

import (
	"errors"
	"fmt"
	"testing"
)

// TestNewAPIErrorParsesCode checks that the standard {"error":{...}} body shape
// yields Code/Message, Body keeps the raw text, and Error() uses the message.
func TestNewAPIErrorParsesCode(t *testing.T) {
	body := `{"error":{"code":"CHANNEL_MESSAGE_INVALID_ARTIST","message":"invalid artist","traceId":"t"}}`
	e := newAPIError(400, []byte(body))
	if e.Code != "CHANNEL_MESSAGE_INVALID_ARTIST" {
		t.Fatalf("Code = %q", e.Code)
	}
	if e.Message != "invalid artist" {
		t.Fatalf("Message = %q", e.Message)
	}
	if e.Body != body {
		t.Fatalf("Body = %q, want the raw text", e.Body)
	}
	if want := "cosmo API error 400: invalid artist"; e.Error() != want {
		t.Fatalf("Error() = %q, want %q", e.Error(), want)
	}
}

// TestNewAPIErrorRawBody checks the fallback for non-JSON bodies: no code, the
// trimmed raw text preserved on Body, and an Error() that never includes it
// (gateway error pages are multi-line HTML that would overflow a status line).
func TestNewAPIErrorRawBody(t *testing.T) {
	e := newAPIError(502, []byte("<html>bad gateway</html>\n"))
	if e.Code != "" || e.Message != "" {
		t.Fatalf("expected no parsed code/message, got %q/%q", e.Code, e.Message)
	}
	if e.Body != "<html>bad gateway</html>" {
		t.Fatalf("Body = %q", e.Body)
	}
	if want := "cosmo API error 502 (Bad Gateway)"; e.Error() != want {
		t.Fatalf("Error() = %q, want %q", e.Error(), want)
	}
}

// TestErrorCode checks code extraction from bare and wrapped APIErrors, and ""
// for everything else.
func TestErrorCode(t *testing.T) {
	ae := &APIError{Status: 400, Code: "SOME_CODE"}
	if got := ErrorCode(ae); got != "SOME_CODE" {
		t.Fatalf("bare: %q", got)
	}
	if got := ErrorCode(fmt.Errorf("load: %w", ae)); got != "SOME_CODE" {
		t.Fatalf("wrapped: %q", got)
	}
	if got := ErrorCode(errors.New("x")); got != "" {
		t.Fatalf("plain error: %q", got)
	}
	if got := ErrorCode(nil); got != "" {
		t.Fatalf("nil: %q", got)
	}
}
