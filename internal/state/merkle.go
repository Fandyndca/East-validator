package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/dgraph-io/badger/v4"
)

// ─────────────────────────────────────────────────────────────────────────────
// Binary Merkle tree over account leaves (sorted by address).
//
// Leaf:   sha256("EAST_LEAF|" + lower(addr) + "|" + balance + "|" + staked +
//                 "|" + pending_unstake + "|" + nonce)
// Node:   sha256("EAST_NODE|" + left || right)   // left,right are 32-byte digests
// Empty:  sha256("EAST_EMPTY")
//
// Proof is a classic sibling path from leaf to root (log2 n hashes).
// Verifiers only need the leaf fields + path + expected root — no full state.
// ─────────────────────────────────────────────────────────────────────────────

const (
	leafPrefix = "EAST_LEAF|"
	nodePrefix = "EAST_NODE|"
	emptyTag   = "EAST_EMPTY"
)

func hashBytes(parts ...[]byte) []byte {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	sum := h.Sum(nil)
	return sum
}

func hashHex(b []byte) string {
	return "0x" + hex.EncodeToString(b)
}

func decodeHex32(s string) ([]byte, error) {
	s = strings.TrimPrefix(strings.ToLower(s), "0x")
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("expected 32-byte hash, got %d", len(b))
	}
	return b, nil
}

// LeafHash computes the Merkle leaf digest for one account.
func LeafHash(addr string, acc Account) []byte {
	addr = strings.ToLower(strings.TrimSpace(addr))
	payload := fmt.Sprintf("%s%s|%d|%d|%d|%d",
		leafPrefix, addr, acc.Balance, acc.Staked, acc.PendingUnstake, acc.Nonce)
	return hashBytes([]byte(payload))
}

func emptyLeaf() []byte {
	return hashBytes([]byte(emptyTag))
}

func parentHash(left, right []byte) []byte {
	// Domain-separated interior node
	return hashBytes([]byte(nodePrefix), left, right)
}

// accountLeaf is an ordered leaf used while building the tree.
type accountLeaf struct {
	Addr string
	Acc  Account
	Hash []byte
}

// loadSortedLeaves returns all accounts sorted by lowercase address.
func (s *Store) loadSortedLeaves() ([]accountLeaf, error) {
	var leaves []accountLeaf
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		it := txn.NewIterator(opts)
		defer it.Close()
		prefix := []byte("acc:")
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			key := string(it.Item().Key())
			addr := strings.ToLower(strings.TrimPrefix(key, "acc:"))
			var acc Account
			if err := it.Item().Value(func(val []byte) error {
				return json.Unmarshal(val, &acc)
			}); err != nil {
				return err
			}
			leaves = append(leaves, accountLeaf{
				Addr: addr,
				Acc:  acc,
				Hash: LeafHash(addr, acc),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(leaves, func(i, j int) bool { return leaves[i].Addr < leaves[j].Addr })
	return leaves, nil
}

// buildMerkleRoot returns the root digest and, if needed for proofs, the full
// level-order layers (index 0 = leaves, last = root layer with 1 node).
func buildMerkleLayers(leafHashes [][]byte) [][][]byte {
	if len(leafHashes) == 0 {
		return [][][]byte{{emptyLeaf()}}
	}
	// Copy leaves
	level := make([][]byte, len(leafHashes))
	for i, h := range leafHashes {
		level[i] = append([]byte{}, h...)
	}
	layers := [][][]byte{level}
	for len(level) > 1 {
		var next [][]byte
		for i := 0; i < len(level); i += 2 {
			left := level[i]
			var right []byte
			if i+1 < len(level) {
				right = level[i+1]
			} else {
				// Odd node: promote (duplicate) — common simple rule; alternative is hash(left||empty)
				right = left
			}
			next = append(next, parentHash(left, right))
		}
		layers = append(layers, next)
		level = next
	}
	return layers
}

// ComputeMerkleStateRoot returns 0x-prefixed Merkle root over all accounts.
func (s *Store) ComputeMerkleStateRoot() (string, error) {
	leaves, err := s.loadSortedLeaves()
	if err != nil {
		return "", err
	}
	hashes := make([][]byte, len(leaves))
	for i := range leaves {
		hashes[i] = leaves[i].Hash
	}
	layers := buildMerkleLayers(hashes)
	root := layers[len(layers)-1][0]
	return hashHex(root), nil
}

// MerkleProof is an inclusion proof for one account against the state root.
type MerkleProof struct {
	Address   string   `json:"address"`
	Account   Account  `json:"account"`
	LeafHash  string   `json:"leaf_hash"`  // 0x…
	LeafIndex int      `json:"leaf_index"` // position in sorted leaf list
	LeafCount int      `json:"leaf_count"`
	// Path is sibling hashes from leaf level up to (but not including) root.
	// path[i] is the sibling at layer i; Side[i] is "L" if sibling is on the
	// left of the current node, "R" if on the right.
	Path      []string `json:"path"`
	Sides     []string `json:"sides"` // "L" | "R" per path entry
	StateRoot string   `json:"state_root"`
}

// ProveAccountMerkle builds a compact Merkle inclusion proof.
func (s *Store) ProveAccountMerkle(addr string) (*MerkleProof, error) {
	addr = strings.ToLower(strings.TrimSpace(addr))
	leaves, err := s.loadSortedLeaves()
	if err != nil {
		return nil, err
	}
	if len(leaves) == 0 {
		root := hashHex(emptyLeaf())
		acc, _ := s.GetAccount(addr)
		return &MerkleProof{
			Address:   addr,
			Account:   acc,
			LeafHash:  hashHex(LeafHash(addr, acc)),
			LeafIndex: 0,
			LeafCount: 0,
			Path:      nil,
			Sides:     nil,
			StateRoot: root,
		}, nil
	}

	idx := -1
	for i, l := range leaves {
		if l.Addr == addr {
			idx = i
			break
		}
	}
	// Account may not exist yet — prove zero account at would-be position
	// by inserting a virtual zero leaf only for proof of non-membership is
	// more complex. Phase-1: if missing, return proof of zero account without
	// claiming inclusion (LeafIndex=-1).
	var acc Account
	var leafHash []byte
	if idx >= 0 {
		acc = leaves[idx].Acc
		leafHash = leaves[idx].Hash
	} else {
		acc, _ = s.GetAccount(addr) // zeros
		leafHash = LeafHash(addr, acc)
		// Non-inclusion: still return current root so client sees mismatch
		hashes := make([][]byte, len(leaves))
		for i := range leaves {
			hashes[i] = leaves[i].Hash
		}
		layers := buildMerkleLayers(hashes)
		root := hashHex(layers[len(layers)-1][0])
		return &MerkleProof{
			Address:   addr,
			Account:   acc,
			LeafHash:  hashHex(leafHash),
			LeafIndex: -1,
			LeafCount: len(leaves),
			Path:      nil,
			Sides:     nil,
			StateRoot: root,
		}, nil
	}

	hashes := make([][]byte, len(leaves))
	for i := range leaves {
		hashes[i] = leaves[i].Hash
	}
	layers := buildMerkleLayers(hashes)
	root := layers[len(layers)-1][0]

	var path []string
	var sides []string
	index := idx
	for layer := 0; layer < len(layers)-1; layer++ {
		level := layers[layer]
		sibling := index ^ 1 // flip last bit
		if sibling >= len(level) {
			// Odd end — sibling is self (duplicated)
			sibling = index
		}
		path = append(path, hashHex(level[sibling]))
		if sibling < index {
			sides = append(sides, "L")
		} else {
			sides = append(sides, "R")
		}
		index /= 2
	}

	return &MerkleProof{
		Address:   addr,
		Account:   acc,
		LeafHash:  hashHex(leafHash),
		LeafIndex: idx,
		LeafCount: len(leaves),
		Path:      path,
		Sides:     sides,
		StateRoot: hashHex(root),
	}, nil
}

// VerifyMerkleProof checks that proof.Account hashes to proof.LeafHash and that
// walking Path yields proof.StateRoot. Pure function — no store access.
func VerifyMerkleProof(proof *MerkleProof) error {
	if proof == nil {
		return fmt.Errorf("nil proof")
	}
	if proof.LeafIndex < 0 {
		return fmt.Errorf("account not included in state tree (non-membership)")
	}
	expectedLeaf := LeafHash(proof.Address, proof.Account)
	gotLeaf, err := decodeHex32(proof.LeafHash)
	if err != nil {
		return fmt.Errorf("leaf_hash: %w", err)
	}
	if hex.EncodeToString(expectedLeaf) != hex.EncodeToString(gotLeaf) {
		return fmt.Errorf("leaf hash does not match account fields")
	}

	current := expectedLeaf
	if len(proof.Path) != len(proof.Sides) {
		return fmt.Errorf("path/sides length mismatch")
	}
	for i, sibHex := range proof.Path {
		sib, err := decodeHex32(sibHex)
		if err != nil {
			return fmt.Errorf("path[%d]: %w", i, err)
		}
		switch proof.Sides[i] {
		case "L":
			current = parentHash(sib, current)
		case "R":
			current = parentHash(current, sib)
		default:
			return fmt.Errorf("path[%d]: invalid side %q", i, proof.Sides[i])
		}
	}
	root, err := decodeHex32(proof.StateRoot)
	if err != nil {
		return fmt.Errorf("state_root: %w", err)
	}
	if hex.EncodeToString(current) != hex.EncodeToString(root) {
		return fmt.Errorf("proof does not reconstruct state root")
	}
	return nil
}
