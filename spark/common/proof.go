package common

import (
	"slices"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/lightsparkdev/spark/common/hashstructure"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
)

// ProofOfPossessionMessageHashForDepositAddress generates a hash of the proof of possession message for a deposit address.
func ProofOfPossessionMessageHashForDepositAddress(userPubKey, operatorPubKey keys.Public, depositAddress []byte, hashVariant pb.HashVariant) []byte {
	if hashVariant == pb.HashVariant_HASH_VARIANT_V2 {
		return proofOfPossessionMessageHashForDepositAddressV2(userPubKey, operatorPubKey, depositAddress)
	}
	return proofOfPossessionMessageHashForDepositAddressLegacy(userPubKey, operatorPubKey, depositAddress)
}

func proofOfPossessionMessageHashForDepositAddressLegacy(userPubKey, operatorPubKey keys.Public, depositAddress []byte) []byte {
	proofMsg := slices.Concat(userPubKey.Serialize(), operatorPubKey.Serialize(), depositAddress)
	return chainhash.HashB(proofMsg)
}

func proofOfPossessionMessageHashForDepositAddressV2(userPubKey, operatorPubKey keys.Public, depositAddress []byte) []byte {
	return hashstructure.NewHasher([]string{"spark", "deposit", "proof_of_possession"}).
		AddBytes(userPubKey.Serialize()).
		AddBytes(operatorPubKey.Serialize()).
		AddBytes(depositAddress).
		Hash()
}
