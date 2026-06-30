//go:build lightspark

package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	sparkProto "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransferleaf "github.com/lightsparkdev/spark/so/ent/transferleaf"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// TestSettleSenderKeyTweak_Commit_EmptyKeyTweak verifies that committing
// a transfer via SettleSenderKeyTweak fails when a TransferLeaf has no
// key_tweak stored, rather than allowing proto.Unmarshal on empty bytes.
//
// This tests defense-in-depth: the empty key_tweak state shouldn't be
// reachable through normal operation, but if it is (due to a bug or race),
// the system should fail fast with a clear error.
func TestSettleSenderKeyTweak_Commit_EmptyKeyTweak(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{44})

	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

	client := dbCtx.Client

	keysharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	publicSharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	signingKeyshare, err := client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(keysharePrivKey).
		SetPublicShares(map[string]keys.Public{"test": publicSharePrivKey.Public()}).
		SetPublicKey(keysharePrivKey.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	tree, err := client.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Regtest).
		SetOwnerIdentityPubkey(senderPrivKey.Public()).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	verifyingPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerSigningPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	leaf, err := client.TreeNode.Create().
		SetStatus(st.TreeNodeStatusAvailable).
		SetTree(tree).
		SetNetwork(tree.Network).
		SetSigningKeyshare(signingKeyshare).
		SetValue(1000).
		SetVerifyingPubkey(verifyingPrivKey.Public()).
		SetOwnerIdentityPubkey(senderPrivKey.Public()).
		SetOwnerSigningPubkey(ownerSigningPrivKey.Public()).
		SetRawTx(createTestTxBytes(t, 3000)).
		SetRawRefundTx(createTestTxBytes(t, 3100)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	// Create transfer in SenderKeyTweakPending status — the state that
	// commitSenderKeyTweaks expects to find.
	transfer, err := client.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TransferStatusSenderKeyTweakPending).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(senderPrivKey.Public()).
		SetReceiverIdentityPubkey(receiverPrivKey.Public()).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	// Create TransferLeaf with NO key_tweak set (simulates the inconsistent
	// state that this defense-in-depth check guards against).
	_, err = client.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(leaf).
		SetPreviousRefundTx(createTestTxBytes(t, 4000)).
		SetIntermediateRefundTx(createTestTxBytes(t, 4001)).
		Save(ctx)
	require.NoError(t, err)

	// Call through the public gRPC handler entry point.
	handler := NewInternalTransferHandler(cfg)
	err = handler.SettleSenderKeyTweak(ctx, &pbinternal.SettleSenderKeyTweakRequest{
		TransferId: transfer.ID.String(),
		Action:     pbinternal.SettleKeyTweakAction_COMMIT,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "transfer leaf has no key tweak stored")
	assert.Contains(t, err.Error(), leaf.ID.String())
}

func TestSettleSenderKeyTweak_Commit_PreimageSwapRequiresSharedPreimage(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{121})

	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	keysharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	publicSharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	verifyingPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerSigningPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	client := dbCtx.Client

	signingKeyshare, err := client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(keysharePrivKey).
		SetPublicShares(map[string]keys.Public{cfg.Identifier: publicSharePrivKey.Public()}).
		SetPublicKey(keysharePrivKey.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	tree, err := client.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Regtest).
		SetOwnerIdentityPubkey(senderPrivKey.Public()).
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
		SetVerifyingPubkey(verifyingPrivKey.Public()).
		SetOwnerIdentityPubkey(senderPrivKey.Public()).
		SetOwnerSigningPubkey(ownerSigningPrivKey.Public()).
		SetRawTx(createTestTxBytes(t, 5000)).
		SetRawRefundTx(createTestTxBytes(t, 5100)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	transfer, err := client.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TransferStatusSenderKeyTweakPending).
		SetType(st.TransferTypePreimageSwap).
		SetSenderIdentityPubkey(senderPrivKey.Public()).
		SetReceiverIdentityPubkey(receiverPrivKey.Public()).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	secretShare, pubkeySharesTweak := createValidSecretShares(cfg, rng)
	keyTweakBytes, err := proto.Marshal(&sparkProto.SendLeafKeyTweak{
		LeafId:            leaf.ID.String(),
		SecretShareTweak:  secretShare,
		PubkeySharesTweak: pubkeySharesTweak,
		SecretCipher:      []byte("encrypted-secret-share"),
		Sig:               &sparkProto.SendLeafKeyTweak_Signature{Signature: []byte("mock-key-tweak-signature")},
	})
	require.NoError(t, err)

	transferLeaf, err := client.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(leaf).
		SetPreviousRefundTx(createTestTxBytes(t, 5200)).
		SetIntermediateRefundTx(createTestTxBytes(t, 5201)).
		SetKeyTweak(keyTweakBytes).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.PreimageRequest.Create().
		SetPaymentHash([]byte("waiting_preimage_request_hash__32")).
		SetStatus(st.PreimageRequestStatusWaitingForPreimage).
		SetReceiverIdentityPubkey(receiverPrivKey.Public()).
		SetTransfers(transfer).
		Save(ctx)
	require.NoError(t, err)

	handler := NewInternalTransferHandler(cfg)
	err = handler.SettleSenderKeyTweak(ctx, &pbinternal.SettleSenderKeyTweakRequest{
		TransferId: transfer.ID.String(),
		Action:     pbinternal.SettleKeyTweakAction_COMMIT,
	})

	require.ErrorContains(t, err, "preimage has not been shared")
	updatedTransfer, err := client.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusSenderKeyTweakPending, updatedTransfer.Status)
	updatedTransferLeaf, err := client.TransferLeaf.Get(ctx, transferLeaf.ID)
	require.NoError(t, err)
	assert.NotEmpty(t, updatedTransferLeaf.KeyTweak, "rejected commit must leave sender key tweak material pending")
}

func TestSettleSenderKeyTweak_Commit_PreimageSwapRequiresStoredMatchingPreimage(t *testing.T) {
	testCases := []struct {
		name              string
		preimage          []byte
		mismatchHash      bool
		expectedSubstring string
	}{
		{
			name:              "missing preimage",
			expectedSubstring: "does not have a stored 32-byte preimage",
		},
		{
			name:              "short preimage",
			preimage:          []byte{0x01},
			expectedSubstring: "does not have a stored 32-byte preimage",
		},
		{
			name:              "wrong payment hash",
			preimage:          append([]byte{0x01}, make([]byte, sha256.Size-1)...),
			mismatchHash:      true,
			expectedSubstring: "stored preimage does not match payment hash",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, dbCtx := db.ConnectToTestPostgres(t)
			cfg := sparktesting.TestConfig(t)
			rng := rand.NewChaCha8([32]byte{123})

			senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
			receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
			keysharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
			publicSharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
			verifyingPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
			ownerSigningPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
			client := dbCtx.Client

			signingKeyshare, err := client.SigningKeyshare.Create().
				SetStatus(st.KeyshareStatusAvailable).
				SetSecretShare(keysharePrivKey).
				SetPublicShares(map[string]keys.Public{cfg.Identifier: publicSharePrivKey.Public()}).
				SetPublicKey(keysharePrivKey.Public()).
				SetMinSigners(2).
				SetCoordinatorIndex(0).
				Save(ctx)
			require.NoError(t, err)

			tree, err := client.Tree.Create().
				SetStatus(st.TreeStatusAvailable).
				SetNetwork(btcnetwork.Regtest).
				SetOwnerIdentityPubkey(senderPrivKey.Public()).
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
				SetVerifyingPubkey(verifyingPrivKey.Public()).
				SetOwnerIdentityPubkey(senderPrivKey.Public()).
				SetOwnerSigningPubkey(ownerSigningPrivKey.Public()).
				SetRawTx(createTestTxBytes(t, 5300)).
				SetRawRefundTx(createTestTxBytes(t, 5301)).
				SetVout(0).
				Save(ctx)
			require.NoError(t, err)

			transfer, err := client.Transfer.Create().
				SetNetwork(btcnetwork.Regtest).
				SetStatus(st.TransferStatusSenderKeyTweakPending).
				SetType(st.TransferTypePreimageSwap).
				SetSenderIdentityPubkey(senderPrivKey.Public()).
				SetReceiverIdentityPubkey(receiverPrivKey.Public()).
				SetTotalValue(1000).
				SetExpiryTime(time.Now().Add(24 * time.Hour)).
				Save(ctx)
			require.NoError(t, err)

			secretShare, pubkeySharesTweak := createValidSecretShares(cfg, rng)
			keyTweakBytes, err := proto.Marshal(&sparkProto.SendLeafKeyTweak{
				LeafId:            leaf.ID.String(),
				SecretShareTweak:  secretShare,
				PubkeySharesTweak: pubkeySharesTweak,
				SecretCipher:      []byte("encrypted-secret-share"),
				Sig:               &sparkProto.SendLeafKeyTweak_Signature{Signature: []byte("mock-key-tweak-signature")},
			})
			require.NoError(t, err)

			transferLeaf, err := client.TransferLeaf.Create().
				SetTransfer(transfer).
				SetLeaf(leaf).
				SetPreviousRefundTx(createTestTxBytes(t, 5400)).
				SetIntermediateRefundTx(createTestTxBytes(t, 5401)).
				SetKeyTweak(keyTweakBytes).
				Save(ctx)
			require.NoError(t, err)

			paymentHash := make([]byte, sha256.Size)
			if len(tc.preimage) > 0 {
				hash := sha256.Sum256(tc.preimage)
				copy(paymentHash, hash[:])
			}
			if tc.mismatchHash {
				paymentHash[0] ^= 0xff
			}
			preimageCreate := client.PreimageRequest.Create().
				SetPaymentHash(paymentHash).
				SetStatus(st.PreimageRequestStatusPreimageShared).
				SetReceiverIdentityPubkey(receiverPrivKey.Public()).
				SetTransfers(transfer)
			if tc.preimage != nil {
				preimageCreate.SetPreimage(tc.preimage)
			}
			_, err = preimageCreate.Save(ctx)
			require.NoError(t, err)

			handler := NewInternalTransferHandler(cfg)
			err = handler.SettleSenderKeyTweak(ctx, &pbinternal.SettleSenderKeyTweakRequest{
				TransferId: transfer.ID.String(),
				Action:     pbinternal.SettleKeyTweakAction_COMMIT,
			})

			require.ErrorContains(t, err, tc.expectedSubstring)
			updatedTransfer, err := client.Transfer.Get(ctx, transfer.ID)
			require.NoError(t, err)
			assert.Equal(t, st.TransferStatusSenderKeyTweakPending, updatedTransfer.Status)
			updatedTransferLeaf, err := client.TransferLeaf.Get(ctx, transferLeaf.ID)
			require.NoError(t, err)
			assert.NotEmpty(t, updatedTransferLeaf.KeyTweak, "rejected commit must leave sender key tweak material pending")
		})
	}
}

func TestSettleSenderKeyTweak_Commit_SwapV3RequiresAtomicSwapCommit(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{124})

	primary, counter := createSwapV3PendingSenderKeyTweakTransfersForTest(t, ctx, dbCtx.Client, cfg, rng)
	handler := NewInternalTransferHandler(cfg)

	err := handler.SettleSenderKeyTweak(ctx, &pbinternal.SettleSenderKeyTweakRequest{
		TransferId: primary.ID.String(),
		Action:     pbinternal.SettleKeyTweakAction_COMMIT,
	})
	require.ErrorContains(t, err, "swap v3 sender key tweaks must be committed atomically")
	assertTransferStillPendingSenderKeyTweak(t, ctx, dbCtx.Client, primary.ID)
	assertTransferStillPendingSenderKeyTweak(t, ctx, dbCtx.Client, counter.ID)

	err = handler.SettleSenderKeyTweak(ctx, &pbinternal.SettleSenderKeyTweakRequest{
		TransferId: counter.ID.String(),
		Action:     pbinternal.SettleKeyTweakAction_COMMIT,
	})
	require.ErrorContains(t, err, "swap v3 sender key tweaks must be committed atomically")
	assertTransferStillPendingSenderKeyTweak(t, ctx, dbCtx.Client, primary.ID)
	assertTransferStillPendingSenderKeyTweak(t, ctx, dbCtx.Client, counter.ID)

	baseHandler := NewBaseTransferHandler(cfg)
	require.NoError(t, baseHandler.CommitSwapKeyTweaks(ctx, counter.ID))
	assertTransferCommittedSenderKeyTweak(t, ctx, dbCtx.Client, primary.ID)
	assertTransferCommittedSenderKeyTweak(t, ctx, dbCtx.Client, counter.ID)

	// Duplicate gossip delivery should be idempotent once both legs are committed.
	require.NoError(t, baseHandler.CommitSwapKeyTweaks(ctx, counter.ID))
	assertTransferCommittedSenderKeyTweak(t, ctx, dbCtx.Client, primary.ID)
	assertTransferCommittedSenderKeyTweak(t, ctx, dbCtx.Client, counter.ID)
}

func TestCommitSwapKeyTweaksRejectsTerminalSideWithoutPartialCommit(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{125})
	baseHandler := NewBaseTransferHandler(cfg)

	primary, counter := createSwapV3PendingSenderKeyTweakTransfersForTest(t, ctx, dbCtx.Client, cfg, rng)
	_, err := counter.Update().SetStatus(st.TransferStatusExpired).Save(ctx)
	require.NoError(t, err)

	err = baseHandler.CommitSwapKeyTweaks(ctx, counter.ID)
	require.ErrorContains(t, err, "counter transfer")
	assertTransferStillPendingSenderKeyTweak(t, ctx, dbCtx.Client, primary.ID)
	assertSenderKeyTweakTransferStateForTest(t, ctx, dbCtx.Client, counter.ID, st.TransferStatusExpired, true, false)

	primary, counter = createSwapV3PendingSenderKeyTweakTransfersForTest(t, ctx, dbCtx.Client, cfg, rng)
	_, err = primary.Update().SetStatus(st.TransferStatusExpired).Save(ctx)
	require.NoError(t, err)

	err = baseHandler.CommitSwapKeyTweaks(ctx, counter.ID)
	require.ErrorContains(t, err, "primary transfer")
	assertSenderKeyTweakTransferStateForTest(t, ctx, dbCtx.Client, primary.ID, st.TransferStatusExpired, true, false)
	assertTransferStillPendingSenderKeyTweak(t, ctx, dbCtx.Client, counter.ID)
}

func TestCommitSenderKeyTweaks_RejectsNilProofValue(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{122})

	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	keysharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	publicSharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	client := dbCtx.Client

	signingKeyshare, err := client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(keysharePrivKey).
		SetPublicShares(map[string]keys.Public{cfg.Identifier: publicSharePrivKey.Public()}).
		SetPublicKey(keysharePrivKey.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	tree, err := client.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Regtest).
		SetOwnerIdentityPubkey(senderPrivKey.Public()).
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
		SetVerifyingPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetOwnerIdentityPubkey(senderPrivKey.Public()).
		SetOwnerSigningPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetRawTx(createTestTxBytes(t, 5300)).
		SetRawRefundTx(createTestTxBytes(t, 5301)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	transfer, err := client.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TransferStatusSenderKeyTweakPending).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(senderPrivKey.Public()).
		SetReceiverIdentityPubkey(receiverPrivKey.Public()).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	secretShare, pubkeySharesTweak := createValidSecretShares(cfg, rng)
	keyTweakBytes, err := proto.Marshal(&sparkProto.SendLeafKeyTweak{
		LeafId:            leaf.ID.String(),
		SecretShareTweak:  secretShare,
		PubkeySharesTweak: pubkeySharesTweak,
		SecretCipher:      []byte("encrypted-secret-share"),
		Sig:               &sparkProto.SendLeafKeyTweak_Signature{Signature: []byte("mock-key-tweak-signature")},
	})
	require.NoError(t, err)
	_, err = client.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(leaf).
		SetPreviousRefundTx(createTestTxBytes(t, 5302)).
		SetIntermediateRefundTx(createTestTxBytes(t, 5303)).
		SetKeyTweak(keyTweakBytes).
		Save(ctx)
	require.NoError(t, err)

	var commitErr error
	baseHandler := NewBaseTransferHandler(cfg)
	require.NotPanics(t, func() {
		_, commitErr = baseHandler.CommitSenderKeyTweaks(ctx, transfer.ID, map[string]*sparkProto.SecretProof{
			leaf.ID.String(): nil,
		})
	})
	require.ErrorContains(t, commitErr, "key tweak proof value is nil")
}

func TestValidateKeyTweakProofRejectsNilProofValue(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{123})
	client := dbCtx.Client

	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	keysharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	publicSharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

	signingKeyshare, err := client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(keysharePrivKey).
		SetPublicShares(map[string]keys.Public{cfg.Identifier: publicSharePrivKey.Public()}).
		SetPublicKey(keysharePrivKey.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	tree, err := client.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Regtest).
		SetOwnerIdentityPubkey(senderPrivKey.Public()).
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
		SetVerifyingPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetOwnerIdentityPubkey(senderPrivKey.Public()).
		SetOwnerSigningPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetRawTx(createTestTxBytes(t, 5400)).
		SetRawRefundTx(createTestTxBytes(t, 5401)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	transfer, err := client.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TransferStatusReceiverKeyTweaked).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(senderPrivKey.Public()).
		SetReceiverIdentityPubkey(receiverPrivKey.Public()).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	secretShare, _ := createValidSecretShares(cfg, rng)
	keyTweakBytes, err := proto.Marshal(&sparkProto.ClaimLeafKeyTweak{
		LeafId:           leaf.ID.String(),
		SecretShareTweak: secretShare,
	})
	require.NoError(t, err)
	transferLeaf, err := client.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(leaf).
		SetPreviousRefundTx(createTestTxBytes(t, 5402)).
		SetIntermediateRefundTx(createTestTxBytes(t, 5403)).
		SetKeyTweak(keyTweakBytes).
		Save(ctx)
	require.NoError(t, err)

	loadedTransferLeaf, err := client.TransferLeaf.Query().
		Where(enttransferleaf.ID(transferLeaf.ID)).
		WithLeaf().
		Only(ctx)
	require.NoError(t, err)

	var validateErr error
	require.NotPanics(t, func() {
		validateErr = NewTransferHandler(cfg).ValidateKeyTweakProof(ctx, []*ent.TransferLeaf{loadedTransferLeaf}, map[string]*sparkProto.SecretProof{
			leaf.ID.String(): nil,
		})
	})
	require.ErrorContains(t, validateErr, "key tweak proof value is nil")
}

func createSwapV3PendingSenderKeyTweakTransfersForTest(
	t *testing.T,
	ctx context.Context,
	client *ent.Client,
	cfg *so.Config,
	rng *rand.ChaCha8,
) (*ent.Transfer, *ent.Transfer) {
	t.Helper()

	alice := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	bob := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	primary := createSwapV3PendingSenderKeyTweakTransferForTest(t, ctx, client, cfg, rng, st.TransferTypePrimarySwapV3, alice, bob, nil, 10_000)
	counter := createSwapV3PendingSenderKeyTweakTransferForTest(t, ctx, client, cfg, rng, st.TransferTypeCounterSwapV3, bob, alice, primary, 20_000)
	return primary, counter
}

func createSwapV3PendingSenderKeyTweakTransferForTest(
	t *testing.T,
	ctx context.Context,
	client *ent.Client,
	cfg *so.Config,
	rng *rand.ChaCha8,
	transferType st.TransferType,
	sender keys.Public,
	receiver keys.Public,
	primary *ent.Transfer,
	txValue int64,
) *ent.Transfer {
	t.Helper()

	keysharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	publicSharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	signingKeyshare, err := client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(keysharePrivKey).
		SetPublicShares(map[string]keys.Public{cfg.Identifier: publicSharePrivKey.Public()}).
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
		SetVerifyingPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetOwnerIdentityPubkey(sender).
		SetOwnerSigningPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetRawTx(createTestTxBytes(t, txValue)).
		SetRawRefundTx(createTestTxBytes(t, txValue+1)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	transferCreate := client.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TransferStatusSenderKeyTweakPending).
		SetType(transferType).
		SetSenderIdentityPubkey(sender).
		SetReceiverIdentityPubkey(receiver).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(time.Hour))
	if primary != nil {
		transferCreate.SetPrimarySwapTransfer(primary)
	}
	transfer, err := transferCreate.Save(ctx)
	require.NoError(t, err)

	secretShare, pubkeySharesTweak := createValidSecretShares(cfg, rng)
	keyTweakBytes, err := proto.Marshal(&sparkProto.SendLeafKeyTweak{
		LeafId:            leaf.ID.String(),
		SecretShareTweak:  secretShare,
		PubkeySharesTweak: pubkeySharesTweak,
		SecretCipher:      []byte("encrypted-secret-share"),
		Sig:               &sparkProto.SendLeafKeyTweak_Signature{Signature: []byte("mock-key-tweak-signature")},
	})
	require.NoError(t, err)

	_, err = client.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(leaf).
		SetPreviousRefundTx(createTestTxBytes(t, txValue+2)).
		SetIntermediateRefundTx(createTestTxBytes(t, txValue+3)).
		SetKeyTweak(keyTweakBytes).
		Save(ctx)
	require.NoError(t, err)

	return transfer
}

func assertTransferStillPendingSenderKeyTweak(t *testing.T, ctx context.Context, client *ent.Client, transferID uuid.UUID) {
	t.Helper()

	assertSenderKeyTweakTransferStateForTest(t, ctx, client, transferID, st.TransferStatusSenderKeyTweakPending, true, false)
}

func assertTransferCommittedSenderKeyTweak(t *testing.T, ctx context.Context, client *ent.Client, transferID uuid.UUID) {
	t.Helper()

	assertSenderKeyTweakTransferStateForTest(t, ctx, client, transferID, st.TransferStatusSenderKeyTweaked, false, true)
}

func assertSenderKeyTweakTransferStateForTest(
	t *testing.T,
	ctx context.Context,
	client *ent.Client,
	transferID uuid.UUID,
	status st.TransferStatus,
	wantKeyTweak bool,
	wantCommittedMaterial bool,
) {
	t.Helper()

	if dbFromCtx, err := ent.GetDbFromContext(ctx); err == nil {
		client = dbFromCtx
	}
	transfer, err := client.Transfer.Get(ctx, transferID)
	require.NoError(t, err)
	assert.Equal(t, status, transfer.Status)

	leaves, err := transfer.QueryTransferLeaves().All(ctx)
	require.NoError(t, err)
	require.Len(t, leaves, 1)
	if wantKeyTweak {
		assert.NotEmpty(t, leaves[0].KeyTweak)
	} else {
		assert.Empty(t, leaves[0].KeyTweak)
	}
	if wantCommittedMaterial {
		assert.NotEmpty(t, leaves[0].SecretCipher)
		assert.NotEmpty(t, leaves[0].Signature)
	} else {
		assert.Empty(t, leaves[0].SecretCipher)
		assert.Empty(t, leaves[0].Signature)
	}
}

func TestDeliverSenderKeyTweak_MissingKeyTweakForLeaf(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{99})

	cfg := sparktesting.TestConfig(t)

	senderIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerIdentityPrivKey := senderIdentityPrivKey

	// Create two signing keyshares, trees, and leaves.
	var leaves [2]*ent.TreeNode
	for i := range leaves {
		keysharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		publicSharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		signingKeyshare, err := dbCtx.Client.SigningKeyshare.Create().
			SetStatus(st.KeyshareStatusAvailable).
			SetSecretShare(keysharePrivKey).
			SetPublicShares(map[string]keys.Public{"test": publicSharePrivKey.Public()}).
			SetPublicKey(keysharePrivKey.Public()).
			SetMinSigners(2).
			SetCoordinatorIndex(0).
			Save(ctx)
		require.NoError(t, err)

		baseTxid := st.NewRandomTxIDForTesting(t)
		tree, err := dbCtx.Client.Tree.Create().
			SetStatus(st.TreeStatusAvailable).
			SetNetwork(btcnetwork.Regtest).
			SetOwnerIdentityPubkey(ownerIdentityPrivKey.Public()).
			SetBaseTxid(baseTxid).
			SetVout(0).
			Save(ctx)
		require.NoError(t, err)

		verifyingPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		ownerSigningPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
		leaf, err := dbCtx.Client.TreeNode.Create().
			SetStatus(st.TreeNodeStatusAvailable).
			SetTree(tree).
			SetNetwork(tree.Network).
			SetSigningKeyshare(signingKeyshare).
			SetValue(1000).
			SetVerifyingPubkey(verifyingPrivKey.Public()).
			SetOwnerIdentityPubkey(ownerIdentityPrivKey.Public()).
			SetOwnerSigningPubkey(ownerSigningPrivKey.Public()).
			SetRawTx(createTestTxBytes(t, int64(3000+i))).
			SetRawRefundTx(createTestTxBytes(t, int64(3100+i))).
			SetVout(0).
			Save(ctx)
		require.NoError(t, err)
		leaves[i] = leaf
	}

	// Create a transfer in SenderInitiated with both leaves.
	transferID := uuid.New()
	transfer, err := dbCtx.Client.Transfer.Create().
		SetID(transferID).
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TransferStatusSenderInitiated).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(senderIdentityPrivKey.Public()).
		SetReceiverIdentityPubkey(receiverIdentityPrivKey.Public()).
		SetTotalValue(2000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	for _, leaf := range leaves {
		_, err = dbCtx.Client.TransferLeaf.Create().
			SetTransfer(transfer).
			SetLeaf(leaf).
			SetPreviousRefundTx(createTestTxBytes(t, 4000)).
			SetIntermediateRefundTx(createTestTxBytes(t, 4001)).
			Save(ctx)
		require.NoError(t, err)
	}

	// Build a transfer package with key tweaks for ONLY the first leaf (not the second).
	// Uses buildKeyTweakPackageForLeaves + signTransferPackage so the signature covers
	// the actual LeavesToSend payload (not an empty slice).
	keyTweakPackage, _ := buildKeyTweakPackageForLeaves(t, cfg, rng, []uuid.UUID{leaves[0].ID})
	pkg := &sparkProto.TransferPackage{
		LeavesToSend: []*sparkProto.UserSignedTxSigningJob{
			{LeafId: leaves[0].ID.String(), RawTx: leaves[0].RawRefundTx},
			{LeafId: leaves[1].ID.String(), RawTx: leaves[1].RawRefundTx},
		},
		KeyTweakPackage: keyTweakPackage,
	}
	signTransferPackage(t, pkg, transferID, ownerIdentityPrivKey)

	req := &pbinternal.DeliverSenderKeyTweakRequest{
		TransferId:              transferID.String(),
		SenderIdentityPublicKey: senderIdentityPrivKey.Public().Serialize(),
		TransferPackage:         pkg,
	}

	handler := NewInternalTransferHandler(cfg)
	err = handler.DeliverSenderKeyTweak(ctx, req)

	// Should fail because leaf[1] has no key tweak in the encrypted package.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key tweak count mismatch")

	// Verify transfer status was NOT updated to SenderKeyTweakPending.
	updatedTransfer, err := dbCtx.Client.Transfer.Get(ctx, transferID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusSenderInitiated, updatedTransfer.Status,
		"transfer must remain SenderInitiated when DeliverSenderKeyTweak fails")
}

func TestDeliverSenderKeyTweak_RejectsMismatchedSenderIdentity(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{101})

	cfg := sparktesting.TestConfig(t)

	senderIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	attackerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

	keysharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	publicSharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	signingKeyshare, err := dbCtx.Client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(keysharePrivKey).
		SetPublicShares(map[string]keys.Public{"test": publicSharePrivKey.Public()}).
		SetPublicKey(keysharePrivKey.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	tree, err := dbCtx.Client.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Regtest).
		SetOwnerIdentityPubkey(senderIdentityPrivKey.Public()).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	verifyingPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerSigningPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	leaf, err := dbCtx.Client.TreeNode.Create().
		SetStatus(st.TreeNodeStatusAvailable).
		SetTree(tree).
		SetNetwork(tree.Network).
		SetSigningKeyshare(signingKeyshare).
		SetValue(1000).
		SetVerifyingPubkey(verifyingPrivKey.Public()).
		SetOwnerIdentityPubkey(senderIdentityPrivKey.Public()).
		SetOwnerSigningPubkey(ownerSigningPrivKey.Public()).
		SetRawTx(createTestTxBytes(t, 3000)).
		SetRawRefundTx(createTestTxBytes(t, 3100)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	transferID := uuid.New()
	transfer, err := dbCtx.Client.Transfer.Create().
		SetID(transferID).
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TransferStatusSenderInitiated).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(senderIdentityPrivKey.Public()).
		SetReceiverIdentityPubkey(receiverIdentityPrivKey.Public()).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	transferLeaf, err := dbCtx.Client.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(leaf).
		SetPreviousRefundTx(createTestTxBytes(t, 4000)).
		SetIntermediateRefundTx(createTestTxBytes(t, 4001)).
		Save(ctx)
	require.NoError(t, err)

	keyTweakPackage, keyTweakProofs := buildKeyTweakPackageForLeaves(t, cfg, rng, []uuid.UUID{leaf.ID})
	pkg := &sparkProto.TransferPackage{
		LeavesToSend: []*sparkProto.UserSignedTxSigningJob{
			{LeafId: leaf.ID.String(), RawTx: leaf.RawRefundTx},
		},
		KeyTweakPackage: keyTweakPackage,
	}
	signTransferPackage(t, pkg, transferID, attackerIdentityPrivKey)

	req := &pbinternal.DeliverSenderKeyTweakRequest{
		TransferId:              transferID.String(),
		SenderIdentityPublicKey: attackerIdentityPrivKey.Public().Serialize(),
		TransferPackage:         pkg,
		SenderKeyTweakProofs:    keyTweakProofs,
	}

	handler := NewInternalTransferHandler(cfg)
	err = handler.DeliverSenderKeyTweak(ctx, req)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "sender identity public key does not match transfer sender")

	updatedTransfer, err := dbCtx.Client.Transfer.Get(ctx, transferID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusSenderInitiated, updatedTransfer.Status)

	updatedTransferLeaf, err := dbCtx.Client.TransferLeaf.Get(ctx, transferLeaf.ID)
	require.NoError(t, err)
	assert.Empty(t, updatedTransferLeaf.KeyTweak)
}

func TestFinalizeTransferWithTransferPackageRejectsSessionSenderMismatch(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{102})

	cfg := sparktesting.TestConfig(t)
	cfg.AuthzEnforced = true

	senderIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	sessionIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

	transferID := uuid.New()
	_, err := dbCtx.Client.Transfer.Create().
		SetID(transferID).
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TransferStatusSenderInitiated).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(senderIdentityPrivKey.Public()).
		SetReceiverIdentityPubkey(receiverIdentityPrivKey.Public()).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)
	_, err = dbCtx.Client.TransferSender.Create().
		SetTransferID(transferID).
		SetIdentityPubkey(senderIdentityPrivKey.Public()).
		SetTransferType(st.TransferTypeTransfer).
		Save(ctx)
	require.NoError(t, err)

	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(sessionIdentityPrivKey.Public().Serialize()), time.Now().Add(time.Hour).Unix())
	handler := NewTransferHandler(cfg)
	_, err = handler.FinalizeTransferWithTransferPackage(ctx, &sparkProto.FinalizeTransferWithTransferPackageRequest{
		TransferId: transferID.String(),
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "session identity does not match request identity")
}

// TestDeliverSenderKeyTweak_ProofMismatch_RetainsSenderInitiated verifies that
// DeliverSenderKeyTweak rejects a request whose coordinator-supplied
// SenderKeyTweakProofs do not match the proofs encrypted in the transfer package,
// and that the transfer remains in SenderInitiated state.
func TestDeliverSenderKeyTweak_ProofMismatch_RetainsSenderInitiated(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{77})

	transferID, leafID, req := setupSingleLeafDeliverFixture(t, ctx, dbCtx, cfg, rng)

	req.SenderKeyTweakProofs = map[string]*sparkProto.SecretProof{
		leafID.String(): {Proofs: [][]byte{[]byte("garbage-proof-bytes-that-cannot-match")}},
	}

	err := NewInternalTransferHandler(cfg).DeliverSenderKeyTweak(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sender key tweak proof mismatch")

	updatedTransfer, err := dbCtx.Client.Transfer.Get(ctx, transferID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusSenderInitiated, updatedTransfer.Status,
		"transfer must remain SenderInitiated when cross-SO proof check fails")
}

// TestDeliverSenderKeyTweak_MissingProofs_RetainsSenderInitiated verifies that
// DeliverSenderKeyTweak rejects a request whose SenderKeyTweakProofs field is
// absent — i.e., the cross-SO proof check is required, not skippable.
func TestDeliverSenderKeyTweak_MissingProofs_RetainsSenderInitiated(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{78})

	transferID, _, req := setupSingleLeafDeliverFixture(t, ctx, dbCtx, cfg, rng)
	// req.SenderKeyTweakProofs intentionally left nil.

	err := NewInternalTransferHandler(cfg).DeliverSenderKeyTweak(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be nil")

	updatedTransfer, err := dbCtx.Client.Transfer.Get(ctx, transferID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusSenderInitiated, updatedTransfer.Status,
		"transfer must remain SenderInitiated when SenderKeyTweakProofs is missing")
}

// setupSingleLeafDeliverFixture builds the minimal DB state and a signed
// transfer package needed to drive DeliverSenderKeyTweak through to the
// cross-SO proof check. The returned request has TransferPackage populated;
// callers set SenderKeyTweakProofs (or leave it nil) to exercise the check.
func setupSingleLeafDeliverFixture(
	t *testing.T,
	ctx context.Context,
	dbCtx *db.TestContext,
	cfg *so.Config,
	rng *rand.ChaCha8,
) (uuid.UUID, uuid.UUID, *pbinternal.DeliverSenderKeyTweakRequest) {
	t.Helper()

	senderIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

	keysharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	publicSharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	signingKeyshare, err := dbCtx.Client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(keysharePrivKey).
		SetPublicShares(map[string]keys.Public{"test": publicSharePrivKey.Public()}).
		SetPublicKey(keysharePrivKey.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	tree, err := dbCtx.Client.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Regtest).
		SetOwnerIdentityPubkey(senderIdentityPrivKey.Public()).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	verifyingPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerSigningPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	leaf, err := dbCtx.Client.TreeNode.Create().
		SetStatus(st.TreeNodeStatusAvailable).
		SetTree(tree).
		SetNetwork(tree.Network).
		SetSigningKeyshare(signingKeyshare).
		SetValue(1000).
		SetVerifyingPubkey(verifyingPrivKey.Public()).
		SetOwnerIdentityPubkey(senderIdentityPrivKey.Public()).
		SetOwnerSigningPubkey(ownerSigningPrivKey.Public()).
		SetRawTx(createTestTxBytes(t, 3000)).
		SetRawRefundTx(createTestTxBytes(t, 3100)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	transferID := uuid.New()
	transfer, err := dbCtx.Client.Transfer.Create().
		SetID(transferID).
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TransferStatusSenderInitiated).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(senderIdentityPrivKey.Public()).
		SetReceiverIdentityPubkey(receiverIdentityPrivKey.Public()).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	_, err = dbCtx.Client.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(leaf).
		SetPreviousRefundTx(createTestTxBytes(t, 4000)).
		SetIntermediateRefundTx(createTestTxBytes(t, 4001)).
		Save(ctx)
	require.NoError(t, err)

	keyTweakPackage, _ := buildKeyTweakPackageForLeaves(t, cfg, rng, []uuid.UUID{leaf.ID})
	pkg := &sparkProto.TransferPackage{
		LeavesToSend: []*sparkProto.UserSignedTxSigningJob{
			{LeafId: leaf.ID.String(), RawTx: leaf.RawRefundTx},
		},
		KeyTweakPackage: keyTweakPackage,
	}
	signTransferPackage(t, pkg, transferID, senderIdentityPrivKey)

	req := &pbinternal.DeliverSenderKeyTweakRequest{
		TransferId:              transferID.String(),
		SenderIdentityPublicKey: senderIdentityPrivKey.Public().Serialize(),
		TransferPackage:         pkg,
	}
	return transferID, leaf.ID, req
}
