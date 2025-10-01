package utils

import (
	"bytes"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/protohash"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func makeMinimalV3MintPartial(t *testing.T, rng io.Reader) *tokenpb.TokenTransaction {
	t.Helper()
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	issuerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	operatorPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	tokenID := bytes.Repeat([]byte{0xAA}, 32)

	return &tokenpb.TokenTransaction{
		Version: 3,
		TokenInputs: &tokenpb.TokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{
				IssuerPublicKey: issuerPubKey.Serialize(),
				TokenIdentifier: tokenID,
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{
			{
				OwnerPublicKey:  ownerPubKey.Serialize(),
				TokenIdentifier: tokenID,
				TokenAmount:     bytes.Repeat([]byte{0x01}, 16),
			},
		},
		SparkOperatorIdentityPublicKeys: [][]byte{operatorPubKey.Serialize()},
		Network:                         sparkpb.Network_MAINNET,
		ClientCreatedTimestamp:          timestamppb.Now(),
		InvoiceAttachments: []*tokenpb.InvoiceAttachment{
			{SparkInvoice: "a"}, {SparkInvoice: "b"},
		},
	}
}

func TestHashTokenTransactionV3_PartialTransactionComputation(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	tx := makeMinimalV3MintPartial(t, rng)
	// Add fields that must be stripped for partial hash
	tx.ExpiryTime = timestamppb.Now()
	out := tx.TokenOutputs[0]
	out.Id = proto.String("550e8400-e29b-41d4-a716-446655440000")
	out.RevocationCommitment = bytes.Repeat([]byte{0x04}, 33)
	out.WithdrawBondSats = proto.Uint64(100)
	out.WithdrawRelativeBlockLocktime = proto.Uint64(42)

	// Hash via V3 partial (which strips fields internally)
	partialViaFunc, err := HashTokenTransactionV3(tx, true)
	require.NoError(t, err)

	// Manually strip on a clone and hash. Ensure it matches.
	stripped := proto.CloneOf(tx)
	stripped.ExpiryTime = nil
	sout := stripped.TokenOutputs[0]
	sout.Id = nil
	sout.RevocationCommitment = nil
	sout.WithdrawBondSats = nil
	sout.WithdrawRelativeBlockLocktime = nil

	partialViaAuto, err := protohash.Hash(stripped)
	require.NoError(t, err)
	require.Equal(t, partialViaFunc, partialViaAuto, "partial hash mismatch between V3 path and stripped auto hasher")
}

func TestValidatePartialTokenTransaction_V3Ordering_Valid(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	base := makeMinimalV3MintPartial(t, rng)
	expectedOps := map[string]*sparkpb.SigningOperatorInfo{
		"op": {Identifier: "op", PublicKey: base.SparkOperatorIdentityPublicKeys[0]},
	}
	sigs := []*tokenpb.SignatureWithIndex{{Signature: bytes.Repeat([]byte{0x11}, 64), InputIndex: 0}}

	err := ValidatePartialTokenTransaction(base, sigs, expectedOps, []common.Network{common.Mainnet}, false, false)
	require.NoError(t, err)
}

func TestValidatePartialTokenTransaction_V3Ordering_OperatorKeysOutOfOrder(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	base := makeMinimalV3MintPartial(t, rng)
	sigs := []*tokenpb.SignatureWithIndex{{Signature: bytes.Repeat([]byte{0x11}, 64), InputIndex: 0}}
	badOps := proto.CloneOf(base)
	badOps.SparkOperatorIdentityPublicKeys = [][]byte{bytes.Repeat([]byte{0x05}, 33), bytes.Repeat([]byte{0x01}, 33)}
	expectedOps := map[string]*sparkpb.SigningOperatorInfo{
		"a": {Identifier: "a", PublicKey: badOps.SparkOperatorIdentityPublicKeys[0]},
		"b": {Identifier: "b", PublicKey: badOps.SparkOperatorIdentityPublicKeys[1]},
	}

	err := ValidatePartialTokenTransaction(badOps, sigs, expectedOps, []common.Network{common.Mainnet}, false, false)
	require.Error(t, err)
}

func TestValidatePartialTokenTransaction_V3Ordering_InvoiceAttachmentsOutOfOrder(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	base := makeMinimalV3MintPartial(t, rng)
	expectedOps := map[string]*sparkpb.SigningOperatorInfo{
		"op": {Identifier: "op", PublicKey: base.SparkOperatorIdentityPublicKeys[0]},
	}
	sigs := []*tokenpb.SignatureWithIndex{{Signature: bytes.Repeat([]byte{0x11}, 64), InputIndex: 0}}
	badInv := proto.CloneOf(base)
	badInv.InvoiceAttachments = []*tokenpb.InvoiceAttachment{{SparkInvoice: "b"}, {SparkInvoice: "a"}}

	err := ValidatePartialTokenTransaction(badInv, sigs, expectedOps, []common.Network{common.Mainnet}, false, false)
	require.Error(t, err)
}

func TestValidatePartialTokenTransaction_TokenAmountLen_Not16(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	base := makeMinimalV3MintPartial(t, rng)
	// Make amount invalid (15 bytes)
	base.TokenOutputs[0].TokenAmount = bytes.Repeat([]byte{0x01}, 15)

	expectedOps := map[string]*sparkpb.SigningOperatorInfo{
		"op": {Identifier: "op", PublicKey: base.SparkOperatorIdentityPublicKeys[0]},
	}
	sigs := []*tokenpb.SignatureWithIndex{{Signature: bytes.Repeat([]byte{0x11}, 64), InputIndex: 0}}

	err := ValidatePartialTokenTransaction(base, sigs, expectedOps, []common.Network{common.Mainnet}, false, false)
	require.ErrorContains(t, err, "token amount must be exactly 16 bytes")
}

func TestValidatePartialTokenTransaction_TokenIdentifierLen_Not32(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	base := makeMinimalV3MintPartial(t, rng)
	// Make identifier invalid (31 bytes)
	badID := bytes.Repeat([]byte{0xAA}, 31)
	base.TokenOutputs[0].TokenIdentifier = badID
	base.TokenInputs = &tokenpb.TokenTransaction_MintInput{MintInput: &tokenpb.TokenMintInput{
		IssuerPublicKey: bytes.Repeat([]byte{0x03}, 33),
		TokenIdentifier: badID,
	}}

	expectedOps := map[string]*sparkpb.SigningOperatorInfo{
		"op": {Identifier: "op", PublicKey: base.SparkOperatorIdentityPublicKeys[0]},
	}
	sigs := []*tokenpb.SignatureWithIndex{{Signature: bytes.Repeat([]byte{0x11}, 64), InputIndex: 0}}

	err := ValidatePartialTokenTransaction(base, sigs, expectedOps, []common.Network{common.Mainnet}, false, false)
	require.ErrorContains(t, err, "token identifier must be exactly 32 bytes")
}

func TestValidatePartialTokenTransaction_TransferAmount_NotZero(t *testing.T) {
	// Build a minimal transfer partial with zero amount to trigger validation
	rng := rand.NewChaCha8([32]byte{})
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	operatorPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	prev := bytes.Repeat([]byte{0xAB}, 32)

	tx := &tokenpb.TokenTransaction{
		Version: 3,
		TokenInputs: &tokenpb.TokenTransaction_TransferInput{
			TransferInput: &tokenpb.TokenTransferInput{
				OutputsToSpend: []*tokenpb.TokenOutputToSpend{{
					PrevTokenTransactionHash: prev,
					PrevTokenTransactionVout: 0,
				}},
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{{
			OwnerPublicKey:  ownerPubKey.Serialize(),
			TokenAmount:     bytes.Repeat([]byte{0x00}, 16),
			TokenIdentifier: bytes.Repeat([]byte{0xAA}, 32),
		}},
		SparkOperatorIdentityPublicKeys: [][]byte{operatorPubKey.Serialize()},
		Network:                         sparkpb.Network_MAINNET,
		ClientCreatedTimestamp:          timestamppb.Now(),
	}

	expectedOps := map[string]*sparkpb.SigningOperatorInfo{
		"op": {Identifier: "op", PublicKey: tx.SparkOperatorIdentityPublicKeys[0]},
	}
	sigs := []*tokenpb.SignatureWithIndex{{Signature: bytes.Repeat([]byte{0x11}, 64), InputIndex: 0}}

	err := ValidatePartialTokenTransaction(tx, sigs, expectedOps, []common.Network{common.Mainnet}, false, false)
	require.ErrorContains(t, err, "output 0 token amount cannot be 0")
}
