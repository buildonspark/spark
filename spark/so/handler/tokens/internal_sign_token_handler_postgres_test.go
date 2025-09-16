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

	ownerPubKey := handler.config.IdentityPublicKey().Serialize()

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
		SetOperatorIdentityPublicKey(opPub.Serialize()).
		SetSecretShare(share1.Serialize()).
		SaveX(ctx)

	return output
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
