package state

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/rs/zerolog/log"
)

// ValidatorSetRecord is the persisted active validator set (equal weight Phase-1).
type ValidatorSetRecord struct {
	Addresses []string `json:"addresses"` // lowercase EVM
	UpdatedAt int64    `json:"updated_at"` // unix ms
	Epoch     uint64   `json:"epoch"`      // monotonic revision for gossip
}

// JailRecord marks a validator as temporarily ineligible after equivocation.
type JailRecord struct {
	Address   string `json:"address"`
	Reason    string `json:"reason"`
	JailedAt  int64  `json:"jailed_at"`  // unix ms
	Until     int64  `json:"until"`      // unix ms; 0 = permanent until admin unjail
	Evidence  string `json:"evidence,omitempty"`
}

func validatorSetKey() []byte { return metaKey("validator_set") }
func jailKey(addr string) []byte {
	return []byte("jail:" + strings.ToLower(addr))
}

// SaveValidatorSet persists the active set and bumps epoch.
func (s *Store) SaveValidatorSet(addrs []string) (*ValidatorSetRecord, error) {
	uniq := make(map[string]struct{})
	var list []string
	for _, a := range addrs {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if _, ok := uniq[a]; ok {
			continue
		}
		uniq[a] = struct{}{}
		list = append(list, a)
	}
	// stable order
	for i := 0; i < len(list); i++ {
		for j := i + 1; j < len(list); j++ {
			if list[j] < list[i] {
				list[i], list[j] = list[j], list[i]
			}
		}
	}

	prev, _ := s.GetValidatorSet()
	epoch := uint64(1)
	if prev != nil {
		epoch = prev.Epoch + 1
	}
	rec := &ValidatorSetRecord{
		Addresses: list,
		UpdatedAt: time.Now().UnixMilli(),
		Epoch:     epoch,
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	err = s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(validatorSetKey(), b)
	})
	if err != nil {
		return nil, err
	}
	log.Info().Int("count", len(list)).Uint64("epoch", epoch).Msg("validator set persisted")
	return rec, nil
}

// GetValidatorSet loads the persisted set; nil if never saved.
func (s *Store) GetValidatorSet() (*ValidatorSetRecord, error) {
	var rec ValidatorSetRecord
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(validatorSetKey())
		if err == badger.ErrKeyNotFound {
			return err
		}
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &rec)
		})
	})
	if err == badger.ErrKeyNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// JailValidator records a jail entry (equivocation, etc.).
func (s *Store) JailValidator(addr, reason, evidence string, duration time.Duration) error {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if addr == "" {
		return fmt.Errorf("address required")
	}
	now := time.Now().UnixMilli()
	until := int64(0)
	if duration > 0 {
		until = time.Now().Add(duration).UnixMilli()
	}
	rec := JailRecord{
		Address:  addr,
		Reason:   reason,
		JailedAt: now,
		Until:    until,
		Evidence: evidence,
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	err = s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(jailKey(addr), b)
	})
	if err != nil {
		return err
	}
	log.Warn().Str("address", addr).Str("reason", reason).Int64("until", until).Msg("validator JAILED")
	return nil
}

// UnjailValidator removes a jail record.
func (s *Store) UnjailValidator(addr string) error {
	addr = strings.ToLower(strings.TrimSpace(addr))
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(jailKey(addr))
	})
}

// IsJailed reports whether addr is currently jailed.
func (s *Store) IsJailed(addr string) (bool, *JailRecord, error) {
	addr = strings.ToLower(strings.TrimSpace(addr))
	var rec JailRecord
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(jailKey(addr))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &rec)
		})
	})
	if err == badger.ErrKeyNotFound {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	// Expired jail?
	if rec.Until > 0 && time.Now().UnixMilli() > rec.Until {
		_ = s.UnjailValidator(addr)
		return false, nil, nil
	}
	return true, &rec, nil
}

// ListJailed returns all non-expired jail records (capped).
func (s *Store) ListJailed(limit int) ([]JailRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var out []JailRecord
	now := time.Now().UnixMilli()
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		it := txn.NewIterator(opts)
		defer it.Close()
		prefix := []byte("jail:")
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			var rec JailRecord
			if err := it.Item().Value(func(val []byte) error {
				return json.Unmarshal(val, &rec)
			}); err != nil {
				return err
			}
			if rec.Until > 0 && now > rec.Until {
				continue
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
