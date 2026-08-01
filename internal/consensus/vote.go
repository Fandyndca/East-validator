package consensus

import (
	"fmt"
	"strings"

	"github.com/eastchain/east-validator/internal/crypto"
	"github.com/eastchain/east-validator/internal/tx"
)

// VoteType mirrors Tendermint/CometBFT vote stages.
type VoteType string

const (
	VotePrevote   VoteType = "prevote"
	VotePrecommit VoteType = "precommit"
)

// Vote is a signed opinion from one validator about a block at (height, round).
// Signature covers BuildVoteMessage(...); recovered signer must equal Voter.
type Vote struct {
	Type      VoteType `json:"type"`
	Height    uint64   `json:"height"`
	Round     int32    `json:"round"`
	BlockHash string   `json:"block_hash"` // empty string = nil / timeout vote
	Voter     string   `json:"voter"`      // EVM address of validator
	Timestamp int64    `json:"timestamp"`  // unix ms
	Signature string   `json:"signature"`
}

// Proposal is the leader's candidate block for (height, round).
// Signature covers BuildProposalBFTMessage(...); recovered signer must equal Proposer.
// Txs carries full transaction bodies so non-leaders can apply the same state
// transition at commit (P0 — no hash-only proposals).
type Proposal struct {
	Height     uint64 `json:"height"`
	Round      int32  `json:"round"`
	BlockHash  string `json:"block_hash"`
	PrevHash   string `json:"prev_hash"`
	MerkleRoot string `json:"merkle_root"`
	Timestamp  int64  `json:"timestamp"`
	Proposer   string `json:"proposer"`
	TxHashes []string `json:"tx_hashes,omitempty"`
	// Txs is the full body list matching TxHashes order (may be empty for empty blocks).
	Txs []*tx.Transaction `json:"txs,omitempty"`
	// POLRound is the locked round that justifies this proposal (-1 if none).
	POLRound  int32  `json:"pol_round"`
	Signature string `json:"signature"`
}

// CommitCertificate is the set of precommits that finalizes a block.
// Quorum = >2/3 of the current validator set (by count; equal weight Phase-1).
type CommitCertificate struct {
	Height    uint64 `json:"height"`
	Round     int32  `json:"round"`
	BlockHash string `json:"block_hash"`
	Votes     []Vote `json:"votes"`
}

// BuildVoteMessage is what a validator signs for a prevote/precommit.
// Keep stable — changing this breaks all existing vote signatures.
//
//	EASTCHAIN_VOTE|{type}|{height}|{round}|{blockHash}
func BuildVoteMessage(voteType VoteType, height uint64, round int32, blockHash string) string {
	if blockHash == "" {
		blockHash = "NIL"
	}
	return fmt.Sprintf("EASTCHAIN_VOTE|%s|%d|%d|%s", voteType, height, round, blockHash)
}

// BuildProposalBFTMessage is what the proposer signs for a BFT proposal.
// Distinct from the older EASTCHAIN_PROPOSAL used by Fullnode Browser path.
//
//	EASTCHAIN_BFT_PROPOSAL|{height}|{round}|{blockHash}|{prevHash}
func BuildProposalBFTMessage(height uint64, round int32, blockHash, prevHash string) string {
	return fmt.Sprintf("EASTCHAIN_BFT_PROPOSAL|%d|%d|%s|%s", height, round, blockHash, prevHash)
}

// SignVote creates a Vote signed with the given secp256k1 private key.
func SignVote(voteType VoteType, height uint64, round int32, blockHash, voterAddr, privKeyHex string, ts int64) (*Vote, error) {
	msg := BuildVoteMessage(voteType, height, round, blockHash)
	sig, err := crypto.SignEIP191(msg, privKeyHex)
	if err != nil {
		return nil, err
	}
	return &Vote{
		Type:      voteType,
		Height:    height,
		Round:     round,
		BlockHash: blockHash,
		Voter:     strings.ToLower(voterAddr),
		Timestamp: ts,
		Signature: sig,
	}, nil
}

// VerifyVote checks EIP-191 signature and that Voter is the recovered signer.
func VerifyVote(v *Vote) error {
	if v == nil {
		return fmt.Errorf("nil vote")
	}
	if v.Type != VotePrevote && v.Type != VotePrecommit {
		return fmt.Errorf("invalid vote type %q", v.Type)
	}
	if v.Voter == "" || v.Signature == "" {
		return fmt.Errorf("vote missing voter or signature")
	}
	msg := BuildVoteMessage(v.Type, v.Height, v.Round, v.BlockHash)
	ok, err := crypto.VerifyEIP191(msg, v.Signature, v.Voter)
	if err != nil {
		return fmt.Errorf("vote sig verify: %w", err)
	}
	if !ok {
		return fmt.Errorf("vote signature does not match voter %s", v.Voter)
	}
	return nil
}

// SignProposalBFT signs a BFT proposal.
func SignProposalBFT(p *Proposal, privKeyHex string) error {
	msg := BuildProposalBFTMessage(p.Height, p.Round, p.BlockHash, p.PrevHash)
	sig, err := crypto.SignEIP191(msg, privKeyHex)
	if err != nil {
		return err
	}
	p.Signature = sig
	return nil
}

// VerifyProposalBFT checks the proposer's EIP-191 signature.
func VerifyProposalBFT(p *Proposal) error {
	if p == nil {
		return fmt.Errorf("nil proposal")
	}
	if p.Proposer == "" || p.Signature == "" {
		return fmt.Errorf("proposal missing proposer or signature")
	}
	msg := BuildProposalBFTMessage(p.Height, p.Round, p.BlockHash, p.PrevHash)
	ok, err := crypto.VerifyEIP191(msg, p.Signature, p.Proposer)
	if err != nil {
		return fmt.Errorf("proposal sig verify: %w", err)
	}
	if !ok {
		return fmt.Errorf("proposal signature does not match proposer %s", p.Proposer)
	}
	return nil
}

// QuorumSize returns the minimum number of distinct validator votes needed
// for +2/3 majority given n validators. For n<=1 returns n (solo commits alone).
func QuorumSize(n int) int {
	if n <= 0 {
		return 0
	}
	if n == 1 {
		return 1
	}
	// floor(2n/3)+1
	return (2*n)/3 + 1
}
