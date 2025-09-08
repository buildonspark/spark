package staticdeposit

import (
	"context"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper function to create test entities with required dependencies
func createTestEntities(t *testing.T, ctx context.Context, rng io.Reader, tx *ent.Tx, utxoSwapStatus st.UtxoSwapStatus) (*ent.Utxo, *ent.UtxoSwap) {
	// Create a SigningKeyshare (required for DepositAddress)
	keyshare, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare([]byte("test_secret_share")).
		SetPublicShares(map[string][]byte{"test": []byte("test_public_share")}).
		SetPublicKey([]byte("test_public_key")).
		SetCoordinatorIndex(1).
		SetMinSigners(1).
		Save(ctx)
	require.NoError(t, err)

	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	// Create a DepositAddress (required for Utxo)
	depositAddress, err := tx.DepositAddress.Create().
		SetAddress("bc1ptest_static_deposit_address_for_testing").
		SetOwnerIdentityPubkey(ownerIdentityPubKey).
		SetOwnerSigningPubkey(ownerSigningPubKey).
		SetSigningKeyshare(keyshare).
		SetIsStatic(true).
		Save(ctx)
	require.NoError(t, err)

	// Create a Utxo
	utxo, err := tx.Utxo.Create().
		SetTxid([]byte("test_txid_123456789012345678901234")).
		SetVout(0).
		SetAmount(1000).
		SetNetwork(st.NetworkRegtest).
		SetPkScript([]byte("test_script")).
		SetBlockHeight(100).
		SetDepositAddress(depositAddress).
		Save(ctx)
	require.NoError(t, err)

	// Create a UtxoSwap if status is provided
	if utxoSwapStatus != "" {
		utxoSwap, err := tx.UtxoSwap.Create().
			SetStatus(utxoSwapStatus).
			SetRequestType(st.UtxoSwapRequestTypeFixedAmount).
			SetCreditAmountSats(900).
			SetMaxFeeSats(100).
			SetRequestedTransferID(uuid.New()).
			SetCoordinatorIdentityPublicKey([]byte("test_coordinator_identity_public_key")).
			SetUtxo(utxo).
			Save(ctx)
		require.NoError(t, err)
		return utxo, utxoSwap
	}

	return utxo, nil
}

func TestGetRegisteredUtxoSwapForUtxo(t *testing.T) {
	ctx := t.Context()
	rng := rand.NewChaCha8([32]byte{})

	testCases := []struct {
		name           string
		setupData      func(*ent.Tx) (*ent.Utxo, *ent.UtxoSwap)
		expectNil      bool
		expectedStatus st.UtxoSwapStatus
	}{
		{
			name: "returns UtxoSwap when found with CREATED status",
			setupData: func(client *ent.Tx) (*ent.Utxo, *ent.UtxoSwap) {
				return createTestEntities(t, ctx, rng, client, st.UtxoSwapStatusCreated)
			},
			expectNil:      false,
			expectedStatus: st.UtxoSwapStatusCreated,
		},
		{
			name: "returns UtxoSwap when found with COMPLETED status",
			setupData: func(client *ent.Tx) (*ent.Utxo, *ent.UtxoSwap) {
				return createTestEntities(t, ctx, rng, client, st.UtxoSwapStatusCompleted)
			},
			expectNil:      false,
			expectedStatus: st.UtxoSwapStatusCompleted,
		},
		{
			name: "returns nil when no UtxoSwap exists for UTXO",
			setupData: func(client *ent.Tx) (*ent.Utxo, *ent.UtxoSwap) {
				// Create a test UTXO but no UtxoSwap
				return createTestEntities(t, ctx, rng, client, "")
			},
			expectNil: true,
		},
		{
			name: "returns nil when UtxoSwap exists but has CANCELLED status",
			setupData: func(client *ent.Tx) (*ent.Utxo, *ent.UtxoSwap) {
				// Create entities with CANCELLED status - should be filtered out
				return createTestEntities(t, ctx, rng, client, st.UtxoSwapStatusCancelled)
			},
			expectNil: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
			defer dbCtx.Close()

			tx, err := ent.GetDbFromContext(ctx)
			require.NoError(t, err)

			// Setup test data
			targetUtxo, expectedUtxoSwap := tc.setupData(tx)

			// Call the function under test
			result, err := GetRegisteredUtxoSwapForUtxo(ctx, tx, targetUtxo)
			require.NoError(t, err)

			// Verify results
			if tc.expectNil {
				assert.Nil(t, result)
			} else {
				require.NotNil(t, result)
				assert.Equal(t, tc.expectedStatus, result.Status)
				// Verify it's the expected UtxoSwap
				if expectedUtxoSwap != nil {
					assert.Equal(t, expectedUtxoSwap.ID, result.ID)
				}
			}
		})
	}
}
