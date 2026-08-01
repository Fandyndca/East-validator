package tx

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/eastchain/east-validator/internal/crypto"
)

// TxType mirrors the existing east-wallet transaction types.
type TxType string

const (
	TxTransfer       TxType = "transfer"
	TxRequestUnstake TxType = "request_unstake"
	TxClaimUnstake   TxType = "claim_unstake"
	TxStake          TxType = "stake"
	// Add more later as needed
)

// Transaction is the canonical shape accepted by the validator.
type Transaction struct {
	Type      TxType `json:"type"`
	From      string `json:"from"`      // hex address 0x...
	To        string `json:"to"`        // only for transfer
	Amount    int64  `json:"amount"`    // smallest unit, 6 decimals: 1 EAST = 1_000_000. NOT the same as the Substrate runtime's 18 decimals — int64 can't hold 1B EAST at 18 decimals (overflows ~9.2e18 max), so this validator uses 6. Any future bridge/sync between this and the Substrate chain must convert between the two.
	Nonce     uint64 `json:"nonce"`
	Timestamp int64  `json:"timestamp"` // unix ms
	Signature string `json:"signature"` // hex, EIP-191 personal_sign over BuildTxMessage(Hash()) — required, verified in ValidateBasic
	Payload   any    `json:"payload,omitempty"`
}

// Hash returns a deterministic hash of the transaction (excluding signature).
func (t *Transaction) Hash() string {
	// We hash a stable subset so signature can be verified against it later.
	type hashable struct {
		Type      TxType `json:"type"`
		From      string `json:"from"`
		To        string `json:"to"`
		Amount    int64  `json:"amount"`
		Nonce     uint64 `json:"nonce"`
		Timestamp int64  `json:"timestamp"`
	}
	h := hashable{
		Type:      t.Type,
		From:      t.From,
		To:        t.To,
		Amount:    t.Amount,
		Nonce:     t.Nonce,
		Timestamp: t.Timestamp,
	}
	b, _ := json.Marshal(h)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (t *Transaction) ValidateBasic() error {
	if t.From == "" {
		return fmt.Errorf("from address required")
	}
	if t.Amount < 0 {
		return fmt.Errorf("amount cannot be negative")
	}
	if t.Timestamp == 0 {
		t.Timestamp = time.Now().UnixMilli()
	}
	switch t.Type {
	case TxTransfer:
		if t.To == "" {
			return fmt.Errorf("to address required for transfer")
		}
		if t.Amount <= 0 {
			return fmt.Errorf("transfer amount must be > 0")
		}
	case TxRequestUnstake, TxClaimUnstake, TxStake:
		// amount rules can be refined later
	default:
		return fmt.Errorf("unknown tx type: %s", t.Type)
	}
	return t.VerifySignature()
}

// VerifySignature checks that Signature is a valid EIP-191 (personal_sign)
// signature over this transaction's hash, produced by the private key for
// From. Without this, anyone who knows API_SECRET could submit a transfer
// claiming to be any address — API_SECRET only protects "can call the
// endpoint at all", not "who this transaction is actually from".
func (t *Transaction) VerifySignature() error {
	if t.Signature == "" {
		return fmt.Errorf("signature required")
	}
	msg := crypto.BuildTxMessage(t.Hash())
	ok, err := crypto.VerifyEIP191(msg, t.Signature, t.From)
	if err != nil {
		return fmt.Errorf("signature verification failed: %w", err)
	}
	if !ok {
		return fmt.Errorf("signature does not match from address")
	}
	return nil
}

func Decode(data []byte) (*Transaction, error) {
	var t Transaction
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	if err := t.ValidateBasic(); err != nil {
		return nil, err
	}
	return &t, nil
}
