package consensus

import (
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/eastchain/east-validator/internal/state"
)

// DefaultJailDuration is how long a double-signing validator is excluded
// from leader election and vote counting (Phase-1 fixed policy).
const DefaultJailDuration = 24 * time.Hour

// JailOnEquivocation persists a jail record and returns a human-readable evidence string.
func JailOnEquivocation(store *state.Store, voter string, height uint64, round int32, hashA, hashB string) error {
	if store == nil {
		return fmt.Errorf("nil store")
	}
	evidence := fmt.Sprintf("equivocation height=%d round=%d hash_a=%s hash_b=%s", height, round, hashA, hashB)
	if err := store.JailValidator(voter, "equivocation", evidence, DefaultJailDuration); err != nil {
		return err
	}
	log.Error().
		Str("voter", voter).
		Uint64("height", height).
		Int32("round", round).
		Str("hash_a", hashA).
		Str("hash_b", hashB).
		Msg("BFT: validator jailed for equivocation")
	return nil
}
