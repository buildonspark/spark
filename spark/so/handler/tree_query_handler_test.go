package handler

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestTreeQueryHandlersRejectNilRequests(t *testing.T) {
	handler := NewTreeQueryHandler(&so.Config{})

	t.Run("QueryNodes", func(t *testing.T) {
		resp, err := handler.QueryNodes(t.Context(), nil, false)
		require.Nil(t, resp)
		require.ErrorContains(t, err, "request is required")
	})

	t.Run("QueryBalance", func(t *testing.T) {
		resp, err := handler.QueryBalance(t.Context(), nil)
		require.Nil(t, resp)
		require.ErrorContains(t, err, "request is required")
	})

	t.Run("QueryUnusedDepositAddresses", func(t *testing.T) {
		resp, err := handler.QueryUnusedDepositAddresses(t.Context(), nil)
		require.Nil(t, resp)
		require.ErrorContains(t, err, "request is required")
	})

	t.Run("QueryStaticDepositAddresses", func(t *testing.T) {
		resp, err := handler.QueryStaticDepositAddresses(t.Context(), nil, false)
		require.Nil(t, resp)
		require.ErrorContains(t, err, "request is required")
	})
}

func TestTreeQueryHandlersRejectNegativePagination(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	handler := NewTreeQueryHandler(&so.Config{})
	identityPubKey := keys.GeneratePrivateKey().Public().Serialize()

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "QueryNodes negative limit",
			call: func() error {
				resp, err := handler.QueryNodes(ctx, &pb.QueryNodesRequest{
					Source: &pb.QueryNodesRequest_OwnerIdentityPubkey{
						OwnerIdentityPubkey: identityPubKey,
					},
					Network: pb.Network_REGTEST,
					Limit:   -1,
				}, false)
				require.Nil(t, resp)
				return err
			},
		},
		{
			name: "QueryNodes negative offset",
			call: func() error {
				resp, err := handler.QueryNodes(ctx, &pb.QueryNodesRequest{
					Source: &pb.QueryNodesRequest_OwnerIdentityPubkey{
						OwnerIdentityPubkey: identityPubKey,
					},
					Network: pb.Network_REGTEST,
					Limit:   1,
					Offset:  -1,
				}, false)
				require.Nil(t, resp)
				return err
			},
		},
		{
			name: "QueryUnusedDepositAddresses negative limit",
			call: func() error {
				resp, err := handler.QueryUnusedDepositAddresses(ctx, &pb.QueryUnusedDepositAddressesRequest{
					IdentityPublicKey: identityPubKey,
					Network:           pb.Network_REGTEST,
					Limit:             -1,
				})
				require.Nil(t, resp)
				return err
			},
		},
		{
			name: "QueryUnusedDepositAddresses negative offset",
			call: func() error {
				resp, err := handler.QueryUnusedDepositAddresses(ctx, &pb.QueryUnusedDepositAddressesRequest{
					IdentityPublicKey: identityPubKey,
					Network:           pb.Network_REGTEST,
					Limit:             1,
					Offset:            -1,
				})
				require.Nil(t, resp)
				return err
			},
		},
		{
			name: "QueryStaticDepositAddresses negative limit",
			call: func() error {
				resp, err := handler.QueryStaticDepositAddresses(ctx, &pb.QueryStaticDepositAddressesRequest{
					IdentityPublicKey: identityPubKey,
					Network:           pb.Network_REGTEST,
					Limit:             -1,
				}, false)
				require.Nil(t, resp)
				return err
			},
		},
		{
			name: "QueryStaticDepositAddresses negative offset",
			call: func() error {
				resp, err := handler.QueryStaticDepositAddresses(ctx, &pb.QueryStaticDepositAddressesRequest{
					IdentityPublicKey: identityPubKey,
					Network:           pb.Network_REGTEST,
					Limit:             1,
					Offset:            -1,
				}, false)
				require.Nil(t, resp)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.ErrorContains(t, tt.call(), "non-negative offset and limit")
		})
	}
}

func TestQueryStaticDepositAddresses(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	rng := rand.NewChaCha8([32]byte{})

	randomPrivKey1 := keys.MustGeneratePrivateKeyFromRand(rng)
	randomPrivKey2 := keys.MustGeneratePrivateKeyFromRand(rng)
	randomPrivKey3 := keys.MustGeneratePrivateKeyFromRand(rng)
	identityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	identityPubKey2 := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	secretShare := keys.MustGeneratePrivateKeyFromRand(rng)

	signingKeyshare1, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secretShare).
		SetPublicShares(map[string]keys.Public{"test": secretShare.Public()}).
		SetPublicKey(randomPrivKey1.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)
	signingKeyshare2, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secretShare).
		SetPublicShares(map[string]keys.Public{"test": secretShare.Public()}).
		SetPublicKey(randomPrivKey2.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)
	signingKeyshare3, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secretShare).
		SetPublicShares(map[string]keys.Public{"test": secretShare.Public()}).
		SetPublicKey(randomPrivKey3.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)
	_, err = tx.DepositAddress.Create().
		SetAddress("bcrt1qfpk6cxxfr49wtvzxd72ahe2xtu7gj6vx7m0ksy").
		SetOwnerIdentityPubkey(identityPubKey).
		SetOwnerSigningPubkey(randomPrivKey1.Public()).
		SetSigningKeyshare(signingKeyshare1).
		SetNetwork(btcnetwork.Regtest).
		SetIsStatic(true).
		Save(ctx)
	require.NoError(t, err)
	_, err = tx.DepositAddress.Create().
		SetAddress("bcrt1q043w4fkg4w0jl6fxrx0kd4ww3rsq2tm4mtmv9e").
		SetOwnerIdentityPubkey(identityPubKey).
		SetOwnerSigningPubkey(randomPrivKey2.Public()).
		SetSigningKeyshare(signingKeyshare2).
		SetNetwork(btcnetwork.Regtest).
		SetIsStatic(true).
		SetIsDefault(false).
		Save(ctx)
	require.NoError(t, err)
	// This is a different identity pubkey, so it should not be returned
	_, err = tx.DepositAddress.Create().
		SetAddress("bcrt1q043w4fkg4w0jl6fxrx0kd4ww3rsq2tm4mtmv9d").
		SetOwnerIdentityPubkey(identityPubKey2).
		SetOwnerSigningPubkey(randomPrivKey2.Public()).
		SetSigningKeyshare(signingKeyshare3).
		SetNetwork(btcnetwork.Regtest).
		SetIsStatic(true).
		Save(ctx)
	require.NoError(t, err)
}

func TestQueryNodesRejectsTooManyNodeIDsBeforeParsing(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	handler := NewTreeQueryHandler(&so.Config{})
	nodeIDs := make([]string, DefaultMaxQueryNodesByID+1)

	resp, err := handler.QueryNodes(ctx, &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_NodeIds{
			NodeIds: &pb.TreeNodeIds{NodeIds: nodeIDs},
		},
	}, false)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "node ids provided")
}

func TestQueryNodes_StatusField(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	rng := rand.NewChaCha8([32]byte{})

	// Create test keys
	identityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	signingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	secretShare := keys.MustGeneratePrivateKeyFromRand(rng)

	// Create signing keyshare
	signingKeyshare, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secretShare).
		SetPublicShares(map[string]keys.Public{"test": secretShare.Public()}).
		SetPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	// Create tree
	baseTxid := st.NewRandomTxIDForTesting(t)

	tree, err := tx.Tree.Create().
		SetOwnerIdentityPubkey(identityPubKey).
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TreeStatusAvailable).
		SetBaseTxid(baseTxid).
		SetVout(1).
		Save(ctx)
	require.NoError(t, err)

	// Create valid test transaction bytes using the same function as other tests
	rawTx := createOldBitcoinTxBytes(t, verifyingPubKey)
	refundTx := createOldBitcoinTxBytes(t, signingPubKey)

	// Test different status values
	statusTests := []struct {
		name          string
		status        st.TreeNodeStatus
		shouldBeFound bool // Whether this status should be returned by QueryNodes (not filtered out)
	}{
		{
			name:          "Available status",
			status:        st.TreeNodeStatusAvailable,
			shouldBeFound: true,
		},
		{
			name:          "Frozen by issuer status",
			status:        st.TreeNodeStatusFrozenByIssuer,
			shouldBeFound: true,
		},
		{
			name:          "Transfer locked status",
			status:        st.TreeNodeStatusTransferLocked,
			shouldBeFound: true,
		},
		{
			name:          "Split locked status",
			status:        st.TreeNodeStatusSplitLocked,
			shouldBeFound: true,
		},
		{
			name:          "Aggregated status",
			status:        st.TreeNodeStatusAggregated,
			shouldBeFound: true,
		},
		{
			name:          "On chain status",
			status:        st.TreeNodeStatusOnChain,
			shouldBeFound: true,
		},
		{
			name:          "Exited status",
			status:        st.TreeNodeStatusExited,
			shouldBeFound: true,
		},
		{
			name:          "Aggregate lock status",
			status:        st.TreeNodeStatusAggregateLock,
			shouldBeFound: true,
		},
		{
			name:          "Creating status - should be filtered out",
			status:        st.TreeNodeStatusCreating,
			shouldBeFound: false,
		},
		{
			name:          "Splitted status - should be filtered out",
			status:        st.TreeNodeStatusSplitted,
			shouldBeFound: false,
		},
		{
			name:          "Investigation status - should be filtered out",
			status:        st.TreeNodeStatusInvestigation,
			shouldBeFound: false,
		},
		{
			name:          "Lost status - should be filtered out",
			status:        st.TreeNodeStatusLost,
			shouldBeFound: false,
		},
		{
			name:          "Reimbursed status - should be filtered out",
			status:        st.TreeNodeStatusReimbursed,
			shouldBeFound: false,
		},
		{
			name:          "Renew locked status",
			status:        st.TreeNodeStatusRenewLocked,
			shouldBeFound: true,
		},
	}

	// Create tree nodes with different statuses
	createdNodes := make(map[st.TreeNodeStatus]*ent.TreeNode)
	for _, tt := range statusTests {
		node, err := tx.TreeNode.Create().
			SetTree(tree).
			SetNetwork(tree.Network).
			SetStatus(tt.status).
			SetOwnerIdentityPubkey(identityPubKey).
			SetOwnerSigningPubkey(signingPubKey).
			SetValue(100000).
			SetVerifyingPubkey(verifyingPubKey).
			SetSigningKeyshare(signingKeyshare).
			SetRawTx(rawTx).
			SetRawRefundTx(refundTx).
			SetDirectTx(rawTx).
			SetDirectRefundTx(refundTx).
			SetDirectFromCpfpRefundTx(refundTx).
			SetVout(1).
			Save(ctx)
		require.NoError(t, err)
		createdNodes[tt.status] = node
	}

	ctx = authn.InjectSessionForTests(ctx, identityPubKey, 9999999999)

	// Create handler
	handler := NewTreeQueryHandler(&so.Config{})

	// Test QueryNodes with owner identity pubkey
	req := &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_OwnerIdentityPubkey{
			OwnerIdentityPubkey: identityPubKey.Serialize(),
		},
		Network: pb.Network_REGTEST,
		Limit:   100,
	}

	resp, err := handler.QueryNodes(ctx, req, false)
	require.NoError(t, err)

	// Verify that only non-filtered statuses are returned
	foundStatuses := make(map[string]bool)
	for _, node := range resp.GetNodes() {
		foundStatuses[node.GetStatus()] = true
	}

	for _, tt := range statusTests {
		t.Run(tt.name, func(t *testing.T) {
			expectedStatusString := string(tt.status)
			if tt.shouldBeFound {
				require.True(t, foundStatuses[expectedStatusString],
					"Status %s should be found in response", expectedStatusString)
			} else {
				require.False(t, foundStatuses[expectedStatusString],
					"Status %s should be filtered out from response", expectedStatusString)
			}
		})
	}

	// Test QueryNodes with specific status filter using protobuf enums
	reqWithStatus := &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_OwnerIdentityPubkey{
			OwnerIdentityPubkey: identityPubKey.Serialize(),
		},
		Network: pb.Network_REGTEST,
		Limit:   100,
		Statuses: []pb.TreeNodeStatus{
			pb.TreeNodeStatus_TREE_NODE_STATUS_AVAILABLE,
			pb.TreeNodeStatus_TREE_NODE_STATUS_FROZEN_BY_ISSUER,
		},
	}

	respWithStatus, err := handler.QueryNodes(ctx, reqWithStatus, false)
	require.NoError(t, err)

	// Verify only the requested statuses are returned
	require.Len(t, respWithStatus.GetNodes(), 2)
	for _, node := range respWithStatus.GetNodes() {
		require.Contains(t, []string{
			string(st.TreeNodeStatusAvailable),
			string(st.TreeNodeStatusFrozenByIssuer),
		}, node.GetStatus(), "Node should have one of the requested statuses")
	}

	// Test QueryNodes with node IDs (should return all statuses, no filtering)
	nodeIDs := make([]string, 0, len(createdNodes))
	for _, node := range createdNodes {
		nodeIDs = append(nodeIDs, node.ID.String())
	}

	reqByIDs := &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_NodeIds{
			NodeIds: &pb.TreeNodeIds{
				NodeIds: nodeIDs,
			},
		},
	}

	respByIDs, err := handler.QueryNodes(ctx, reqByIDs, false)
	require.NoError(t, err)

	// Should return all nodes regardless of status when querying by IDs
	require.Len(t, respByIDs.GetNodes(), len(createdNodes))
	allStatusesFound := make(map[string]bool)
	for _, node := range respByIDs.GetNodes() {
		allStatusesFound[node.GetStatus()] = true
	}

	// Verify all statuses are present in the response
	for _, tt := range statusTests {
		expectedStatusString := string(tt.status)
		t.Logf("Status %s should be found when querying by node IDs", expectedStatusString)
		require.True(t, allStatusesFound[expectedStatusString],
			"Status %s should be found when querying by node IDs", expectedStatusString)
	}
}

func createAncestorChainTestNodes(t *testing.T, createTime time.Time) (context.Context, *ent.TreeNode, *ent.TreeNode, *ent.TreeNode) {
	ctx, _ := db.NewTestSQLiteContext(t)
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	rng := rand.NewChaCha8([32]byte{})

	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	signingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	secretShare := keys.MustGeneratePrivateKeyFromRand(rng)

	signingKeyshare, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secretShare).
		SetPublicShares(map[string]keys.Public{"test": secretShare.Public()}).
		SetPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	tree, err := tx.Tree.Create().
		SetOwnerIdentityPubkey(ownerIdentityPubKey).
		SetNetwork(btcnetwork.Mainnet).
		SetStatus(st.TreeStatusAvailable).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(1).
		Save(ctx)
	require.NoError(t, err)

	rawTx := createOldBitcoinTxBytes(t, verifyingPubKey)
	refundTx := createOldBitcoinTxBytes(t, signingPubKey)

	root, err := tx.TreeNode.Create().
		SetCreateTime(createTime).
		SetTree(tree).
		SetNetwork(tree.Network).
		SetStatus(st.TreeNodeStatusAvailable).
		SetOwnerIdentityPubkey(ownerIdentityPubKey).
		SetOwnerSigningPubkey(signingPubKey).
		SetValue(300000).
		SetVerifyingPubkey(verifyingPubKey).
		SetSigningKeyshare(signingKeyshare).
		SetRawTx(rawTx).
		SetRawRefundTx(refundTx).
		SetDirectTx(rawTx).
		SetDirectRefundTx(refundTx).
		SetDirectFromCpfpRefundTx(refundTx).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	parent, err := tx.TreeNode.Create().
		SetCreateTime(createTime).
		SetTree(tree).
		SetParent(root).
		SetNetwork(tree.Network).
		SetStatus(st.TreeNodeStatusAvailable).
		SetOwnerIdentityPubkey(ownerIdentityPubKey).
		SetOwnerSigningPubkey(signingPubKey).
		SetValue(200000).
		SetVerifyingPubkey(verifyingPubKey).
		SetSigningKeyshare(signingKeyshare).
		SetRawTx(rawTx).
		SetRawRefundTx(refundTx).
		SetDirectTx(rawTx).
		SetDirectRefundTx(refundTx).
		SetDirectFromCpfpRefundTx(refundTx).
		SetVout(1).
		Save(ctx)
	require.NoError(t, err)

	leaf, err := tx.TreeNode.Create().
		SetCreateTime(createTime).
		SetTree(tree).
		SetParent(parent).
		SetNetwork(tree.Network).
		SetStatus(st.TreeNodeStatusAvailable).
		SetOwnerIdentityPubkey(ownerIdentityPubKey).
		SetOwnerSigningPubkey(signingPubKey).
		SetValue(100000).
		SetVerifyingPubkey(verifyingPubKey).
		SetSigningKeyshare(signingKeyshare).
		SetRawTx(rawTx).
		SetRawRefundTx(refundTx).
		SetDirectTx(rawTx).
		SetDirectRefundTx(refundTx).
		SetDirectFromCpfpRefundTx(refundTx).
		SetVout(2).
		Save(ctx)
	require.NoError(t, err)

	return ctx, root, parent, leaf
}

func TestGetAncestorChain_RootInclusion(t *testing.T) {
	testCases := []struct {
		name              string
		createTime        time.Time
		isSSP             bool
		expectRootInChain bool
	}{
		{
			name:              "legacy mainnet non-SSP skips root",
			createTime:        ancestorChainRootSkipCutoff.Add(-time.Minute),
			isSSP:             false,
			expectRootInChain: false,
		},
		{
			name:              "legacy mainnet SSP includes root",
			createTime:        ancestorChainRootSkipCutoff.Add(-time.Minute),
			isSSP:             true,
			expectRootInChain: true,
		},
		{
			name:              "post-cutoff mainnet non-SSP includes root",
			createTime:        ancestorChainRootSkipCutoff,
			isSSP:             false,
			expectRootInChain: true,
		},
		{
			name:              "post-cutoff mainnet SSP includes root",
			createTime:        ancestorChainRootSkipCutoff,
			isSSP:             true,
			expectRootInChain: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, root, parent, leaf := createAncestorChainTestNodes(t, tc.createTime)
			dbClient, err := ent.GetDbFromContext(ctx)
			require.NoError(t, err)

			nodeMap := make(map[string]*pb.TreeNode)
			protoLeaf, err := leaf.MarshalSparkProto(ctx)
			require.NoError(t, err)
			nodeMap[leaf.ID.String()] = protoLeaf

			err = getAncestorChain(ctx, dbClient, leaf, nodeMap, tc.isSSP)
			require.NoError(t, err)
			require.Contains(t, nodeMap, leaf.ID.String())
			require.Contains(t, nodeMap, parent.ID.String())

			if tc.expectRootInChain {
				require.Len(t, nodeMap, 3)
				require.Contains(t, nodeMap, root.ID.String())
			} else {
				require.Len(t, nodeMap, 2)
				require.NotContains(t, nodeMap, root.ID.String())
			}
		})
	}
}

func createTreeQueryTestContext(t *testing.T) (context.Context, *so.Config) {
	ctx, _ := db.NewTestSQLiteContext(t)
	cfg := sparktesting.TestConfig(t)
	return ctx, cfg
}

// PrivacyTestData contains all the test data needed for privacy tests
type PrivacyTestData struct {
	OwnerIdentityPubKey     keys.Public
	RequesterIdentityPubKey keys.Public
	MasterIdentityPubKey    keys.Public
	Node                    *ent.TreeNode
	WalletSetting           *ent.WalletSetting
}

// createPrivacyTestData creates all the necessary test data for privacy tests
func createPrivacyTestData(t *testing.T, privacyEnabled bool, sameRequesterAndOwner bool, injectSession bool, setMasterKey bool) (context.Context, *so.Config, *PrivacyTestData) {
	// Create test context and config
	ctx, cfg := createTreeQueryTestContext(t)
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Create random number generator
	rng := rand.NewChaCha8([32]byte{})

	// Create test keys
	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	var requesterIdentityPubKey keys.Public
	if sameRequesterAndOwner {
		requesterIdentityPubKey = ownerIdentityPubKey
	} else {
		requesterIdentityPubKey = keys.MustGeneratePrivateKeyFromRand(rng).Public()
	}
	var masterIdentityPubKey keys.Public
	if setMasterKey {
		masterIdentityPubKey = keys.MustGeneratePrivateKeyFromRand(rng).Public()
	}
	signingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	secretShare := keys.MustGeneratePrivateKeyFromRand(rng)

	// Create signing keyshare
	signingKeyshare, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secretShare).
		SetPublicShares(map[string]keys.Public{"test": secretShare.Public()}).
		SetPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	// Create tree
	baseTxid := st.NewRandomTxIDForTesting(t)

	tree, err := tx.Tree.Create().
		SetOwnerIdentityPubkey(ownerIdentityPubKey).
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TreeStatusAvailable).
		SetBaseTxid(baseTxid).
		SetVout(1).
		Save(ctx)
	require.NoError(t, err)

	// Create test transaction bytes
	rawTx := createOldBitcoinTxBytes(t, verifyingPubKey)
	refundTx := createOldBitcoinTxBytes(t, signingPubKey)

	// Create tree node
	node, err := tx.TreeNode.Create().
		SetTree(tree).
		SetNetwork(tree.Network).
		SetStatus(st.TreeNodeStatusAvailable).
		SetOwnerIdentityPubkey(ownerIdentityPubKey).
		SetOwnerSigningPubkey(signingPubKey).
		SetValue(100000).
		SetVerifyingPubkey(verifyingPubKey).
		SetSigningKeyshare(signingKeyshare).
		SetRawTx(rawTx).
		SetRawRefundTx(refundTx).
		SetDirectTx(rawTx).
		SetDirectRefundTx(refundTx).
		SetDirectFromCpfpRefundTx(refundTx).
		SetVout(1).
		Save(ctx)
	require.NoError(t, err)

	// Create wallet setting
	walletSettingCreate := tx.WalletSetting.
		Create().
		SetOwnerIdentityPublicKey(ownerIdentityPubKey).
		SetPrivateEnabled(privacyEnabled)
	if !masterIdentityPubKey.IsZero() {
		walletSettingCreate = walletSettingCreate.SetMasterIdentityPublicKey(masterIdentityPubKey)
	}
	walletSetting, err := walletSettingCreate.Save(ctx)
	require.NoError(t, err)

	// Set up session context for the requester if requested
	if injectSession {
		ctx = authn.InjectSessionForTests(ctx, requesterIdentityPubKey, 9999999999)
	}

	return ctx, cfg, &PrivacyTestData{
		OwnerIdentityPubKey:     ownerIdentityPubKey,
		RequesterIdentityPubKey: requesterIdentityPubKey,
		MasterIdentityPubKey:    masterIdentityPubKey,
		Node:                    node,
		WalletSetting:           walletSetting,
	}
}

func TestQueryNodes_PrivacyEnabled_OwnerIdentityPubkey(t *testing.T) {
	// Create test data with privacy enabled and different requester/owner
	ctx, cfg, testData := createPrivacyTestData(t, true, false, true, false)

	// Create handler
	handler := NewTreeQueryHandler(cfg)

	// Test QueryNodes with owner identity pubkey - should return empty results when requester doesn't have access
	req := &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_OwnerIdentityPubkey{
			OwnerIdentityPubkey: testData.OwnerIdentityPubKey.Serialize(),
		},
		Network: pb.Network_REGTEST,
		Limit:   100,
	}

	resp, err := handler.QueryNodes(ctx, req, false)
	require.NoError(t, err)
	assert.Empty(t, resp.GetNodes(), "Should return empty results when owner has privacy enabled and requester doesn't have read access")
}

func TestQueryNodes_PrivacyEnabled_NodeIds(t *testing.T) {
	ctx, cfg, testData := createPrivacyTestData(t, true, false, true, false)
	handler := NewTreeQueryHandler(cfg)

	resp, err := handler.QueryNodes(ctx, &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_NodeIds{
			NodeIds: &pb.TreeNodeIds{
				NodeIds: []string{testData.Node.ID.String()},
			},
		},
	}, false)
	require.NoError(t, err)
	assert.Empty(t, resp.GetNodes(), "Should not reveal a private node by ID when requester doesn't have read access")

	ownerCtx := authn.InjectSessionForTests(ctx, testData.OwnerIdentityPubKey, 9999999999)
	ownerResp, err := handler.QueryNodes(ownerCtx, &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_NodeIds{
			NodeIds: &pb.TreeNodeIds{
				NodeIds: []string{testData.Node.ID.String()},
			},
		},
	}, false)
	require.NoError(t, err)
	require.Len(t, ownerResp.GetNodes(), 1, "Owner should still be able to query their own private node by ID")
	assert.Equal(t, testData.Node.ID.String(), ownerResp.GetNodes()[testData.Node.ID.String()].GetId())
}

// TestQueryNodes_PrivacyEnabled_NodeIds_AncestorsBypassFilter locks in the
// contract the SDK unilateral-exit walk depends on: ancestor (non-leaf) nodes
// must be returned when queried by ID even if their recorded owner is a
// privacy-enabled wallet. Transfers only re-own the leaf, so ancestors keep
// their original owner; filtering them would make the exit walk fail with
// "Exit chain is incomplete" while the include_parents walk still exposes the
// same nodes unfiltered.
func TestQueryNodes_PrivacyEnabled_NodeIds_AncestorsBypassFilter(t *testing.T) {
	ctx, cfg, testData := createPrivacyTestData(t, true, false, true, false)
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Give the private wallet's node a child leaf, making it an ancestor.
	rng := rand.NewChaCha8([32]byte{1})
	signingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	tree, err := testData.Node.QueryTree().Only(ctx)
	require.NoError(t, err)
	keyshare, err := testData.Node.QuerySigningKeyshare().Only(ctx)
	require.NoError(t, err)
	rawTx := createOldBitcoinTxBytes(t, verifyingPubKey)
	refundTx := createOldBitcoinTxBytes(t, signingPubKey)

	child, err := tx.TreeNode.Create().
		SetTree(tree).
		SetParent(testData.Node).
		SetNetwork(tree.Network).
		SetStatus(st.TreeNodeStatusAvailable).
		SetOwnerIdentityPubkey(testData.OwnerIdentityPubKey).
		SetOwnerSigningPubkey(signingPubKey).
		SetValue(100000).
		SetVerifyingPubkey(verifyingPubKey).
		SetSigningKeyshare(keyshare).
		SetRawTx(rawTx).
		SetRawRefundTx(refundTx).
		SetDirectTx(rawTx).
		SetDirectRefundTx(refundTx).
		SetDirectFromCpfpRefundTx(refundTx).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	handler := NewTreeQueryHandler(cfg)

	// A requester without read access fetches both the ancestor and the leaf
	// by ID, as the SDK unilateral-exit walk does.
	resp, err := handler.QueryNodes(ctx, &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_NodeIds{
			NodeIds: &pb.TreeNodeIds{
				NodeIds: []string{testData.Node.ID.String(), child.ID.String()},
			},
		},
	}, false)
	require.NoError(t, err)

	nodes := resp.GetNodes()
	assert.NotContains(t, nodes, child.ID.String(), "leaf owned by a private wallet must stay hidden from other requesters")
	require.Contains(t, nodes, testData.Node.ID.String(), "ancestor (non-leaf) node must not be dropped by the privacy filter")
	assert.Equal(t, bitcointransaction.NUMSPoint().Serialize(), nodes[testData.Node.ID.String()].GetOwnerIdentityPublicKey(),
		"ancestor returned to a requester without read access must not reveal the private wallet's identity pubkey")

	// Masking is unconditional for non-leaf nodes: even the recorded owner
	// sees the NUMS point, keeping the response independent of who asks.
	ownerCtx := authn.InjectSessionForTests(ctx, testData.OwnerIdentityPubKey, 9999999999)
	ownerResp, err := handler.QueryNodes(ownerCtx, &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_NodeIds{
			NodeIds: &pb.TreeNodeIds{
				NodeIds: []string{testData.Node.ID.String()},
			},
		},
	}, false)
	require.NoError(t, err)
	require.Contains(t, ownerResp.GetNodes(), testData.Node.ID.String())
	assert.Equal(t, bitcointransaction.NUMSPoint().Serialize(), ownerResp.GetNodes()[testData.Node.ID.String()].GetOwnerIdentityPublicKey(),
		"non-leaf nodes are masked for every external requester, including the recorded owner")
}

// TestQueryNodes_PrivacyEnabled_IncludeParents_MasksPrivateAncestorOwner
// covers the include_parents ancestor walk: ancestors owned by a
// privacy-enabled wallet are returned (the walk never filtered them), but
// their owner identity pubkey must be masked with a NUMS point so the walk
// cannot be used to learn a private wallet's identity.
func TestQueryNodes_PrivacyEnabled_IncludeParents_MasksPrivateAncestorOwner(t *testing.T) {
	ctx, cfg, testData := createPrivacyTestData(t, true, false, true, false)
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// The requester owns a leaf whose parent belongs to the private wallet,
	// as happens after a transfer (only the leaf is re-owned).
	rng := rand.NewChaCha8([32]byte{2})
	signingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	tree, err := testData.Node.QueryTree().Only(ctx)
	require.NoError(t, err)
	keyshare, err := testData.Node.QuerySigningKeyshare().Only(ctx)
	require.NoError(t, err)
	rawTx := createOldBitcoinTxBytes(t, verifyingPubKey)
	refundTx := createOldBitcoinTxBytes(t, signingPubKey)

	child, err := tx.TreeNode.Create().
		SetTree(tree).
		SetParent(testData.Node).
		SetNetwork(tree.Network).
		SetStatus(st.TreeNodeStatusAvailable).
		SetOwnerIdentityPubkey(testData.RequesterIdentityPubKey).
		SetOwnerSigningPubkey(signingPubKey).
		SetValue(100000).
		SetVerifyingPubkey(verifyingPubKey).
		SetSigningKeyshare(keyshare).
		SetRawTx(rawTx).
		SetRawRefundTx(refundTx).
		SetDirectTx(rawTx).
		SetDirectRefundTx(refundTx).
		SetDirectFromCpfpRefundTx(refundTx).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	handler := NewTreeQueryHandler(cfg)

	resp, err := handler.QueryNodes(ctx, &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_NodeIds{
			NodeIds: &pb.TreeNodeIds{
				NodeIds: []string{child.ID.String()},
			},
		},
		IncludeParents: true,
	}, false)
	require.NoError(t, err)

	nodes := resp.GetNodes()
	require.Contains(t, nodes, child.ID.String(), "requester's own leaf must be returned")
	assert.Equal(t, testData.RequesterIdentityPubKey.Serialize(), nodes[child.ID.String()].GetOwnerIdentityPublicKey())
	require.Contains(t, nodes, testData.Node.ID.String(), "include_parents must return the ancestor")
	assert.Equal(t, bitcointransaction.NUMSPoint().Serialize(), nodes[testData.Node.ID.String()].GetOwnerIdentityPublicKey(),
		"ancestor owned by a private wallet must have its identity pubkey masked in the include_parents walk")
}

// TestQueryNodes_OwnerSource_MasksOwnNonLeafAncestors locks in that non-leaf
// masking is independent of the request source and the requester: even a
// wallet querying its own nodes by owner identity pubkey sees the NUMS point
// on its own non-leaf nodes, while its leaves keep the real owner pubkey.
func TestQueryNodes_OwnerSource_MasksOwnNonLeafAncestors(t *testing.T) {
	// Privacy disabled, requester is the owner — masking must apply anyway.
	ctx, cfg, testData := createPrivacyTestData(t, false, true, true, false)
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	rng := rand.NewChaCha8([32]byte{3})
	signingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	tree, err := testData.Node.QueryTree().Only(ctx)
	require.NoError(t, err)
	keyshare, err := testData.Node.QuerySigningKeyshare().Only(ctx)
	require.NoError(t, err)
	rawTx := createOldBitcoinTxBytes(t, verifyingPubKey)
	refundTx := createOldBitcoinTxBytes(t, signingPubKey)

	child, err := tx.TreeNode.Create().
		SetTree(tree).
		SetParent(testData.Node).
		SetNetwork(tree.Network).
		SetStatus(st.TreeNodeStatusAvailable).
		SetOwnerIdentityPubkey(testData.OwnerIdentityPubKey).
		SetOwnerSigningPubkey(signingPubKey).
		SetValue(100000).
		SetVerifyingPubkey(verifyingPubKey).
		SetSigningKeyshare(keyshare).
		SetRawTx(rawTx).
		SetRawRefundTx(refundTx).
		SetDirectTx(rawTx).
		SetDirectRefundTx(refundTx).
		SetDirectFromCpfpRefundTx(refundTx).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	handler := NewTreeQueryHandler(cfg)

	resp, err := handler.QueryNodes(ctx, &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_OwnerIdentityPubkey{
			OwnerIdentityPubkey: testData.OwnerIdentityPubKey.Serialize(),
		},
		Network: pb.Network_REGTEST,
		Limit:   100,
	}, false)
	require.NoError(t, err)

	nodes := resp.GetNodes()
	require.Contains(t, nodes, testData.Node.ID.String())
	require.Contains(t, nodes, child.ID.String())
	assert.Equal(t, bitcointransaction.NUMSPoint().Serialize(), nodes[testData.Node.ID.String()].GetOwnerIdentityPublicKey(),
		"non-leaf node must be masked even for its own owner querying by owner identity pubkey")
	assert.Equal(t, testData.OwnerIdentityPubKey.Serialize(), nodes[child.ID.String()].GetOwnerIdentityPublicKey(),
		"leaf must keep the real owner identity pubkey")
}

// TestQueryNodes_NodeIds_SSPBypassPrivacy locks in the contract the internal
// SparkInternalService.QueryNodes depends on: the by-ID source must bypass the
// per-wallet privacy filter when isSSP=true. SyncTreeNodes / sync_node read
// peer leaves by ID over that internal RPC; without this bypass, leaves owned
// by privacy-enabled wallets are filtered out and the sync fails with
// "expected N, got 0". End-to-end SO-to-SO sync coverage lives in a
// multi-operator integration test (follow-up).
func TestQueryNodes_NodeIds_SSPBypassPrivacy(t *testing.T) {
	// Privacy enabled, different requester, no session injected.
	ctx, cfg, testData := createPrivacyTestData(t, true, false, false, false)
	handler := NewTreeQueryHandler(cfg)

	req := &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_NodeIds{
			NodeIds: &pb.TreeNodeIds{
				NodeIds: []string{testData.Node.ID.String()},
			},
		},
	}

	// Public path (isSSP=false) filters the private node out.
	publicResp, err := handler.QueryNodes(ctx, req, false)
	require.NoError(t, err)
	assert.Empty(t, publicResp.GetNodes(), "public QueryNodes should filter a private wallet's node when queried by ID")

	// Internal path (isSSP=true) must return it, which is what
	// SparkInternalService.QueryNodes uses to unblock SO-to-SO sync.
	internalResp, err := handler.QueryNodes(ctx, req, true)
	require.NoError(t, err)
	require.Len(t, internalResp.GetNodes(), 1, "internal QueryNodes (isSSP=true) must bypass the privacy filter for the by-ID source")
	assert.Equal(t, testData.Node.ID.String(), internalResp.GetNodes()[testData.Node.ID.String()].GetId())
}

func TestQueryNodes_PrivacyDisabled_OwnerIdentityPubkey(t *testing.T) {
	// Create test data with privacy disabled and different requester/owner
	ctx, cfg, testData := createPrivacyTestData(t, false, false, true, false)

	// Create handler
	handler := NewTreeQueryHandler(cfg)

	// Test QueryNodes with owner identity pubkey - should return nodes when privacy is disabled
	req := &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_OwnerIdentityPubkey{
			OwnerIdentityPubkey: testData.OwnerIdentityPubKey.Serialize(),
		},
		Network: pb.Network_REGTEST,
		Limit:   100,
	}

	resp, err := handler.QueryNodes(ctx, req, false)
	require.NoError(t, err)
	assert.Len(t, resp.GetNodes(), 1, "Should return nodes when owner has privacy disabled (everyone has access)")
	assert.Equal(t, testData.Node.ID.String(), resp.GetNodes()[testData.Node.ID.String()].GetId())
}

func TestQueryNodes_OwnerCanSeeOwnNodes(t *testing.T) {
	// Create test data with privacy enabled and same requester/owner
	ctx, cfg, testData := createPrivacyTestData(t, true, true, true, false)

	// Create handler
	handler := NewTreeQueryHandler(cfg)

	// Test QueryNodes with owner identity pubkey - should return nodes even with privacy enabled
	req := &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_OwnerIdentityPubkey{
			OwnerIdentityPubkey: testData.OwnerIdentityPubKey.Serialize(),
		},
		Network: pb.Network_REGTEST,
		Limit:   100,
	}

	resp, err := handler.QueryNodes(ctx, req, false)
	require.NoError(t, err)
	assert.Len(t, resp.GetNodes(), 1, "Owner should be able to see their own nodes even with privacy enabled")
	assert.Equal(t, testData.Node.ID.String(), resp.GetNodes()[testData.Node.ID.String()].GetId())
}

func TestQueryNodes_MasterCanSeeNodes(t *testing.T) {
	// Create test data with privacy enabled, different requester/owner, but requester is master
	ctx, cfg, testData := createPrivacyTestData(t, true, false, true, true)

	// Set up session context as the master (not the owner)
	ctx = authn.InjectSessionForTests(ctx, testData.MasterIdentityPubKey, 9999999999)

	// Create handler
	handler := NewTreeQueryHandler(cfg)

	// Test QueryNodes with owner identity pubkey - should return nodes when requester is master
	req := &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_OwnerIdentityPubkey{
			OwnerIdentityPubkey: testData.OwnerIdentityPubKey.Serialize(),
		},
		Network: pb.Network_REGTEST,
		Limit:   100,
	}

	resp, err := handler.QueryNodes(ctx, req, false)
	require.NoError(t, err)
	assert.Len(t, resp.GetNodes(), 1, "Master should be able to see nodes even when privacy is enabled")
	assert.Equal(t, testData.Node.ID.String(), resp.GetNodes()[testData.Node.ID.String()].GetId())
}

func TestQueryNodes_SSPBypassPrivacy(t *testing.T) {
	// Create test data with privacy enabled and different requester/owner
	ctx, cfg, testData := createPrivacyTestData(t, true, false, false, false)

	// Create handler
	handler := NewTreeQueryHandler(cfg)

	// Test QueryNodes with isSSP=true - should bypass privacy check and return nodes
	req := &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_OwnerIdentityPubkey{
			OwnerIdentityPubkey: testData.OwnerIdentityPubKey.Serialize(),
		},
		Network: pb.Network_REGTEST,
		Limit:   100,
	}

	resp, err := handler.QueryNodes(ctx, req, true) // isSSP=true
	require.NoError(t, err)
	assert.Len(t, resp.GetNodes(), 1, "SSP should be able to see nodes even when owner has privacy enabled")
	assert.Equal(t, testData.Node.ID.String(), resp.GetNodes()[testData.Node.ID.String()].GetId())
}

// TestQueryStaticDepositAddresses_PrivacyGate verifies the public endpoint
// (isSSP=false) denies a caller without read access to a privacy-enabled wallet,
// while the SSP-internal path (isSSP=true) bypasses the check.
//
// The returned-address path needs cross-SO proofs-of-possession infra, so it
// isn't unit-testable here. Instead we observe the gate's short-circuit: it
// returns empty before the request's network is validated, so a
// network-unspecified request succeeds-empty for the public caller (gate fired)
// but errors for the SSP (gate skipped, reaches the network check). End-to-end
// coverage is left to integration.
func TestQueryStaticDepositAddresses_PrivacyGate(t *testing.T) {
	// Privacy enabled, requester != owner, no session injected → no read access.
	// Global wallet privacy is enforced, while the static-deposit-specific knob
	// is off in the base context.
	baseCtx, cfg, testData := createPrivacyTestData(t, true, false, false, false)
	handler := NewTreeQueryHandler(cfg)

	req := &pb.QueryStaticDepositAddressesRequest{
		IdentityPublicKey: testData.OwnerIdentityPubKey.Serialize(),
		// Network intentionally UNSPECIFIED: the privacy gate short-circuits to an
		// empty response before the network check, so reaching the "network must be
		// specified" error means the gate did NOT fire.
	}

	// Static-deposit knob OFF: gate is dark → proceeds past it to network validation.
	_, err := handler.QueryStaticDepositAddresses(baseCtx, req, false)
	require.ErrorContains(t, err, "network must be specified")

	// Static-deposit knob ON.
	onCtx := knobs.InjectKnobsService(baseCtx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobStaticDepositAddressPrivacyEnabled: 100,
	}))

	// Public caller without read access: privacy gate short-circuits to empty.
	resp, err := handler.QueryStaticDepositAddresses(onCtx, req, false)
	require.NoError(t, err)
	assert.Empty(t, resp.GetDepositAddresses(), "private wallet's addresses must not be returned to a caller without read access")

	// SSP bypasses the gate even with the knob on, so it proceeds to network validation.
	_, err = handler.QueryStaticDepositAddresses(onCtx, req, true)
	require.ErrorContains(t, err, "network must be specified")
}

func TestQueryBalance_PrivacyEnabled_DifferentRequester(t *testing.T) {
	// Create test data with privacy enabled and different requester/owner
	ctx, cfg, testData := createPrivacyTestData(t, true, false, true, false)

	// Create handler
	handler := NewTreeQueryHandler(cfg)

	// Test QueryBalance with different requester - should return empty balance when requester doesn't have access
	req := &pb.QueryBalanceRequest{
		IdentityPublicKey: testData.OwnerIdentityPubKey.Serialize(),
		Network:           pb.Network_REGTEST,
	}

	resp, err := handler.QueryBalance(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), resp.GetBalance(), "Balance should be 0 when privacy is enabled and requester doesn't have read access")
	assert.Empty(t, resp.GetNodeBalances(), "NodeBalances should be empty when privacy is enabled and requester doesn't have read access")
}

func TestQueryBalance_PrivacyDisabled_DifferentRequester(t *testing.T) {
	// Create test data with privacy disabled and different requester/owner
	ctx, cfg, testData := createPrivacyTestData(t, false, false, true, false)

	// Create handler
	handler := NewTreeQueryHandler(cfg)

	// Test QueryBalance with different requester - should return actual balance when privacy is disabled
	req := &pb.QueryBalanceRequest{
		IdentityPublicKey: testData.OwnerIdentityPubKey.Serialize(),
		Network:           pb.Network_REGTEST,
	}

	resp, err := handler.QueryBalance(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, testData.Node.Value, resp.GetBalance(), "Balance should be returned when privacy is disabled (everyone has access)")
	assert.Len(t, resp.GetNodeBalances(), 1, "NodeBalances should contain the node when privacy is disabled")
	assert.Equal(t, testData.Node.Value, resp.GetNodeBalances()[testData.Node.ID.String()])
}

func TestQueryBalance_PrivacyEnabled_OwnerCanSeeOwnBalance(t *testing.T) {
	// Create test data with privacy enabled and same requester/owner
	ctx, cfg, testData := createPrivacyTestData(t, true, true, true, false)

	// Create handler
	handler := NewTreeQueryHandler(cfg)

	// Test QueryBalance with owner as requester - should return actual balance
	req := &pb.QueryBalanceRequest{
		IdentityPublicKey: testData.OwnerIdentityPubKey.Serialize(),
		Network:           pb.Network_REGTEST,
	}

	resp, err := handler.QueryBalance(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, testData.Node.Value, resp.GetBalance(), "Owner should be able to see their own balance even when privacy is enabled")
	assert.Len(t, resp.GetNodeBalances(), 1, "Owner should be able to see their own node balances even when privacy is enabled")
	assert.Equal(t, testData.Node.Value, resp.GetNodeBalances()[testData.Node.ID.String()])
}

func TestQueryBalance_MasterCanSeeBalance(t *testing.T) {
	// Create test data with privacy enabled, different requester/owner, but requester is master
	ctx, cfg, testData := createPrivacyTestData(t, true, false, true, true)

	// Set up session context as the master (not the owner)
	ctx = authn.InjectSessionForTests(ctx, testData.MasterIdentityPubKey, 9999999999)

	// Create handler
	handler := NewTreeQueryHandler(cfg)

	// Test QueryBalance with master as requester - should return actual balance
	req := &pb.QueryBalanceRequest{
		IdentityPublicKey: testData.OwnerIdentityPubKey.Serialize(),
		Network:           pb.Network_REGTEST,
	}

	resp, err := handler.QueryBalance(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, testData.Node.Value, resp.GetBalance(), "Master should be able to see balance even when privacy is enabled")
	assert.Len(t, resp.GetNodeBalances(), 1, "Master should be able to see node balances even when privacy is enabled")
	assert.Equal(t, testData.Node.Value, resp.GetNodeBalances()[testData.Node.ID.String()])
}

func TestQueryBalance_NoSession(t *testing.T) {
	// Create test data with privacy enabled but no session injected
	ctx, cfg, testData := createPrivacyTestData(t, true, false, false, false)

	// Create handler
	handler := NewTreeQueryHandler(cfg)

	// Test QueryBalance without session - should return empty balance
	req := &pb.QueryBalanceRequest{
		IdentityPublicKey: testData.OwnerIdentityPubKey.Serialize(),
		Network:           pb.Network_REGTEST,
	}

	resp, err := handler.QueryBalance(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), resp.GetBalance(), "Balance should be 0 when no session is provided and privacy is enabled")
	assert.Empty(t, resp.GetNodeBalances(), "NodeBalances should be empty when no session is provided and privacy is enabled")
}

// getAncestorChainsBatched parity tests use db.ConnectToTestPostgres, since the batched walk's
// raw SQL isn't SQLite-compatible; they're skipped when SKIP_POSTGRES_TESTS is set.

func createBatchedTestTreeAndKeyshare(t *testing.T, ctx context.Context, tx *ent.Client, rng *rand.ChaCha8, owner keys.Public, network btcnetwork.Network) (*ent.Tree, *ent.SigningKeyshare) {
	t.Helper()
	secretShare := keys.MustGeneratePrivateKeyFromRand(rng)
	keyshare, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secretShare).
		SetPublicShares(map[string]keys.Public{"test": secretShare.Public()}).
		SetPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	tree, err := tx.Tree.Create().
		SetOwnerIdentityPubkey(owner).
		SetNetwork(network).
		SetStatus(st.TreeStatusAvailable).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(1).
		Save(ctx)
	require.NoError(t, err)

	return tree, keyshare
}

func createBatchedTestNode(
	t *testing.T,
	ctx context.Context,
	tx *ent.Client,
	rng *rand.ChaCha8,
	tree *ent.Tree,
	parent *ent.TreeNode,
	keyshare *ent.SigningKeyshare,
	owner keys.Public,
	createTime time.Time,
	value uint64,
	vout int16,
) *ent.TreeNode {
	t.Helper()
	signingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	rawTx := createOldBitcoinTxBytes(t, verifyingPubKey)
	refundTx := createOldBitcoinTxBytes(t, signingPubKey)

	create := tx.TreeNode.Create().
		SetCreateTime(createTime).
		SetTree(tree).
		SetNetwork(tree.Network).
		SetStatus(st.TreeNodeStatusAvailable).
		SetOwnerIdentityPubkey(owner).
		SetOwnerSigningPubkey(signingPubKey).
		SetValue(value).
		SetVerifyingPubkey(verifyingPubKey).
		SetSigningKeyshare(keyshare).
		SetRawTx(rawTx).
		SetRawRefundTx(refundTx).
		SetDirectTx(rawTx).
		SetDirectRefundTx(refundTx).
		SetDirectFromCpfpRefundTx(refundTx).
		SetVout(vout)
	if parent != nil {
		create = create.SetParent(parent)
	}
	node, err := create.Save(ctx)
	require.NoError(t, err)
	return node
}

// requireTreeNodeMapsEqual compares key-by-key with proto.Equal; generated proto messages carry
// internal state that isn't safe to compare via a whole-map reflect.DeepEqual.
func requireTreeNodeMapsEqual(t *testing.T, expected, actual map[string]*pb.TreeNode) {
	t.Helper()
	require.Len(t, actual, len(expected))
	for id, expectedNode := range expected {
		actualNode, ok := actual[id]
		require.True(t, ok, "expected node %s to be present", id)
		assert.True(t, proto.Equal(expectedNode, actualNode), "node %s mismatch:\nexpected: %v\nactual:   %v", id, expectedNode, actualNode)
	}
}

// legacyAncestorChain runs getAncestorChain over each node into one shared map, exactly as
// QueryNodes' non-batched path does — the baseline the batched implementation must match.
func legacyAncestorChain(t *testing.T, ctx context.Context, dbClient *ent.Client, nodes []*ent.TreeNode, isSSP bool) map[string]*pb.TreeNode {
	t.Helper()
	legacyMap := make(map[string]*pb.TreeNode)
	for _, node := range nodes {
		require.NoError(t, getAncestorChain(ctx, dbClient, node, legacyMap, isSSP))
	}
	return legacyMap
}

// compareBatchedToLegacy asserts both paths agree and returns the batched result for further
// test-specific assertions.
func compareBatchedToLegacy(t *testing.T, ctx context.Context, dbClient *ent.Client, nodes []*ent.TreeNode, isSSP bool) map[string]*pb.TreeNode {
	t.Helper()
	legacyMap := legacyAncestorChain(t, ctx, dbClient, nodes, isSSP)
	batchedMap, err := getAncestorChainsBatched(ctx, dbClient, nodes, isSSP)
	require.NoError(t, err)
	requireTreeNodeMapsEqual(t, legacyMap, batchedMap)
	return batchedMap
}

func TestGetAncestorChainsBatched_ConvergingSiblings(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{10})
	owner := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	postCutoff := ancestorChainRootSkipCutoff.Add(time.Hour)

	tree, keyshare := createBatchedTestTreeAndKeyshare(t, ctx, tc.Client, rng, owner, btcnetwork.Mainnet)
	root := createBatchedTestNode(t, ctx, tc.Client, rng, tree, nil, keyshare, owner, postCutoff, 400000, 0)
	branch := createBatchedTestNode(t, ctx, tc.Client, rng, tree, root, keyshare, owner, postCutoff, 300000, 0)
	leafA := createBatchedTestNode(t, ctx, tc.Client, rng, tree, branch, keyshare, owner, postCutoff, 100000, 0)
	leafB := createBatchedTestNode(t, ctx, tc.Client, rng, tree, branch, keyshare, owner, postCutoff, 100000, 1)

	requested := []*ent.TreeNode{leafA, leafB}
	batchedMap := compareBatchedToLegacy(t, ctx, tc.Client, requested, false)

	require.Len(t, batchedMap, 2, "branch and root should each appear once, deduped across both leaves' paths")
	assert.Contains(t, batchedMap, branch.ID.String())
	assert.Contains(t, batchedMap, root.ID.String())
}

func TestGetAncestorChainsBatched_MultipleTrees(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{11})
	owner := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	postCutoff := ancestorChainRootSkipCutoff.Add(time.Hour)

	tree1, keyshare1 := createBatchedTestTreeAndKeyshare(t, ctx, tc.Client, rng, owner, btcnetwork.Regtest)
	root1 := createBatchedTestNode(t, ctx, tc.Client, rng, tree1, nil, keyshare1, owner, postCutoff, 200000, 0)
	leaf1 := createBatchedTestNode(t, ctx, tc.Client, rng, tree1, root1, keyshare1, owner, postCutoff, 100000, 0)

	tree2, keyshare2 := createBatchedTestTreeAndKeyshare(t, ctx, tc.Client, rng, owner, btcnetwork.Regtest)
	root2 := createBatchedTestNode(t, ctx, tc.Client, rng, tree2, nil, keyshare2, owner, postCutoff, 200000, 0)
	leaf2 := createBatchedTestNode(t, ctx, tc.Client, rng, tree2, root2, keyshare2, owner, postCutoff, 100000, 0)

	requested := []*ent.TreeNode{leaf1, leaf2}
	batchedMap := compareBatchedToLegacy(t, ctx, tc.Client, requested, false)

	require.Len(t, batchedMap, 2, "each tree's root should be resolved independently")
	assert.Contains(t, batchedMap, root1.ID.String())
	assert.Contains(t, batchedMap, root2.ID.String())
}

func TestGetAncestorChainsBatched_RootSuppression_AllPreCutoff(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{12})
	owner := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	preCutoff := ancestorChainRootSkipCutoff.Add(-time.Hour)

	tree, keyshare := createBatchedTestTreeAndKeyshare(t, ctx, tc.Client, rng, owner, btcnetwork.Mainnet)
	root := createBatchedTestNode(t, ctx, tc.Client, rng, tree, nil, keyshare, owner, preCutoff, 300000, 0)
	branch := createBatchedTestNode(t, ctx, tc.Client, rng, tree, root, keyshare, owner, preCutoff, 200000, 0)
	leaf := createBatchedTestNode(t, ctx, tc.Client, rng, tree, branch, keyshare, owner, preCutoff, 100000, 0)

	requested := []*ent.TreeNode{leaf}
	batchedMap := compareBatchedToLegacy(t, ctx, tc.Client, requested, false)

	assert.NotContains(t, batchedMap, root.ID.String(), "root must be suppressed when its only touched direct child predates the cutoff")
	assert.Contains(t, batchedMap, branch.ID.String(), "non-root ancestors are never suppressed")

	// isSSP bypasses suppression entirely, same as getAncestorChain today.
	sspBatchedMap, err := getAncestorChainsBatched(ctx, tc.Client, requested, true)
	require.NoError(t, err)
	assert.Contains(t, sspBatchedMap, root.ID.String(), "SSP callers must still see the root")
}

// TestGetAncestorChainsBatched_RootSuppression_DepthOneLeaf: the leaf's parent IS the root
// directly, so the leaf itself (never in the hydrated ancestor set) must count toward suppression.
func TestGetAncestorChainsBatched_RootSuppression_DepthOneLeaf(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{15})
	owner := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	preCutoff := ancestorChainRootSkipCutoff.Add(-time.Hour)
	postCutoff := ancestorChainRootSkipCutoff.Add(time.Hour)

	tree, keyshare := createBatchedTestTreeAndKeyshare(t, ctx, tc.Client, rng, owner, btcnetwork.Mainnet)
	root := createBatchedTestNode(t, ctx, tc.Client, rng, tree, nil, keyshare, owner, preCutoff, 200000, 0)
	preCutoffLeaf := createBatchedTestNode(t, ctx, tc.Client, rng, tree, root, keyshare, owner, preCutoff, 100000, 0)
	postCutoffLeaf := createBatchedTestNode(t, ctx, tc.Client, rng, tree, root, keyshare, owner, postCutoff, 100000, 1)

	preCutoffMap := compareBatchedToLegacy(t, ctx, tc.Client, []*ent.TreeNode{preCutoffLeaf}, false)
	assert.NotContains(t, preCutoffMap, root.ID.String(), "root must be suppressed when the requesting leaf itself predates the cutoff")

	postCutoffMap := compareBatchedToLegacy(t, ctx, tc.Client, []*ent.TreeNode{postCutoffLeaf}, false)
	assert.Contains(t, postCutoffMap, root.ID.String(), "root must be kept when the requesting leaf itself postdates the cutoff")
}

// TestGetAncestorChainsBatched_RootSuppression_MixedSiblingsKeepsRoot: a root is kept if any one
// touched path's direct-child-of-root is post-cutoff, even if another path alone would suppress it.
func TestGetAncestorChainsBatched_RootSuppression_MixedSiblingsKeepsRoot(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{13})
	owner := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	preCutoff := ancestorChainRootSkipCutoff.Add(-time.Hour)
	postCutoff := ancestorChainRootSkipCutoff.Add(time.Hour)

	tree, keyshare := createBatchedTestTreeAndKeyshare(t, ctx, tc.Client, rng, owner, btcnetwork.Mainnet)
	root := createBatchedTestNode(t, ctx, tc.Client, rng, tree, nil, keyshare, owner, preCutoff, 400000, 0)
	branchA := createBatchedTestNode(t, ctx, tc.Client, rng, tree, root, keyshare, owner, preCutoff, 150000, 0)
	branchB := createBatchedTestNode(t, ctx, tc.Client, rng, tree, root, keyshare, owner, postCutoff, 150000, 1)
	leafA := createBatchedTestNode(t, ctx, tc.Client, rng, tree, branchA, keyshare, owner, preCutoff, 100000, 0)
	leafB := createBatchedTestNode(t, ctx, tc.Client, rng, tree, branchB, keyshare, owner, postCutoff, 100000, 0)

	// leafA alone: its only path's direct-child-of-root (branchA) is pre-cutoff, so the root
	// is suppressed — matches today's single-leaf behavior.
	aloneMap, err := getAncestorChainsBatched(ctx, tc.Client, []*ent.TreeNode{leafA}, false)
	require.NoError(t, err)
	assert.NotContains(t, aloneMap, root.ID.String(), "requesting leafA alone should suppress the root")

	// leafA + leafB together: leafB's path (via branchB, post-cutoff) does not suppress, so the
	// root is kept in the shared response — even though leafA's own path would suppress it.
	requested := []*ent.TreeNode{leafA, leafB}
	batchedMap := compareBatchedToLegacy(t, ctx, tc.Client, requested, false)
	assert.Contains(t, batchedMap, root.ID.String(), "root must be kept: branchB's path does not suppress it")
}

// TestGetAncestorChainsBatched_RootSuppression_GateConditions exercises each condition
// shouldSuppressRootForChild ANDs together with the other held constant. The other suppression
// tests move the timestamp a full hour either side of the cutoff on mainnet trees only, so
// neither the network gate nor the exact cutoff ever decides the outcome on its own.
func TestGetAncestorChainsBatched_RootSuppression_GateConditions(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{22})
	owner := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	t.Run("pre-cutoff node on a non-mainnet tree keeps its root", func(t *testing.T) {
		preCutoff := ancestorChainRootSkipCutoff.Add(-time.Hour)
		tree, keyshare := createBatchedTestTreeAndKeyshare(t, ctx, tc.Client, rng, owner, btcnetwork.Regtest)
		root := createBatchedTestNode(t, ctx, tc.Client, rng, tree, nil, keyshare, owner, preCutoff, 200000, 0)
		leaf := createBatchedTestNode(t, ctx, tc.Client, rng, tree, root, keyshare, owner, preCutoff, 100000, 0)

		batchedMap := compareBatchedToLegacy(t, ctx, tc.Client, []*ent.TreeNode{leaf}, false)
		assert.Contains(t, batchedMap, root.ID.String(), "suppression needs the mainnet gate, not a pre-cutoff timestamp alone")
	})

	t.Run("node created exactly at the cutoff keeps its root", func(t *testing.T) {
		tree, keyshare := createBatchedTestTreeAndKeyshare(t, ctx, tc.Client, rng, owner, btcnetwork.Mainnet)
		root := createBatchedTestNode(t, ctx, tc.Client, rng, tree, nil, keyshare, owner, ancestorChainRootSkipCutoff, 200000, 0)
		leaf := createBatchedTestNode(t, ctx, tc.Client, rng, tree, root, keyshare, owner, ancestorChainRootSkipCutoff, 100000, 0)

		batchedMap := compareBatchedToLegacy(t, ctx, tc.Client, []*ent.TreeNode{leaf}, false)
		assert.Contains(t, batchedMap, root.ID.String(), "the cutoff is exclusive: created exactly at it is not created before it")
	})
}

// TestGetAncestorChainsBatched_RequestedNodeIsAncestorOfAnother covers a requested node that is
// itself another requested node's ancestor.
func TestGetAncestorChainsBatched_RequestedNodeIsAncestorOfAnother(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{14})
	owner := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	postCutoff := ancestorChainRootSkipCutoff.Add(time.Hour)

	tree, keyshare := createBatchedTestTreeAndKeyshare(t, ctx, tc.Client, rng, owner, btcnetwork.Mainnet)
	root := createBatchedTestNode(t, ctx, tc.Client, rng, tree, nil, keyshare, owner, postCutoff, 200000, 0)
	leaf := createBatchedTestNode(t, ctx, tc.Client, rng, tree, root, keyshare, owner, postCutoff, 100000, 0)

	// Both the root (already a top-level requested node) and the leaf are requested together.
	requested := []*ent.TreeNode{root, leaf}
	batchedMap := compareBatchedToLegacy(t, ctx, tc.Client, requested, false)
	require.Contains(t, batchedMap, root.ID.String())
}

// TestGetAncestorChainsBatched_DepthBoundErrors pins that a walk stopped by the depth bound fails
// loudly. Returning the truncated chain instead would hand a wallet an incomplete unilateral exit
// path, and getAncestorChain (which walks unbounded) would have returned the full one.
func TestGetAncestorChainsBatched_DepthBoundErrors(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{21})
	owner := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	postCutoff := ancestorChainRootSkipCutoff.Add(time.Hour)

	originalMaxDepth := ancestorChainMaxDepth
	ancestorChainMaxDepth = 2
	t.Cleanup(func() { ancestorChainMaxDepth = originalMaxDepth })

	// buildChain returns the deepest leaf of a chain of ancestorCount+1 nodes.
	buildChain := func(t *testing.T, ancestorCount int) *ent.TreeNode {
		t.Helper()
		tree, keyshare := createBatchedTestTreeAndKeyshare(t, ctx, tc.Client, rng, owner, btcnetwork.Regtest)
		var node *ent.TreeNode
		for i := 0; i <= ancestorCount; i++ {
			node = createBatchedTestNode(t, ctx, tc.Client, rng, tree, node, keyshare, owner, postCutoff, 100000, int16(i))
		}
		return node
	}

	t.Run("chain ending exactly at the bound is complete, not truncated", func(t *testing.T) {
		// Deepest ancestor sits at depth == ancestorChainMaxDepth. The walk reached the root, so
		// this must succeed even though it touched the bound.
		leaf := buildChain(t, ancestorChainMaxDepth+1)
		batchedMap := compareBatchedToLegacy(t, ctx, tc.Client, []*ent.TreeNode{leaf}, false)
		assert.Len(t, batchedMap, ancestorChainMaxDepth+1, "every ancestor above the leaf is returned")
	})

	t.Run("chain past the bound errors instead of truncating", func(t *testing.T) {
		leaf := buildChain(t, ancestorChainMaxDepth+2)

		_, err := getAncestorChainsBatched(ctx, tc.Client, []*ent.TreeNode{leaf}, false)
		require.Error(t, err, "a walk stopped by the depth bound must not return a truncated chain")
		assert.Contains(t, err.Error(), "depth bound")

		// The legacy path walks unbounded, so it still resolves the full chain.
		legacyMap := legacyAncestorChain(t, ctx, tc.Client, []*ent.TreeNode{leaf}, false)
		assert.Len(t, legacyMap, ancestorChainMaxDepth+2, "legacy resolves every ancestor above the leaf")
	})
}

func TestGetAncestorChainsBatched_EmptyInput(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	batchedMap, err := getAncestorChainsBatched(ctx, tc.Client, nil, false)
	require.NoError(t, err)
	assert.Empty(t, batchedMap)
}
