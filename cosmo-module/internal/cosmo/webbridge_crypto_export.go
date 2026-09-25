package cosmo

// WebbridgeEncryptJSON exposes the pinned Cosmo client's own request-body
// encryption to the local web bridge. The key and algorithm stay inside the
// same Cosmo package used by the existing v4 login/auth flow.
func WebbridgeEncryptJSON(_ *Client, plain []byte) (string, error) {
	return encrypt(string(plain), cosmoKey)
}
