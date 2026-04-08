package ent_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/require"
)

func createSigningKeyshareForAggregateTest(t *testing.T, ctx context.Context, client *ent.Client, secretVersion *int32) *ent.SigningKeyshare {
	t.Helper()

	secret := keys.GeneratePrivateKey()
	createQuery := client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secret).
		SetPublicShares(map[string]keys.Public{"op": secret.Public()}).
		SetPublicKey(secret.Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(0)
	if secretVersion != nil {
		createQuery = createQuery.SetSecretVersion(*secretVersion)
	}

	keyshare, err := createQuery.Save(ctx)
	require.NoError(t, err)
	return keyshare
}

func TestAggregateKeyshares_ClearsSecretVersion(t *testing.T) {
	t.Parallel()

	ctx, _ := db.NewTestSQLiteContext(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	version := int32(7)
	first := createSigningKeyshareForAggregateTest(t, ctx, client, &version)
	second := createSigningKeyshareForAggregateTest(t, ctx, client, &version)
	targetVersion := int32(1)
	target := createSigningKeyshareForAggregateTest(t, ctx, client, &targetVersion)

	updated, err := ent.AggregateKeyshares(ctx, nil, []*ent.SigningKeyshare{first, second}, target.ID)
	require.NoError(t, err)
	require.Nil(t, updated.SecretVersion)

	expectedSecret := first.SecretShare.Add(*second.SecretShare)
	require.NotNil(t, updated.SecretShare)
	require.True(t, updated.SecretShare.Equals(expectedSecret))

	persisted, err := client.SigningKeyshare.Get(ctx, target.ID)
	require.NoError(t, err)
	require.Nil(t, persisted.SecretVersion)
	require.NotNil(t, persisted.SecretShare)
	require.True(t, persisted.SecretShare.Equals(expectedSecret))
}

func TestAggregateKeyshares_ClearsSecretVersionWhenSumVersionIsNil(t *testing.T) {
	t.Parallel()

	ctx, _ := db.NewTestSQLiteContext(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	first := createSigningKeyshareForAggregateTest(t, ctx, client, nil)
	second := createSigningKeyshareForAggregateTest(t, ctx, client, nil)
	targetVersion := int32(4)
	target := createSigningKeyshareForAggregateTest(t, ctx, client, &targetVersion)

	updated, err := ent.AggregateKeyshares(ctx, nil, []*ent.SigningKeyshare{first, second}, target.ID)
	require.NoError(t, err)
	require.Nil(t, updated.SecretVersion)

	expectedSecret := first.SecretShare.Add(*second.SecretShare)
	require.NotNil(t, updated.SecretShare)
	require.True(t, updated.SecretShare.Equals(expectedSecret))

	persisted, err := client.SigningKeyshare.Get(ctx, target.ID)
	require.NoError(t, err)
	require.Nil(t, persisted.SecretVersion)
	require.NotNil(t, persisted.SecretShare)
	require.True(t, persisted.SecretShare.Equals(expectedSecret))
}

func TestAggregateKeyshares_IgnoresInputSecretVersions(t *testing.T) {
	t.Parallel()

	ctx, _ := db.NewTestSQLiteContext(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	firstVersion := int32(7)
	secondVersion := int32(8)
	first := createSigningKeyshareForAggregateTest(t, ctx, client, &firstVersion)
	second := createSigningKeyshareForAggregateTest(t, ctx, client, &secondVersion)
	targetVersion := int32(4)
	target := createSigningKeyshareForAggregateTest(t, ctx, client, &targetVersion)

	originalSecret := *target.SecretShare

	updated, err := ent.AggregateKeyshares(ctx, nil, []*ent.SigningKeyshare{first, second}, target.ID)
	require.NoError(t, err)
	require.Nil(t, updated.SecretVersion)

	expectedSecret := first.SecretShare.Add(*second.SecretShare)
	require.NotNil(t, updated.SecretShare)
	require.True(t, updated.SecretShare.Equals(expectedSecret))

	persisted, err := client.SigningKeyshare.Get(ctx, target.ID)
	require.NoError(t, err)
	require.Nil(t, persisted.SecretVersion)
	require.NotNil(t, persisted.SecretShare)
	require.True(t, persisted.SecretShare.Equals(expectedSecret))
	require.False(t, persisted.SecretShare.Equals(originalSecret))
}

func TestCalculateAndStoreLastKey_ClearsSecretVersion(t *testing.T) {
	t.Parallel()

	ctx, _ := db.NewTestSQLiteContext(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	version := int32(9)
	first := createSigningKeyshareForAggregateTest(t, ctx, client, &version)

	lastSecret := keys.GeneratePrivateKey()
	targetSecret := first.SecretShare.Add(lastSecret)
	targetPublic := first.PublicKey.Add(lastSecret.Public())
	targetPublicShares := map[string]keys.Public{
		"op": first.PublicShares["op"].Add(lastSecret.Public()),
	}
	target := &ent.SigningKeyshare{
		SecretShare:  &targetSecret,
		PublicKey:    targetPublic,
		PublicShares: targetPublicShares,
		MinSigners:   1,
	}

	lastKeyID := uuid.New()
	lastKey, err := ent.CalculateAndStoreLastKey(ctx, nil, target, []*ent.SigningKeyshare{first}, lastKeyID)
	require.NoError(t, err)
	require.Nil(t, lastKey.SecretVersion)
	require.NotNil(t, lastKey.SecretShare)
	require.True(t, lastKey.SecretShare.Equals(lastSecret))

	persisted, err := client.SigningKeyshare.Get(ctx, lastKeyID)
	require.NoError(t, err)
	require.Nil(t, persisted.SecretVersion)
}
