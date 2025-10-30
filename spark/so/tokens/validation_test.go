package tokens

import (
	"math/big"
	"math/rand/v2"
	"testing"

	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/entfixtures"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	stop := db.StartPostgresServer()
	defer stop()
	m.Run()
}

var maxSupply = big.NewInt(10)
var maxSupplyPlusOne = new(big.Int).Add(maxSupply, big.NewInt(1))

func TestValidateMintDoesNotExceedMaxSupplyEnt(t *testing.T) {
	testCases := []struct {
		name              string
		expectError       bool
		existingMintValue *big.Int
		newMintValue      *big.Int
	}{
		{
			name:              "valid mint, no existing mint",
			expectError:       false,
			existingMintValue: nil,
			newMintValue:      maxSupply,
		},
		{
			name:              "invalid mint, no existing mint",
			expectError:       true,
			existingMintValue: nil,
			newMintValue:      maxSupplyPlusOne,
		},
		{
			name:              "valid mint, existing mint",
			expectError:       false,
			existingMintValue: new(big.Int).Sub(maxSupply, big.NewInt(5)),
			newMintValue:      big.NewInt(5),
		},
		{
			name:              "invalid mint, existing mint",
			expectError:       true,
			existingMintValue: new(big.Int).Sub(maxSupply, big.NewInt(5)),
			newMintValue:      maxSupplyPlusOne,
		},
	}

	ctx, _ := db.ConnectToTestPostgres(t)
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	k := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobUseNumericAmountForCurrentTokenSupply: 1,
	})
	ctx = knobs.InjectKnobsService(ctx, k)

	seededRand := rand.NewChaCha8([32]byte{})
	f := entfixtures.New(t, ctx, tx).WithRNG(seededRand)

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tokenCreate := f.CreateTokenCreate(st.NetworkRegtest, nil, maxSupply)

			if tc.existingMintValue != nil {
				_, _ = f.CreateMintTransaction(
					tokenCreate,
					entfixtures.OutputSpecs(tc.existingMintValue),
					st.TokenTransactionStatusSigned,
				)
			}

			newMint, _ := f.CreateMintTransaction(
				tokenCreate,
				entfixtures.OutputSpecs(tc.newMintValue),
				st.TokenTransactionStatusStarted,
			)
			newMintTx, err := tx.TokenTransaction.Query().
				Where(tokentransaction.ID(newMint.ID)).
				WithMint().
				WithCreatedOutput().
				Only(ctx)
			require.NoError(t, err)
			require.NotNil(t, newMintTx.Edges.Mint)

			err = ValidateMintDoesNotExceedMaxSupplyEnt(ctx, newMintTx)

			if tc.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
