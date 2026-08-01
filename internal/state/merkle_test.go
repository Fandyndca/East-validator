package state

import (
	"testing"
)

func TestMerkleProofRoundTrip(t *testing.T) {
	// Build synthetic leaves without Badger
	leaves := []accountLeaf{
		{Addr: "0xaaa", Acc: Account{Balance: 100, Nonce: 1}, Hash: LeafHash("0xaaa", Account{Balance: 100, Nonce: 1})},
		{Addr: "0xbbb", Acc: Account{Balance: 200, Nonce: 2}, Hash: LeafHash("0xbbb", Account{Balance: 200, Nonce: 2})},
		{Addr: "0xccc", Acc: Account{Balance: 300, Nonce: 3}, Hash: LeafHash("0xccc", Account{Balance: 300, Nonce: 3})},
	}
	hashes := make([][]byte, len(leaves))
	for i := range leaves {
		hashes[i] = leaves[i].Hash
	}
	layers := buildMerkleLayers(hashes)
	root := layers[len(layers)-1][0]

	// Prove index 1 (0xbbb)
	idx := 1
	var path []string
	var sides []string
	index := idx
	for layer := 0; layer < len(layers)-1; layer++ {
		level := layers[layer]
		sibling := index ^ 1
		if sibling >= len(level) {
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

	proof := &MerkleProof{
		Address:   "0xbbb",
		Account:   leaves[1].Acc,
		LeafHash:  hashHex(leaves[1].Hash),
		LeafIndex: 1,
		LeafCount: 3,
		Path:      path,
		Sides:     sides,
		StateRoot: hashHex(root),
	}
	if err := VerifyMerkleProof(proof); err != nil {
		t.Fatalf("verify: %v", err)
	}

	// Tamper balance → must fail
	proof.Account.Balance = 999
	if err := VerifyMerkleProof(proof); err == nil {
		t.Fatal("expected failure after tamper")
	}
}

func TestEmptyTreeRootStable(t *testing.T) {
	layers := buildMerkleLayers(nil)
	if len(layers) != 1 || len(layers[0]) != 1 {
		t.Fatalf("unexpected empty layers: %+v", layers)
	}
	a := hashHex(layers[0][0])
	b := hashHex(emptyLeaf())
	if a != b {
		t.Fatalf("empty root mismatch %s vs %s", a, b)
	}
}
