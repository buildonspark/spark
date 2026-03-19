package tokens

import (
	"bytes"
	"context"
	"testing"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	multisigpb "github.com/lightsparkdev/spark/proto/multisig"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	sparktokeninternal "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/entfixtures"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

type internalSignTokenTestSetup struct {
	t        *testing.T
	handler  *InternalSignTokenHandler
	ctx      context.Context
	client   *ent.Client
	fixtures *entfixtures.Fixtures
	cleanup  func()
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

func TestBuildInputOperatorShareMap(t *testing.T) {
	testHash := hash32(0xA1)
	testSecret := hash32(0x42)
	testOperatorPubKey := keys.GeneratePrivateKey().Public().Serialize()
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
		validPubKey := keys.GeneratePrivateKey().Public().Serialize()

		req := &sparktokeninternal.ExchangeRevocationSecretsSharesRequest{
			OperatorShares: []*sparktokeninternal.OperatorRevocationShares{},
			OperatorTransactionSignatures: []*sparktokeninternal.OperatorTransactionSignature{
				{
					OperatorIdentityPublicKey: validPubKey,
					Signature:                 bytes.Repeat([]byte{0x01}, 64),
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
							InputTtxoRef: &tokenpb.TokenOutputToSpend{
								PrevTokenTransactionHash: testHashTransfer[:],
								PrevTokenTransactionVout: 0,
							},
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

func TestSignAndPersistTokenTransaction_RejectsMultisigForPreV3(t *testing.T) {
	setup := setUpInternalSignTokenTestHandler(t)
	defer setup.cleanup()

	tokenCreate := setup.fixtures.CreateTokenCreate(btcnetwork.Regtest, nil, nil)
	testHash := hash32(0xE1)

	// Create a pre-V3 create transaction (default version is V0).
	setup.client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(testHash).
		SetFinalizedTokenTransactionHash(testHash).
		SetStatus(st.TokenTransactionStatusStarted).
		SetCreateID(tokenCreate.ID).
		SaveX(setup.ctx)

	// Re-query with edges loaded so validateTokenTransactionForSigning can inspect them.
	tokenTx, err := setup.client.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(testHash)).
		WithCreate().
		WithCreatedOutput().
		WithSpentOutput().
		WithMint().
		Only(setup.ctx)
	require.NoError(t, err)
	assert.Less(t, tokenTx.Version, st.TokenTransactionVersionV3)

	multisigSig := &tokenpb.SignatureWithIndex{
		InputIndex: 0,
		AuthoritySignatures: &tokenpb.SignatureWithIndex_MultisigSignatures{
			MultisigSignatures: &multisigpb.MultisigSignatureSet{
				MultisigConfig: &multisigpb.MultisigConfig{
					Version:    0,
					Threshold:  1,
					PublicKeys: [][]byte{keys.GeneratePrivateKey().Public().Serialize()},
				},
			},
		},
	}

	_, err = setup.handler.SignAndPersistTokenTransaction(
		setup.ctx,
		tokenTx,
		nil,
		testHash,
		[]*tokenpb.SignatureWithIndex{multisigSig},
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "multisig signatures are not supported for token transactions with version < V3")
}
