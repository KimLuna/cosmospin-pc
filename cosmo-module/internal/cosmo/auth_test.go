package cosmo

import (
	"encoding/base64"
	"fmt"
	"testing"
	"time"
)

// makeJWT builds a throwaway unsigned JWT carrying just an exp claim.
func makeJWT(exp int64) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp)))
	return header + "." + payload + ".sig"
}

func TestDecodeExpAndExpiry(t *testing.T) {
	future := time.Now().Add(7 * 24 * time.Hour).Unix()
	tok := makeJWT(future)
	exp, err := decodeExp(tok)
	if err != nil {
		t.Fatalf("decodeExp: %v", err)
	}
	if exp != future {
		t.Fatalf("exp = %d, want %d", exp, future)
	}
	if isExpired(tok, refreshBuffer) {
		t.Fatalf("token 7d out should not be within the 24h refresh buffer")
	}
	// A token expiring in 1h is inside the 24h buffer => treated as expired.
	soon := makeJWT(time.Now().Add(time.Hour).Unix())
	if !isExpired(soon, refreshBuffer) {
		t.Fatalf("token expiring in 1h should be considered stale")
	}
	// Garbage token is treated as expired.
	if !isExpired("not-a-jwt", refreshBuffer) {
		t.Fatalf("unreadable token should be treated as expired")
	}
}

func TestExtractCredentials(t *testing.T) {
	// Mirrors the nested shape login/refresh return.
	resp := map[string]any{
		"user": map[string]any{"nickname": "x"},
		"credentials": map[string]any{
			"accessToken":  "aaa",
			"refreshToken": "rrr",
		},
	}
	creds, err := ExtractCredentials(resp)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if creds.AccessToken != "aaa" || creds.RefreshToken != "rrr" {
		t.Fatalf("got %+v", creds)
	}

	if _, err := ExtractCredentials(map[string]any{"nope": 1}); err == nil {
		t.Fatalf("expected error when tokens absent")
	}
}
