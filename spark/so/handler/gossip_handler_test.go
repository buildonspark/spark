package handler

import (
	"context"
	"crypto/sha256"
	"errors"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/preimagerequest"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	msdk "go.opentelemetry.io/otel/sdk/metric"
	md "go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

func TestHandleCancelTransferGossipMessage_NonExistentTransfer_Succeeds(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)

	handler := NewGossipHandler(config)

	nonExistentTransferID := uuid.New()
	cancelTransfer := &pbgossip.GossipMessageCancelTransfer{
		TransferId: nonExistentTransferID.String(),
	}

	err := handler.handleCancelTransferGossipMessage(ctx, cancelTransfer)

	require.NoError(t, err, "cancelling a non-existent transfer should succeed")
}

func TestHandleCancelTransferGossipMessage_InvalidTransferID_ReturnsError(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx := t.Context()

	handler := NewGossipHandler(config)

	cancelTransfer := &pbgossip.GossipMessageCancelTransfer{
		TransferId: "not-a-valid-uuid",
	}

	err := handler.handleCancelTransferGossipMessage(ctx, cancelTransfer)

	require.Error(t, err, "cancelling with a malformed transfer ID should return an error")
}

func TestHandleRollbackTransfer_NonExistentTransfer_Succeeds(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)

	handler := NewGossipHandler(config)

	nonExistentTransferID := uuid.New()
	rollbackTransfer := &pbgossip.GossipMessageRollbackTransfer{
		TransferId: nonExistentTransferID.String(),
	}

	err := handler.handleRollbackTransfer(ctx, rollbackTransfer)

	require.NoError(t, err, "rolling back a non-existent transfer should succeed")
}

func TestHandleRollbackTransfer_InvalidTransferID_ReturnsError(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx := t.Context()

	handler := NewGossipHandler(config)

	rollbackTransfer := &pbgossip.GossipMessageRollbackTransfer{
		TransferId: "not-a-valid-uuid",
	}

	err := handler.handleRollbackTransfer(ctx, rollbackTransfer)

	require.Error(t, err, "rolling back with a malformed transfer ID should return an error")
}

func TestHandlePreimageSwapGossipScopesPreimageByPreimageRequestTransferID(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	preimage := make([]byte, sha256.Size)
	preimage[0] = 42
	paymentHash := sha256.Sum256(preimage)

	sender := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	otherReceiver := keys.GeneratePrivateKey().Public()
	transfer := createPreimageGossipTestTransfer(t, ctx, client, sender, receiver)
	otherTransfer := createPreimageGossipTestTransfer(t, ctx, client, sender, otherReceiver)
	request := createPreimageGossipTestRequest(t, ctx, client, paymentHash[:], receiver, transfer)
	otherRequest := createPreimageGossipTestRequest(t, ctx, client, paymentHash[:], otherReceiver, otherTransfer)

	err = NewGossipHandler(sparktesting.TestConfig(t)).handlePreimageSwapGossipMessage(ctx, &pbgossip.GossipMessagePreimageSwap{
		Preimage:                  preimage,
		PaymentHash:               paymentHash[:],
		PreimageRequestTransferId: transfer.ID.String(),
	}, false)
	require.NoError(t, err)

	updated, err := client.PreimageRequest.Get(ctx, request.ID)
	require.NoError(t, err)
	require.Equal(t, preimage, updated.Preimage)
	require.Equal(t, st.PreimageRequestStatusPreimageShared, updated.Status)

	unchanged, err := client.PreimageRequest.Get(ctx, otherRequest.ID)
	require.NoError(t, err)
	require.Empty(t, unchanged.Preimage)
	require.Equal(t, st.PreimageRequestStatusWaitingForPreimage, unchanged.Status)
}

func TestHandlePreimageSwapGossipScopesPreimageByLegacyTransferID(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	preimage := make([]byte, sha256.Size)
	preimage[0] = 46
	paymentHash := sha256.Sum256(preimage)

	sender := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	otherReceiver := keys.GeneratePrivateKey().Public()
	transfer := createPreimageGossipTestTransfer(t, ctx, client, sender, receiver)
	otherTransfer := createPreimageGossipTestTransfer(t, ctx, client, sender, otherReceiver)
	request := createPreimageGossipTestRequest(t, ctx, client, paymentHash[:], receiver, transfer)
	otherRequest := createPreimageGossipTestRequest(t, ctx, client, paymentHash[:], otherReceiver, otherTransfer)

	err = NewGossipHandler(sparktesting.TestConfig(t)).handlePreimageSwapGossipMessage(ctx, &pbgossip.GossipMessagePreimageSwap{
		Preimage:    preimage,
		PaymentHash: paymentHash[:],
		TransferId:  transfer.ID.String(),
	}, false)
	require.NoError(t, err)

	updated, err := client.PreimageRequest.Get(ctx, request.ID)
	require.NoError(t, err)
	require.Equal(t, preimage, updated.Preimage)
	require.Equal(t, st.PreimageRequestStatusPreimageShared, updated.Status)

	unchanged, err := client.PreimageRequest.Get(ctx, otherRequest.ID)
	require.NoError(t, err)
	require.Empty(t, unchanged.Preimage)
	require.Equal(t, st.PreimageRequestStatusWaitingForPreimage, unchanged.Status)
}

func TestBuildPreimageSwapGossipMessageUsesBindingFieldWithoutLegacyTransferIDWhenNoKeyTweaks(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	preimage := make([]byte, sha256.Size)
	preimage[0] = 47
	paymentHash := sha256.Sum256(preimage)
	sender := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	transfer := createPreimageGossipTestTransfer(t, ctx, client, sender, receiver)

	gossip, err := buildPreimageSwapGossipMessage(ctx, preimage, paymentHash[:], transfer, false)
	require.NoError(t, err)
	require.Equal(t, preimage, gossip.GetPreimage())
	require.Equal(t, paymentHash[:], gossip.GetPaymentHash())
	require.Equal(t, transfer.ID.String(), gossip.GetPreimageRequestTransferId())
	require.Empty(t, gossip.GetTransferId())
	require.Empty(t, gossip.GetSenderKeyTweakProofs())
}

func TestHandlePreimageSwapGossipRejectsAmbiguousPaymentHashWithoutTransferID(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	preimage := make([]byte, sha256.Size)
	preimage[0] = 43
	paymentHash := sha256.Sum256(preimage)

	sender := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	otherReceiver := keys.GeneratePrivateKey().Public()
	createPreimageGossipTestRequest(t, ctx, client, paymentHash[:], receiver, createPreimageGossipTestTransfer(t, ctx, client, sender, receiver))
	createPreimageGossipTestRequest(t, ctx, client, paymentHash[:], otherReceiver, createPreimageGossipTestTransfer(t, ctx, client, sender, otherReceiver))

	err = NewGossipHandler(sparktesting.TestConfig(t)).handlePreimageSwapGossipMessage(ctx, &pbgossip.GossipMessagePreimageSwap{
		Preimage:    preimage,
		PaymentHash: paymentHash[:],
	}, false)

	require.ErrorContains(t, err, "matches multiple preimage requests without a transfer binding")
	count, err := client.PreimageRequest.Query().
		Where(preimagerequest.PaymentHashEQ(paymentHash[:]), preimagerequest.PreimageIsNil()).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, count)
}

// TestHandlePreimageGossipRejectsNon32BytePreimage verifies that a gossiped
// preimage whose length is not 32 bytes is rejected before it can be persisted,
// mirroring the length guard the user-facing ValidatePreimage path enforces.
func TestHandlePreimageGossipRejectsNon32BytePreimage(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	short := []byte("too_short")
	paymentHash := sha256.Sum256(short)

	err := NewGossipHandler(sparktesting.TestConfig(t)).handlePreimageGossipMessage(ctx, &pbgossip.GossipMessagePreimage{
		Preimage:    short,
		PaymentHash: paymentHash[:],
	}, false)
	require.ErrorContains(t, err, "preimage must be")
}

// TestHandlePreimageSwapGossipRejectsNon32BytePreimage is the same guard for the
// swap-gossip path.
func TestHandlePreimageSwapGossipRejectsNon32BytePreimage(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	short := []byte("too_short")
	paymentHash := sha256.Sum256(short)

	err := NewGossipHandler(sparktesting.TestConfig(t)).handlePreimageSwapGossipMessage(ctx, &pbgossip.GossipMessagePreimageSwap{
		Preimage:    short,
		PaymentHash: paymentHash[:],
	}, false)
	require.ErrorContains(t, err, "preimage must be")
}

func TestHandlePreimageGossipIgnoresReturnedRequestWhenActiveRequestExists(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	preimage := make([]byte, sha256.Size)
	preimage[0] = 44
	paymentHash := sha256.Sum256(preimage)

	sender := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	returnedRequest := createPreimageGossipTestRequest(t, ctx, client, paymentHash[:], receiver, createPreimageGossipTestTransfer(t, ctx, client, sender, receiver))
	returnedRequest, err = returnedRequest.Update().
		SetStatus(st.PreimageRequestStatusReturned).
		Save(ctx)
	require.NoError(t, err)
	activeRequest := createPreimageGossipTestRequest(t, ctx, client, paymentHash[:], receiver, createPreimageGossipTestTransfer(t, ctx, client, sender, receiver))

	err = NewGossipHandler(sparktesting.TestConfig(t)).handlePreimageGossipMessage(ctx, &pbgossip.GossipMessagePreimage{
		Preimage:    preimage,
		PaymentHash: paymentHash[:],
	}, false)
	require.NoError(t, err)

	updated, err := client.PreimageRequest.Get(ctx, activeRequest.ID)
	require.NoError(t, err)
	require.Equal(t, preimage, updated.Preimage)

	unchanged, err := client.PreimageRequest.Get(ctx, returnedRequest.ID)
	require.NoError(t, err)
	require.Empty(t, unchanged.Preimage)
	require.Equal(t, st.PreimageRequestStatusReturned, unchanged.Status)
}

func TestHandlePreimageSwapGossipIgnoresReturnedRequestWhenActiveRequestExistsWithoutTransferID(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	preimage := make([]byte, sha256.Size)
	preimage[0] = 45
	paymentHash := sha256.Sum256(preimage)

	sender := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	returnedRequest := createPreimageGossipTestRequest(t, ctx, client, paymentHash[:], receiver, createPreimageGossipTestTransfer(t, ctx, client, sender, receiver))
	returnedRequest, err = returnedRequest.Update().
		SetStatus(st.PreimageRequestStatusReturned).
		Save(ctx)
	require.NoError(t, err)
	activeRequest := createPreimageGossipTestRequest(t, ctx, client, paymentHash[:], receiver, createPreimageGossipTestTransfer(t, ctx, client, sender, receiver))

	err = NewGossipHandler(sparktesting.TestConfig(t)).handlePreimageSwapGossipMessage(ctx, &pbgossip.GossipMessagePreimageSwap{
		Preimage:    preimage,
		PaymentHash: paymentHash[:],
	}, false)
	require.NoError(t, err)

	updated, err := client.PreimageRequest.Get(ctx, activeRequest.ID)
	require.NoError(t, err)
	require.Equal(t, preimage, updated.Preimage)
	require.Equal(t, st.PreimageRequestStatusPreimageShared, updated.Status)

	unchanged, err := client.PreimageRequest.Get(ctx, returnedRequest.ID)
	require.NoError(t, err)
	require.Empty(t, unchanged.Preimage)
	require.Equal(t, st.PreimageRequestStatusReturned, unchanged.Status)
}

// The handler's settlement guard only commits sender key tweaks when
// transfer_id is present alongside the proofs, so the builder must keep
// setting the legacy field (in addition to the binding field) whenever key
// tweaks are included. This protects the rolling-deploy invariant.
func TestBuildPreimageSwapGossipMessageWithKeyTweaksSetsBothTransferIDs(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	preimage := make([]byte, sha256.Size)
	preimage[0] = 48
	paymentHash := sha256.Sum256(preimage)
	sender := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	transfer := createPreimageGossipTestTransfer(t, ctx, client, sender, receiver)

	keysharePrivKey := keys.GeneratePrivateKey()
	signingKeyshare, err := client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(keysharePrivKey).
		SetPublicShares(map[string]keys.Public{"test": keys.GeneratePrivateKey().Public()}).
		SetPublicKey(keysharePrivKey.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	tree, err := client.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Regtest).
		SetOwnerIdentityPubkey(sender).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	leaf, err := client.TreeNode.Create().
		SetStatus(st.TreeNodeStatusAvailable).
		SetTree(tree).
		SetNetwork(tree.Network).
		SetSigningKeyshare(signingKeyshare).
		SetValue(1000).
		SetVerifyingPubkey(keys.GeneratePrivateKey().Public()).
		SetOwnerIdentityPubkey(sender).
		SetOwnerSigningPubkey(keys.GeneratePrivateKey().Public()).
		SetRawTx(createTestTxBytes(t, 6000)).
		SetRawRefundTx(createTestTxBytes(t, 6001)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	proofs := [][]byte{[]byte("key-tweak-proof-bytes")}
	keyTweakBytes, err := proto.Marshal(&pb.SendLeafKeyTweak{
		LeafId:           leaf.ID.String(),
		SecretShareTweak: &pb.SecretShare{Proofs: proofs},
	})
	require.NoError(t, err)
	_, err = client.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(leaf).
		SetPreviousRefundTx(createTestTxBytes(t, 6002)).
		SetIntermediateRefundTx(createTestTxBytes(t, 6003)).
		SetKeyTweak(keyTweakBytes).
		Save(ctx)
	require.NoError(t, err)

	gossip, err := buildPreimageSwapGossipMessage(ctx, preimage, paymentHash[:], transfer, true)
	require.NoError(t, err)
	require.Equal(t, preimage, gossip.GetPreimage())
	require.Equal(t, paymentHash[:], gossip.GetPaymentHash())
	require.Equal(t, transfer.ID.String(), gossip.GetTransferId())
	require.Equal(t, transfer.ID.String(), gossip.GetPreimageRequestTransferId())
	require.Len(t, gossip.GetSenderKeyTweakProofs(), 1)
	require.Equal(t, proofs, gossip.GetSenderKeyTweakProofs()[leaf.ID.String()].GetProofs())
}

func TestHandlePreimageSwapGossipRejectsMismatchedTransferBinding(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	preimage := make([]byte, sha256.Size)
	preimage[0] = 49
	paymentHash := sha256.Sum256(preimage)

	sender := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	otherReceiver := keys.GeneratePrivateKey().Public()
	transfer := createPreimageGossipTestTransfer(t, ctx, client, sender, receiver)
	otherTransfer := createPreimageGossipTestTransfer(t, ctx, client, sender, otherReceiver)
	createPreimageGossipTestRequest(t, ctx, client, paymentHash[:], receiver, transfer)
	createPreimageGossipTestRequest(t, ctx, client, paymentHash[:], otherReceiver, otherTransfer)

	err = NewGossipHandler(sparktesting.TestConfig(t)).handlePreimageSwapGossipMessage(ctx, &pbgossip.GossipMessagePreimageSwap{
		Preimage:                  preimage,
		PaymentHash:               paymentHash[:],
		TransferId:                transfer.ID.String(),
		PreimageRequestTransferId: otherTransfer.ID.String(),
	}, false)

	require.ErrorContains(t, err, "does not match preimage_request_transfer_id")
	count, err := client.PreimageRequest.Query().
		Where(
			preimagerequest.PaymentHashEQ(paymentHash[:]),
			preimagerequest.PreimageIsNil(),
			preimagerequest.StatusEQ(st.PreimageRequestStatusWaitingForPreimage),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, count)
}

func TestHandlePreimageSwapGossipRejectsBoundTransferWithNoMatchingRequest(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	preimage := make([]byte, sha256.Size)
	preimage[0] = 50
	paymentHash := sha256.Sum256(preimage)

	sender := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	otherReceiver := keys.GeneratePrivateKey().Public()
	transfer := createPreimageGossipTestTransfer(t, ctx, client, sender, receiver)
	request := createPreimageGossipTestRequest(t, ctx, client, paymentHash[:], receiver, transfer)
	unboundTransfer := createPreimageGossipTestTransfer(t, ctx, client, sender, otherReceiver)

	err = NewGossipHandler(sparktesting.TestConfig(t)).handlePreimageSwapGossipMessage(ctx, &pbgossip.GossipMessagePreimageSwap{
		Preimage:                  preimage,
		PaymentHash:               paymentHash[:],
		PreimageRequestTransferId: unboundTransfer.ID.String(),
	}, false)

	require.ErrorContains(t, err, "did not match a preimage request for payment hash")
	unchanged, err := client.PreimageRequest.Get(ctx, request.ID)
	require.NoError(t, err)
	require.Empty(t, unchanged.Preimage)
	require.Equal(t, st.PreimageRequestStatusWaitingForPreimage, unchanged.Status)
}

func createPreimageGossipTestTransfer(t *testing.T, ctx context.Context, client *ent.Client, sender keys.Public, receiver keys.Public) *ent.Transfer {
	t.Helper()

	transfer, err := client.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TransferStatusSenderInitiated).
		SetType(st.TransferTypePreimageSwap).
		SetSenderIdentityPubkey(sender).
		SetReceiverIdentityPubkey(receiver).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(time.Hour)).
		Save(ctx)
	require.NoError(t, err)
	return transfer
}

func createPreimageGossipTestRequest(t *testing.T, ctx context.Context, client *ent.Client, paymentHash []byte, receiver keys.Public, transfer *ent.Transfer) *ent.PreimageRequest {
	t.Helper()

	request, err := client.PreimageRequest.Create().
		SetPaymentHash(paymentHash).
		SetReceiverIdentityPubkey(receiver).
		SetStatus(st.PreimageRequestStatusWaitingForPreimage).
		SetTransfers(transfer).
		Save(ctx)
	require.NoError(t, err)
	return request
}

func TestHandleSettleSenderKeyTweakGossipMessage_InvalidTransferID_ReturnsError(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx := t.Context()

	handler := NewGossipHandler(config)

	settleSenderKeyTweak := &pbgossip.GossipMessageSettleSenderKeyTweak{
		TransferId: "not-a-valid-uuid",
	}

	err := handler.handleSettleSenderKeyTweakGossipMessage(ctx, settleSenderKeyTweak)

	require.Error(t, err, "settling sender key tweak with a malformed transfer ID should return an error")
}

func TestHandleRollbackUtxoSwapGossipMessage_NonExistentUtxo_Succeeds(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewGossipHandler(cfg)

	nonExistentTxid := chainhash.DoubleHashB([]byte("nonexistent_txid_for_gossip_test"))
	rollbackRequest, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(ctx, cfg, &pb.UTXO{
		Txid:    nonExistentTxid,
		Vout:    0,
		Network: pb.Network_REGTEST,
	}, nil)
	require.NoError(t, err)

	gossipMsg := &pbgossip.GossipMessageRollbackUtxoSwap{
		OnChainUtxo:          rollbackRequest.GetOnChainUtxo(),
		Signature:            rollbackRequest.GetSignature(),
		CoordinatorPublicKey: rollbackRequest.GetCoordinatorPublicKey(),
	}

	err = handler.handleRollbackUtxoSwapGossipMessage(ctx, gossipMsg)
	require.NoError(t, err, "rolling back a non-existent UTXO should succeed")
}

func TestHandleArchiveStaticDepositAddressGossipMessageSkipsCoordinatorDelivery(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewGossipHandler(cfg)

	depositAddress, ownerIdentityPubKey, address := createDefaultStaticDepositAddressForGossipTest(t, ctx)
	attackerPrivKey := keys.GeneratePrivateKey()

	err := handler.handleArchiveStaticDepositAddressGossipMessage(ctx, archiveStaticDepositAddressGossip(
		t,
		attackerPrivKey,
		ownerIdentityPubKey,
		address,
	),
		true, /* forCoordinator */
	)

	require.NoError(t, err)
	updatedAddress, err := sessionClient(t, ctx).DepositAddress.Get(ctx, depositAddress.ID)
	require.NoError(t, err)
	require.True(t, updatedAddress.IsDefault, "coordinator delivery must not mutate deposit address state")
}

func TestHandleArchiveStaticDepositAddressGossipMessageAcceptsOperatorSignature(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	handler := NewGossipHandler(cfg)

	depositAddress, ownerIdentityPubKey, address := createDefaultStaticDepositAddressForGossipTest(t, ctx)

	err := handler.handleArchiveStaticDepositAddressGossipMessage(ctx, archiveStaticDepositAddressGossip(
		t,
		cfg.IdentityPrivateKey,
		ownerIdentityPubKey,
		address,
	),
		false, /* forCoordinator */
	)

	require.NoError(t, err)
	updatedAddress, err := sessionClient(t, ctx).DepositAddress.Get(ctx, depositAddress.ID)
	require.NoError(t, err)
	require.False(t, updatedAddress.IsDefault)
}

// --- Consensus commit / rollback row transitions ---

// sessionClient returns the Ent client backed by the same session-managed
// transaction the handlers use (via ent.GetDbFromContext). Tests must insert
// setup rows and read back via this client so writes are visible across
// handler boundaries without needing explicit commits.
func sessionClient(t *testing.T, ctx context.Context) *ent.Client {
	t.Helper()
	tx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	return tx.Client()
}

func createDefaultStaticDepositAddressForGossipTest(t *testing.T, ctx context.Context) (*ent.DepositAddress, keys.Public, string) {
	t.Helper()
	client := sessionClient(t, ctx)

	ownerIdentityPrivKey := keys.GeneratePrivateKey()
	ownerSigningPrivKey := keys.GeneratePrivateKey()
	operatorSharePrivKey := keys.GeneratePrivateKey()
	keysharePubKey := keys.GeneratePrivateKey().Public()

	signingKeyshare, err := client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(operatorSharePrivKey).
		SetPublicShares(map[string]keys.Public{"operator": operatorSharePrivKey.Public()}).
		SetPublicKey(keysharePubKey).
		SetMinSigners(1).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	address := "archive-gossip-" + uuid.NewString()
	depositAddress, err := client.DepositAddress.Create().
		SetAddress(address).
		SetOwnerIdentityPubkey(ownerIdentityPrivKey.Public()).
		SetOwnerSigningPubkey(ownerSigningPrivKey.Public()).
		SetSigningKeyshare(signingKeyshare).
		SetNetwork(btcnetwork.Regtest).
		SetIsStatic(true).
		SetIsDefault(true).
		SetAddressSignatures(map[string][]byte{"operator": []byte("address-signature")}).
		SetPossessionSignature([]byte("possession-signature")).
		Save(ctx)
	require.NoError(t, err)
	return depositAddress, ownerIdentityPrivKey.Public(), address
}

func archiveStaticDepositAddressGossip(
	t *testing.T,
	coordinatorPrivKey keys.Private,
	ownerIdentityPubKey keys.Public,
	address string,
) *pbgossip.GossipMessageArchiveStaticDepositAddress {
	t.Helper()
	messageHash, err := CreateArchiveStaticDepositAddressStatement(ownerIdentityPubKey, btcnetwork.Regtest, address)
	require.NoError(t, err)
	signature := ecdsa.Sign(coordinatorPrivKey.ToBTCEC(), messageHash)

	return &pbgossip.GossipMessageArchiveStaticDepositAddress{
		OwnerIdentityPublicKey: ownerIdentityPubKey.Serialize(),
		Network:                pb.Network_REGTEST,
		Address:                address,
		Signature:              signature.Serialize(),
		CoordinatorPublicKey:   coordinatorPrivKey.Public().Serialize(),
	}
}

// insertParticipantRow inserts a PARTICIPANT FlowExecution row keyed by id
// in IN_FLIGHT status. The op_type is fixed to STORE_PREIMAGE_SHARE because
// that flow's Commit and Rollback are no-ops, so the tests focus on the row
// transition rather than any domain-specific commit/rollback effect.
func insertParticipantRow(t *testing.T, ctx context.Context, id uuid.UUID) *ent.FlowExecution {
	t.Helper()
	row, err := sessionClient(t, ctx).FlowExecution.Create().
		SetID(id).
		SetRole(st.FlowExecutionRoleParticipant).
		SetOpType(int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE)).
		SetCoordinatorIndex(1).
		Save(ctx)
	require.NoError(t, err)
	return row
}

// consensusCommitMessage builds a GossipMessage carrying a ConsensusCommit for
// the STORE_PREIMAGE_SHARE flow (no-op Commit) with the provided execution id.
func consensusCommitMessage(t *testing.T, executionID string) *pbgossip.GossipMessage {
	t.Helper()
	opAny, err := anypb.New(&pbinternal.StorePreimageSharePrepareRequest{})
	require.NoError(t, err)
	return &pbgossip.GossipMessage{
		MessageId: uuid.NewString(),
		Message: &pbgossip.GossipMessage_ConsensusCommit{
			ConsensusCommit: &pbgossip.GossipMessageConsensusCommit{
				OpType:          pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE,
				Operation:       opAny,
				FlowExecutionId: executionID,
			},
		},
	}
}

// consensusRollbackMessage mirrors consensusCommitMessage for the rollback side.
func consensusRollbackMessage(t *testing.T, executionID string) *pbgossip.GossipMessage {
	t.Helper()
	opAny, err := anypb.New(&pbinternal.StorePreimageSharePrepareRequest{})
	require.NoError(t, err)
	return &pbgossip.GossipMessage{
		MessageId: uuid.NewString(),
		Message: &pbgossip.GossipMessage_ConsensusRollback{
			ConsensusRollback: &pbgossip.GossipMessageConsensusRollback{
				OpType:          pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE,
				Operation:       opAny,
				FlowExecutionId: executionID,
			},
		},
	}
}

func TestClassifyConsensusOp(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	opType := int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER)

	participant := uuid.New()
	_, err = client.FlowExecution.Create().
		SetID(participant).SetRole(st.FlowExecutionRoleParticipant).
		SetOpType(opType).SetCoordinatorIndex(1).Save(ctx)
	require.NoError(t, err)

	coordinator := uuid.New()
	_, err = client.FlowExecution.Create().
		SetID(coordinator).SetRole(st.FlowExecutionRoleCoordinator).
		SetOpType(opType).SetCoordinatorIndex(0).Save(ctx)
	require.NoError(t, err)

	participantCommitted := uuid.New()
	_, err = client.FlowExecution.Create().
		SetID(participantCommitted).SetRole(st.FlowExecutionRoleParticipant).
		SetOpType(opType).SetCoordinatorIndex(1).
		SetStatus(st.FlowExecutionStatusCommitted).Save(ctx)
	require.NoError(t, err)

	participantRolledBack := uuid.New()
	_, err = client.FlowExecution.Create().
		SetID(participantRolledBack).SetRole(st.FlowExecutionRoleParticipant).
		SetOpType(opType).SetCoordinatorIndex(1).
		SetStatus(st.FlowExecutionStatusRolledBack).Save(ctx)
	require.NoError(t, err)

	participantWrongOp := uuid.New()
	_, err = client.FlowExecution.Create().
		SetID(participantWrongOp).SetRole(st.FlowExecutionRoleParticipant).
		SetOpType(int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_COOP_EXIT)).
		SetCoordinatorIndex(1).Save(ctx)
	require.NoError(t, err)

	unknown := uuid.New()

	cases := []struct {
		name                string
		id                  string
		expectedDisposition consensusOpDisposition
		expectedRow         bool // the fence only carries the row on the applyOp disposition
	}{
		{"participant row -> apply", participant.String(), applyOp, true},
		{"coordinator row -> skip echo", coordinator.String(), skipCoordinatorEcho, false},
		{"no row -> skip foreign", unknown.String(), skipForeignOp, false},
		{"empty id -> apply (pre-upgrade)", "", applyOp, false},
		{"participant row committed -> skip terminal", participantCommitted.String(), skipAlreadyTerminal, false},
		{"op type mismatch -> skip foreign", participantWrongOp.String(), skipForeignOp, false},
		{"participant row rolled back -> skip terminal", participantRolledBack.String(), skipAlreadyTerminal, false},
		// An unparseable flow id can never become valid on retry, so it is skipped
		// (never errored) — erroring would loop gossip redelivery forever.
		{"invalid flow id -> skip malformed", "not-a-uuid", skipMalformedFlowID, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			disposition, row, err := classifyConsensusOp(ctx, c.id, pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER)
			require.NoError(t, err)
			require.Equal(t, c.expectedDisposition, disposition)
			if c.expectedRow {
				require.NotNil(t, row, "the applyOp disposition must carry the FlowExecution row the fence binds against")
			} else {
				require.Nil(t, row, "non-apply dispositions must not carry a row")
			}
		})
	}
}

func TestHandleGossipMessage_ConsensusCommit_TransitionsParticipantRowToCommitted(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	row := insertParticipantRow(t, ctx, uuid.New())

	h := NewGossipHandler(sparktesting.TestConfig(t))
	err := h.HandleGossipMessage(ctx, consensusCommitMessage(t, row.ID.String()), false /* forCoordinator */)
	require.NoError(t, err)

	updated, err := sessionClient(t, ctx).FlowExecution.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusCommitted, updated.Status)
}

func TestHandleGossipMessage_ConsensusRollback_TransitionsParticipantRowToRolledBack(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	row := insertParticipantRow(t, ctx, uuid.New())

	h := NewGossipHandler(sparktesting.TestConfig(t))
	err := h.HandleGossipMessage(ctx, consensusRollbackMessage(t, row.ID.String()), false)
	require.NoError(t, err)

	updated, err := sessionClient(t, ctx).FlowExecution.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusRolledBack, updated.Status)
}

func TestHandleGossipMessage_ConsensusCommit_RedeliveredGossipIsIdempotent(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	row := insertParticipantRow(t, ctx, uuid.New())
	h := NewGossipHandler(sparktesting.TestConfig(t))

	// First delivery transitions to COMMITTED.
	require.NoError(t, h.HandleGossipMessage(ctx, consensusCommitMessage(t, row.ID.String()), false))
	// Redelivery is a no-op and must not return an error.
	require.NoError(t, h.HandleGossipMessage(ctx, consensusCommitMessage(t, row.ID.String()), false))

	updated, err := sessionClient(t, ctx).FlowExecution.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusCommitted, updated.Status)
}

func TestHandleGossipMessage_ConsensusCommit_MissingRowIsNoOp(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	h := NewGossipHandler(sparktesting.TestConfig(t))
	err := h.HandleGossipMessage(ctx, consensusCommitMessage(t, uuid.NewString()), false)
	require.NoError(t, err, "missing FlowExecution row should be tolerated (pre-upgrade rollout)")
}

func TestHandleGossipMessage_ConsensusCommit_EmptyExecutionIDIsNoOp(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	// Any existing row must remain untouched when the gossip carries no id.
	row := insertParticipantRow(t, ctx, uuid.New())

	h := NewGossipHandler(sparktesting.TestConfig(t))
	err := h.HandleGossipMessage(ctx, consensusCommitMessage(t, "" /* empty id */), false)
	require.NoError(t, err)

	unchanged, err := sessionClient(t, ctx).FlowExecution.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusInFlight, unchanged.Status)
}

func TestHandleGossipMessage_ConsensusCommit_AtCoordinatorIsSkippedAndRowUntouched(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)

	// Even if a row exists under the same id, the coordinator-side path
	// (forCoordinator=true) never transitions participant rows — the
	// coordinator already marked its COORDINATOR row terminal before sending.
	row := insertParticipantRow(t, ctx, uuid.New())

	h := NewGossipHandler(sparktesting.TestConfig(t))
	err := h.HandleGossipMessage(ctx, consensusCommitMessage(t, row.ID.String()), true /* forCoordinator */)
	require.NoError(t, err)

	unchanged, err := sessionClient(t, ctx).FlowExecution.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusInFlight, unchanged.Status)
}

// --- runConsensusCommit / runConsensusRollback: AlreadyExists-as-success rule ---

// stubFlowHandler is a consensus.FlowHandler whose Commit and Rollback
// return pre-set errors. Used to exercise the dispatch wrappers without
// pulling in real handler side effects.
type stubFlowHandler struct {
	commitErr   error
	rollbackErr error
}

func (s *stubFlowHandler) Prepare(_ context.Context, _ proto.Message) (proto.Message, error) {
	return nil, nil
}
func (s *stubFlowHandler) Commit(_ context.Context, _ proto.Message) error   { return s.commitErr }
func (s *stubFlowHandler) Rollback(_ context.Context, _ proto.Message) error { return s.rollbackErr }

func TestRunConsensusCommit_AlreadyExists_MarksRowCommitted(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	row := insertParticipantRow(t, ctx, uuid.New())

	staleErr := sparkerrors.AlreadyExistsDuplicateOperation(errors.New("stale finalize"))
	h := &stubFlowHandler{commitErr: staleErr}

	err := runConsensusCommit(ctx, h,
		pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE,
		row.ID.String(),
		&pbinternal.StorePreimageSharePrepareRequest{},
	)
	require.NoError(t, err, "AlreadyExists from handler.Commit must be treated as success")

	updated, err := sessionClient(t, ctx).FlowExecution.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusCommitted, updated.Status,
		"row must transition to COMMITTED when the handler reports AlreadyExists")
}

func TestRunConsensusCommit_NonAlreadyExistsError_PropagatesAndLeavesRowInFlight(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	row := insertParticipantRow(t, ctx, uuid.New())

	internalErr := sparkerrors.InternalDatabaseWriteError(errors.New("disk full"))
	h := &stubFlowHandler{commitErr: internalErr}

	err := runConsensusCommit(ctx, h,
		pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE,
		row.ID.String(),
		&pbinternal.StorePreimageSharePrepareRequest{},
	)
	require.Error(t, err, "non-AlreadyExists handler errors must propagate")

	unchanged, err := sessionClient(t, ctx).FlowExecution.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusInFlight, unchanged.Status,
		"row must stay IN_FLIGHT when the handler returns a non-AlreadyExists error")
}

func TestRunConsensusRollback_AlreadyExists_MarksRowRolledBack(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	row := insertParticipantRow(t, ctx, uuid.New())

	staleErr := sparkerrors.AlreadyExistsDuplicateOperation(errors.New("already rolled back"))
	h := &stubFlowHandler{rollbackErr: staleErr}

	err := runConsensusRollback(ctx, h,
		pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STORE_PREIMAGE_SHARE,
		row.ID.String(),
		&pbinternal.StorePreimageSharePrepareRequest{},
	)
	require.NoError(t, err, "AlreadyExists from handler.Rollback must be treated as success")

	updated, err := sessionClient(t, ctx).FlowExecution.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, st.FlowExecutionStatusRolledBack, updated.Status,
		"row must transition to ROLLED_BACK when the handler reports AlreadyExists")
}

// TestHandleGossipMessage_ConsensusRollback_RedeliveredAfterTerminal_DoesNotCancelNewerSwap
// pins the terminal-row fence against the concrete hazard it exists for: the
// static-deposit refund rollback payload is keyed by on_chain_utxo, which does
// not discriminate between attempts. Once attempt A's swap is CANCELLED (freeing
// the (utxo, status != CANCELLED) unique slot) and a retry B creates a fresh
// swap for the same UTXO, a redelivered rollback from A must NOT reach the
// handler — it would find, and cancel, B's active swap.
func TestHandleGossipMessage_ConsensusRollback_RedeliveredAfterTerminal_DoesNotCancelNewerSwap(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	client := sessionClient(t, ctx)

	// Attempt A: its swap is already CANCELLED and its participant FlowExecution
	// row is terminal — the state after A's rollback was first delivered.
	cancelledSwap, utxo := createTestRefundUtxoSwap(t, ctx, st.UtxoSwapStatusCancelled)
	flowA := uuid.New()
	_, err := client.FlowExecution.Create().
		SetID(flowA).SetRole(st.FlowExecutionRoleParticipant).
		SetOpType(int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STATIC_DEPOSIT_UTXO_REFUND)).
		SetCoordinatorIndex(1).
		SetStatus(st.FlowExecutionStatusRolledBack).Save(ctx)
	require.NoError(t, err)

	// Retry attempt B: a fresh CREATED swap for the same UTXO.
	rng := rand.NewChaCha8([32]byte{3})
	identityKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	newerSwap, err := client.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		SetUtxo(utxo).
		SetUtxoValueSats(utxo.Amount).
		SetRequestType(st.UtxoSwapRequestTypeRefund).
		SetCreditAmountSats(utxo.Amount).
		SetSspSignature([]byte("test_spend_tx_sighash")).
		SetSspIdentityPublicKey(identityKey).
		SetUserIdentityPublicKey(identityKey).
		SetCoordinatorIdentityPublicKey(identityKey).
		Save(ctx)
	require.NoError(t, err)

	// Redeliver attempt A's rollback gossip.
	opAny, err := anypb.New(&pbinternal.StaticDepositUtxoRefundRollbackRequest{
		OnChainUtxo: &pb.UTXO{Txid: utxo.Txid, Vout: utxo.Vout, Network: pb.Network_REGTEST},
	})
	require.NoError(t, err)
	h := NewGossipHandler(sparktesting.TestConfig(t))
	require.NoError(t, h.HandleGossipMessage(ctx, &pbgossip.GossipMessage{
		MessageId: uuid.NewString(),
		Message: &pbgossip.GossipMessage_ConsensusRollback{
			ConsensusRollback: &pbgossip.GossipMessageConsensusRollback{
				OpType:          pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_STATIC_DEPOSIT_UTXO_REFUND,
				Operation:       opAny,
				FlowExecutionId: flowA.String(),
			},
		},
	}, false))

	survivor, err := client.UtxoSwap.Get(ctx, newerSwap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCreated, survivor.Status,
		"retry attempt's active swap must survive a redelivered rollback from the earlier attempt")
	unchanged, err := client.UtxoSwap.Get(ctx, cancelledSwap.ID)
	require.NoError(t, err)
	assert.Equal(t, st.UtxoSwapStatusCancelled, unchanged.Status)
}

func TestConsensusRollbackFenceThroughGossip(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{92})
	gossipHandler := NewGossipHandler(setUpTestConfigWithRegtestNoAuthz(t))
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	opType := int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER)

	newPreparedTransfer := func(t *testing.T, owner uuid.UUID) *ent.Transfer {
		t.Helper()
		transfer := createTestTransfer(t, ctx, rng, client, st.TransferStatusSenderKeyTweakPending)
		// These tests dispatch via prepareBoundFakeFlowHandler (a bound handler),
		// so the participant row must carry a prepare payload the decision is bound
		// against (as DispatchPrepare persists for a bound flow). The SEND_TRANSFER
		// prepare shape is used as a realistic fixture; the real SendTransferFlowHandler
		// implements the interface in a later PR in the other-flows stack.
		prepareAny, err := anypb.New(&pbinternal.SendTransferPrepareRequest{
			OriginalRequest: &pb.StartTransferV3Request{TransferId: transfer.ID.String()},
		})
		require.NoError(t, err)
		prepareBytes, err := proto.Marshal(prepareAny)
		require.NoError(t, err)
		_, err = client.FlowExecution.Create().
			SetID(owner).SetRole(st.FlowExecutionRoleParticipant).
			SetOpType(opType).SetCoordinatorIndex(1).SetPreparePayload(prepareBytes).Save(ctx)
		require.NoError(t, err)
		return transfer
	}
	rollbackGossip := func(t *testing.T, transferID uuid.UUID, flowID string) *pbgossip.GossipMessage {
		t.Helper()
		op, err := anypb.New(&pbinternal.SendTransferRollbackRequest{TransferId: transferID.String()})
		require.NoError(t, err)
		return &pbgossip.GossipMessage{
			Message: &pbgossip.GossipMessage_ConsensusRollback{
				ConsensusRollback: &pbgossip.GossipMessageConsensusRollback{
					OpType:          pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER,
					Operation:       op,
					FlowExecutionId: flowID,
				},
			},
		}
	}
	commitGossip := func(t *testing.T, transferID uuid.UUID, flowID string) *pbgossip.GossipMessage {
		t.Helper()
		op, err := anypb.New(&pbinternal.SendTransferCommitRequest{TransferId: transferID.String()})
		require.NoError(t, err)
		return &pbgossip.GossipMessage{
			Message: &pbgossip.GossipMessage_ConsensusCommit{
				ConsensusCommit: &pbgossip.GossipMessageConsensusCommit{
					OpType:          pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER,
					Operation:       op,
					FlowExecutionId: flowID,
				},
			},
		}
	}
	statusOf := func(t *testing.T, id uuid.UUID) st.TransferStatus {
		t.Helper()
		updated, err := client.Transfer.Get(ctx, id)
		require.NoError(t, err)
		return updated.Status
	}

	t.Run("foreign flow rollback is a no-op", func(t *testing.T) {
		owner := uuid.New()
		transfer := newPreparedTransfer(t, owner)
		foreign := uuid.New().String()
		require.NoError(t, gossipHandler.HandleGossipMessage(ctx, rollbackGossip(t, transfer.ID, foreign), false))
		require.Equal(t, st.TransferStatusSenderKeyTweakPending, statusOf(t, transfer.ID))
	})

	t.Run("owning flow rollback proceeds", func(t *testing.T) {
		owner := uuid.New()
		transfer := newPreparedTransfer(t, owner)
		require.NoError(t, gossipHandler.HandleGossipMessage(ctx, rollbackGossip(t, transfer.ID, owner.String()), false))
		require.Equal(t, st.TransferStatusReturned, statusOf(t, transfer.ID))
	})

	t.Run("foreign flow commit is a no-op", func(t *testing.T) {
		owner := uuid.New()
		transfer := newPreparedTransfer(t, owner)
		require.NoError(t, gossipHandler.HandleGossipMessage(ctx, commitGossip(t, transfer.ID, uuid.New().String()), false))
		require.Equal(t, st.TransferStatusSenderKeyTweakPending, statusOf(t, transfer.ID))
	})

	t.Run("owning flow commit proceeds", func(t *testing.T) {
		owner := uuid.New()
		transfer := newPreparedTransfer(t, owner)
		require.NoError(t, gossipHandler.HandleGossipMessage(ctx, commitGossip(t, transfer.ID, owner.String()), false))
		require.Equal(t, st.TransferStatusSenderKeyTweaked, statusOf(t, transfer.ID))
	})

	t.Run("coordinator-role row commit is skipped (self-echo)", func(t *testing.T) {
		coordinator := uuid.New()
		transfer := createTestTransfer(t, ctx, rng, client, st.TransferStatusSenderKeyTweakPending)
		_, err := client.FlowExecution.Create().
			SetID(coordinator).SetRole(st.FlowExecutionRoleCoordinator).
			SetOpType(opType).SetCoordinatorIndex(0).Save(ctx)
		require.NoError(t, err)
		require.NoError(t, gossipHandler.HandleGossipMessage(ctx, commitGossip(t, transfer.ID, coordinator.String()), false))
		require.Equal(t, st.TransferStatusSenderKeyTweakPending, statusOf(t, transfer.ID))
	})
}

// TestConsensusFenceMetricDispositions pins the metric labels for the three
// dispositions this PR's PrepareBoundFlowHandler fence introduces
// (missing_flow_execution_id, missing_prepare_payload, payload_mismatch), so
// label drift is caught by a test rather than going unnoticed.
func TestConsensusFenceMetricDispositions(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	reader := msdk.NewManualReader()
	prevProvider := otel.GetMeterProvider()
	testProvider := msdk.NewMeterProvider(msdk.WithReader(reader))
	otel.SetMeterProvider(testProvider)
	prev := consensusOpFencedTotal
	consensusOpFencedTotal = newConsensusOpFencedCounter()
	t.Cleanup(func() {
		consensusOpFencedTotal = prev
		otel.SetMeterProvider(prevProvider)
		// Deliberately not t.Context(): it is cancelled before t.Cleanup runs, so
		// Shutdown needs a live context.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second) //nolint:usetesting // see comment above
		defer cancel()
		require.NoError(t, testProvider.Shutdown(shutdownCtx))
	})

	opType := pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER
	handler := &prepareBoundFakeFlowHandler{}
	rollbackOp := &pbinternal.SendTransferRollbackRequest{TransferId: uuid.NewString()}

	// missing_flow_execution_id: bound handler, empty id → row is nil.
	require.NoError(t, runConsensusRollback(ctx, handler, opType, "", rollbackOp))

	// missing_prepare_payload: bound handler, row exists but has no payload.
	noPayloadID := uuid.New()
	_, err = client.FlowExecution.Create().
		SetID(noPayloadID).SetRole(st.FlowExecutionRoleParticipant).
		SetOpType(int32(opType)).SetCoordinatorIndex(1).Save(ctx)
	require.NoError(t, err)
	require.NoError(t, runConsensusRollback(ctx, handler, opType, noPayloadID.String(), rollbackOp))

	// payload_mismatch: bound handler, row with a payload for a different transfer.
	mismatchID := seedFenceParticipantRow(t, ctx, uuid.NewString())
	require.NoError(t, runConsensusRollback(ctx, handler, opType, mismatchID.String(), rollbackOp))

	// Same fence on the COMMIT phase (phase="commit"): the label path added in
	// runConsensusCommit must also emit.
	commitMismatchID := seedFenceParticipantRow(t, ctx, uuid.NewString())
	require.NoError(t, runConsensusCommit(ctx, handler, opType, commitMismatchID.String(),
		&pbinternal.SendTransferCommitRequest{TransferId: uuid.NewString()}))

	require.Equal(t, 0, handler.rollbackCalls, "all rollback-phase dispositions must fence before the handler")
	require.Equal(t, 0, handler.commitCalls, "the commit-phase mismatch must fence before the handler")

	var rm md.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &rm))
	byDisposition := map[string]int64{}
	byPhase := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "gossip.consensus_op_fenced_total" {
				continue
			}
			sum, ok := m.Data.(md.Sum[int64])
			require.True(t, ok, "expected Sum[int64] for %s", m.Name)
			for _, dp := range sum.DataPoints {
				d, _ := dp.Attributes.Value(attribute.Key("disposition"))
				byDisposition[d.AsString()] += dp.Value
				p, _ := dp.Attributes.Value(attribute.Key("phase"))
				byPhase[p.AsString()] += dp.Value
			}
		}
	}
	require.Equal(t, int64(1), byDisposition["missing_flow_execution_id"])
	require.Equal(t, int64(1), byDisposition["missing_prepare_payload"])
	require.Equal(t, int64(2), byDisposition["payload_mismatch"], "one rollback + one commit")
	require.GreaterOrEqual(t, byPhase["rollback"], int64(1), "rollback-phase label emitted")
	require.GreaterOrEqual(t, byPhase["commit"], int64(1), "commit-phase label emitted")
}

// TestConsensusDecisionFence_CorruptedPreparePayloadSkips pins the fail-closed
// behavior when a persisted prepare payload can't be unmarshalled (a corrupt
// row). It's unreachable in normal operation — DispatchPrepare always persists
// a valid Any — but an undecodable row is unrecoverable by retry, so the fence
// must SKIP (leave the row IN_FLIGHT, no dispatch, corrupt_prepare_payload
// metric) rather than return an error that would loop gossip redelivery forever.
func TestConsensusDecisionFence_CorruptedPreparePayloadSkips(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	reader := msdk.NewManualReader()
	prevProvider := otel.GetMeterProvider()
	testProvider := msdk.NewMeterProvider(msdk.WithReader(reader))
	otel.SetMeterProvider(testProvider)
	prev := consensusOpFencedTotal
	consensusOpFencedTotal = newConsensusOpFencedCounter()
	t.Cleanup(func() {
		consensusOpFencedTotal = prev
		otel.SetMeterProvider(prevProvider)
		// Deliberately not t.Context(): it is cancelled before t.Cleanup runs, so
		// Shutdown needs a live context.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second) //nolint:usetesting // see comment above
		defer cancel()
		require.NoError(t, testProvider.Shutdown(shutdownCtx))
	})

	flowID := uuid.New()
	_, err = client.FlowExecution.Create().
		SetID(flowID).SetRole(st.FlowExecutionRoleParticipant).
		SetOpType(int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER)).
		SetCoordinatorIndex(1).
		SetPreparePayload([]byte{0xff, 0xff, 0xff}). // invalid protobuf wire bytes
		Save(ctx)
	require.NoError(t, err)

	handler := &prepareBoundFakeFlowHandler{}
	err = runConsensusRollback(ctx, handler,
		pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER,
		flowID.String(),
		&pbinternal.SendTransferRollbackRequest{TransferId: uuid.NewString()})
	// Skip, not error: an undecodable row can never become valid via retry, so
	// erroring would loop gossip redelivery indefinitely.
	require.NoError(t, err)
	require.Equal(t, 0, handler.rollbackCalls, "a corrupt prepare payload must fail closed, not dispatch")
	// The row must stay IN_FLIGHT — the fence must not transition it (e.g. to
	// ROLLED_BACK) before returning, so the real decision can still land.
	assertFenceRowStatus(t, ctx, flowID, st.FlowExecutionStatusInFlight)

	var rm md.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &rm))
	var corruptCount int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "gossip.consensus_op_fenced_total" {
				continue
			}
			sum, ok := m.Data.(md.Sum[int64])
			require.True(t, ok, "expected Sum[int64] for %s", m.Name)
			for _, dp := range sum.DataPoints {
				d, _ := dp.Attributes.Value(attribute.Key("disposition"))
				if d.AsString() == "corrupt_prepare_payload" {
					corruptCount += dp.Value
				}
			}
		}
	}
	require.Equal(t, int64(1), corruptCount, "corrupt payload must increment the corrupt_prepare_payload disposition")
}

// TestConsensusDecisionFence_UndecodableAnyTypeURLSkips covers the SECOND
// corrupt_prepare_payload branch: a payload that IS a valid anypb.Any envelope
// but whose type_url does not resolve to a registered message, so
// prepareAny.UnmarshalNew() (not proto.Unmarshal) fails. It must skip with the
// same disposition as raw wire corruption, not error or dispatch.
func TestConsensusDecisionFence_UndecodableAnyTypeURLSkips(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	reader := msdk.NewManualReader()
	prevProvider := otel.GetMeterProvider()
	testProvider := msdk.NewMeterProvider(msdk.WithReader(reader))
	otel.SetMeterProvider(testProvider)
	prev := consensusOpFencedTotal
	consensusOpFencedTotal = newConsensusOpFencedCounter()
	t.Cleanup(func() {
		consensusOpFencedTotal = prev
		otel.SetMeterProvider(prevProvider)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second) //nolint:usetesting // cancelled t.Context() before cleanup
		defer cancel()
		require.NoError(t, testProvider.Shutdown(shutdownCtx))
	})

	// A well-formed Any whose type_url names no registered message.
	bogusAny := &anypb.Any{TypeUrl: "type.googleapis.com/spark.internal.NotARealMessage", Value: []byte{0x08, 0x01}}
	bogusBytes, err := proto.Marshal(bogusAny)
	require.NoError(t, err)

	flowID := uuid.New()
	_, err = client.FlowExecution.Create().
		SetID(flowID).SetRole(st.FlowExecutionRoleParticipant).
		SetOpType(int32(pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER)).
		SetCoordinatorIndex(1).
		SetPreparePayload(bogusBytes).
		Save(ctx)
	require.NoError(t, err)

	handler := &prepareBoundFakeFlowHandler{}
	err = runConsensusRollback(ctx, handler,
		pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER,
		flowID.String(),
		&pbinternal.SendTransferRollbackRequest{TransferId: uuid.NewString()})
	require.NoError(t, err)
	require.Equal(t, 0, handler.rollbackCalls, "an unresolvable Any type_url must fail closed, not dispatch")
	assertFenceRowStatus(t, ctx, flowID, st.FlowExecutionStatusInFlight)

	var rm md.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &rm))
	var corruptCount int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "gossip.consensus_op_fenced_total" {
				continue
			}
			sum, ok := m.Data.(md.Sum[int64])
			require.True(t, ok, "expected Sum[int64] for %s", m.Name)
			for _, dp := range sum.DataPoints {
				d, _ := dp.Attributes.Value(attribute.Key("disposition"))
				if d.AsString() == "corrupt_prepare_payload" {
					corruptCount += dp.Value
				}
			}
		}
	}
	require.Equal(t, int64(1), corruptCount, "unresolvable Any type_url must increment corrupt_prepare_payload")
}

func TestConsensusFenceMetricIncrements(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	gossipHandler := NewGossipHandler(sparktesting.TestConfig(t))

	// The fence counter binds to the global meter provider in init(); rebind it to a
	// manual reader so the skip-foreign increment is observable.
	reader := msdk.NewManualReader()
	prevProvider := otel.GetMeterProvider()
	testProvider := msdk.NewMeterProvider(msdk.WithReader(reader))
	otel.SetMeterProvider(testProvider)
	prev := consensusOpFencedTotal
	consensusOpFencedTotal = newConsensusOpFencedCounter()
	t.Cleanup(func() {
		consensusOpFencedTotal = prev
		otel.SetMeterProvider(prevProvider)
		// Deliberately not t.Context(): it is cancelled before t.Cleanup runs, so
		// Shutdown needs a live context.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second) //nolint:usetesting // see comment above
		defer cancel()
		require.NoError(t, testProvider.Shutdown(shutdownCtx))
	})

	// A foreign flow id has no FlowExecution row, so both ops are fenced (skip-foreign).
	transferID := uuid.New().String()
	commitOp, err := anypb.New(&pbinternal.SendTransferCommitRequest{TransferId: transferID})
	require.NoError(t, err)
	rollbackOp, err := anypb.New(&pbinternal.SendTransferRollbackRequest{TransferId: transferID})
	require.NoError(t, err)
	require.NoError(t, gossipHandler.HandleGossipMessage(ctx, &pbgossip.GossipMessage{
		Message: &pbgossip.GossipMessage_ConsensusCommit{ConsensusCommit: &pbgossip.GossipMessageConsensusCommit{
			OpType:          pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER,
			Operation:       commitOp,
			FlowExecutionId: uuid.New().String(),
		}},
	}, false))
	require.NoError(t, gossipHandler.HandleGossipMessage(ctx, &pbgossip.GossipMessage{
		Message: &pbgossip.GossipMessage_ConsensusRollback{ConsensusRollback: &pbgossip.GossipMessageConsensusRollback{
			OpType:          pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_SEND_TRANSFER,
			Operation:       rollbackOp,
			FlowExecutionId: uuid.New().String(),
		}},
	}, false))

	var rm md.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &rm))
	byPhase := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "gossip.consensus_op_fenced_total" {
				continue
			}
			sum, ok := m.Data.(md.Sum[int64])
			require.True(t, ok, "expected Sum[int64] for %s", m.Name)
			for _, dp := range sum.DataPoints {
				phase, _ := dp.Attributes.Value(attribute.Key("phase"))
				byPhase[phase.AsString()] += dp.Value
			}
		}
	}
	require.Equal(t, int64(1), byPhase["commit"], "commit-phase fence count")
	require.Equal(t, int64(1), byPhase["rollback"], "rollback-phase fence count")
}
