package tokens

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"math/rand/v2"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

func TestMain(m *testing.M) {
	stop := db.StartPostgresServer()
	defer stop()

	m.Run()
}

func setUpInternalSignTokenTestHandlerPostgres(t *testing.T) (*InternalSignTokenHandler, context.Context, *ent.Tx, func()) {
	t.Helper()

	config := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	handler := &InternalSignTokenHandler{config: config}

	cleanup := func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			t.Errorf("rollback failed: %v", rollbackErr)
		}
	}

	return handler, ctx, tx, cleanup
}

// createTestSpentOutputWithShares creates a spent output with one partial share and returns it.
func createTestSpentOutputWithShares(t *testing.T, ctx context.Context, tx *ent.Tx, handler *InternalSignTokenHandler, tokenCreateID uuid.UUID, secretPriv keys.Private, shares []*secretsharing.SecretShare, operatorIDs []string) *ent.TokenOutput {
	t.Helper()
	coordinatorShare := shares[0] // index 1
	secretShare, err := keys.PrivateKeyFromBigInt(coordinatorShare.Share)
	require.NoError(t, err)

	keyshare := tx.SigningKeyshare.Create().
		SetSecretShare(secretShare.Serialize()).
		SetPublicKey(secretPriv.Public()).
		SetStatus(st.KeyshareStatusInUse).
		SetPublicShares(map[string]keys.Public{}).
		SetMinSigners(1).
		SetCoordinatorIndex(1).
		SaveX(ctx)

	ownerPubKey := handler.config.IdentityPublicKey()

	output := tx.TokenOutput.Create().
		SetID(uuid.New()).
		SetOwnerPublicKey(ownerPubKey).
		SetTokenPublicKey(ownerPubKey).
		SetTokenAmount([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 100}).
		SetRevocationKeyshare(keyshare).
		SetStatus(st.TokenOutputStatusSpentSigned).
		SetWithdrawBondSats(1).
		SetWithdrawRelativeBlockLocktime(1).
		SetWithdrawRevocationCommitment(secretPriv.Public().Serialize()).
		SetCreatedTransactionOutputVout(0).
		SetNetwork(st.NetworkRegtest).
		SetTokenIdentifier([]byte("token_identifier")).
		SetTokenCreateID(tokenCreateID).
		SetSpentTransactionInputVout(0).
		SaveX(ctx)

	// add partial share for operator 2
	opPub := handler.config.SigningOperatorMap[operatorIDs[1]].IdentityPublicKey
	share1, err := keys.PrivateKeyFromBigInt(shares[1].Share)
	require.NoError(t, err)
	tx.TokenPartialRevocationSecretShare.Create().
		SetTokenOutput(output).
		SetOperatorIdentityPublicKey(opPub).
		SetSecretShare(share1.Serialize()).
		SaveX(ctx)

	return output
}

func TestGetSecretSharesNotInInput(t *testing.T) {
	handler, ctx, tx, cleanup := setUpInternalSignTokenTestHandlerPostgres(t)
	defer cleanup()
	rng := rand.NewChaCha8([32]byte{})

	aliceOperatorPubKey := handler.config.SigningOperatorMap["0000000000000000000000000000000000000000000000000000000000000001"].IdentityPublicKey
	bobOperatorPubKey := handler.config.SigningOperatorMap["0000000000000000000000000000000000000000000000000000000000000002"].IdentityPublicKey
	carolOperatorPubKey := handler.config.SigningOperatorMap["0000000000000000000000000000000000000000000000000000000000000003"].IdentityPublicKey

	aliceSecret := keys.MustGeneratePrivateKeyFromRand(rng)
	aliceSigningKeyshare := tx.SigningKeyshare.Create().
		SetSecretShare(aliceSecret.Serialize()).
		SetPublicKey(aliceSecret.Public()).
		SetStatus(st.KeyshareStatusInUse).
		SetPublicShares(map[string]keys.Public{}).
		SetMinSigners(1).
		SetCoordinatorIndex(1).
		SaveX(ctx)

	bobSecret := keys.MustGeneratePrivateKeyFromRand(rng)
	bobSigningKeyshare := tx.SigningKeyshare.Create().
		SetSecretShare(bobSecret.Serialize()).
		SetPublicKey(bobSecret.Public()).
		SetStatus(st.KeyshareStatusInUse).
		SetPublicShares(map[string]keys.Public{}).
		SetMinSigners(1).
		SetCoordinatorIndex(1).
		SaveX(ctx)

	carolSecret := keys.MustGeneratePrivateKeyFromRand(rng)
	carolSigningKeyshare := tx.SigningKeyshare.Create().
		SetSecretShare(carolSecret.Serialize()).
		SetPublicKey(carolSecret.Public()).
		SetStatus(st.KeyshareStatusInUse).
		SetPublicShares(map[string]keys.Public{}).
		SetMinSigners(1).
		SetCoordinatorIndex(1).
		SaveX(ctx)

	// Minimal TokenCreate required for TokenOutput and TokenTransaction relationships
	tokenCreate := tx.TokenCreate.Create().
		SetIssuerPublicKey(handler.config.IdentityPublicKey()).
		SetTokenName("test token").
		SetTokenTicker("TTK").
		SetDecimals(8).
		SetMaxSupply([]byte{1}).
		SetIsFreezable(true).
		SetNetwork(st.NetworkRegtest).
		SetTokenIdentifier([]byte("token_identifier")).
		SetCreationEntityPublicKey(handler.config.IdentityPublicKey()).
		SaveX(ctx)

	withdrawRevocationCommitment := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	tokenOutputInDb := tx.TokenOutput.Create().
		SetID(uuid.New()).
		SetOwnerPublicKey(aliceOperatorPubKey).
		SetTokenPublicKey(aliceOperatorPubKey).
		SetTokenAmount([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 100}).
		SetRevocationKeyshare(aliceSigningKeyshare).
		SetStatus(st.TokenOutputStatusCreatedFinalized).
		SetWithdrawBondSats(1).
		SetWithdrawRelativeBlockLocktime(1).
		SetWithdrawRevocationCommitment(withdrawRevocationCommitment.Serialize()).
		SetCreatedTransactionOutputVout(0).
		SetNetwork(st.NetworkRegtest).
		SetTokenIdentifier([]byte("token_identifier")).
		SetTokenCreateID(tokenCreate.ID).
		SaveX(ctx)

	tx.TokenPartialRevocationSecretShare.Create().
		SetTokenOutput(tokenOutputInDb).
		SetOperatorIdentityPublicKey(bobOperatorPubKey).
		SetSecretShare(bobSigningKeyshare.SecretShare).
		SaveX(ctx)

	tx.TokenPartialRevocationSecretShare.Create().
		SetTokenOutput(tokenOutputInDb).
		SetOperatorIdentityPublicKey(carolOperatorPubKey).
		SetSecretShare(carolSigningKeyshare.SecretShare).
		SaveX(ctx)

	t.Run("returns empty map when input share map is empty", func(t *testing.T) {
		inputOperatorShareMap := make(map[ShareKey]ShareValue)

		_, err := handler.getSecretSharesNotInInput(ctx, inputOperatorShareMap)

		require.ErrorContains(t, err, "no input operator shares provided")
	})

	t.Run("excludes the revocation secret share if it is in the input", func(t *testing.T) {
		inputOperatorShareMap := make(map[ShareKey]ShareValue)
		inputOperatorShareMap[ShareKey{
			TokenOutputID:             tokenOutputInDb.ID,
			OperatorIdentityPublicKey: aliceOperatorPubKey,
		}] = ShareValue{
			SecretShare:               aliceSigningKeyshare.SecretShare,
			OperatorIdentityPublicKey: aliceOperatorPubKey,
		}

		result, err := handler.getSecretSharesNotInInput(ctx, inputOperatorShareMap)
		require.NoError(t, err)
		assert.Len(t, result, 2)
		assert.Equal(t, bobSigningKeyshare.SecretShare, result[bobOperatorPubKey][0].SecretShare)
		assert.Equal(t, carolSigningKeyshare.SecretShare, result[carolOperatorPubKey][0].SecretShare)
	})

	t.Run("excludes the partial revocation secret share if it is in the input", func(t *testing.T) {
		inputOperatorShareMap := make(map[ShareKey]ShareValue)
		inputOperatorShareMap[ShareKey{
			TokenOutputID:             tokenOutputInDb.ID,
			OperatorIdentityPublicKey: bobOperatorPubKey,
		}] = ShareValue{
			SecretShare:               bobSigningKeyshare.SecretShare,
			OperatorIdentityPublicKey: bobOperatorPubKey,
		}

		result, err := handler.getSecretSharesNotInInput(ctx, inputOperatorShareMap)
		require.NoError(t, err)
		assert.Len(t, result, 2)
		assert.Equal(t, aliceSigningKeyshare.SecretShare, result[aliceOperatorPubKey][0].SecretShare)
		assert.Equal(t, carolSigningKeyshare.SecretShare, result[carolOperatorPubKey][0].SecretShare)
	})
}

func TestRecoverFullRevocationSecretsAndFinalize_RequireThresholdOperators(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{})

	handler := &InternalSignTokenHandler{config: cfg}
	ctx, _ := db.ConnectToTestPostgres(t)
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Configure 3 operators, threshold 2.
	limitedOps := make(map[string]*so.SigningOperator)
	ids := make([]string, 3)
	for i := range ids {
		id := fmt.Sprintf("%064x", i+1)
		limitedOps[id] = handler.config.SigningOperatorMap[id]
		ids[i] = id
	}
	handler.config.SigningOperatorMap = limitedOps
	handler.config.Threshold = 2

	priv := keys.MustGeneratePrivateKeyFromRand(rng)
	secretInt := new(big.Int).SetBytes(priv.Serialize())
	shares, err := secretsharing.SplitSecret(secretInt, secp256k1.S256().N, 2, 3)
	require.NoError(t, err)

	tokenCreate := tx.TokenCreate.Create().
		SetIssuerPublicKey(handler.config.IdentityPublicKey()).
		SetTokenName("test token").
		SetTokenTicker("TTK").
		SetDecimals(8).
		SetMaxSupply([]byte{1}).
		SetIsFreezable(true).
		SetNetwork(st.NetworkRegtest).
		SetTokenIdentifier([]byte("token_identifier")).
		SetCreationEntityPublicKey(handler.config.IdentityPublicKey()).
		SaveX(ctx)

	output := createTestSpentOutputWithShares(t, ctx, tx, handler, tokenCreate.ID, priv, shares, ids)
	hash := bytes.Repeat([]byte{0x24}, 32)
	_ = tx.TokenTransaction.Create().
		SetCreateID(tokenCreate.ID).
		SetPartialTokenTransactionHash(hash).
		SetFinalizedTokenTransactionHash(hash).
		SetStatus(st.TokenTransactionStatusSigned).
		AddSpentOutput(output).
		SaveX(ctx)

	// Commit so data visible in new transaction.
	require.NoError(t, tx.Commit())
	t.Run("flag false does not finalize when threshold requirement disabled", func(t *testing.T) {
		handler.config.Token.RequireThresholdOperators = false
		finalized, err := handler.recoverFullRevocationSecretsAndFinalize(ctx, hash)
		require.NoError(t, err)
		assert.False(t, finalized)
	})
	t.Run("flag true finalizes when threshold requirement enabled", func(t *testing.T) {
		handler.config.Token.RequireThresholdOperators = true
		finalized, err := handler.recoverFullRevocationSecretsAndFinalize(ctx, hash)
		require.NoError(t, err)
		assert.True(t, finalized)
	})
}
