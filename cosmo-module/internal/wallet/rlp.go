package wallet

import (
	"encoding/binary"
	"math/big"
)

// Minimal RLP encoding, enough for the ZKsync type-0x71 transaction: byte
// strings, integers (as minimal big-endian byte strings), and lists.

// rlpBytes encodes a byte string per RLP.
func rlpBytes(b []byte) []byte {
	// A single byte in [0x00, 0x7f] is its own encoding.
	if len(b) == 1 && b[0] <= 0x7f {
		return []byte{b[0]}
	}
	return append(rlpLenPrefix(0x80, len(b)), b...)
}

// rlpUint encodes a uint64 as a minimal big-endian byte string (0 -> empty).
func rlpUint(v uint64) []byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], v)
	return rlpBytes(trimLeadingZeros(buf[:]))
}

// rlpBig encodes a non-negative big.Int as a minimal big-endian byte string
// (Bytes already omits leading zeros; 0 -> empty).
func rlpBig(v *big.Int) []byte { return rlpBytes(v.Bytes()) }

// rlpList concatenates already-encoded items and wraps them as an RLP list.
func rlpList(items ...[]byte) []byte {
	var payload []byte
	for _, it := range items {
		payload = append(payload, it...)
	}
	return append(rlpLenPrefix(0xc0, len(payload)), payload...)
}

// rlpLenPrefix builds the length prefix for a string (base 0x80) or list (base
// 0xc0) of length n.
func rlpLenPrefix(base byte, n int) []byte {
	if n <= 55 {
		return []byte{base + byte(n)}
	}
	lenBytes := trimLeadingZeros(bigEndian(n))
	return append([]byte{base + 55 + byte(len(lenBytes))}, lenBytes...)
}

func bigEndian(n int) []byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(n))
	return buf[:]
}

func trimLeadingZeros(b []byte) []byte {
	i := 0
	for i < len(b) && b[i] == 0 {
		i++
	}
	return b[i:]
}
