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
	"log"
	"math/big"
	"strings"
	"time"

	"codeberg.org/djvu/cosmo-tui/internal/cosmo"
	"codeberg.org/djvu/cosmo-tui/internal/wallet"
)

type RPC interface {
	GasStationAbstract(ctx context.Context) (cosmo.AbstractGas, error)
	AbstractNonce(ctx context.Context, addr string) (uint64, error)
	AbstractEstimateGas(ctx context.Context, from, to, data string) (uint64, error)
	AbstractSendRawTx(ctx context.Context, rawHex string) (string, error)
}

type ReceiptReader interface {
	AbstractTxState(ctx context.Context, hash string) (cosmo.AbstractReceipt, error)
}

const gasLimitFallback = 400000

func Send(ctx context.Context, c RPC, w *wallet.Wallet, from, to wallet.Address, data []byte) (string, error) {
	paymaster, err := wallet.ParseAddress(cosmo.AbstractPaymaster)
	if err != nil {
		return "", err
	}
	gas, err := c.GasStationAbstract(ctx)
	if err != nil {
		log.Printf("[CHAIN] stage=gas-station-error error=%v", err)
		return "", fmt.Errorf("gas station: %w", err)
	}
	log.Printf("[CHAIN] stage=gas-station-ok standard=%d paymasterInputPresent=%t", gas.Standard, strings.TrimSpace(gas.PaymasterInput) != "")
	paymasterInput, err := decodeHex(gas.PaymasterInput)
	if err != nil {
		log.Printf("[CHAIN] stage=paymaster-input-error error=%v", err)
		return "", fmt.Errorf("paymaster input: %w", err)
	}
	nonce, err := c.AbstractNonce(ctx, from.Hex())
	if err != nil {
		log.Printf("[CHAIN] stage=nonce-error error=%v", err)
		return "", fmt.Errorf("nonce: %w", err)
	}
	log.Printf("[CHAIN] stage=nonce-ok nonce=%d", nonce)

	gasLimit, err := c.AbstractEstimateGas(ctx, from.Hex(), to.Hex(), "0x"+hex.EncodeToString(data))
	if err != nil {
		log.Printf("[CHAIN] stage=estimate-gas-fallback fallback=%d error=%v", gasLimitFallback, err)
		gasLimit = gasLimitFallback
	} else {
		log.Printf("[CHAIN] stage=estimate-gas-ok estimated=%d", gasLimit)
	}
	gasLimit += gasLimit / 5
	log.Printf("[CHAIN] stage=gas-limit final=%d", gasLimit)

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
		log.Printf("[CHAIN] stage=sign-error error=%v", err)
		return "", fmt.Errorf("sign: %w", err)
	}
	hash, err := c.AbstractSendRawTx(ctx, "0x"+hex.EncodeToString(raw))
	if err != nil {
		log.Printf("[CHAIN] stage=broadcast-error error=%v", err)
		return "", err
	}
	log.Printf("[CHAIN] stage=broadcast-ok txHash=%s", hash)
	return hash, nil
}

var (
	PollInterval = 1500 * time.Millisecond
	PollTries    = 20
)

type Proof struct {
	Moved    func(cosmo.AbstractReceipt) bool
	Reverted error
	Unproven error
}

func AwaitReceipt(ctx context.Context, c ReceiptReader, hash string, p Proof) (bool, error) {
	var lastRPCError error
	for i := 0; i < PollTries; i++ {
		time.Sleep(PollInterval)
		rec, err := c.AbstractTxState(ctx, hash)
		switch {
		case err != nil:
			lastRPCError = err
			if i == 0 || i == PollTries-1 {
				log.Printf("[CHAIN] stage=receipt-rpc-error txHash=%s try=%d error=%v", hash, i+1, err)
			}
			continue
		case rec.Status == cosmo.TxReverted:
			log.Printf("[CHAIN] stage=receipt-reverted txHash=%s try=%d", hash, i+1)
			return false, p.Reverted
		case rec.Status != cosmo.TxSuccess:
			continue
		case p.Moved(rec):
			log.Printf("[CHAIN] stage=receipt-proven txHash=%s try=%d logs=%d", hash, i+1, len(rec.Logs))
			return true, nil
		case len(rec.Logs) == 0:
			log.Printf("[CHAIN] stage=receipt-success-no-logs txHash=%s try=%d", hash, i+1)
			return false, nil
		default:
			log.Printf("[CHAIN] stage=receipt-unproven txHash=%s try=%d logs=%d", hash, i+1, len(rec.Logs))
			return false, p.Unproven
		}
	}
	if lastRPCError != nil {
		log.Printf("[CHAIN] stage=receipt-timeout txHash=%s lastRPCError=%v", hash, lastRPCError)
	} else {
		log.Printf("[CHAIN] stage=receipt-timeout txHash=%s", hash)
	}
	return false, nil
}

func decodeHex(s string) ([]byte, error) {
	return hex.DecodeString(strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X"))
}