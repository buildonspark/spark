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
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
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

// mimoEnabledContext injects a fixed knobs service with KnobMimoTransferMultiReceiverEnabled=1,
// simulating the MIMO feature flag being turned on.
func mimoEnabledContext(ctx context.Context) context.Context {
	return knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobMimoTransferMultiReceiverEnabled: 1,
	}))
}

//
// MIMO receiver enabled tests
//

// TestClaimTransferMIMO_ReceiverPubkeyMismatch verifies that calling ClaimTransfer with a
// pubkey that doesn't match any TransferReceiver record on the transfer is rejected. The
// transfer has a receiver with receiverPubKey, but the request uses wrongPubKey.
func TestClaimTransferMIMO_ReceiverPubkeyMismatch(t *testing.T) {
	sparktesting.RequireGripMock(t)
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	ctx = mimoEnabledContext(ctx)

	rng := rand.NewChaCha8([32]byte{11})
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	wrongPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderKeyTweaked)

	_, err := sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverPubKey).
		SetStatus(st.TransferReceiverStatusSenderInitiated).
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
	ctx = mimoEnabledContext(ctx)

	rng := rand.NewChaCha8([32]byte{12})
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderKeyTweaked)

	_, err := sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverPubKey).
		SetStatus(st.TransferReceiverStatusCompleted).
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
	ctx = mimoEnabledContext(ctx)

	rng := rand.NewChaCha8([32]byte{30})
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderInitiated)

	_, err := sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverPubKey).
		SetStatus(st.TransferReceiverStatusSenderInitiated).
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
	ctx = mimoEnabledContext(ctx)

	rng := rand.NewChaCha8([32]byte{31})
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusExpired)

	_, err := sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverPubKey).
		SetStatus(st.TransferReceiverStatusSenderInitiated).
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
	ctx = mimoEnabledContext(ctx)

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
		SetStatus(st.TransferReceiverStatusSenderInitiated).
		Save(ctx)
	require.NoError(t, err)

	sender, err := sessionCtx.Client.TransferSender.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(senderPubKey).
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
	ctx = mimoEnabledContext(ctx)

	rng := rand.NewChaCha8([32]byte{20})
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderKeyTweaked)

	_, err := sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverPubKey).
		SetStatus(st.TransferReceiverStatusCancelled).
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
// to KeyTweaked when a single receiver exists. Mimo enabled version..
func TestClaimTransferTweakKeys_DualWritesReceiverStatusMimoEnabled(t *testing.T) {
	sparktesting.RequireGripMock(t)
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	ctx = mimoEnabledContext(ctx)

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
		SetStatus(st.TransferReceiverStatusSenderInitiated).
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

//
// MIMO receiver disabled tests
//

// TestClaimTransferMIMO_FallsBackWhenNoReceivers verifies backward compatibility: when no
// TransferReceiver records exist for a transfer (pre-MIMO data), ClaimTransfer falls back to
// the legacy code path. Does not inject the MIMO-enabled context. Asserts the error does not
// contain the MIMO-specific "no transfer receivers found" message.
func TestClaimTransferMIMO_FallsBackWhenNoReceivers(t *testing.T) {
	sparktesting.RequireGripMock(t)
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{14})
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderKeyTweaked)
	_ = transfer

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	// No TransferReceiver records: should fall back to legacy path.
	req := &pb.ClaimTransferRequest{
		TransferId:             transfer.ID.String(),
		OwnerIdentityPublicKey: receiverPubKey.Serialize(),
		ClaimPackage: &pb.ClaimPackage{
			LeavesToClaim:   []*pb.UserSignedTxSigningJob{},
			KeyTweakPackage: map[string][]byte{},
		},
	}
	_, err := handler.ClaimTransfer(ctx, req)
	require.Error(t, err)
	// Verify we didn't hit MIMO-specific errors.
	assert.NotContains(t, err.Error(), "no transfer receivers found")
}

// TestClaimTransferTweakKeys_DualWritesReceiverStatus verifies that ClaimTransferTweakKeys
// updates both the Transfer status to ReceiverKeyTweaked AND the TransferReceiver status
// to KeyTweaked when a single receiver exists (MIMO knob disabled).
func TestClaimTransferTweakKeys_DualWritesReceiverStatus(t *testing.T) {
	sparktesting.RequireGripMock(t)
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{26})
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
		SetStatus(st.TransferReceiverStatusSenderInitiated).
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
		SetStatus(st.TransferReceiverStatusSenderInitiated).
		Save(ctx)
	require.NoError(t, err)

	_, err = sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiver2PubKey).
		SetStatus(st.TransferReceiverStatusSenderInitiated).
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
		Save(ctx)
	require.NoError(t, err)

	_, err = sessionCtx.Client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiver2PubKey).
		SetStatus(st.TransferReceiverStatusKeyTweaked).
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
		Save(ctx)
	require.NoError(t, err)

	// Set up VSS shares and key tweaks (same pattern as TestClaimTransferSignRefunds_Success).
	tweakPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	secretInt := new(big.Int).SetBytes(tweakPrivKey.Serialize())
	pubkeyShareTweakPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	cfg := sparktesting.TestConfig(t)
	threshold := int(cfg.Threshold)
	numberOfShares := len(cfg.SigningOperatorMap)

	shares, err := secretsharing.SplitSecretWithProofs(secretInt, secp256k1.S256().N, threshold, numberOfShares)
	require.NoError(t, err)
	require.NotEmpty(t, shares)

	share := shares[0]
	secretShareBytes := make([]byte, 32)
	share.Share.FillBytes(secretShareBytes)

	claimKeyTweak := &pb.ClaimLeafKeyTweak{
		SecretShareTweak: &pb.SecretShare{
			SecretShare: secretShareBytes,
			Proofs:      share.Proofs,
		},
		PubkeySharesTweak: map[string][]byte{
			"operator1": pubkeyShareTweakPubKey.Serialize(),
		},
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
			createTestLeafRefundTxSigningJob(t, rng, leaf),
		},
	}
	resp, err := handler.ClaimTransferSignRefunds(ctx, req)
	require.NoError(t, err)
	assert.NotNil(t, resp)

	// Verify receiver status was dual-written during the settle phase.
	updatedReceiver, err := sessionCtx.Client.TransferReceiver.Get(ctx, receiver.ID)
	require.NoError(t, err)
	assert.NotEqual(t, st.TransferReceiverStatusKeyTweaked, updatedReceiver.Status,
		"receiver status should have advanced beyond KeyTweaked")
}

//
// Unit tests for new helper functions
//

func TestIsMimoReceiveEnabled(t *testing.T) {
	baseCtx := t.Context()
	enabledCtx := mimoEnabledContext(baseCtx)
	disabledCtx := knobs.InjectKnobsService(baseCtx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobMimoTransferMultiReceiverEnabled: 0,
	}))

	// A non-nil receiver stand-in (fields don't matter for this function).
	dummyReceiver := &ent.TransferReceiver{}

	tests := []struct {
		name     string
		ctx      context.Context
		receiver *ent.TransferReceiver
		want     bool
	}{
		{"nil receiver, knob enabled", enabledCtx, nil, false},
		{"nil receiver, knob disabled", disabledCtx, nil, false},
		{"non-nil receiver, knob enabled", enabledCtx, dummyReceiver, true},
		{"non-nil receiver, knob disabled", disabledCtx, dummyReceiver, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isMimoReceiveEnabled(tc.ctx, tc.receiver)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestBuildFinalizeGossipMessage verifies that buildFinalizeGossipMessage produces the
// correct gossip message type and populates the expected fields:
//
//	mimoEnabled == true  → GossipMessageFinalizeTransferReceiver (with receiver pubkey)
//	mimoEnabled == false → GossipMessageFinalizeTransfer         (legacy, no receiver pubkey)
func TestBuildFinalizeGossipMessage(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{42})
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiver := &ent.TransferReceiver{IdentityPubkey: receiverPubKey}
	transferID := uuid.New()
	nodes := []*pbinternal.TreeNode{{Id: "node-1"}}
	ts := timestamppb.Now()

	t.Run("MIMO enabled: FinalizeTransferReceiver with receiver pubkey", func(t *testing.T) {
		msg := buildFinalizeGossipMessage(true, transferID, receiver, nodes, ts)
		inner := msg.GetFinalizeTransferReceiver()
		require.NotNil(t, inner, "expected FinalizeTransferReceiver message")
		assert.Nil(t, msg.GetFinalizeTransfer(), "should not contain legacy FinalizeTransfer")
		assert.Equal(t, transferID.String(), inner.TransferId)
		assert.Equal(t, receiverPubKey.Serialize(), inner.ReceiverIdentityPublicKey)
		assert.Equal(t, nodes, inner.InternalNodes)
		assert.True(t, proto.Equal(ts, inner.CompletionTimestamp))
	})

	t.Run("MIMO disabled: legacy FinalizeTransfer", func(t *testing.T) {
		msg := buildFinalizeGossipMessage(false, transferID, nil, nodes, ts)
		inner := msg.GetFinalizeTransfer()
		require.NotNil(t, inner, "expected FinalizeTransfer message")
		assert.Nil(t, msg.GetFinalizeTransferReceiver(), "should not contain FinalizeTransferReceiver")
		assert.Equal(t, transferID.String(), inner.TransferId)
		assert.Equal(t, nodes, inner.InternalNodes)
		assert.True(t, proto.Equal(ts, inner.CompletionTimestamp))
	})
}

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
		SetStatus(st.TransferReceiverStatusSenderInitiated).
		Save(ctx)
	require.NoError(t, err)

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	t.Run("nil transfer returns false", func(t *testing.T) {
		isMimo, receiver, err := handler.loadTransferReceiverByPublicKeyForUpdate(ctx, nil, &receiverPubKey)
		require.NoError(t, err)
		assert.False(t, isMimo)
		assert.Nil(t, receiver)
	})

	t.Run("nil pubkey returns false", func(t *testing.T) {
		isMimo, receiver, err := handler.loadTransferReceiverByPublicKeyForUpdate(ctx, transfer, nil)
		require.NoError(t, err)
		assert.False(t, isMimo)
		assert.Nil(t, receiver)
	})

	t.Run("matching receiver with knob enabled", func(t *testing.T) {
		enabledCtx := mimoEnabledContext(ctx)
		isMimo, receiver, err := handler.loadTransferReceiverByPublicKeyForUpdate(enabledCtx, transfer, &receiverPubKey)
		require.NoError(t, err)
		assert.True(t, isMimo)
		require.NotNil(t, receiver)
		assert.Equal(t, receiverPubKey, receiver.IdentityPubkey)
	})

	t.Run("matching receiver with knob disabled", func(t *testing.T) {
		isMimo, receiver, err := handler.loadTransferReceiverByPublicKeyForUpdate(ctx, transfer, &receiverPubKey)
		require.NoError(t, err)
		assert.False(t, isMimo)
		require.NotNil(t, receiver)
		assert.Equal(t, receiverPubKey, receiver.IdentityPubkey)
	})

	t.Run("no matching receiver with knob enabled returns error", func(t *testing.T) {
		enabledCtx := mimoEnabledContext(ctx)
		isMimo, receiver, err := handler.loadTransferReceiverByPublicKeyForUpdate(enabledCtx, transfer, &otherPubKey)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no transfer receivers found")
		assert.False(t, isMimo)
		assert.Nil(t, receiver)
	})

	t.Run("no matching receiver with knob disabled returns nil", func(t *testing.T) {
		isMimo, receiver, err := handler.loadTransferReceiverByPublicKeyForUpdate(ctx, transfer, &otherPubKey)
		require.NoError(t, err)
		assert.False(t, isMimo)
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
			SetStatus(st.TransferReceiverStatusSenderInitiated).
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
			SetStatus(st.TransferReceiverStatusSenderInitiated).
			Save(ctx)
		require.NoError(t, err)

		_, err = sessionCtx.Client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiver2PubKey).
			SetStatus(st.TransferReceiverStatusSenderInitiated).
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
		SetStatus(st.TransferReceiverStatusSenderInitiated).
		Save(ctx)
	require.NoError(t, err)

	sender, err := sessionCtx.Client.TransferSender.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(senderPubKey).
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
		leaves, err := getTransferLeavesForReceiverQuery(ctx, transfer, nil).All(ctx)
		require.NoError(t, err)
		assert.Len(t, leaves, 2)
	})

	t.Run("receiver with MIMO enabled scopes to receiver leaves only", func(t *testing.T) {
		enabledCtx := mimoEnabledContext(ctx)
		leaves, err := getTransferLeavesForReceiverQuery(enabledCtx, transfer, receiver).All(enabledCtx)
		require.NoError(t, err)
		assert.Len(t, leaves, 1)
	})

	t.Run("receiver with MIMO disabled returns all leaves", func(t *testing.T) {
		leaves, err := getTransferLeavesForReceiverQuery(ctx, transfer, receiver).All(ctx)
		require.NoError(t, err)
		assert.Len(t, leaves, 2)
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

	t.Run("MIMO enabled: reverts receiver and transfer from KeyTweaked", func(t *testing.T) {
		enabledCtx := mimoEnabledContext(ctx)
		leaf := createTestTreeNode(t, ctx, rng, sessionCtx.Client, tree, keyshare)
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusReceiverKeyTweaked)
		receiver, err := sessionCtx.Client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPubKey).
			SetStatus(st.TransferReceiverStatusKeyTweaked).
			Save(ctx)
		require.NoError(t, err)

		transferLeaf := createTestTransferLeaf(t, ctx, sessionCtx.Client, transfer, leaf)
		_, err = transferLeaf.Update().SetKeyTweak([]byte("some-tweak")).Save(ctx)
		require.NoError(t, err)

		err = handler.revertClaimTransfer(enabledCtx, transfer, receiver, []*ent.TransferLeaf{transferLeaf})
		require.NoError(t, err)

		updatedTransfer, err := sessionCtx.Client.Transfer.Get(ctx, transfer.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferStatusSenderKeyTweaked, updatedTransfer.Status)

		updatedReceiver, err := sessionCtx.Client.TransferReceiver.Get(ctx, receiver.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferReceiverStatusSenderInitiated, updatedReceiver.Status)

		updatedLeaf, err := sessionCtx.Client.TransferLeaf.Get(ctx, transferLeaf.ID)
		require.NoError(t, err)
		assert.Nil(t, updatedLeaf.KeyTweak)
	})

	t.Run("MIMO enabled: rejects revert when receiver key tweak already applied", func(t *testing.T) {
		enabledCtx := mimoEnabledContext(ctx)
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusReceiverKeyTweaked)
		receiver, err := sessionCtx.Client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPubKey).
			SetStatus(st.TransferReceiverStatusKeyTweakApplied).
			Save(ctx)
		require.NoError(t, err)

		err = handler.revertClaimTransfer(enabledCtx, transfer, receiver, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already applied")
	})

	t.Run("MIMO enabled: no-op for early receiver status", func(t *testing.T) {
		enabledCtx := mimoEnabledContext(ctx)
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderKeyTweaked)
		receiver, err := sessionCtx.Client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPubKey).
			SetStatus(st.TransferReceiverStatusSenderInitiated).
			Save(ctx)
		require.NoError(t, err)

		err = handler.revertClaimTransfer(enabledCtx, transfer, receiver, nil)
		require.NoError(t, err)

		updatedReceiver, err := sessionCtx.Client.TransferReceiver.Get(ctx, receiver.ID)
		require.NoError(t, err)
		assert.Equal(t, st.TransferReceiverStatusSenderInitiated, updatedReceiver.Status)
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
		assert.Equal(t, st.TransferReceiverStatusSenderInitiated, updatedReceiver.Status)
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

	t.Run("MIMO: receiver at RefundSigned returns early", func(t *testing.T) {
		enabledCtx := mimoEnabledContext(ctx)
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusReceiverRefundSigned)

		_, err := sessionCtx.Client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPubKey).
			SetStatus(st.TransferReceiverStatusRefundSigned).
			Save(ctx)
		require.NoError(t, err)

		req := &pbinternal.InitiateSettleReceiverKeyTweakRequest{
			TransferId:                transfer.ID.String(),
			ReceiverIdentityPublicKey: receiverPubKey.Serialize(),
		}
		err = handler.InitiateSettleReceiverKeyTweak(enabledCtx, req)
		assert.NoError(t, err)
	})

	t.Run("legacy: transfer at ReceiverRefundSigned returns early", func(t *testing.T) {
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusReceiverRefundSigned)

		req := &pbinternal.InitiateSettleReceiverKeyTweakRequest{
			TransferId:                transfer.ID.String(),
			ReceiverIdentityPublicKey: receiverPubKey.Serialize(),
		}
		err := handler.InitiateSettleReceiverKeyTweak(ctx, req)
		assert.NoError(t, err)
	})
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
		enabledCtx := mimoEnabledContext(ctx)
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusReceiverRefundSigned)

		_, err := sessionCtx.Client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPubKey).
			SetStatus(st.TransferReceiverStatusRefundSigned).
			Save(ctx)
		require.NoError(t, err)

		req := &pbinternal.SettleReceiverKeyTweakRequest{
			TransferId:                transfer.ID.String(),
			Action:                    pbinternal.SettleKeyTweakAction_COMMIT,
			ReceiverIdentityPublicKey: receiverPubKey.Serialize(),
		}
		err = handler.SettleReceiverKeyTweak(enabledCtx, req)
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
		enabledCtx := mimoEnabledContext(ctx)
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderInitiated)

		_, err := sessionCtx.Client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPubKey).
			SetStatus(st.TransferReceiverStatusSenderInitiated).
			Save(ctx)
		require.NoError(t, err)

		req := &pbinternal.InitiateSettleReceiverKeyTweakRequest{
			TransferId:                transfer.ID.String(),
			ReceiverIdentityPublicKey: receiverPubKey.Serialize(),
		}
		err = handler.InitiateSettleReceiverKeyTweak(enabledCtx, req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not ready for receiver claim")
	})

	t.Run("MIMO: rejects Expired transfer", func(t *testing.T) {
		enabledCtx := mimoEnabledContext(ctx)
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusExpired)

		_, err := sessionCtx.Client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPubKey).
			SetStatus(st.TransferReceiverStatusSenderInitiated).
			Save(ctx)
		require.NoError(t, err)

		req := &pbinternal.InitiateSettleReceiverKeyTweakRequest{
			TransferId:                transfer.ID.String(),
			ReceiverIdentityPublicKey: receiverPubKey.Serialize(),
		}
		err = handler.InitiateSettleReceiverKeyTweak(enabledCtx, req)
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
		enabledCtx := mimoEnabledContext(ctx)
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderInitiated)

		_, err := sessionCtx.Client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPubKey).
			SetStatus(st.TransferReceiverStatusSenderInitiated).
			Save(ctx)
		require.NoError(t, err)

		req := &pbinternal.SettleReceiverKeyTweakRequest{
			TransferId:                transfer.ID.String(),
			Action:                    pbinternal.SettleKeyTweakAction_COMMIT,
			ReceiverIdentityPublicKey: receiverPubKey.Serialize(),
		}
		err = handler.SettleReceiverKeyTweak(enabledCtx, req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not ready for receiver claim")
	})

	t.Run("MIMO: ROLLBACK proceeds despite SenderInitiated transfer", func(t *testing.T) {
		enabledCtx := mimoEnabledContext(ctx)
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusSenderInitiated)

		_, err := sessionCtx.Client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPubKey).
			SetStatus(st.TransferReceiverStatusSenderInitiated).
			Save(ctx)
		require.NoError(t, err)

		req := &pbinternal.SettleReceiverKeyTweakRequest{
			TransferId:                transfer.ID.String(),
			Action:                    pbinternal.SettleKeyTweakAction_ROLLBACK,
			ReceiverIdentityPublicKey: receiverPubKey.Serialize(),
		}
		err = handler.SettleReceiverKeyTweak(enabledCtx, req)
		require.NoError(t, err)
	})

	t.Run("MIMO: ROLLBACK proceeds despite Expired transfer", func(t *testing.T) {
		enabledCtx := mimoEnabledContext(ctx)
		transfer := createTestTransferForMIMO(t, ctx, sessionCtx.Client, senderPubKey, receiverPubKey, st.TransferStatusExpired)

		_, err := sessionCtx.Client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(receiverPubKey).
			SetStatus(st.TransferReceiverStatusSenderInitiated).
			Save(ctx)
		require.NoError(t, err)

		req := &pbinternal.SettleReceiverKeyTweakRequest{
			TransferId:                transfer.ID.String(),
			Action:                    pbinternal.SettleKeyTweakAction_ROLLBACK,
			ReceiverIdentityPublicKey: receiverPubKey.Serialize(),
		}
		err = handler.SettleReceiverKeyTweak(enabledCtx, req)
		require.NoError(t, err)
	})
}

func TestStartTransferV3_MultiReceiverRequiresKnob(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{50})
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiver1PubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiver2PubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	// Build a minimal V3 request with two distinct receivers.
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

	t.Run("multi-receiver rejected when knob disabled", func(t *testing.T) {
		ctx := knobs.InjectKnobsService(t.Context(), knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobMimoTransferMultiReceiverEnabled: 0,
		}))
		_, err := handler.startTransferV3Internal(ctx, makeReq(map[string][]byte{
			"leaf-1": receiver1PubKey.Serialize(),
			"leaf-2": receiver2PubKey.Serialize(),
		}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "multi-receiver transfers are not enabled")
	})

	t.Run("multi-receiver rejected when knob service absent", func(t *testing.T) {
		// No knob service injected at all.
		_, err := handler.startTransferV3Internal(t.Context(), makeReq(map[string][]byte{
			"leaf-1": receiver1PubKey.Serialize(),
			"leaf-2": receiver2PubKey.Serialize(),
		}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "multi-receiver transfers are not enabled")
	})

	t.Run("multi-receiver allowed when knob enabled", func(t *testing.T) {
		ctx := mimoEnabledContext(t.Context())
		_, err := handler.startTransferV3Internal(ctx, makeReq(map[string][]byte{
			"leaf-1": receiver1PubKey.Serialize(),
			"leaf-2": receiver2PubKey.Serialize(),
		}))
		// Should pass the knob check and fail later (e.g., transfer package validation).
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "multi-receiver transfers are not enabled")
	})

	t.Run("single-receiver allowed regardless of knob", func(t *testing.T) {
		ctx := knobs.InjectKnobsService(t.Context(), knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobMimoTransferMultiReceiverEnabled: 0,
		}))
		_, err := handler.startTransferV3Internal(ctx, makeReq(map[string][]byte{
			"leaf-1": receiver1PubKey.Serialize(),
		}))
		// Should pass the knob check and fail later.
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "multi-receiver transfers are not enabled")
	})
}
