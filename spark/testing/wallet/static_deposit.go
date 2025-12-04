package wallet

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/handler"
)

func CreateSspFixedQuoteSignature(
	transactionID string,
	outputIndex uint32,
	network btcnetwork.Network,
	creditAmountSats uint64,
	identityPrivateKey keys.Private,
) ([]byte, error) {
	hasher := sha256.New()

	// Writing to a sha256 never returns an error, so we don't need to check any of the errors below.
	// Add network value as UTF-8 bytes
	_, _ = hasher.Write([]byte(network.String()))

	// Add transaction ID as UTF-8 bytes
	_, _ = hasher.Write([]byte(transactionID))

	// Add output index as 4-byte unsigned integer (little-endian)
	_ = binary.Write(hasher, binary.LittleEndian, outputIndex)

	// Request type fixed amount
	_ = binary.Write(hasher, binary.LittleEndian, uint8(0))

	// Add credit amount as 8-byte unsigned integer (little-endian)
	_ = binary.Write(hasher, binary.LittleEndian, creditAmountSats)

	// Hash the payload with SHA-256
	hash := hasher.Sum(nil)

	// Sign the hash of the payload using ECDSA
	signature := ecdsa.Sign(identityPrivateKey.ToBTCEC(), hash[:])

	return signature.Serialize(), nil
}

func CreateUserSignature(
	transactionID string,
	outputIndex uint32,
	network btcnetwork.Network,
	requestType pb.UtxoSwapRequestType,
	creditAmountSats uint64,
	sspSignature []byte,
	identityPrivateKey keys.Private,
) []byte {
	hash := handler.CreateUserStatement(transactionID, outputIndex, network, requestType, creditAmountSats, sspSignature)
	// Sign the hash of the payload using ECDSA
	return ecdsa.Sign(identityPrivateKey.ToBTCEC(), hash).Serialize()
}
