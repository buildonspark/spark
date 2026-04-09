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
