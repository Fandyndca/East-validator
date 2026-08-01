package consensus

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/eastchain/east-validator/internal/crypto"
	"github.com/eastchain/east-validator/internal/state"
	"github.com/eastchain/east-validator/internal/tx"
)

type SealResult struct {
	Header    state.BlockHeader `json:"header"`
	SealerSig string            `json:"sealer_signature"`
	Txs       []*tx.Transaction `json:"-"`
}

// RecomputeMerkleRoot — simple ordered hash of tx hashes (Phase 1).
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

// VerifySealerSignature checks EIP-191 EASTCHAIN_BLOCK|{height}|{hash} against proposer.
func VerifySealerSignature(height uint64, blockHash, signature, proposer string) error {
	if signature == "" {
		return fmt.Errorf("missing sealer signature")
	}
	if proposer == "" {
		return fmt.Errorf("missing proposer")
	}
	msg := crypto.BuildChainSigningMessage(height, blockHash)
	ok, err := crypto.VerifyEIP191(msg, signature, proposer)
	if err != nil {
		return fmt.Errorf("sealer sig verify: %w", err)
	}
	if !ok {
		return fmt.Errorf("sealer signature does not match proposer %s", proposer)
	}
	return nil
}

// GossipBlockInput bundles fields needed to validate a gossiped block (P0 hardened).
type GossipBlockInput struct {
	Height    uint64
	Hash      string
	PrevHash  string
	Timestamp int64
	Proposer  string
	Signature string
	Txs       []*tx.Transaction
	// AllowedProposers — if non-empty, proposer must be in this set (validator set).
	AllowedProposers []string
}

// VerifyAndSaveGossipedBlock validates a block received via P2P before
// persisting and replaying txs. P0 checks:
//  1. height == tip+1, prevHash links
//  2. merkle + block hash recompute
//  3. sealer EIP-191 signature matches proposer
//  4. proposer is in the allowed validator set (when provided)
//  5. each tx passes ValidateBasic (signature + type rules)
//  6. apply txs; failure of any tx aborts the block (no partial apply)
func VerifyAndSaveGossipedBlock(store *state.Store, height uint64, hash, prevHash string, timestamp int64, proposer string, txs []*tx.Transaction) error {
	return VerifyAndSaveGossipedBlockStrict(store, GossipBlockInput{
		Height:    height,
		Hash:      hash,
		PrevHash:  prevHash,
		Timestamp: timestamp,
		Proposer:  proposer,
		Txs:       txs,
	})
}

// VerifyAndSaveGossipedBlockStrict is the full P0 path including sealer signature.
func VerifyAndSaveGossipedBlockStrict(store *state.Store, in GossipBlockInput) error {
	latest, err := store.GetLatestHeight()
	if err != nil {
		return err
	}
	if in.Height != latest+1 {
		return fmt.Errorf("gossiped block height %d != expected %d — ignoring (stale or out of order)", in.Height, latest+1)
	}

	var expectedPrev string
	if latest == 0 {
		expectedPrev = "GENESIS"
	} else {
		prev, err := store.GetBlock(latest)
		if err != nil {
			return fmt.Errorf("load prev block: %w", err)
		}
		expectedPrev = prev.Hash
	}
	if in.PrevHash != expectedPrev {
		return fmt.Errorf("gossiped block prev_hash mismatch: got %s, expected %s", in.PrevHash, expectedPrev)
	}

	// Proposer must be an active validator when a set is provided.
	if len(in.AllowedProposers) > 0 {
		ok := false
		for _, a := range in.AllowedProposers {
			if strings.EqualFold(a, in.Proposer) {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("proposer %s not in validator set", in.Proposer)
		}
	}

	// Reject jailed proposers.
	if jailed, _, err := store.IsJailed(in.Proposer); err == nil && jailed {
		return fmt.Errorf("proposer %s is jailed", in.Proposer)
	}

	txHashes := make([]string, 0, len(in.Txs))
	for _, t := range in.Txs {
		if t == nil {
			return fmt.Errorf("nil tx in gossiped block")
		}
		// Re-verify every tx signature / oracle rules before applying.
		if err := t.ValidateBasic(); err != nil {
			return fmt.Errorf("tx %s invalid: %w", t.Hash(), err)
		}
		txHashes = append(txHashes, "0x"+t.Hash())
	}

	recomputedMerkle := RecomputeMerkleRoot(txHashes)
	recomputedHash := RecomputeBlockHash(in.Height, in.PrevHash, recomputedMerkle, in.Timestamp, in.Proposer)
	if recomputedHash != in.Hash {
		return fmt.Errorf("gossiped block hash mismatch: got %s, recomputed %s", in.Hash, recomputedHash)
	}

	// Sealer signature required on public gossip (P0).
	if in.Signature != "" {
		if err := VerifySealerSignature(in.Height, in.Hash, in.Signature, in.Proposer); err != nil {
			return err
		}
	} else {
		// Fail-closed when signature missing for non-empty validator networks.
		// Solo/dev may still gossip without sig only if no validators configured.
		if len(in.AllowedProposers) > 1 {
			return fmt.Errorf("missing sealer signature on gossiped block")
		}
		log.Warn().Uint64("height", in.Height).Msg("gossiped block has no sealer signature (allowed only for solo/dev)")
	}

	// Apply all txs; any failure aborts (no partial state).
	for _, t := range in.Txs {
		if err := store.ApplyTx(t); err != nil {
			return fmt.Errorf("apply tx %s: %w", t.Hash(), err)
		}
	}

	header := state.BlockHeader{
		Height:    in.Height,
		Hash:      in.Hash,
		PrevHash:  in.PrevHash,
		StateRoot: "",
		TxHashes:  txHashes,
		Timestamp: in.Timestamp,
		Proposer:  in.Proposer,
		TxCount:   len(txHashes),
		Signature: in.Signature,
	}
	return store.SaveBlock(header)
}
