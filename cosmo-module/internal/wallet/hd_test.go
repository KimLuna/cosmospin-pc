package wallet

import (
	"encoding/hex"
	"testing"
)

// TestHDDerivationVector checks the full entropy -> mnemonic -> seed -> BIP-32
// path against the canonical all-zero-entropy test vector ("abandon abandon
// ... about") at m/44'/60'/0'/0/0, the same path Privy uses. Secret-free.
func TestHDDerivationVector(t *testing.T) {
	key, err := KeyFromEntropy(make([]byte, 16))
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	const wantPriv = "1ab42cc412b618bdea3a599e3c9bae199ebf030895b039e9db1e30dafb12b727"
	if got := hex.EncodeToString(key.Serialize()); got != wantPriv {
		t.Fatalf("priv key\n got %s\nwant %s", got, wantPriv)
	}
	const wantAddr = "0x9858effd232b4033e47d90003d41ec34ecaeda94"
	if got := FromKey(key).EOA().Hex(); got != wantAddr {
		t.Fatalf("address\n got %s\nwant %s", got, wantAddr)
	}
}

// TestShamirRoundTrip builds a 2-of-2 GF(256) split of a known secret by hand
// (degree-1 polynomial f(x) = secret + a*x per byte) and confirms combineShares
// recovers it - the inverse of Privy's split, matching its share layout
// [y-bytes..., x].
func TestShamirRoundTrip(t *testing.T) {
	secret := mustHex(t, "000102030405060708090a0b0c0d0e0f")
	a := mustHex(t, "1f2e3d4c5b6a798897a6b5c4d3e2f100") // arbitrary coefficients

	share := func(x byte) []byte {
		s := make([]byte, len(secret)+1)
		for i := range secret {
			s[i] = secret[i] ^ gfMul(a[i], x) // f(x) = secret_i + a_i*x
		}
		s[len(secret)] = x
		return s
	}

	got := combineShares([][]byte{share(1), share(2)})
	if !equalBytes(got, secret) {
		t.Fatalf("combine\n got %x\nwant %x", got, secret)
	}
	// Any two distinct x-coords must recover the same secret.
	got = combineShares([][]byte{share(7), share(200)})
	if !equalBytes(got, secret) {
		t.Fatalf("combine (other coords)\n got %x\nwant %x", got, secret)
	}
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- shared test helpers ---

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

func mustParse(t *testing.T, s string) Address {
	t.Helper()
	a, err := ParseAddress(s)
	if err != nil {
		t.Fatalf("bad addr %q: %v", s, err)
	}
	return a
}
