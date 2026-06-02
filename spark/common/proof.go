package common

import (
	"slices"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/lightsparkdev/spark/common/hashstructure"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/sighash"
	pb "github.com/lightsparkdev/spark/proto/spark"
)

// ProofOfPossessionMessageHashForDepositAddress generates the sighash that an operator FROST-signs to prove knowledge
// of its secret share for a deposit address.
func ProofOfPossessionMessageHashForDepositAddress(userPubKey, operatorPubKey keys.Public, depositAddress []byte, hashVariant pb.HashVariant) sighash.Hash {
	var raw []byte
	if hashVariant == pb.HashVariant_HASH_VARIANT_V2 {
		raw = proofOfPossessionMessageHashForDepositAddressV2(userPubKey, operatorPubKey, depositAddress)
	} else {
		raw = proofOfPossessionMessageHashForDepositAddressLegacy(userPubKey, operatorPubKey, depositAddress)
	}
	// Both branches return sha256 hashes, which are always 32 bytes; Parse can't fail here.
	h, _ := sighash.Parse(raw)
	return h
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
