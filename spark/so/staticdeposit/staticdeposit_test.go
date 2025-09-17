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
	secret := keys.MustGeneratePrivateKeyFromRand(rng)
	pubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	// Create a SigningKeyshare (required for DepositAddress)
	keyshare, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secret.Serialize()).
		SetPublicShares(map[string]keys.Public{"test": secret.Public()}).
		SetPublicKey(pubKey).
		SetCoordinatorIndex(1).
		SetMinSigners(1).
		Save(ctx)
	require.NoError(t, err)

	// Create a DepositAddress (required for Utxo)
	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
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
		coordinatorIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		utxoSwap, err := tx.UtxoSwap.Create().
			SetStatus(utxoSwapStatus).
			SetRequestType(st.UtxoSwapRequestTypeFixedAmount).
			SetCreditAmountSats(900).
			SetMaxFeeSats(100).
			SetRequestedTransferID(uuid.Must(uuid.NewRandomFromReader(rng))).
			SetCoordinatorIdentityPublicKey(coordinatorIdentityPubKey.Serialize()).
			SetUtxo(utxo).
			Save(ctx)
		require.NoError(t, err)
		return utxo, utxoSwap
	}

	return utxo, nil
}

func TestGetRegisteredUtxoSwapForUtxo(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})

	testCases := []struct {
		name           string
		swapStatus     st.UtxoSwapStatus
		expectNil      bool
		expectedStatus st.UtxoSwapStatus
	}{
		{
			name:           "returns UtxoSwap when found with CREATED status",
			swapStatus:     st.UtxoSwapStatusCreated,
			expectNil:      false,
			expectedStatus: st.UtxoSwapStatusCreated,
		},
		{
			name:           "returns UtxoSwap when found with COMPLETED status",
			swapStatus:     st.UtxoSwapStatusCompleted,
			expectNil:      false,
			expectedStatus: st.UtxoSwapStatusCompleted,
		},
		{
			name:       "returns nil when no UtxoSwap exists for UTXO",
			swapStatus: "", // Create a test UTXO but no UtxoSwap
			expectNil:  true,
		},
		{
			name:       "returns nil when UtxoSwap exists but has CANCELLED status",
			swapStatus: st.UtxoSwapStatusCancelled, // Create entities with CANCELLED status - should be filtered out
			expectNil:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := db.NewTestSQLiteContext(t)
			tx, err := ent.GetDbFromContext(ctx)
			require.NoError(t, err)
			targetUtxo, expectedUtxoSwap := createTestEntities(t, ctx, rng, tx, tc.swapStatus)

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
