package ent_test

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/require"
)

func newTestGrant(t *testing.T, ctx context.Context, client *ent.Client) *ent.DelegationGrant {
	t.Helper()
	grant, err := client.DelegationGrant.Create().
		SetOwnerIdentityPubkey(keys.GeneratePrivateKey().Public()).
		SetNetwork(btcnetwork.Regtest).
		SetExpiryTime(time.Now().Add(time.Hour)).
		SetScopeTransfer(true).
		SetScopeRenew(false).
		SetScopeClaim(false).
		SetFeeFlatSats(0).
		SetVersion(1).
		SetOwnerSignature([]byte{0x01, 0x02, 0x03}).
		Save(ctx)
	require.NoError(t, err)
	return grant
}

func newTestLeaf(t *testing.T, ctx context.Context, client *ent.Client) *ent.TreeNode {
	t.Helper()
	rawTx, err := hex.DecodeString(sampleRefundTxHex)
	require.NoError(t, err)

	tree, err := client.Tree.Create().
		SetID(uuid.New()).
		SetNetwork(btcnetwork.Regtest).
		SetStatus(st.TreeStatusAvailable).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		SetOwnerIdentityPubkey(keys.GeneratePrivateKey().Public()).
		Save(ctx)
	require.NoError(t, err)

	secret := keys.GeneratePrivateKey()
	keyshare, err := client.SigningKeyshare.Create().
		SetID(uuid.New()).
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secret).
		SetPublicShares(map[string]keys.Public{"1": secret.Public()}).
		SetPublicKey(secret.Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	node, err := client.TreeNode.Create().
		SetID(uuid.New()).
		SetTree(tree).
		SetNetwork(btcnetwork.Regtest).
		SetSigningKeyshare(keyshare).
		SetValue(500).
		SetVerifyingPubkey(keys.GeneratePrivateKey().Public()).
		SetOwnerIdentityPubkey(keys.GeneratePrivateKey().Public()).
		SetOwnerSigningPubkey(keys.GeneratePrivateKey().Public()).
		SetRawTx(rawTx).
		SetRawRefundTx(rawTx).
		SetVout(0).
		SetStatus(st.TreeNodeStatusAvailable).
		Save(ctx)
	require.NoError(t, err)
	return node
}

// TestOneActiveDecompositionPerLeaf verifies the partial-unique index that lets a
// leaf carry only one ACTIVE delegate-path decomposition at a time, while still
// allowing a fresh ACTIVE decomposition once a prior one is tombstoned.
func TestOneActiveDecompositionPerLeaf(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	client := dbCtx.Client

	grant := newTestGrant(t, ctx, client)
	leaf := newTestLeaf(t, ctx, client)

	_, err := client.LeafDecomposition.Create().
		SetDelegateSigningPubkey(keys.GeneratePrivateKey().Public()).
		SetTreeNode(leaf).
		SetDelegationGrant(grant).
		Save(ctx)
	require.NoError(t, err)

	// A second ACTIVE decomposition on the same leaf must be rejected.
	_, err = client.LeafDecomposition.Create().
		SetDelegateSigningPubkey(keys.GeneratePrivateKey().Public()).
		SetTreeNode(leaf).
		SetDelegationGrant(grant).
		Save(ctx)
	require.Error(t, err)
	require.True(t, ent.IsConstraintError(err), "expected a unique-constraint error, got: %v", err)

	// Tombstoning the first frees the leaf for a new ACTIVE decomposition.
	first, err := client.LeafDecomposition.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, first, 1)
	_, err = first[0].Update().SetStatus(st.LeafDecompositionStatusConsumed).Save(ctx)
	require.NoError(t, err)

	_, err = client.LeafDecomposition.Create().
		SetDelegateSigningPubkey(keys.GeneratePrivateKey().Public()).
		SetTreeNode(leaf).
		SetDelegationGrant(grant).
		Save(ctx)
	require.NoError(t, err)
}

// TestOneActiveSpenderRecordPerGrantSpender verifies the partial-unique index
// enforcing at most one ACTIVE authorization record per (grant, spender), while
// permitting re-adding a spender after its prior record is tombstoned.
func TestOneActiveSpenderRecordPerGrantSpender(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	client := dbCtx.Client

	grant := newTestGrant(t, ctx, client)
	spenderKey := keys.GeneratePrivateKey().Public()

	create := func() *ent.DelegationGrantSpenderCreate {
		return client.DelegationGrantSpender.Create().
			SetSpenderIdentityPubkey(spenderKey).
			SetDelegationGrant(grant).
			SetPerTxCapSats(100).
			SetRollingLimitSats(500).
			SetRollingWindowSeconds(86400).
			SetVersion(1).
			SetOwnerSignature([]byte{0x01, 0x02, 0x03})
	}

	_, err := create().Save(ctx)
	require.NoError(t, err)

	// A second ACTIVE record for the same (grant, spender) must be rejected.
	_, err = create().Save(ctx)
	require.Error(t, err)
	require.True(t, ent.IsConstraintError(err), "expected a unique-constraint error, got: %v", err)

	// A different spender on the same grant is fine.
	_, err = client.DelegationGrantSpender.Create().
		SetSpenderIdentityPubkey(keys.GeneratePrivateKey().Public()).
		SetDelegationGrant(grant).
		SetPerTxCapSats(100).
		SetRollingLimitSats(500).
		SetRollingWindowSeconds(86400).
		SetVersion(1).
		SetOwnerSignature([]byte{0x04}).
		Save(ctx)
	require.NoError(t, err)

	// Tombstoning the active record frees the (grant, spender) pair for re-add.
	existing, err := client.DelegationGrantSpender.Query().All(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, existing)
	for _, s := range existing {
		if s.SpenderIdentityPubkey.Equals(spenderKey) && s.Status == st.DelegationStatusActive {
			_, err = s.Update().SetStatus(st.DelegationStatusRevoked).Save(ctx)
			require.NoError(t, err)
		}
	}

	_, err = create().SetVersion(2).Save(ctx)
	require.NoError(t, err)
}
