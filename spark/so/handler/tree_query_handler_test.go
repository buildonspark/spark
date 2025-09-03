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
	rng := rand.NewChaCha8([32]byte{})

	randomPrivKey1 := keys.MustGeneratePrivateKeyFromRand(rng)
	randomPrivKey2 := keys.MustGeneratePrivateKeyFromRand(rng)
	randomPrivKey3 := keys.MustGeneratePrivateKeyFromRand(rng)
	identityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	identityPubKey2 := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	secretShare := keys.MustGeneratePrivateKeyFromRand(rng)

	signingKeyshare1, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secretShare.Serialize()).
		SetPublicShares(map[string][]byte{"test": secretShare.Public().Serialize()}).
		SetPublicKey(randomPrivKey1.Public().Serialize()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)
	signingKeyshare2, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secretShare.Serialize()).
		SetPublicShares(map[string][]byte{"test": secretShare.Public().Serialize()}).
		SetPublicKey(randomPrivKey2.Public().Serialize()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)
	signingKeyshare3, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secretShare.Serialize()).
		SetPublicShares(map[string][]byte{"test": secretShare.Public().Serialize()}).
		SetPublicKey(randomPrivKey3.Public().Serialize()).
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
