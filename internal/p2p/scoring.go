package p2p

import (
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/rs/zerolog/log"
)

// PeerScorer tracks simple behaviour scores and temporary bans.
// Positive: valid blocks / useful sync. Negative: invalid gossip, protocol abuse.
type PeerScorer struct {
	mu     sync.Mutex
	score  map[peer.ID]int
	banned map[peer.ID]time.Time // until
	// thresholds
	banBelow int
	banFor   time.Duration
}

func NewPeerScorer() *PeerScorer {
	return &PeerScorer{
		score:    make(map[peer.ID]int),
		banned:   make(map[peer.ID]time.Time),
		banBelow: -50,
		banFor:   30 * time.Minute,
	}
}

func (s *PeerScorer) Add(id peer.ID, delta int, reason string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.score[id] += delta
	sc := s.score[id]
	if sc <= s.banBelow {
		s.banned[id] = time.Now().Add(s.banFor)
		log.Warn().Str("peer", id.String()).Int("score", sc).Str("reason", reason).Msg("peer BANNED")
	} else if delta < 0 {
		log.Debug().Str("peer", id.String()).Int("score", sc).Str("reason", reason).Int("delta", delta).Msg("peer score")
	}
}

func (s *PeerScorer) IsBanned(id peer.ID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	until, ok := s.banned[id]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(s.banned, id)
		// soft reset score so they can rehabilitate
		if s.score[id] < 0 {
			s.score[id] = s.banBelow / 2
		}
		return false
	}
	return true
}

func (s *PeerScorer) Score(id peer.ID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.score[id]
}

func (s *PeerScorer) Snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	scores := map[string]int{}
	for id, sc := range s.score {
		scores[id.String()] = sc
	}
	bans := map[string]string{}
	now := time.Now()
	for id, until := range s.banned {
		if now.Before(until) {
			bans[id.String()] = until.UTC().Format(time.RFC3339)
		}
	}
	return map[string]any{
		"scores": scores,
		"banned": bans,
	}
}
