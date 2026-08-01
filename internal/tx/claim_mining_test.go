package tx

import (
	"crypto/ecdsa"
	"encoding/hex"
	"os"
	"testing"

	"github.com/eastchain/east-validator/internal/crypto"
	gethcrypto "github.com/ethereum/go-ethereum/crypto"
)

func privHex(key *ecdsa.PrivateKey) string {
	return "0x" + hex.EncodeToString(gethcrypto.FromECDSA(key))
}

func TestClaimMiningPayloadRoundTrip(t *testing.T) {
	raw := map[string]any{
		"bucket":           "mining",
		"epoch_id":         float64(7),
		"oracle_signature": "0xabc",
	}
	tr := &Transaction{Type: TxClaimMining, From: "0x1", Amount: 5, Payload: raw}
	p, err := tr.ParseClaimMiningPayload()
	if err != nil {
		t.Fatal(err)
	}
	if p.Bucket != "mining" || p.EpochID != 7 {
		t.Fatalf("unexpected payload: %+v", p)
	}
}

func TestClaimMiningValidateWithOracle(t *testing.T) {
	oracleKey, err := gethcrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	oracleAddr := gethcrypto.PubkeyToAddress(oracleKey.PublicKey).Hex()
	userKey, err := gethcrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	userAddr := gethcrypto.PubkeyToAddress(userKey.PublicKey).Hex()

	t.Setenv("MINING_ORACLE_ADDRESS", oracleAddr)

	amount := int64(10)
	nonce := uint64(1)
	epoch := int64(42)
	msg := crypto.BuildMintMessage("mining", userAddr, amount, nonce, epoch)
	oracleSig, err := crypto.SignEIP191(msg, privHex(oracleKey))
	if err != nil {
		t.Fatal(err)
	}

	tr := &Transaction{
		Type:      TxClaimMining,
		From:      userAddr,
		Amount:    amount,
		Nonce:     nonce,
		Timestamp: 1_700_000_000_000,
		Payload: ClaimMiningPayload{
			Bucket:          "mining",
			EpochID:         epoch,
			OracleSignature: oracleSig,
		},
	}
	userSig, err := crypto.SignEIP191(crypto.BuildTxMessage(tr.Hash()), privHex(userKey))
	if err != nil {
		t.Fatal(err)
	}
	tr.Signature = userSig

	if err := tr.ValidateBasic(); err != nil {
		t.Fatalf("ValidateBasic: %v", err)
	}

	t.Setenv("MINING_ORACLE_ADDRESS", "0x0000000000000000000000000000000000000001")
	if err := tr.ValidateBasic(); err == nil {
		t.Fatal("expected oracle mismatch error")
	}
}

func TestClaimMiningDisabledWithoutOracleEnv(t *testing.T) {
	os.Unsetenv("MINING_ORACLE_ADDRESS")
	tr := &Transaction{
		Type:      TxClaimMining,
		From:      "0x1111111111111111111111111111111111111111",
		Amount:    1,
		Nonce:     1,
		Timestamp: 1,
		Payload: ClaimMiningPayload{
			Bucket:          "mining",
			EpochID:         1,
			OracleSignature: "0x00",
		},
	}
	if err := tr.ValidateBasic(); err == nil {
		t.Fatal("expected disabled without MINING_ORACLE_ADDRESS")
	}
}
