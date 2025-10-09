package tokens

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"

	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	sparktokeninternal "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

func setUpInternalSignTokenTestHandler(t *testing.T) (*InternalSignTokenHandler, context.Context, *ent.Tx, func()) {
	t.Helper()

	config := sparktesting.TestConfig(t)
	ctx, _ := db.NewTestSQLiteContext(t)
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
func TestExchangeRevocationSecretsShares(t *testing.T) {
	handler, ctx, tx, cleanup := setUpInternalSignTokenTestHandler(t)
	defer cleanup()
	testHash := []byte{
		0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77,
		0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff,
		0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80,
		0x90, 0xa0, 0xb0, 0xc0, 0xd0, 0xe0, 0xf0, 0x00,
	}
	// Minimal TokenCreate required for TokenTransaction
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
	testTransaction := tx.TokenTransaction.Create().
		SetPartialTokenTransactionHash(testHash).
		SetFinalizedTokenTransactionHash(testHash).
		SetStatus(st.TokenTransactionStatusSigned).
		SetCreateID(tokenCreate.ID).
		SaveX(ctx)

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

		_, err := handler.ExchangeRevocationSecretsShares(ctx, req)

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

		_, err := handler.ExchangeRevocationSecretsShares(ctx, req)

		require.ErrorContains(t, err, "unable to parse request operator identity public key")
	})
}

func TestValidateSignaturesPackageAndPersistPeerSignatures_RequireThresholdOperators(t *testing.T) {
	handler, ctx, tx, cleanup := setUpInternalSignTokenTestHandler(t)
	defer cleanup()

	// Limit to 3 operators and set threshold to 2.
	limitedOperators := make(map[string]*so.SigningOperator)
	ids := make([]string, 3)
	for i := range ids {
		id := fmt.Sprintf("%064x", i+1)
		op, ok := handler.config.SigningOperatorMap[id]
		require.True(t, ok, "operator %s must exist", id)
		limitedOperators[id] = op
		ids[i] = id
	}
	handler.config.SigningOperatorMap = limitedOperators
	handler.config.Threshold = 2

	// Prepare a fake transaction in the DB.
	testHash := bytes.Repeat([]byte{0x42}, 32)
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
	tokenTransaction := tx.TokenTransaction.Create().
		SetPartialTokenTransactionHash(testHash).
		SetFinalizedTokenTransactionHash(testHash).
		SetStatus(st.TokenTransactionStatusStarted).
		SetCreateID(tokenCreate.ID).
		SaveX(ctx)

	// Helper to build signatures map with operators 0 and 1.
	buildSignatures := func() map[string][]byte {
		sigs := make(map[string][]byte)

		// Operator 0 (self)
		sig0 := ecdsa.Sign(handler.config.IdentityPrivateKey.ToBTCEC(), testHash)
		sigs[ids[0]] = sig0.Serialize()

		// Operator 1
		const operator1PrivHex = "bc0f5b9055c4a88b881d4bb48d95b409cd910fb27c088380f8ecda2150ee8faf"
		privBytes, _ := hex.DecodeString(operator1PrivHex)
		privKey1, _ := keys.ParsePrivateKey(privBytes)
		sig1 := ecdsa.Sign(privKey1.ToBTCEC(), testHash)
		sigs[ids[1]] = sig1.Serialize()

		return sigs
	}

	signatures := buildSignatures()

	t.Run("flag false with missing operator fails", func(t *testing.T) {
		handler.config.Token.RequireThresholdOperators = false
		err := handler.validateSignaturesPackageAndPersistPeerSignatures(ctx, signatures, tokenTransaction)
		require.Error(t, err, "expected failure when RequireThresholdOperators is false and not all operators signed")
	})

	t.Run("flag true with threshold signatures succeeds", func(t *testing.T) {
		handler.config.Token.RequireThresholdOperators = true
		err := handler.validateSignaturesPackageAndPersistPeerSignatures(ctx, signatures, tokenTransaction)
		require.NoError(t, err, "expected success when RequireThresholdOperators is true and threshold signatures provided")
	})
}
