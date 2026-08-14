//go:build lightspark

package handler

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	sparkconst "github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransferreceiver "github.com/lightsparkdev/spark/so/ent/transferreceiver"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestUpdateTransferLocksNewRemoteLeafFromAnyLocalNodeStatus(t *testing.T) {
	testCases := []struct {
		name   string
		status st.TreeNodeStatus
	}{
		{name: "transfer locked", status: st.TreeNodeStatusTransferLocked},
		{name: "on chain", status: st.TreeNodeStatusOnChain},
		{name: "exited", status: st.TreeNodeStatusExited},
		{name: "reimbursed", status: st.TreeNodeStatusReimbursed},
		{name: "parent exited", status: st.TreeNodeStatusParentExited},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := db.ConnectToTestPostgres(t)
			dbTx, err := ent.GetDbFromContext(ctx)
			require.NoError(t, err)

			owner := keys.GeneratePrivateKey().Public()
			receiver := keys.GeneratePrivateKey().Public()
			node := createSyncTransferTestNode(t, ctx, dbTx, owner, tc.status)
			remoteTransfer := syncTransferRemoteTransfer(t, node, owner, receiver)

			err = NewSspRequestHandler(&so.Config{Identifier: "test-operator"}).updateTransfer(
				ctx,
				remoteTransfer,
				&pbssp.SyncTransferRequest{OperatorId: "source-operator"},
			)
			require.NoError(t, err)

			refreshed, err := dbTx.TreeNode.Get(ctx, node.ID)
			require.NoError(t, err)
			require.Equal(t, st.TreeNodeStatusTransferLocked, refreshed.Status)

			transferLeafs, err := dbTx.TransferLeaf.Query().All(ctx)
			require.NoError(t, err)
			require.Len(t, transferLeafs, 1)
		})
	}
}

func TestUpdateTransferLocksNewRemoteLeafWhenLocalOwnerDiverges(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	staleOwner := keys.GeneratePrivateKey().Public()
	sender := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	node := createSyncTransferTestNode(t, ctx, dbTx, staleOwner, st.TreeNodeStatusAvailable)
	remoteTransfer := syncTransferRemoteTransfer(t, node, sender, receiver)

	err = NewSspRequestHandler(&so.Config{Identifier: "test-operator"}).updateTransfer(
		ctx,
		remoteTransfer,
		&pbssp.SyncTransferRequest{OperatorId: "source-operator"},
	)
	require.NoError(t, err)

	refreshed, err := dbTx.TreeNode.Get(ctx, node.ID)
	require.NoError(t, err)
	require.Equal(t, st.TreeNodeStatusTransferLocked, refreshed.Status)
	// Locking without adopting the owner would leave the node in a state neither
	// a rejection nor a completed repair produces.
	require.True(t, refreshed.OwnerIdentityPubkey.Equals(sender))

	transferLeafs, err := dbTx.TransferLeaf.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, transferLeafs, 1)
}

func TestUpdateTransferSyncsRemoteRefundTxPayingUnexpectedReceiver(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	owner := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	otherReceiver := keys.GeneratePrivateKey().Public()
	node := createSyncTransferTestNode(t, ctx, dbTx, owner, st.TreeNodeStatusAvailable)
	remoteTransfer := syncTransferRemoteTransfer(t, node, owner, receiver)
	setSyncTransferRemoteCpfpRefund(t, remoteTransfer, node, otherReceiver)

	err = NewSspRequestHandler(&so.Config{Identifier: "test-operator"}).updateTransfer(
		ctx,
		remoteTransfer,
		&pbssp.SyncTransferRequest{OperatorId: "source-operator"},
	)
	require.NoError(t, err)

	transferLeafs, err := dbTx.TransferLeaf.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, transferLeafs, 1)
	require.Equal(t, remoteTransfer.GetLeaves()[0].GetIntermediateRefundTx(), transferLeafs[0].IntermediateRefundTx)
}

func TestUpdateTransferSyncsExistingRemoteLeafFromAnyLocalNodeStatus(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	owner := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	node := createSyncTransferTestNode(t, ctx, dbTx, owner, st.TreeNodeStatusOnChain)
	localTransfer := createSyncTransferLocalTransfer(t, ctx, dbTx, node, owner, receiver)
	localLeaf, err := dbTx.TransferLeaf.Create().
		SetTransfer(localTransfer).
		SetLeaf(node).
		SetPreviousRefundTx(createTestTxBytes(t, 3000)).
		SetIntermediateRefundTx(createTestTxBytes(t, 3001)).
		SetSecretCipher([]byte("local-secret")).
		SetSignature([]byte("local-signature")).
		Save(ctx)
	require.NoError(t, err)

	remoteTransfer := syncTransferRemoteTransfer(t, node, owner, receiver)
	remoteTransfer.Id = localTransfer.ID.String()
	remoteTransfer.Leaves[0].SecretCipher = []byte("remote-secret")
	remoteTransfer.Leaves[0].Sig = &pb.TransferLeaf_Signature{Signature: []byte("remote-signature")}

	err = NewSspRequestHandler(&so.Config{Identifier: "test-operator"}).updateTransfer(
		ctx,
		remoteTransfer,
		&pbssp.SyncTransferRequest{OperatorId: "source-operator"},
	)
	require.NoError(t, err)

	refreshedNode, err := dbTx.TreeNode.Get(ctx, node.ID)
	require.NoError(t, err)
	require.Equal(t, st.TreeNodeStatusTransferLocked, refreshedNode.Status)

	refreshedLeaf, err := dbTx.TransferLeaf.Get(ctx, localLeaf.ID)
	require.NoError(t, err)
	require.Equal(t, []byte("remote-secret"), refreshedLeaf.SecretCipher)
	require.Equal(t, []byte("remote-signature"), refreshedLeaf.Signature)
}

// The refunds below sit one TimeLockInterval higher than this operator's node
// implies, because the chosen operator has not advanced as far. That is the
// divergence sync_transfers exists to repair.
func TestUpdateTransferSyncsRemoteRefundTxWhoseTimelockDivergesFromLocalNode(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	owner := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	node := createSyncTransferWatchtowerReadyTestNode(t, ctx, dbTx, owner, st.TreeNodeStatusAvailable)
	remoteTransfer := syncTransferRemoteTransfer(t, node, owner, receiver)

	nodeTx, err := common.TxFromRawTxBytes(node.RawTx)
	require.NoError(t, err)
	directTx, err := common.TxFromRawTxBytes(node.DirectTx)
	require.NoError(t, err)
	divergedTimelock := sparkconst.InitialTimeLock
	remoteLeaf := remoteTransfer.GetLeaves()[0]
	remoteLeaf.IntermediateRefundTx = createVersion3CPFPRefundTx(
		t, nodeTx.TxHash(), uint32(node.Vout), receiver, int64(node.Value), divergedTimelock,
	)
	remoteLeaf.IntermediateDirectRefundTx = createVersion3DirectRefundTx(
		t, directTx.TxHash(), 0, receiver, int64(node.Value), divergedTimelock+sparkconst.DirectTimelockOffset,
	)
	remoteLeaf.IntermediateDirectFromCpfpRefundTx = createVersion3DirectRefundTx(
		t, nodeTx.TxHash(), uint32(node.Vout), receiver, int64(node.Value), divergedTimelock+sparkconst.DirectTimelockOffset,
	)

	err = NewSspRequestHandler(&so.Config{Identifier: "test-operator"}).updateTransfer(
		ctx,
		remoteTransfer,
		&pbssp.SyncTransferRequest{OperatorId: "source-operator"},
	)
	require.NoError(t, err)

	transferLeafs, err := dbTx.TransferLeaf.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, transferLeafs, 1)
	require.Equal(t, remoteLeaf.GetIntermediateRefundTx(), transferLeafs[0].IntermediateRefundTx)
	require.Equal(t, remoteLeaf.GetIntermediateDirectRefundTx(), transferLeafs[0].IntermediateDirectRefundTx)
	require.Equal(t, remoteLeaf.GetIntermediateDirectFromCpfpRefundTx(), transferLeafs[0].IntermediateDirectFromCpfpRefundTx)
}

func TestUpdateTransferAcceptsRemoteDirectRefundTxs(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	owner := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	node := createSyncTransferWatchtowerReadyTestNode(t, ctx, dbTx, owner, st.TreeNodeStatusAvailable)
	remoteTransfer := syncTransferRemoteTransfer(t, node, owner, receiver)
	addSyncTransferDirectRefunds(t, remoteTransfer, node, receiver)

	err = NewSspRequestHandler(&so.Config{Identifier: "test-operator"}).updateTransfer(
		ctx,
		remoteTransfer,
		&pbssp.SyncTransferRequest{OperatorId: "source-operator"},
	)
	require.NoError(t, err)

	transferLeafs, err := dbTx.TransferLeaf.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, transferLeafs, 1)
	require.Equal(t, remoteTransfer.GetLeaves()[0].GetIntermediateDirectRefundTx(), transferLeafs[0].IntermediateDirectRefundTx)
	require.Equal(t, remoteTransfer.GetLeaves()[0].GetIntermediateDirectFromCpfpRefundTx(), transferLeafs[0].IntermediateDirectFromCpfpRefundTx)
}

func TestUpdateTransferPersistsTypedSignatureOnNewLeaf(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	owner := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	node := createSyncTransferWatchtowerReadyTestNode(t, ctx, dbTx, owner, st.TreeNodeStatusAvailable)
	remoteTransfer := syncTransferRemoteTransfer(t, node, owner, receiver)
	addSyncTransferDirectRefunds(t, remoteTransfer, node, receiver)
	remoteTransfer.Leaves[0].Sig = &pb.TransferLeaf_TypedSignature{
		TypedSignature: &pbcommon.Signature{
			Scheme:    pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR,
			Signature: []byte("remote-typed-signature"),
		},
	}

	err = NewSspRequestHandler(&so.Config{Identifier: "test-operator"}).updateTransfer(
		ctx,
		remoteTransfer,
		&pbssp.SyncTransferRequest{OperatorId: "source-operator"},
	)
	require.NoError(t, err)

	transferLeafs, err := dbTx.TransferLeaf.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, transferLeafs, 1)
	require.Equal(t, []byte("remote-typed-signature"), transferLeafs[0].Signature)
	require.Equal(t, int32(pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR), transferLeafs[0].SignatureScheme)
}

func TestUpdateTransferPersistsTypedSignatureOnExistingLeaf(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	owner := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	node := createSyncTransferWatchtowerReadyTestNode(t, ctx, dbTx, owner, st.TreeNodeStatusAvailable)
	localTransfer := createSyncTransferLocalTransfer(t, ctx, dbTx, node, owner, receiver)
	localLeaf, err := dbTx.TransferLeaf.Create().
		SetTransfer(localTransfer).
		SetLeaf(node).
		SetPreviousRefundTx(node.RawRefundTx).
		SetIntermediateRefundTx(createTestTxBytes(t, 3000)).
		SetSecretCipher([]byte("local-secret")).
		SetSignature([]byte("local-signature")).
		Save(ctx)
	require.NoError(t, err)

	remoteTransfer := syncTransferRemoteTransfer(t, node, owner, receiver)
	remoteTransfer.Id = localTransfer.ID.String()
	setSyncTransferRemoteCpfpRefund(t, remoteTransfer, node, receiver)
	remoteTransfer.Leaves[0].Sig = &pb.TransferLeaf_TypedSignature{
		TypedSignature: &pbcommon.Signature{
			Scheme:    pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR,
			Signature: []byte("remote-typed-signature"),
		},
	}

	err = NewSspRequestHandler(&so.Config{Identifier: "test-operator"}).updateTransfer(
		ctx,
		remoteTransfer,
		&pbssp.SyncTransferRequest{OperatorId: "source-operator"},
	)
	require.NoError(t, err)

	refreshedLeaf, err := dbTx.TransferLeaf.Get(ctx, localLeaf.ID)
	require.NoError(t, err)
	require.Equal(t, []byte("remote-typed-signature"), refreshedLeaf.Signature)
	require.Equal(t, int32(pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR), refreshedLeaf.SignatureScheme)
}

func TestUpdateTransferRejectsInvalidTypedSignature(t *testing.T) {
	testCases := []struct {
		name  string
		typed *pbcommon.Signature
	}{
		{
			name: "unspecified scheme",
			typed: &pbcommon.Signature{
				Scheme:    pbcommon.SignatureScheme_SIGNATURE_SCHEME_UNSPECIFIED,
				Signature: []byte("remote-typed-signature"),
			},
		},
		{
			name: "empty signature bytes",
			typed: &pbcommon.Signature{
				Scheme: pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := db.ConnectToTestPostgres(t)
			dbTx, err := ent.GetDbFromContext(ctx)
			require.NoError(t, err)

			owner := keys.GeneratePrivateKey().Public()
			receiver := keys.GeneratePrivateKey().Public()
			node := createSyncTransferTestNode(t, ctx, dbTx, owner, st.TreeNodeStatusAvailable)
			remoteTransfer := syncTransferRemoteTransfer(t, node, owner, receiver)
			remoteTransfer.Leaves[0].Sig = &pb.TransferLeaf_TypedSignature{TypedSignature: tc.typed}

			err = NewSspRequestHandler(&so.Config{Identifier: "test-operator"}).updateTransfer(
				ctx,
				remoteTransfer,
				&pbssp.SyncTransferRequest{OperatorId: "source-operator"},
			)
			require.Error(t, err)
			require.ErrorContains(t, err, "invalid typed signature")

			refreshed, err := dbTx.TreeNode.Get(ctx, node.ID)
			require.NoError(t, err)
			require.Equal(t, st.TreeNodeStatusAvailable, refreshed.Status)

			transferLeafs, err := dbTx.TransferLeaf.Query().All(ctx)
			require.NoError(t, err)
			require.Empty(t, transferLeafs)
		})
	}
}

func TestUpdateTransferClearsLocalDirectRefundsWhenRemoteOmitsThem(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	owner := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	node := createSyncTransferWatchtowerReadyTestNode(t, ctx, dbTx, owner, st.TreeNodeStatusAvailable)
	localTransfer := createSyncTransferLocalTransfer(t, ctx, dbTx, node, owner, receiver)
	localIntermediateDirectRefundTx := createTestTxBytes(t, 3001)
	localIntermediateDirectFromCpfpRefundTx := createTestTxBytes(t, 3002)
	localLeaf, err := dbTx.TransferLeaf.Create().
		SetTransfer(localTransfer).
		SetLeaf(node).
		SetPreviousRefundTx(node.RawRefundTx).
		SetPreviousDirectRefundTx(node.DirectRefundTx).
		SetPreviousDirectFromCpfpRefundTx(node.DirectFromCpfpRefundTx).
		SetIntermediateRefundTx(createTestTxBytes(t, 3000)).
		SetIntermediateDirectRefundTx(localIntermediateDirectRefundTx).
		SetIntermediateDirectFromCpfpRefundTx(localIntermediateDirectFromCpfpRefundTx).
		SetSecretCipher([]byte("local-secret")).
		SetSignature([]byte("local-signature")).
		Save(ctx)
	require.NoError(t, err)

	remoteTransfer := syncTransferRemoteTransfer(t, node, owner, receiver)
	remoteTransfer.Id = localTransfer.ID.String()
	setSyncTransferRemoteCpfpRefund(t, remoteTransfer, node, receiver)
	remoteTransfer.Leaves[0].SecretCipher = []byte("remote-secret")
	remoteTransfer.Leaves[0].Sig = &pb.TransferLeaf_Signature{Signature: []byte("remote-signature")}

	err = NewSspRequestHandler(&so.Config{Identifier: "test-operator"}).updateTransfer(
		ctx,
		remoteTransfer,
		&pbssp.SyncTransferRequest{OperatorId: "source-operator"},
	)
	require.NoError(t, err)

	refreshedNode, err := dbTx.TreeNode.Get(ctx, node.ID)
	require.NoError(t, err)
	require.Equal(t, st.TreeNodeStatusTransferLocked, refreshedNode.Status)

	// An omission by the chosen operator is a value to adopt, not data to protect.
	refreshedLeaf, err := dbTx.TransferLeaf.Get(ctx, localLeaf.ID)
	require.NoError(t, err)
	require.Equal(t, []byte("remote-secret"), refreshedLeaf.SecretCipher)
	require.Equal(t, []byte("remote-signature"), refreshedLeaf.Signature)
	require.Empty(t, refreshedLeaf.IntermediateDirectRefundTx)
	require.Empty(t, refreshedLeaf.IntermediateDirectFromCpfpRefundTx)
}

func TestUpdateTransferAcceptsOmittedRemoteDirectRefundsWhenLocalLeafLacksThem(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	owner := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	node := createSyncTransferWatchtowerReadyTestNode(t, ctx, dbTx, owner, st.TreeNodeStatusAvailable)
	localTransfer := createSyncTransferLocalTransfer(t, ctx, dbTx, node, owner, receiver)
	localLeaf, err := dbTx.TransferLeaf.Create().
		SetTransfer(localTransfer).
		SetLeaf(node).
		SetPreviousRefundTx(node.RawRefundTx).
		SetIntermediateRefundTx(createTestTxBytes(t, 3000)).
		SetSecretCipher([]byte("local-secret")).
		SetSignature([]byte("local-signature")).
		Save(ctx)
	require.NoError(t, err)

	remoteTransfer := syncTransferRemoteTransfer(t, node, owner, receiver)
	remoteTransfer.Id = localTransfer.ID.String()
	setSyncTransferRemoteCpfpRefund(t, remoteTransfer, node, receiver)
	remoteTransfer.Leaves[0].SecretCipher = []byte("remote-secret")
	remoteTransfer.Leaves[0].Sig = &pb.TransferLeaf_Signature{Signature: []byte("remote-signature")}

	err = NewSspRequestHandler(&so.Config{Identifier: "test-operator"}).updateTransfer(
		ctx,
		remoteTransfer,
		&pbssp.SyncTransferRequest{OperatorId: "source-operator"},
	)
	require.NoError(t, err)

	refreshedNode, err := dbTx.TreeNode.Get(ctx, node.ID)
	require.NoError(t, err)
	require.Equal(t, st.TreeNodeStatusTransferLocked, refreshedNode.Status)

	refreshedLeaf, err := dbTx.TransferLeaf.Get(ctx, localLeaf.ID)
	require.NoError(t, err)
	require.Equal(t, []byte("remote-secret"), refreshedLeaf.SecretCipher)
	require.Equal(t, []byte("remote-signature"), refreshedLeaf.Signature)
	require.Empty(t, refreshedLeaf.IntermediateDirectRefundTx)
	require.Empty(t, refreshedLeaf.IntermediateDirectFromCpfpRefundTx)
}

func TestUpdateTransferSyncsOwnerIdentityForExistingRemoteLeaf(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	staleOwner := keys.GeneratePrivateKey().Public()
	sender := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	// owner_identity_pubkey diverges from the sender — e.g. a partial
	// keyshare-only revert left it stale. The leaf is already tied to the
	// local transfer via transfer_leaf below, so the sync should correct it.
	node := createSyncTransferWatchtowerReadyTestNode(t, ctx, dbTx, staleOwner, st.TreeNodeStatusAvailable)
	localTransfer := createSyncTransferLocalTransfer(t, ctx, dbTx, node, sender, receiver)
	localLeaf, err := dbTx.TransferLeaf.Create().
		SetTransfer(localTransfer).
		SetLeaf(node).
		SetPreviousRefundTx(node.RawRefundTx).
		SetIntermediateRefundTx(createTestTxBytes(t, 3000)).
		SetSecretCipher([]byte("local-secret")).
		SetSignature([]byte("local-signature")).
		Save(ctx)
	require.NoError(t, err)

	remoteTransfer := syncTransferRemoteTransfer(t, node, sender, receiver)
	remoteTransfer.Id = localTransfer.ID.String()
	setSyncTransferRemoteCpfpRefund(t, remoteTransfer, node, receiver)
	remoteTransfer.Leaves[0].SecretCipher = []byte("remote-secret")
	remoteTransfer.Leaves[0].Sig = &pb.TransferLeaf_Signature{Signature: []byte("remote-signature")}

	err = NewSspRequestHandler(&so.Config{Identifier: "test-operator"}).updateTransfer(
		ctx,
		remoteTransfer,
		&pbssp.SyncTransferRequest{OperatorId: "source-operator"},
	)
	require.NoError(t, err)

	refreshedNode, err := dbTx.TreeNode.Get(ctx, node.ID)
	require.NoError(t, err)
	require.Equal(t, st.TreeNodeStatusTransferLocked, refreshedNode.Status)
	require.True(t, refreshedNode.OwnerIdentityPubkey.Equals(sender))

	refreshedLeaf, err := dbTx.TransferLeaf.Get(ctx, localLeaf.ID)
	require.NoError(t, err)
	require.Equal(t, []byte("remote-secret"), refreshedLeaf.SecretCipher)
}

func TestUpdateTransferSyncsReceiverAsOwnerWhenRemoteHasApplied(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	sender := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	node := createSyncTransferWatchtowerReadyTestNode(t, ctx, dbTx, sender, st.TreeNodeStatusAvailable)
	localTransfer := createSyncTransferLocalTransfer(t, ctx, dbTx, node, sender, receiver)
	_, err = dbTx.TransferLeaf.Create().
		SetTransfer(localTransfer).
		SetLeaf(node).
		SetPreviousRefundTx(node.RawRefundTx).
		SetIntermediateRefundTx(createTestTxBytes(t, 3000)).
		SetSecretCipher([]byte("local-secret")).
		SetSignature([]byte("local-signature")).
		Save(ctx)
	require.NoError(t, err)

	remoteTransfer := syncTransferRemoteTransfer(t, node, sender, receiver)
	remoteTransfer.Id = localTransfer.ID.String()
	remoteTransfer.Status = pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAK_APPLIED
	remoteTransfer.Leaves[0].Leaf.OwnerIdentityPublicKey = receiver.Serialize()
	setSyncTransferRemoteCpfpRefund(t, remoteTransfer, node, receiver)

	err = NewSspRequestHandler(&so.Config{Identifier: "test-operator"}).updateTransfer(
		ctx,
		remoteTransfer,
		&pbssp.SyncTransferRequest{OperatorId: "source-operator"},
	)
	require.NoError(t, err)

	refreshedNode, err := dbTx.TreeNode.Get(ctx, node.ID)
	require.NoError(t, err)
	require.True(t, refreshedNode.OwnerIdentityPubkey.Equals(receiver))
}

// The owner the chosen operator reports wins even against what the transfer's own
// status implies: which side is stale is the requester's call, not the SO's.
func TestUpdateTransferSyncsExistingRemoteLeafOwnerAgainstTransferStatus(t *testing.T) {
	testCases := []struct {
		name        string
		remoteOwner func(sender, receiver, thirdParty keys.Public) keys.Public
	}{
		{
			name:        "receiver while status still says sender",
			remoteOwner: func(_, receiver, _ keys.Public) keys.Public { return receiver },
		},
		{
			name:        "third party",
			remoteOwner: func(_, _, thirdParty keys.Public) keys.Public { return thirdParty },
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := db.ConnectToTestPostgres(t)
			dbTx, err := ent.GetDbFromContext(ctx)
			require.NoError(t, err)

			sender := keys.GeneratePrivateKey().Public()
			receiver := keys.GeneratePrivateKey().Public()
			thirdParty := keys.GeneratePrivateKey().Public()
			node := createSyncTransferWatchtowerReadyTestNode(t, ctx, dbTx, sender, st.TreeNodeStatusAvailable)
			localTransfer := createSyncTransferLocalTransfer(t, ctx, dbTx, node, sender, receiver)
			_, err = dbTx.TransferLeaf.Create().
				SetTransfer(localTransfer).
				SetLeaf(node).
				SetPreviousRefundTx(node.RawRefundTx).
				SetIntermediateRefundTx(createTestTxBytes(t, 3000)).
				SetSecretCipher([]byte("local-secret")).
				SetSignature([]byte("local-signature")).
				Save(ctx)
			require.NoError(t, err)

			remoteOwner := tc.remoteOwner(sender, receiver, thirdParty)
			remoteTransfer := syncTransferRemoteTransfer(t, node, sender, receiver)
			remoteTransfer.Id = localTransfer.ID.String()
			remoteTransfer.Leaves[0].Leaf.OwnerIdentityPublicKey = remoteOwner.Serialize()
			setSyncTransferRemoteCpfpRefund(t, remoteTransfer, node, receiver)

			err = NewSspRequestHandler(&so.Config{Identifier: "test-operator"}).updateTransfer(
				ctx,
				remoteTransfer,
				&pbssp.SyncTransferRequest{OperatorId: "source-operator"},
			)
			require.NoError(t, err)

			refreshedNode, err := dbTx.TreeNode.Get(ctx, node.ID)
			require.NoError(t, err)
			require.True(t, refreshedNode.OwnerIdentityPubkey.Equals(remoteOwner))
		})
	}
}

// A stale local owner_signing_pubkey is repaired by fix_keyshare / sync_tree_nodes,
// not here, so it must not block the owner-identity sync.
func TestUpdateTransferSyncsExistingRemoteLeafOwnerWhenLocalSigningOwnerDiverges(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	sender := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	node := createSyncTransferWatchtowerReadyTestNode(t, ctx, dbTx, sender, st.TreeNodeStatusAvailable)
	localTransfer := createSyncTransferLocalTransfer(t, ctx, dbTx, node, sender, receiver)
	_, err = dbTx.TransferLeaf.Create().
		SetTransfer(localTransfer).
		SetLeaf(node).
		SetPreviousRefundTx(node.RawRefundTx).
		SetIntermediateRefundTx(createTestTxBytes(t, 3000)).
		SetSecretCipher([]byte("local-secret")).
		SetSignature([]byte("local-signature")).
		Save(ctx)
	require.NoError(t, err)

	remoteTransfer := syncTransferRemoteTransfer(t, node, sender, receiver)
	remoteTransfer.Id = localTransfer.ID.String()
	remoteTransfer.Status = pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAK_APPLIED
	remoteTransfer.Leaves[0].Leaf.OwnerIdentityPublicKey = receiver.Serialize()
	remoteTransfer.Leaves[0].Leaf.OwnerSigningPublicKey = keys.GeneratePrivateKey().Public().Serialize()
	setSyncTransferRemoteCpfpRefund(t, remoteTransfer, node, receiver)

	err = NewSspRequestHandler(&so.Config{Identifier: "test-operator"}).updateTransfer(
		ctx,
		remoteTransfer,
		&pbssp.SyncTransferRequest{OperatorId: "source-operator"},
	)
	require.NoError(t, err)

	refreshedNode, err := dbTx.TreeNode.Get(ctx, node.ID)
	require.NoError(t, err)
	require.True(t, refreshedNode.OwnerIdentityPubkey.Equals(receiver))
}

// An unparseable owner key describes no state a repair could legitimately write.
func TestUpdateTransferRejectsExistingRemoteLeafWithMalformedOwnerKey(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	sender := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	node := createSyncTransferWatchtowerReadyTestNode(t, ctx, dbTx, sender, st.TreeNodeStatusAvailable)
	localTransfer := createSyncTransferLocalTransfer(t, ctx, dbTx, node, sender, receiver)
	_, err = dbTx.TransferLeaf.Create().
		SetTransfer(localTransfer).
		SetLeaf(node).
		SetPreviousRefundTx(node.RawRefundTx).
		SetIntermediateRefundTx(createTestTxBytes(t, 3000)).
		SetSecretCipher([]byte("local-secret")).
		SetSignature([]byte("local-signature")).
		Save(ctx)
	require.NoError(t, err)

	remoteTransfer := syncTransferRemoteTransfer(t, node, sender, receiver)
	remoteTransfer.Id = localTransfer.ID.String()
	setSyncTransferRemoteCpfpRefund(t, remoteTransfer, node, receiver)
	remoteTransfer.GetLeaves()[0].GetLeaf().OwnerIdentityPublicKey = []byte("not-a-key")

	err = NewSspRequestHandler(&so.Config{Identifier: "test-operator"}).updateTransfer(
		ctx,
		remoteTransfer,
		&pbssp.SyncTransferRequest{OperatorId: "source-operator"},
	)
	require.Error(t, err)

	refreshedNode, err := dbTx.TreeNode.Get(ctx, node.ID)
	require.NoError(t, err)
	require.True(t, refreshedNode.OwnerIdentityPubkey.Equals(sender))
}

func TestUpdateTransferRejectsNewRemoteLeafWithMalformedOwnerKey(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	owner := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	node := createSyncTransferTestNode(t, ctx, dbTx, owner, st.TreeNodeStatusAvailable)
	remoteTransfer := syncTransferRemoteTransfer(t, node, owner, receiver)
	remoteTransfer.GetLeaves()[0].GetLeaf().OwnerIdentityPublicKey = []byte("not-a-key")

	err = NewSspRequestHandler(&so.Config{Identifier: "test-operator"}).updateTransfer(
		ctx,
		remoteTransfer,
		&pbssp.SyncTransferRequest{OperatorId: "source-operator"},
	)
	require.Error(t, err)
	require.ErrorContains(t, err, "invalid remote owner identity public key")

	refreshedNode, err := dbTx.TreeNode.Get(ctx, node.ID)
	require.NoError(t, err)
	require.Equal(t, st.TreeNodeStatusAvailable, refreshedNode.Status)
	require.True(t, refreshedNode.OwnerIdentityPubkey.Equals(owner))
}

func TestUpdateTransferRejectsMalformedRemoteLeaf(t *testing.T) {
	testCases := []struct {
		name        string
		mutate      func(*pb.Transfer)
		errContains string
	}{
		{
			name: "nil transfer leaf",
			mutate: func(transfer *pb.Transfer) {
				transfer.Leaves = []*pb.TransferLeaf{nil}
			},
			errContains: "remote transfer leaf 0 is required",
		},
		{
			name: "missing tree node",
			mutate: func(transfer *pb.Transfer) {
				transfer.Leaves = []*pb.TransferLeaf{{Leaf: nil}}
			},
			errContains: "remote transfer leaf 0 is missing tree node",
		},
		{
			name: "missing tree node id",
			mutate: func(transfer *pb.Transfer) {
				transfer.Leaves = []*pb.TransferLeaf{{Leaf: &pb.TreeNode{}}}
			},
			errContains: "remote transfer leaf 0 is missing tree node ID",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := db.ConnectToTestPostgres(t)
			dbTx, err := ent.GetDbFromContext(ctx)
			require.NoError(t, err)

			owner := keys.GeneratePrivateKey().Public()
			receiver := keys.GeneratePrivateKey().Public()
			node := createSyncTransferTestNode(t, ctx, dbTx, owner, st.TreeNodeStatusAvailable)
			remoteTransfer := syncTransferRemoteTransfer(t, node, owner, receiver)
			tc.mutate(remoteTransfer)

			var updateErr error
			require.NotPanics(t, func() {
				updateErr = NewSspRequestHandler(&so.Config{Identifier: "test-operator"}).updateTransfer(
					ctx,
					remoteTransfer,
					&pbssp.SyncTransferRequest{OperatorId: "source-operator"},
				)
			})
			require.Error(t, updateErr)
			require.ErrorContains(t, updateErr, tc.errContains)

			refreshed, err := dbTx.TreeNode.Get(ctx, node.ID)
			require.NoError(t, err)
			require.Equal(t, st.TreeNodeStatusAvailable, refreshed.Status)
		})
	}
}

// A local transfer wrongly parked in a terminal status is one of the states
// sync_transfers exists to undo, so the sync must be able to move it back out.
func TestUpdateTransferSyncsTransferOutOfLocalTerminalStatus(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	owner := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	node := createSyncTransferTestNode(t, ctx, dbTx, owner, st.TreeNodeStatusAvailable)
	localTransfer := createSyncTransferLocalTransfer(t, ctx, dbTx, node, owner, receiver)
	localLeaf, err := dbTx.TransferLeaf.Create().
		SetTransfer(localTransfer).
		SetLeaf(node).
		SetPreviousRefundTx(createTestTxBytes(t, 3000)).
		SetIntermediateRefundTx(createTestTxBytes(t, 3001)).
		SetSecretCipher([]byte("local-secret")).
		SetSignature([]byte("local-signature")).
		Save(ctx)
	require.NoError(t, err)
	localTransfer, err = localTransfer.Update().SetStatus(st.TransferStatusReturned).Save(ctx)
	require.NoError(t, err)
	// The receiver row goes terminal alongside the transfer, and the
	// receiver-side pending query filters on it alone.
	_, err = dbTx.TransferReceiver.Update().
		Where(enttransferreceiver.TransferIDEQ(localTransfer.ID)).
		SetStatus(st.TransferReceiverStatusCancelled).
		Save(ctx)
	require.NoError(t, err)

	remoteTransfer := syncTransferRemoteTransfer(t, node, owner, receiver)
	remoteTransfer.Id = localTransfer.ID.String()
	remoteTransfer.Status = pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING
	remoteTransfer.Leaves[0].SecretCipher = []byte("remote-secret")
	remoteTransfer.Leaves[0].Sig = &pb.TransferLeaf_Signature{Signature: []byte("remote-signature")}

	err = NewSspRequestHandler(&so.Config{Identifier: "test-operator"}).updateTransfer(
		ctx,
		remoteTransfer,
		&pbssp.SyncTransferRequest{OperatorId: "source-operator"},
	)
	require.NoError(t, err)

	refreshedTransfer, err := dbTx.Transfer.Get(ctx, localTransfer.ID)
	require.NoError(t, err)
	require.Equal(t, st.TransferStatusSenderKeyTweakPending, refreshedTransfer.Status)

	refreshedReceivers, err := dbTx.TransferReceiver.Query().
		Where(enttransferreceiver.TransferIDEQ(localTransfer.ID)).
		All(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, refreshedReceivers)
	for _, r := range refreshedReceivers {
		require.Equal(t, st.TransferReceiverStatusInitiated, r.Status)
		require.True(t, r.CompletionTime.IsZero())
	}

	refreshedNode, err := dbTx.TreeNode.Get(ctx, node.ID)
	require.NoError(t, err)
	require.Equal(t, st.TreeNodeStatusTransferLocked, refreshedNode.Status)

	refreshedLeaf, err := dbTx.TransferLeaf.Get(ctx, localLeaf.ID)
	require.NoError(t, err)
	require.Equal(t, []byte("remote-secret"), refreshedLeaf.SecretCipher)
	require.Equal(t, []byte("remote-signature"), refreshedLeaf.Signature)
}

// Chain-derived and quarantine node statuses are owned by other flows; a
// terminal sync may only release the lock the transfer itself holds.
func TestUpdateTransferTerminalSyncReleasesOnlyTransferLockedNodes(t *testing.T) {
	testCases := []struct {
		name   string
		status st.TreeNodeStatus
	}{
		{name: "on chain", status: st.TreeNodeStatusOnChain},
		{name: "exited", status: st.TreeNodeStatusExited},
		{name: "lost", status: st.TreeNodeStatusLost},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := db.ConnectToTestPostgres(t)
			dbTx, err := ent.GetDbFromContext(ctx)
			require.NoError(t, err)

			owner := keys.GeneratePrivateKey().Public()
			receiver := keys.GeneratePrivateKey().Public()
			lockedNode := createSyncTransferTestNode(t, ctx, dbTx, owner, st.TreeNodeStatusTransferLocked)
			untouchableNode := createSyncTransferTestNode(t, ctx, dbTx, owner, tc.status)
			localTransfer := createSyncTransferLocalTransfer(t, ctx, dbTx, lockedNode, owner, receiver)
			for _, node := range []*ent.TreeNode{lockedNode, untouchableNode} {
				_, err = dbTx.TransferLeaf.Create().
					SetTransfer(localTransfer).
					SetLeaf(node).
					SetPreviousRefundTx(createTestTxBytes(t, 3000)).
					SetIntermediateRefundTx(createTestTxBytes(t, 3001)).
					SetSecretCipher([]byte("local-secret")).
					SetSignature([]byte("local-signature")).
					Save(ctx)
				require.NoError(t, err)
			}

			remoteTransfer := syncTransferRemoteTransfer(t, lockedNode, owner, receiver)
			remoteTransfer.Id = localTransfer.ID.String()
			remoteTransfer.Status = pb.TransferStatus_TRANSFER_STATUS_COMPLETED

			// The terminal branch ends by pulling fresh node bytes from the
			// source operator, which needs a live peer this harness does not
			// have. The status writes land first and are what this test pins.
			err = NewSspRequestHandler(&so.Config{Identifier: "test-operator"}).updateTransfer(
				ctx,
				remoteTransfer,
				&pbssp.SyncTransferRequest{OperatorId: "source-operator"},
			)
			require.Error(t, err)
			require.ErrorContains(t, err, "failed to sync tree nodes")

			refreshedLocked, err := dbTx.TreeNode.Get(ctx, lockedNode.ID)
			require.NoError(t, err)
			require.Equal(t, st.TreeNodeStatusAvailable, refreshedLocked.Status)

			refreshedUntouchable, err := dbTx.TreeNode.Get(ctx, untouchableNode.ID)
			require.NoError(t, err)
			require.Equal(t, tc.status, refreshedUntouchable.Status)
		})
	}
}

// EXPIRED is terminal but never reaches updateTransfer's RETURNED/COMPLETED
// branch, so getOrCreateLocalTransfer must carry the receiver rows itself.
func TestUpdateTransferSyncsExpiredTransferReceiversToCancelled(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	owner := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	node := createSyncTransferTestNode(t, ctx, dbTx, owner, st.TreeNodeStatusAvailable)
	localTransfer := createSyncTransferLocalTransfer(t, ctx, dbTx, node, owner, receiver)
	_, err = dbTx.TransferLeaf.Create().
		SetTransfer(localTransfer).
		SetLeaf(node).
		SetPreviousRefundTx(createTestTxBytes(t, 3000)).
		SetIntermediateRefundTx(createTestTxBytes(t, 3001)).
		SetSecretCipher([]byte("local-secret")).
		SetSignature([]byte("local-signature")).
		Save(ctx)
	require.NoError(t, err)

	remoteTransfer := syncTransferRemoteTransfer(t, node, owner, receiver)
	remoteTransfer.Id = localTransfer.ID.String()
	remoteTransfer.Status = pb.TransferStatus_TRANSFER_STATUS_EXPIRED

	err = NewSspRequestHandler(&so.Config{Identifier: "test-operator"}).updateTransfer(
		ctx,
		remoteTransfer,
		&pbssp.SyncTransferRequest{OperatorId: "source-operator"},
	)
	require.NoError(t, err)

	refreshedTransfer, err := dbTx.Transfer.Get(ctx, localTransfer.ID)
	require.NoError(t, err)
	require.Equal(t, st.TransferStatusExpired, refreshedTransfer.Status)

	refreshedReceivers, err := dbTx.TransferReceiver.Query().
		Where(enttransferreceiver.TransferIDEQ(localTransfer.ID)).
		All(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, refreshedReceivers)
	for _, r := range refreshedReceivers {
		require.Equal(t, st.TransferReceiverStatusCancelled, r.Status)
		require.True(t, r.CompletionTime.IsZero())
	}
}

func createSyncTransferTestNode(t *testing.T, ctx context.Context, dbTx *ent.Client, owner keys.Public, status st.TreeNodeStatus) *ent.TreeNode {
	t.Helper()

	tree, err := dbTx.Tree.Create().
		SetNetwork(btcnetwork.Regtest).
		SetOwnerIdentityPubkey(owner).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		SetStatus(st.TreeStatusAvailable).
		Save(ctx)
	require.NoError(t, err)

	secret := keys.GeneratePrivateKey()
	keyshare, err := dbTx.SigningKeyshare.Create().
		SetPublicShares(map[string]keys.Public{"key": secret.Public()}).
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secret).
		SetPublicKey(secret.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(1).
		Save(ctx)
	require.NoError(t, err)

	node, err := dbTx.TreeNode.Create().
		SetTree(tree).
		SetNetwork(tree.Network).
		SetValue(1000).
		SetStatus(status).
		SetVerifyingPubkey(keys.GeneratePrivateKey().Public()).
		SetOwnerIdentityPubkey(owner).
		SetOwnerSigningPubkey(keys.GeneratePrivateKey().Public()).
		SetRawTx(createTestTxBytesWithIndex(t, 1000, 0)).
		SetRawRefundTx(createTestTxBytes(t, 900)).
		SetVout(0).
		SetSigningKeyshare(keyshare).
		Save(ctx)
	require.NoError(t, err)
	return node
}

func createSyncTransferWatchtowerReadyTestNode(t *testing.T, ctx context.Context, dbTx *ent.Client, owner keys.Public, status st.TreeNodeStatus) *ent.TreeNode {
	t.Helper()

	node := createSyncTransferTestNode(t, ctx, dbTx, owner, status)
	nodeTx, err := common.TxFromRawTxBytes(node.RawTx)
	require.NoError(t, err)
	directTx := createTestTxBytesWithIndex(t, int64(node.Value), 0)
	parsedDirectTx, err := common.TxFromRawTxBytes(directTx)
	require.NoError(t, err)
	rawRefundTx := createVersion3CPFPRefundTx(t, nodeTx.TxHash(), uint32(node.Vout), node.OwnerSigningPubkey, int64(node.Value), sparkconst.InitialTimeLock)
	directRefundTx := createVersion3DirectRefundTx(
		t,
		parsedDirectTx.TxHash(),
		0,
		node.OwnerSigningPubkey,
		int64(node.Value),
		sparkconst.InitialTimeLock-sparkconst.TimeLockInterval+sparkconst.DirectTimelockOffset,
	)
	directFromCpfpRefundTx := createVersion3DirectRefundTx(
		t,
		nodeTx.TxHash(),
		uint32(node.Vout),
		node.OwnerSigningPubkey,
		int64(node.Value),
		sparkconst.InitialTimeLock-sparkconst.TimeLockInterval+sparkconst.DirectTimelockOffset,
	)

	node, err = node.Update().
		SetRawRefundTx(rawRefundTx).
		SetDirectTx(directTx).
		SetDirectRefundTx(directRefundTx).
		SetDirectFromCpfpRefundTx(directFromCpfpRefundTx).
		Save(ctx)
	require.NoError(t, err)
	return node
}

func createSyncTransferLocalTransfer(
	t *testing.T,
	ctx context.Context,
	dbTx *ent.Client,
	node *ent.TreeNode,
	sender keys.Public,
	receiver keys.Public,
) *ent.Transfer {
	t.Helper()

	transfer, err := dbTx.Transfer.Create().
		SetID(uuid.New()).
		SetNetwork(btcnetwork.Regtest).
		SetType(st.TransferTypeTransfer).
		SetStatus(st.TransferStatusSenderKeyTweakPending).
		SetTotalValue(node.Value).
		SetExpiryTime(time.Now().Add(time.Hour)).
		SetSenderIdentityPubkey(sender).
		SetReceiverIdentityPubkey(receiver).
		Save(ctx)
	require.NoError(t, err)

	_, err = dbTx.TransferSender.Create().
		SetTransfer(transfer).
		SetIdentityPubkey(sender).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	_, err = dbTx.TransferReceiver.Create().
		SetTransfer(transfer).
		SetIdentityPubkey(receiver).
		SetStatus(st.TransferReceiverStatusInitiated).
		SetTransferType(transfer.Type).
		Save(ctx)
	require.NoError(t, err)

	return transfer
}

func syncTransferRemoteTransfer(t *testing.T, node *ent.TreeNode, sender keys.Public, receiver keys.Public) *pb.Transfer {
	t.Helper()

	return &pb.Transfer{
		Id:                        uuid.NewString(),
		Network:                   pb.Network_REGTEST,
		Type:                      pb.TransferType_TRANSFER,
		Status:                    pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING,
		TotalValue:                node.Value,
		ExpiryTime:                timestamppb.New(time.Now().Add(time.Hour)),
		SenderIdentityPublicKey:   sender.Serialize(),
		ReceiverIdentityPublicKey: receiver.Serialize(),
		Leaves: []*pb.TransferLeaf{{
			Leaf: &pb.TreeNode{
				Id:                     node.ID.String(),
				OwnerIdentityPublicKey: sender.Serialize(),
				OwnerSigningPublicKey:  node.OwnerSigningPubkey.Serialize(),
			},
			SecretCipher:         []byte("remote-secret"),
			Sig:                  &pb.TransferLeaf_Signature{Signature: []byte("remote-signature")},
			IntermediateRefundTx: createTestTxBytes(t, 901),
		}},
	}
}

func setSyncTransferRemoteCpfpRefund(t *testing.T, remoteTransfer *pb.Transfer, node *ent.TreeNode, receiver keys.Public) {
	t.Helper()

	require.Len(t, remoteTransfer.GetLeaves(), 1)
	nodeTx, err := common.TxFromRawTxBytes(node.RawTx)
	require.NoError(t, err)
	remoteTransfer.GetLeaves()[0].IntermediateRefundTx = createVersion3CPFPRefundTx(
		t,
		nodeTx.TxHash(),
		uint32(node.Vout),
		receiver,
		int64(node.Value),
		sparkconst.InitialTimeLock-sparkconst.TimeLockInterval,
	)
}

func addSyncTransferDirectRefunds(t *testing.T, remoteTransfer *pb.Transfer, node *ent.TreeNode, receiver keys.Public) {
	t.Helper()

	setSyncTransferRemoteCpfpRefund(t, remoteTransfer, node, receiver)
	nodeTx, err := common.TxFromRawTxBytes(node.RawTx)
	require.NoError(t, err)
	directTx, err := common.TxFromRawTxBytes(node.DirectTx)
	require.NoError(t, err)

	remoteLeaf := remoteTransfer.GetLeaves()[0]
	remoteLeaf.IntermediateDirectRefundTx = createVersion3DirectRefundTx(
		t,
		directTx.TxHash(),
		0,
		receiver,
		int64(node.Value),
		sparkconst.InitialTimeLock-sparkconst.TimeLockInterval+sparkconst.DirectTimelockOffset,
	)
	remoteLeaf.IntermediateDirectFromCpfpRefundTx = createVersion3DirectRefundTx(
		t,
		nodeTx.TxHash(),
		uint32(node.Vout),
		receiver,
		int64(node.Value),
		sparkconst.InitialTimeLock-sparkconst.TimeLockInterval+sparkconst.DirectTimelockOffset,
	)
}
