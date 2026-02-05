package tokens

import (
	"context"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
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

type internalSignTokenTestSetup struct {
	t           *testing.T
	handler     *InternalSignTokenHandler
	ctx         context.Context
	client      *ent.Client
	fixtures    *entfixtures.Fixtures
	cleanup     func()
	operatorIDs []string // populated by setupThresholdOperators
}

func setUpInternalSignTokenTestHandler(t *testing.T) *internalSignTokenTestSetup {
	t.Helper()

	config := sparktesting.TestConfig(t)
	ctx, _ := db.NewTestSQLiteContext(t)
	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	dbClient := entTx.Client()

	return &internalSignTokenTestSetup{
		t:        t,
		handler:  &InternalSignTokenHandler{config: config},
		ctx:      ctx,
		client:   dbClient,
		fixtures: entfixtures.New(t, ctx, dbClient),
		cleanup: func() {
			if rollbackErr := entTx.Rollback(); rollbackErr != nil {
				t.Errorf("rollback failed: %v", rollbackErr)
			}
		},
	}
}

// setupThresholdOperators configures the handler with 3 operators and threshold 2.
// Sets RequireThresholdOperators to true. Returns operator IDs for signature building.
func (s *internalSignTokenTestSetup) setupThresholdOperators() []string {
	s.t.Helper()
	limitedOperators := make(map[string]*so.SigningOperator)
	ids := make([]string, 3)
	for i := range ids {
		id := fmt.Sprintf("%064x", i+1)
		op, ok := s.handler.config.SigningOperatorMap[id]
		require.True(s.t, ok, "operator %s must exist", id)
		limitedOperators[id] = op
		ids[i] = id
	}
	s.handler.config.SigningOperatorMap = limitedOperators
	s.handler.config.Threshold = 2
	s.handler.config.Token.RequireThresholdOperators = true
	s.operatorIDs = ids
	return ids
}

// buildThresholdSignatures creates valid signatures from threshold operators for the given hash.
func (s *internalSignTokenTestSetup) buildThresholdSignatures(testHash []byte) map[string][]byte {
	s.t.Helper()
	require.NotEmpty(s.t, s.operatorIDs, "must call setupThresholdOperators first")

	sigs := make(map[string][]byte)

	// First operator uses handler's identity key
	sig0 := ecdsa.Sign(s.handler.config.IdentityPrivateKey.ToBTCEC(), testHash)
	sigs[s.operatorIDs[0]] = sig0.Serialize()

	// Second operator uses known test key
	const operator1PrivHex = "bc0f5b9055c4a88b881d4bb48d95b409cd910fb27c088380f8ecda2150ee8faf"
	privBytes, _ := hex.DecodeString(operator1PrivHex)
	privKey1, _ := keys.ParsePrivateKey(privBytes)
	sig1 := ecdsa.Sign(privKey1.ToBTCEC(), testHash)
	sigs[s.operatorIDs[1]] = sig1.Serialize()

	return sigs
}

// createCreateTransaction creates a CREATE transaction (no outputs) using fixtures.
func (s *internalSignTokenTestSetup) createCreateTransaction(
	testHash []byte,
	status st.TokenTransactionStatus,
) *ent.TokenTransaction {
	s.t.Helper()

	tokenCreate := s.fixtures.CreateTokenCreate(btcnetwork.Regtest, nil, nil)
	tx := s.fixtures.CreateCreateTransaction(tokenCreate, status, entfixtures.MintTransactionOpts{Hash: testHash})

	// Reload with edges
	tx, err := s.client.TokenTransaction.Query().
		Where(tokentransaction.IDEQ(tx.ID)).
		WithCreate().
		Only(s.ctx)
	require.NoError(s.t, err)

	return tx
}

func TestBuildInputOperatorShareMap(t *testing.T) {
	testHash := hash32(0xA1)
	testSecret := hash32(0x42)
	testOperatorPubKey := pubKey33(0x02)
	testUUID := uuid.New()

	t.Run("parses new InputTtxoRef format", func(t *testing.T) {
		shares := []*sparktokeninternal.OperatorRevocationShares{
			{
				OperatorIdentityPublicKey: testOperatorPubKey,
				Shares: []*sparktokeninternal.RevocationSecretShare{
					{
						SecretShare: testSecret,
						InputTtxoRef: &tokenpb.TokenOutputToSpend{
							PrevTokenTransactionHash: testHash,
							PrevTokenTransactionVout: 1,
						},
					},
				},
			},
		}

		result, err := buildInputOperatorShareMap(shares)
		require.NoError(t, err)
		require.Len(t, result.ByHashVout, 1)
		require.Empty(t, result.ByUUID)

		// Verify the hash/vout key
		var hashKey [32]byte
		copy(hashKey[:], testHash)
		opPubKey, err := keys.ParsePublicKey(testOperatorPubKey)
		require.NoError(t, err)
		shareKey := HashVoutShareKey{
			PrevTxHash:                hashKey,
			PrevVout:                  1,
			OperatorIdentityPublicKey: opPubKey,
		}
		value, ok := result.ByHashVout[shareKey]
		require.True(t, ok)
		require.Equal(t, testSecret, value.SecretShare.Serialize())
	})

	t.Run("parses legacy UUID format", func(t *testing.T) {
		shares := []*sparktokeninternal.OperatorRevocationShares{
			{
				OperatorIdentityPublicKey: testOperatorPubKey,
				Shares: []*sparktokeninternal.RevocationSecretShare{
					{
						InputTtxoId: testUUID.String(),
						SecretShare: testSecret,
					},
				},
			},
		}

		result, err := buildInputOperatorShareMap(shares)
		require.NoError(t, err)
		require.Len(t, result.ByUUID, 1)
		require.Empty(t, result.ByHashVout)

		opPubKey, err := keys.ParsePublicKey(testOperatorPubKey)
		require.NoError(t, err)
		shareKey := ShareKey{
			TokenOutputID:             testUUID,
			OperatorIdentityPublicKey: opPubKey,
		}
		value, ok := result.ByUUID[shareKey]
		require.True(t, ok)
		require.Equal(t, testSecret, value.SecretShare.Serialize())
	})

	t.Run("prefers InputTtxoRef when both formats provided", func(t *testing.T) {
		shares := []*sparktokeninternal.OperatorRevocationShares{
			{
				OperatorIdentityPublicKey: testOperatorPubKey,
				Shares: []*sparktokeninternal.RevocationSecretShare{
					{
						InputTtxoId: testUUID.String(),
						SecretShare: testSecret,
						InputTtxoRef: &tokenpb.TokenOutputToSpend{
							PrevTokenTransactionHash: testHash,
							PrevTokenTransactionVout: 2,
						},
					},
				},
			},
		}

		result, err := buildInputOperatorShareMap(shares)
		require.NoError(t, err)
		// When InputTtxoRef is provided, it takes precedence
		require.Empty(t, result.ByUUID)
		require.Len(t, result.ByHashVout, 1)
	})
}

func TestExchangeRevocationSecretsShares_TransferTransaction(t *testing.T) {
	setup := setUpInternalSignTokenTestHandler(t)
	defer setup.cleanup()

	testHashCreate := hash32(0xC1)
	testHashTransfer := hash32(0xD1)

	tokenCreate := setup.fixtures.CreateTokenCreate(btcnetwork.Regtest, nil, nil)
	_ = setup.client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(testHashCreate).
		SetFinalizedTokenTransactionHash(testHashCreate).
		SetStatus(st.TokenTransactionStatusSigned).
		SetCreateID(tokenCreate.ID).
		SaveX(setup.ctx)

	// Create a transfer transaction (no Create/Mint edge) for testing operator_shares validation
	transferTransaction := setup.client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(testHashTransfer).
		SetFinalizedTokenTransactionHash(testHashTransfer).
		SetStatus(st.TokenTransactionStatusSigned).
		SaveX(setup.ctx)

	t.Run("fails when no operator shares provided for transfer", func(t *testing.T) {
		// Use proper 33-byte public keys to pass the parsing checks
		validPubKey := pubKey33(0x02)

		req := &sparktokeninternal.ExchangeRevocationSecretsSharesRequest{
			OperatorShares: []*sparktokeninternal.OperatorRevocationShares{},
			OperatorTransactionSignatures: []*sparktokeninternal.OperatorTransactionSignature{
				{
					OperatorIdentityPublicKey: validPubKey,
					Signature:                 sig64(0x01),
				},
			},
			FinalTokenTransaction:     nil,
			FinalTokenTransactionHash: transferTransaction.FinalizedTokenTransactionHash,
			OperatorIdentityPublicKey: validPubKey,
		}

		_, err := setup.handler.ExchangeRevocationSecretsShares(setup.ctx, req)

		require.ErrorContains(t, err, "no operator shares provided in request for transfer transaction")
	})

	t.Run("fails when operator signatures verification fails", func(t *testing.T) {
		req := &sparktokeninternal.ExchangeRevocationSecretsSharesRequest{
			OperatorShares: []*sparktokeninternal.OperatorRevocationShares{
				{
					OperatorIdentityPublicKey: []byte("operator1_pubkey"),
					Shares: []*sparktokeninternal.RevocationSecretShare{
						{
							InputTtxoId: uuid.New().String(),
							SecretShare: []byte("secret1"),
						},
					},
				},
			},
			OperatorTransactionSignatures: []*sparktokeninternal.OperatorTransactionSignature{
				{
					OperatorIdentityPublicKey: []byte("invalid_operator"),
					Signature:                 []byte("invalid_signature"),
				},
			},
			FinalTokenTransaction:     nil,
			FinalTokenTransactionHash: transferTransaction.FinalizedTokenTransactionHash,
			OperatorIdentityPublicKey: []byte("requesting_operator"),
		}

		_, err := setup.handler.ExchangeRevocationSecretsShares(setup.ctx, req)

		require.ErrorContains(t, err, "unable to parse request operator identity public key")
	})
}

func TestValidateSignaturesPackageAndPersistPeerSignatures_RequireThresholdOperators(t *testing.T) {
	setup := setUpInternalSignTokenTestHandler(t)
	defer setup.cleanup()

	setup.setupThresholdOperators()
	// Temporarily disable RequireThresholdOperators for test setup
	setup.handler.config.Token.RequireThresholdOperators = false

	testHash := hash32(0x42)
	tokenTransaction := setup.createCreateTransaction(testHash, st.TokenTransactionStatusStarted)
	signatures := setup.buildThresholdSignatures(testHash)

	t.Run("flag false with missing operator fails", func(t *testing.T) {
		setup.handler.config.Token.RequireThresholdOperators = false
		err := setup.handler.validateAndPersistPeerSignatures(setup.ctx, signatures, tokenTransaction)
		require.Error(t, err, "expected failure when RequireThresholdOperators is false and not all operators signed")
	})

	t.Run("flag true with threshold signatures succeeds", func(t *testing.T) {
		setup.handler.config.Token.RequireThresholdOperators = true
		err := setup.handler.validateAndPersistPeerSignatures(setup.ctx, signatures, tokenTransaction)
		require.NoError(t, err, "expected success when RequireThresholdOperators is true and threshold signatures provided")
	})
}
