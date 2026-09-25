package cosmo

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"codeberg.org/djvu/cosmo-tui/internal/config"
)

// Cosmo authenticates via Privy. The login flow mirrors the app: request an
// email one-time code from Privy, exchange the code for a Privy access token,
// then hand that token to Cosmo's login-by-privy endpoint for account tokens.
const (
	privyEndpoint = "https://auth.privy.io"
	privyAppID    = "cm7tt02g000a96m8uislrblbe"
	privyClient   = "@privy-io/js-sdk-core:0.60.7.0.1"
	// Privy sits behind Cloudflare, which bans the default Go/urllib UA.
	loginUserAgent = "okhttp/4.9.2"
)

// loginHTTP is the pre-auth client used for the login handshake.
var loginHTTP = &http.Client{Timeout: 15 * time.Second}

// SendCode asks Privy to email a one-time login code to the address.
func SendCode(ctx context.Context, email string) error {
	return privyPost(ctx, "/api/v1/passwordless/init",
		map[string]string{"email": email}, nil)
}

// PrivySession is the Privy side of the login handshake, kept so the caller can
// provision the embedded-wallet signing key (see internal/wallet). AccessToken
// authorizes the Privy recovery calls; EOA is the embedded-wallet address they
// are keyed by (empty if the response carried no ethereum wallet account).
type PrivySession struct {
	AccessToken  string
	RefreshToken string
	EOA          string
}

// VerifyCode exchanges the emailed code for a Privy session (access token plus
// the embedded-wallet address the recovery flow needs).
func VerifyCode(ctx context.Context, email, code string) (PrivySession, error) {
	var body struct {
		Token            string `json:"token"`
		PrivyAccessToken string `json:"privy_access_token"`
		RefreshToken     string `json:"refresh_token"`
		User             struct {
			LinkedAccounts []linkedAccount `json:"linked_accounts"`
		} `json:"user"`
	}
	err := privyPost(ctx, "/api/v1/passwordless/authenticate", map[string]string{
		"email": email,
		"code":  code,
		"mode":  "login-or-sign-up",
	}, &body)
	if err != nil {
		return PrivySession{}, err
	}
	token := body.Token
	if token == "" {
		token = body.PrivyAccessToken
	}
	if token == "" {
		return PrivySession{}, fmt.Errorf("Privy did not return an access token")
	}
	return PrivySession{
		AccessToken:  token,
		RefreshToken: body.RefreshToken,
		EOA:          embeddedWalletAddress(body.User.LinkedAccounts),
	}, nil
}

// linkedAccount is one entry of the Privy user's linked_accounts (only the
// fields needed to locate the embedded wallet are decoded).
type linkedAccount struct {
	Type         string `json:"type"`
	Address      string `json:"address"`
	ChainType    string `json:"chain_type"`
	WalletClient string `json:"wallet_client"`
}

// embeddedWalletAddress picks the Privy embedded-wallet ethereum address out of
// the linked accounts: the app's own wallet_client=="privy" account is
// preferred, otherwise the first ethereum wallet with an address.
func embeddedWalletAddress(accounts []linkedAccount) string {
	fallback := ""
	for _, a := range accounts {
		if a.Type != "wallet" || a.ChainType != "ethereum" || a.Address == "" {
			continue
		}
		if a.WalletClient == "privy" {
			return a.Address
		}
		if fallback == "" {
			fallback = a.Address
		}
	}
	return fallback
}

// LoginByPrivy exchanges a Privy access token for Cosmo account credentials.
func LoginByPrivy(ctx context.Context, privyToken string) (config.Credentials, error) {
	var raw map[string]any
	err := postEncrypted(ctx, loginHTTP, "/users/login-by-privy",
		map[string]string{"token": privyToken}, &raw)
	if err != nil {
		return config.Credentials{}, err
	}
	return ExtractCredentials(raw)
}

// Login runs the full flow after the user has the emailed code, returning the
// Cosmo credentials plus the Privy session (for wallet provisioning).
func Login(ctx context.Context, email, code string) (config.Credentials, PrivySession, error) {
	session, err := VerifyCode(ctx, email, code)
	if err != nil {
		return config.Credentials{}, PrivySession{}, err
	}
	creds, err := LoginByPrivy(ctx, session.AccessToken)
	if err != nil {
		return config.Credentials{}, PrivySession{}, err
	}
	return creds, session, nil
}

// privyPost sends a JSON request with the headers Privy requires, decoding the
// response into out (out may be nil).
func privyPost(ctx context.Context, path string, payload, out any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, privyEndpoint+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("privy-app-id", privyAppID)
	req.Header.Set("privy-client", privyClient)
	req.Header.Set("privy-ca-id", uuid4())
	req.Header.Set("Origin", privyEndpoint)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", loginUserAgent)

	resp, err := loginHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		// Privy returns {"error": "..."}; surface it when present.
		msg := bytes.TrimSpace(body)
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			msg = []byte(e.Error)
		}
		return &APIError{Status: resp.StatusCode, Body: string(msg)}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// uuid4 returns a random RFC-4122 v4 UUID (for the privy-ca-id header).
func uuid4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
