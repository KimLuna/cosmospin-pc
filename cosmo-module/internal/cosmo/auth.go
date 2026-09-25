package cosmo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"codeberg.org/djvu/cosmo-tui/internal/config"
)

// refreshBuffer: refresh when this little (or less) of the access token's life
// remains, so a token that would expire mid-session is renewed up front. Access
// tokens live ~7 days, so 24h is a comfortable margin (matches lib/auth.py).
const refreshBuffer = 24 * time.Hour

// ErrNeedsLogin means we have no usable refresh token; the user must log in.
var ErrNeedsLogin = errors.New("no valid credentials; log in again")

// decodeExp returns the exp (epoch seconds) from a JWT without verifying it.
func decodeExp(token string) (int64, error) {
	parts := splitJWT(token)
	if len(parts) < 2 {
		return 0, fmt.Errorf("malformed JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, err
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return 0, err
	}
	return claims.Exp, nil
}

// splitJWT splits on '.' without pulling in strings for one call site.
func splitJWT(token string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			parts = append(parts, token[start:i])
			start = i + 1
		}
	}
	return append(parts, token[start:])
}

// isExpired reports whether the token is expired or expires within buffer.
// An unreadable token is treated as expired so we refresh.
func isExpired(token string, buffer time.Duration) bool {
	exp, err := decodeExp(token)
	if err != nil {
		return true
	}
	return time.Until(time.Unix(exp, 0)) <= buffer
}

// EnsureToken returns a valid access token, refreshing and persisting it when
// stale. Safe to call before every request and from concurrent goroutines: the
// client mutex covers the whole check-refresh-save sequence, so parallel
// callers of a stale client wait for one refresh instead of each redeeming the
// refresh token (which could invalidate the others' if redemption is one-shot).
func (c *Client) EnsureToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.creds.AccessToken != "" && !isExpired(c.creds.AccessToken, refreshBuffer) {
		return c.creds.AccessToken, nil
	}
	if c.creds.RefreshToken == "" {
		return "", ErrNeedsLogin
	}
	fresh, err := refresh(ctx, c.hc, c.creds.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNeedsLogin, err)
	}
	c.creds = fresh
	if c.save != nil {
		_ = c.save(fresh) // best effort; an in-memory token still works this run
	}
	return c.creds.AccessToken, nil
}

// refresh exchanges a refresh token for a fresh credential pair.
func refresh(ctx context.Context, hc *http.Client, refreshToken string) (config.Credentials, error) {
	var raw map[string]any
	err := postEncrypted(ctx, hc, "/users/refresh-access-token",
		map[string]string{"refreshToken": refreshToken}, &raw)
	if err != nil {
		return config.Credentials{}, err
	}
	return ExtractCredentials(raw)
}

// ExtractCredentials pulls an accessToken/refreshToken pair out of an API
// response. Cosmo nests them under a "credentials" object, so we search
// recursively and accept snake_case (mirrors lib/auth.py extract_credentials).
func ExtractCredentials(node any) (config.Credentials, error) {
	access := findKey(node, "accessToken", "access_token")
	refreshTok := findKey(node, "refreshToken", "refresh_token")
	if access == "" || refreshTok == "" {
		return config.Credentials{}, fmt.Errorf("could not find accessToken/refreshToken in response")
	}
	return config.Credentials{AccessToken: access, RefreshToken: refreshTok}, nil
}

// findKey recursively searches a decoded-JSON tree for the first string value
// under camel or snake, preferring camel at each node.
func findKey(node any, camel, snake string) string {
	switch n := node.(type) {
	case map[string]any:
		if v, ok := n[camel]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		if v, ok := n[snake]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		for _, v := range n {
			if s := findKey(v, camel, snake); s != "" {
				return s
			}
		}
	case []any:
		for _, v := range n {
			if s := findKey(v, camel, snake); s != "" {
				return s
			}
		}
	}
	return ""
}
