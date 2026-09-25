package wallet

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

// Public, secret-free test vector from the real objekt send captured on
// 2026-07-18 (objekt/tokenId 24780398). No private key is involved: these
// checks exercise the calldata/EIP-712/serialization pipeline against known
// on-chain data.
const (
	capFrom           = "0x3a6E4EFfEb030f950870C98834eb40f5BE9c7f2A"
	capTokenAddr      = "0x99Bb83AE9bb0C0A6be865CaCF67760947f91Cb70"
	capRecipient      = "0x9CCCFc221a3CcE9090E60950EF07517bFCCc840d"
	capPaymaster      = "0xac54d831fd7f32737191515eec0a16f35013f8e5"
	capEOA            = "0x0E8EBC5e3e6ec68Db336662dc2E83A737135ba48"
	capTokenID        = 24780398
	capChainID        = 2741 // Abstract mainnet, as the capture's 0x0ab5 fields read
	capNonce          = 2
	capGasLimit       = 0x03ef92
	capMaxFee         = 0x02b275d0
	capExpectDigest   = "f3df933a42af7d25bb923d7bb4eee94bf965fa4123955aaad1ae824e2c4c8150"
	capInnerSig       = "28447088df909363d878d992bed8573df2d0b21d3b5070de9d7570d643780b2d27267b6b4397466204ac862462efac623a6d13392b576b462c889a3fb5df56fb1b"
	capPaymasterInput = "8c5a3445000000000000000000000000000000000000000000000000000000000000002000000000000000000000000000000000000000000000000000000000000000e0000000000000000000000000000000000000000000000000000000006a5b0a93000000000000000000000000000000000000000000000000002386f26fc10000000000000000000000000000000000000000000000000000000000000000006000000000000000000000000000000000000000000000000000000000000000417cf4bd4ceb401deed7431d4887815858112d4cf34d20e39f499da01da8bf8f9a085af35be317d9163c0ee8045abf15b3a6e7fdc17b19e76daa1b4251ee062ae21c00000000000000000000000000000000000000000000000000000000000000"
	capRawTx          = "71f902ea02808402b275d08303ef929499bb83ae9bb0c0a6be865cacf67760947f91cb7080b86423b872dd0000000000000000000000003a6e4effeb030f950870c98834eb40f5be9c7f2a0000000000000000000000009cccfc221a3cce9090e60950ef07517bfccc840d00000000000000000000000000000000000000000000000000000000017a1e6e820ab58080820ab5943a6e4effeb030f950870c98834eb40f5be9c7f2a82c350c0b90100000000000000000000000000000000000000000000000000000000000000006000000000000000000000000074b9ae28ec45e3fa11533c7954752597c3de3e7a00000000000000000000000000000000000000000000000000000000000000e0000000000000000000000000000000000000000000000000000000000000004128447088df909363d878d992bed8573df2d0b21d3b5070de9d7570d643780b2d27267b6b4397466204ac862462efac623a6d13392b576b462c889a3fb5df56fb1b000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000f9013c94ac54d831fd7f32737191515eec0a16f35013f8e5b901248c5a3445000000000000000000000000000000000000000000000000000000000000002000000000000000000000000000000000000000000000000000000000000000e0000000000000000000000000000000000000000000000000000000006a5b0a93000000000000000000000000000000000000000000000000002386f26fc10000000000000000000000000000000000000000000000000000000000000000006000000000000000000000000000000000000000000000000000000000000000417cf4bd4ceb401deed7431d4887815858112d4cf34d20e39f499da01da8bf8f9a085af35be317d9163c0ee8045abf15b3a6e7fdc17b19e76daa1b4251ee062ae21c00000000000000000000000000000000000000000000000000000000000000"
)

func capParams(t *testing.T) TransferParams {
	t.Helper()
	from := mustParse(t, capFrom)
	to := mustParse(t, capTokenAddr)
	recipient := mustParse(t, capRecipient)
	paymaster := mustParse(t, capPaymaster)
	return TransferParams{
		ChainID:              big.NewInt(capChainID),
		Nonce:                capNonce,
		GasLimit:             capGasLimit,
		MaxFeePerGas:         big.NewInt(capMaxFee),
		MaxPriorityFeePerGas: big.NewInt(0),
		GasPerPubdata:        big.NewInt(GasPerPubdata),
		From:                 from,
		To:                   to,
		Value:                big.NewInt(0),
		Data:                 TransferCalldata(from, recipient, big.NewInt(capTokenID)),
		Paymaster:            paymaster,
		PaymasterInput:       mustHex(t, capPaymasterInput),
	}
}

func TestDigestMatchesCapture(t *testing.T) {
	got := hex.EncodeToString(capParams(t).Digest())
	if got != capExpectDigest {
		t.Fatalf("digest\n got %s\nwant %s", got, capExpectDigest)
	}
}

func TestRawTxByteForByte(t *testing.T) {
	p := capParams(t)
	raw := p.serialize(agwCustomSignature(mustHex(t, capInnerSig)))
	if got, want := hex.EncodeToString(raw), capRawTx; got != want {
		t.Fatalf("raw tx mismatch\n got %s\nwant %s", got, want)
	}
}

// TestCapturedSignatureRecovers confirms the captured inner signature recovers
// to the Privy EOA over our reproduced digest - i.e. our digest is the exact
// preimage the real wallet signed.
func TestCapturedSignatureRecovers(t *testing.T) {
	digest := capParams(t).Digest()
	sig := mustHex(t, capInnerSig)
	// r||s||v (v in 27/28) -> decred compact [v r s].
	pub, _, err := ecdsa.RecoverCompact(ethToCompact(sig), digest)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	got := pubkeyAddressFromUncompressed(pub.SerializeUncompressed())
	if want := mustParse(t, capEOA); got != want {
		t.Fatalf("recovered %s, want %s", got.Hex(), want.Hex())
	}
}

// TestSignRecoverRoundTrip signs the captured digest with a throwaway key and
// confirms our own signature recovers to that key's address (exercises the
// sign path with no secret material).
func TestSignRecoverRoundTrip(t *testing.T) {
	key, err := KeyFromEntropy(bytes.Repeat([]byte{0x11}, 16))
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	w := FromKey(key)
	digest := capParams(t).Digest()
	sig := ethSign(key, digest)
	pub, _, err := ecdsa.RecoverCompact(ethToCompact(sig), digest)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if got := pubkeyAddressFromUncompressed(pub.SerializeUncompressed()); got != w.EOA() {
		t.Fatalf("recovered %s, want %s", got.Hex(), w.EOA().Hex())
	}
}

func pubkeyAddressFromUncompressed(pub []byte) Address {
	h := keccak256(pub[1:])
	var a Address
	copy(a[:], h[12:])
	return a
}

// ethToCompact converts an r||s||v (v in 27/28) signature to decred's compact
// [v r s] recovery format.
func ethToCompact(sig []byte) []byte {
	comp := make([]byte, 65)
	comp[0] = sig[64]
	copy(comp[1:33], sig[0:32])
	copy(comp[33:65], sig[32:64])
	return comp
}

// TestVoteCalldataMatchesCapturedVote reproduces the calldata of a real gravity
// vote captured from the app (gravity 194 / poll 235, 1 COMO for "dejavu",
// tx 0x43ce01a6…). The whole argument encoding is checked byte-for-byte, which
// pins the ERC-1155 layout: the four head words, the 0xa0 offset to the tail,
// the 0xe0 payload length, and the untouched server payload. No key is needed -
// this covers only the call being signed, not the signature.
func TestVoteCalldataMatchesCapturedVote(t *testing.T) {
	from := mustAddr("0x3a6E4EFfEb030f950870C98834eb40f5BE9c7f2A") // the user's AGW account
	poll := mustAddr("0xF1A787da84af2A6e8227aD87112a21181B7b9b39") // gravity 194's poll contract

	// voteData exactly as fabricate-vote returned it: pollId 235 (0xeb), the
	// choice hash, then a 65-byte server signature.
	voteData := hexBytes(t, ""+
		"00000000000000000000000000000000000000000000000000000000000000eb"+
		"c5debb0d6374544b9ca578c4d1037fe79e51db6653bc50f325dcf96b3ccb569b"+
		"0000000000000000000000000000000000000000000000000000000000000060"+
		"0000000000000000000000000000000000000000000000000000000000000041"+
		"7d00837cb7ab1966a68db014a22d89a7ef7670e6d284823b7ab1babd8f73170e"+
		"536bc9421c9dd3684a1fcdf97597c2f37e63e4a228b319f99e7ce5f6759a6001"+
		"1b00000000000000000000000000000000000000000000000000000000000000")
	if len(voteData) != 0xe0 {
		t.Fatalf("vote payload = %d bytes, want 224", len(voteData))
	}

	want := hexBytes(t, ""+
		"f242432a"+
		"0000000000000000000000003a6e4effeb030f950870c98834eb40f5be9c7f2a"+
		"000000000000000000000000f1a787da84af2a6e8227ad87112a21181b7b9b39"+
		"0000000000000000000000000000000000000000000000000000000000000001"+
		"0000000000000000000000000000000000000000000000000000000000000001"+
		"00000000000000000000000000000000000000000000000000000000000000a0"+
		"00000000000000000000000000000000000000000000000000000000000000e0")
	want = append(want, voteData...)

	// COMO id 1 is tripleS's, read off this capture — not a universal constant;
	// the id is per-artist and comes from cosmo.Artist.ComoTokenID in real use.
	got := VoteCalldata(from, poll, big.NewInt(1), big.NewInt(1), voteData)
	if !bytes.Equal(got, want) {
		t.Fatalf("calldata mismatch\n got %x\nwant %x", got, want)
	}
	// The captured transaction's RLP length prefix was 0x01a4 = 420 bytes.
	if len(got) != 420 {
		t.Fatalf("calldata = %d bytes, want 420", len(got))
	}
}

// TestVoteCalldataPadsPayload a payload that is not a whole number of words is
// zero-padded to a word boundary, while the length word keeps the true length.
func TestVoteCalldataPadsPayload(t *testing.T) {
	a := mustAddr("0x3a6E4EFfEb030f950870C98834eb40f5BE9c7f2A")
	got := VoteCalldata(a, a, big.NewInt(1), big.NewInt(2), []byte{0xaa, 0xbb, 0xcc})
	if len(got) != 4+32*6+32 {
		t.Fatalf("calldata = %d bytes, want %d", len(got), 4+32*6+32)
	}
	lenWord := got[4+32*5 : 4+32*6]
	if lenWord[31] != 3 {
		t.Fatalf("length word = %x, want 3", lenWord)
	}
	tail := got[4+32*6:]
	if !bytes.Equal(tail[:3], []byte{0xaa, 0xbb, 0xcc}) {
		t.Fatalf("payload = %x", tail[:3])
	}
	for i, b := range tail[3:] {
		if b != 0 {
			t.Fatalf("padding byte %d = %x, want 0", i, b)
		}
	}
}

// hexBytes decodes a hex string for the test vectors above.
func hexBytes(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex: %v", err)
	}
	return b
}
