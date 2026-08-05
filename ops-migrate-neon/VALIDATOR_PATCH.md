# Validator patch — seed balance + stake + pending_unstake

`/admin/seed` sebelumnya **hanya** set `balance`. Untuk migrasi stake Neon → chain, perlu set `staked` dan `pending_unstake` juga.

## 1. `internal/state/state.go`

Ganti fungsi `SeedBalance` dengan:

```go
// SeedBalance sets only free balance (legacy). Prefer SeedAccount for migrations.
func (s *Store) SeedBalance(addr string, balance int64) error {
	return s.SeedAccount(addr, &Account{Balance: balance}, seedOnlyBalance)
}

type seedMode int

const (
	seedOverwrite   seedMode = iota // set all fields from snapshot
	seedOnlyBalance                 // only Balance
	seedMergeMax                    // per-field max(existing, incoming)
)

// SeedAccount writes balance / staked / pending_unstake (6-dec subunits).
// Nonce is never changed by seed (avoids breaking in-flight txs).
func (s *Store) SeedAccount(addr string, incoming *Account, mode seedMode) error {
	if incoming == nil {
		return fmt.Errorf("nil account")
	}
	return s.db.Update(func(txn *badger.Txn) error {
		acc, err := getAccountTxn(txn, addr)
		if err != nil {
			return err
		}
		switch mode {
		case seedOnlyBalance:
			acc.Balance = incoming.Balance
		case seedMergeMax:
			if incoming.Balance > acc.Balance {
				acc.Balance = incoming.Balance
			}
			if incoming.Staked > acc.Staked {
				acc.Staked = incoming.Staked
			}
			if incoming.PendingUnstake > acc.PendingUnstake {
				acc.PendingUnstake = incoming.PendingUnstake
			}
		default: // seedOverwrite
			acc.Balance = incoming.Balance
			acc.Staked = incoming.Staked
			acc.PendingUnstake = incoming.PendingUnstake
		}
		return setAccountTxn(txn, addr, acc)
	})
}
```

## 2. `internal/api/server.go`

Ganti `seedRequest` + `handleSeed`:

```go
type seedRequest struct {
	Address        string `json:"address"`
	Balance        int64  `json:"balance"`                   // subunits
	Staked         int64  `json:"staked"`                    // subunits
	PendingUnstake int64  `json:"pending_unstake"`           // subunits
	Mode           string `json:"mode,omitempty"`            // "overwrite" | "merge_max" | "balance_only"
}

func (s *Server) handleSeed(w http.ResponseWriter, r *http.Request) {
	var req seedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if req.Address == "" {
		http.Error(w, "address required", http.StatusBadRequest)
		return
	}
	mode := seedOverwrite
	switch strings.ToLower(strings.TrimSpace(req.Mode)) {
	case "merge_max", "merge":
		mode = seedMergeMax
	case "balance_only", "balance":
		mode = seedOnlyBalance
	case "", "overwrite", "set":
		mode = seedOverwrite
	default:
		http.Error(w, "invalid mode (use overwrite|merge_max|balance_only)", http.StatusBadRequest)
		return
	}
	// seedMode constants live in package state — export them or map here:
	// Prefer calling store with exported helpers if you keep seedMode private:
	acc := &state.Account{
		Balance:        req.Balance,
		Staked:         req.Staked,
		PendingUnstake: req.PendingUnstake,
	}
	if err := s.store.SeedAccount(req.Address, acc, mode); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"address": strings.ToLower(req.Address),
		"mode":    req.Mode,
		"seeded": map[string]int64{
			"balance":         req.Balance,
			"staked":          req.Staked,
			"pending_unstake": req.PendingUnstake,
		},
	})
}
```

Export `seedMode` values from `state` package (rename to `SeedModeOverwrite`, etc.) if needed so `api` can pass them without import cycle issues — `api` already imports `state`.

Simplest: put mode mapping **inside** `SeedAccount` as string:

```go
func (s *Store) SeedAccount(addr string, balance, staked, pending int64, mode string) error {
	incoming := &Account{Balance: balance, Staked: staked, PendingUnstake: pending}
	switch strings.ToLower(mode) {
	case "merge_max", "merge":
		return s.seedAccount(addr, incoming, seedMergeMax)
	case "balance_only", "balance":
		return s.seedAccount(addr, incoming, seedOnlyBalance)
	default:
		return s.seedAccount(addr, incoming, seedOverwrite)
	}
}
```

## 3. Redeploy validator di Railway setelah patch

Tanpa patch ini, skrip migrasi hanya bisa set **balance** (API lama); **staked tetap 0**.

## Contoh curl setelah patch

```bash
curl -sS -X POST "$VALIDATOR/admin/seed" \
  -H "Content-Type: application/json" \
  -H "X-API-Secret: $API_SECRET" \
  -d '{
    "address": "0xD06e3ACDbbEeCf31392F5da4F8B04B99AE35ea9f",
    "balance": 20000000000000,
    "staked": 2000000000,
    "pending_unstake": 0,
    "mode": "merge_max"
  }'
```

Unit: **subunits** (1 EAST = 1_000_000).
