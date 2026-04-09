package ent_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entephemeral"
	ephemeralenttest "github.com/lightsparkdev/spark/so/entephemeral/enttest"
	"github.com/stretchr/testify/require"
)

func TestSigningKeyshareGetSecretShare_MainSecretPreferredWithoutEphemeral(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)
	secret := keys.MustParsePrivateKeyHex("adeab186b64a2239f15640cb43d7c57c35376f5e1c42f574671880a34a4a80ad")

	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, &secret, nil)
	resolved, err := keyshare.GetSecretShare(ctx)
	require.NoError(t, err)
	require.Equal(t, secret, *resolved)
}

func TestSigningKeyshareGetSecretShare_ErrWhenEphemeralUnavailable(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)
	version := int32(0)

	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, nil, &version)
	_, err := keyshare.GetSecretShare(ctx)
	require.Error(t, err)
	require.ErrorContains(t, err, "ephemeral DB is unavailable")
	require.ErrorIs(t, err, ent.ErrSigningKeyshareSecretUnavailable)
}

func TestSigningKeyshareGetSecretShare_LoadsFromEphemeralWhenMainSecretNil(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)

	ephemeralClient := ephemeralenttest.Open(t, "sqlite3", "file:ephemeral_get_secret_share_ok?mode=memory&_fk=1")
	t.Cleanup(func() {
		_ = ephemeralClient.Close()
	})

	version := int32(0)
	secret := keys.MustParsePrivateKeyHex("5ab9bcbbf7e7073f5d6fd5cb56af8f3d4f77d8a7c356c9f67018a2ac8d15f11a")
	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, nil, &version)

	_, err := ephemeralClient.SigningKeyshareSecret.Create().
		SetSigningKeyshareID(keyshare.ID).
		SetVersion(version).
		SetSecretShare(secret).
		Save(ctx)
	require.NoError(t, err)

	ctxWithEphemeral := entephemeral.Inject(ctx, db.NewReadOnlyEphemeralSession(ctx, ephemeralClient))
	resolved, err := keyshare.GetSecretShare(ctxWithEphemeral)
	require.NoError(t, err)
	require.Equal(t, secret, *resolved)
}

func TestSigningKeyshareGetSecretShare_ErrWhenEphemeralVersionMissing(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)

	ephemeralClient := ephemeralenttest.Open(t, "sqlite3", "file:ephemeral_get_secret_share_missing?mode=memory&_fk=1")
	t.Cleanup(func() {
		_ = ephemeralClient.Close()
	})

	version := int32(7)
	keyshare := mustCreateSigningKeyshare(t, ctx, tc.Client, nil, &version)

	ctxWithEphemeral := entephemeral.Inject(ctx, db.NewReadOnlyEphemeralSession(ctx, ephemeralClient))
	_, err := keyshare.GetSecretShare(ctxWithEphemeral)
	require.Error(t, err)
	require.ErrorContains(t, err, "was not found in ephemeral DB")
	require.ErrorIs(t, err, ent.ErrSigningKeyshareSecretMissing)
}

func mustCreateSigningKeyshare(
	t *testing.T,
	ctx context.Context,
	client *ent.Client,
	secret *keys.Private,
	version *int32,
) *ent.SigningKeyshare {
	t.Helper()

	publicKeySource := keys.MustParsePrivateKeyHex("e6d2b44c26c0c1b507fab0d5e66c388c5676c109b9ee41520ceba5b52e3a2a92")

	create := client.SigningKeyshare.Create().
		SetID(uuid.New()).
		SetStatus(st.KeyshareStatusAvailable).
		SetPublicShares(map[string]keys.Public{"1": publicKeySource.Public()}).
		SetPublicKey(publicKeySource.Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(0)

	if secret != nil {
		create.SetSecretShare(*secret)
	}
	if version != nil {
		create.SetSecretVersion(*version)
	}

	keyshare, err := create.Save(ctx)
	require.NoError(t, err)
	return keyshare
}
