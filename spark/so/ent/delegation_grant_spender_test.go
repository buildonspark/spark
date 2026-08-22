package ent_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testSpenderGrant(t *testing.T, ctx context.Context, client *ent.Client) *ent.DelegationGrant {
	t.Helper()
	ownerPriv := keys.GeneratePrivateKey()
	grant, err := client.DelegationGrant.Create().
		SetID(uuid.New()).
		SetOwnerIdentityPubkey(ownerPriv.Public()).
		SetNetwork(btcnetwork.Regtest).
		SetExpiryTime(time.Now().Add(time.Hour)).
		SetScopeTransfer(true).
		SetScopeRenew(false).
		SetScopeClaim(false).
		SetFeeFlatSats(0).
		SetVersion(1).
		SetOwnerSignature([]byte{0x01}).
		Save(ctx)
	require.NoError(t, err)
	return grant
}

// The unlimited flags are bound into the owner-signed statement (v2), so a row that
// cannot carry them would drop policy the owner signed and every operator verified.
func TestDelegationGrantSpender_UnlimitedFlagsRoundTrip(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	client := dbCtx.Client
	grant := testSpenderGrant(t, ctx, client)

	spenderPriv := keys.GeneratePrivateKey()
	created, err := client.DelegationGrantSpender.Create().
		SetSpenderIdentityPubkey(spenderPriv.Public()).
		SetStatus(st.DelegationStatusActive).
		SetPerTxCapSats(0).
		SetRollingLimitSats(0).
		SetRollingWindowSeconds(86400).
		SetPerTxUnlimited(true).
		SetRollingUnlimited(true).
		SetVersion(1).
		SetOwnerSignature([]byte{0x02}).
		SetDelegationGrantID(grant.ID).
		Save(ctx)
	require.NoError(t, err)

	reread, err := client.DelegationGrantSpender.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.True(t, reread.PerTxUnlimited)
	assert.True(t, reread.RollingUnlimited)
}

// Absent flags must read as bounded: a lost or stripped flag has to fail closed,
// never widen a spender to unlimited.
func TestDelegationGrantSpender_UnlimitedFlagsDefaultToBounded(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	client := dbCtx.Client
	grant := testSpenderGrant(t, ctx, client)

	spenderPriv := keys.GeneratePrivateKey()
	created, err := client.DelegationGrantSpender.Create().
		SetSpenderIdentityPubkey(spenderPriv.Public()).
		SetStatus(st.DelegationStatusActive).
		SetPerTxCapSats(100).
		SetRollingLimitSats(500).
		SetRollingWindowSeconds(86400).
		SetVersion(1).
		SetOwnerSignature([]byte{0x02}).
		SetDelegationGrantID(grant.ID).
		Save(ctx)
	require.NoError(t, err)

	reread, err := client.DelegationGrantSpender.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.False(t, reread.PerTxUnlimited)
	assert.False(t, reread.RollingUnlimited)
}
