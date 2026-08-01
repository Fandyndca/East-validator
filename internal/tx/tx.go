package tx

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
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
	// TxClaimMining mints from the "mining" supply bucket into From's balance.
	// Requires an oracle EIP-191 signature in payload (see ClaimMiningPayload).
	// Phase-1 bridge off Neon mintFromBucket('mining') → on-chain.
	TxClaimMining TxType = "claim_mining"
)

// SubunitsPerEAST is the on-chain balance scale (6 decimals).
// Transfer/stake Amount fields use this scale.
// claim_mining Amount is human EAST (1 = 1 EAST) to match genesis bucket caps;
// ApplyTx multiplies by SubunitsPerEAST when crediting Balance.
const SubunitsPerEAST int64 = 1_000_000

// ClaimMiningPayload is stored in Transaction.Payload for type claim_mining.
type ClaimMiningPayload struct {
	Bucket          string `json:"bucket"`           // must be "mining" for claim_mining
	EpochID         int64  `json:"epoch_id"`         // PoC / uptime epoch (informational + signed)
	OracleSignature string `json:"oracle_signature"` // EIP-191 over BuildMintMessage(...)
}

// Transaction is the canonical shape accepted by the validator.
type Transaction struct {
	Type      TxType `json:"type"`
	From      string `json:"from"`      // hex address 0x... (beneficiary for claim_mining)
	To        string `json:"to"`        // only for transfer
	Amount    int64  `json:"amount"`    // transfer/stake: 6-dec subunits; claim_mining: human EAST
	Nonce     uint64 `json:"nonce"`
	Timestamp int64  `json:"timestamp"` // unix ms
	Signature string `json:"signature"` // EIP-191 personal_sign over BuildTxMessage(Hash()) by From
	Payload   any    `json:"payload,omitempty"`
}

// Hash returns a deterministic hash of the transaction (excluding signature).
// Payload is intentionally excluded so oracle signature can sit in Payload
// without changing the user-signed hash surface — oracle is verified separately.
func (t *Transaction) Hash() string {
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

// ParseClaimMiningPayload extracts a typed payload from Transaction.Payload.
func (t *Transaction) ParseClaimMiningPayload() (*ClaimMiningPayload, error) {
	if t.Payload == nil {
		return nil, fmt.Errorf("claim_mining requires payload")
	}
	// Payload may already be map[string]any after JSON decode, or struct.
	b, err := json.Marshal(t.Payload)
	if err != nil {
		return nil, fmt.Errorf("invalid claim_mining payload: %w", err)
	}
	var p ClaimMiningPayload
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("invalid claim_mining payload: %w", err)
	}
	if p.Bucket == "" {
		p.Bucket = "mining"
	}
	if !strings.EqualFold(p.Bucket, "mining") {
		return nil, fmt.Errorf("claim_mining bucket must be \"mining\", got %q", p.Bucket)
	}
	p.Bucket = "mining"
	if p.OracleSignature == "" {
		return nil, fmt.Errorf("oracle_signature required")
	}
	return &p, nil
}

// MiningOracleAddress returns the configured oracle EVM address (env).
// Empty means claim_mining is rejected (fail-closed).
func MiningOracleAddress() string {
	return strings.TrimSpace(os.Getenv("MINING_ORACLE_ADDRESS"))
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
		if t.Amount <= 0 {
			return fmt.Errorf("%s amount must be > 0", t.Type)
		}
	case TxClaimMining:
		if t.Amount <= 0 {
			return fmt.Errorf("claim_mining amount must be > 0 (human EAST)")
		}
		// Soft upper bound per claim — prevents accidental huge oracle mistakes.
		// Bucket cap is still the hard limit in ApplyTx.
		const maxHumanPerClaim int64 = 1_000_000 // 1M EAST
		if t.Amount > maxHumanPerClaim {
			return fmt.Errorf("claim_mining amount exceeds per-claim max (%d EAST)", maxHumanPerClaim)
		}
		p, err := t.ParseClaimMiningPayload()
		if err != nil {
			return err
		}
		oracle := MiningOracleAddress()
		if oracle == "" {
			return fmt.Errorf("claim_mining disabled: MINING_ORACLE_ADDRESS not set on validator")
		}
		msg := crypto.BuildMintMessage(p.Bucket, t.From, t.Amount, t.Nonce, p.EpochID)
		ok, err := crypto.VerifyEIP191(msg, p.OracleSignature, oracle)
		if err != nil {
			return fmt.Errorf("oracle signature verification failed: %w", err)
		}
		if !ok {
			return fmt.Errorf("oracle signature does not match MINING_ORACLE_ADDRESS")
		}
	default:
		return fmt.Errorf("unknown tx type: %s", t.Type)
	}
	return t.VerifySignature()
}

// VerifySignature checks that Signature is a valid EIP-191 (personal_sign)
// signature over this transaction's hash, produced by the private key for From.
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
