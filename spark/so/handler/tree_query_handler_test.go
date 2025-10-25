package handler

import (
	"context"
	"encoding/hex"
	"math/rand/v2"
	"testing"

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
)

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
		SetNetwork(st.NetworkRegtest).
		SetIsStatic(true).
		Save(ctx)
	require.NoError(t, err)
	_, err = tx.DepositAddress.Create().
		SetAddress("bcrt1q043w4fkg4w0jl6fxrx0kd4ww3rsq2tm4mtmv9e").
		SetOwnerIdentityPubkey(identityPubKey).
		SetOwnerSigningPubkey(randomPrivKey2.Public()).
		SetSigningKeyshare(signingKeyshare2).
		SetNetwork(st.NetworkRegtest).
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
		SetNetwork(st.NetworkRegtest).
		SetIsStatic(true).
		Save(ctx)
	require.NoError(t, err)
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
	tree, err := tx.Tree.Create().
		SetOwnerIdentityPubkey(identityPubKey).
		SetNetwork(st.NetworkRegtest).
		SetStatus(st.TreeStatusAvailable).
		SetBaseTxid([]byte{1, 2, 3, 4}).
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
	}

	// Create tree nodes with different statuses
	createdNodes := make(map[st.TreeNodeStatus]*ent.TreeNode)
	for _, tt := range statusTests {
		node, err := tx.TreeNode.Create().
			SetTree(tree).
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

	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(identityPubKey.Serialize()), 9999999999)

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
	for _, node := range resp.Nodes {
		foundStatuses[node.Status] = true
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
	require.Len(t, respWithStatus.Nodes, 2)
	for _, node := range respWithStatus.Nodes {
		require.Contains(t, []string{
			string(st.TreeNodeStatusAvailable),
			string(st.TreeNodeStatusFrozenByIssuer),
		}, node.Status, "Node should have one of the requested statuses")
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
	require.Len(t, respByIDs.Nodes, len(createdNodes))
	allStatusesFound := make(map[string]bool)
	for _, node := range respByIDs.Nodes {
		allStatusesFound[node.Status] = true
	}

	// Verify all statuses are present in the response
	for _, tt := range statusTests {
		expectedStatusString := string(tt.status)
		t.Logf("Status %s should be found when querying by node IDs", expectedStatusString)
		require.True(t, allStatusesFound[expectedStatusString],
			"Status %s should be found when querying by node IDs", expectedStatusString)
	}
}

// createTestContextWithKnobsBypassed creates a test context with knobs that always return true for privacy
func createTestContextWithKnobsBypassed(t *testing.T) (context.Context, *so.Config) {
	ctx, _ := db.NewTestSQLiteContext(t)
	cfg := sparktesting.TestConfig(t)

	// Create fixed knobs that always enable privacy (bypass knob check)
	fixedKnobs := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobPrivacyEnabled: 100, // 100% rollout = always enabled
	})
	ctx = knobs.InjectKnobsService(ctx, fixedKnobs)

	return ctx, cfg
}

// PrivacyTestData contains all the test data needed for privacy tests
type PrivacyTestData struct {
	OwnerIdentityPubKey     keys.Public
	RequesterIdentityPubKey keys.Public
	Node                    *ent.TreeNode
	WalletSetting           *ent.WalletSetting
}

// createPrivacyTestData creates all the necessary test data for privacy tests
func createPrivacyTestData(t *testing.T, privacyEnabled bool, sameRequesterAndOwner bool, injectSession bool) (context.Context, *so.Config, *PrivacyTestData) {
	// Create test context and config
	ctx, cfg := createTestContextWithKnobsBypassed(t)
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
	tree, err := tx.Tree.Create().
		SetOwnerIdentityPubkey(ownerIdentityPubKey).
		SetNetwork(st.NetworkRegtest).
		SetStatus(st.TreeStatusAvailable).
		SetBaseTxid([]byte{1, 2, 3, 4}).
		SetVout(1).
		Save(ctx)
	require.NoError(t, err)

	// Create test transaction bytes
	rawTx := createOldBitcoinTxBytes(t, verifyingPubKey)
	refundTx := createOldBitcoinTxBytes(t, signingPubKey)

	// Create tree node
	node, err := tx.TreeNode.Create().
		SetTree(tree).
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
	walletSetting, err := tx.WalletSetting.
		Create().
		SetOwnerIdentityPublicKey(ownerIdentityPubKey).
		SetPrivateEnabled(privacyEnabled).
		Save(ctx)
	require.NoError(t, err)

	// Set up session context for the requester if requested
	if injectSession {
		ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(requesterIdentityPubKey.Serialize()), 9999999999)
	}

	return ctx, cfg, &PrivacyTestData{
		OwnerIdentityPubKey:     ownerIdentityPubKey,
		RequesterIdentityPubKey: requesterIdentityPubKey,
		Node:                    node,
		WalletSetting:           walletSetting,
	}
}

func TestQueryNodes_PrivacyEnabled_OwnerIdentityPubkey(t *testing.T) {
	// Create test data with privacy enabled and different requester/owner
	ctx, cfg, testData := createPrivacyTestData(t, true, false, true)

	// Create handler
	handler := NewTreeQueryHandler(cfg)

	// Test QueryNodes with owner identity pubkey - should return empty results
	req := &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_OwnerIdentityPubkey{
			OwnerIdentityPubkey: testData.OwnerIdentityPubKey.Serialize(),
		},
		Network: pb.Network_REGTEST,
		Limit:   100,
	}

	resp, err := handler.QueryNodes(ctx, req, false)
	require.NoError(t, err)
	assert.Empty(t, resp.Nodes, "Should return empty results when owner has privacy enabled and requester is different")
}

func TestQueryNodes_PrivacyDisabled_OwnerIdentityPubkey(t *testing.T) {
	// Create test data with privacy disabled and different requester/owner
	ctx, cfg, testData := createPrivacyTestData(t, false, false, true)

	// Create handler
	handler := NewTreeQueryHandler(cfg)

	// Test QueryNodes with owner identity pubkey - should return nodes
	req := &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_OwnerIdentityPubkey{
			OwnerIdentityPubkey: testData.OwnerIdentityPubKey.Serialize(),
		},
		Network: pb.Network_REGTEST,
		Limit:   100,
	}

	resp, err := handler.QueryNodes(ctx, req, false)
	require.NoError(t, err)
	assert.Len(t, resp.Nodes, 1, "Should return nodes when owner has privacy disabled")
	assert.Equal(t, testData.Node.ID.String(), resp.Nodes[testData.Node.ID.String()].Id)
}

func TestQueryNodes_OwnerCanSeeOwnNodes(t *testing.T) {
	// Create test data with privacy enabled and same requester/owner
	ctx, cfg, testData := createPrivacyTestData(t, true, true, true)

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
	assert.Len(t, resp.Nodes, 1, "Owner should be able to see their own nodes even with privacy enabled")
	assert.Equal(t, testData.Node.ID.String(), resp.Nodes[testData.Node.ID.String()].Id)
}

func TestQueryNodes_SSPBypassPrivacy(t *testing.T) {
	// Create test data with privacy enabled and different requester/owner
	ctx, cfg, testData := createPrivacyTestData(t, true, false, false)

	// Create handler
	handler := NewTreeQueryHandler(cfg)

	// Test QueryNodes with isSSP=true - should bypass privacy and return nodes
	req := &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_OwnerIdentityPubkey{
			OwnerIdentityPubkey: testData.OwnerIdentityPubKey.Serialize(),
		},
		Network: pb.Network_REGTEST,
		Limit:   100,
	}

	resp, err := handler.QueryNodes(ctx, req, true) // isSSP=true
	require.NoError(t, err)
	assert.Len(t, resp.Nodes, 1, "SSP should be able to see nodes even when owner has privacy enabled")
	assert.Equal(t, testData.Node.ID.String(), resp.Nodes[testData.Node.ID.String()].Id)
}

func TestQueryBalance_PrivacyEnabled_DifferentRequester(t *testing.T) {
	// Create test data with privacy enabled and different requester/owner
	ctx, cfg, testData := createPrivacyTestData(t, true, false, true)

	// Create handler
	handler := NewTreeQueryHandler(cfg)

	// Test QueryBalance with different requester - should return empty balance
	req := &pb.QueryBalanceRequest{
		IdentityPublicKey: testData.OwnerIdentityPubKey.Serialize(),
		Network:           pb.Network_REGTEST,
	}

	resp, err := handler.QueryBalance(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), resp.Balance, "Balance should be 0 when privacy is enabled and requester is not the owner")
	assert.Empty(t, resp.NodeBalances, "NodeBalances should be empty when privacy is enabled and requester is not the owner")
}

func TestQueryBalance_PrivacyDisabled_DifferentRequester(t *testing.T) {
	// Create test data with privacy disabled and different requester/owner
	ctx, cfg, testData := createPrivacyTestData(t, false, false, true)

	// Create handler
	handler := NewTreeQueryHandler(cfg)

	// Test QueryBalance with different requester - should return actual balance
	req := &pb.QueryBalanceRequest{
		IdentityPublicKey: testData.OwnerIdentityPubKey.Serialize(),
		Network:           pb.Network_REGTEST,
	}

	resp, err := handler.QueryBalance(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, testData.Node.Value, resp.Balance, "Balance should be returned when privacy is disabled")
	assert.Len(t, resp.NodeBalances, 1, "NodeBalances should contain the node when privacy is disabled")
	assert.Equal(t, testData.Node.Value, resp.NodeBalances[testData.Node.ID.String()])
}

func TestQueryBalance_PrivacyEnabled_OwnerCanSeeOwnBalance(t *testing.T) {
	// Create test data with privacy enabled and same requester/owner
	ctx, cfg, testData := createPrivacyTestData(t, true, true, true)

	// Create handler
	handler := NewTreeQueryHandler(cfg)

	// Test QueryBalance with owner as requester - should return actual balance
	req := &pb.QueryBalanceRequest{
		IdentityPublicKey: testData.OwnerIdentityPubKey.Serialize(),
		Network:           pb.Network_REGTEST,
	}

	resp, err := handler.QueryBalance(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, testData.Node.Value, resp.Balance, "Owner should be able to see their own balance even when privacy is enabled")
	assert.Len(t, resp.NodeBalances, 1, "Owner should be able to see their own node balances even when privacy is enabled")
	assert.Equal(t, testData.Node.Value, resp.NodeBalances[testData.Node.ID.String()])
}

func TestQueryBalance_NoSession(t *testing.T) {
	// Create test data with privacy enabled but no session injected
	ctx, cfg, testData := createPrivacyTestData(t, true, false, false)

	// Create handler
	handler := NewTreeQueryHandler(cfg)

	// Test QueryBalance without session - should return empty balance
	req := &pb.QueryBalanceRequest{
		IdentityPublicKey: testData.OwnerIdentityPubKey.Serialize(),
		Network:           pb.Network_REGTEST,
	}

	resp, err := handler.QueryBalance(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), resp.Balance, "Balance should be 0 when no session is provided and privacy is enabled")
	assert.Empty(t, resp.NodeBalances, "NodeBalances should be empty when no session is provided and privacy is enabled")
}

func TestQueryNodesByValue_PrivacyEnabled_DifferentRequester(t *testing.T) {
	// Create test data with privacy enabled and different requester/owner
	ctx, cfg, testData := createPrivacyTestData(t, true, false, true)

	// Create handler
	handler := NewTreeQueryHandler(cfg)

	// Test QueryNodesByValue with different requester - should return empty result
	req := &pb.QueryNodesByValueRequest{
		OwnerIdentityPublicKey: testData.OwnerIdentityPubKey.Serialize(),
		Value:                  int64(testData.Node.Value),
		Limit:                  100,
		Offset:                 0,
	}

	resp, err := handler.QueryNodesByValue(ctx, req)
	require.NoError(t, err)
	assert.Empty(t, resp.Nodes, "Nodes should be empty when privacy is enabled and requester is not the owner")
	assert.Equal(t, int64(-1), resp.Offset, "Offset should be -1 when privacy blocks access")
}

func TestQueryNodesByValue_PrivacyDisabled_DifferentRequester(t *testing.T) {
	// Create test data with privacy disabled and different requester/owner
	ctx, cfg, testData := createPrivacyTestData(t, false, false, true)

	// Create handler
	handler := NewTreeQueryHandler(cfg)

	// Test QueryNodesByValue with different requester - should return actual nodes
	req := &pb.QueryNodesByValueRequest{
		OwnerIdentityPublicKey: testData.OwnerIdentityPubKey.Serialize(),
		Value:                  int64(testData.Node.Value),
		Limit:                  100,
		Offset:                 0,
	}

	resp, err := handler.QueryNodesByValue(ctx, req)
	require.NoError(t, err)
	assert.Len(t, resp.Nodes, 1, "Nodes should be returned when privacy is disabled")
	assert.Equal(t, testData.Node.ID.String(), resp.Nodes[testData.Node.ID.String()].Id)
	assert.Equal(t, int64(-1), resp.Offset, "Offset should be -1 when all results are returned")
}

func TestQueryNodesByValue_PrivacyEnabled_OwnerCanSeeOwnNodes(t *testing.T) {
	// Create test data with privacy enabled and same requester/owner
	ctx, cfg, testData := createPrivacyTestData(t, true, true, true)

	// Create handler
	handler := NewTreeQueryHandler(cfg)

	// Test QueryNodesByValue with owner as requester - should return actual nodes
	req := &pb.QueryNodesByValueRequest{
		OwnerIdentityPublicKey: testData.OwnerIdentityPubKey.Serialize(),
		Value:                  int64(testData.Node.Value),
		Limit:                  100,
		Offset:                 0,
	}

	resp, err := handler.QueryNodesByValue(ctx, req)
	require.NoError(t, err)
	assert.Len(t, resp.Nodes, 1, "Owner should be able to see their own nodes even when privacy is enabled")
	assert.Equal(t, testData.Node.ID.String(), resp.Nodes[testData.Node.ID.String()].Id)
	assert.Equal(t, int64(-1), resp.Offset, "Offset should be -1 when all results are returned")
}

func TestQueryNodesByValue_NoSession(t *testing.T) {
	// Create test data with privacy enabled but no session injected
	ctx, cfg, testData := createPrivacyTestData(t, true, false, false)

	// Create handler
	handler := NewTreeQueryHandler(cfg)

	// Test QueryNodesByValue without session - should return empty result
	req := &pb.QueryNodesByValueRequest{
		OwnerIdentityPublicKey: testData.OwnerIdentityPubKey.Serialize(),
		Value:                  int64(testData.Node.Value),
		Limit:                  100,
		Offset:                 0,
	}

	resp, err := handler.QueryNodesByValue(ctx, req)
	require.NoError(t, err)
	assert.Empty(t, resp.Nodes, "Nodes should be empty when no session is provided and privacy is enabled")
	assert.Equal(t, int64(-1), resp.Offset, "Offset should be -1 when privacy blocks access")
}
