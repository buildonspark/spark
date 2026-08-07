//go:build lightspark

package handler

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func aggregateLeavesRequest(f *aggregateLeavesFixture) *pbssp.AggregateLeavesRequest {
	return &pbssp.AggregateLeavesRequest{
		TargetNodeId:           f.target.ID.String(),
		LeafIds:                []string{f.leaves[0].ID.String(), f.leaves[1].ID.String()},
		OwnerIdentityPublicKey: f.ownerIdentity.Public().Serialize(),
	}
}

func TestSspAggregateLeavesEnforcesSessionIdentity(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{52})
	f := createAggregateLeavesFixture(t, ctx, rng)

	// A session for someone else must not be able to name this owner's key.
	ctx = authn.InjectSessionForTests(ctx, keys.MustGeneratePrivateKeyFromRand(rng).Public(), time.Now().Add(time.Hour).Unix())

	handler := NewSspRequestHandler(&so.Config{Identifier: "operator1", AuthzEnforced: true})
	_, err := handler.AggregateLeaves(ctx, aggregateLeavesRequest(f))
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.ErrorContains(t, err, "session identity does not match request identity")
}

// TestSspAggregateLeavesConsolidatedShortCircuitChecksOwnership is the
// important one: the short-circuit hands back signed transactions, so it must
// answer only the owner. A caller who passes the session check for their own
// identity must still not be able to name someone else's consolidated node.
func TestSspAggregateLeavesConsolidatedShortCircuitChecksOwnership(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{53})
	f := createAggregateLeavesFixture(t, ctx, rng)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Put the target in the state the short-circuit answers from, owned by
	// someone other than the caller.
	victim := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	_, err = dbClient.TreeNode.UpdateOne(f.target).
		SetStatus(st.TreeNodeStatusConsolidated).
		SetOwnerIdentityPubkey(victim).
		Save(ctx)
	require.NoError(t, err)

	attacker := keys.MustGeneratePrivateKeyFromRand(rng)
	ctx = authn.InjectSessionForTests(ctx, attacker.Public(), time.Now().Add(time.Hour).Unix())

	req := aggregateLeavesRequest(f)
	req.OwnerIdentityPublicKey = attacker.Public().Serialize()

	handler := NewSspRequestHandler(&so.Config{Identifier: "operator1", AuthzEnforced: true})
	resp, err := handler.AggregateLeaves(ctx, req)
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.ErrorContains(t, err, "not owned by the caller",
		"the short-circuit must deny on ownership before returning a signed package")
}
