package common

import (
	"bytes"
	"crypto/sha256"
)

// ProofOfPossessionMessageHashForDepositAddress generates a hash of the proof of possession message for a deposit address.
func ProofOfPossessionMessageHashForDepositAddress(userPubkey, operatorPubkey, depositAddress []byte) []byte {
	proofMsg := bytes.Join([][]byte{
		userPubkey,
		operatorPubkey,
		depositAddress,
	}, nil)
	hash := sha256.Sum256(proofMsg)
	return hash[:]
}
