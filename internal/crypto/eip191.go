package crypto

import (
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

// BuildChainSigningMessage matches east-wallet chain-signing.ts:
//   EASTCHAIN_BLOCK|{height}|{blockHash}
func BuildChainSigningMessage(height uint64, blockHash string) string {
	return fmt.Sprintf("EASTCHAIN_BLOCK|%d|%s", height, blockHash)
}

// BuildProposalMessage — what a Fullnode Browser signs when proposing a block.
// Keep stable; any change breaks existing proposers.
func BuildProposalMessage(proposalID string, height uint64, blockHash string) string {
	return fmt.Sprintf("EASTCHAIN_PROPOSAL|%s|%d|%s", proposalID, height, blockHash)
}

// BuildTxMessage — what a wallet/client signs to prove ownership of the
// `from` address for a submitted transaction. The tx hash already commits
// to type/from/to/amount/nonce/timestamp (see tx.Transaction.Hash), so
// signing the hash is sufficient — no need to repeat every field here.
// Keep stable; any change breaks every existing wallet integration.
func BuildTxMessage(txHash string) string {
	return fmt.Sprintf("EASTCHAIN_TX|%s", txHash)
}

// BuildHeartbeatMessage — what a node signs to prove it owns the address
// claiming uptime credit. Without this, anyone could POST /heartbeat on
// behalf of any address and inflate its uptime score.
func BuildHeartbeatMessage(address, nodeID string, unixMs int64) string {
	return fmt.Sprintf("EASTCHAIN_HEARTBEAT|%s|%s|%d", address, nodeID, unixMs)
}

// BuildValidatorRegisterMessage — what a node signs to prove it owns the
// address it's registering as a block-producing validator. Without this,
// anyone could register someone else's already-staked address into their
// own leader schedule.
func BuildValidatorRegisterMessage(address string, unixMs int64) string {
	return fmt.Sprintf("EASTCHAIN_VALIDATOR_REGISTER|%s|%d", address, unixMs)
}

// VerifyEIP191 recovers the signer address from an EIP-191 personal_sign signature
// and checks it equals expectedAddress (case-insensitive).
func VerifyEIP191(message, signatureHex, expectedAddress string) (bool, error) {
	recovered, err := RecoverAddress(message, signatureHex)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(recovered, expectedAddress), nil
}

// RecoverAddress returns the 0x-prefixed checksummed address that signed message.
func RecoverAddress(message, signatureHex string) (string, error) {
	sig, err := hexutil.Decode(signatureHex)
	if err != nil {
		return "", fmt.Errorf("invalid signature hex: %w", err)
	}
	if len(sig) != 65 {
		return "", fmt.Errorf("signature must be 65 bytes, got %d", len(sig))
	}

	// go-ethereum expects V in {0,1} for SigToPub; MetaMask/ethers often use 27/28
	if sig[64] >= 27 {
		sig[64] -= 27
	}

	hash := crypto.Keccak256([]byte(fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(message), message)))
	pub, err := crypto.SigToPub(hash, sig)
	if err != nil {
		return "", fmt.Errorf("recover public key: %w", err)
	}
	addr := crypto.PubkeyToAddress(*pub)
	return addr.Hex(), nil
}

// SignEIP191 signs message with a secp256k1 private key (hex, optional 0x prefix).
// Returns 0x-prefixed 65-byte signature (r||s||v) compatible with ethers.verifyMessage.
func SignEIP191(message, privateKeyHex string) (string, error) {
	pkHex := strings.TrimPrefix(privateKeyHex, "0x")
	key, err := crypto.HexToECDSA(pkHex)
	if err != nil {
		return "", fmt.Errorf("invalid private key: %w", err)
	}
	hash := crypto.Keccak256([]byte(fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(message), message)))
	sig, err := crypto.Sign(hash, key)
	if err != nil {
		return "", err
	}
	// crypto.Sign returns v in {0,1}; ethers expects 27/28
	sig[64] += 27
	return hexutil.Encode(sig), nil
}

// AddressFromPrivateKey derives the EVM address for a private key hex.
func AddressFromPrivateKey(privateKeyHex string) (string, error) {
	pkHex := strings.TrimPrefix(privateKeyHex, "0x")
	key, err := crypto.HexToECDSA(pkHex)
	if err != nil {
		return "", err
	}
	return crypto.PubkeyToAddress(key.PublicKey).Hex(), nil
}

// IsValidAddress checks basic 0x + 40 hex form.
func IsValidAddress(addr string) bool {
	return common.IsHexAddress(addr)
}

// BuildMintMessage is what the mining/staking oracle signs to authorize a
// claim_* mint from a supply bucket. Keep stable — wallet + Vercel oracle
// must produce the exact same string.
//
//   EASTCHAIN_MINT|{bucket}|{beneficiary}|{amount}|{nonce}|{epoch_id}
//
// amount is human EAST (same unit as genesis bucket caps), not 6-decimal
// subunits — see tx.ClaimMiningPayload and state.ApplyTx claim_mining branch.
func BuildMintMessage(bucket, beneficiary string, amount int64, nonce uint64, epochID int64) string {
	return fmt.Sprintf("EASTCHAIN_MINT|%s|%s|%d|%d|%d", bucket, beneficiary, amount, nonce, epochID)
}
