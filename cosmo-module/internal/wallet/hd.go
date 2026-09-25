package wallet

// HD key derivation for the Privy embedded wallet: reconstruct the 16-byte
// BIP-39 entropy from the two Shamir shares, expand it to a mnemonic + seed,
// then BIP-32 derive the standard Ethereum account key at m/44'/60'/0'/0/0.
//
// BIP-32 is hand-rolled on top of decred/secp256k1 (point math + scalars) so
// the whole wallet leans on a single elliptic-curve implementation rather than
// pulling in go-bip32 and its own EC dependency.

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	bip39 "github.com/tyler-smith/go-bip39"
)

// hardened marks a BIP-32 child index as hardened (index >= 2^31).
const hardened uint32 = 0x80000000

// ethPath is the standard Ethereum derivation path m/44'/60'/0'/0/0 that Privy
// uses for the first embedded-wallet account.
var ethPath = []uint32{hardened + 44, hardened + 60, hardened + 0, 0, 0}

// secp256k1N is the order of the secp256k1 group, used to reduce derived
// child scalars. It is a fixed curve parameter.
var secp256k1N, _ = new(big.Int).SetString(
	"fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141", 16)

// KeyFromEntropy derives the Ethereum account key from raw BIP-39 entropy
// (16/20/24/28/32 bytes) via mnemonic -> seed -> m/44'/60'/0'/0/0.
func KeyFromEntropy(entropy []byte) (*secp256k1.PrivateKey, error) {
	mnemonic, err := bip39.NewMnemonic(entropy)
	if err != nil {
		return nil, fmt.Errorf("entropy -> mnemonic: %w", err)
	}
	return keyFromMnemonic(mnemonic)
}

// keyFromMnemonic derives the account key from a BIP-39 mnemonic (empty
// passphrase, as Privy uses).
func keyFromMnemonic(mnemonic string) (*secp256k1.PrivateKey, error) {
	seed := bip39.NewSeed(mnemonic, "")
	node, err := masterKey(seed)
	if err != nil {
		return nil, err
	}
	for _, idx := range ethPath {
		node, err = node.child(idx)
		if err != nil {
			return nil, err
		}
	}
	return secp256k1.PrivKeyFromBytes(node.key[:]), nil
}

// hdNode is one BIP-32 extended key: a 32-byte private scalar plus its chain
// code.
type hdNode struct {
	key   [32]byte
	chain [32]byte
}

// masterKey computes the BIP-32 master node from a seed.
func masterKey(seed []byte) (hdNode, error) {
	h := hmac.New(sha512.New, []byte("Bitcoin seed"))
	h.Write(seed)
	sum := h.Sum(nil)
	var n hdNode
	copy(n.key[:], sum[:32])
	copy(n.chain[:], sum[32:])
	if new(big.Int).SetBytes(n.key[:]).Sign() == 0 {
		return hdNode{}, errors.New("invalid master key (zero)")
	}
	return n, nil
}

// child derives the BIP-32 child node at index. Hardened indices sign over the
// parent private key; normal indices sign over the parent compressed pubkey.
func (n hdNode) child(index uint32) (hdNode, error) {
	h := hmac.New(sha512.New, n.chain[:])
	if index >= hardened {
		h.Write([]byte{0x00})
		h.Write(n.key[:])
	} else {
		priv := secp256k1.PrivKeyFromBytes(n.key[:])
		h.Write(priv.PubKey().SerializeCompressed())
	}
	var idx [4]byte
	binary.BigEndian.PutUint32(idx[:], index)
	h.Write(idx[:])
	sum := h.Sum(nil)

	// childKey = (IL + parentKey) mod n, with IL the left 32 bytes of the HMAC.
	il := new(big.Int).SetBytes(sum[:32])
	if il.Cmp(secp256k1N) >= 0 {
		return hdNode{}, fmt.Errorf("derive %d: IL >= n", index)
	}
	child := il.Add(il, new(big.Int).SetBytes(n.key[:]))
	child.Mod(child, secp256k1N)
	if child.Sign() == 0 {
		return hdNode{}, fmt.Errorf("derive %d: zero child key", index)
	}
	var out hdNode
	child.FillBytes(out.key[:])
	copy(out.chain[:], sum[32:])
	return out, nil
}
