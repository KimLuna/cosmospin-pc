package cosmo

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
)

// encrypt mirrors the Cosmo app's request-body encryption (cosmo-util
// lib/auth.py): AES-256-CBC with PKCS7 padding and a random IV, returning
// base64(iv || ciphertext). The app sends this as a text/plain body with the
// header x-cosmo-encrypted:1 on its login and refresh endpoints.
func encrypt(plaintext, keyB64 string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key) // 32-byte key => AES-256
	if err != nil {
		return "", err
	}
	iv := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return "", err
	}
	padded := pkcs7Pad([]byte(plaintext), aes.BlockSize)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
	return base64.StdEncoding.EncodeToString(append(iv, ct...)), nil
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	n := blockSize - len(data)%blockSize
	return append(data, bytes.Repeat([]byte{byte(n)}, n)...)
}

// decrypt reverses encrypt: it base64-decodes an iv || ciphertext blob and
// AES-256-CBC decrypts it, stripping PKCS7 padding. Cosmo uses this same
// framing (and cosmoKey) for the response bodies it marks with the header
// x-cosmo-encrypted:1.
func decrypt(ciphertextB64, keyB64 string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key) // 32-byte key => AES-256
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return nil, err
	}
	if len(raw) < aes.BlockSize || (len(raw)-aes.BlockSize)%aes.BlockSize != 0 || len(raw) == aes.BlockSize {
		return nil, errors.New("encrypted body: malformed length")
	}
	iv, ct := raw[:aes.BlockSize], raw[aes.BlockSize:]
	pt := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(pt, ct)
	return pkcs7Unpad(pt, aes.BlockSize)
}

// pkcs7Unpad removes PKCS7 padding, validating the padding bytes.
func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, errors.New("encrypted body: not block-aligned")
	}
	n := int(data[len(data)-1])
	if n < 1 || n > blockSize || n > len(data) {
		return nil, errors.New("encrypted body: bad padding")
	}
	for _, b := range data[len(data)-n:] {
		if int(b) != n {
			return nil, errors.New("encrypted body: bad padding")
		}
	}
	return data[:len(data)-n], nil
}
