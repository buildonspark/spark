package handler_test

import (
	"context"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/handler"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

// createTestSigningKeyshare creates a test signing keyshare for use in tests.
func createTestSigningKeyshare(t *testing.T, ctx context.Context, rng io.Reader, client *ent.Client) *ent.SigningKeyshare {
	publicKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	secret := keys.MustGeneratePrivateKeyFromRand(rng)

	return client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secret.Serialize()).
		SetPublicShares(map[string]keys.Public{"test": secret.Public()}).
		SetPublicKey(publicKey).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		SaveX(ctx)
}

func TestReserveEntityDkgKey_Success(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{})

	signingKeyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	keyHandler := handler.NewEntityDkgKeyHandler(cfg)

	// Test successful reservation
	req := &pbinternal.ReserveEntityDkgKeyRequest{
		KeyshareId: signingKeyshare.ID.String(),
	}

	err := keyHandler.ReserveEntityDkgKey(ctx, req)
	require.NoError(t, err)

	// Commit the transaction to persist changes
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	// Verify entity DKG key was created
	entityDkgKey, err := sessionCtx.Client.EntityDkgKey.Query().WithSigningKeyshare().Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, signingKeyshare.ID, entityDkgKey.Edges.SigningKeyshare.ID)

	// Verify signing keyshare was marked as used
	updatedKeyshare, err := sessionCtx.Client.SigningKeyshare.Get(ctx, signingKeyshare.ID)
	require.NoError(t, err)
	assert.Equal(t, st.KeyshareStatusInUse, updatedKeyshare.Status)
}

func TestReserveEntityDkgKey_Idempotent(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{})
	signingKeyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	keyHandler := handler.NewEntityDkgKeyHandler(cfg)

	req := &pbinternal.ReserveEntityDkgKeyRequest{
		KeyshareId: signingKeyshare.ID.String(),
	}

	// First call - should succeed and create EntityDkgKey
	err := keyHandler.ReserveEntityDkgKey(ctx, req)
	require.NoError(t, err)

	// Second call - should be idempotent and access existing EntityDkgKey
	err = keyHandler.ReserveEntityDkgKey(ctx, req)
	require.NoError(t, err, "should not error on idempotent call")

	// Commit the transaction to persist changes
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	// Verify only one entity DKG key exists
	count, err := sessionCtx.Client.EntityDkgKey.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "should have exactly one EntityDkgKey")
}

func TestReserveEntityDkgKey_ConflictingKeyshareID(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{})
	signingKeyshare1 := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	signingKeyshare2 := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)

	keyHandler := handler.NewEntityDkgKeyHandler(cfg)

	// First call - create EntityDkgKey with first keyshare
	req1 := &pbinternal.ReserveEntityDkgKeyRequest{
		KeyshareId: signingKeyshare1.ID.String(),
	}
	err := keyHandler.ReserveEntityDkgKey(ctx, req1)
	require.NoError(t, err)

	// Commit the first transaction to persist changes
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	err = tx.Commit()
	require.NoError(t, err)

	// Second call - try to reserve with different keyshare ID
	req2 := &pbinternal.ReserveEntityDkgKeyRequest{
		KeyshareId: signingKeyshare2.ID.String(),
	}
	err = keyHandler.ReserveEntityDkgKey(ctx, req2)
	require.ErrorContains(t, err, "entity DKG key already reserved with different keyshare ID")
}

func TestReserveEntityDkgKey_InvalidUUID(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)

	keyHandler := handler.NewEntityDkgKeyHandler(cfg)

	// Test with invalid UUID format
	req := &pbinternal.ReserveEntityDkgKeyRequest{
		KeyshareId: "invalid-uuid",
	}

	err := keyHandler.ReserveEntityDkgKey(ctx, req)
	require.ErrorContains(t, err, "invalid DKG key ID format")
}

func TestReserveEntityDkgKey_DatabaseContextError(t *testing.T) {
	// Test with a context that doesn't have a database transaction
	cfg := sparktesting.TestConfig(t)
	keyHandler := handler.NewEntityDkgKeyHandler(cfg)

	req := &pbinternal.ReserveEntityDkgKeyRequest{
		KeyshareId: uuid.New().String(),
	}

	err := keyHandler.ReserveEntityDkgKey(t.Context(), req)
	require.ErrorContains(t, err, "failed to get or create current tx for request")
}
