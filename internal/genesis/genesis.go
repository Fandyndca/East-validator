package genesis

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// TOTAL_MAX_SUPPLY mirrors east-wallet src/lib/db/ledger.ts
const TotalMaxSupply int64 = 1_000_000_000

// EASTChainID is the EIP-155 numeric chain ID (reserved on ChainList).
const EASTChainID int64 = 172026

// Default supply buckets — must sum exactly to TotalMaxSupply.
//
// These 10 buckets are a more granular breakdown of the official EASTCHAIN
// tokenomics doc's 7 categories: mining + staking + validator + campaign
// here sum to exactly 500,000,000 — the doc's single "Ecosystem Rewards"
// category, which it describes as funding PoC mining, validator rewards,
// staking rewards, and the referral program (i.e. exactly these four).
// The doc's other 6 categories map 1:1 to liquidity/treasury/emergency/
// marketing/team/founder below.
var DefaultBuckets = []Bucket{
	{Name: "mining", Cap: 300_000_000},
	{Name: "staking", Cap: 80_000_000},
	{Name: "validator", Cap: 80_000_000},
	{Name: "campaign", Cap: 40_000_000},
	{Name: "liquidity", Cap: 150_000_000},
	{Name: "treasury", Cap: 100_000_000},
	{Name: "emergency", Cap: 70_000_000},
	{Name: "marketing", Cap: 70_000_000},
	{Name: "team", Cap: 60_000_000},
	{Name: "founder", Cap: 50_000_000},
}

type Bucket struct {
	Name   string `json:"name"`
	Cap    int64  `json:"cap"`
	Minted int64  `json:"minted"`
}

type AccountSeed struct {
	Address string `json:"address"`
	Balance int64  `json:"balance"`
	Staked  int64  `json:"staked,omitempty"`
}

type Genesis struct {
	ChainID             string        `json:"chain_id"`              // human-readable, e.g. "eastchain-1"
	NumericChainID      int64         `json:"numeric_chain_id"`      // EIP-155: 172026
	GenesisTime         int64         `json:"genesis_time"`
	TotalMaxSupply      int64         `json:"total_max_supply"`
	Buckets             []Bucket      `json:"buckets"`
	Accounts            []AccountSeed `json:"accounts,omitempty"`
	MinValidatorStake   int64         `json:"min_validator_stake"`
	MinFullnodeStake    int64         `json:"min_fullnode_stake"`
	ChainSigningAddress string        `json:"chain_signing_address,omitempty"`
}

func Default() Genesis {
	return Genesis{
		ChainID:           "eastchain-1",
		NumericChainID:    EASTChainID,
		GenesisTime:       time.Now().UnixMilli(),
		TotalMaxSupply:    TotalMaxSupply,
		Buckets:           append([]Bucket(nil), DefaultBuckets...),
		MinValidatorStake: 100,
		MinFullnodeStake:  10,
	}
}

func (g *Genesis) Validate() error {
	if g.NumericChainID == 0 {
		g.NumericChainID = EASTChainID
	}
	if g.NumericChainID != EASTChainID {
		return fmt.Errorf("numeric_chain_id must be %d (got %d)", EASTChainID, g.NumericChainID)
	}
	var sum int64
	for _, b := range g.Buckets {
		if b.Cap < 0 || b.Minted < 0 {
			return fmt.Errorf("bucket %s has negative cap/minted", b.Name)
		}
		if b.Minted > b.Cap {
			return fmt.Errorf("bucket %s minted (%d) > cap (%d)", b.Name, b.Minted, b.Cap)
		}
		sum += b.Cap
	}
	if sum != g.TotalMaxSupply {
		return fmt.Errorf("bucket caps sum to %d, expected total_max_supply %d", sum, g.TotalMaxSupply)
	}
	if g.TotalMaxSupply != TotalMaxSupply {
		return fmt.Errorf("total_max_supply must be %d (got %d)", TotalMaxSupply, g.TotalMaxSupply)
	}
	if g.MinValidatorStake <= 0 {
		g.MinValidatorStake = 100
	}
	if g.MinFullnodeStake <= 0 {
		g.MinFullnodeStake = 10
	}
	if g.MinFullnodeStake > g.MinValidatorStake {
		return fmt.Errorf("min_fullnode_stake (%d) cannot exceed min_validator_stake (%d)", g.MinFullnodeStake, g.MinValidatorStake)
	}
	return nil
}

func LoadFile(path string) (*Genesis, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var g Genesis
	if err := json.Unmarshal(b, &g); err != nil {
		return nil, err
	}
	if err := g.Validate(); err != nil {
		return nil, err
	}
	return &g, nil
}

func (g *Genesis) WriteFile(path string) error {
	if err := g.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
