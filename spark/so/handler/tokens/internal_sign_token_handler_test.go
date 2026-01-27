package tokens

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	sparktokeninternal "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entfixtures"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

type internalSignTokenTestSetup struct {
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

func makeFinalMintTxForTests() *tokenpb.TokenTransaction {
	issuerPub := bytes.Repeat([]byte{0x02}, 33)
	ownerPub := bytes.Repeat([]byte{0x03}, 33)
	op1 := bytes.Repeat([]byte{0x11}, 33)
	op2 := bytes.Repeat([]byte{0x22}, 33)
	tokenIdentifier := bytes.Repeat([]byte{0xAA}, 32)
	amount16 := bytes.Repeat([]byte{0x01}, 16)
	revKey := bytes.Repeat([]byte{0x04}, 33)
	withdrawBond := uint64(100)
	withdrawLock := uint64(100)

	ops := [][]byte{op1, op2}
	if bytes.Compare(ops[0], ops[1]) > 0 {
		ops[0], ops[1] = ops[1], ops[0]
	}

	now := time.Now().UTC()
	return &tokenpb.TokenTransaction{
		Version:                         3,
		Network:                         sparkpb.Network_REGTEST,
		SparkOperatorIdentityPublicKeys: ops,
		ClientCreatedTimestamp:          timestamppb.New(now),
		ExpiryTime:                      timestamppb.New(now.Add(time.Hour)),
		TokenInputs: &tokenpb.TokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{
				IssuerPublicKey: issuerPub,
				TokenIdentifier: tokenIdentifier,
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{
			{
				OwnerPublicKey:                ownerPub,
				TokenIdentifier:               tokenIdentifier,
				TokenAmount:                   amount16,
				RevocationCommitment:          revKey,
				WithdrawBondSats:              &withdrawBond,
				WithdrawRelativeBlockLocktime: &withdrawLock,
			},
		},
	}
}

func TestBuildInputOperatorShareMap(t *testing.T) {
	testHash := []byte{
		0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77,
		0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff,
		0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80,
		0x90, 0xa0, 0xb0, 0xc0, 0xd0, 0xe0, 0xf0, 0x00,
	}
	testSecret := bytes.Repeat([]byte{0x42}, 32)
	testOperatorPubKey := bytes.Repeat([]byte{0x02}, 33)
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

func TestExchangeRevocationSecretsShares(t *testing.T) {
	setup := setUpInternalSignTokenTestHandler(t)
	defer setup.cleanup()

	testHash := []byte{
		0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77,
		0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff,
		0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80,
		0x90, 0xa0, 0xb0, 0xc0, 0xd0, 0xe0, 0xf0, 0x00,
	}

	tokenCreate := setup.fixtures.CreateTokenCreate(btcnetwork.Regtest, nil, nil)
	testTransaction := setup.client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(testHash).
		SetFinalizedTokenTransactionHash(testHash).
		SetStatus(st.TokenTransactionStatusSigned).
		SetCreateID(tokenCreate.ID).
		SaveX(setup.ctx)

	t.Run("fails when no operator shares provided", func(t *testing.T) {
		req := &sparktokeninternal.ExchangeRevocationSecretsSharesRequest{
			OperatorShares: []*sparktokeninternal.OperatorRevocationShares{},
			OperatorTransactionSignatures: []*sparktokeninternal.OperatorTransactionSignature{
				{
					OperatorIdentityPublicKey: []byte("invalid_operator"),
					Signature:                 []byte("invalid_signature"),
				},
			},
			FinalTokenTransaction:     makeFinalMintTxForTests(),
			FinalTokenTransactionHash: testTransaction.FinalizedTokenTransactionHash,
			OperatorIdentityPublicKey: []byte("requesting_operator"),
		}

		_, err := setup.handler.ExchangeRevocationSecretsShares(setup.ctx, req)

		require.ErrorContains(t, err, "no operator shares provided in request")
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
			FinalTokenTransaction:     makeFinalMintTxForTests(),
			FinalTokenTransactionHash: testTransaction.FinalizedTokenTransactionHash,
			OperatorIdentityPublicKey: []byte("requesting_operator"),
		}

		_, err := setup.handler.ExchangeRevocationSecretsShares(setup.ctx, req)

		require.ErrorContains(t, err, "unable to parse request operator identity public key")
	})
}

func TestValidateSignaturesPackageAndPersistPeerSignatures_RequireThresholdOperators(t *testing.T) {
	setup := setUpInternalSignTokenTestHandler(t)
	defer setup.cleanup()

	// Limit to 3 operators and set threshold to 2.
	limitedOperators := make(map[string]*so.SigningOperator)
	ids := make([]string, 3)
	for i := range ids {
		id := fmt.Sprintf("%064x", i+1)
		op, ok := setup.handler.config.SigningOperatorMap[id]
		require.True(t, ok, "operator %s must exist", id)
		limitedOperators[id] = op
		ids[i] = id
	}
	setup.handler.config.SigningOperatorMap = limitedOperators
	setup.handler.config.Threshold = 2

	testHash := bytes.Repeat([]byte{0x42}, 32)
	tokenCreate := setup.fixtures.CreateTokenCreate(btcnetwork.Regtest, nil, nil)
	tokenTransaction := setup.client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(testHash).
		SetFinalizedTokenTransactionHash(testHash).
		SetStatus(st.TokenTransactionStatusStarted).
		SetCreateID(tokenCreate.ID).
		SaveX(setup.ctx)

	buildSignatures := func() map[string][]byte {
		sigs := make(map[string][]byte)

		sig0 := ecdsa.Sign(setup.handler.config.IdentityPrivateKey.ToBTCEC(), testHash)
		sigs[ids[0]] = sig0.Serialize()

		const operator1PrivHex = "bc0f5b9055c4a88b881d4bb48d95b409cd910fb27c088380f8ecda2150ee8faf"
		privBytes, _ := hex.DecodeString(operator1PrivHex)
		privKey1, _ := keys.ParsePrivateKey(privBytes)
		sig1 := ecdsa.Sign(privKey1.ToBTCEC(), testHash)
		sigs[ids[1]] = sig1.Serialize()

		return sigs
	}

	signatures := buildSignatures()

	t.Run("flag false with missing operator fails", func(t *testing.T) {
		setup.handler.config.Token.RequireThresholdOperators = false
		err := setup.handler.validateSignaturesPackageAndPersistPeerSignatures(setup.ctx, signatures, tokenTransaction)
		require.Error(t, err, "expected failure when RequireThresholdOperators is false and not all operators signed")
	})

	t.Run("flag true with threshold signatures succeeds", func(t *testing.T) {
		setup.handler.config.Token.RequireThresholdOperators = true
		err := setup.handler.validateSignaturesPackageAndPersistPeerSignatures(setup.ctx, signatures, tokenTransaction)
		require.NoError(t, err, "expected success when RequireThresholdOperators is true and threshold signatures provided")
	})
}
