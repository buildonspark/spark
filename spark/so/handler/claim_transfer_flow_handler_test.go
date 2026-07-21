package handler

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
)

// TestClaimTransferValidateDecisionAgainstPrepare directly covers the
// CLAIM_TRANSFER binding fence: it binds both the transfer_id (UUID-aware, so a
// non-canonical prepared id still matches) and the receiver_identity_public_key
// that selects the MIMO receiver row.
func TestClaimTransferValidateDecisionAgainstPrepare(t *testing.T) {
	handler := NewClaimTransferFlowHandler(sparktesting.TestConfig(t))
	transferID := uuid.New().String()
	receiverKey := keys.GeneratePrivateKey().Public()
	receiver := receiverKey.Serialize()
	prepare := &pbinternal.ClaimTransferPrepareRequest{
		OriginalRequest: &pb.ClaimTransferRequest{
			TransferId:             strings.ToUpper(transferID), // non-canonical
			OwnerIdentityPublicKey: receiver,
		},
	}

	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.ClaimTransferCommitRequest{
		TransferId: transferID, ReceiverIdentityPublicKey: receiver,
	}))
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.ClaimTransferRollbackRequest{
		TransferId: transferID, ReceiverIdentityPublicKey: receiver,
	}))
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, prepare))

	// Wrong transfer id — asserted on BOTH decision variants, whose validation
	// is copy-pasted.
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.ClaimTransferCommitRequest{
		TransferId: uuid.NewString(), ReceiverIdentityPublicKey: receiver,
	}), "does not match the prepared transfer id")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.ClaimTransferRollbackRequest{
		TransferId: uuid.NewString(), ReceiverIdentityPublicKey: receiver,
	}), "does not match the prepared transfer id")
	// Right transfer id, wrong receiver — the MIMO-receiver hijack this binding
	// closes, again on both variants.
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.ClaimTransferCommitRequest{
		TransferId: transferID, ReceiverIdentityPublicKey: keys.GeneratePrivateKey().Public().Serialize(),
	}), "receiver identity does not match")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.ClaimTransferRollbackRequest{
		TransferId: transferID, ReceiverIdentityPublicKey: keys.GeneratePrivateKey().Public().Serialize(),
	}), "receiver identity does not match")
	// Wrong op types.
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(&pbinternal.SendTransferPrepareRequest{}, &pbinternal.ClaimTransferCommitRequest{}), "unexpected prepare op type")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.SendTransferCommitRequest{}), "unexpected decision op type")

	// Presumed-abort path (the reconciler echoes the prepare shape as the
	// decision): both the transfer id and the receiver bind must still reject a
	// mismatch — this branch mutates a receiver-selected row too.
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.ClaimTransferPrepareRequest{
		OriginalRequest: &pb.ClaimTransferRequest{TransferId: uuid.NewString(), OwnerIdentityPublicKey: receiver},
	}), "does not match the prepared transfer id")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.ClaimTransferPrepareRequest{
		OriginalRequest: &pb.ClaimTransferRequest{TransferId: transferID, OwnerIdentityPublicKey: keys.GeneratePrivateKey().Public().Serialize()},
	}), "receiver identity does not match")

	// Fail-closed: an unparseable prepared transfer id or a malformed decision
	// receiver key is a mismatch, never a pass.
	require.Error(t, handler.ValidateDecisionAgainstPrepare(&pbinternal.ClaimTransferPrepareRequest{
		OriginalRequest: &pb.ClaimTransferRequest{TransferId: "not-a-uuid", OwnerIdentityPublicKey: receiver},
	}, &pbinternal.ClaimTransferCommitRequest{TransferId: transferID, ReceiverIdentityPublicKey: receiver}))
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.ClaimTransferCommitRequest{
		TransferId: transferID, ReceiverIdentityPublicKey: []byte{0x01, 0x02}, // unparseable key
	}), "receiver identity does not match")

	// Encoding mismatch: parseClaimTransferRequest accepts an uncompressed
	// (65-byte) owner key while BuildCommitPayload/RollbackPayload canonicalize
	// to compressed via Public.Serialize(). The receiver bind must compare the
	// underlying point, not raw bytes, or a valid uncompressed-supplied claim is
	// permanently fenced and stranded IN_FLIGHT.
	uncompressedPrepare := &pbinternal.ClaimTransferPrepareRequest{
		OriginalRequest: &pb.ClaimTransferRequest{
			TransferId:             transferID,
			OwnerIdentityPublicKey: receiverKey.ToBTCEC().SerializeUncompressed(), // 65-byte form
		},
	}
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(uncompressedPrepare, &pbinternal.ClaimTransferCommitRequest{
		TransferId: transferID, ReceiverIdentityPublicKey: receiver, // compressed decision key
	}))
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(uncompressedPrepare, &pbinternal.ClaimTransferRollbackRequest{
		TransferId: transferID, ReceiverIdentityPublicKey: receiver,
	}))
}
