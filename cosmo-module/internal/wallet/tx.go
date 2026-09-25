package wallet

import (
	"math/big"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

// ZKsync/Abstract + AGW constants. These are fixed protocol/deployment values
// (see objekt-send-flow); the per-request paymaster input and gas come from the
// Cosmo gas-station endpoint.
const (
	zkTxType = 0x71 // ZKsync EIP-712 transaction type byte

	// GasPerPubdata is the gasPerPubdataByteLimit every transaction here is
	// signed with. It is a TransferParams field rather than something serialize
	// reaches for, since it is part of what the signature commits to — but there
	// is one right value, so callers should pass this rather than a literal.
	GasPerPubdata = 50000
)

// transferFromSelector is the 4-byte selector of ERC-721 transferFrom(address,address,uint256).
var transferFromSelector = []byte{0x23, 0xb8, 0x72, 0xdd}

// safeTransferFromSelector is the 4-byte selector of ERC-1155
// safeTransferFrom(address,address,uint256,uint256,bytes), the call a gravity
// vote is made of: COMO is an ERC-1155 token, and voting transfers it to the
// gravity's poll contract with the server-issued vote payload as the trailing
// bytes. The poll contract reads that payload in its receive hook to record
// which candidate the COMO backed.
var safeTransferFromSelector = []byte{0xf2, 0x42, 0x43, 0x2a}

// ComoToken is the COMO ERC-1155 contract on Abstract. Voting calls
// safeTransferFrom on it.
//
// The token id to spend is deliberately not a constant here: every artist's
// COMO is a different id within this one contract, so it belongs to the artist
// record and reaches VoteCalldata from cosmo.Artist.ComoTokenID. See the note
// there — assuming an id would spend the wrong artist's COMO.
var ComoToken = mustAddr("0xd0ee3ba23a384a8eefd43f33a957ded60ed12706")

// agwValidator is the AGW k1-owner validator module on Abstract (a deployed
// contract shared across AGW accounts) named in the modular custom signature.
var agwValidator = mustAddr("0x74b9ae28ec45e3fa11533c7954752597c3de3e7a")

func mustAddr(s string) Address {
	a, err := ParseAddress(s)
	if err != nil {
		panic(err)
	}
	return a
}

// TransferParams is everything needed to build one objekt-transfer transaction.
// From is the AGW smart account (sender and token owner); To is the objekt
// ERC-721 contract. Gas, nonce, and paymaster input are sourced per-send.
type TransferParams struct {
	ChainID              *big.Int
	Nonce                uint64
	GasLimit             uint64
	MaxFeePerGas         *big.Int
	MaxPriorityFeePerGas *big.Int
	GasPerPubdata        *big.Int
	From                 Address
	To                   Address
	Value                *big.Int
	Data                 []byte
	Paymaster            Address
	PaymasterInput       []byte
}

// TransferCalldata builds ERC-721 transferFrom(owner, recipient, tokenID)
// calldata.
func TransferCalldata(owner, recipient Address, tokenID *big.Int) []byte {
	out := make([]byte, 0, 4+96)
	out = append(out, transferFromSelector...)
	out = append(out, leftPad32(owner[:])...)
	out = append(out, leftPad32(recipient[:])...)
	out = append(out, leftPad32(tokenID.Bytes())...)
	return out
}

// VoteCalldata builds the ERC-1155 safeTransferFrom(from, poll, id, amount,
// voteData) calldata that casts a gravity vote: it moves amount COMO from the
// user's AGW account to the gravity's poll contract, carrying voteData - the
// opaque, server-signed payload from Cosmo's fabricate-vote endpoint, which
// names the candidate and authorizes the ballot. voteData is passed through
// untouched; the client cannot mint a valid one itself.
//
// The layout is the standard ABI encoding of (address,address,uint256,uint256,
// bytes): four head words, a word holding the offset to the tail, then the
// byte string's length and its word-padded contents.
func VoteCalldata(from, poll Address, tokenID, amount *big.Int, voteData []byte) []byte {
	const headWords = 5 // from, poll, id, amount, offset
	padded := (len(voteData) + 31) / 32 * 32

	out := make([]byte, 0, 4+32*(headWords+1)+padded)
	out = append(out, safeTransferFromSelector...)
	out = append(out, leftPad32(from[:])...)
	out = append(out, leftPad32(poll[:])...)
	out = append(out, leftPad32(tokenID.Bytes())...)
	out = append(out, leftPad32(amount.Bytes())...)
	// Offset from the start of the arguments to the bytes tail.
	out = append(out, leftPad32(big.NewInt(int64(32*headWords)).Bytes())...)
	out = append(out, leftPad32(big.NewInt(int64(len(voteData))).Bytes())...)
	out = append(out, voteData...)
	out = append(out, make([]byte, padded-len(voteData))...)
	return out
}

// EIP-712 type hashes for the ZKsync transaction domain and message. These are
// keccak-256 of the canonical type strings.
var (
	domainTypeHash = keccak256([]byte("EIP712Domain(string name,string version,uint256 chainId)"))
	txTypeHash     = keccak256([]byte("Transaction(uint256 txType,uint256 from,uint256 to,uint256 gasLimit,uint256 gasPerPubdataByteLimit,uint256 maxFeePerGas,uint256 maxPriorityFeePerGas,uint256 paymaster,uint256 nonce,uint256 value,bytes data,bytes32[] factoryDeps,bytes paymasterInput)"))
)

// Digest computes the ZKsync EIP-712 signing hash for the transaction:
// keccak256(0x1901 || domainSeparator || structHash).
func (p TransferParams) Digest() []byte {
	domainSep := keccak256(
		domainTypeHash,
		keccak256([]byte("zkSync")),
		keccak256([]byte("2")),
		u256(p.ChainID),
	)
	structHash := keccak256(
		txTypeHash,
		u256(big.NewInt(zkTxType)),
		u256Addr(p.From),
		u256Addr(p.To),
		u256(new(big.Int).SetUint64(p.GasLimit)),
		u256(p.GasPerPubdata),
		u256(p.MaxFeePerGas),
		u256(p.MaxPriorityFeePerGas),
		u256Addr(p.Paymaster),
		u256(new(big.Int).SetUint64(p.Nonce)),
		u256(p.Value),
		keccak256(p.Data),
		keccak256(nil), // empty bytes32[] factoryDeps
		keccak256(p.PaymasterInput),
	)
	return keccak256([]byte{0x19, 0x01}, domainSep, structHash)
}

// Sign signs the transaction and returns the serialized raw ZKsync type-0x71
// transaction ready for eth_sendRawTransaction.
func (w *Wallet) SignTransfer(p TransferParams) ([]byte, error) {
	sig := ethSign(w.key, p.Digest())
	return p.serialize(agwCustomSignature(sig)), nil
}

// ethSign signs digest and returns a 65-byte r||s||v signature with v in
// {27,28}, the layout Ethereum tooling expects.
func ethSign(key *secp256k1.PrivateKey, digest []byte) []byte {
	// decred's SignCompact returns [v(1) r(32) s(32)] with v = 27 + recovery
	// code (low-S canonical, deterministic RFC-6979). Reorder to r||s||v.
	comp := ecdsa.SignCompact(key, digest, false)
	out := make([]byte, 65)
	copy(out[0:32], comp[1:33])
	copy(out[32:64], comp[33:65])
	out[64] = comp[0]
	return out
}

// agwCustomSignature encodes the AGW modular-account custom signature:
// abi.encode(bytes signature, address validator, bytes hookData) with an empty
// hookData. For a 65-byte signature this is a fixed 256-byte blob.
func agwCustomSignature(sig []byte) []byte {
	out := make([]byte, 256)
	// head: three words - offset(sig)=0x60, validator, offset(hookData)=0xe0.
	out[31] = 0x60
	copy(out[32:64], leftPad32(agwValidator[:]))
	out[95] = 0xe0
	// sig: length word (0x41) then the 65 bytes, zero-padded to a word boundary.
	out[127] = 0x41
	copy(out[128:193], sig)
	// hookData: length word 0 at [224:256] (already zero).
	return out
}

// serialize produces the signed raw transaction: 0x71 || RLP(fields). The field
// order mirrors the ZKsync type-0x71 layout; r and s are empty for EIP-712
// signed txs (the signature travels in customSignature).
func (p TransferParams) serialize(customSig []byte) []byte {
	body := rlpList(
		rlpUint(p.Nonce),
		rlpBig(p.MaxPriorityFeePerGas),
		rlpBig(p.MaxFeePerGas),
		rlpUint(p.GasLimit),
		rlpBytes(p.To[:]),
		rlpBig(p.Value),
		rlpBytes(p.Data),
		rlpBig(p.ChainID),
		rlpBytes(nil), // r
		rlpBytes(nil), // s
		rlpBig(p.ChainID),
		rlpBytes(p.From[:]),
		rlpBig(p.GasPerPubdata),
		rlpList(), // factoryDeps
		rlpBytes(customSig),
		rlpList(rlpBytes(p.Paymaster[:]), rlpBytes(p.PaymasterInput)),
	)
	return append([]byte{zkTxType}, body...)
}

// u256 left-pads a non-negative big.Int to a 32-byte big-endian word.
func u256(v *big.Int) []byte { return leftPad32(v.Bytes()) }

// u256Addr encodes an address as a left-padded 32-byte word (its uint256 form).
func u256Addr(a Address) []byte { return leftPad32(a[:]) }

// leftPad32 left-pads b (<=32 bytes) into a fresh 32-byte word.
func leftPad32(b []byte) []byte {
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}
