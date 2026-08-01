package p2p

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/rs/zerolog/log"

	"github.com/eastchain/east-validator/internal/tx"
)

// Stream protocol for catch-up sync when a node is behind the tip.
// Request/response over a dedicated stream (not gossip) so large ranges
// don't flood the pubsub mesh.
const SyncProtocolID = protocol.ID("/eastchain/blocksync/1.0.0")

const (
	maxBlocksPerResponse = 50
	syncStreamTimeout    = 30 * time.Second
)

// SyncRequest asks a peer for blocks in [FromHeight, ToHeight] inclusive.
type SyncRequest struct {
	FromHeight uint64 `json:"from_height"`
	ToHeight   uint64 `json:"to_height"`
}

// SyncResponse carries up to maxBlocksPerResponse block announces.
type SyncResponse struct {
	Blocks []BlockAnnounce `json:"blocks"`
	Error  string          `json:"error,omitempty"`
}

// BlockProvider is implemented by the local store/consensus layer so the
// P2P package does not import state (avoids cycles).
type BlockProvider interface {
	// LatestHeight returns the local tip.
	LatestHeight() (uint64, error)
	// GetBlockRange returns block announces for [from, to] capped by the provider.
	GetBlockRange(from, to uint64) ([]BlockAnnounce, error)
}

// RegisterSyncHandler serves block-sync requests from peers.
func (n *Node) RegisterSyncHandler(provider BlockProvider) {
	if n.host == nil || provider == nil {
		return
	}
	n.host.SetStreamHandler(SyncProtocolID, func(s network.Stream) {
		defer s.Close()
		_ = s.SetDeadline(time.Now().Add(syncStreamTimeout))

		var req SyncRequest
		if err := json.NewDecoder(io.LimitReader(s, 64*1024)).Decode(&req); err != nil {
			_ = json.NewEncoder(s).Encode(SyncResponse{Error: "invalid_request"})
			return
		}
		if req.ToHeight < req.FromHeight {
			_ = json.NewEncoder(s).Encode(SyncResponse{Error: "invalid_range"})
			return
		}
		// Cap range
		if req.ToHeight-req.FromHeight+1 > maxBlocksPerResponse {
			req.ToHeight = req.FromHeight + maxBlocksPerResponse - 1
		}

		blocks, err := provider.GetBlockRange(req.FromHeight, req.ToHeight)
		if err != nil {
			_ = json.NewEncoder(s).Encode(SyncResponse{Error: err.Error()})
			return
		}
		// Strip oversized tx bodies if needed — keep hashes always
		for i := range blocks {
			if len(blocks[i].Txs) > 200 {
				blocks[i].Txs = nil
			}
		}
		_ = json.NewEncoder(s).Encode(SyncResponse{Blocks: blocks})
	})
	log.Info().Str("protocol", string(SyncProtocolID)).Msg("block-sync stream handler registered")
}

// RequestBlocks pulls a range from a specific peer.
func (n *Node) RequestBlocks(ctx context.Context, peerID peer.ID, from, to uint64) ([]BlockAnnounce, error) {
	if n.host == nil {
		return nil, fmt.Errorf("p2p disabled")
	}
	if to < from {
		return nil, fmt.Errorf("invalid range")
	}
	if to-from+1 > maxBlocksPerResponse {
		to = from + maxBlocksPerResponse - 1
	}

	s, err := n.host.NewStream(ctx, peerID, SyncProtocolID)
	if err != nil {
		return nil, fmt.Errorf("open sync stream: %w", err)
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(syncStreamTimeout))

	if err := json.NewEncoder(s).Encode(SyncRequest{FromHeight: from, ToHeight: to}); err != nil {
		return nil, err
	}
	var resp SyncResponse
	if err := json.NewDecoder(io.LimitReader(s, 8*1024*1024)).Decode(&resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("peer error: %s", resp.Error)
	}
	return resp.Blocks, nil
}

// Ensure unused import of tx is referenced when BlockAnnounce carries txs.
var _ = tx.Transaction{}
