package handler

import (
	"math/rand/v2"
	"testing"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestHandler() *TreeCreationHandler {
	config := &so.Config{
		BitcoindConfigs: map[string]so.BitcoindConfig{
			"regtest": {
				DepositConfirmationThreshold: 1,
			},
		},
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	}
	return NewTreeCreationHandler(config)
}

func createTestTx() *wire.MsgTx {
	tx := wire.NewMsgTx(wire.TxVersion)
	// Add a proper input
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: [32]byte{1, 2, 3}, Index: 0},
		Sequence:         wire.MaxTxInSequenceNum,
	})
	// Add a proper output with a valid P2PKH script
	pkScript := []byte{0x76, 0xa9, 0x14, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14, 0x88, 0xac}
	tx.AddTxOut(&wire.TxOut{Value: 100000, PkScript: pkScript})
	return tx
}

func createTestUTXO(rawTx []byte, vout uint32) *pb.UTXO {
	return &pb.UTXO{
		RawTx:   rawTx,
		Vout:    vout,
		Network: pb.Network_REGTEST,
		Txid:    make([]byte, 32),
	}
}

func TestNewTreeCreationHandler(t *testing.T) {
	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	handler := NewTreeCreationHandler(config)

	assert.NotNil(t, handler)
	assert.Equal(t, config, handler.config)
}

func TestFindParentOutputFromUtxo(t *testing.T) {
	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()

	handler := createTestHandler()
	testTx := createTestTx()

	var txBuf []byte
	txBuf, err := common.SerializeTx(testTx)
	require.NoError(t, err)

	tests := []struct {
		name                  string
		utxo                  *pb.UTXO
		expectError           bool
		expectedErrorContains string
		setupTree             bool
	}{
		{
			name:        "valid utxo with single output",
			utxo:        createTestUTXO(txBuf, 0),
			expectError: false,
		},
		{
			name: "invalid raw transaction",
			utxo: &pb.UTXO{
				RawTx: []byte{0x01, 0x02}, // invalid tx
				Vout:  0,
			},
			expectError:           true,
			expectedErrorContains: "EOF", // The actual error from Bitcoin transaction parsing
		},
		{
			name:                  "vout out of bounds",
			utxo:                  createTestUTXO(txBuf, 5), // tx only has 1 output (index 0)
			expectError:           true,
			expectedErrorContains: "vout out of bounds",
		},
		{
			name:                  "tree already exists",
			utxo:                  createTestUTXO(txBuf, 0),
			expectError:           true,
			expectedErrorContains: "already exists",
			setupTree:             true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupTree {
				// Create a tree with the same base txid to trigger "already exists" error
				db, err := ent.GetDbFromContext(ctx)
				require.NoError(t, err)

				tx, err := common.TxFromRawTxBytes(tt.utxo.RawTx)
				require.NoError(t, err)

				txHash := tx.TxHash()
				_, err = db.Tree.Create().
					SetOwnerIdentityPubkey([]byte("test")).
					SetNetwork(st.NetworkRegtest).
					SetBaseTxid(txHash[:]).
					SetVout(0).
					SetStatus(st.TreeStatusPending).
					Save(ctx)
				require.NoError(t, err)
			}

			output, err := handler.findParentOutputFromUtxo(ctx, tt.utxo)

			if tt.expectError {
				require.ErrorContains(t, err, tt.expectedErrorContains)
				assert.Nil(t, output)
			} else {
				require.NoError(t, err)
				require.NotNil(t, output)
				assert.Equal(t, int64(100000), output.Value)
			}
		})
	}
}

func TestFindParentOutputFromNodeOutput(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{1})

	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()

	handler := createTestHandler()

	// Setup test data
	db, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	keysharePrivkey := keys.MustGeneratePrivateKeyFromRand(rng)
	publicSharePrivkey := keys.MustGeneratePrivateKeyFromRand(rng)
	identityPrivkey := keys.MustGeneratePrivateKeyFromRand(rng)
	signingPrivkey := keys.MustGeneratePrivateKeyFromRand(rng)
	verifyingPrivkey := keys.MustGeneratePrivateKeyFromRand(rng)

	// Create a signing keyshare
	signingKeyshare, err := db.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(keysharePrivkey.Serialize()).
		SetPublicShares(map[string][]byte{"test": publicSharePrivkey.Public().Serialize()}).
		SetPublicKey(keysharePrivkey.Public().Serialize()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	// Create a tree
	tree, err := db.Tree.Create().
		SetOwnerIdentityPubkey(identityPrivkey.Public().Serialize()).
		SetNetwork(st.NetworkRegtest).
		SetBaseTxid(make([]byte, 32)).
		SetVout(0).
		SetStatus(st.TreeStatusAvailable).
		Save(ctx)
	require.NoError(t, err)

	testTx := createTestTx()
	txBuf, err := common.SerializeTx(testTx)
	require.NoError(t, err)

	// Create a tree node
	node, err := db.TreeNode.Create().
		SetTree(tree).
		SetStatus(st.TreeNodeStatusAvailable).
		SetOwnerIdentityPubkey(identityPrivkey.Public().Serialize()).
		SetOwnerSigningPubkey(signingPrivkey.Public().Serialize()).
		SetValue(100000).
		SetVerifyingPubkey(verifyingPrivkey.Public().Serialize()).
		SetSigningKeyshare(signingKeyshare).
		SetRawTx(txBuf).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	tests := []struct {
		name                  string
		nodeOutput            *pb.NodeOutput
		expectError           bool
		expectedErrorContains string
		setupChild            bool
	}{
		{
			name: "valid node output",
			nodeOutput: &pb.NodeOutput{
				NodeId: node.ID.String(),
				Vout:   0,
			},
			expectError: false,
		},
		{
			name: "invalid node ID",
			nodeOutput: &pb.NodeOutput{
				NodeId: "invalid-uuid",
				Vout:   0,
			},
			expectError:           true,
			expectedErrorContains: "invalid UUID",
		},
		{
			name: "non-existent node",
			nodeOutput: &pb.NodeOutput{
				NodeId: uuid.New().String(),
				Vout:   0,
			},
			expectError:           true,
			expectedErrorContains: "not found",
		},
		{
			name: "vout out of bounds",
			nodeOutput: &pb.NodeOutput{
				NodeId: node.ID.String(),
				Vout:   5, // tx only has 1 output
			},
			expectError:           true,
			expectedErrorContains: "vout out of bounds",
		},
		{
			name: "child already exists",
			nodeOutput: &pb.NodeOutput{
				NodeId: node.ID.String(),
				Vout:   0,
			},
			expectError:           true,
			expectedErrorContains: "already exists",
			setupChild:            true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupChild {
				// Create a child node to trigger "already exists" error
				_, err = db.TreeNode.Create().
					SetTree(tree).
					SetStatus(st.TreeNodeStatusAvailable).
					SetOwnerIdentityPubkey([]byte("test_identity")).
					SetOwnerSigningPubkey([]byte("test_signing")).
					SetValue(50000).
					SetVerifyingPubkey([]byte("test_verifying")).
					SetSigningKeyshare(signingKeyshare).
					SetRawTx(txBuf).
					SetParent(node).
					SetVout(0).
					Save(ctx)
				require.NoError(t, err)
			}

			output, err := handler.findParentOutputFromNodeOutput(ctx, tt.nodeOutput)

			if tt.expectError {
				require.ErrorContains(t, err, tt.expectedErrorContains)
				assert.Nil(t, output)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, output)
				assert.Equal(t, int64(100000), output.Value)
			}
		})
	}
}

func TestFindParentOutputFromPrepareTreeAddressRequest(t *testing.T) {
	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()

	handler := createTestHandler()
	testTx := createTestTx()
	txBuf, err := common.SerializeTx(testTx)
	require.NoError(t, err)

	tests := []struct {
		name        string
		req         *pb.PrepareTreeAddressRequest
		expectError bool
	}{
		{
			name: "parent node output source",
			req: &pb.PrepareTreeAddressRequest{
				Source: &pb.PrepareTreeAddressRequest_ParentNodeOutput{
					ParentNodeOutput: &pb.NodeOutput{
						NodeId: uuid.New().String(),
						Vout:   0,
					},
				},
			},
			expectError: true, // Will fail because node doesn't exist
		},
		{
			name: "on-chain utxo source",
			req: &pb.PrepareTreeAddressRequest{
				Source: &pb.PrepareTreeAddressRequest_OnChainUtxo{
					OnChainUtxo: createTestUTXO(txBuf, 0),
				},
			},
			expectError: false,
		},
		{
			name: "invalid source - nil",
			req: &pb.PrepareTreeAddressRequest{
				Source: nil,
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := handler.findParentOutputFromPrepareTreeAddressRequest(ctx, tt.req)

			if tt.expectError {
				require.Error(t, err)
				assert.Nil(t, output)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, output)
			}
		})
	}
}

func TestFindParentOutputFromCreateTreeRequest(t *testing.T) {
	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()

	handler := createTestHandler()
	testTx := createTestTx()
	txBuf, err := common.SerializeTx(testTx)
	require.NoError(t, err)

	tests := []struct {
		name        string
		req         *pb.CreateTreeRequest
		expectError bool
	}{
		{
			name: "parent node output source",
			req: &pb.CreateTreeRequest{
				Source: &pb.CreateTreeRequest_ParentNodeOutput{
					ParentNodeOutput: &pb.NodeOutput{
						NodeId: uuid.New().String(),
						Vout:   0,
					},
				},
			},
			expectError: true, // Will fail because node doesn't exist
		},
		{
			name: "on-chain utxo source",
			req: &pb.CreateTreeRequest{
				Source: &pb.CreateTreeRequest_OnChainUtxo{
					OnChainUtxo: createTestUTXO(txBuf, 0),
				},
			},
			expectError: false,
		},
		{
			name: "invalid source - nil",
			req: &pb.CreateTreeRequest{
				Source: nil,
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := handler.findParentOutputFromCreateTreeRequest(ctx, tt.req)

			if tt.expectError {
				require.Error(t, err)
				assert.Nil(t, output)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, output)
			}
		})
	}
}

func TestGetSigningKeyshareFromOutput_Invalid_Errors(t *testing.T) {
	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()

	handler := createTestHandler()

	tests := []struct {
		name    string
		output  *wire.TxOut
		network common.Network
	}{
		{
			// Will fail because P2TRAddressFromPkScript won't work with this script
			name: "valid output with existing deposit address",
			output: &wire.TxOut{
				Value:    100000,
				PkScript: []byte{0x00, 0x14, 0x75, 0x1e, 0x76, 0xe8, 0x19, 0x91, 0x96, 0xd4, 0x54, 0x94, 0x1c, 0x45, 0xd1, 0xb3, 0xa3, 0x23, 0xf1, 0x43, 0x3b, 0xd6}, // P2WPKH script
			},
			network: common.Regtest,
		},
		{
			name: "invalid pkScript",
			output: &wire.TxOut{
				Value:    100000,
				PkScript: []byte{0x01, 0x02}, // invalid script
			},
			network: common.Regtest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			userPubKey, keyshare, err := handler.getSigningKeyshareFromOutput(ctx, tt.network, tt.output)

			require.Error(t, err)
			assert.Zero(t, userPubKey)
			assert.Nil(t, keyshare)
		})
	}
}

func TestValidateAndCountTreeAddressNodes(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{1})

	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()

	handler := createTestHandler()

	parentPrivkey := keys.MustGeneratePrivateKeyFromRand(rng)
	child1Privkey := keys.MustGeneratePrivateKeyFromRand(rng)
	child2Privkey := keys.MustGeneratePrivateKeyFromRand(rng)

	tests := []struct {
		name                string
		parentUserPublicKey keys.Public
		nodes               []*pb.AddressRequestNode
		expectedCount       int
		expectError         bool
	}{
		{
			name:                "empty nodes",
			parentUserPublicKey: parentPrivkey.Public(),
			nodes:               []*pb.AddressRequestNode{},
			expectedCount:       0,
			expectError:         false,
		},
		{
			name:                "single leaf node",
			parentUserPublicKey: parentPrivkey.Public(),
			nodes: []*pb.AddressRequestNode{
				{
					UserPublicKey: parentPrivkey.Public().Serialize(),
					Children:      nil,
				},
			},
			expectedCount: 0, // len(nodes) - 1 = 1 - 1 = 0
			expectError:   false,
		},
		{
			name:                "nodes with children - key mismatch",
			parentUserPublicKey: parentPrivkey.Public(),
			nodes: []*pb.AddressRequestNode{
				{
					UserPublicKey: child1Privkey.Public().Serialize(), // This doesn't match parent
					Children:      nil,
				},
				{
					UserPublicKey: child2Privkey.Public().Serialize(), // This doesn't match parent
					Children:      nil,
				},
			},
			expectedCount: 0,
			expectError:   true, // Public key validation will fail
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count, err := handler.validateAndCountTreeAddressNodes(ctx, tt.parentUserPublicKey, tt.nodes)

			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedCount, count)
			}
		})
	}
}

func TestCreatePrepareTreeAddressNodeFromAddressNode(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{1})

	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()

	handler := createTestHandler()

	privKey := keys.MustGeneratePrivateKeyFromRand(rng)

	tests := []struct {
		name        string
		node        *pb.AddressRequestNode
		expectError bool
	}{
		{
			name: "leaf node",
			node: &pb.AddressRequestNode{
				UserPublicKey: privKey.Public().Serialize(),
				Children:      nil,
			},
			expectError: false,
		},
		{
			name: "node with children",
			node: &pb.AddressRequestNode{
				UserPublicKey: privKey.Public().Serialize(),
				Children: []*pb.AddressRequestNode{
					{
						UserPublicKey: privKey.Public().Serialize(),
						Children:      nil,
					},
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := handler.createPrepareTreeAddressNodeFromAddressNode(ctx, tt.node)

			if tt.expectError {
				require.Error(t, err)
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, result)
				assert.Equal(t, tt.node.UserPublicKey, result.UserPublicKey)
				assert.Len(t, result.Children, len(tt.node.Children))
			}
		})
	}
}

func TestUpdateParentNodeStatus(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{1})

	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()

	handler := createTestHandler()

	// Setup test data
	db, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	keysharePrivkey := keys.MustGeneratePrivateKeyFromRand(rng)
	publicSharePrivkey := keys.MustGeneratePrivateKeyFromRand(rng)
	identityPrivkey := keys.MustGeneratePrivateKeyFromRand(rng)
	signingPrivkey := keys.MustGeneratePrivateKeyFromRand(rng)
	verifyingPrivkey := keys.MustGeneratePrivateKeyFromRand(rng)

	// Create a signing keyshare
	signingKeyshare, err := db.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(keysharePrivkey.Serialize()).
		SetPublicShares(map[string][]byte{"test": publicSharePrivkey.Public().Serialize()}).
		SetPublicKey(keysharePrivkey.Public().Serialize()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	// Create a tree
	tree, err := db.Tree.Create().
		SetOwnerIdentityPubkey(identityPrivkey.Public().Serialize()).
		SetNetwork(st.NetworkRegtest).
		SetBaseTxid(make([]byte, 32)).
		SetVout(0).
		SetStatus(st.TreeStatusAvailable).
		Save(ctx)
	require.NoError(t, err)

	// Create a tree node with Available status
	availableNode, err := db.TreeNode.Create().
		SetTree(tree).
		SetStatus(st.TreeNodeStatusAvailable).
		SetOwnerIdentityPubkey(identityPrivkey.Public().Serialize()).
		SetOwnerSigningPubkey(signingPrivkey.Public().Serialize()).
		SetValue(100000).
		SetVerifyingPubkey(verifyingPrivkey.Public().Serialize()).
		SetSigningKeyshare(signingKeyshare).
		SetRawTx([]byte("test_tx")).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	// Create a tree node with different status
	creatingNode, err := db.TreeNode.Create().
		SetTree(tree).
		SetStatus(st.TreeNodeStatusCreating).
		SetOwnerIdentityPubkey(identityPrivkey.Public().Serialize()).
		SetOwnerSigningPubkey(signingPrivkey.Public().Serialize()).
		SetValue(100000).
		SetVerifyingPubkey(verifyingPrivkey.Public().Serialize()).
		SetSigningKeyshare(signingKeyshare).
		SetRawTx([]byte("test_tx")).
		SetVout(1).
		Save(ctx)
	require.NoError(t, err)

	tests := []struct {
		name                string
		parentNodeOutput    *pb.NodeOutput
		expectError         bool
		expectedFinalStatus st.TreeNodeStatus
	}{
		{
			name:             "nil parent node output",
			parentNodeOutput: nil,
			expectError:      false,
		},
		{
			name: "invalid node ID",
			parentNodeOutput: &pb.NodeOutput{
				NodeId: "invalid-uuid",
				Vout:   0,
			},
			expectError: true,
		},
		{
			name: "non-existent node",
			parentNodeOutput: &pb.NodeOutput{
				NodeId: uuid.New().String(),
				Vout:   0,
			},
			expectError: true,
		},
		{
			name: "available node - should be updated to splitted",
			parentNodeOutput: &pb.NodeOutput{
				NodeId: availableNode.ID.String(),
				Vout:   0,
			},
			expectError:         false,
			expectedFinalStatus: st.TreeNodeStatusSplitted,
		},
		{
			name: "creating node - should remain unchanged",
			parentNodeOutput: &pb.NodeOutput{
				NodeId: creatingNode.ID.String(),
				Vout:   1,
			},
			expectError:         false,
			expectedFinalStatus: st.TreeNodeStatusCreating, // Should remain unchanged
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := handler.updateParentNodeStatus(ctx, tt.parentNodeOutput)

			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)

				if tt.parentNodeOutput != nil {
					// Verify the status was updated correctly
					nodeID, err := uuid.Parse(tt.parentNodeOutput.NodeId)
					require.NoError(t, err)

					updatedNode, err := db.TreeNode.Get(ctx, nodeID)
					require.NoError(t, err)
					assert.Equal(t, tt.expectedFinalStatus, updatedNode.Status)
				}
			}
		})
	}
}

func TestCreateTestHelpers(t *testing.T) {
	t.Run("createTestHandler", func(t *testing.T) {
		handler := createTestHandler()
		assert.NotNil(t, handler)
		assert.NotNil(t, handler.config)
	})

	t.Run("createTestTx", func(t *testing.T) {
		tx := createTestTx()
		assert.NotNil(t, tx)
		assert.Len(t, tx.TxOut, 1)
		assert.Equal(t, int64(100000), tx.TxOut[0].Value)
	})

	t.Run("createTestUTXO", func(t *testing.T) {
		rawTx := []byte("test_tx")
		vout := uint32(1)
		utxo := createTestUTXO(rawTx, vout)

		assert.NotNil(t, utxo)
		assert.Equal(t, rawTx, utxo.RawTx)
		assert.Equal(t, vout, utxo.Vout)
		assert.Equal(t, pb.Network_REGTEST, utxo.Network)
	})
}

func TestEdgeCases(t *testing.T) {
	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()

	handler := createTestHandler()

	t.Run("findParentOutputFromUtxo with malformed transaction", func(t *testing.T) {
		utxo := &pb.UTXO{
			Vout: 0,
		}

		output, err := handler.findParentOutputFromUtxo(ctx, utxo)
		require.Error(t, err)
		assert.Nil(t, output)
	})

	t.Run("validateAndCountTreeAddressNodes with nil parent key", func(t *testing.T) {
		rng := rand.NewChaCha8([32]byte{1})
		userPrivkey := keys.MustGeneratePrivateKeyFromRand(rng)
		nodes := []*pb.AddressRequestNode{
			{
				UserPublicKey: userPrivkey.Public().Serialize(),
				Children:      nil,
			},
		}

		count, err := handler.validateAndCountTreeAddressNodes(ctx, keys.Public{}, nodes)
		require.Error(t, err) // Should fail due to nil parent key
		assert.Equal(t, 0, count)
	})
}

// Ensures that the confirmation txid matches the utxo id in tree creation.
// Regression test for https://linear.app/lightsparkdev/issue/LIG-8038
func TestPrepareSigningJobs_EnsureConfTxidMatchesUtxoId(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{1})

	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()

	handler := createTestHandler()

	db, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	keysharePrivkey := keys.MustGeneratePrivateKeyFromRand(rng)
	publicSharePrivkey := keys.MustGeneratePrivateKeyFromRand(rng)
	identityPrivkey := keys.MustGeneratePrivateKeyFromRand(rng)
	signingPrivkey := keys.MustGeneratePrivateKeyFromRand(rng)

	signingKeyshare, err := db.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(keysharePrivkey.Serialize()).
		SetPublicShares(map[string][]byte{"test": publicSharePrivkey.Public().Serialize()}).
		SetPublicKey(keysharePrivkey.Public().Serialize()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	testPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	taprootScript, err := common.P2TRScriptFromPubKey(testPubKey)
	require.NoError(t, err)

	legitimateTx := wire.NewMsgTx(wire.TxVersion)
	legitimateTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: [32]byte{1, 2, 3}, Index: 0},
		Sequence:         wire.MaxTxInSequenceNum,
	})
	legitimateTxOutput := &wire.TxOut{Value: 100000, PkScript: taprootScript}
	legitimateTx.AddTxOut(legitimateTxOutput)
	legitimateTxHash := legitimateTx.TxHash()

	// Create DIFFERENT (malicious) transaction with same UTXO structure but different TXID
	maliciousTx := wire.NewMsgTx(wire.TxVersion)
	maliciousTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: [32]byte{9, 8, 7}, Index: 0}, // Different input!
		Sequence:         wire.MaxTxInSequenceNum,
	})
	maliciousTx.AddTxOut(legitimateTxOutput) // Same output value and script
	maliciousTxBuf, err := common.SerializeTx(maliciousTx)
	require.NoError(t, err)
	maliciousTxHash := maliciousTx.TxHash()

	// Verify the TXIDs are actually different (sanity check)
	require.NotEqual(t, legitimateTxHash, maliciousTxHash, "Test setup error: TXIDs should be different")

	outputAddress, err := common.P2TRAddressFromPkScript(legitimateTxOutput.PkScript, common.Regtest)
	require.NoError(t, err)

	// Create a deposit address that's confirmed with the LEGITIMATE transaction
	_, err = db.DepositAddress.Create().
		SetAddress(*outputAddress).
		SetOwnerIdentityPubkey(identityPrivkey.Public().Serialize()).
		SetOwnerSigningPubkey(signingPrivkey.Public().Serialize()).
		SetSigningKeyshare(signingKeyshare).
		SetConfirmationHeight(100).                     // Confirmed at height 100
		SetConfirmationTxid(legitimateTxHash.String()). // CONFIRMED with legitimate TX
		SetNetwork(st.NetworkRegtest).
		Save(ctx)
	require.NoError(t, err)

	nodePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	nodeTaprootScript, err := common.P2TRScriptFromPubKey(nodePrivKey.Public())
	require.NoError(t, err)

	nodeTx := wire.NewMsgTx(wire.TxVersion)
	nodeTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: maliciousTxHash, Index: 0}, // References the malicious UTXO
		Sequence:         wire.MaxTxInSequenceNum,
	})
	nodeTx.AddTxOut(&wire.TxOut{Value: 50000, PkScript: nodeTaprootScript})
	nodeTxBuf, err := common.SerializeTx(nodeTx)
	require.NoError(t, err)

	// Create a CreateTreeRequest that tries to use the MALICIOUS transaction
	// This should be rejected because the TXID doesn't match the confirmed TXID
	req := &pb.CreateTreeRequest{
		UserIdentityPublicKey: identityPrivkey.Public().Serialize(),
		Source: &pb.CreateTreeRequest_OnChainUtxo{
			OnChainUtxo: &pb.UTXO{
				RawTx:   maliciousTxBuf,     // MALICIOUS transaction bytes
				Txid:    maliciousTxHash[:], // MALICIOUS TXID
				Vout:    0,
				Network: pb.Network_REGTEST,
			},
		},
		Node: &pb.CreationNode{
			NodeTxSigningJob: &pb.SigningJob{
				RawTx:                  nodeTxBuf,
				SigningPublicKey:       signingPrivkey.Public().Serialize(),
				SigningNonceCommitment: &pbcommon.SigningCommitment{Hiding: make([]byte, 33), Binding: make([]byte, 33)},
			},
		},
	}

	signingJobs, nodes, err := handler.prepareSigningJobs(ctx, req, false)

	require.ErrorContains(t, err, "onfirmation txid does not match utxo txid")
	assert.Empty(t, signingJobs)
	assert.Empty(t, nodes)
}

// Validation is done in signing_coordinator.go, but this is a unit test for the
// potential issue at the tree creation handler level.
// https://linear.app/lightsparkdev/issue/LIG-8087
func TestPrepareSigningJobs_InvalidChildrenOutputs(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{1})

	tests := []struct {
		name                  string
		childrenValues        []int64
		exepectedErrorContent string
	}{
		{
			name:                  "child values exceed parent value",
			childrenValues:        []int64{50000, 75000},
			exepectedErrorContent: "total output value is greater than the previous output value",
		},
		{
			name:                  "child values are negative",
			childrenValues:        []int64{-500, -750},
			exepectedErrorContent: "output value is negative",
		},
		{
			name:                  "child value includes negative first",
			childrenValues:        []int64{-5000, 6000},
			exepectedErrorContent: "output value is negative",
		},
		{
			name:                  "child value includes negative second",
			childrenValues:        []int64{6000, -5000},
			exepectedErrorContent: "output value is negative",
		},
		{
			name:                  "many children exceed parent value",
			childrenValues:        []int64{1, 2, 3, 4, 5, 6, 1000000},
			exepectedErrorContent: "total output value is greater than the previous output value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()

			ctx, dbCtx := db.NewTestSQLiteContext(t, ctx)
			defer dbCtx.Close()

			handler := createTestHandler()

			dbTx, err := ent.GetDbFromContext(ctx)
			require.NoError(t, err)

			keysharePrivkey := keys.MustGeneratePrivateKeyFromRand(rng)
			publicSharePrivkey := keys.MustGeneratePrivateKeyFromRand(rng)
			identityPrivkey := keys.MustGeneratePrivateKeyFromRand(rng)
			signingPrivkey := keys.MustGeneratePrivateKeyFromRand(rng)

			signingKeyshare, err := dbTx.SigningKeyshare.Create().
				SetStatus(st.KeyshareStatusAvailable).
				SetSecretShare(keysharePrivkey.Serialize()).
				SetPublicShares(map[string][]byte{"test": publicSharePrivkey.Public().Serialize()}).
				SetPublicKey(keysharePrivkey.Public().Serialize()).
				SetMinSigners(2).
				SetCoordinatorIndex(0).
				Save(ctx)
			require.NoError(t, err)

			parentValue := int64(1000)
			parentPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
			parentTaprootScript, err := common.P2TRScriptFromPubKey(parentPubKey)
			require.NoError(t, err)

			parentTx := wire.NewMsgTx(wire.TxVersion)
			parentTx.AddTxIn(&wire.TxIn{
				PreviousOutPoint: wire.OutPoint{Hash: [32]byte{1, 2, 3}, Index: 0},
				Sequence:         wire.MaxTxInSequenceNum,
			})
			parentTxOutput := &wire.TxOut{Value: parentValue, PkScript: parentTaprootScript}
			parentTx.AddTxOut(parentTxOutput)
			parentTxBuf, err := common.SerializeTx(parentTx)
			require.NoError(t, err)
			parentTxHash := parentTx.TxHash()

			parentAddress, err := common.P2TRAddressFromPkScript(parentTxOutput.PkScript, common.Regtest)
			require.NoError(t, err)

			_, err = dbTx.DepositAddress.Create().
				SetAddress(*parentAddress).
				SetOwnerIdentityPubkey(identityPrivkey.Public().Serialize()).
				SetOwnerSigningPubkey(signingPrivkey.Public().Serialize()).
				SetSigningKeyshare(signingKeyshare).
				SetConfirmationHeight(0). // Not confirmed, so no txid validation
				// Don't set confirmation txid to bypass the validation
				SetNetwork(st.NetworkRegtest).
				Save(ctx)
			require.NoError(t, err)

			// Create malicious node transaction that splits unfairly with
			// massive inflation.  More specifically, the child outputs are
			// larger than the parent output. The outputs are defined in the
			// test inputs, and they are set below in the range across
			// tt.childrenValues.
			maliciousNodeTx := wire.NewMsgTx(wire.TxVersion)
			maliciousNodeTx.AddTxIn(&wire.TxIn{
				PreviousOutPoint: wire.OutPoint{Hash: parentTxHash, Index: 0},
				Sequence:         wire.MaxTxInSequenceNum,
			})

			var childrenSigningJobs []*pb.CreationNode
			for _, childValue := range tt.childrenValues {
				childKeyshare, err := dbTx.SigningKeyshare.Create().
					SetStatus(st.KeyshareStatusAvailable).
					SetSecretShare(keys.MustGeneratePrivateKeyFromRand(rng).Serialize()).
					SetPublicShares(map[string][]byte{"test": publicSharePrivkey.Public().Serialize()}).
					SetPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()).
					SetMinSigners(2).
					SetCoordinatorIndex(0).
					Save(ctx)
				require.NoError(t, err)

				// Create malicious child transaction with bad output values
				childPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
				childScript, err := common.P2TRScriptFromPubKey(childPubKey)
				require.NoError(t, err)

				maliciousNodeTx.AddTxOut(&wire.TxOut{Value: childValue, PkScript: childScript})

				childAddress, err := common.P2TRAddressFromPkScript(childScript, common.Regtest)
				require.NoError(t, err)

				_, err = dbTx.DepositAddress.Create().
					SetAddress(*childAddress).
					SetOwnerIdentityPubkey(identityPrivkey.Public().Serialize()).
					SetOwnerSigningPubkey(childKeyshare.PublicKey).
					SetSigningKeyshare(childKeyshare).
					SetNetwork(st.NetworkRegtest).
					Save(ctx)
				require.NoError(t, err)

				childNodeTx := wire.NewMsgTx(wire.TxVersion)
				childNodeTx.AddTxIn(&wire.TxIn{
					PreviousOutPoint: wire.OutPoint{Hash: maliciousNodeTx.TxHash(), Index: 0},
					Sequence:         wire.MaxTxInSequenceNum,
				})
				childNodeTx.AddTxOut(&wire.TxOut{Value: childValue, PkScript: childScript})
				childNodeTxBuf, err := common.SerializeTx(childNodeTx)
				require.NoError(t, err)

				childrenSigningJobs = append(childrenSigningJobs, &pb.CreationNode{
					NodeTxSigningJob: &pb.SigningJob{
						RawTx:                  childNodeTxBuf,
						SigningPublicKey:       childKeyshare.PublicKey,
						SigningNonceCommitment: &pbcommon.SigningCommitment{Hiding: make([]byte, 33), Binding: make([]byte, 33)},
					},
				})
			}
			maliciousNodeTxBuf, err := common.SerializeTx(maliciousNodeTx)
			require.NoError(t, err)

			req := &pb.CreateTreeRequest{
				UserIdentityPublicKey: identityPrivkey.Public().Serialize(),
				Source: &pb.CreateTreeRequest_OnChainUtxo{
					OnChainUtxo: &pb.UTXO{
						RawTx:   parentTxBuf,
						Txid:    parentTxHash[:],
						Vout:    0,
						Network: pb.Network_REGTEST,
					},
				},
				Node: &pb.CreationNode{
					NodeTxSigningJob: &pb.SigningJob{
						RawTx:                  maliciousNodeTxBuf, // Transaction with inflated child outputs
						SigningPublicKey:       signingPrivkey.Public().Serialize(),
						SigningNonceCommitment: &pbcommon.SigningCommitment{Hiding: make([]byte, 33), Binding: make([]byte, 33)},
					},
					Children: childrenSigningJobs,
				},
			}

			_, _, err = handler.prepareSigningJobs(ctx, req, false)

			require.ErrorContains(t, err, tt.exepectedErrorContent)
		})
	}
}
