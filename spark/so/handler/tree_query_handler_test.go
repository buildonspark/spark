package handler

import (
	"math/rand/v2"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func TestQueryStaticDepositAddresses(t *testing.T) {
	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()

	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Create test data
	randomPrivKey1 := keys.MustGeneratePrivateKeyFromRand(rand.NewChaCha8([32]byte{1}))
	randomPrivKey2 := keys.MustGeneratePrivateKeyFromRand(rand.NewChaCha8([32]byte{2}))
	randomPrivKey3 := keys.MustGeneratePrivateKeyFromRand(rand.NewChaCha8([32]byte{3}))
	signingKeyshare1, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare([]byte("test_secret_share")).
		SetPublicShares(map[string][]byte{"test": []byte("test_public_share")}).
		SetPublicKey(randomPrivKey1.Public().Serialize()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)
	signingKeyshare2, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare([]byte("test_secret_share")).
		SetPublicShares(map[string][]byte{"test": []byte("test_public_share")}).
		SetPublicKey(randomPrivKey2.Public().Serialize()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)
	signingKeyshare3, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare([]byte("test_secret_share")).
		SetPublicShares(map[string][]byte{"test": []byte("test_public_share")}).
		SetPublicKey(randomPrivKey3.Public().Serialize()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)
	_, err = tx.DepositAddress.Create().
		SetAddress("bcrt1qfpk6cxxfr49wtvzxd72ahe2xtu7gj6vx7m0ksy").
		SetOwnerIdentityPubkey([]byte("test_identity_pubkey")).
		SetOwnerSigningPubkey(randomPrivKey1.Public().Serialize()).
		SetSigningKeyshare(signingKeyshare1).
		SetNetwork(st.NetworkRegtest).
		SetIsStatic(true).
		Save(ctx)
	require.NoError(t, err)
	_, err = tx.DepositAddress.Create().
		SetAddress("bcrt1q043w4fkg4w0jl6fxrx0kd4ww3rsq2tm4mtmv9e").
		SetOwnerIdentityPubkey([]byte("test_identity_pubkey")).
		SetOwnerSigningPubkey(randomPrivKey2.Public().Serialize()).
		SetSigningKeyshare(signingKeyshare2).
		SetNetwork(st.NetworkRegtest).
		SetIsStatic(true).
		Save(ctx)
	require.NoError(t, err)
	// This is a different identity pubkey, so it should not be returned
	_, err = tx.DepositAddress.Create().
		SetAddress("bcrt1q043w4fkg4w0jl6fxrx0kd4ww3rsq2tm4mtmv9d").
		SetOwnerIdentityPubkey([]byte("test_identity_pubkey2")).
		SetOwnerSigningPubkey(randomPrivKey2.Public().Serialize()).
		SetSigningKeyshare(signingKeyshare3).
		SetNetwork(st.NetworkRegtest).
		SetIsStatic(true).
		Save(ctx)
	require.NoError(t, err)
}
