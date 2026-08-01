package p2p

import (
	"fmt"

	"github.com/eastchain/east-validator/internal/state"
	"github.com/eastchain/east-validator/internal/tx"
)

// StoreBlockProvider adapts *state.Store to the BlockProvider interface
// used by the block-sync stream protocol.
type StoreBlockProvider struct {
	Store *state.Store
}

func (p StoreBlockProvider) LatestHeight() (uint64, error) {
	return p.Store.GetLatestHeight()
}

func (p StoreBlockProvider) GetBlockRange(from, to uint64) ([]BlockAnnounce, error) {
	if to < from {
		return nil, fmt.Errorf("invalid range")
	}
	out := make([]BlockAnnounce, 0, to-from+1)
	for h := from; h <= to; h++ {
		blk, err := p.Store.GetBlock(h)
		if err != nil {
			// Stop at first missing block rather than erroring the whole range
			break
		}
		// Reconstruct a minimal announce — full txs are not retained in
		// BlockHeader today, so peers get hashes + header fields and must
		// already have applied state via gossip or accept header-only sync.
		var txs []*tx.Transaction
		out = append(out, BlockAnnounce{
			Height:    blk.Height,
			Hash:      blk.Hash,
			PrevHash:  blk.PrevHash,
			Timestamp: blk.Timestamp,
			Proposer:  blk.Proposer,
			Signature: blk.Signature,
			TxHashes:  blk.TxHashes,
			Txs:       txs,
		})
	}
	return out, nil
}
