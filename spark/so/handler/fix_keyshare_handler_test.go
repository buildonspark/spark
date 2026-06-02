package handler

import (
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/require"
)

func TestFixKeyshareParseRequest_FailsWhenMainSecretMissingAndEphemeralUnavailable(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)

	badOperator := &so.SigningOperator{ID: 1, Identifier: "bad-operator"}
	goodOperator := &so.SigningOperator{ID: 2, Identifier: "good-operator"}
	config := &so.Config{
		Threshold: 1,
		SigningOperatorMap: map[string]*so.SigningOperator{
			badOperator.Identifier:  badOperator,
			goodOperator.Identifier: goodOperator,
		},
	}

	publicKeySource := keys.MustParsePrivateKeyHex("e6d2b44c26c0c1b507fab0d5e66c388c5676c109b9ee41520ceba5b52e3a2a92")
	version := int32(0)
	keyshare, err := tc.Client.SigningKeyshare.Create().
		SetID(uuid.New()).
		SetStatus(st.KeyshareStatusAvailable).
		SetPublicShares(map[string]keys.Public{
			badOperator.Identifier:  publicKeySource.Public(),
			goodOperator.Identifier: publicKeySource.Public(),
		}).
		SetPublicKey(publicKeySource.Public()).
		SetSecretVersion(version).
		SetMinSigners(1).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	handler := NewFixKeyshareHandler(config)
	_, err = handler.parseRequest(
		ctx,
		keyshare.ID.String(),
		badOperator.Identifier,
		[]string{goodOperator.Identifier},
	)
	require.Error(t, err)
	require.ErrorContains(t, err, "ephemeral DB is unavailable")
	require.ErrorIs(t, err, ent.ErrSigningKeyshareSecretUnavailable)
}

func TestFixKeyshareParseRequestRejectsInvalidGoodOperatorSet(t *testing.T) {
	ctx, tc := db.NewTestSQLiteContext(t)

	badOperator := &so.SigningOperator{ID: 0, Identifier: "operator-0"}
	goodOperator1 := &so.SigningOperator{ID: 1, Identifier: "operator-1"}
	goodOperator2 := &so.SigningOperator{ID: 2, Identifier: "operator-2"}
	config := &so.Config{
		Threshold: 2,
		SigningOperatorMap: map[string]*so.SigningOperator{
			badOperator.Identifier:   badOperator,
			goodOperator1.Identifier: goodOperator1,
			goodOperator2.Identifier: goodOperator2,
		},
	}

	keyshareSecret := keys.GeneratePrivateKey()
	keyshare, err := tc.Client.SigningKeyshare.Create().
		SetID(uuid.New()).
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(keyshareSecret).
		SetPublicShares(map[string]keys.Public{
			badOperator.Identifier:   keyshareSecret.Public(),
			goodOperator1.Identifier: keyshareSecret.Public(),
			goodOperator2.Identifier: keyshareSecret.Public(),
		}).
		SetPublicKey(keyshareSecret.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	tests := []struct {
		name            string
		goodOperatorIDs []string
		wantError       string
	}{
		{
			name:            "duplicate good operator",
			goodOperatorIDs: []string{goodOperator1.Identifier, goodOperator1.Identifier},
			wantError:       "duplicate good signing operator ID: operator-1",
		},
		{
			name:            "bad operator also listed as good",
			goodOperatorIDs: []string{badOperator.Identifier, goodOperator1.Identifier},
			wantError:       "bad signing operator ID operator-0 cannot also be listed as a good signing operator",
		},
		{
			name:            "bad operator listed after valid good operator",
			goodOperatorIDs: []string{goodOperator1.Identifier, badOperator.Identifier},
			wantError:       "bad signing operator ID operator-0 cannot also be listed as a good signing operator",
		},
	}

	handler := NewFixKeyshareHandler(config)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := handler.parseRequest(ctx, keyshare.ID.String(), badOperator.Identifier, tt.goodOperatorIDs)
			require.ErrorContains(t, err, tt.wantError)
		})
	}
}
