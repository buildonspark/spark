package handler

import (
	"testing"

	"github.com/google/uuid"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/stretchr/testify/require"
)

// applyLeafSignature guards a column-level contract that is invisible at the
// handler boundary until a rolled-back typed attempt is retried with a legacy
// signature: the legacy arm must CLEAR signature_scheme (SetNillable with nil
// is a no-op and would leave a stale scheme paired with fresh legacy bytes),
// and the typed arm must record its scheme. These tests inspect the mutation
// directly since the NULL-vs-stale distinction cannot be observed otherwise.
func TestApplyLeafSignature_TypedArmRecordsScheme(t *testing.T) {
	_, dbCtx := db.NewTestSQLiteContext(t)
	sig := []byte("typed-signature-bytes")

	update := applyLeafSignature(
		dbCtx.Client.TransferLeaf.UpdateOneID(uuid.New()),
		nil,
		&pbcommon.Signature{Scheme: pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR, Signature: sig},
	)

	m := update.Mutation()
	mutationSig, ok := m.Signature()
	require.True(t, ok)
	require.Equal(t, sig, mutationSig)
	mutationScheme, ok := m.SignatureScheme()
	require.True(t, ok)
	require.Equal(t, int32(pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR), mutationScheme)
	require.False(t, m.SignatureSchemeCleared())
}

func TestApplyLeafSignature_LegacyArmClearsStaleScheme(t *testing.T) {
	_, dbCtx := db.NewTestSQLiteContext(t)
	sig := []byte("legacy-signature-bytes")

	update := applyLeafSignature(dbCtx.Client.TransferLeaf.UpdateOneID(uuid.New()), sig, nil)

	m := update.Mutation()
	mutationSig, ok := m.Signature()
	require.True(t, ok)
	require.Equal(t, sig, mutationSig)
	require.True(t, m.SignatureSchemeCleared(), "legacy arm must clear any previously stored scheme")
	_, schemeSet := m.SignatureScheme()
	require.False(t, schemeSet)
}
