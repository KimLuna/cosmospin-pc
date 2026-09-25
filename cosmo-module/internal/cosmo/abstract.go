package cosmo

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
)

// Abstract is the ZKsync-stack L2 that objekt NFTs live on. Sending an objekt is
// an on-chain ERC-721 transferFrom, not a REST call, and Cosmo proxies the chain
// RPC through its own authenticated endpoint. These methods expose just what the
// send flow needs: the per-copy ownership facts, the sponsored-gas parameters,
// and the JSON-RPC calls to read the nonce/gas and broadcast the signed tx.
// The signing itself lives in internal/wallet.

const (
	// AbstractChainID is Abstract mainnet's chain id (0xab5).
	AbstractChainID = 2741

	// AbstractPaymaster is Cosmo's gas-sponsoring paymaster contract on
	// Abstract. The address is constant; GasStationAbstract returns only the
	// signed, per-request paymasterInput (which carries a fresh deadline).
	AbstractPaymaster = "0xac54d831fd7f32737191515eec0a16f35013f8e5"
)

// ObjektOwnership is GET /owned-by/me/{objektId}: the on-chain facts the send
// flow needs about one owned copy. Owner is the AGW smart-account address that
// holds it (the transfer's from), and ObjektID equals the on-chain tokenId.
type ObjektOwnership struct {
	TokenAddress string
	Transferable bool
	Owner        string
	ObjektID     int64
}

// OwnedObjektDetail fetches the live ownership record for one owned objekt copy,
// used to re-check transferability and resolve the token contract right before a
// send.
func (c *Client) OwnedObjektDetail(ctx context.Context, objektID int64) (ObjektOwnership, error) {
	var wire struct {
		TokenAddress string `json:"tokenAddress"`
		Transferable bool   `json:"transferable"`
		Inventory    struct {
			Owner    string `json:"owner"`
			ObjektID int64  `json:"objektId"`
		} `json:"inventory"`
	}
	path := "/owned-by/me/" + strconv.FormatInt(objektID, 10)
	if err := c.getJSON(ctx, path, nil, &wire); err != nil {
		return ObjektOwnership{}, err
	}
	return ObjektOwnership{
		TokenAddress: wire.TokenAddress,
		Transferable: wire.Transferable,
		Owner:        wire.Inventory.Owner,
		ObjektID:     wire.Inventory.ObjektID,
	}, nil
}

// AbstractGas is GET /gas-station/abstract: the sponsored-gas parameters for one
// send. Standard is the maxFeePerGas to use (wei); PaymasterInput is the signed
// 0x-hex blob to place in the transaction's paymaster params.
type AbstractGas struct {
	Standard       int64  `json:"standard"`
	PaymasterInput string `json:"paymasterInput"`
}

// GasStationAbstract fetches the current sponsored-gas parameters.
func (c *Client) GasStationAbstract(ctx context.Context) (AbstractGas, error) {
	var out AbstractGas
	if err := c.getJSON(ctx, "/gas-station/abstract", nil, &out); err != nil {
		return AbstractGas{}, err
	}
	return out, nil
}

// --- JSON-RPC over Cosmo's authenticated Abstract proxy ---

type abstractRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type abstractRPCResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// AbstractRPC makes one JSON-RPC call against Cosmo's Abstract proxy and returns
// the raw result. A JSON-RPC-level error (200 with an {error} body) is surfaced
// as a Go error.
func (c *Client) AbstractRPC(ctx context.Context, method string, params ...any) (json.RawMessage, error) {
	if params == nil {
		params = []any{}
	}
	body := abstractRPCRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: params}
	var resp abstractRPCResponse
	if err := c.sendJSON(ctx, http.MethodPost, "/json-rpc/abstract", body, &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("abstract rpc %s: %s (code %d)", method, resp.Error.Message, resp.Error.Code)
	}
	return resp.Result, nil
}

// AbstractNonce returns the next transaction nonce for addr (eth_getTransactionCount
// at the latest block).
func (c *Client) AbstractNonce(ctx context.Context, addr string) (uint64, error) {
	res, err := c.AbstractRPC(ctx, "eth_getTransactionCount", addr, "latest")
	if err != nil {
		return 0, err
	}
	return decodeRPCUint(res)
}

// AbstractEstimateGas estimates the gas limit for a call (eth_estimateGas). data
// is 0x-hex calldata.
func (c *Client) AbstractEstimateGas(ctx context.Context, from, to, data string) (uint64, error) {
	call := map[string]string{"from": from, "to": to, "data": data}
	res, err := c.AbstractRPC(ctx, "eth_estimateGas", call, "latest")
	if err != nil {
		return 0, err
	}
	return decodeRPCUint(res)
}

// AbstractSendRawTx broadcasts a signed raw transaction (0x-hex) and returns its
// hash (eth_sendRawTransaction).
func (c *Client) AbstractSendRawTx(ctx context.Context, rawHex string) (string, error) {
	res, err := c.AbstractRPC(ctx, "eth_sendRawTransaction", rawHex)
	if err != nil {
		return "", err
	}
	var hash string
	if err := json.Unmarshal(res, &hash); err != nil {
		return "", err
	}
	return hash, nil
}

// ownerOfSelector is the ERC-721 ownerOf(uint256) function selector.
var ownerOfSelector = []byte{0x63, 0x52, 0x21, 0x1e}

// AbstractOwnerOf asks a token contract who holds a tokenId (ERC-721 ownerOf).
// An address that does not answer — no such function, or no token — reports an
// empty string rather than an error, so a caller probing several candidate
// contracts can simply take the one that answers.
//
// This is not a formality: Cosmo has been observed reporting a tokenAddress
// that does not hold the copy at all (a Cream02 Unit objekt whose token really
// lives on the main objekt contract). Calling transferFrom on such an address
// does not revert — there is no matching code to revert — so the transaction is
// mined successfully having moved nothing. Asking the chain who owns the token
// is the only reliable check.
func (c *Client) AbstractOwnerOf(ctx context.Context, tokenAddress string, tokenID int64) (string, error) {
	data := make([]byte, 0, 4+32)
	data = append(data, ownerOfSelector...)
	id := big.NewInt(tokenID).Bytes()
	data = append(data, make([]byte, 32-len(id))...)
	data = append(data, id...)

	call := map[string]string{"to": tokenAddress, "data": "0x" + hex.EncodeToString(data)}
	res, err := c.AbstractRPC(ctx, "eth_call", call, "latest")
	if err != nil {
		return "", err
	}
	var word string
	if err := json.Unmarshal(res, &word); err != nil {
		return "", err
	}
	word = strings.TrimPrefix(strings.TrimPrefix(word, "0x"), "0X")
	if len(word) < 40 {
		return "", nil // no data: the address does not implement ownerOf
	}
	return "0x" + word[len(word)-40:], nil
}

// AbstractTxStatus is what the chain knows about a broadcast transaction.
type AbstractTxStatus int

const (
	TxPending  AbstractTxStatus = iota // accepted, not yet mined (no receipt)
	TxSuccess                          // mined, receipt status 0x1
	TxReverted                         // mined, receipt status 0x0
)

// TransferTopic is the ERC-721/ERC-20 Transfer(address,address,uint256) event
// signature — keccak-256 of the canonical event string. An ERC-721 transfer logs
// it on the token contract with four topics (signature, from, to, tokenId); the
// three-topic version on the L2 base-token contract is the gas fee movement.
const TransferTopic = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

// TransferSingleTopic is the ERC-1155 TransferSingle(address,address,address,
// uint256,uint256) event signature. COMO is an ERC-1155 token, so a gravity
// vote logs this on the COMO contract with four topics (signature, operator,
// from, to) and the id/amount in the data.
const TransferSingleTopic = "0xc3d58168c5ae7397731d063d5bbf3d657854427343f4c083240f7aacaa2d0f62"

// AbstractLog is one event a transaction emitted.
type AbstractLog struct {
	Address string   // the contract that emitted it
	Topics  []string // topic[0] is the event signature
}

// AbstractReceipt is a broadcast transaction's outcome. Logs are what the
// transaction actually did, and are the only trustworthy proof that an objekt
// changed hands: a state read (ownerOf) lags the receipt through Cosmo's RPC
// proxy by long enough to report a completed transfer as not having happened.
type AbstractReceipt struct {
	Status AbstractTxStatus
	Logs   []AbstractLog
}

// AbstractTxState fetches a transaction's receipt (eth_getTransactionReceipt).
// A transaction still in the mempool has no receipt at all, which is TxPending
// rather than an error.
func (c *Client) AbstractTxState(ctx context.Context, hash string) (AbstractReceipt, error) {
	res, err := c.AbstractRPC(ctx, "eth_getTransactionReceipt", hash)
	if err != nil {
		return AbstractReceipt{}, err
	}
	if len(res) == 0 || string(res) == "null" {
		return AbstractReceipt{}, nil // TxPending
	}
	var wire struct {
		Status json.RawMessage `json:"status"`
		Logs   []struct {
			Address string   `json:"address"`
			Topics  []string `json:"topics"`
		} `json:"logs"`
	}
	if err := json.Unmarshal(res, &wire); err != nil {
		return AbstractReceipt{}, err
	}
	out := AbstractReceipt{Status: TxSuccess} // a receipt exists, so it is mined
	if len(wire.Status) > 0 {
		status, err := decodeRPCUint(wire.Status)
		if err != nil {
			return AbstractReceipt{}, err
		}
		if status == 0 {
			out.Status = TxReverted
		}
	}
	for _, l := range wire.Logs {
		out.Logs = append(out.Logs, AbstractLog{Address: l.Address, Topics: l.Topics})
	}
	return out, nil
}

// MovedToken reports whether the receipt records tokenID leaving the token
// contract for recipient — the ERC-721 Transfer event the transfer was supposed
// to cause. A transaction can be mined successfully having done nothing at all
// (transferFrom to an address with no matching code does not revert), and this
// is what tells the two apart.
func (r AbstractReceipt) MovedToken(tokenAddress string, tokenID int64, recipient string) bool {
	want := big.NewInt(tokenID)
	for _, l := range r.Logs {
		if len(l.Topics) < 4 || !strings.EqualFold(l.Address, tokenAddress) {
			continue
		}
		if !strings.EqualFold(l.Topics[0], TransferTopic) {
			continue
		}
		if !topicIsAddress(l.Topics[2], recipient) {
			continue
		}
		if id, ok := topicUint(l.Topics[3]); ok && id.Cmp(want) == 0 {
			return true
		}
	}
	return false
}

// MovedComo reports whether the receipt records COMO leaving comoAddress for
// recipient - the ERC-1155 TransferSingle a gravity vote is supposed to cause.
// As with MovedToken, this is the trustworthy proof that a vote landed: a
// transaction can mine successfully having moved nothing, and Cosmo's own
// /gravities/{id}/status stays at zero until the poll is revealed, so it cannot
// be used to confirm a vote either.
func (r AbstractReceipt) MovedComo(comoAddress, recipient string) bool {
	for _, l := range r.Logs {
		if len(l.Topics) < 4 || !strings.EqualFold(l.Address, comoAddress) {
			continue
		}
		if !strings.EqualFold(l.Topics[0], TransferSingleTopic) {
			continue
		}
		if topicIsAddress(l.Topics[3], recipient) { // topics: sig, operator, from, to
			return true
		}
	}
	return false
}

// topicIsAddress reports whether a 32-byte topic word holds the given address in
// its low 20 bytes.
func topicIsAddress(topic, addr string) bool {
	t := strings.TrimPrefix(strings.TrimPrefix(topic, "0x"), "0X")
	if len(t) < 40 {
		return false
	}
	return strings.EqualFold(t[len(t)-40:], strings.TrimPrefix(strings.TrimPrefix(addr, "0x"), "0X"))
}

// topicUint parses a topic word as an unsigned integer.
func topicUint(topic string) (*big.Int, bool) {
	t := strings.TrimPrefix(strings.TrimPrefix(topic, "0x"), "0X")
	return new(big.Int).SetString(t, 16)
}

// decodeRPCUint parses a JSON string result of the form "0x…" into a uint64.
func decodeRPCUint(res json.RawMessage) (uint64, error) {
	var hexStr string
	if err := json.Unmarshal(res, &hexStr); err != nil {
		return 0, err
	}
	hexStr = strings.TrimPrefix(strings.TrimPrefix(hexStr, "0x"), "0X")
	if hexStr == "" {
		return 0, nil
	}
	return strconv.ParseUint(hexStr, 16, 64)
}
