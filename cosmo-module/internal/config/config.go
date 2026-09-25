// Package config handles cosmo-tui's on-disk state: credential storage and
// the user's runtime configuration.
//
// Both live under the user's config directory: $XDG_CONFIG_HOME or ~/.config
// on Linux and macOS (deliberately not Apple's ~/Library/Application Support —
// terminal users expect ~/.config), %AppData% on Windows. Tokens go in a
// single 0600 auth.json (the Unix mode bits are
// ignored on Windows, but the file still lands inside the user's
// already-protected profile); options come from a plaintext "config" file
// beside it (see Options).
package config

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrNoCredentials is returned by Load when auth.json holds no usable token
// pair: it is absent, unparseable, or missing a refresh token. All three mean
// the same thing to the caller - sign in again - and a fresh login rewrites the
// file, so a corrupt one repairs itself rather than wedging startup.
var ErrNoCredentials = errors.New("no credentials stored; log in first")

// ErrNoWalletKey is returned by LoadWalletKey when no wallet key is stored.
var ErrNoWalletKey = errors.New("no wallet key stored")

// Credentials is the normalized token pair we persist. Cosmo's login/refresh
// responses nest these under a "credentials" object; see cosmo.ExtractCredentials.
type Credentials struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}

// appDir returns cosmo-tui's config directory: XDG-style everywhere except
// Windows (os.UserConfigDir would put macOS under ~/Library/Application
// Support, which terminal tools conventionally ignore in favor of ~/.config).
func appDir() (string, error) {
	if runtime.GOOS == "windows" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "cosmo-tui"), nil
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "cosmo-tui"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "cosmo-tui"), nil
}

// Path returns the auth.json location under the OS config dir.
func Path() (string, error) {
	dir, err := appDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "auth.json"), nil
}

// Load reads the stored credentials, returning ErrNoCredentials when the file
// is absent or its contents are unusable (truncated, corrupt, or lacking a
// refresh token). A read error that is not "absent" - a permission problem, or
// the path being a directory - is reported as-is: that is a broken environment
// rather than stale state, and logging in again would only fail at Save.
func Load() (Credentials, error) {
	p, err := Path()
	if err != nil {
		return Credentials{}, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return Credentials{}, ErrNoCredentials
	}
	if err != nil {
		return Credentials{}, err
	}
	var c Credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return Credentials{}, ErrNoCredentials
	}
	// A well-formed file of the wrong shape ({}, null, someone else's JSON)
	// decodes cleanly into zero values; without a refresh token there is
	// nothing to renew from, so treat it as no credentials at all.
	if c.RefreshToken == "" {
		return Credentials{}, ErrNoCredentials
	}
	return c, nil
}

// Save writes credentials to auth.json with owner-only permissions, creating
// the parent directory as needed.
func Save(c Credentials) error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	// Create with 0600 up front so the secret is never briefly world-readable.
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(c)
}

// walletKeyPath returns the wallet.key location under the OS config dir. It
// holds the reconstructed Privy signing key (hex), which controls real on-chain
// assets, so it is kept in its own 0600 file separate from auth.json.
func walletKeyPath() (string, error) {
	dir, err := appDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "wallet.key"), nil
}

// SaveWalletKey persists the raw wallet private key (hex) with owner-only
// permissions.
func SaveWalletKey(key []byte) error {
	p, err := walletKeyPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(hex.EncodeToString(key) + "\n")
	return err
}

// RemoveWalletKey deletes the stored wallet key, reporting success when there
// was nothing to delete. Used to drop a key that no longer belongs to the
// signed-in account, so a stale one cannot linger unnoticed behind the
// "sending unavailable" notice.
func RemoveWalletKey() error {
	p, err := walletKeyPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// LoadWalletKey reads the stored wallet private key, returning ErrNoWalletKey
// when none is stored yet.
func LoadWalletKey() ([]byte, error) {
	p, err := walletKeyPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoWalletKey
	}
	if err != nil {
		return nil, err
	}
	return hex.DecodeString(strings.TrimSpace(string(data)))
}
