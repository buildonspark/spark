package handler

import (
	"context"
	"math/big"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/distributed-lab/gripmock"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// createTestTransferForMIMO creates an ent.Transfer record with the given sender/receiver
// pubkeys and status. Used by all MIMO tests to set up the transfer under test.
func createTestTransferForMIMO(t *testing.T, ctx context.Context, client *ent.Client, senderPubKey, receiverPubKey keys.Public, status st.TransferStatus) *ent.Transfer {
	t.Helper()
	transfer, err := client.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetStatus(status).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(senderPubKey).
		SetReceiverIdentityPubkey(receiverPubKey).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)
	return transfer
}

//
// MIMO receiver tests
//

// TestClaimTransferMIMO_ReceiverPubkeyMismatch verifies that calling ClaimTransfer with a
// pubkey that doesn't match any TransferReceiver record on the transfer is rejected. The
// transfer has a receiver with receiverPubKey, but the request uses wrongPubKey.
func TestClaimTransferMIMO_ReceiverPubkeyMismatch(t *testing.T) {
	sparktesting.RequireGripMock(t)
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{11})
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	wrongPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderKeyTweaked)

	_, err := sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverPubKey).
		SetStatus(st.TransferReceiverStatusInitiated).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	req := &pb.ClaimTransferRequest{
		TransferId:             transfer.ID.String(),
		OwnerIdentityPublicKey: wrongPubKey.Serialize(),
		ClaimPackage: &pb.ClaimPackage{
			LeavesToClaim:   []*pb.UserSignedTxSigningJob{},
			KeyTweakPackage: map[string][]byte{"so1": []byte("data")},
		},
	}
	_, err = handler.ClaimTransfer(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no transfer receivers found for transfer")
}

// TestClaimTransferMIMO_AlreadyCompleted verifies that a receiver who has already claimed a
// transfer (status Completed) cannot claim it again.
func TestClaimTransferMIMO_AlreadyCompleted(t *testing.T) {
	sparktesting.RequireGripMock(t)
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{12})
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderKeyTweaked)

	_, err := sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverPubKey).
		SetStatus(st.TransferReceiverStatusCompleted).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	req := &pb.ClaimTransferRequest{
		TransferId:             transfer.ID.String(),
		OwnerIdentityPublicKey: receiverPubKey.Serialize(),
		ClaimPackage: &pb.ClaimPackage{
			LeavesToClaim:   []*pb.UserSignedTxSigningJob{},
			KeyTweakPackage: map[string][]byte{"so1": []byte("data")},
		},
	}
	_, err = handler.ClaimTransfer(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already been claimed by this receiver")
}

func TestClaimTransferMIMO_TransferNotReady(t *testing.T) {
	sparktesting.RequireGripMock(t)
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{30})
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderInitiated)

	_, err := sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverPubKey).
		SetStatus(st.TransferReceiverStatusInitiated).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	req := &pb.ClaimTransferRequest{
		TransferId:             transfer.ID.String(),
		OwnerIdentityPublicKey: receiverPubKey.Serialize(),
		ClaimPackage: &pb.ClaimPackage{
			LeavesToClaim:   []*pb.UserSignedTxSigningJob{},
			KeyTweakPackage: map[string][]byte{"so1": []byte("data")},
		},
	}
	_, err = handler.ClaimTransfer(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not ready for receiver claim")
}

func TestClaimTransferMIMO_TransferExpired(t *testing.T) {
	sparktesting.RequireGripMock(t)
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{31})
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusExpired)

	_, err := sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverPubKey).
		SetStatus(st.TransferReceiverStatusInitiated).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	req := &pb.ClaimTransferRequest{
		TransferId:             transfer.ID.String(),
		OwnerIdentityPublicKey: receiverPubKey.Serialize(),
		ClaimPackage: &pb.ClaimPackage{
			LeavesToClaim:   []*pb.UserSignedTxSigningJob{},
			KeyTweakPackage: map[string][]byte{"so1": []byte("data")},
		},
	}
	_, err = handler.ClaimTransfer(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminal state")
}

// TestClaimTransferMIMO_LeafScopedByReceiver verifies that the MIMO path only considers leaves
// assigned to the specific receiver (via the TransferLeaf→TransferReceiver FK), not all leaves
// on the transfer. Creates 1 leaf linked to the receiver but submits 2 LeavesToClaim; the
// resulting "inconsistent leaves to claim" error proves the handler scoped correctly.
func TestClaimTransferMIMO_LeafScopedByReceiver(t *testing.T) {
	sparktesting.RequireGripMock(t)
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{13})
	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	tree := createTestTreeForClaim(t, ctx, ownerIdentityPrivKey.Public(), sessionCtx.Client)
	leaf := createTestTreeNode(t, ctx, rng, sessionCtx.Client, tree, keyshare)

	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderKeyTweaked)

	receiver, err := sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverPubKey).
		SetStatus(st.TransferReceiverStatusReceiverClaimPending).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	sender, err := sessionCtx.Client.TransferSender.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(senderPubKey).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	// Create TransferLeaf with receiver and sender FKs.
	_, err = sessionCtx.Client.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(leaf).
		SetPreviousRefundTx(createTestTxBytes(t, 2000)).
		SetIntermediateRefundTx(createTestTxBytes(t, 2001)).
		SetTransferReceiverID(receiver.ID).
		SetTransferSenderID(sender.ID).
		Save(ctx)
	require.NoError(t, err)

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	// Claim with 2 leaves_to_claim but only 1 leaf scoped to this receiver.
	// Should get a leaf count mismatch error, proving scoping works.
	dummyJob := &pb.UserSignedTxSigningJob{LeafId: leaf.ID.String()}
	req := &pb.ClaimTransferRequest{
		TransferId:             transfer.ID.String(),
		OwnerIdentityPublicKey: receiverPubKey.Serialize(),
		ClaimPackage: &pb.ClaimPackage{
			LeavesToClaim:               []*pb.UserSignedTxSigningJob{dummyJob, dummyJob},
			DirectFromCpfpLeavesToClaim: []*pb.UserSignedTxSigningJob{dummyJob, dummyJob},
			KeyTweakPackage:             map[string][]byte{"so1": []byte("data")},
			HashVariant:                 pb.HashVariant_HASH_VARIANT_V2,
			UserSignature:               []byte("dummy"),
		},
	}
	_, err = handler.ClaimTransfer(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inconsistent leaves to claim")
}

// TestClaimTransferMIMO_ReceiverNotClaimableStatus verifies that a receiver in a non-claimable
// status (e.g., Cancelled) is rejected by the MIMO validation in ClaimTransfer.
func TestClaimTransferMIMO_ReceiverNotClaimableStatus(t *testing.T) {
	sparktesting.RequireGripMock(t)
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{20})
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderKeyTweaked)

	_, err := sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverPubKey).
		SetStatus(st.TransferReceiverStatusCancelled).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	req := &pb.ClaimTransferRequest{
		TransferId:             transfer.ID.String(),
		OwnerIdentityPublicKey: receiverPubKey.Serialize(),
		ClaimPackage: &pb.ClaimPackage{
			LeavesToClaim:   []*pb.UserSignedTxSigningJob{},
			KeyTweakPackage: map[string][]byte{"so1": []byte("data")},
		},
	}
	_, err = handler.ClaimTransfer(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in a claimable status")
	assert.Equal(t, codes.FailedPrecondition, status.Code(err),
		"a non-claimable receiver status is a precondition failure, not an internal error")
}

func TestValidateTransferReadyForReceiverClaim(t *testing.T) {
	tests := []struct {
		name      string
		status    st.TransferStatus
		wantError bool
		errSubstr string
	}{
		// Pre-SENDER_KEY_TWEAKED: reject
		{
			name:      "SenderInitiated",
			status:    st.TransferStatusSenderInitiated,
			wantError: true,
			errSubstr: "not ready for receiver claim",
		},
		{
			name:      "SenderInitiatedCoordinator",
			status:    st.TransferStatusSenderInitiatedCoordinator,
			wantError: true,
			errSubstr: "not ready for receiver claim",
		},
		{
			name:      "SenderKeyTweakPending",
			status:    st.TransferStatusSenderKeyTweakPending,
			wantError: true,
			errSubstr: "not ready for receiver claim",
		},
		{
			name:      "ApplyingSenderKeyTweak",
			status:    st.TransferStatusApplyingSenderKeyTweak,
			wantError: true,
			errSubstr: "not ready for receiver claim",
		},
		// Terminal: reject
		{
			name:      "Expired",
			status:    st.TransferStatusExpired,
			wantError: true,
			errSubstr: "terminal state",
		},
		{
			name:      "Returned",
			status:    st.TransferStatusReturned,
			wantError: true,
			errSubstr: "terminal state",
		},
		// SENDER_KEY_TWEAKED and later: allow
		{name: "SenderKeyTweaked", status: st.TransferStatusSenderKeyTweaked},
		{name: "ReceiverKeyTweaked", status: st.TransferStatusReceiverKeyTweaked},
		{name: "ReceiverKeyTweakLocked", status: st.TransferStatusReceiverKeyTweakLocked},
		{name: "ReceiverKeyTweakApplied", status: st.TransferStatusReceiverKeyTweakApplied},
		{name: "ReceiverRefundSigned", status: st.TransferStatusReceiverRefundSigned},
		{name: "Completed", status: st.TransferStatusCompleted},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transfer := &ent.Transfer{
				ID:     uuid.New(),
				Status: tc.status,
			}
			err := validateTransferReadyForReceiverClaim(transfer)
			if tc.wantError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errSubstr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestClaimTransferTweakKeys_DualWritesReceiverStatus verifies that ClaimTransferTweakKeys
// updates both the Transfer status to ReceiverKeyTweaked AND the TransferReceiver status
// to KeyTweaked when a single receiver exists.
func TestClaimTransferTweakKeys_DualWritesReceiverStatus(t *testing.T) {
	sparktesting.RequireGripMock(t)
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{21})
	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	tree := createTestTreeForClaim(t, ctx, ownerIdentityPrivKey.Public(), sessionCtx.Client)
	leaf := createTestTreeNode(t, ctx, rng, sessionCtx.Client, tree, keyshare)

	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderKeyTweaked)

	receiver, err := sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverPubKey).
		SetStatus(st.TransferReceiverStatusInitiated).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	_ = createTestTransferLeaf(t, ctx, sessionCtx.Client, transfer, leaf)

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	pubkeyShareTweakPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	leafTweak := &pb.ClaimLeafKeyTweak{
		LeafId: leaf.ID.String(),
		SecretShareTweak: &pb.SecretShare{
			SecretShare: make([]byte, 32),
			Proofs:      [][]byte{pubkeyShareTweakPubKey.Serialize()},
		},
		PubkeySharesTweak: map[string][]byte{
			"operator1": pubkeyShareTweakPubKey.Serialize(),
		},
	}

	req := &pb.ClaimTransferTweakKeysRequest{
		TransferId:             transfer.ID.String(),
		OwnerIdentityPublicKey: receiverPubKey.Serialize(),
		LeavesToReceive:        []*pb.ClaimLeafKeyTweak{leafTweak},
	}
	err = handler.ClaimTransferTweakKeys(ctx, req)
	require.NoError(t, err)

	// Read back from the handler's transaction (ClaimTransferTweakKeys doesn't commit —
	// that's the gRPC middleware's job). Using ent.GetDbFromContext reads within the same tx.
	txClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	updatedTransfer, err := txClient.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusReceiverKeyTweaked, updatedTransfer.Status)

	updatedReceiver, err := txClient.TransferReceiver.Get(ctx, receiver.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferReceiverStatusKeyTweaked, updatedReceiver.Status)
}

// TestClaimTransferTweakKeys_NoReceiverStillWorks verifies that ClaimTransferTweakKeys
// succeeds when no TransferReceiver exists (pre-MIMO data). The transfer status should
// still advance to ReceiverKeyTweaked and no panic occurs from the nil receiver.
func TestClaimTransferTweakKeys_NoReceiverStillWorks(t *testing.T) {
	sparktesting.RequireGripMock(t)
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{22})
	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	tree := createTestTreeForClaim(t, ctx, ownerIdentityPrivKey.Public(), sessionCtx.Client)
	leaf := createTestTreeNode(t, ctx, rng, sessionCtx.Client, tree, keyshare)

	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderKeyTweaked)

	// No TransferReceiver created — simulates pre-MIMO data.
	_ = createTestTransferLeaf(t, ctx, sessionCtx.Client, transfer, leaf)

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	pubkeyShareTweakPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	leafTweak := &pb.ClaimLeafKeyTweak{
		LeafId: leaf.ID.String(),
		SecretShareTweak: &pb.SecretShare{
			SecretShare: make([]byte, 32),
			Proofs:      [][]byte{pubkeyShareTweakPubKey.Serialize()},
		},
		PubkeySharesTweak: map[string][]byte{
			"operator1": pubkeyShareTweakPubKey.Serialize(),
		},
	}

	req := &pb.ClaimTransferTweakKeysRequest{
		TransferId:             transfer.ID.String(),
		OwnerIdentityPublicKey: receiverPubKey.Serialize(),
		LeavesToReceive:        []*pb.ClaimLeafKeyTweak{leafTweak},
	}
	err := handler.ClaimTransferTweakKeys(ctx, req)
	require.NoError(t, err)

	// Read back from the handler's transaction (ClaimTransferTweakKeys doesn't commit).
	txClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	updatedTransfer, err := txClient.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusReceiverKeyTweaked, updatedTransfer.Status)
}

//
// Legacy path tests
//

// TestClaimTransferTweakKeys_MultipleReceiversRejected verifies that ClaimTransferTweakKeys
// rejects transfers with multiple receivers, directing the caller to use ClaimTransfer instead.
func TestClaimTransferTweakKeys_MultipleReceiversRejected(t *testing.T) {
	sparktesting.RequireGripMock(t)
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{23})
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiver2PubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderKeyTweaked)

	_, err := sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverPubKey).
		SetStatus(st.TransferReceiverStatusInitiated).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	_, err = sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiver2PubKey).
		SetStatus(st.TransferReceiverStatusInitiated).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	req := &pb.ClaimTransferTweakKeysRequest{
		TransferId:             transfer.ID.String(),
		OwnerIdentityPublicKey: receiverPubKey.Serialize(),
		LeavesToReceive:        []*pb.ClaimLeafKeyTweak{},
	}
	err = handler.ClaimTransferTweakKeys(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple receivers")
	assert.Contains(t, err.Error(), "upgrade to the latest SDK")
}

// TestClaimTransferSignRefunds_MultipleReceiversRejected verifies that ClaimTransferSignRefunds
// rejects transfers with multiple receivers, directing the caller to use ClaimTransfer instead.
func TestClaimTransferSignRefunds_MultipleReceiversRejected(t *testing.T) {
	sparktesting.RequireGripMock(t)
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{24})
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiver2PubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusReceiverKeyTweaked)

	_, err := sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverPubKey).
		SetStatus(st.TransferReceiverStatusKeyTweaked).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	_, err = sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiver2PubKey).
		SetStatus(st.TransferReceiverStatusKeyTweaked).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	req := &pb.ClaimTransferSignRefundsRequest{
		TransferId:             transfer.ID.String(),
		OwnerIdentityPublicKey: receiverPubKey.Serialize(),
		SigningJobs:            []*pb.LeafRefundTxSigningJob{},
	}
	_, err = handler.ClaimTransferSignRefunds(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple receivers")
	assert.Contains(t, err.Error(), "upgrade to the latest SDK")
}

// TestClaimTransferSignRefunds_DualWritesReceiverStatus verifies that ClaimTransferSignRefunds
// dual-writes the receiver status alongside the transfer status during the settle phase.
// Follows the same pattern as TestClaimTransferSignRefunds_Success but adds a TransferReceiver
// and verifies its status is updated.
func TestClaimTransferSignRefunds_DualWritesReceiverStatus(t *testing.T) {
	sparktesting.RequireGripMock(t)
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	err := gripmock.AddStub("spark_internal.SparkInternalService", "initiate_settle_receiver_key_tweak", nil, nil)
	require.NoError(t, err, "Failed to add initiate_settle_receiver_key_tweak stub")

	err = gripmock.AddStub("spark_internal.SparkInternalService", "settle_receiver_key_tweak", nil, nil)
	require.NoError(t, err, "Failed to add settle_receiver_key_tweak stub")

	err = gripmock.AddStub("spark_internal.SparkInternalService", "frost_round1", nil, frostRound1StubOutput)
	require.NoError(t, err, "Failed to add frost_round1 stub")

	err = gripmock.AddStub("spark_internal.SparkInternalService", "frost_round2", nil, frostRound2StubOutput)
	require.NoError(t, err, "Failed to add frost_round2 stub")

	rng := rand.NewChaCha8([32]byte{25})
	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	tree := createTestTreeForClaim(t, ctx, ownerIdentityPrivKey.Public(), sessionCtx.Client)
	leaf := createTestTreeNode(t, ctx, rng, sessionCtx.Client, tree, keyshare)

	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusReceiverKeyTweaked)
	transferLeaf := createTestTransferLeaf(t, ctx, sessionCtx.Client, transfer, leaf)

	receiver, err := sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverPubKey).
		SetStatus(st.TransferReceiverStatusKeyTweaked).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)
	transferLeaf, err = transferLeaf.Update().SetTransferReceiverID(receiver.ID).Save(ctx)
	require.NoError(t, err)

	// Set up VSS shares and key tweaks (same pattern as TestClaimTransferSignRefunds_Success).
	tweakPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	secretInt := new(big.Int).SetBytes(tweakPrivKey.Serialize())

	cfg := sparktesting.TestConfig(t)
	threshold := int(cfg.Threshold)
	numberOfShares := len(cfg.SigningOperatorMap)

	shares, err := secretsharing.SplitSecretWithProofs(secretInt, secp256k1.S256().N, threshold, numberOfShares)
	require.NoError(t, err)
	require.NotEmpty(t, shares)

	share := shares[0]
	secretShareBytes := make([]byte, 32)
	share.Share.FillBytes(secretShareBytes)

	// pubkey_shares_tweak entries must equal f(operator.ID+1)·G — see #6867.
	pubkeySharesTweak := buildValidPubkeySharesTweak(t, cfg, share.Proofs)

	claimKeyTweak := &pb.ClaimLeafKeyTweak{
		SecretShareTweak: &pb.SecretShare{
			SecretShare: secretShareBytes,
			Proofs:      share.Proofs,
		},
		PubkeySharesTweak: pubkeySharesTweak,
	}

	claimKeyTweakBytes, err := proto.Marshal(claimKeyTweak)
	require.NoError(t, err)

	_, err = transferLeaf.Update().SetKeyTweak(claimKeyTweakBytes).Save(ctx)
	require.NoError(t, err)

	handler := NewTransferHandler(cfg)

	req := &pb.ClaimTransferSignRefundsRequest{
		TransferId:             transfer.ID.String(),
		OwnerIdentityPublicKey: receiverPubKey.Serialize(),
		SigningJobs: []*pb.LeafRefundTxSigningJob{
			createTestLeafRefundTxSigningJob(t, rng, leaf, leaf.VerifyingPubkey.Sub(keyshare.PublicKey).Sub(tweakPrivKey.Public())),
		},
	}
	resp, err := handler.ClaimTransferSignRefunds(ctx, req)
	require.NoError(t, err)
	assert.NotNil(t, resp)

	// Verify receiver status was dual-written during the settle phase.
	// Read from the session's tx — see comment in
	// TestClaimTransferSignRefunds_Success on why we no longer use
	// sessionCtx.Client (bare client) after the InitiateSettleReceiverKeyTweak
	// / SettleReceiverKeyTweak mid-flow commits were removed.
	txClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updatedReceiver, err := txClient.TransferReceiver.Get(ctx, receiver.ID)
	require.NoError(t, err)
	assert.NotEqual(t, st.TransferReceiverStatusKeyTweaked, updatedReceiver.Status,
		"receiver status should have advanced beyond KeyTweaked")
}

//
// Unit tests for helper functions
//

func TestVerifyClaimPackageSignature(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{30})
	privKey := keys.MustGeneratePrivateKeyFromRand(rng)
	pubKey := privKey.Public()
	transferID := uuid.New()
	keyTweakPackage := map[string][]byte{"so1": []byte("tweak-data")}

	signingPayload := common.GetClaimPackageSigningPayload(transferID, keyTweakPackage)
	validSig := ecdsa.Sign(privKey.ToBTCEC(), signingPayload).Serialize()

	t.Run("valid signature", func(t *testing.T) {
		pkg := &pb.ClaimPackage{
			HashVariant:     pb.HashVariant_HASH_VARIANT_V2,
			UserSignature:   validSig,
			KeyTweakPackage: keyTweakPackage,
		}
		err := verifyClaimPackageSignature(transferID, pkg, pubKey)
		assert.NoError(t, err)
	})

	t.Run("wrong hash variant", func(t *testing.T) {
		pkg := &pb.ClaimPackage{
			HashVariant:     pb.HashVariant_HASH_VARIANT_UNSPECIFIED,
			UserSignature:   validSig,
			KeyTweakPackage: keyTweakPackage,
		}
		err := verifyClaimPackageSignature(transferID, pkg, pubKey)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "HASH_VARIANT_V2")
	})

	t.Run("empty signature", func(t *testing.T) {
		pkg := &pb.ClaimPackage{
			HashVariant:     pb.HashVariant_HASH_VARIANT_V2,
			UserSignature:   nil,
			KeyTweakPackage: keyTweakPackage,
		}
		err := verifyClaimPackageSignature(transferID, pkg, pubKey)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "user_signature is required")
	})

	t.Run("invalid signature bytes", func(t *testing.T) {
		pkg := &pb.ClaimPackage{
			HashVariant:     pb.HashVariant_HASH_VARIANT_V2,
			UserSignature:   []byte("not-a-valid-signature"),
			KeyTweakPackage: keyTweakPackage,
		}
		err := verifyClaimPackageSignature(transferID, pkg, pubKey)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unable to verify claim package signature")
	})

	t.Run("wrong key", func(t *testing.T) {
		wrongKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		pkg := &pb.ClaimPackage{
			HashVariant:     pb.HashVariant_HASH_VARIANT_V2,
			UserSignature:   validSig,
			KeyTweakPackage: keyTweakPackage,
		}
		err := verifyClaimPackageSignature(transferID, pkg, wrongKey)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unable to verify claim package signature")
	})

	t.Run("wrong transfer ID", func(t *testing.T) {
		pkg := &pb.ClaimPackage{
			HashVariant:     pb.HashVariant_HASH_VARIANT_V2,
			UserSignature:   validSig,
			KeyTweakPackage: keyTweakPackage,
		}
		err := verifyClaimPackageSignature(uuid.New(), pkg, pubKey)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unable to verify claim package signature")
	})
}

func TestLoadTransferReceiverByPublicKeyForUpdate(t *testing.T) {
	sparktesting.RequireGripMock(t)
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{31})
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	otherPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderKeyTweaked)

	_, err := sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverPubKey).
		SetStatus(st.TransferReceiverStatusInitiated).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	t.Run("nil transfer returns error", func(t *testing.T) {
		receiver, err := handler.loadTransferReceiverByPublicKeyForUpdate(ctx, nil, &receiverPubKey)
		require.Error(t, err)
		assert.Nil(t, receiver)
	})

	t.Run("nil pubkey returns error", func(t *testing.T) {
		receiver, err := handler.loadTransferReceiverByPublicKeyForUpdate(ctx, transfer, nil)
		require.Error(t, err)
		assert.Nil(t, receiver)
	})

	t.Run("matching receiver is returned", func(t *testing.T) {
		receiver, err := handler.loadTransferReceiverByPublicKeyForUpdate(ctx, transfer, &receiverPubKey)
		require.NoError(t, err)
		require.NotNil(t, receiver)
		assert.Equal(t, receiverPubKey, receiver.IdentityPubkey)
	})

	t.Run("no matching receiver returns error", func(t *testing.T) {
		receiver, err := handler.loadTransferReceiverByPublicKeyForUpdate(ctx, transfer, &otherPubKey)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no transfer receivers found")
		assert.Nil(t, receiver)
	})
}

func TestLoadSingleTransferReceiverForUnsupportedMimoPath(t *testing.T) {
	sparktesting.RequireGripMock(t)
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{32})
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	t.Run("no receivers returns nil", func(t *testing.T) {
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderKeyTweaked)
		receiver, err := handler.loadSingleTransferReceiverForUnsupportedMimoPath(ctx, transfer)
		require.NoError(t, err)
		assert.Nil(t, receiver)
	})

	t.Run("single receiver returns it", func(t *testing.T) {
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderKeyTweaked)
		_, err := sessionCtx.Client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPubKey).
			SetStatus(st.TransferReceiverStatusInitiated).
			SetTransferType(transfer.Type).
			Save(ctx)
		require.NoError(t, err)

		receiver, err := handler.loadSingleTransferReceiverForUnsupportedMimoPath(ctx, transfer)
		require.NoError(t, err)
		require.NotNil(t, receiver)
		assert.Equal(t, receiverPubKey, receiver.IdentityPubkey)
	})

	t.Run("multiple receivers returns error", func(t *testing.T) {
		receiver2PubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderKeyTweaked)

		_, err := sessionCtx.Client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPubKey).
			SetStatus(st.TransferReceiverStatusInitiated).
			SetTransferType(transfer.Type).
			Save(ctx)
		require.NoError(t, err)

		_, err = sessionCtx.Client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiver2PubKey).
			SetStatus(st.TransferReceiverStatusInitiated).
			SetTransferType(transfer.Type).
			Save(ctx)
		require.NoError(t, err)

		receiver, err := handler.loadSingleTransferReceiverForUnsupportedMimoPath(ctx, transfer)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "multiple receivers")
		assert.Nil(t, receiver)
	})
}

func TestGetTransferLeavesForReceiverQuery(t *testing.T) {
	sparktesting.RequireGripMock(t)
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{33})
	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	tree := createTestTreeForClaim(t, ctx, ownerIdentityPrivKey.Public(), sessionCtx.Client)
	leaf1 := createTestTreeNode(t, ctx, rng, sessionCtx.Client, tree, keyshare)
	leaf2 := createTestTreeNode(t, ctx, rng, sessionCtx.Client, tree, keyshare)

	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderKeyTweaked)

	receiver, err := sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverPubKey).
		SetStatus(st.TransferReceiverStatusInitiated).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	sender, err := sessionCtx.Client.TransferSender.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(senderPubKey).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	// leaf1 is scoped to the receiver; leaf2 has no receiver FK.
	_, err = sessionCtx.Client.TransferLeaf.Create().
		SetTransfer(transfer).SetLeaf(leaf1).
		SetPreviousRefundTx(createTestTxBytes(t, 3000)).
		SetIntermediateRefundTx(createTestTxBytes(t, 3001)).
		SetTransferReceiverID(receiver.ID).
		SetTransferSenderID(sender.ID).
		Save(ctx)
	require.NoError(t, err)

	_, err = sessionCtx.Client.TransferLeaf.Create().
		SetTransfer(transfer).SetLeaf(leaf2).
		SetPreviousRefundTx(createTestTxBytes(t, 3002)).
		SetIntermediateRefundTx(createTestTxBytes(t, 3003)).
		SetTransferSenderID(sender.ID).
		Save(ctx)
	require.NoError(t, err)

	t.Run("nil receiver returns all leaves", func(t *testing.T) {
		leaves, err := getTransferLeavesForReceiverQuery(transfer, nil).All(ctx)
		require.NoError(t, err)
		assert.Len(t, leaves, 2)
	})

	t.Run("receiver scopes to that receiver's leaves only", func(t *testing.T) {
		leaves, err := getTransferLeavesForReceiverQuery(transfer, receiver).All(ctx)
		require.NoError(t, err)
		assert.Len(t, leaves, 1)
	})
}

func TestRevertClaimTransfer(t *testing.T) {
	sparktesting.RequireGripMock(t)
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{34})
	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	tree := createTestTreeForClaim(t, ctx, ownerIdentityPrivKey.Public(), sessionCtx.Client)

	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	t.Run("reverts receiver and transfer from KeyTweaked", func(t *testing.T) {
		leaf := createTestTreeNode(t, ctx, rng, sessionCtx.Client, tree, keyshare)
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusReceiverKeyTweaked)
		receiver, err := sessionCtx.Client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPubKey).
			SetStatus(st.TransferReceiverStatusKeyTweaked).
			SetTransferType(transfer.Type).
			Save(ctx)
		require.NoError(t, err)

		transferLeaf := createTestTransferLeaf(t, ctx, sessionCtx.Client, transfer, leaf)
		_, err = transferLeaf.Update().SetKeyTweak([]byte("some-tweak")).Save(ctx)
		require.NoError(t, err)

		err = handler.revertClaimTransfer(ctx, transfer, receiver, []*ent.TransferLeaf{transferLeaf})
		require.NoError(t, err)

		updatedTransfer, err := sessionCtx.Client.Transfer.Get(ctx, transfer.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferStatusSenderKeyTweaked, updatedTransfer.Status)

		updatedReceiver, err := sessionCtx.Client.TransferReceiver.Get(ctx, receiver.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferReceiverStatusReceiverClaimPending, updatedReceiver.Status)

		updatedLeaf, err := sessionCtx.Client.TransferLeaf.Get(ctx, transferLeaf.ID)
		require.NoError(t, err)
		assert.Nil(t, updatedLeaf.KeyTweak)
	})

	t.Run("rejects revert when receiver key tweak already applied", func(t *testing.T) {
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusReceiverKeyTweaked)
		receiver, err := sessionCtx.Client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPubKey).
			SetStatus(st.TransferReceiverStatusKeyTweakApplied).
			SetTransferType(transfer.Type).
			Save(ctx)
		require.NoError(t, err)

		err = handler.revertClaimTransfer(ctx, transfer, receiver, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already applied")
	})

	t.Run("no-op for early receiver status", func(t *testing.T) {
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderKeyTweaked)
		receiver, err := sessionCtx.Client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPubKey).
			SetStatus(st.TransferReceiverStatusInitiated).
			SetTransferType(transfer.Type).
			Save(ctx)
		require.NoError(t, err)

		err = handler.revertClaimTransfer(ctx, transfer, receiver, nil)
		require.NoError(t, err)

		updatedReceiver, err := sessionCtx.Client.TransferReceiver.Get(ctx, receiver.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferReceiverStatusInitiated, updatedReceiver.Status)
	})

	t.Run("MIMO disabled: reverts using transfer status", func(t *testing.T) {
		leaf := createTestTreeNode(t, ctx, rng, sessionCtx.Client, tree, keyshare)
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusReceiverKeyTweaked)
		transferLeaf := createTestTransferLeaf(t, ctx, sessionCtx.Client, transfer, leaf)

		err := handler.revertClaimTransfer(ctx, transfer, nil, []*ent.TransferLeaf{transferLeaf})
		require.NoError(t, err)

		updatedTransfer, err := sessionCtx.Client.Transfer.Get(ctx, transfer.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferStatusSenderKeyTweaked, updatedTransfer.Status)
	})

	t.Run("MIMO disabled: rejects revert when transfer key tweak already applied", func(t *testing.T) {
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusReceiverKeyTweakApplied)
		err := handler.revertClaimTransfer(ctx, transfer, nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already applied")
	})

	t.Run("MIMO disabled: no-op for early transfer status", func(t *testing.T) {
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderInitiated)
		err := handler.revertClaimTransfer(ctx, transfer, nil, nil)
		require.NoError(t, err)

		updatedTransfer, err := sessionCtx.Client.Transfer.Get(ctx, transfer.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferStatusSenderInitiated, updatedTransfer.Status)
	})

	t.Run("MIMO disabled: dual-writes receiver when receiver exists", func(t *testing.T) {
		leaf := createTestTreeNode(t, ctx, rng, sessionCtx.Client, tree, keyshare)
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusReceiverKeyTweaked)
		receiver, err := sessionCtx.Client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPubKey).
			SetStatus(st.TransferReceiverStatusKeyTweaked).
			SetTransferType(transfer.Type).
			Save(ctx)
		require.NoError(t, err)

		transferLeaf := createTestTransferLeaf(t, ctx, sessionCtx.Client, transfer, leaf)

		// MIMO disabled but receiver exists — reads transfer status, dual-writes both.
		err = handler.revertClaimTransfer(ctx, transfer, receiver, []*ent.TransferLeaf{transferLeaf})
		require.NoError(t, err)

		updatedTransfer, err := sessionCtx.Client.Transfer.Get(ctx, transfer.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferStatusSenderKeyTweaked, updatedTransfer.Status)

		updatedReceiver, err := sessionCtx.Client.TransferReceiver.Get(ctx, receiver.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferReceiverStatusReceiverClaimPending, updatedReceiver.Status)
	})
}

// TestInitiateSettleReceiverKeyTweak_RefundSignedReturnsEarly verifies that
// InitiateSettleReceiverKeyTweak returns nil (early return) when the receiver
// or transfer is already at RefundSigned status, since the key tweak has
// already been applied at that point.
func TestInitiateSettleReceiverKeyTweak_RefundSignedReturnsEarly(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{40})
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusReceiverRefundSigned)

	_, err := sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverPubKey).
		SetStatus(st.TransferReceiverStatusRefundSigned).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	req := &pbinternal.InitiateSettleReceiverKeyTweakRequest{
		TransferId:                transfer.ID.String(),
		ReceiverIdentityPublicKey: receiverPubKey.Serialize(),
	}
	assert.NoError(t, handler.InitiateSettleReceiverKeyTweak(ctx, req))
}

// TestSettleReceiverKeyTweak_RefundSignedReturnsEarly verifies that
// SettleReceiverKeyTweak returns nil (early return) when the receiver is
// already at RefundSigned status in the MIMO path.
func TestSettleReceiverKeyTweak_RefundSignedReturnsEarly(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{41})
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	t.Run("MIMO: receiver at RefundSigned returns early on COMMIT", func(t *testing.T) {
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusReceiverRefundSigned)

		_, err := sessionCtx.Client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPubKey).
			SetStatus(st.TransferReceiverStatusRefundSigned).
			SetTransferType(transfer.Type).
			Save(ctx)
		require.NoError(t, err)

		req := &pbinternal.SettleReceiverKeyTweakRequest{
			TransferId:                transfer.ID.String(),
			Action:                    pbinternal.SettleKeyTweakAction_COMMIT,
			ReceiverIdentityPublicKey: receiverPubKey.Serialize(),
		}
		err = handler.SettleReceiverKeyTweak(ctx, req)
		assert.NoError(t, err)
	})
}

func TestInitiateSettleReceiverKeyTweak_RejectsEarlyTransferStatus(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{42})
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	t.Run("MIMO: rejects SenderInitiated transfer", func(t *testing.T) {
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderInitiated)

		_, err := sessionCtx.Client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPubKey).
			SetStatus(st.TransferReceiverStatusInitiated).
			SetTransferType(transfer.Type).
			Save(ctx)
		require.NoError(t, err)

		req := &pbinternal.InitiateSettleReceiverKeyTweakRequest{
			TransferId:                transfer.ID.String(),
			ReceiverIdentityPublicKey: receiverPubKey.Serialize(),
		}
		err = handler.InitiateSettleReceiverKeyTweak(ctx, req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not ready for receiver claim")
	})

	t.Run("MIMO: rejects Expired transfer", func(t *testing.T) {
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusExpired)

		_, err := sessionCtx.Client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPubKey).
			SetStatus(st.TransferReceiverStatusInitiated).
			SetTransferType(transfer.Type).
			Save(ctx)
		require.NoError(t, err)

		req := &pbinternal.InitiateSettleReceiverKeyTweakRequest{
			TransferId:                transfer.ID.String(),
			ReceiverIdentityPublicKey: receiverPubKey.Serialize(),
		}
		err = handler.InitiateSettleReceiverKeyTweak(ctx, req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "terminal state")
	})
}

func TestSettleReceiverKeyTweak_RejectsEarlyTransferStatus(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{43})
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	t.Run("MIMO: COMMIT rejects SenderInitiated transfer", func(t *testing.T) {
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderInitiated)

		_, err := sessionCtx.Client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPubKey).
			SetStatus(st.TransferReceiverStatusInitiated).
			SetTransferType(transfer.Type).
			Save(ctx)
		require.NoError(t, err)

		req := &pbinternal.SettleReceiverKeyTweakRequest{
			TransferId:                transfer.ID.String(),
			Action:                    pbinternal.SettleKeyTweakAction_COMMIT,
			ReceiverIdentityPublicKey: receiverPubKey.Serialize(),
		}
		err = handler.SettleReceiverKeyTweak(ctx, req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not ready for receiver claim")
	})

	t.Run("MIMO: ROLLBACK proceeds despite SenderInitiated transfer", func(t *testing.T) {
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderInitiated)

		_, err := sessionCtx.Client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPubKey).
			SetStatus(st.TransferReceiverStatusInitiated).
			SetTransferType(transfer.Type).
			Save(ctx)
		require.NoError(t, err)

		req := &pbinternal.SettleReceiverKeyTweakRequest{
			TransferId:                transfer.ID.String(),
			Action:                    pbinternal.SettleKeyTweakAction_ROLLBACK,
			ReceiverIdentityPublicKey: receiverPubKey.Serialize(),
		}
		err = handler.SettleReceiverKeyTweak(ctx, req)
		require.NoError(t, err)
	})

	t.Run("MIMO: ROLLBACK proceeds despite Expired transfer", func(t *testing.T) {
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusExpired)

		_, err := sessionCtx.Client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPubKey).
			SetStatus(st.TransferReceiverStatusInitiated).
			SetTransferType(transfer.Type).
			Save(ctx)
		require.NoError(t, err)

		req := &pbinternal.SettleReceiverKeyTweakRequest{
			TransferId:                transfer.ID.String(),
			Action:                    pbinternal.SettleKeyTweakAction_ROLLBACK,
			ReceiverIdentityPublicKey: receiverPubKey.Serialize(),
		}
		err = handler.SettleReceiverKeyTweak(ctx, req)
		require.NoError(t, err)
	})
}

// Asserts the entry point has no receiver-count gate: both shapes reach package
// parsing. Proving a multi-receiver send completes is the integration suite's job.
func TestStartTransferV3Consensus_NoReceiverCountGate(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{60})
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiver1PubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiver2PubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	makeReq := func(receivers map[string][]byte) *pb.StartTransferV3Request {
		return &pb.StartTransferV3Request{
			TransferId: uuid.New().String(),
			SenderPackages: []*pb.SenderTransferPackage{{
				OwnerIdentityPublicKey:     senderPrivKey.Public().Serialize(),
				TransferPackage:            &pb.TransferPackage{},
				ReceiverIdentityPublicKeys: receivers,
			}},
		}
	}

	// Pinning the parse error proves the request traversed the entry gates
	// rather than failing earlier for an unrelated reason.
	t.Run("multi-receiver reaches package parsing", func(t *testing.T) {
		_, err := handler.startTransferV3Consensus(t.Context(), makeReq(map[string][]byte{
			"leaf-1": receiver1PubKey.Serialize(),
			"leaf-2": receiver2PubKey.Serialize(),
		}), "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid transfer package")
	})

	t.Run("single-receiver reaches package parsing", func(t *testing.T) {
		_, err := handler.startTransferV3Consensus(t.Context(), makeReq(map[string][]byte{
			"leaf-1": receiver1PubKey.Serialize(),
		}), "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid transfer package")
	})
}

// -----------------------------------------------------------------------------
// INITIATED is invalid for receivers on post-sender-tweak transfers
// -----------------------------------------------------------------------------
//
// `INITIATED` semantically means the transfer never hit SENDER_KEY_TWEAKED.
// Post-sender-tweak receiver rows always start at RECEIVER_CLAIM_PENDING
// (per the dual-write contract). The receiver-status switches in
// transfer_handler.go reject INITIATED on post-sender-tweak transfers; these
// tests pin that contract at the public RPC boundary.

// TestClaimTransferMIMO_RejectsInitiatedReceiver verifies that ClaimTransfer
// rejects a receiver still at INITIATED on a post-sender-tweak transfer.
// Mirrors TestClaimTransferMIMO_ReceiverNotClaimableStatus (which uses
// CANCELLED) — both exercise the `default` fall-through of the claimable-
// status switch.
func TestClaimTransferMIMO_RejectsInitiatedReceiver(t *testing.T) {
	sparktesting.RequireGripMock(t)
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{50})
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderKeyTweaked)

	_, err := sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverPubKey).
		SetStatus(st.TransferReceiverStatusInitiated).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	req := &pb.ClaimTransferRequest{
		TransferId:             transfer.ID.String(),
		OwnerIdentityPublicKey: receiverPubKey.Serialize(),
		ClaimPackage: &pb.ClaimPackage{
			LeavesToClaim:   []*pb.UserSignedTxSigningJob{},
			KeyTweakPackage: map[string][]byte{"so1": []byte("data")},
		},
	}
	_, err = handler.ClaimTransfer(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in a claimable status")
	assert.Contains(t, err.Error(), "INITIATED")
}

// TestInitiateSettleReceiverKeyTweak_RejectsInitiatedReceiver verifies that
// InitiateSettleReceiverKeyTweak rejects a receiver still at INITIATED on a
// post-sender-tweak transfer. The receiver-status switch in the handler now
// only accepts RECEIVER_CLAIM_PENDING in the pre-claim window; INITIATED
// falls through to default with "unexpected transfer receiver status".
func TestInitiateSettleReceiverKeyTweak_RejectsInitiatedReceiver(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{51})
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderKeyTweaked)

	_, err := sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverPubKey).
		SetStatus(st.TransferReceiverStatusInitiated).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	req := &pbinternal.InitiateSettleReceiverKeyTweakRequest{
		TransferId:                transfer.ID.String(),
		ReceiverIdentityPublicKey: receiverPubKey.Serialize(),
	}
	err = handler.InitiateSettleReceiverKeyTweak(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected transfer receiver status")
	assert.Contains(t, err.Error(), "INITIATED")
}

// TestSettleReceiverKeyTweak_RejectsInitiatedReceiverOnCommit verifies that
// SettleReceiverKeyTweak with COMMIT action rejects a receiver still at
// INITIATED on a post-sender-tweak transfer. The rollback switch's
// "do nothing" case used to include INITIATED; it now falls through to
// default and returns an "invalid status" error on COMMIT.
func TestSettleReceiverKeyTweak_RejectsInitiatedReceiverOnCommit(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{52})
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderKeyTweaked)

	_, err := sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverPubKey).
		SetStatus(st.TransferReceiverStatusInitiated).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	req := &pbinternal.SettleReceiverKeyTweakRequest{
		TransferId:                transfer.ID.String(),
		Action:                    pbinternal.SettleKeyTweakAction_COMMIT,
		ReceiverIdentityPublicKey: receiverPubKey.Serialize(),
	}
	err = handler.SettleReceiverKeyTweak(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid status")
	assert.Contains(t, err.Error(), "INITIATED")
}

// TestSettleReceiverKeyTweak_AcceptsReceiverClaimPendingOnRollback verifies
// the positive replacement coverage for the line-4906 switch: a receiver at
// RECEIVER_CLAIM_PENDING is still in the "do nothing" case on ROLLBACK after
// the INITIATED arm was removed.
func TestSettleReceiverKeyTweak_AcceptsReceiverClaimPendingOnRollback(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{53})
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderKeyTweaked)

	receiver, err := sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverPubKey).
		SetStatus(st.TransferReceiverStatusReceiverClaimPending).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	req := &pbinternal.SettleReceiverKeyTweakRequest{
		TransferId:                transfer.ID.String(),
		Action:                    pbinternal.SettleKeyTweakAction_ROLLBACK,
		ReceiverIdentityPublicKey: receiverPubKey.Serialize(),
	}
	err = handler.SettleReceiverKeyTweak(ctx, req)
	require.NoError(t, err)

	updated, err := sessionCtx.Client.TransferReceiver.Get(ctx, receiver.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferReceiverStatusReceiverClaimPending, updated.Status,
		"ROLLBACK on RECEIVER_CLAIM_PENDING is a no-op; status must not change")
}

// TestMimoReceiverStatusAuthoritative covers the gate that decides whether a
// claim skips advancing the parent transfers.status: knob on AND receiver count > 1.
// Tested directly because the gate is otherwise only reachable through the full
// claim/settle crypto paths; the end-to-end "parent stays SENDER_KEY_TWEAKED until
// the last receiver completes" behavior is exercised via scenario testing.
func TestMimoReceiverStatusAuthoritative(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{77})
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	buildTransfer := func(numReceivers int) *ent.Transfer {
		receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderKeyTweaked)
		for range numReceivers {
			_, err := sessionCtx.Client.TransferReceiver.Create().
				SetTransferID(transfer.ID).
				SetIdentityPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
				SetStatus(st.TransferReceiverStatusReceiverClaimPending).
				SetTransferType(transfer.Type).
				Save(ctx)
			require.NoError(t, err)
		}
		return transfer
	}

	withKnobs := func(values map[string]float64) context.Context {
		return knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(values))
	}
	knobOn := map[string]float64{
		knobs.KnobMimoAuthoritativeReceiverStatusEnabled: 1,
	}
	knobOff := map[string]float64{}

	t.Run("knob on, multi-receiver is receiver-authoritative", func(t *testing.T) {
		got, err := isMimoReceiverStatusAuthoritative(withKnobs(knobOn), buildTransfer(2))
		require.NoError(t, err)
		assert.True(t, got)
	})
	t.Run("knob on, single-receiver is not authoritative", func(t *testing.T) {
		got, err := isMimoReceiverStatusAuthoritative(withKnobs(knobOn), buildTransfer(1))
		require.NoError(t, err)
		assert.False(t, got)
	})
	t.Run("knob off, multi-receiver is not authoritative", func(t *testing.T) {
		got, err := isMimoReceiverStatusAuthoritative(withKnobs(knobOff), buildTransfer(2))
		require.NoError(t, err)
		assert.False(t, got)
	})
}
