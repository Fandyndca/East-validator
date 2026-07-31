package tx

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
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
	Amount    int64  `json:"amount"`    // in smallest unit (e.g. 1 EAST = 1e6 or keep integer for now)
	Nonce     uint64 `json:"nonce"`
	Timestamp int64  `json:"timestamp"` // unix ms
	Signature string `json:"signature"` // hex, Phase 1 still optional (TODO: enforce)
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
