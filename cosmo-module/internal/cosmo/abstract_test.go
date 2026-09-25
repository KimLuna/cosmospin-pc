package cosmo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestAbstractTxState checks the receipt reduction: a transaction Cosmo's RPC
// proxy has no receipt for is pending (not an error), the status word decides
// success from revert, and the logs come through intact — they are the only
// proof an objekt actually moved.
func TestAbstractTxState(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		want     AbstractTxStatus
		wantLogs int
	}{
		{"pending", `{"jsonrpc":"2.0","id":1,"result":null}`, TxPending, 0},
		{"success", `{"jsonrpc":"2.0","id":1,"result":{"status":"0x1","blockNumber":"0x2a"}}`, TxSuccess, 0},
		{"reverted", `{"jsonrpc":"2.0","id":1,"result":{"status":"0x0"}}`, TxReverted, 0},
		// A receipt exists, so the transaction is mined; without a status word
		// there is nothing to call a revert.
		{"no status word", `{"jsonrpc":"2.0","id":1,"result":{"blockNumber":"0x2a"}}`, TxSuccess, 0},
		{"with logs", `{"jsonrpc":"2.0","id":1,"result":{"status":"0x1","logs":[
			{"address":"0x99Bb83AE9bb0C0A6be865CaCF67760947f91Cb70","topics":["` + TransferTopic + `","0x1","0x2","0x3"]},
			{"address":"0x000000000000000000000000000000000000800A","topics":["` + TransferTopic + `","0x1","0x2"]}
		]}}`, TxSuccess, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(func(r *http.Request) (*http.Response, error) {
				if !strings.HasSuffix(r.URL.Path, "/json-rpc/abstract") {
					t.Fatalf("unexpected path %q", r.URL.Path)
				}
				return jsonResponse(tc.body), nil
			})
			got, err := c.AbstractTxState(context.Background(), "0xdeadbeef")
			if err != nil {
				t.Fatalf("AbstractTxState: %v", err)
			}
			if got.Status != tc.want {
				t.Fatalf("status = %d, want %d", got.Status, tc.want)
			}
			if len(got.Logs) != tc.wantLogs {
				t.Fatalf("logs = %d, want %d", len(got.Logs), tc.wantLogs)
			}
		})
	}
}

// TestReceiptMovedToken checks the proof an objekt changed hands: a four-topic
// Transfer on the token contract, for this tokenId, to this recipient. A
// transaction that mined without one moved nothing, which is what happened when
// Cosmo reported a contract address that has no transferFrom.
func TestReceiptMovedToken(t *testing.T) {
	const (
		token = "0x99Bb83AE9bb0C0A6be865CaCF67760947f91Cb70"
		from  = "0x3a6E4EFfEb030f950870C98834eb40f5BE9c7f2A"
		to    = "0x9CCCFc221a3CcE9090E60950EF07517bFCCc840d"
		id    = 23881050 // 0x16c655a
	)
	word := func(v string) string {
		v = strings.TrimPrefix(v, "0x")
		return "0x" + strings.Repeat("0", 64-len(v)) + v
	}
	// The gas payment on the L2 base-token contract: present on every
	// transaction, transfer or not.
	fee := AbstractLog{Address: "0x000000000000000000000000000000000000800A",
		Topics: []string{TransferTopic, word(from), word("0x8001")}}
	move := AbstractLog{Address: strings.ToLower(token),
		Topics: []string{TransferTopic, word(from), word(to), word("16c655a")}}

	cases := []struct {
		name string
		logs []AbstractLog
		want bool
	}{
		{"the transfer, among fee logs", []AbstractLog{fee, move, fee}, true},
		{"only fee logs", []AbstractLog{fee, fee}, false},
		{"no logs at all", nil, false},
		{"a different tokenId", []AbstractLog{{Address: token,
			Topics: []string{TransferTopic, word(from), word(to), word("16c655b")}}}, false},
		{"a different recipient", []AbstractLog{{Address: token,
			Topics: []string{TransferTopic, word(from), word(from), word("16c655a")}}}, false},
		{"the right id on another contract", []AbstractLog{{Address: "0xA4B37bE40F7b231Ee9574c4b16b7DDb7EAcDC99B",
			Topics: []string{TransferTopic, word(from), word(to), word("16c655a")}}}, false},
		{"a three-topic Transfer (ERC-20 shape)", []AbstractLog{{Address: token,
			Topics: []string{TransferTopic, word(from), word(to)}}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := AbstractReceipt{Status: TxSuccess, Logs: tc.logs}
			if got := r.MovedToken(token, id, to); got != tc.want {
				t.Fatalf("MovedToken = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAbstractTxStateRPCError surfaces a JSON-RPC-level error rather than
// reporting the transaction as pending forever.
func TestAbstractTxStateRPCError(t *testing.T) {
	c := testClient(func(*http.Request) (*http.Response, error) {
		return jsonResponse(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"rate limited"}}`), nil
	})
	if _, err := c.AbstractTxState(context.Background(), "0xdeadbeef"); err == nil ||
		!strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("err = %v, want the rpc error surfaced", err)
	}
}

// TestAbstractOwnerOf checks the ownerOf reduction: a 32-byte word yields the
// low 20 bytes as the holder, and an address that answers nothing yields an
// empty holder rather than an error (the caller then tries the next contract).
func TestAbstractOwnerOf(t *testing.T) {
	cases := []struct {
		name, result, want string
	}{
		{"holder", `"0x0000000000000000000000003a6e4effeb030f950870c98834eb40f5be9c7f2a"`,
			"0x3a6e4effeb030f950870c98834eb40f5be9c7f2a"},
		{"no code at the address", `"0x"`, ""},
		{"empty result", `""`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotData string
			c := testClient(func(r *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(r.Body)
				var req struct {
					Method string `json:"method"`
					Params []any  `json:"params"`
				}
				if err := json.Unmarshal(body, &req); err != nil {
					t.Fatalf("request body: %v", err)
				}
				if req.Method != "eth_call" {
					t.Fatalf("method = %q, want eth_call", req.Method)
				}
				gotData = req.Params[0].(map[string]any)["data"].(string)
				return jsonResponse(`{"jsonrpc":"2.0","id":1,"result":` + tc.result + `}`), nil
			})
			got, err := c.AbstractOwnerOf(context.Background(), "0x99Bb83AE9bb0C0A6be865CaCF67760947f91Cb70", 23881050)
			if err != nil {
				t.Fatalf("AbstractOwnerOf: %v", err)
			}
			if got != tc.want {
				t.Fatalf("holder = %q, want %q", got, tc.want)
			}
			// ownerOf(uint256) selector plus the id right-aligned in a 32-byte word.
			if want := "0x6352211e" + "00000000000000000000000000000000000000000000000000000000016c655a"; gotData != want {
				t.Fatalf("calldata = %q, want %q", gotData, want)
			}
		})
	}
}
