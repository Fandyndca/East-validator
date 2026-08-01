package state

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// NodeTier distinguishes reward weight later.
type NodeTier string

const (
	TierLight NodeTier = "light"
	TierFull  NodeTier = "full"
)

type HeartbeatRecord struct {
	Address   string   `json:"address"`
	NodeID    string   `json:"node_id"`
	Tier      NodeTier `json:"tier"`
	LastSeen  int64    `json:"last_seen"`  // unix ms
	EpochID   int64    `json:"epoch_id"`   // floor(last_seen / epochSeconds)
	Count     int64    `json:"count"`      // heartbeats in this epoch
	FirstSeen int64    `json:"first_seen"` // first heartbeat in this epoch
}

func hbKey(addr string) []byte {
	return []byte("hb:" + strings.ToLower(addr))
}

func epochScoreKey(epochID int64, addr string) []byte {
	return []byte(fmt.Sprintf("epoch:%d:%s", epochID, strings.ToLower(addr)))
}

// RecordHeartbeat upserts the latest heartbeat and increments epoch counter.
// expectedIntervalSec is used only for docs/clients; we accept any interval.
func (s *Store) RecordHeartbeat(address, nodeID string, tier NodeTier, epochSeconds int64) (*HeartbeatRecord, error) {
	if address == "" {
		return nil, fmt.Errorf("address required")
	}
	if tier != TierFull {
		tier = TierLight
	}
	if epochSeconds <= 0 {
		epochSeconds = 604800
	}
	now := time.Now().UnixMilli()
	epochID := now / (epochSeconds * 1000)

	var out HeartbeatRecord
	err := s.db.Update(func(txn *badger.Txn) error {
		var rec HeartbeatRecord
		item, err := txn.Get(hbKey(address))
		if err == nil {
			_ = item.Value(func(val []byte) error { return json.Unmarshal(val, &rec) })
		} else if err != badger.ErrKeyNotFound {
			return err
		}

		// New epoch → reset counter
		if rec.EpochID != epochID {
			rec = HeartbeatRecord{
				Address:   strings.ToLower(address),
				NodeID:    nodeID,
				Tier:      tier,
				LastSeen:  now,
				EpochID:   epochID,
				Count:     1,
				FirstSeen: now,
			}
		} else {
			rec.Address = strings.ToLower(address)
			rec.NodeID = nodeID
			rec.Tier = tier
			rec.LastSeen = now
			rec.Count++
			if rec.FirstSeen == 0 {
				rec.FirstSeen = now
			}
		}

		b, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		if err := txn.Set(hbKey(address), b); err != nil {
			return err
		}
		// Mirror into epoch index for later score listing
		if err := txn.Set(epochScoreKey(epochID, address), b); err != nil {
			return err
		}
		out = rec
		return nil
	})
	return &out, err
}

func (s *Store) GetHeartbeat(address string) (*HeartbeatRecord, error) {
	var rec HeartbeatRecord
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(hbKey(address))
		if err == badger.ErrKeyNotFound {
			return fmt.Errorf("not found")
		}
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error { return json.Unmarshal(val, &rec) })
	})
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// UptimeScore is a simple Phase-1 score: heartbeat count in the epoch.
// Later can weight by expected heartbeats (epochSeconds / interval).
type UptimeScore struct {
	Address  string   `json:"address"`
	NodeID   string   `json:"node_id"`
	Tier     NodeTier `json:"tier"`
	EpochID  int64    `json:"epoch_id"`
	Count    int64    `json:"count"`
	LastSeen int64    `json:"last_seen"`
	// Approx uptime ratio if client heartbeats every ~2–5 min.
	// Not exact — just a hint for claim UI.
	ApproxRatio float64 `json:"approx_ratio"`
}

func (s *Store) GetUptimeScore(address string, epochSeconds int64) (*UptimeScore, error) {
	rec, err := s.GetHeartbeat(address)
	if err != nil {
		return nil, err
	}
	if epochSeconds <= 0 {
		epochSeconds = 604800
	}
	// Assume target ~1 heartbeat / 3 minutes
	expected := epochSeconds / 180
	if expected < 1 {
		expected = 1
	}
	ratio := float64(rec.Count) / float64(expected)
	if ratio > 1 {
		ratio = 1
	}
	return &UptimeScore{
		Address:     rec.Address,
		NodeID:      rec.NodeID,
		Tier:        rec.Tier,
		EpochID:     rec.EpochID,
		Count:       rec.Count,
		LastSeen:    rec.LastSeen,
		ApproxRatio: ratio,
	}, nil
}

// ListEpochScores returns heartbeats recorded for a given epoch (capped).
func (s *Store) ListEpochScores(epochID int64, limit int) ([]HeartbeatRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	prefix := []byte(fmt.Sprintf("epoch:%d:", epochID))
	var out []HeartbeatRecord
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			var rec HeartbeatRecord
			if err := it.Item().Value(func(val []byte) error { return json.Unmarshal(val, &rec) }); err != nil {
				return err
			}
			out = append(out, rec)
			if len(out) >= limit {
				break
			}
		}
		return nil
	})
	return out, err
}

func CurrentEpochID(epochSeconds int64) int64 {
	if epochSeconds <= 0 {
		epochSeconds = 604800
	}
	return time.Now().UnixMilli() / (epochSeconds * 1000)
}
