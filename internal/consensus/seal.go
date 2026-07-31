package consensus

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/eastchain/east-validator/internal/crypto"
	"github.com/eastchain/east-validator/internal/state"
)

// Proposal is what a Fullnode Browser submits for the sealer to verify + seal.
type Proposal struct {
	ProposalID string   `json:"proposal_id"`
	Height     uint64   `json:"height"`
	PrevHash   string   `json:"prev_hash"`
	TxHashes   []string `json:"tx_hashes"`
	MerkleRoot string   `json:"merkle_root"`
	BlockHash  string   `json:"block_hash"`
	Timestamp  int64    `json:"timestamp"`
	Proposer   string   `json:"proposer"`  // EVM address of the fullnode browser
	Signature  string   `json:"signature"` // EIP-191 sig over BuildProposalMessage
}

type SealResult struct {
	Header    state.BlockHeader `json:"header"`
	SealerSig string            `json:"sealer_signature"`
}

// RecomputeMerkleRoot — simple ordered hash of tx hashes (Phase 1).
// Must stay in sync with whatever Fullnode Browser uses.
func RecomputeMerkleRoot(txHashes []string) string {
	if len(txHashes) == 0 {
		sum := sha256.Sum256([]byte("EMPTY"))
		return "0x" + hex.EncodeToString(sum[:])
	}
	h := sha256.New()
	for _, th := range txHashes {
		h.Write([]byte(th))
	}
	return "0x" + hex.EncodeToString(h.Sum(nil))
}

// RecomputeBlockHash mirrors a deterministic formula:
// sha256(height | prevHash | merkleRoot | timestamp | proposer)
func RecomputeBlockHash(height uint64, prevHash, merkleRoot string, ts int64, proposer string) string {
	payload := fmt.Sprintf("%d|%s|%s|%d|%s", height, prevHash, merkleRoot, ts, proposer)
	sum := sha256.Sum256([]byte(payload))
	return "0x" + hex.EncodeToString(sum[:])
}

// VerifyAndSeal checks proposer signature + hash consistency, then seals with sealer key.
func VerifyAndSeal(
	store *state.Store,
	p Proposal,
	sealerPrivKey string, // CHAIN_SIGNING_PRIVATE_KEY
) (*SealResult, error) {
	latest, err := store.GetLatestHeight()
	if err != nil {
		return nil, err
	}
	expectedHeight := latest + 1
	if p.Height != expectedHeight {
		return nil, fmt.Errorf("bad height: got %d, expected %d", p.Height, expectedHeight)
	}

	// prev hash
	var expectedPrev string
	if latest == 0 {
		expectedPrev = "GENESIS"
	} else {
		prev, err := store.GetBlock(latest)
		if err != nil {
			return nil, fmt.Errorf("load prev block: %w", err)
		}
		expectedPrev = prev.Hash
	}
	if p.PrevHash != expectedPrev {
		return nil, fmt.Errorf("prev_hash mismatch")
	}

	// recompute merkle + block hash
	merkle := RecomputeMerkleRoot(p.TxHashes)
	if p.MerkleRoot != "" && p.MerkleRoot != merkle {
		return nil, fmt.Errorf("merkle_root mismatch")
	}
	blockHash := RecomputeBlockHash(p.Height, p.PrevHash, merkle, p.Timestamp, p.Proposer)
	if p.BlockHash != "" && p.BlockHash != blockHash {
		return nil, fmt.Errorf("block_hash mismatch")
	}

	// proposer signature (EIP-191)
	msg := crypto.BuildProposalMessage(p.ProposalID, p.Height, blockHash)
	ok, err := crypto.VerifyEIP191(msg, p.Signature, p.Proposer)
	if err != nil || !ok {
		return nil, fmt.Errorf("invalid proposer signature: %v", err)
	}

	// optional: enforce min stake for proposer
	acc, _ := store.GetAccount(p.Proposer)
	if min := store.MinValidatorStake(); min > 0 && acc.Staked < min {
		return nil, fmt.Errorf("proposer stake %d < minimum %d", acc.Staked, min)
	}

	// sealer signature
	var sealerSig string
	if sealerPrivKey != "" {
		sealMsg := crypto.BuildChainSigningMessage(p.Height, blockHash)
		sealerSig, err = crypto.SignEIP191(sealMsg, sealerPrivKey)
		if err != nil {
			return nil, fmt.Errorf("sealer sign failed: %w", err)
		}
	}

	header := state.BlockHeader{
		Height:    p.Height,
		Hash:      blockHash,
		PrevHash:  p.PrevHash,
		StateRoot: "", // Phase 1 placeholder
		TxHashes:  p.TxHashes,
		Timestamp: p.Timestamp,
		Proposer:  p.Proposer,
		TxCount:   len(p.TxHashes),
		Signature: sealerSig,
	}
	if header.Timestamp == 0 {
		header.Timestamp = time.Now().UnixMilli()
	}

	if err := store.SaveBlock(header); err != nil {
		return nil, err
	}

	return &SealResult{Header: header, SealerSig: sealerSig}, nil
}

// Debug helper
func ProposalJSON(p Proposal) string {
	b, _ := json.MarshalIndent(p, "", "  ")
	return string(b)
}
