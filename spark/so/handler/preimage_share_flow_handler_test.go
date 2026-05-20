package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"testing"
	"time"

	btcecdsa "github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	eciesgo "github.com/ecies/go/v2"
	"github.com/lightningnetwork/lnd/lnwire"
	"github.com/lightningnetwork/lnd/zpay32"
	"github.com/lightsparkdev/spark/common/keys"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestValidatePreimageShareRejectsShortProofSet(t *testing.T) {
	config := preimageShareFlowTestConfig()
	preimageBytes, err := hex.DecodeString("2d059c3ede82a107aa1452c0bea47759be3c5c6e5342be6a310f6c3a907d9f4c")
	require.NoError(t, err)
	paymentHash := sha256.Sum256(preimageBytes)
	shares, err := secretsharing.SplitSecretWithProofs(
		new(big.Int).SetBytes(preimageBytes),
		secp256k1.S256().N,
		int(config.Threshold),
		2,
	)
	require.NoError(t, err)
	secretShare := shares[int(config.Index)].MarshalProto()
	secretShare.Proofs = secretShare.Proofs[:1]
	shareBytes, err := proto.Marshal(secretShare)
	require.NoError(t, err)
	publicKey, err := eciesgo.NewPublicKeyFromBytes(config.IdentityPublicKey().Serialize())
	require.NoError(t, err)
	encryptedShare, err := eciesgo.Encrypt(publicKey, shareBytes)
	require.NoError(t, err)

	_, err = validatePreimageShare(config, &pb.StorePreimageShareV2Request{
		PaymentHash: paymentHash[:],
		EncryptedPreimageShares: map[string][]byte{
			config.Identifier: encryptedShare,
		},
		Threshold:             uint32(config.Threshold),
		InvoiceString:         lightningInvoiceForPaymentHash(t, paymentHash),
		UserIdentityPublicKey: keys.GeneratePrivateKey().Public().Serialize(),
	})
	require.ErrorContains(t, err, "invalid VSS proof length")
}

func preimageShareFlowTestConfig() *so.Config {
	identityKey := keys.GeneratePrivateKey()
	identifier := "operator-1"
	return &so.Config{
		Identifier:         identifier,
		IdentityPrivateKey: identityKey,
		Threshold:          2,
		Index:              0,
		SigningOperatorMap: map[string]*so.SigningOperator{
			identifier: {
				Identifier:        identifier,
				IdentityPublicKey: identityKey.Public(),
			},
		},
	}
}

func lightningInvoiceForPaymentHash(t *testing.T, paymentHash [32]byte) string {
	t.Helper()
	invoice, err := zpay32.NewInvoice(
		&chaincfg.RegressionNetParams,
		paymentHash,
		time.Unix(1700000000, 0),
		zpay32.Amount(lnwire.MilliSatoshi(123450000)),
		zpay32.Description("preimage share flow test"),
	)
	require.NoError(t, err)
	nodeKey := keys.GeneratePrivateKey().ToBTCEC()
	encoded, err := invoice.Encode(zpay32.MessageSigner{
		SignCompact: func(msg []byte) ([]byte, error) {
			return btcecdsa.SignCompact(nodeKey, msg, true), nil
		},
	})
	require.NoError(t, err)
	return encoded
}
