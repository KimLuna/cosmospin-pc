package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestLoadUnusableCredentials checks that a file we cannot get a refresh token
// out of reports ErrNoCredentials, so startup falls into the sign-in flow
// instead of failing, and that a good file still round-trips.
func TestLoadUnusableCredentials(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	p, err := Path()
	if err != nil || filepath.Dir(p) != filepath.Join(dir, "cosmo-tui") {
		t.Skipf("XDG_CONFIG_HOME override not honored here (path %q, err %v)", p, err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	for _, tc := range []struct {
		name string
		data string
	}{
		{"empty", ""},
		{"truncated", `{"accessToken": "abc`},
		{"not json", "\x00\x01garbage"},
		{"empty object", `{}`},
		{"null", `null`},
		{"wrong shape", `{"foo": 1}`},
		{"access token only", `{"accessToken": "abc"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(p, []byte(tc.data), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			if _, err := Load(); !errors.Is(err, ErrNoCredentials) {
				t.Fatalf("Load(%q): err = %v, want ErrNoCredentials", tc.data, err)
			}
		})
	}

	want := Credentials{AccessToken: "access", RefreshToken: "refresh"}
	if err := Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != want {
		t.Fatalf("round-trip: got %+v, want %+v", got, want)
	}
}

// TestWalletKeyRoundTrip checks the wallet key persists to a 0600 file and reads
// back byte-for-byte, and that a missing file reports ErrNoWalletKey.
func TestWalletKeyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if got, err := walletKeyPath(); err != nil || filepath.Dir(got) != filepath.Join(dir, "cosmo-tui") {
		t.Skipf("XDG_CONFIG_HOME override not honored here (path %q, err %v)", got, err)
	}

	if _, err := LoadWalletKey(); !errors.Is(err, ErrNoWalletKey) {
		t.Fatalf("LoadWalletKey with no file: err = %v, want ErrNoWalletKey", err)
	}

	key := bytes.Repeat([]byte{0xab}, 32)
	if err := SaveWalletKey(key); err != nil {
		t.Fatalf("SaveWalletKey: %v", err)
	}

	p, _ := walletKeyPath()
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("wallet.key mode = %o, want 600", perm)
	}

	got, err := LoadWalletKey()
	if err != nil {
		t.Fatalf("LoadWalletKey: %v", err)
	}
	if !bytes.Equal(got, key) {
		t.Fatalf("round-trip mismatch: got %x, want %x", got, key)
	}
}
