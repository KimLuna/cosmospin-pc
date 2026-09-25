// Package wallet reconstructs the Cosmo user's Privy embedded-wallet signing
// key and signs the on-chain objekt transfer that "sending" an objekt actually
// is: a ZKsync (Abstract) type-0x71 transaction whose AGW smart-account custom
// signature is an ECDSA signature from the Privy EOA.
//
// The whole flow is self-contained and leans on a minimal set of crypto
// primitives (decred/secp256k1 for EC, x/crypto/sha3 for keccak, stdlib
// AES-GCM/HMAC/SHA-2, go-bip39 for the mnemonic); the ZKsync/AGW byte layouts
// and RLP are hand-rolled. See the objekt-send-flow notes for the reverse
// engineering this implements.
//
// The reconstructed key controls real assets. It is held only in memory; it,
// its entropy, mnemonic, and Shamir shares must never be written to disk.
package wallet

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"golang.org/x/crypto/sha3"
)

// Address is a 20-byte Ethereum-style address.
type Address [20]byte

// ParseAddress parses a hex address, with or without the "0x" prefix.
func ParseAddress(s string) (Address, error) {
	var a Address
	h := strings.TrimPrefix(strings.TrimSpace(s), "0x")
	h = strings.TrimPrefix(h, "0X")
	b, err := hex.DecodeString(h)
	if err != nil {
		return a, fmt.Errorf("bad address %q: %w", s, err)
	}
	if len(b) != 20 {
		return a, fmt.Errorf("bad address %q: got %d bytes, want 20", s, len(b))
	}
	copy(a[:], b)
	return a, nil
}

// Hex renders the address as lowercase 0x-prefixed hex.
func (a Address) Hex() string { return "0x" + hex.EncodeToString(a[:]) }

// String implements fmt.Stringer.
func (a Address) String() string { return a.Hex() }

// Wallet holds the reconstructed Privy EOA key and signs transfers. The key is
// in-memory only.
type Wallet struct {
	key  *secp256k1.PrivateKey
	addr Address
}

// FromKey wraps a reconstructed private key as a Wallet, computing its EOA
// address.
func FromKey(key *secp256k1.PrivateKey) *Wallet {
	return &Wallet{key: key, addr: pubkeyAddress(key)}
}

// FromKeyBytes wraps a raw 32-byte private key (as persisted by KeyBytes).
func FromKeyBytes(b []byte) (*Wallet, error) {
	if len(b) != 32 {
		return nil, fmt.Errorf("wallet key: got %d bytes, want 32", len(b))
	}
	return FromKey(secp256k1.PrivKeyFromBytes(b)), nil
}

// EOA returns the wallet's signer address (the Privy embedded-wallet EOA).
func (w *Wallet) EOA() Address { return w.addr }

// KeyBytes returns the raw 32-byte private key for at-rest storage. The caller
// must protect it: it controls real on-chain assets.
func (w *Wallet) KeyBytes() []byte { return w.key.Serialize() }

// pubkeyAddress derives the Ethereum address of a key: the low 20 bytes of the
// keccak-256 of the uncompressed public key (minus its 0x04 prefix).
func pubkeyAddress(key *secp256k1.PrivateKey) Address {
	pub := key.PubKey().SerializeUncompressed() // 0x04 || X(32) || Y(32)
	h := keccak256(pub[1:])
	var a Address
	copy(a[:], h[12:])
	return a
}

// keccak256 hashes b with legacy Keccak-256 (the pre-standard variant Ethereum
// uses, distinct from SHA3-256).
func keccak256(b ...[]byte) []byte {
	h := sha3.NewLegacyKeccak256()
	for _, part := range b {
		h.Write(part)
	}
	return h.Sum(nil)
}
