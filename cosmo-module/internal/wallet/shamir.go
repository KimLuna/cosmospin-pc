package wallet

// GF(256) Shamir secret-sharing reconstruction, matching the
// privy-io/shamir-secret-sharing wire format: each share is
// [y0..y_{n-1}, x] where the final byte is the x-coordinate. The field is
// GF(2^8) with the AES reduction polynomial 0x11b.

var (
	gfExp [512]byte
	gfLog [256]byte
)

func init() {
	x := byte(1)
	for i := 0; i < 255; i++ {
		gfExp[i] = x
		gfLog[x] = byte(i)
		x ^= gfMulNoLog(x, 2)
	}
	for i := 255; i < 512; i++ {
		gfExp[i] = gfExp[i-255]
	}
}

// gfMulNoLog multiplies in GF(2^8) by carryless multiply + reduction, used only
// to seed the log/exp tables before they exist.
func gfMulNoLog(a, b byte) byte {
	var p byte
	for i := 0; i < 8; i++ {
		if b&1 != 0 {
			p ^= a
		}
		hi := a & 0x80
		a <<= 1
		if hi != 0 {
			a ^= 0x1b
		}
		b >>= 1
	}
	return p
}

func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[int(gfLog[a])+int(gfLog[b])]
}

func gfDiv(a, b byte) byte {
	if a == 0 {
		return 0
	}
	return gfExp[(int(gfLog[a])-int(gfLog[b])+255)%255]
}

// interpolateAt0 evaluates the Lagrange interpolation of the points (xs, ys) at
// x=0, recovering the constant term (the secret byte).
func interpolateAt0(xs, ys []byte) byte {
	var secret byte
	for i := range xs {
		num, den := byte(1), byte(1)
		for j := range xs {
			if i == j {
				continue
			}
			num = gfMul(num, xs[j])       // (0 - xj) == xj in GF(2^8)
			den = gfMul(den, xs[i]^xs[j]) // (xi - xj) == xi ^ xj
		}
		secret ^= gfMul(ys[i], gfDiv(num, den))
	}
	return secret
}

// combineShares reconstructs the secret from Shamir shares. Each share is
// [y-bytes..., x]; all shares must share the same length.
func combineShares(shares [][]byte) []byte {
	secretLen := len(shares[0]) - 1
	xs := make([]byte, len(shares))
	for i, s := range shares {
		xs[i] = s[len(s)-1]
	}
	out := make([]byte, secretLen)
	ys := make([]byte, len(shares))
	for b := 0; b < secretLen; b++ {
		for i, s := range shares {
			ys[i] = s[b]
		}
		out[b] = interpolateAt0(xs, ys)
	}
	return out
}
