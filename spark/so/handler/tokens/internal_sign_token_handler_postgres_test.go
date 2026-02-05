package tokens

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	sparktokeninternal "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
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

// Test byte array helpers for concise test data creation
func hash32(b byte) []byte   { return bytes.Repeat([]byte{b}, 32) }
func pubKey33(b byte) []byte { return bytes.Repeat([]byte{b}, 33) }
func sig64(b byte) []byte    { return bytes.Repeat([]byte{b}, 64) }

// setupThresholdOperators configures the handler with 3 operators and threshold 2.
func (s *internalSignTokenPostgresTestSetup) setupThresholdOperators() []string {
	limitedOperators := make(map[string]*so.SigningOperator)
	ids := make([]string, 3)
	for i := range ids {
		id := fmt.Sprintf("%064x", i+1)
		op, ok := s.handler.config.SigningOperatorMap[id]
		if !ok {
			panic(fmt.Sprintf("operator %s must exist", id))
		}
		limitedOperators[id] = op
		ids[i] = id
	}
	s.handler.config.SigningOperatorMap = limitedOperators
	s.handler.config.Threshold = 2
	s.handler.config.Token.RequireThresholdOperators = true
	return ids
}

// buildThresholdSignatures creates valid signatures from threshold operators for the given hash.
func (s *internalSignTokenPostgresTestSetup) buildThresholdSignatures(operatorIDs []string, testHash []byte) map[string][]byte {
	sigs := make(map[string][]byte)

	// First operator uses handler's identity key
	sig0 := ecdsa.Sign(s.handler.config.IdentityPrivateKey.ToBTCEC(), testHash)
	sigs[operatorIDs[0]] = sig0.Serialize()

	// Second operator uses known test key
	const operator1PrivHex = "bc0f5b9055c4a88b881d4bb48d95b409cd910fb27c088380f8ecda2150ee8faf"
	privBytes, _ := hex.DecodeString(operator1PrivHex)
	privKey1, _ := keys.ParsePrivateKey(privBytes)
	sig1 := ecdsa.Sign(privKey1.ToBTCEC(), testHash)
	sigs[operatorIDs[1]] = sig1.Serialize()

	return sigs
}

// buildOperatorSignaturesProto converts a signature map to proto format for RPC requests.
func (s *internalSignTokenPostgresTestSetup) buildOperatorSignaturesProto(signatures map[string][]byte) []*sparktokeninternal.OperatorTransactionSignature {
	operatorSigs := make([]*sparktokeninternal.OperatorTransactionSignature, 0, len(signatures))
	for id, sig := range signatures {
		operatorSigs = append(operatorSigs, &sparktokeninternal.OperatorTransactionSignature{
			OperatorIdentityPublicKey: s.handler.config.SigningOperatorMap[id].IdentityPublicKey.Serialize(),
			Signature:                 sig,
		})
	}
	return operatorSigs
}

// createMintTransactionWithOutput creates a MINT transaction with one output using fixtures.
func (s *internalSignTokenPostgresTestSetup) createMintTransactionWithOutput(
	testHash []byte,
	txStatus st.TokenTransactionStatus,
) (*ent.TokenTransaction, *ent.TokenOutput) {
	tokenCreate := s.fixtures.CreateTokenCreate(btcnetwork.Regtest, nil, nil)
	tx, outputs := s.fixtures.CreateMintTransactionWithOpts(
		tokenCreate,
		entfixtures.OutputSpecs(big.NewInt(100)),
		txStatus,
		entfixtures.MintTransactionOpts{Hash: testHash},
	)

	// Reload with edges
	tx, err := s.client.TokenTransaction.Query().
		Where(tokentransaction.IDEQ(tx.ID)).
		WithMint().
		WithCreatedOutput().
		Only(s.ctx)
	if err != nil {
		panic(err)
	}

	return tx, outputs[0]
}

// createCreateTransaction creates a CREATE transaction (no outputs) using fixtures.
func (s *internalSignTokenPostgresTestSetup) createCreateTransaction(
	testHash []byte,
	status st.TokenTransactionStatus,
) *ent.TokenTransaction {
	tokenCreate := s.fixtures.CreateTokenCreate(btcnetwork.Regtest, nil, nil)
	tx := s.fixtures.CreateCreateTransaction(tokenCreate, status, entfixtures.MintTransactionOpts{Hash: testHash})

	// Reload with edges
	tx, err := s.client.TokenTransaction.Query().
		Where(tokentransaction.IDEQ(tx.ID)).
		WithCreate().
		Only(s.ctx)
	if err != nil {
		panic(err)
	}

	return tx
}

func TestExchangeRevocationSecretsShares_MintTransaction(t *testing.T) {
	setup := setUpInternalSignTokenTestHandlerPostgres(t)

	operatorIDs := setup.setupThresholdOperators()
	testHash := hash32(0x43)

	mintTransaction, output := setup.createMintTransactionWithOutput(
		testHash,
		st.TokenTransactionStatusSigned,
	)

	signatures := setup.buildThresholdSignatures(operatorIDs, testHash)
	operatorSigs := setup.buildOperatorSignaturesProto(signatures)

	t.Run("succeeds with empty operator shares for MINT", func(t *testing.T) {
		req := &sparktokeninternal.ExchangeRevocationSecretsSharesRequest{
			OperatorShares:                []*sparktokeninternal.OperatorRevocationShares{},
			OperatorTransactionSignatures: operatorSigs,
			FinalTokenTransaction:         nil,
			FinalTokenTransactionHash:     testHash,
			OperatorIdentityPublicKey:     setup.handler.config.IdentityPublicKey().Serialize(),
		}

		resp, err := setup.handler.ExchangeRevocationSecretsShares(setup.ctx, req)
		require.NoError(t, err, "MINT transaction should succeed with empty operator shares")
		require.NotNil(t, resp)

		// Verify transaction was finalized
		updatedTx, err := setup.client.TokenTransaction.Get(setup.ctx, mintTransaction.ID)
		require.NoError(t, err)
		require.Equal(t, st.TokenTransactionStatusFinalized, updatedTx.Status, "transaction should be FINALIZED")

		// Verify output was finalized
		updatedOutput, err := setup.client.TokenOutput.Get(setup.ctx, output.ID)
		require.NoError(t, err)
		require.Equal(t, st.TokenOutputStatusCreatedFinalized, updatedOutput.Status, "output should be CREATED_FINALIZED")
	})
}

func TestExchangeRevocationSecretsShares_CreateTransaction(t *testing.T) {
	setup := setUpInternalSignTokenTestHandlerPostgres(t)

	operatorIDs := setup.setupThresholdOperators()
	testHash := hash32(0x44)

	createTransaction := setup.createCreateTransaction(testHash, st.TokenTransactionStatusSigned)

	signatures := setup.buildThresholdSignatures(operatorIDs, testHash)
	operatorSigs := setup.buildOperatorSignaturesProto(signatures)

	t.Run("succeeds with empty operator shares for CREATE", func(t *testing.T) {
		req := &sparktokeninternal.ExchangeRevocationSecretsSharesRequest{
			OperatorShares:                []*sparktokeninternal.OperatorRevocationShares{},
			OperatorTransactionSignatures: operatorSigs,
			FinalTokenTransaction:         nil,
			FinalTokenTransactionHash:     testHash,
			OperatorIdentityPublicKey:     setup.handler.config.IdentityPublicKey().Serialize(),
		}

		resp, err := setup.handler.ExchangeRevocationSecretsShares(setup.ctx, req)
		require.NoError(t, err, "CREATE transaction should succeed with empty operator shares")
		require.NotNil(t, resp)

		// Verify transaction was finalized
		updatedTx, err := setup.client.TokenTransaction.Get(setup.ctx, createTransaction.ID)
		require.NoError(t, err)
		require.Equal(t, st.TokenTransactionStatusFinalized, updatedTx.Status, "transaction should be FINALIZED")
	})
}

func TestValidatePersistAndFinalizeMintOrCreate(t *testing.T) {
	setup := setUpInternalSignTokenTestHandlerPostgres(t)

	operatorIDs := setup.setupThresholdOperators()

	t.Run("finalizes MINT transaction and updates output status", func(t *testing.T) {
		testHash := hash32(0x45)
		mintTransaction, output := setup.createMintTransactionWithOutput(testHash, st.TokenTransactionStatusSigned)
		signatures := setup.buildThresholdSignatures(operatorIDs, testHash)

		err := setup.handler.validatePersistAndFinalizeMintOrCreate(setup.ctx, signatures, mintTransaction)
		require.NoError(t, err)

		// Verify transaction status
		updatedTx, err := setup.client.TokenTransaction.Get(setup.ctx, mintTransaction.ID)
		require.NoError(t, err)
		require.Equal(t, st.TokenTransactionStatusFinalized, updatedTx.Status)

		// Verify output status changed from CREATED_SIGNED to CREATED_FINALIZED
		updatedOutput, err := setup.client.TokenOutput.Get(setup.ctx, output.ID)
		require.NoError(t, err)
		require.Equal(t, st.TokenOutputStatusCreatedFinalized, updatedOutput.Status)
	})

	t.Run("fails with insufficient signatures", func(t *testing.T) {
		testHash := hash32(0x46)
		createTransaction := setup.createCreateTransaction(testHash, st.TokenTransactionStatusSigned)

		// Only provide 1 signature when threshold is 2
		sigs := make(map[string][]byte)
		sig0 := ecdsa.Sign(setup.handler.config.IdentityPrivateKey.ToBTCEC(), testHash)
		sigs[operatorIDs[0]] = sig0.Serialize()

		err := setup.handler.validatePersistAndFinalizeMintOrCreate(setup.ctx, sigs, createTransaction)
		require.Error(t, err, "should fail with insufficient signatures")
		require.Contains(t, err.Error(), "expected")
	})

	t.Run("is idempotent for already finalized transaction", func(t *testing.T) {
		testHash := hash32(0x47)
		createTransaction := setup.createCreateTransaction(testHash, st.TokenTransactionStatusFinalized)
		signatures := setup.buildThresholdSignatures(operatorIDs, testHash)

		// Should succeed without error (idempotent)
		err := setup.handler.validatePersistAndFinalizeMintOrCreate(setup.ctx, signatures, createTransaction)
		require.NoError(t, err, "should be idempotent for already finalized transaction")
	})
}

func TestExchangeRevocationSecretsShares_TransferTransaction_HappyPath(t *testing.T) {
	setup := setUpInternalSignTokenTestHandlerPostgres(t)

	// Configure 3 operators, threshold 2
	operatorIDs := setup.setupThresholdOperators()

	// Create the revocation secret and split it into shares for threshold recovery
	revocationPriv := setup.fixtures.GeneratePrivateKey()
	secretInt := new(big.Int).SetBytes(revocationPriv.Serialize())
	shares, err := secretsharing.SplitSecret(secretInt, secp256k1.S256().N, 2, 3)
	require.NoError(t, err)

	tokenCreate := setup.fixtures.CreateTokenCreate(btcnetwork.Regtest, nil, nil)

	// Create a spent output with secret shares set up for threshold recovery
	// This helper creates: coordinator's share in keyshare + one partial share from operator 1
	spentOutput := createTestSpentOutputWithShares(t, setup, tokenCreate, revocationPriv, shares, operatorIDs)

	// Get operator public keys for the proto
	operatorPubKeys := []keys.Public{
		setup.handler.config.SigningOperatorMap[operatorIDs[0]].IdentityPublicKey,
		setup.handler.config.SigningOperatorMap[operatorIDs[1]].IdentityPublicKey,
	}

	// Use fixture to create transfer transaction with matching proto hash
	transferResult := setup.fixtures.CreateTransferTransactionWithProto(
		tokenCreate,
		[]*ent.TokenOutput{spentOutput},
		entfixtures.OutputSpecs(big.NewInt(100)), // Same amount as spent output
		entfixtures.TransferTransactionOpts{
			OperatorPublicKeys: operatorPubKeys,
			Status:             st.TokenTransactionStatusSigned,
		},
	)

	// Build operator signatures for the transfer hash
	signatures := setup.buildThresholdSignatures(operatorIDs, transferResult.Hash)
	operatorSigs := setup.buildOperatorSignaturesProto(signatures)

	// Build operator shares - provide share[0] from operator 0
	// The database already has share[1] from createTestSpentOutputWithShares
	// Together they reach the threshold of 2
	share0, err := keys.PrivateKeyFromBigInt(shares[0].Share)
	require.NoError(t, err)

	operatorShares := []*sparktokeninternal.OperatorRevocationShares{
		{
			OperatorIdentityPublicKey: setup.handler.config.SigningOperatorMap[operatorIDs[0]].IdentityPublicKey.Serialize(),
			Shares: []*sparktokeninternal.RevocationSecretShare{
				{
					SecretShare: share0.Serialize(),
					InputTtxoRef: &tokenpb.TokenOutputToSpend{
						PrevTokenTransactionHash: spentOutput.CreatedTransactionFinalizedHash,
						PrevTokenTransactionVout: uint32(spentOutput.CreatedTransactionOutputVout),
					},
				},
			},
		},
	}

	t.Run("succeeds and finalizes transfer with threshold shares", func(t *testing.T) {
		req := &sparktokeninternal.ExchangeRevocationSecretsSharesRequest{
			OperatorShares:                operatorShares,
			OperatorTransactionSignatures: operatorSigs,
			FinalTokenTransaction:         transferResult.Proto,
			FinalTokenTransactionHash:     transferResult.Hash,
			OperatorIdentityPublicKey:     setup.handler.config.IdentityPublicKey().Serialize(),
			OutputsToSpend: []*sparktokeninternal.OutputToSpend{
				{
					CreatedTokenTransactionHash: spentOutput.CreatedTransactionFinalizedHash,
					CreatedTokenTransactionVout: uint32(spentOutput.CreatedTransactionOutputVout),
				},
			},
		}

		resp, err := setup.handler.ExchangeRevocationSecretsShares(setup.ctx, req)
		require.NoError(t, err, "TRANSFER transaction should succeed with valid operator shares")
		require.NotNil(t, resp)

		// Verify the response contains operator shares
		// (DB state verification is covered by unit tests for FinalizeTransferTransactionInternal)
		require.NotEmpty(t, resp.ReceivedOperatorShares, "response should include revocation secret shares")
	})
}
