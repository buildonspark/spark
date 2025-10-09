package handler

import (
	"context"
	"encoding/hex"
	"math/rand/v2"
	"testing"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNewFinalizeSignatureHandler(t *testing.T) {
	t.Parallel()
	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	handler := NewFinalizeSignatureHandler(config)

	assert.NotNil(t, handler)
	assert.Equal(t, config, handler.config)
}

func TestFinalizeSignatureHandler_FinalizeNodeSignatures_EmptyRequest(t *testing.T) {
	t.Parallel()
	ctx, _ := db.NewTestSQLiteContext(t)

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	handler := NewFinalizeSignatureHandler(config)

	req := &pb.FinalizeNodeSignaturesRequest{
		NodeSignatures: []*pb.NodeSignatures{},
		Intent:         pbcommon.SignatureIntent_CREATION,
	}

	resp, err := handler.FinalizeNodeSignatures(ctx, req)
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Empty(t, resp.Nodes)
}

func TestFinalizeSignatureHandler_FinalizeNodeSignaturesV2_EmptyRequest(t *testing.T) {
	t.Parallel()
	ctx, _ := db.NewTestSQLiteContext(t)

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	handler := NewFinalizeSignatureHandler(config)

	req := &pb.FinalizeNodeSignaturesRequest{
		NodeSignatures: []*pb.NodeSignatures{},
		Intent:         pbcommon.SignatureIntent_CREATION,
	}

	resp, err := handler.FinalizeNodeSignaturesV2(ctx, req)
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Empty(t, resp.Nodes)
}

func TestFinalizeSignatureHandler_ErrorCases(t *testing.T) {
	tests := []struct {
		name          string
		setupFunc     func(t *testing.T, ctx context.Context, handler *FinalizeSignatureHandler) any
		verifyFunc    func(t *testing.T, ctx context.Context, handler *FinalizeSignatureHandler, input any) error
		expectedError string
	}{
		{
			name: "FinalizeNodeSignatures_InvalidNodeID",
			setupFunc: func(t *testing.T, ctx context.Context, handler *FinalizeSignatureHandler) any {
				return &pb.FinalizeNodeSignaturesRequest{
					NodeSignatures: []*pb.NodeSignatures{
						{NodeId: "invalid-uuid"},
					},
					Intent: pbcommon.SignatureIntent_CREATION,
				}
			},
			verifyFunc: func(t *testing.T, ctx context.Context, handler *FinalizeSignatureHandler, input any) error {
				require.IsType(t, &pb.FinalizeNodeSignaturesRequest{}, input)
				req := input.(*pb.FinalizeNodeSignaturesRequest)
				resp, err := handler.FinalizeNodeSignatures(ctx, req)
				assert.Nil(t, resp)
				return err
			},
			expectedError: "invalid node id",
		},
		{
			name: "FinalizeNodeSignatures_NodeNotFound",
			setupFunc: func(t *testing.T, ctx context.Context, handler *FinalizeSignatureHandler) any {
				nodeID := uuid.New()
				return &pb.FinalizeNodeSignaturesRequest{
					NodeSignatures: []*pb.NodeSignatures{
						{NodeId: nodeID.String()},
					},
					Intent: pbcommon.SignatureIntent_CREATION,
				}
			},
			verifyFunc: func(t *testing.T, ctx context.Context, handler *FinalizeSignatureHandler, input any) error {
				req := input.(*pb.FinalizeNodeSignaturesRequest)
				resp, err := handler.FinalizeNodeSignatures(ctx, req)
				assert.Nil(t, resp)
				return err
			},
			expectedError: "failed to get first node",
		},
		{
			name: "VerifyAndUpdateTransfer_NoTransferFound",
			setupFunc: func(t *testing.T, ctx context.Context, handler *FinalizeSignatureHandler) any {
				nodeID := uuid.New()
				return &pb.FinalizeNodeSignaturesRequest{
					NodeSignatures: []*pb.NodeSignatures{
						{NodeId: nodeID.String()},
					},
					Intent: pbcommon.SignatureIntent_TRANSFER,
				}
			},
			verifyFunc: func(t *testing.T, ctx context.Context, handler *FinalizeSignatureHandler, input any) error {
				req := input.(*pb.FinalizeNodeSignaturesRequest)
				transfer, err := handler.verifyAndUpdateTransfer(ctx, req)
				assert.Nil(t, transfer)
				return err
			},
			expectedError: "failed to find pending transfer",
		},
		{
			name: "UpdateNode_InvalidNodeID",
			setupFunc: func(t *testing.T, ctx context.Context, handler *FinalizeSignatureHandler) any {
				return &pb.NodeSignatures{NodeId: "invalid-uuid"}
			},
			verifyFunc: func(t *testing.T, ctx context.Context, handler *FinalizeSignatureHandler, input any) error {
				nodeSignatures := input.(*pb.NodeSignatures)
				sparkNode, internalNode, err := handler.updateNode(ctx, nodeSignatures, pbcommon.SignatureIntent_CREATION, false)
				assert.Nil(t, sparkNode)
				assert.Nil(t, internalNode)
				return err
			},
			expectedError: "invalid node id",
		},
		{
			name: "UpdateNode_NodeNotFound",
			setupFunc: func(t *testing.T, ctx context.Context, handler *FinalizeSignatureHandler) any {
				nodeID := uuid.New()
				return &pb.NodeSignatures{NodeId: nodeID.String()}
			},
			verifyFunc: func(t *testing.T, ctx context.Context, handler *FinalizeSignatureHandler, input any) error {
				nodeSignatures := input.(*pb.NodeSignatures)
				sparkNode, internalNode, err := handler.updateNode(ctx, nodeSignatures, pbcommon.SignatureIntent_CREATION, false)
				assert.Nil(t, sparkNode)
				assert.Nil(t, internalNode)
				return err
			},
			expectedError: "failed to get node",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, _ := db.NewTestSQLiteContext(t)

			config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
			handler := NewFinalizeSignatureHandler(config)

			input := tt.setupFunc(t, ctx, handler)
			err := tt.verifyFunc(t, ctx, handler, input)

			require.ErrorContains(t, err, tt.expectedError)
		})
	}
}

func createTestTree(t *testing.T, ctx context.Context, network st.Network, status st.TreeStatus) (*ent.Tree, *ent.TreeNode) {
	dbTX, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	testID := uuid.New()
	baseTxid := []byte("base_txid_" + testID.String())
	rng := rand.NewChaCha8([32]byte{})
	ownerIdentity := keys.MustGeneratePrivateKeyFromRand(rng)
	verifyingPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerSigningKey := keys.MustGeneratePrivateKeyFromRand(rng)
	secretShare := keys.MustGeneratePrivateKeyFromRand(rng)
	publicShare1 := keys.MustGeneratePrivateKeyFromRand(rng)
	publicShare2 := keys.MustGeneratePrivateKeyFromRand(rng)
	publicShare3 := keys.MustGeneratePrivateKeyFromRand(rng)

	tree, err := dbTX.Tree.Create().
		SetID(uuid.New()).
		SetNetwork(network).
		SetStatus(status).
		SetBaseTxid(baseTxid).
		SetVout(0).
		SetOwnerIdentityPubkey(ownerIdentity.Public()).
		Save(ctx)
	require.NoError(t, err)

	keyshare, err := dbTX.SigningKeyshare.Create().
		SetID(uuid.New()).
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secretShare).
		SetPublicShares(map[string]keys.Public{"1": publicShare1.Public(), "2": publicShare2.Public(), "3": publicShare3.Public()}).
		SetPublicKey(secretShare.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	exampleTxString := "03000000000101d8966edeae1a3a05d0e5a3c971bb0a1b99bb901e76863812a40ea61fc60b87a000000000006c0700400214470000000000002251206b631936db9ab75c98e13235462f902944d9d81a45e3041bacaeec957bf7eeb700000000000000000451024e730140e06339a1f987b228843cf20f462f991264f89ca54c531c1c14d0df937d80acfd2ed9c626c6ad95106f3c9d90bc1de92b3d24aa89f03dd21974bb406e47ac84b000000000"
	nodeRawTx, err := hex.DecodeString(exampleTxString)
	require.NoError(t, err)
	nodeRawRefundTx, err := hex.DecodeString(exampleTxString)
	require.NoError(t, err)
	node, err := dbTX.TreeNode.Create().
		SetID(uuid.New()).
		SetTree(tree).
		SetSigningKeyshare(keyshare).
		SetValue(1000).
		SetVerifyingPubkey(verifyingPrivKey.Public()).
		SetOwnerIdentityPubkey(ownerIdentity.Public()).
		SetOwnerSigningPubkey(ownerSigningKey.Public()).
		SetRawTx(nodeRawTx).
		SetRawRefundTx(nodeRawRefundTx).
		SetVout(0).
		SetStatus(st.TreeNodeStatusCreating).
		Save(ctx)
	require.NoError(t, err)

	return tree, node
}

func TestFinalizeSignatureHandler_FinalizeNodeSignatures_InvalidIntent(t *testing.T) {
	t.Parallel()
	ctx, _ := db.NewTestSQLiteContext(t)

	config := &so.Config{
		SigningOperatorMap: map[string]*so.SigningOperator{
			"test-operator": {
				ID:         0,
				Identifier: "test-operator",
				AddressRpc: "localhost:8080",
				AddressDkg: "localhost:8081",
			},
		},
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	}
	handler := NewFinalizeSignatureHandler(config)

	_, node := createTestTree(t, ctx, st.NetworkRegtest, st.TreeStatusAvailable)

	req := &pb.FinalizeNodeSignaturesRequest{
		NodeSignatures: []*pb.NodeSignatures{
			{
				NodeId: node.ID.String(),
			},
		},
		Intent: pbcommon.SignatureIntent(999),
	}

	resp, err := handler.FinalizeNodeSignatures(ctx, req)

	require.ErrorContains(t, err, "invalid intent")
	assert.Nil(t, resp)
}

func TestFinalizeSignatureHandler_FinalizeNodeSignatures_EmptyOperatorsMap(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	config := &so.Config{
		SigningOperatorMap: map[string]*so.SigningOperator{},
	}
	handler := NewFinalizeSignatureHandler(config)

	_, node := createTestTree(t, ctx, st.NetworkRegtest, st.TreeStatusAvailable)

	req := &pb.FinalizeNodeSignaturesRequest{
		NodeSignatures: []*pb.NodeSignatures{
			{
				NodeId: node.ID.String(),
			},
		},
		Intent: pbcommon.SignatureIntent(999),
	}

	resp, err := handler.FinalizeNodeSignatures(ctx, req)

	require.ErrorContains(t, err, "no signing operators configured")
	assert.Nil(t, resp)
}

func TestFinalizeSignatureHandler_FinalizeNodeSignaturesV2_RequireDirectTx(t *testing.T) {
	t.Parallel()
	ctx, _ := db.NewTestSQLiteContext(t)

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	handler := NewFinalizeSignatureHandler(config)

	req := &pb.FinalizeNodeSignaturesRequest{
		NodeSignatures: []*pb.NodeSignatures{},
		Intent:         pbcommon.SignatureIntent_CREATION,
	}

	resp, err := handler.FinalizeNodeSignaturesV2(ctx, req)
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Empty(t, resp.Nodes)
}

// Test that nodes with children are not set to Available status even with refund tx
// Regression test for https://linear.app/lightsparkdev/issue/LIG-8094
func TestFinalizeSignatureHandler_UpdateNode_NodeWithChildrenStatus(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{})

	config := &so.Config{}
	handler := NewFinalizeSignatureHandler(config)

	tree, parentNode := createTestTree(t, ctx, st.NetworkRegtest, st.TreeStatusAvailable)

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	keyshare, err := parentNode.QuerySigningKeyshare().Only(ctx)
	require.NoError(t, err)

	childVerifyingKey := keys.MustGeneratePrivateKeyFromRand(rng)
	childOwnerIdentity := keys.MustGeneratePrivateKeyFromRand(rng)
	childOwnerSigning := keys.MustGeneratePrivateKeyFromRand(rng)

	rawTx := createTestTxBytesWithIndex(t, 500, 0)
	childNode, err := dbTx.TreeNode.Create().
		SetID(uuid.New()).
		SetTree(tree).
		SetSigningKeyshare(keyshare).
		SetParent(parentNode).
		SetValue(500).
		SetVerifyingPubkey(childVerifyingKey.Public()).
		SetOwnerIdentityPubkey(childOwnerIdentity.Public()).
		SetOwnerSigningPubkey(childOwnerSigning.Public()).
		SetRawTx(rawTx).
		SetRawRefundTx(rawTx).
		SetVout(0).
		SetStatus(st.TreeNodeStatusCreating).
		Save(ctx)
	require.NoError(t, err)

	// Test case 1: Node with children and refund tx should be set to Splitted, not Available
	// Use TRANSFER intent to skip node tx signature validation while still testing status logic
	nodeSignatures := &pb.NodeSignatures{
		NodeId: parentNode.ID.String(),
		// No signatures provided to avoid validation errors
	}

	sparkNode, internalNode, err := handler.updateNode(ctx, nodeSignatures, pbcommon.SignatureIntent_TRANSFER, false)
	require.NoError(t, err)
	assert.NotNil(t, sparkNode)
	assert.NotNil(t, internalNode)

	// Verify that parent node status is Splitted because it has children
	updatedParent, err := dbTx.TreeNode.Query().
		Where(treenode.IDEQ(parentNode.ID)).
		WithChildren().
		Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusSplitted, updatedParent.Status, "Node with children should be set to Splitted status")

	// Test case 2: Child node without children and with refund tx should be set to Available
	childNodeSignatures := &pb.NodeSignatures{
		NodeId: childNode.ID.String(),
		// No signatures provided to avoid validation errors
	}

	childSparkNode, childInternalNode, err := handler.updateNode(ctx, childNodeSignatures, pbcommon.SignatureIntent_TRANSFER, false)
	require.NoError(t, err)
	assert.NotNil(t, childSparkNode)
	assert.NotNil(t, childInternalNode)

	// Verify that child node status is Available because it has no children and has refund tx
	updatedChild, err := dbTx.TreeNode.Query().
		Where(func(s *sql.Selector) {
			s.Where(sql.EQ("id", childNode.ID))
		}).
		WithChildren().
		Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusAvailable, updatedChild.Status, "Node without children and with refund tx should be set to Available status")
}

// Test that nodes without refund tx are set to Splitted regardless of children
func TestFinalizeSignatureHandler_UpdateNode_NodeWithoutRefundTxStatus(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	config := &so.Config{}
	handler := NewFinalizeSignatureHandler(config)

	_, leafNode := createTestTree(t, ctx, st.NetworkRegtest, st.TreeStatusAvailable)

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	leafNode, err = leafNode.Update().
		ClearRawRefundTx().
		Save(ctx)
	require.NoError(t, err)

	// Test: Node without refund tx should be set to Splitted
	nodeSignatures := &pb.NodeSignatures{
		NodeId: leafNode.ID.String(),
		// No RefundTxSignature provided
	}

	sparkNode, internalNode, err := handler.updateNode(ctx, nodeSignatures, pbcommon.SignatureIntent_TRANSFER, false)
	require.NoError(t, err)
	assert.NotNil(t, sparkNode)
	assert.NotNil(t, internalNode)

	// Verify that node status is Splitted because it has no refund tx
	updatedNode, err := dbTx.TreeNode.Get(ctx, leafNode.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusSplitted, updatedNode.Status, "Node without refund tx should be set to Splitted status")
}

// Regression test for https://linear.app/lightsparkdev/issue/LIG-8094
func TestFinalizeSignatureHandler_UpdateNode_LoadsChildrenRelationships(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	config := &so.Config{}
	handler := NewFinalizeSignatureHandler(config)

	tree, parentNode := createTestTree(t, ctx, st.NetworkRegtest, st.TreeStatusAvailable)
	rng := rand.NewChaCha8([32]byte{})
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	keyshare, err := parentNode.QuerySigningKeyshare().Only(ctx)
	require.NoError(t, err)

	child1VerifyingKey := keys.MustGeneratePrivateKeyFromRand(rng)
	child1OwnerIdentity := keys.MustGeneratePrivateKeyFromRand(rng)
	child1OwnerSigning := keys.MustGeneratePrivateKeyFromRand(rng)

	rawTx1 := createTestTxBytesWithIndex(t, 250, 0)
	child1, err := dbTx.TreeNode.Create().
		SetID(uuid.New()).
		SetTree(tree).
		SetSigningKeyshare(keyshare).
		SetParent(parentNode).
		SetValue(250).
		SetVerifyingPubkey(child1VerifyingKey.Public()).
		SetOwnerIdentityPubkey(child1OwnerIdentity.Public()).
		SetOwnerSigningPubkey(child1OwnerSigning.Public()).
		SetRawTx(rawTx1).
		SetRawRefundTx(rawTx1).
		SetVout(0).
		SetStatus(st.TreeNodeStatusCreating).
		Save(ctx)
	require.NoError(t, err)

	child2VerifyingKey := keys.MustGeneratePrivateKeyFromRand(rng)
	child2OwnerIdentity := keys.MustGeneratePrivateKeyFromRand(rng)
	child2OwnerSigning := keys.MustGeneratePrivateKeyFromRand(rng)

	rawTx2 := createTestTxBytesWithIndex(t, 250, 0)
	child2, err := dbTx.TreeNode.Create().
		SetID(uuid.New()).
		SetTree(tree).
		SetSigningKeyshare(keyshare).
		SetParent(parentNode).
		SetValue(250).
		SetVerifyingPubkey(child2VerifyingKey.Public()).
		SetOwnerIdentityPubkey(child2OwnerIdentity.Public()).
		SetOwnerSigningPubkey(child2OwnerSigning.Public()).
		SetRawTx(rawTx2).
		SetRawRefundTx(rawTx2).
		SetVout(0).
		SetStatus(st.TreeNodeStatusCreating).
		Save(ctx)
	require.NoError(t, err)

	// Test that updateNode correctly loads and considers children
	nodeSignatures := &pb.NodeSignatures{
		NodeId: parentNode.ID.String(),
		// No signatures provided to avoid validation errors
	}

	sparkNode, internalNode, err := handler.updateNode(ctx, nodeSignatures, pbcommon.SignatureIntent_TRANSFER, false)
	require.NoError(t, err)
	assert.NotNil(t, sparkNode)
	assert.NotNil(t, internalNode)

	// Verify that parent node with 2 children is set to Splitted
	updatedParent, err := dbTx.TreeNode.Query().
		Where(func(s *sql.Selector) {
			s.Where(sql.EQ("id", parentNode.ID))
		}).
		WithChildren().
		Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusSplitted, updatedParent.Status, "Node with children should be set to Splitted status")
	assert.Len(t, updatedParent.Edges.Children, 2, "Parent should have 2 children loaded")

	childIDs := make([]uuid.UUID, len(updatedParent.Edges.Children))
	for i, child := range updatedParent.Edges.Children {
		childIDs[i] = child.ID
	}
	assert.Contains(t, childIDs, child1.ID)
	assert.Contains(t, childIDs, child2.ID)
}

// Test edge case: Tree not in Available status should not trigger status logic
func TestFinalizeSignatureHandler_UpdateNode_TreeNotAvailableStatus(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	config := &so.Config{}
	handler := NewFinalizeSignatureHandler(config)

	// Create a tree with Pending status (not Available)
	_, leafNode := createTestTree(t, ctx, st.NetworkRegtest, st.TreeStatusPending)

	// Test: Node in non-Available tree should not have its status changed by the children logic
	nodeSignatures := &pb.NodeSignatures{
		NodeId: leafNode.ID.String(),
		// No signatures provided to avoid validation errors
	}

	sparkNode, internalNode, err := handler.updateNode(ctx, nodeSignatures, pbcommon.SignatureIntent_TRANSFER, false)
	require.NoError(t, err)
	assert.NotNil(t, sparkNode)
	assert.NotNil(t, internalNode)

	// Verify that node status remains unchanged (Creating) because tree is not Available
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updatedNode, err := dbTx.TreeNode.Get(ctx, leafNode.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TreeNodeStatusCreating, updatedNode.Status, "Node status should remain unchanged when tree is not Available")
}

// Regression test for https://linear.app/lightsparkdev/issue/LIG-8045
func TestConfirmTreeWithNonRootConfirmation(t *testing.T) {
	t.Parallel()
	rng := rand.NewChaCha8([32]byte{})
	ctx, _ := db.NewTestSQLiteContext(t)

	config := &so.Config{
		SigningOperatorMap: map[string]*so.SigningOperator{
			"test-operator": {
				ID:         0,
				Identifier: "test-operator",
				AddressRpc: "localhost:8080",
				AddressDkg: "localhost:8081",
			},
		},
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	}
	handler := NewFinalizeSignatureHandler(config)

	// Create a tree in a not-yet-finalized (PENDING) state
	tree, rootNode := createTestTree(t, ctx, st.NetworkRegtest, st.TreeStatusPending)

	dbTX, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	keyshare, err := rootNode.QuerySigningKeyshare().Only(ctx)
	require.NoError(t, err)

	testID := uuid.Must(uuid.NewRandomFromReader(rng)).String()

	// Create a child node in the tree - this represents a non-root node
	// that can receive deposits independently of the root node
	childVerifyingKey := keys.MustGeneratePrivateKeyFromRand(rng)
	childOwnerIdentity := keys.MustGeneratePrivateKeyFromRand(rng)
	childOwnerSigning := keys.MustGeneratePrivateKeyFromRand(rng)

	rawTx := createTestTxBytesWithIndex(t, 65536, 0)
	childNode, err := dbTX.TreeNode.Create().
		SetID(uuid.New()).
		SetTree(tree).
		SetSigningKeyshare(keyshare).
		SetValue(65536).
		SetVerifyingPubkey(childVerifyingKey.Public()).
		SetOwnerIdentityPubkey(childOwnerIdentity.Public()).
		SetOwnerSigningPubkey(childOwnerSigning.Public()).
		SetRawTx(rawTx).
		SetRawRefundTx(rawTx).
		SetVout(0).
		SetStatus(st.TreeNodeStatusCreating).
		Save(ctx)
	require.NoError(t, err)

	// Create a deposit address for the child node - this simulates the scenario
	// where a user deposits to a non-root node's address instead of the tree's root
	depositAddress, err := dbTX.DepositAddress.Create().
		SetID(uuid.New()).
		SetAddress("child_deposit_address_" + testID).
		SetOwnerIdentityPubkey(childOwnerIdentity.Public()).
		SetOwnerSigningPubkey(childOwnerSigning.Public()).
		SetConfirmationHeight(100).
		// This txid is different from the tree's base txid, which is the core of the issue.
		SetConfirmationTxid("other_non_root_deposit_txid_" + testID).
		SetSigningKeyshare(keyshare).
		SetNetwork(st.NetworkRegtest).
		Save(ctx)
	require.NoError(t, err)

	// Create a UTXO that represents the actual Bitcoin transaction
	// confirming the deposit to the child node's address
	_, err = dbTX.Utxo.Create().
		SetID(uuid.New()).
		SetBlockHeight(100).
		// The actual transaction ID of the deposit is different from tree base txid
		SetTxid([]byte("non_root_deposit_txid_" + testID)).
		SetVout(0).
		SetAmount(65536).
		SetNetwork(st.NetworkRegtest).
		SetPkScript([]byte("pk_script_" + testID)).
		SetDepositAddress(depositAddress).
		Save(ctx)
	require.NoError(t, err)

	// This creates the mismatch that triggers the old bug path: the tree's base
	// txid is "non_root_deposit_txid" but the deposit address confirmation txid
	// is "other_non_root_deposit_txid"
	_, err = tree.Update().
		SetBaseTxid([]byte("non_root_deposit_txid_" + testID)).
		Save(ctx)
	require.NoError(t, err)

	// Create a block height record for the regtest network
	_, err = dbTX.BlockHeight.Create().
		SetID(uuid.New()).
		SetNetwork(st.NetworkRegtest).
		SetHeight(103).
		Save(ctx)
	require.NoError(t, err)

	req := &pb.FinalizeNodeSignaturesRequest{
		NodeSignatures: []*pb.NodeSignatures{
			{NodeId: rootNode.ID.String()},
			{NodeId: childNode.ID.String()},
		},
		Intent: pbcommon.SignatureIntent_CREATION,
	}

	_, err = handler.FinalizeNodeSignatures(ctx, req)
	require.ErrorContains(t, err, "confirmation txid does not match tree base txid")
}

// Test that trees with < 3 confirmations cannot be finalized
func TestFinalizeTreeWithInsufficientConfirmations(t *testing.T) {
	t.Parallel()
	rng := rand.NewChaCha8([32]byte{})
	ctx, _ := db.NewTestSQLiteContext(t)

	config := &so.Config{
		SigningOperatorMap: map[string]*so.SigningOperator{
			"test-operator": {
				ID:         0,
				Identifier: "test-operator",
				AddressRpc: "localhost:8080",
				AddressDkg: "localhost:8081",
			},
		},
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	}
	handler := NewFinalizeSignatureHandler(config)

	// Create a tree in a not-yet-finalized (PENDING) state
	tree, rootNode := createTestTree(t, ctx, st.NetworkRegtest, st.TreeStatusPending)

	dbTX, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	keyshare, err := rootNode.QuerySigningKeyshare().Only(ctx)
	require.NoError(t, err)

	testID := uuid.Must(uuid.NewRandomFromReader(rng)).String()

	// Create a child node in the tree - this represents a non-root node
	// that can receive deposits independently of the root node
	childVerifyingKey := keys.MustGeneratePrivateKeyFromRand(rng)
	childOwnerIdentity := keys.MustGeneratePrivateKeyFromRand(rng)
	childOwnerSigning := keys.MustGeneratePrivateKeyFromRand(rng)

	rawTx := createTestTxBytesWithIndex(t, 65536, 0)
	childNode, err := dbTX.TreeNode.Create().
		SetID(uuid.New()).
		SetTree(tree).
		SetSigningKeyshare(keyshare).
		SetValue(65536).
		SetVerifyingPubkey(childVerifyingKey.Public()).
		SetOwnerIdentityPubkey(childOwnerIdentity.Public()).
		SetOwnerSigningPubkey(childOwnerSigning.Public()).
		SetRawTx(rawTx).
		SetRawRefundTx(rawTx).
		SetVout(0).
		SetStatus(st.TreeNodeStatusCreating).
		Save(ctx)
	require.NoError(t, err)

	// Create a deposit address for the child node
	depositAddress, err := dbTX.DepositAddress.Create().
		SetID(uuid.New()).
		SetAddress("child_deposit_address_" + testID).
		SetOwnerIdentityPubkey(childOwnerIdentity.Public()).
		SetOwnerSigningPubkey(childOwnerSigning.Public()).
		SetConfirmationHeight(100).
		SetConfirmationTxid("deposit_txid_" + testID).
		SetSigningKeyshare(keyshare).
		SetNetwork(st.NetworkRegtest).
		Save(ctx)
	require.NoError(t, err)

	// Create a UTXO that represents the actual Bitcoin transaction
	// confirming the deposit to the child node's address
	_, err = dbTX.Utxo.Create().
		SetID(uuid.New()).
		SetBlockHeight(100).
		SetTxid([]byte("deposit_txid_" + testID)).
		SetVout(0).
		SetAmount(65536).
		SetNetwork(st.NetworkRegtest).
		SetPkScript([]byte("pk_script_" + testID)).
		SetDepositAddress(depositAddress).
		Save(ctx)
	require.NoError(t, err)

	_, err = tree.Update().
		SetBaseTxid([]byte("deposit_txid_" + testID)).
		Save(ctx)
	require.NoError(t, err)

	// Create a block height record for the regtest network at 102
	// This gives only 2 confirmations (102 - 100 = 2), which is less than the required 3
	_, err = dbTX.BlockHeight.Create().
		SetID(uuid.New()).
		SetNetwork(st.NetworkRegtest).
		SetHeight(102).
		Save(ctx)
	require.NoError(t, err)

	req := &pb.FinalizeNodeSignaturesRequest{
		NodeSignatures: []*pb.NodeSignatures{
			{NodeId: rootNode.ID.String()},
			{NodeId: childNode.ID.String()},
		},
		Intent: pbcommon.SignatureIntent_CREATION,
	}

	_, err = handler.FinalizeNodeSignatures(ctx, req)
	require.Error(t, err, "should fail with insufficient confirmations")
	require.ErrorContains(t, err, "expected at least")

	// Check that the error has the correct gRPC status code
	grpcError, ok := status.FromError(err)
	require.True(t, ok, "error should be a gRPC status error")
	assert.Equal(t, codes.FailedPrecondition, grpcError.Code(), "error should have FailedPrecondition status code (9)")
}

// Test that trees cannot be finalized when no block height is present in db
func TestFinalizeTreeWithNoBlockHeight(t *testing.T) {
	t.Parallel()
	rng := rand.NewChaCha8([32]byte{})
	ctx, _ := db.NewTestSQLiteContext(t)

	config := &so.Config{
		SigningOperatorMap: map[string]*so.SigningOperator{
			"test-operator": {
				ID:         0,
				Identifier: "test-operator",
				AddressRpc: "localhost:8080",
				AddressDkg: "localhost:8081",
			},
		},
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	}
	handler := NewFinalizeSignatureHandler(config)

	// Create a tree in a not-yet-finalized (PENDING) state
	tree, rootNode := createTestTree(t, ctx, st.NetworkRegtest, st.TreeStatusPending)

	dbTX, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	keyshare, err := rootNode.QuerySigningKeyshare().Only(ctx)
	require.NoError(t, err)

	testID := uuid.Must(uuid.NewRandomFromReader(rng)).String()

	// Create a child node in the tree - this represents a non-root node
	// that can receive deposits independently of the root node
	childVerifyingKey := keys.MustGeneratePrivateKeyFromRand(rng)
	childOwnerIdentity := keys.MustGeneratePrivateKeyFromRand(rng)
	childOwnerSigning := keys.MustGeneratePrivateKeyFromRand(rng)

	rawTx := createTestTxBytesWithIndex(t, 65536, 0)
	childNode, err := dbTX.TreeNode.Create().
		SetID(uuid.New()).
		SetTree(tree).
		SetSigningKeyshare(keyshare).
		SetValue(65536).
		SetVerifyingPubkey(childVerifyingKey.Public()).
		SetOwnerIdentityPubkey(childOwnerIdentity.Public()).
		SetOwnerSigningPubkey(childOwnerSigning.Public()).
		SetRawTx(rawTx).
		SetRawRefundTx(rawTx).
		SetVout(0).
		SetStatus(st.TreeNodeStatusCreating).
		Save(ctx)
	require.NoError(t, err)

	// Create a deposit address for the child node
	depositAddress, err := dbTX.DepositAddress.Create().
		SetID(uuid.New()).
		SetAddress("child_deposit_address_" + testID).
		SetOwnerIdentityPubkey(childOwnerIdentity.Public()).
		SetOwnerSigningPubkey(childOwnerSigning.Public()).
		SetConfirmationHeight(100).
		SetConfirmationTxid("deposit_txid_" + testID).
		SetSigningKeyshare(keyshare).
		SetNetwork(st.NetworkRegtest).
		Save(ctx)
	require.NoError(t, err)

	// Create a UTXO that represents the actual Bitcoin transaction
	// confirming the deposit to the child node's address
	_, err = dbTX.Utxo.Create().
		SetID(uuid.New()).
		SetBlockHeight(100).
		SetTxid([]byte("deposit_txid_" + testID)).
		SetVout(0).
		SetAmount(65536).
		SetNetwork(st.NetworkRegtest).
		SetPkScript([]byte("pk_script_" + testID)).
		SetDepositAddress(depositAddress).
		Save(ctx)
	require.NoError(t, err)

	_, err = tree.Update().
		SetBaseTxid([]byte("deposit_txid_" + testID)).
		Save(ctx)
	require.NoError(t, err)

	// Do NOT create a block height record - this simulates the case where
	// blockchain sync hasn't happened yet or the block height is not tracked

	req := &pb.FinalizeNodeSignaturesRequest{
		NodeSignatures: []*pb.NodeSignatures{
			{NodeId: rootNode.ID.String()},
			{NodeId: childNode.ID.String()},
		},
		Intent: pbcommon.SignatureIntent_CREATION,
	}

	_, err = handler.FinalizeNodeSignatures(ctx, req)
	require.Error(t, err, "should fail with no block height")
	assert.ErrorContains(t, err, "no block height present in db")
}
