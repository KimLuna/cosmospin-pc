package cosmo

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"testing"
)

// TestEncryptRoundTrip encrypts with the real key path and manually decrypts,
// confirming the AES-256-CBC/PKCS7/base64(iv||ct) framing the app expects.
func TestEncryptRoundTrip(t *testing.T) {
	plaintext := `{"refreshToken":"abc.def.ghi"}`
	out, err := encrypt(plaintext, cosmoKey)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(out)
	if err != nil {
		t.Fatalf("output not base64: %v", err)
	}
	if len(raw) < aes.BlockSize || len(raw)%aes.BlockSize != 0 {
		t.Fatalf("ciphertext length %d not block-aligned", len(raw))
	}
	key, _ := base64.StdEncoding.DecodeString(cosmoKey)
	block, _ := aes.NewCipher(key)
	iv, ct := raw[:aes.BlockSize], raw[aes.BlockSize:]
	dec := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(dec, ct)

	// strip PKCS7 padding
	pad := int(dec[len(dec)-1])
	if pad < 1 || pad > aes.BlockSize {
		t.Fatalf("bad padding byte %d", pad)
	}
	got := dec[:len(dec)-pad]
	if !bytes.Equal(got, []byte(plaintext)) {
		t.Fatalf("round trip mismatch: got %q want %q", got, plaintext)
	}
}

// TestDecryptRoundTrip confirms decrypt reverses encrypt, i.e. it reads the
// same iv||ciphertext framing Cosmo now uses for x-cosmo-encrypted responses.
func TestDecryptRoundTrip(t *testing.T) {
	// Length that is not a multiple of the block size, to exercise padding.
	plaintext := `{"nickname":"dstk","statusMessage":null}`
	enc, err := encrypt(plaintext, cosmoKey)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	got, err := decrypt(enc, cosmoKey)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(got) != plaintext {
		t.Fatalf("round trip mismatch: got %q want %q", got, plaintext)
	}
}

// TestDecryptRejectsGarbage ensures decrypt fails cleanly on non-encrypted or
// malformed input rather than returning junk.
func TestDecryptRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "not base64!!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if _, err := decrypt(in, cosmoKey); err == nil {
			t.Errorf("decrypt(%q) = nil error, want error", in)
		}
	}
}
