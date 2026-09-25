// Package chain broadcasts the two on-chain actions cosmo-tui takes: sending an
// objekt (an ERC-721 transferFrom) and casting a gravity vote (an ERC-1155 COMO
// transfer). Both are the same transaction underneath — a ZKsync type-0x71
// transfer signed by the user's AGW smart account and paid for by Cosmo's
// sponsoring paymaster — and differ only in the calldata they carry and in what
// their receipt has to be seen doing.
//
// So the two halves live here: Send, which prices, signs and broadcasts one of
// those transactions, and AwaitReceipt, which decides whether it did what it
// claimed. Callers keep what is theirs — resolving the contract, building the
// calldata, and wording the failures.
//
// The signing itself stays in internal/wallet, which knows nothing about the
// API; this package is where that meets the Cosmo client.
package chain

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"

	"codeberg.org/djvu/cosmo-tui/internal/cosmo"
	"codeberg.org/djvu/cosmo-tui/internal/wallet"
)

// RPC is the slice of the Cosmo client Send needs: the sponsored-gas parameters,
// and the JSON-RPC calls that price a transaction and carry it. *cosmo.Client
// satisfies it; narrowing it keeps Send testable without an HTTP client.
type RPC interface {
	GasStationAbstract(ctx context.Context) (cosmo.AbstractGas, error)
	AbstractNonce(ctx context.Context, addr string) (uint64, error)
	AbstractEstimateGas(ctx context.Context, from, to, data string) (uint64, error)
	AbstractSendRawTx(ctx context.Context, rawHex string) (string, error)
}

// ReceiptReader is the receipt read AwaitReceipt polls.
type ReceiptReader interface {
	AbstractTxState(ctx context.Context, hash string) (cosmo.AbstractReceipt, error)
}

// gasLimitFallback is used when eth_estimateGas fails; comfortably above an
// observed transferFrom cost. A 20% margin is added to whichever value is used.
const gasLimitFallback = 400000

// Send signs and broadcasts a sponsored transfer from the user's AGW account to
// the contract at to, carrying data, and returns the transaction hash.
//
// A returned hash means the transaction is on the wire, not that it did
// anything: pass it to AwaitReceipt to find out.
func Send(ctx context.Context, c RPC, w *wallet.Wallet, from, to wallet.Address, data []byte) (string, error) {
	paymaster, err := wallet.ParseAddress(cosmo.AbstractPaymaster)
	if err != nil {
		return "", err
	}
	gas, err := c.GasStationAbstract(ctx)
	if err != nil {
		return "", fmt.Errorf("gas station: %w", err)
	}
	paymasterInput, err := decodeHex(gas.PaymasterInput)
	if err != nil {
		return "", fmt.Errorf("paymaster input: %w", err)
	}
	// From the latest block, so the transaction has to be mined before the next
	// one is signed or the two would reuse a nonce.
	nonce, err := c.AbstractNonce(ctx, from.Hex())
	if err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}

	gasLimit, err := c.AbstractEstimateGas(ctx, from.Hex(), to.Hex(), "0x"+hex.EncodeToString(data))
	if err != nil {
		gasLimit = gasLimitFallback
	}
	gasLimit += gasLimit / 5 // safety margin

	raw, err := w.SignTransfer(wallet.TransferParams{
		ChainID:              big.NewInt(cosmo.AbstractChainID),
		Nonce:                nonce,
		GasLimit:             gasLimit,
		MaxFeePerGas:         big.NewInt(gas.Standard),
		MaxPriorityFeePerGas: big.NewInt(0),
		GasPerPubdata:        big.NewInt(wallet.GasPerPubdata),
		From:                 from,
		To:                   to,
		Value:                big.NewInt(0),
		Data:                 data,
		Paymaster:            paymaster,
		PaymasterInput:       paymasterInput,
	})
	if err != nil {
		return "", fmt.Errorf("sign: %w", err)
	}
	return c.AbstractSendRawTx(ctx, "0x"+hex.EncodeToString(raw))
}

// Receipt polling cadence. Variables so tests can run the loop without the waits.
var (
	PollInterval = 1500 * time.Millisecond
	PollTries    = 20 // ≈30s
)

// Proof is what a broadcast transaction has to be seen doing before it counts as
// having happened, plus how to say so when it does not. Moved reads the receipt's
// logs — the only trustworthy evidence, since a transaction can mine successfully
// having moved nothing and a state read lags the receipt. Reverted and Unproven
// are the caller's wording for the two ways that goes wrong.
type Proof struct {
	Moved    func(cosmo.AbstractReceipt) bool
	Reverted error // the transaction reverted on-chain
	Unproven error // it was mined, and its logs do not show the move
}

// AwaitReceipt waits for a broadcast transaction to be mined and reports whether
// its receipt proves p. A transaction that never surfaces within the polling
// budget is (false, nil): broadcast, unconfirmed, not a failure — the one thing
// this must never do is claim something landed that was not seen to.
func AwaitReceipt(ctx context.Context, c ReceiptReader, hash string, p Proof) (bool, error) {
	for i := 0; i < PollTries; i++ {
		time.Sleep(PollInterval)
		rec, err := c.AbstractTxState(ctx, hash)
		switch {
		case err != nil:
			continue // a transient RPC hiccup; the transaction itself is still live
		case rec.Status == cosmo.TxReverted:
			return false, p.Reverted
		case rec.Status != cosmo.TxSuccess:
			continue // not mined yet
		case p.Moved(rec):
			return true, nil
		case len(rec.Logs) == 0:
			// A receipt with no logs at all is not something this chain produces
			// (the gas payment alone logs), so treat it as a proxy that strips
			// them rather than as proof of a no-op.
			return false, nil
		default:
			return false, p.Unproven
		}
	}
	return false, nil // broadcast, but no receipt yet
}

// decodeHex parses 0x-prefixed (or bare) hex into bytes.
func decodeHex(s string) ([]byte, error) {
	return hex.DecodeString(strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X"))
}
