// Package cosmo is the API client for api.cosmo.fans: auth/token lifecycle,
// login, and the room/live/talk feature endpoints. It talks plain
// Bearer-JWT HTTP (plus one AES-encrypted body on login/refresh) and needs no
// device, proxy, or the app itself.
package cosmo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"codeberg.org/djvu/cosmo-tui/internal/config"
)

const (
	// endpoint is the API host; apiBase is the v3 prefix most calls use, with
	// apiBaseV4 for the handful of endpoints served under /bff/v4.
	endpoint  = "https://api.cosmo.fans"
	apiBase   = endpoint + "/bff/v3"
	apiBaseV4 = endpoint + "/bff/v4"

	// cosmoKey is the app's static AES-256 key (BASE64_ENCRYPTION_KEY). It is
	// baked into the APK; if MODHAUS ever rotates it, regenerate with the
	// Python cosmo-token extractor and update this constant.
	cosmoKey = "aUkaF3wWVltAI1QYVmxsZXfTV6L7mPTxA5+MMkLV9DE="

	// UserAgent: some Cosmo endpoints reject the default Go UA, so we pose as
	// the app's OkHttp stack. Exported so media downloaders (internal/hls) send
	// the same UA when pulling from the CDN.
	UserAgent = "okhttp/4.12.0"
)

// Client is an authenticated Cosmo API client. It owns the token lifecycle:
// every request refreshes the access token first if it is stale, persisting the
// new tokens via the save callback.
type Client struct {
	hc *http.Client
	// mu guards creds: requests run in concurrent goroutines (every page load
	// is its own tea.Cmd, plus the SSE loops), and serializing EnsureToken also
	// keeps two of them from racing to redeem the same refresh token.
	mu    sync.Mutex
	creds config.Credentials
	save  func(config.Credentials) error
}

// New builds a client from stored credentials. save is called whenever tokens
// are refreshed (typically config.Save); it may be nil to skip persistence.
func New(creds config.Credentials, save func(config.Credentials) error) *Client {
	return &Client{
		hc:    &http.Client{Timeout: 30 * time.Second},
		creds: creds,
		save:  save,
	}
}

// APIError describes a non-2xx API response. Code and Message are parsed from
// the API's {"error":{"code","message"}} JSON body when present (e.g.
// CHANNEL_MESSAGE_INVALID_ARTIST); Body always keeps the raw text.
type APIError struct {
	Status  int
	Code    string
	Message string
	Body    string
}

// Error is a single line suitable for a status bar: the parsed API message (or
// code) when the body was the standard JSON shape, otherwise just the status.
// Gateway errors (502/504) carry multi-line HTML bodies, so the raw Body is
// never included; it stays on the struct for debugging.
func (e *APIError) Error() string {
	switch {
	case e.Message != "":
		return fmt.Sprintf("cosmo API error %d: %s", e.Status, e.Message)
	case e.Code != "":
		return fmt.Sprintf("cosmo API error %d: %s", e.Status, e.Code)
	default:
		if t := http.StatusText(e.Status); t != "" {
			return fmt.Sprintf("cosmo API error %d (%s)", e.Status, t)
		}
		return fmt.Sprintf("cosmo API error %d", e.Status)
	}
}

// newAPIError builds an APIError, extracting code/message from the standard
// {"error":{...}} body shape when the body parses as JSON.
func newAPIError(status int, body []byte) *APIError {
	e := &APIError{Status: status, Body: string(bytes.TrimSpace(body))}
	var wire struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &wire) == nil {
		e.Code, e.Message = wire.Error.Code, wire.Error.Message
	}
	return e
}

// ErrorCode returns the API error code carried by err, or "" when err is not
// an APIError or its body carried no code.
func ErrorCode(err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

// getJSON performs an authenticated GET against /bff/v3 and decodes the JSON
// response into out.
func (c *Client) getJSON(ctx context.Context, path string, params url.Values, out any) error {
	return c.getJSONBase(ctx, apiBase, path, params, out)
}

// getJSONV4 is getJSON for endpoints served under /bff/v4.
func (c *Client) getJSONV4(ctx context.Context, path string, params url.Values, out any) error {
	return c.getJSONBase(ctx, apiBaseV4, path, params, out)
}

// getJSONBase performs an authenticated GET against a given versioned base and
// decodes the JSON response into out.
func (c *Client) getJSONBase(ctx context.Context, base, path string, params url.Values, out any) error {
	token, err := c.EnsureToken(ctx)
	if err != nil {
		return err
	}
	u := base + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", UserAgent)
	return c.do(req, out)
}

// do sends req, checks status, and decodes JSON into out (out may be nil).
func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decodeResponse(resp, out)
}

// decodeResponse checks status and decodes the (possibly encrypted) JSON body
// into out (out may be nil). Cosmo marks encrypted bodies with the response
// header x-cosmo-encrypted:1; those are base64(iv||ciphertext) under cosmoKey,
// the same framing the app uses for its encrypted request bodies.
func decodeResponse(resp *http.Response, out any) error {
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return newAPIError(resp.StatusCode, body)
	}
	if out == nil {
		return nil
	}
	if resp.Header.Get("x-cosmo-encrypted") == "1" {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		plain, err := decrypt(string(bytes.TrimSpace(body)), cosmoKey)
		if err != nil {
			return err
		}
		return json.Unmarshal(plain, out)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// sendJSON performs an authenticated request with a JSON body (POST/PUT),
// decoding the JSON response into out (out may be nil).
func (c *Client) sendJSON(ctx context.Context, method, path string, body, out any) error {
	token, err := c.EnsureToken(ctx)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, apiBase+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	return c.do(req, out)
}

// postEncrypted sends the app's encrypted-body POST (login/refresh): the JSON
// payload is AES-encrypted and sent as text/plain with x-cosmo-encrypted:1. It
// is pre-auth (no Bearer token) and decodes the JSON response into out. Uses a
// caller-supplied http.Client so it works before a Client exists.
func postEncrypted(ctx context.Context, hc *http.Client, path string, payload, out any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	enc, err := encrypt(string(raw), cosmoKey)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+path, bytes.NewBufferString(enc))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("x-cosmo-encrypted", "1")
	req.Header.Set("User-Agent", UserAgent)

	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decodeResponse(resp, out)
}
