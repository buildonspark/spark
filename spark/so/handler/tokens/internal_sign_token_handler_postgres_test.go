package tokens

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entfixtures"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

func TestMain(m *testing.M) {
	stop := db.StartPostgresServer()
	defer stop()

	m.Run()
}

type internalSignTokenPostgresTestSetup struct {
	handler  *InternalSignTokenHandler
	ctx      context.Context
	client   *ent.Client
	fixtures *entfixtures.Fixtures
}

func setUpInternalSignTokenTestHandlerPostgres(t *testing.T) *internalSignTokenPostgresTestSetup {
	t.Helper()

	config := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	return &internalSignTokenPostgresTestSetup{
		handler:  &InternalSignTokenHandler{config: config},
		ctx:      ctx,
		client:   dbClient,
		fixtures: entfixtures.New(t, ctx, dbClient),
	}
}

// createTestSpentOutputWithShares creates a spent output with one partial share and returns it.
func createTestSpentOutputWithShares(t *testing.T, setup *internalSignTokenPostgresTestSetup, tokenCreate *ent.TokenCreate, secretPriv keys.Private, shares []*secretsharing.SecretShare, operatorIDs []string) *ent.TokenOutput {
	t.Helper()
	coordinatorShare := shares[0]
	secretShare, err := keys.PrivateKeyFromBigInt(coordinatorShare.Share)
	require.NoError(t, err)

	keyshare := setup.client.SigningKeyshare.Create().
		SetSecretShare(secretShare).
		SetPublicKey(secretPriv.Public()).
		SetStatus(st.KeyshareStatusInUse).
		SetPublicShares(map[string]keys.Public{}).
		SetMinSigners(1).
		SetCoordinatorIndex(1).
		SaveX(setup.ctx)

	txHash := setup.fixtures.RandomBytes(32)
	tokenTx := setup.client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(txHash).
		SetFinalizedTokenTransactionHash(txHash).
		SetStatus(st.TokenTransactionStatusFinalized).
		SetCreateID(tokenCreate.ID).
		SaveX(setup.ctx)

	ownerPubKey := setup.handler.config.IdentityPublicKey()

	output := setup.client.TokenOutput.Create().
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
		SetOutputCreatedTokenTransaction(tokenTx).
		SetCreatedTransactionFinalizedHash(tokenTx.FinalizedTokenTransactionHash).
		SetNetwork(btcnetwork.Regtest).
		SetTokenIdentifier(tokenCreate.TokenIdentifier).
		SetTokenCreateID(tokenCreate.ID).
		SetSpentTransactionInputVout(0).
		SaveX(setup.ctx)

	opPub := setup.handler.config.SigningOperatorMap[operatorIDs[1]].IdentityPublicKey
	share1, err := keys.PrivateKeyFromBigInt(shares[1].Share)
	require.NoError(t, err)
	setup.client.TokenPartialRevocationSecretShare.Create().
		SetTokenOutput(output).
		SetOperatorIdentityPublicKey(opPub).
		SetSecretShare(share1).
		SaveX(setup.ctx)

	return output
}

func TestGetSecretSharesNotInInput(t *testing.T) {
	setup := setUpInternalSignTokenTestHandlerPostgres(t)

	aliceOperatorPubKey := setup.handler.config.SigningOperatorMap["0000000000000000000000000000000000000000000000000000000000000001"].IdentityPublicKey
	bobOperatorPubKey := setup.handler.config.SigningOperatorMap["0000000000000000000000000000000000000000000000000000000000000002"].IdentityPublicKey
	carolOperatorPubKey := setup.handler.config.SigningOperatorMap["0000000000000000000000000000000000000000000000000000000000000003"].IdentityPublicKey

	aliceSecret := setup.fixtures.GeneratePrivateKey()
	aliceSigningKeyshare := setup.client.SigningKeyshare.Create().
		SetSecretShare(aliceSecret).
		SetPublicKey(aliceSecret.Public()).
		SetStatus(st.KeyshareStatusInUse).
		SetPublicShares(map[string]keys.Public{}).
		SetMinSigners(1).
		SetCoordinatorIndex(1).
		SaveX(setup.ctx)

	bobSecret := setup.fixtures.GeneratePrivateKey()
	bobSigningKeyshare := setup.client.SigningKeyshare.Create().
		SetSecretShare(bobSecret).
		SetPublicKey(bobSecret.Public()).
		SetStatus(st.KeyshareStatusInUse).
		SetPublicShares(map[string]keys.Public{}).
		SetMinSigners(1).
		SetCoordinatorIndex(1).
		SaveX(setup.ctx)

	carolSecret := setup.fixtures.GeneratePrivateKey()
	carolSigningKeyshare := setup.client.SigningKeyshare.Create().
		SetSecretShare(carolSecret).
		SetPublicKey(carolSecret.Public()).
		SetStatus(st.KeyshareStatusInUse).
		SetPublicShares(map[string]keys.Public{}).
		SetMinSigners(1).
		SetCoordinatorIndex(1).
		SaveX(setup.ctx)

	tokenCreate := setup.fixtures.CreateTokenCreate(btcnetwork.Regtest, nil, nil)

	txHash := setup.fixtures.RandomBytes(32)
	tokenTx := setup.client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(txHash).
		SetFinalizedTokenTransactionHash(txHash).
		SetStatus(st.TokenTransactionStatusFinalized).
		SetCreateID(tokenCreate.ID).
		SaveX(setup.ctx)

	withdrawRevocationCommitment := setup.fixtures.GeneratePrivateKey().Public()
	tokenOutputInDb := setup.client.TokenOutput.Create().
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
		SetOutputCreatedTokenTransaction(tokenTx).
		SetCreatedTransactionFinalizedHash(tokenTx.FinalizedTokenTransactionHash).
		SetNetwork(btcnetwork.Regtest).
		SetTokenIdentifier(tokenCreate.TokenIdentifier).
		SetTokenCreateID(tokenCreate.ID).
		SaveX(setup.ctx)

	setup.client.TokenPartialRevocationSecretShare.Create().
		SetTokenOutput(tokenOutputInDb).
		SetOperatorIdentityPublicKey(bobOperatorPubKey).
		SetSecretShare(bobSigningKeyshare.SecretShare).
		SaveX(setup.ctx)

	setup.client.TokenPartialRevocationSecretShare.Create().
		SetTokenOutput(tokenOutputInDb).
		SetOperatorIdentityPublicKey(carolOperatorPubKey).
		SetSecretShare(carolSigningKeyshare.SecretShare).
		SaveX(setup.ctx)

	t.Run("returns empty map when input share map is empty", func(t *testing.T) {
		inputOperatorShareMap := &InputOperatorShareMaps{
			ByUUID:     make(map[ShareKey]ShareValue),
			ByHashVout: make(map[HashVoutShareKey]ShareValue),
		}

		_, err := setup.handler.getSecretSharesNotInInput(setup.ctx, inputOperatorShareMap)

		require.ErrorContains(t, err, "no input operator shares provided")
	})

	t.Run("excludes the revocation secret share if it is in the input", func(t *testing.T) {
		inputOperatorShareMap := &InputOperatorShareMaps{
			ByUUID: map[ShareKey]ShareValue{
				{
					TokenOutputID:             tokenOutputInDb.ID,
					OperatorIdentityPublicKey: aliceOperatorPubKey,
				}: {
					SecretShare:               aliceSigningKeyshare.SecretShare,
					OperatorIdentityPublicKey: aliceOperatorPubKey,
				},
			},
			ByHashVout: make(map[HashVoutShareKey]ShareValue),
		}

		result, err := setup.handler.getSecretSharesNotInInput(setup.ctx, inputOperatorShareMap)
		require.NoError(t, err)
		assert.Len(t, result, 2)
		assert.Equal(t, bobSigningKeyshare.SecretShare.Serialize(), result[bobOperatorPubKey][0].SecretShare)
		assert.Equal(t, carolSigningKeyshare.SecretShare.Serialize(), result[carolOperatorPubKey][0].SecretShare)
	})

	t.Run("excludes the partial revocation secret share if it is in the input", func(t *testing.T) {
		inputOperatorShareMap := &InputOperatorShareMaps{
			ByUUID: map[ShareKey]ShareValue{
				{
					TokenOutputID:             tokenOutputInDb.ID,
					OperatorIdentityPublicKey: bobOperatorPubKey,
				}: {
					SecretShare:               bobSigningKeyshare.SecretShare,
					OperatorIdentityPublicKey: bobOperatorPubKey,
				},
			},
			ByHashVout: make(map[HashVoutShareKey]ShareValue),
		}

		result, err := setup.handler.getSecretSharesNotInInput(setup.ctx, inputOperatorShareMap)
		require.NoError(t, err)
		assert.Len(t, result, 2)
		assert.Equal(t, aliceSigningKeyshare.SecretShare.Serialize(), result[aliceOperatorPubKey][0].SecretShare)
		assert.Equal(t, carolSigningKeyshare.SecretShare.Serialize(), result[carolOperatorPubKey][0].SecretShare)
	})

	t.Run("works with ByHashVout format", func(t *testing.T) {
		var hashKey [32]byte
		copy(hashKey[:], tokenOutputInDb.CreatedTransactionFinalizedHash)

		inputOperatorShareMap := &InputOperatorShareMaps{
			ByUUID: make(map[ShareKey]ShareValue),
			ByHashVout: map[HashVoutShareKey]ShareValue{
				{
					PrevTxHash:                hashKey,
					PrevVout:                  uint32(tokenOutputInDb.CreatedTransactionOutputVout),
					OperatorIdentityPublicKey: aliceOperatorPubKey,
				}: {
					SecretShare:               aliceSigningKeyshare.SecretShare,
					OperatorIdentityPublicKey: aliceOperatorPubKey,
				},
			},
		}

		result, err := setup.handler.getSecretSharesNotInInput(setup.ctx, inputOperatorShareMap)
		require.NoError(t, err)
		assert.Len(t, result, 2)
		assert.Equal(t, bobSigningKeyshare.SecretShare.Serialize(), result[bobOperatorPubKey][0].SecretShare)
		assert.Equal(t, carolSigningKeyshare.SecretShare.Serialize(), result[carolOperatorPubKey][0].SecretShare)
	})
}

func TestRecoverFullRevocationSecretsAndFinalize_RequireThresholdOperators(t *testing.T) {
	cfg := sparktesting.TestConfig(t)

	ctx, _ := db.ConnectToTestPostgres(t)
	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	dbClient := entTx.Client()

	setup := &internalSignTokenPostgresTestSetup{
		handler:  &InternalSignTokenHandler{config: cfg},
		ctx:      ctx,
		client:   dbClient,
		fixtures: entfixtures.New(t, ctx, dbClient),
	}

	// Configure 3 operators, threshold 2.
	limitedOps := make(map[string]*so.SigningOperator)
	ids := make([]string, 3)
	for i := range ids {
		id := fmt.Sprintf("%064x", i+1)
		limitedOps[id] = setup.handler.config.SigningOperatorMap[id]
		ids[i] = id
	}
	setup.handler.config.SigningOperatorMap = limitedOps
	setup.handler.config.Threshold = 2

	priv := setup.fixtures.GeneratePrivateKey()
	secretInt := new(big.Int).SetBytes(priv.Serialize())
	shares, err := secretsharing.SplitSecret(secretInt, secp256k1.S256().N, 2, 3)
	require.NoError(t, err)

	tokenCreate := setup.fixtures.CreateTokenCreate(btcnetwork.Regtest, nil, nil)

	output := createTestSpentOutputWithShares(t, setup, tokenCreate, priv, shares, ids)
	hash := bytes.Repeat([]byte{0x24}, 32)
	_ = setup.client.TokenTransaction.Create().
		SetCreateID(tokenCreate.ID).
		SetPartialTokenTransactionHash(hash).
		SetFinalizedTokenTransactionHash(hash).
		SetStatus(st.TokenTransactionStatusSigned).
		AddSpentOutput(output).
		SaveX(setup.ctx)

	// Commit so data visible in new transaction.
	require.NoError(t, entTx.Commit())
	t.Run("flag false does not finalize when threshold requirement disabled", func(t *testing.T) {
		setup.handler.config.Token.RequireThresholdOperators = false
		finalized, err := setup.handler.recoverFullRevocationSecretsAndFinalize(setup.ctx, hash)
		require.NoError(t, err)
		assert.False(t, finalized)
	})
	t.Run("flag true finalizes when threshold requirement enabled", func(t *testing.T) {
		setup.handler.config.Token.RequireThresholdOperators = true
		finalized, err := setup.handler.recoverFullRevocationSecretsAndFinalize(setup.ctx, hash)
		require.NoError(t, err)
		assert.True(t, finalized)
	})
}
