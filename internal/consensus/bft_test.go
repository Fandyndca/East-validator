package consensus

import (
	"testing"

	gethcrypto "github.com/ethereum/go-ethereum/crypto"
)

func TestQuorumSize(t *testing.T) {
	cases := []struct {
		n    int
		want int
	}{
		{0, 0},
		{1, 1},
		{2, 2}, // floor(4/3)+1 = 2
		{3, 3}, // floor(6/3)+1 = 3
		{4, 3}, // floor(8/3)+1 = 3
		{7, 5}, // floor(14/3)+1 = 5
	}
	for _, c := range cases {
		if got := QuorumSize(c.n); got != c.want {
			t.Errorf("QuorumSize(%d)=%d want %d", c.n, got, c.want)
		}
	}
}

func TestVoteSignAndVerify(t *testing.T) {
	key, err := gethcrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	addr := gethcrypto.PubkeyToAddress(key.PublicKey).Hex()
	privHex := "0x" + encodeHex(gethcrypto.FromECDSA(key))

	v, err := SignVote(VotePrevote, 10, 0, "0xabc", addr, privHex, 1_700_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyVote(v); err != nil {
		t.Fatalf("VerifyVote: %v", err)
	}

	// Tamper
	v.BlockHash = "0xevil"
	if err := VerifyVote(v); err == nil {
		t.Fatal("expected verify failure after tamper")
	}
}

func TestProposalSignAndVerify(t *testing.T) {
	key, err := gethcrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	addr := gethcrypto.PubkeyToAddress(key.PublicKey).Hex()
	privHex := "0x" + encodeHex(gethcrypto.FromECDSA(key))

	p := &Proposal{
		Height:     1,
		Round:      0,
		BlockHash:  "0xdead",
		PrevHash:   "GENESIS",
		MerkleRoot: "0xempty",
		Timestamp:  1,
		Proposer:   addr,
		POLRound:   -1,
	}
	if err := SignProposalBFT(p, privHex); err != nil {
		t.Fatal(err)
	}
	if err := VerifyProposalBFT(p); err != nil {
		t.Fatalf("VerifyProposalBFT: %v", err)
	}
}

func encodeHex(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexdigits[v>>4]
		out[i*2+1] = hexdigits[v&0x0f]
	}
	return string(out)
}
