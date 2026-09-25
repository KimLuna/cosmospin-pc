package cosmo

import "testing"

// TestEmbeddedWalletAddress checks the Privy embedded-wallet address is picked
// out of linked_accounts: the wallet_client=="privy" ethereum account wins over
// other wallets, non-wallet/non-ethereum entries are ignored, and an absent
// wallet yields "".
func TestEmbeddedWalletAddress(t *testing.T) {
	tests := []struct {
		name     string
		accounts []linkedAccount
		want     string
	}{
		{
			name: "privy wallet preferred over an external one",
			accounts: []linkedAccount{
				{Type: "email", Address: "", ChainType: ""},
				{Type: "wallet", Address: "0xEXTERNAL", ChainType: "ethereum", WalletClient: "metamask"},
				{Type: "wallet", Address: "0x0E8E", ChainType: "ethereum", WalletClient: "privy"},
			},
			want: "0x0E8E",
		},
		{
			name: "falls back to the first ethereum wallet",
			accounts: []linkedAccount{
				{Type: "wallet", Address: "0xSOLANA", ChainType: "solana", WalletClient: "privy"},
				{Type: "wallet", Address: "0xFALLBACK", ChainType: "ethereum", WalletClient: ""},
			},
			want: "0xFALLBACK",
		},
		{
			name:     "no wallet account",
			accounts: []linkedAccount{{Type: "email", Address: ""}},
			want:     "",
		},
		{
			name:     "nil",
			accounts: nil,
			want:     "",
		},
	}
	for _, tt := range tests {
		if got := embeddedWalletAddress(tt.accounts); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}
