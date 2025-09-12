package common

import (
	"slices"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/lightsparkdev/spark/common/keys"
)

// ProofOfPossessionMessageHashForDepositAddress generates a hash of the proof of possession message for a deposit address.
func ProofOfPossessionMessageHashForDepositAddress(userPubKey, operatorPubKey keys.Public, depositAddress []byte) []byte {
	proofMsg := slices.Concat(userPubKey.Serialize(), operatorPubKey.Serialize(), depositAddress)
	return chainhash.HashB(proofMsg)
}
