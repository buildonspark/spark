package utils

import (
	"bytes"
	"testing"

	"github.com/lightsparkdev/spark/common"
	protohash "github.com/lightsparkdev/spark/common/protohash"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func makeBytes(b byte, n int) []byte {
	bs := make([]byte, n)
	for i := range bs {
		bs[i] = b
	}
	return bs
}

func makeMinimalV3MintPartial(t *testing.T) *tokenpb.TokenTransaction {
	t.Helper()
	owner := makeBytes(0x02, 33)
	issuer := makeBytes(0x03, 33)
	operator := makeBytes(0x01, 33)

	tokenID := makeBytes(0xAA, 32)

	tx := &tokenpb.TokenTransaction{
		Version: 3,
		TokenInputs: &tokenpb.TokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{
				IssuerPublicKey: issuer,
				TokenIdentifier: tokenID,
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{
			{
				OwnerPublicKey:  owner,
				TokenIdentifier: tokenID,
				TokenAmount:     makeBytes(0x01, 16),
			},
		},
		SparkOperatorIdentityPublicKeys: [][]byte{operator},
		Network:                         sparkpb.Network_MAINNET,
		ClientCreatedTimestamp:          timestamppb.Now(),
		InvoiceAttachments: []*tokenpb.InvoiceAttachment{
			{SparkInvoice: "a"}, {SparkInvoice: "b"},
		},
	}
	return tx
}

func clone[T proto.Message](m T) T { return proto.Clone(m).(T) }

func TestHashTokenTransactionV3_PartialTransactionComputation(t *testing.T) {
	tx := makeMinimalV3MintPartial(t)
	// Add fields that must be stripped for partial hash
	tx.ExpiryTime = timestamppb.Now()
	out := tx.TokenOutputs[0]
	id := "550e8400-e29b-41d4-a716-446655440000"
	out.Id = &id
	out.RevocationCommitment = makeBytes(0x04, 33)
	wb := uint64(100)
	out.WithdrawBondSats = &wb
	wl := uint64(42)
	out.WithdrawRelativeBlockLocktime = &wl

	// Hash via V3 partial (which strips fields internally)
	partialViaFunc, err := HashTokenTransactionV3(tx, true)
	if err != nil {
		t.Fatalf("partial v3 hash failed: %v", err)
	}

	// Manually strip on a clone and hash. Ensure it matches.
	stripped := clone(tx)
	stripped.ExpiryTime = nil
	sout := stripped.TokenOutputs[0]
	sout.Id = nil
	sout.RevocationCommitment = nil
	sout.WithdrawBondSats = nil
	sout.WithdrawRelativeBlockLocktime = nil

	partialViaAuto, err := protohash.Hash(stripped)
	if err != nil {
		t.Fatalf("auto partial hash failed: %v", err)
	}

	if !bytes.Equal(partialViaFunc, partialViaAuto) {
		t.Fatalf("partial hash mismatch between V3 path and stripped auto hasher")
	}
}

func TestValidatePartialTokenTransaction_V3Ordering_Valid(t *testing.T) {
	base := makeMinimalV3MintPartial(t)
	expectedOps := map[string]*sparkpb.SigningOperatorInfo{
		"op": {Identifier: "op", PublicKey: base.SparkOperatorIdentityPublicKeys[0]},
	}
	sigs := []*tokenpb.SignatureWithIndex{{Signature: makeBytes(0x11, 64), InputIndex: 0}}
	if err := ValidatePartialTokenTransaction(base, sigs, expectedOps, []common.Network{common.Mainnet}, false, false); err != nil {
		t.Fatalf("expected valid ordering to pass, got error: %v", err)
	}
}

func TestValidatePartialTokenTransaction_V3Ordering_OperatorKeysOutOfOrder(t *testing.T) {
	base := makeMinimalV3MintPartial(t)
	sigs := []*tokenpb.SignatureWithIndex{{Signature: makeBytes(0x11, 64), InputIndex: 0}}
	badOps := clone(base)
	badOps.SparkOperatorIdentityPublicKeys = [][]byte{makeBytes(0x05, 33), makeBytes(0x01, 33)}
	expectedOps := map[string]*sparkpb.SigningOperatorInfo{
		"a": {Identifier: "a", PublicKey: badOps.SparkOperatorIdentityPublicKeys[0]},
		"b": {Identifier: "b", PublicKey: badOps.SparkOperatorIdentityPublicKeys[1]},
	}
	if err := ValidatePartialTokenTransaction(badOps, sigs, expectedOps, []common.Network{common.Mainnet}, false, false); err == nil {
		t.Fatalf("expected operator key ordering validation error, got nil")
	}
}

func TestValidatePartialTokenTransaction_V3Ordering_InvoiceAttachmentsOutOfOrder(t *testing.T) {
	base := makeMinimalV3MintPartial(t)
	expectedOps := map[string]*sparkpb.SigningOperatorInfo{
		"op": {Identifier: "op", PublicKey: base.SparkOperatorIdentityPublicKeys[0]},
	}
	sigs := []*tokenpb.SignatureWithIndex{{Signature: makeBytes(0x11, 64), InputIndex: 0}}
	badInv := clone(base)
	badInv.InvoiceAttachments = []*tokenpb.InvoiceAttachment{{SparkInvoice: "b"}, {SparkInvoice: "a"}}
	if err := ValidatePartialTokenTransaction(badInv, sigs, expectedOps, []common.Network{common.Mainnet}, false, false); err == nil {
		t.Fatalf("expected invoice ordering validation error, got nil")
	}
}
