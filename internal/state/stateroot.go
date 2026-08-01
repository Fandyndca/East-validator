package state

// ComputeStateRoot returns the Merkle state root (binary tree over accounts).
// Preferred name used by seal paths; equivalent to ComputeMerkleStateRoot.
func (s *Store) ComputeStateRoot() (string, error) {
	return s.ComputeMerkleStateRoot()
}

// ProveAccount returns a Merkle inclusion proof for address.
// Prefer this over the old flat-hash proof.
func (s *Store) ProveAccount(addr string) (*MerkleProof, error) {
	return s.ProveAccountMerkle(addr)
}

// AccountProof is kept as an alias name in docs; use MerkleProof.
// Deprecated: use MerkleProof via ProveAccount.
type AccountProof = MerkleProof
