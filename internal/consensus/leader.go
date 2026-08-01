package consensus

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/eastchain/east-validator/internal/state"
)

// LeaderSchedule implements a CometBFT-inspired round-robin proposer selection.
//
// Rules:
//   - 0 registered validators → local node is always leader (solo / bootstrap mode)
//   - 1 validator            → that validator is always leader
//   - 2+ validators          → leader for height h is validators[h % n] (deterministic rotation)
//
// Addresses are compared case-insensitively and stored lowercased + sorted
// so every node derives the same schedule without extra coordination.
//
// A registered validator whose on-chain staked balance has dropped below
// the genesis minimum is skipped from rotation (not removed from the list —
// it becomes eligible again automatically if it re-stakes). This is checked
// live via store on every LeaderForHeight/IsLocalLeader call rather than
// once at registration, per Ferry's decision.
type LeaderSchedule struct {
	mu         sync.RWMutex
	validators []string // sorted lowercase EVM addresses
	localAddr  string   // this node's sealing / proposer address (lowercase)
	store      *state.Store
}

func NewLeaderSchedule(localAddr string) *LeaderSchedule {
	return &LeaderSchedule{
		localAddr: strings.ToLower(strings.TrimSpace(localAddr)),
	}
}

// SetStore wires the state store used to verify live stake for eligibility
// filtering. Must be called before LeaderForHeight/IsLocalLeader are used
// with a non-empty validator set, or eligibility filtering is skipped
// (falls back to trusting the registered list as-is).
func (l *LeaderSchedule) SetStore(s *state.Store) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.store = s
}

// eligibleValidatorsLocked returns registered validators that meet the
// minimum stake and are not jailed. Caller must hold l.mu.
// If store isn't wired yet, returns the full list unfiltered (fail-open).
func (l *LeaderSchedule) eligibleValidatorsLocked() []string {
	if l.store == nil {
		return l.validators
	}
	minStake := l.store.MinValidatorStake()
	out := make([]string, 0, len(l.validators))
	for _, addr := range l.validators {
		if jailed, _, err := l.store.IsJailed(addr); err == nil && jailed {
			continue
		}
		acc, err := l.store.GetAccount(addr)
		if err != nil {
			continue
		}
		if minStake > 0 && acc.Staked < minStake {
			continue
		}
		out = append(out, addr)
	}
	return out
}

// SetLocalAddress updates this node's identity (e.g. after reading CHAIN_SIGNING_ADDRESS).
func (l *LeaderSchedule) SetLocalAddress(addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.localAddr = strings.ToLower(strings.TrimSpace(addr))
}

// SetValidators replaces the active validator set. Empty list = solo mode.
// When a store is wired, the set is also persisted (and epoch bumped) so
// restarts and gossip peers converge on the same roster.
func (l *LeaderSchedule) SetValidators(addrs []string) {
	uniq := make(map[string]struct{})
	for _, a := range addrs {
		a = strings.ToLower(strings.TrimSpace(a))
		if a != "" {
			uniq[a] = struct{}{}
		}
	}
	list := make([]string, 0, len(uniq))
	for a := range uniq {
		list = append(list, a)
	}
	sort.Strings(list)

	l.mu.Lock()
	l.validators = list
	store := l.store
	l.mu.Unlock()

	if store != nil && len(list) > 0 {
		if _, err := store.SaveValidatorSet(list); err != nil {
			// Non-fatal: in-memory set still applies this process lifetime.
			_ = err
		}
	}
}

// LoadFromStore replaces the in-memory set with the persisted one (if any).
// Returns true if a persisted set was loaded.
func (l *LeaderSchedule) LoadFromStore() bool {
	l.mu.RLock()
	store := l.store
	l.mu.RUnlock()
	if store == nil {
		return false
	}
	rec, err := store.GetValidatorSet()
	if err != nil || rec == nil || len(rec.Addresses) == 0 {
		return false
	}
	l.mu.Lock()
	l.validators = append([]string(nil), rec.Addresses...)
	sort.Strings(l.validators)
	l.mu.Unlock()
	return true
}

// IsEligible reports whether addr currently meets the minimum validator
// stake. Used by the registration endpoint to reject a validator that
// hasn't staked enough before adding it to the schedule.
func (l *LeaderSchedule) IsEligible(addr string) (bool, error) {
	l.mu.RLock()
	store := l.store
	l.mu.RUnlock()
	if store == nil {
		return false, fmt.Errorf("leader schedule has no store wired — cannot verify stake")
	}
	acc, err := store.GetAccount(addr)
	if err != nil {
		return false, err
	}
	return acc.Staked >= store.MinValidatorStake(), nil
}

// AddValidator inserts one address into the set (idempotent).
func (l *LeaderSchedule) AddValidator(addr string) {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if addr == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, v := range l.validators {
		if v == addr {
			return
		}
	}
	l.validators = append(l.validators, addr)
	sort.Strings(l.validators)
}

// RemoveValidator drops an address from the set.
func (l *LeaderSchedule) RemoveValidator(addr string) {
	addr = strings.ToLower(strings.TrimSpace(addr))
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.validators[:0]
	for _, v := range l.validators {
		if v != addr {
			out = append(out, v)
		}
	}
	l.validators = out
}

// Validators returns a copy of the sorted set.
func (l *LeaderSchedule) Validators() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]string, len(l.validators))
	copy(out, l.validators)
	return out
}

// Count returns the number of registered validators.
func (l *LeaderSchedule) Count() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.validators)
}

// LeaderForHeight returns the proposer address for the given block height.
// Height is 1-based in EAST; we still use height % n for rotation.
// Only validators currently meeting the minimum stake are eligible — see
// eligibleValidatorsLocked.
func (l *LeaderSchedule) LeaderForHeight(height uint64) string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	eligible := l.eligibleValidatorsLocked()
	n := len(eligible)
	if n == 0 {
		// Solo / bootstrap, OR every registered validator has fallen below
		// minimum stake: local node is the only eligible producer.
		return l.localAddr
	}
	if n == 1 {
		return eligible[0]
	}
	// CometBFT-style deterministic round-robin
	idx := int(height % uint64(n))
	return eligible[idx]
}

// IsLocalLeader reports whether this node should propose/seal at height.
func (l *LeaderSchedule) IsLocalLeader(height uint64) bool {
	leader := l.LeaderForHeight(height)
	l.mu.RLock()
	local := l.localAddr
	n := len(l.eligibleValidatorsLocked())
	l.mu.RUnlock()
	if local == "" {
		if n == 0 {
			// True solo/bootstrap: no identity configured, no eligible
			// validators either — nothing to compare against, so allow.
			return true
		}
		// Eligible validators ARE registered but this node has no local
		// identity (CHAIN_SIGNING_ADDRESS not set) — refuse rather than
		// silently assume leadership. Producing here could double-seal
		// alongside whichever node is actually scheduled for this height.
		return false
	}
	return strings.EqualFold(leader, local)
}

// Stats for /health and /stats.
func (l *LeaderSchedule) Stats(nextHeight uint64) map[string]any {
	l.mu.RLock()
	defer l.mu.RUnlock()
	eligible := l.eligibleValidatorsLocked()
	n := len(eligible)
	mode := "solo"
	if n == 1 {
		mode = "single"
	} else if n >= 2 {
		mode = "round_robin"
	}
	leader := l.localAddr
	if n > 0 {
		idx := int(nextHeight % uint64(n))
		if n == 1 {
			idx = 0
		}
		leader = eligible[idx]
	}
	return map[string]any{
		"mode":                  mode,
		"validator_count":       n,
		"registered_validators": append([]string(nil), l.validators...), // includes any below minimum stake
		"eligible_validators":   append([]string(nil), eligible...),     // registered AND currently meeting minimum stake
		"local_address":         l.localAddr,
		"next_height":           nextHeight,
		"next_leader":           leader,
		"is_local_leader":       n == 0 || l.localAddr == "" || strings.EqualFold(leader, l.localAddr),
	}
}
