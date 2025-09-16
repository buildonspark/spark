package tokens

import (
	"context"
	"math/rand/v2"

	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

type queryTokenTestFixture struct {
	Handler *QueryTokenHandler
	Ctx     context.Context
	Tx      *ent.Tx
}

func setUpQueryTokenTestHandler(t *testing.T) *queryTokenTestFixture {
	t.Helper()
	config := sparktesting.TestConfig(t)
	ctx, _ := db.NewTestSQLiteContext(t)

	handler := &QueryTokenHandler{
		config:                     config,
		includeExpiredTransactions: true,
	}

	return &queryTokenTestFixture{
		Handler: handler,
		Ctx:     ctx,
	}
}

// createTestTokenOutputs creates the specified number of token outputs with their required dependencies
// for testing pagination functionality. Returns the created outputs.
func createTestTokenOutputs(t *testing.T, ctx context.Context, tx *ent.Tx, count int, ownerKey keys.Public, tokenCreate *ent.TokenCreate, rng *rand.ChaCha8) []*ent.TokenOutput {
	t.Helper()

	randomBytes := func(length int) []byte {
		b := make([]byte, length)
		_, err := rng.Read(b)
		require.NoError(t, err)
		return b
	}

	outputs := make([]*ent.TokenOutput, count)
	for i := range outputs {
		keyshare, err := tx.SigningKeyshare.Create().
			SetStatus(st.KeyshareStatusAvailable).
			SetSecretShare(keys.MustGeneratePrivateKeyFromRand(rng).Serialize()).
			SetPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
			SetMinSigners(1).
			SetCoordinatorIndex(0).
			SetPublicShares(map[string]keys.Public{}).
			Save(ctx)
		require.NoError(t, err)

		mintTx, err := tx.TokenTransaction.Create().
			SetPartialTokenTransactionHash(randomBytes(32)).
			SetFinalizedTokenTransactionHash(randomBytes(32)).
			SetStatus(st.TokenTransactionStatusFinalized).
			Save(ctx)
		require.NoError(t, err)

		out, err := tx.TokenOutput.Create().
			SetStatus(st.TokenOutputStatusCreatedFinalized).
			SetOwnerPublicKey(ownerKey.Serialize()).
			SetWithdrawBondSats(1_000).
			SetWithdrawRelativeBlockLocktime(10).
			SetWithdrawRevocationCommitment(keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()).
			SetTokenAmount(randomBytes(16)).
			SetCreatedTransactionOutputVout(0).
			SetTokenIdentifier(tokenCreate.TokenIdentifier).
			SetTokenCreateID(tokenCreate.ID).
			SetOutputCreatedTokenTransactionID(mintTx.ID).
			SetRevocationKeyshare(keyshare).
			SetNetwork(st.NetworkRegtest).
			Save(ctx)
		require.NoError(t, err)
		outputs[i] = out
	}

	return outputs
}

func TestExpiredOutputBeforeFinalization(t *testing.T) {
	setup := setUpQueryTokenTestHandler(t)
	handler := setup.Handler
	ctx := setup.Ctx

	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	rng := rand.NewChaCha8([32]byte{})
	t.Run("return output after transaction has expired in signed state", func(t *testing.T) {
		randomBytes := func(length int) []byte {
			b := make([]byte, length)
			_, err := rng.Read(b)
			require.NoError(t, err)
			return b
		}

		// Create two signing keyshares (one for the mint output, one for transfer output)
		signKS1, err := tx.SigningKeyshare.Create().
			SetStatus(st.KeyshareStatusAvailable).
			SetSecretShare(keys.MustGeneratePrivateKeyFromRand(rng).Serialize()).
			SetPublicShares(map[string]keys.Public{}).
			SetPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
			SetMinSigners(1).
			SetCoordinatorIndex(0).
			Save(ctx)
		require.NoError(t, err)

		signKS2, err := tx.SigningKeyshare.Create().
			SetStatus(st.KeyshareStatusAvailable).
			SetSecretShare(keys.MustGeneratePrivateKeyFromRand(rng).Serialize()).
			SetPublicShares(map[string]keys.Public{}).
			SetPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
			SetMinSigners(1).
			SetCoordinatorIndex(0).
			Save(ctx)
		require.NoError(t, err)

		// Create a mint transaction that produces an output we will later spend
		mintEnt, err := tx.TokenMint.Create().
			SetIssuerPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()).
			SetWalletProvidedTimestamp(uint64(time.Now().UnixMilli())).
			SetIssuerSignature(randomBytes(64)).
			Save(ctx)
		require.NoError(t, err)

		tokenIdentifier := randomBytes(32)
		tokenCreate, err := tx.TokenCreate.Create().
			SetIssuerPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
			SetTokenName("TestToken").
			SetTokenTicker("TT").
			SetDecimals(0).
			SetMaxSupply(randomBytes(16)).
			SetIsFreezable(true).
			SetNetwork(st.NetworkRegtest).
			SetTokenIdentifier(tokenIdentifier).
			SetCreationEntityPublicKey(handler.config.IdentityPublicKey()).
			Save(ctx)
		require.NoError(t, err)

		mintTx, err := tx.TokenTransaction.Create().
			SetPartialTokenTransactionHash(keys.MustGeneratePrivateKeyFromRand(rng).Serialize()).
			SetFinalizedTokenTransactionHash(keys.MustGeneratePrivateKeyFromRand(rng).Serialize()).
			SetStatus(st.TokenTransactionStatusFinalized).
			SetMintID(mintEnt.ID).
			Save(ctx)
		require.NoError(t, err)

		mintOutput, err := tx.TokenOutput.Create().
			SetStatus(st.TokenOutputStatusCreatedFinalized).
			SetOwnerPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()).
			SetWithdrawBondSats(1_000).
			SetWithdrawRelativeBlockLocktime(10).
			SetWithdrawRevocationCommitment(keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()).
			SetTokenAmount(randomBytes(16)).
			SetCreatedTransactionOutputVout(0).
			SetRevocationKeyshareID(signKS1.ID).
			SetTokenIdentifier(tokenIdentifier).
			SetTokenCreateID(tokenCreate.ID).
			SetOutputCreatedTokenTransactionID(mintTx.ID).
			SetNetwork(st.NetworkRegtest).
			Save(ctx)
		require.NoError(t, err)

		// Create a transfer transaction (SIGNED & expired) that spends the mint output
		expiredAt := time.Now().Add(-1 * time.Hour)
		transferTx, err := tx.TokenTransaction.Create().
			SetPartialTokenTransactionHash(keys.MustGeneratePrivateKeyFromRand(rng).Serialize()).
			SetFinalizedTokenTransactionHash(keys.MustGeneratePrivateKeyFromRand(rng).Serialize()).
			SetStatus(st.TokenTransactionStatusSigned).
			SetExpiryTime(expiredAt).
			Save(ctx)
		require.NoError(t, err)

		// Update mintOutput to mark it as spent by transferTx
		_, err = mintOutput.Update().
			SetStatus(st.TokenOutputStatusSpentSigned).
			SetOutputSpentTokenTransactionID(transferTx.ID).
			SetSpentTransactionInputVout(0).
			Save(ctx)
		require.NoError(t, err)

		// Create a new output produced by the transferTx
		_, err = tx.TokenOutput.Create().
			SetStatus(st.TokenOutputStatusCreatedSigned).
			SetOwnerPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()).
			SetWithdrawBondSats(500).
			SetWithdrawRelativeBlockLocktime(10).
			SetWithdrawRevocationCommitment(keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()).
			SetTokenAmount(randomBytes(16)).
			SetCreatedTransactionOutputVout(0).
			SetRevocationKeyshareID(signKS2.ID).
			SetTokenIdentifier(tokenIdentifier).
			SetTokenCreateID(tokenCreate.ID).
			SetOutputCreatedTokenTransactionID(transferTx.ID).
			SetNetwork(st.NetworkRegtest).
			Save(ctx)
		require.NoError(t, err)

		outputsResp, err := handler.QueryTokenOutputsToken(ctx, &tokenpb.QueryTokenOutputsRequest{
			OwnerPublicKeys: [][]byte{mintOutput.OwnerPublicKey},
			Network:         sparkpb.Network_REGTEST,
		})
		require.NoError(t, err)

		require.Len(t, outputsResp.OutputsWithPreviousTransactionData, 1)
		assert.Equal(t, mintOutput.ID.String(), outputsResp.OutputsWithPreviousTransactionData[0].Output.GetId())
	})
}

func TestQueryTokenOutputsPagination(t *testing.T) {
	setup := setUpQueryTokenTestHandler(t)
	handler := setup.Handler
	ctx := setup.Ctx

	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	rng := rand.NewChaCha8([32]byte{})
	randomBytes := func(length int) []byte {
		b := make([]byte, length)
		_, err := rng.Read(b)
		require.NoError(t, err)
		return b
	}

	issuerKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerKey := keys.MustGeneratePrivateKeyFromRand(rng)

	tokenIdentifier := randomBytes(32)
	tokenCreate, err := tx.TokenCreate.Create().
		SetIssuerPublicKey(issuerKey.Public()).
		SetTokenName("TestToken").
		SetTokenTicker("TT").
		SetDecimals(0).
		SetMaxSupply(randomBytes(16)).
		SetIsFreezable(true).
		SetNetwork(st.NetworkRegtest).
		SetTokenIdentifier(tokenIdentifier).
		SetCreationEntityPublicKey(handler.config.IdentityPublicKey()).
		Save(ctx)
	require.NoError(t, err)

	createTestTokenOutputs(t, ctx, tx, 7, ownerKey.Public(), tokenCreate, rng)

	t.Run("forward pagination", func(t *testing.T) {
		// Page size 3, expect 3 pages: 3,3,1
		var all []*tokenpb.OutputWithPreviousTransactionData
		var afterCursor string

		for page := 0; page < 3; page++ {
			req := &tokenpb.QueryTokenOutputsRequest{
				OwnerPublicKeys: [][]byte{ownerKey.Public().Serialize()},
				Network:         sparkpb.Network_REGTEST,
				PageRequest: &sparkpb.PageRequest{
					PageSize:  3,
					Direction: sparkpb.Direction_NEXT,
				},
			}
			if afterCursor != "" {
				req.PageRequest.Cursor = afterCursor
			}

			resp, err := handler.QueryTokenOutputsToken(ctx, req)
			require.NoError(t, err)
			require.NotNil(t, resp.PageResponse)

			all = append(all, resp.OutputsWithPreviousTransactionData...)

			if page == 0 {
				require.False(t, resp.PageResponse.HasPreviousPage)
			} else {
				require.True(t, resp.PageResponse.HasPreviousPage)
			}

			if page < 2 {
				require.True(t, resp.PageResponse.HasNextPage)
				require.NotEmpty(t, resp.PageResponse.NextCursor)
				afterCursor = resp.PageResponse.NextCursor
			} else {
				require.False(t, resp.PageResponse.HasNextPage)
			}
		}

		require.Len(t, all, 7)
		// Validate the IDs are sorted ascending by UUID (matching query order by ID)
		for i := 1; i < len(all); i++ {
			prev := all[i-1].Output.GetId()
			curr := all[i].Output.GetId()
			require.LessOrEqual(t, prev, curr)
		}
	})

	t.Run("backward pagination", func(t *testing.T) {
		resp, err := handler.QueryTokenOutputsToken(ctx, &tokenpb.QueryTokenOutputsRequest{
			OwnerPublicKeys: [][]byte{ownerKey.Public().Serialize()},
			Network:         sparkpb.Network_REGTEST,
			PageRequest: &sparkpb.PageRequest{
				PageSize:  DefaultTokenOutputPageSize,
				Direction: sparkpb.Direction_NEXT,
			},
		})
		require.NoError(t, err)
		require.Len(t, resp.OutputsWithPreviousTransactionData, 7)

		// Use the PageResponse.NextCursor from the response, which is properly encoded
		beforeCursor := resp.PageResponse.NextCursor
		require.NotEmpty(t, beforeCursor)

		// Backward pagination should return an error
		_, err = handler.QueryTokenOutputsToken(ctx, &tokenpb.QueryTokenOutputsRequest{
			OwnerPublicKeys: [][]byte{ownerKey.Public().Serialize()},
			Network:         sparkpb.Network_REGTEST,
			PageRequest: &sparkpb.PageRequest{
				PageSize:  3,
				Cursor:    beforeCursor,
				Direction: sparkpb.Direction_PREVIOUS,
			},
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "backward pagination with 'previous' direction is not currently supported")
	})

	t.Run("default page size", func(t *testing.T) {
		resp, err := handler.QueryTokenOutputsToken(ctx, &tokenpb.QueryTokenOutputsRequest{
			OwnerPublicKeys: [][]byte{ownerKey.Public().Serialize()},
			Network:         sparkpb.Network_REGTEST,
			// PageRequest not set, should use DefaultTokenOutputPageSize
		})
		require.NoError(t, err)
		require.NotNil(t, resp.PageResponse)

		// Should get all 7 outputs since DefaultTokenOutputPageSize (500) > 7
		require.Len(t, resp.OutputsWithPreviousTransactionData, 7)
		require.False(t, resp.PageResponse.HasNextPage)
		require.False(t, resp.PageResponse.HasPreviousPage)
	})

	t.Run("page size limit", func(t *testing.T) {
		resp, err := handler.QueryTokenOutputsToken(ctx, &tokenpb.QueryTokenOutputsRequest{
			OwnerPublicKeys: [][]byte{ownerKey.Public().Serialize()},
			Network:         sparkpb.Network_REGTEST,
			PageRequest: &sparkpb.PageRequest{
				PageSize:  MaxTokenOutputPageSize + 100,
				Direction: sparkpb.Direction_NEXT,
			},
		})
		require.NoError(t, err)

		require.Len(t, resp.OutputsWithPreviousTransactionData, 7)
	})
}
