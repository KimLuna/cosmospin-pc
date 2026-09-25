package chain

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"codeberg.org/djvu/cosmo-tui/internal/cosmo"
	"codeberg.org/djvu/cosmo-tui/internal/wallet"
)

// fakeRPC records what Send asked the chain and serves canned answers.
type fakeRPC struct {
	gas      cosmo.AbstractGas
	gasErr   error
	nonce    uint64
	estimate uint64
	estErr   error
	sendErr  error

	gotNonceAddr         string
	gotEstFrom, gotEstTo string
	gotEstData           string
	gotRaw               string
}

func (f *fakeRPC) GasStationAbstract(context.Context) (cosmo.AbstractGas, error) {
	return f.gas, f.gasErr
}

func (f *fakeRPC) AbstractNonce(_ context.Context, addr string) (uint64, error) {
	f.gotNonceAddr = addr
	return f.nonce, nil
}

func (f *fakeRPC) AbstractEstimateGas(_ context.Context, from, to, data string) (uint64, error) {
	f.gotEstFrom, f.gotEstTo, f.gotEstData = from, to, data
	return f.estimate, f.estErr
}

func (f *fakeRPC) AbstractSendRawTx(_ context.Context, raw string) (string, error) {
	f.gotRaw = raw
	if f.sendErr != nil {
		return "", f.sendErr
	}
	return "0xdeadbeef", nil
}

func testWallet(t *testing.T) *wallet.Wallet {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	w, err := wallet.FromKeyBytes(key)
	if err != nil {
		t.Fatalf("wallet: %v", err)
	}
	return w
}

func addrs(t *testing.T) (from, to wallet.Address) {
	t.Helper()
	from, err := wallet.ParseAddress("0x3a6E4EFfEb030f950870C98834eb40f5BE9c7f2A")
	if err != nil {
		t.Fatal(err)
	}
	to, err = wallet.ParseAddress("0x99Bb83AE9bb0C0A6be865CaCF67760947f91Cb70")
	if err != nil {
		t.Fatal(err)
	}
	return from, to
}

// okGas is a gas-station answer with a well-formed paymaster input.
func okGas() cosmo.AbstractGas {
	return cosmo.AbstractGas{Standard: 45000000, PaymasterInput: "0x8c5a3445"}
}

// TestSendSignsAndBroadcasts walks the happy path and checks Send priced the
// call against the right addresses and put a signed 0x71 transaction on the wire.
func TestSendSignsAndBroadcasts(t *testing.T) {
	from, to := addrs(t)
	rpc := &fakeRPC{gas: okGas(), nonce: 7, estimate: 100000}

	hash, err := Send(context.Background(), rpc, testWallet(t), from, to, []byte{0xde, 0xad})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if hash != "0xdeadbeef" {
		t.Fatalf("hash = %q", hash)
	}
	// The nonce and the estimate must be for the sending account and the target
	// contract; a mismatch here signs against the wrong account's nonce.
	if rpc.gotNonceAddr != from.Hex() {
		t.Errorf("nonce asked for %q, want %q", rpc.gotNonceAddr, from.Hex())
	}
	if rpc.gotEstFrom != from.Hex() || rpc.gotEstTo != to.Hex() {
		t.Errorf("estimate for %s -> %s, want %s -> %s",
			rpc.gotEstFrom, rpc.gotEstTo, from.Hex(), to.Hex())
	}
	if rpc.gotEstData != "0xdead" {
		t.Errorf("estimate data = %q, want the calldata", rpc.gotEstData)
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(rpc.gotRaw, "0x"))
	if err != nil || len(raw) == 0 {
		t.Fatalf("broadcast %q is not hex: %v", rpc.gotRaw, err)
	}
	if raw[0] != 0x71 {
		t.Errorf("broadcast type byte = %#x, want 0x71", raw[0])
	}
}

// TestSendFallsBackOnEstimateFailure a failed estimate must not fail the send:
// the fallback limit is used so the transfer still goes out.
func TestSendFallsBackOnEstimateFailure(t *testing.T) {
	from, to := addrs(t)
	rpc := &fakeRPC{gas: okGas(), estErr: errors.New("estimate unavailable")}

	if _, err := Send(context.Background(), rpc, testWallet(t), from, to, []byte{0x01}); err != nil {
		t.Fatalf("Send should survive a failed estimate, got %v", err)
	}
	if rpc.gotRaw == "" {
		t.Fatal("nothing was broadcast")
	}
}

// TestSendStopsOnGasStationFailure without the sponsored-gas parameters there is
// nothing to sign, so the send must stop rather than broadcast something unpaid.
func TestSendStopsOnGasStationFailure(t *testing.T) {
	from, to := addrs(t)
	rpc := &fakeRPC{gasErr: errors.New("503")}

	_, err := Send(context.Background(), rpc, testWallet(t), from, to, []byte{0x01})
	if err == nil || !strings.Contains(err.Error(), "gas station") {
		t.Fatalf("err = %v, want a gas-station failure", err)
	}
	if rpc.gotRaw != "" {
		t.Fatal("nothing should be broadcast when the gas parameters are missing")
	}
}

// TestSendRejectsBadPaymasterInput a paymaster blob that is not hex would be
// signed over as garbage, so it has to stop the send.
func TestSendRejectsBadPaymasterInput(t *testing.T) {
	from, to := addrs(t)
	rpc := &fakeRPC{gas: cosmo.AbstractGas{Standard: 1, PaymasterInput: "nothex"}}

	_, err := Send(context.Background(), rpc, testWallet(t), from, to, []byte{0x01})
	if err == nil || !strings.Contains(err.Error(), "paymaster input") {
		t.Fatalf("err = %v, want a paymaster-input failure", err)
	}
	if rpc.gotRaw != "" {
		t.Fatal("nothing should be broadcast with an unusable paymaster input")
	}
}

// receipts serves a scripted sequence of receipts, then pending.
type receipts struct {
	seq   []cosmo.AbstractReceipt
	err   error
	calls int
}

func (r *receipts) AbstractTxState(context.Context, string) (cosmo.AbstractReceipt, error) {
	if r.err != nil {
		return cosmo.AbstractReceipt{}, r.err
	}
	if r.calls >= len(r.seq) {
		return cosmo.AbstractReceipt{}, nil // pending
	}
	rec := r.seq[r.calls]
	r.calls++
	return rec, nil
}

// alwaysMoved / neverMoved stand in for a caller's log check.
func alwaysMoved(cosmo.AbstractReceipt) bool { return true }
func neverMoved(cosmo.AbstractReceipt) bool  { return false }

func testProof(moved func(cosmo.AbstractReceipt) bool) Proof {
	return Proof{
		Moved:    moved,
		Reverted: errors.New("reverted"),
		Unproven: errors.New("mined but moved nothing"),
	}
}

// TestAwaitReceiptClassifies pins the outcome for each shape of receipt. The
// distinction that matters most is the last two: a mined transaction that moved
// nothing is a failure, but a receipt stripped of its logs is merely unproven.
func TestAwaitReceiptClassifies(t *testing.T) {
	interval, tries := PollInterval, PollTries
	PollInterval, PollTries = 0, 3
	t.Cleanup(func() { PollInterval, PollTries = interval, tries })

	logged := []cosmo.AbstractLog{{Address: "0x1", Topics: []string{"0x2"}}}

	cases := []struct {
		name      string
		c         ReceiptReader
		moved     func(cosmo.AbstractReceipt) bool
		confirmed bool
		wantErr   string
	}{
		{
			name:      "the move is logged",
			c:         &receipts{seq: []cosmo.AbstractReceipt{{Status: cosmo.TxSuccess, Logs: logged}}},
			moved:     alwaysMoved,
			confirmed: true,
		},
		{
			name:    "reverted",
			c:       &receipts{seq: []cosmo.AbstractReceipt{{Status: cosmo.TxReverted}}},
			moved:   alwaysMoved,
			wantErr: "reverted",
		},
		{
			name:    "mined with logs but no move",
			c:       &receipts{seq: []cosmo.AbstractReceipt{{Status: cosmo.TxSuccess, Logs: logged}}},
			moved:   neverMoved,
			wantErr: "mined but moved nothing",
		},
		{
			// A proxy that strips logs must not turn every send into a failure.
			name:  "mined with no logs at all",
			c:     &receipts{seq: []cosmo.AbstractReceipt{{Status: cosmo.TxSuccess}}},
			moved: neverMoved,
		},
		{
			// Broadcast but never surfaced: unconfirmed, not failed.
			name:  "never mined",
			c:     &receipts{},
			moved: alwaysMoved,
		},
		{
			// Transient RPC errors are retried, not surfaced.
			name:  "rpc keeps erroring",
			c:     &receipts{err: errors.New("hiccup")},
			moved: alwaysMoved,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			confirmed, err := AwaitReceipt(context.Background(), tc.c, "0xabc", testProof(tc.moved))
			if confirmed != tc.confirmed {
				t.Errorf("confirmed = %v, want %v", confirmed, tc.confirmed)
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("unexpected error %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("err = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestAwaitReceiptWaitsForTheReceipt a transaction pending on the first polls is
// confirmed once its receipt turns up, rather than given up on immediately.
func TestAwaitReceiptWaitsForTheReceipt(t *testing.T) {
	interval, tries := PollInterval, PollTries
	PollInterval, PollTries = 0, 5
	t.Cleanup(func() { PollInterval, PollTries = interval, tries })

	c := &receipts{seq: []cosmo.AbstractReceipt{
		{},                        // pending
		{},                        // still pending
		{Status: cosmo.TxSuccess}, // mined
	}}
	confirmed, err := AwaitReceipt(context.Background(), c, "0xabc", testProof(alwaysMoved))
	if !confirmed || err != nil {
		t.Fatalf("confirmed=%v err=%v, want a confirmed move", confirmed, err)
	}
}
