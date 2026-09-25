package wallet

// Provisioning reconstructs the Privy EOA key from a live Privy session via
// Privy's managed recovery flow, so the user never has to extract a device
// share by hand. It runs once (at login, the only moment cosmo-tui holds a Privy
// session); the caller persists the resulting key and never calls this again
// unless the key file is lost.
//
// The flow (all against auth.privy.io, authorized with the Privy access token):
//  1. recovery/key_material -> a fresh recovery_key (32 bytes, base64)
//  2. recovery/shares {recovery_key_hash=sha256(recovery_key)} -> an encrypted
//     recovery share + IV, AES-256-GCM decryptable with the recovery_key
//  3. recovery/auth_share -> the auth share
//  4. Shamir-combine the two 17-byte shares -> 16-byte BIP-39 entropy -> key
//
// The recovered key must reproduce the expected EOA, which is the check that the
// whole chain (shares, GCM, Shamir, HD derivation) ran correctly.

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	privyBase   = "https://auth.privy.io"
	privyAppID  = "cm7tt02g000a96m8uislrblbe"
	privyClient = "@privy-io/js-sdk-core:0.60.7.0.1"
	// Privy sits behind Cloudflare, which bans the default Go UA.
	privyUserAgent = "okhttp/4.9.2"
)

var provisionHTTP = &http.Client{Timeout: 20 * time.Second}

// Provision reconstructs the wallet for the Privy embedded-wallet address eoa,
// using the Privy access token to drive the managed recovery flow. It verifies
// the reconstructed key matches eoa before returning.
func Provision(ctx context.Context, privyAccessToken, eoa string) (*Wallet, error) {
	want, err := ParseAddress(eoa)
	if err != nil {
		return nil, fmt.Errorf("provision: bad EOA: %w", err)
	}
	base := "/api/v1/embedded_wallets/" + want.Hex() + "/recovery"

	// 1. Fresh recovery key.
	var keyMat struct {
		RecoveryKey string `json:"recovery_key"`
	}
	if err := privyPost(ctx, privyAccessToken, base+"/key_material",
		map[string]string{"chain_type": "ethereum"}, &keyMat); err != nil {
		return nil, fmt.Errorf("provision: key_material: %w", err)
	}
	recoveryKey, err := base64.StdEncoding.DecodeString(keyMat.RecoveryKey)
	if err != nil {
		return nil, fmt.Errorf("provision: recovery_key b64: %w", err)
	}

	// 2. Encrypted recovery share, decrypted with the recovery key.
	hash := sha256.Sum256(recoveryKey)
	var shares struct {
		EncryptedShare string `json:"encrypted_recovery_share"`
		IV             string `json:"encrypted_recovery_share_iv"`
	}
	if err := privyPost(ctx, privyAccessToken, base+"/shares", map[string]string{
		"recovery_key_hash": base64.StdEncoding.EncodeToString(hash[:]),
		"chain_type":        "ethereum",
	}, &shares); err != nil {
		return nil, fmt.Errorf("provision: shares: %w", err)
	}
	recoveryShare, err := decryptShare(recoveryKey, shares.IV, shares.EncryptedShare)
	if err != nil {
		return nil, fmt.Errorf("provision: decrypt recovery share: %w", err)
	}

	// 3. Auth share.
	var authResp struct {
		Share string `json:"share"`
	}
	if err := privyPost(ctx, privyAccessToken, base+"/auth_share",
		map[string]string{"chain_type": "ethereum"}, &authResp); err != nil {
		return nil, fmt.Errorf("provision: auth_share: %w", err)
	}
	authShare, err := base64.StdEncoding.DecodeString(authResp.Share)
	if err != nil {
		return nil, fmt.Errorf("provision: auth share b64: %w", err)
	}

	// 4. Combine -> entropy -> key, and verify against the expected EOA.
	entropy := combineShares([][]byte{recoveryShare, authShare})
	key, err := KeyFromEntropy(entropy)
	if err != nil {
		return nil, fmt.Errorf("provision: derive key: %w", err)
	}
	w := FromKey(key)
	if w.EOA() != want {
		return nil, fmt.Errorf("provision: reconstructed %s, expected %s (recovery mismatch)",
			w.EOA().Hex(), want.Hex())
	}
	return w, nil
}

// decryptShare AES-256-GCM decrypts a base64 (ciphertext||tag) + base64 IV with
// the 32-byte recovery key, yielding the 17-byte Shamir share.
func decryptShare(key []byte, ivB64, ctB64 string) ([]byte, error) {
	iv, err := base64.StdEncoding.DecodeString(ivB64)
	if err != nil {
		return nil, fmt.Errorf("iv b64: %w", err)
	}
	ct, err := base64.StdEncoding.DecodeString(ctB64)
	if err != nil {
		return nil, fmt.Errorf("ciphertext b64: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCMWithNonceSize(block, len(iv))
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, iv, ct, nil)
}

// privyPost sends an authenticated JSON POST to Privy, decoding into out.
func privyPost(ctx context.Context, accessToken, path string, payload, out any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, privyBase+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("privy-app-id", privyAppID)
	req.Header.Set("privy-client", privyClient)
	req.Header.Set("privy-ca-id", uuid4())
	req.Header.Set("Origin", privyBase)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", privyUserAgent)

	resp, err := provisionHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(body))
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		return fmt.Errorf("privy %d: %s", resp.StatusCode, msg)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// uuid4 returns a random RFC-4122 v4 UUID for the privy-ca-id header.
func uuid4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
