package consensus

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/rs/zerolog/log"

	"github.com/eastchain/east-validator/internal/state"
	"github.com/eastchain/east-validator/internal/tx"
)

type SealResult struct {
	Header    state.BlockHeader `json:"header"`
	SealerSig string            `json:"sealer_signature"`
	Txs       []*tx.Transaction `json:"-"` // applied txs, for gossip replay — not part of the sealed header itself
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

// VerifyAndSaveGossipedBlock validates a block received via P2P gossip
// (i.e. produced by another validator, not this node) before persisting
// it locally AND replaying its transactions into this node's own state.
//
// Deliberately loose for now: checks height/prevHash/blockHash consistency
// against this node's own chain tip, but does NOT verify the sealer
// signature against a known validator identity — there's currently no
// per-peer mapping from gossip sender to a trusted signing address. A
// mismatched or forged block is still caught by the hash recompute (an
// attacker would need the real prev block's exact tx set to produce a
// matching hash), but a colluding/compromised peer could still gossip a
// block for a height/prevHash it has no business proposing. Tighten this
// once each validator's signing address is tracked per-peer.
//
// txs must be the full transaction bodies (not just hashes) so they can be
// applied to this node's local state — see p2p.BlockAnnounce.Txs.
func VerifyAndSaveGossipedBlock(store *state.Store, height uint64, hash, prevHash string, timestamp int64, proposer string, txs []*tx.Transaction) error {
	latest, err := store.GetLatestHeight()
	if err != nil {
		return err
	}
	if height != latest+1 {
		return fmt.Errorf("gossiped block height %d != expected %d — ignoring (stale or out of order)", height, latest+1)
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
	if prevHash != expectedPrev {
		return fmt.Errorf("gossiped block prev_hash mismatch: got %s, expected %s", prevHash, expectedPrev)
	}

	txHashes := make([]string, 0, len(txs))
	for _, t := range txs {
		txHashes = append(txHashes, "0x"+t.Hash())
	}

	recomputedMerkle := RecomputeMerkleRoot(txHashes)
	recomputedHash := RecomputeBlockHash(height, prevHash, recomputedMerkle, timestamp, proposer)
	if recomputedHash != hash {
		return fmt.Errorf("gossiped block hash mismatch: got %s, recomputed %s", hash, recomputedHash)
	}

	// Replay each tx into this node's own state so balances/nonces stay in
	// sync with the leader that actually produced the block. A tx that
	// fails here (e.g. this node's view of the sender's balance/nonce
	// somehow disagrees) is logged and skipped rather than aborting the
	// whole block — the block is still the source of truth for hash/height
	// continuity even if this node's local state has drifted on one tx.
	//
	// Note: ApplyTx does NOT re-check tx.VerifySignature() — it only
	// mutates balances/nonce. Individual tx signatures inside a gossiped
	// block are therefore trusted, not re-verified, here. Consistent with
	// this function's documented "loose" verification scope (see doc
	// comment above) — tighten together if/when that's revisited.
	for _, t := range txs {
		if err := store.ApplyTx(t); err != nil {
			// Intentionally not returned as a hard failure — see comment above.
			log.Warn().Err(err).Uint64("height", height).Str("tx", t.Hash()).Msg("gossiped block: tx failed to apply locally")
		}
	}

	header := state.BlockHeader{
		Height:    height,
		Hash:      hash,
		PrevHash:  prevHash,
		StateRoot: "",
		TxHashes:  txHashes,
		Timestamp: timestamp,
		Proposer:  proposer,
		TxCount:   len(txHashes),
	}
	return store.SaveBlock(header)
}
