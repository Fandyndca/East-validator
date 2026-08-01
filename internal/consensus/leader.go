package consensus

import (
	"sort"
	"strings"
	"sync"
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
type LeaderSchedule struct {
	mu         sync.RWMutex
	validators []string // sorted lowercase EVM addresses
	localAddr  string   // this node's sealing / proposer address (lowercase)
}

func NewLeaderSchedule(localAddr string) *LeaderSchedule {
	return &LeaderSchedule{
		localAddr: strings.ToLower(strings.TrimSpace(localAddr)),
	}
}

// SetLocalAddress updates this node's identity (e.g. after reading CHAIN_SIGNING_ADDRESS).
func (l *LeaderSchedule) SetLocalAddress(addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.localAddr = strings.ToLower(strings.TrimSpace(addr))
}

// SetValidators replaces the active validator set. Empty list = solo mode.
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
	l.mu.Unlock()
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
func (l *LeaderSchedule) LeaderForHeight(height uint64) string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	n := len(l.validators)
	if n == 0 {
		// Solo / bootstrap: local node is the only producer
		return l.localAddr
	}
	if n == 1 {
		return l.validators[0]
	}
	// CometBFT-style deterministic round-robin
	idx := int(height % uint64(n))
	return l.validators[idx]
}

// IsLocalLeader reports whether this node should propose/seal at height.
func (l *LeaderSchedule) IsLocalLeader(height uint64) bool {
	leader := l.LeaderForHeight(height)
	l.mu.RLock()
	local := l.localAddr
	n := len(l.validators)
	l.mu.RUnlock()
	if local == "" {
		if n == 0 {
			// True solo/bootstrap: no identity configured, no validators
			// registered either — nothing to compare against, so allow.
			return true
		}
		// A validator set IS registered but this node has no local identity
		// (CHAIN_SIGNING_ADDRESS not set) — refuse rather than silently
		// assume leadership. Producing here could double-seal alongside
		// whichever node is actually scheduled for this height.
		return false
	}
	return strings.EqualFold(leader, local)
}

// Stats for /health and /stats.
func (l *LeaderSchedule) Stats(nextHeight uint64) map[string]any {
	l.mu.RLock()
	defer l.mu.RUnlock()
	n := len(l.validators)
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
		leader = l.validators[idx]
	}
	return map[string]any{
		"mode":            mode,
		"validator_count": n,
		"validators":      append([]string(nil), l.validators...),
		"local_address":   l.localAddr,
		"next_height":     nextHeight,
		"next_leader":     leader,
		"is_local_leader": n == 0 || l.localAddr == "" || strings.EqualFold(leader, l.localAddr),
	}
}
