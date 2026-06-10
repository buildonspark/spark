package handler

import (
	"context"
	"crypto/sha256"
	"errors"
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
