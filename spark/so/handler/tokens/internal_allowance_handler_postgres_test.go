package tokens

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// setupInternalAllowanceTest returns an internal handler with allowances enabled. The internal
// path has no session authz and no fan-out, so no operator map manipulation is needed.
func setupInternalAllowanceTest(t *testing.T) (context.Context, *db.TestContext, *InternalAllowanceHandler, *ent.TokenCreate) {
	t.Helper()
	ctx, tc := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobTokenAllowancesEnabled: 1.0,
	}))
	tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)
	return ctx, tc, NewInternalAllowanceHandler(cfg), tokenCreate
}

func TestInternalCreateTokenAllowance_ValidatesSignatureIndependently(t *testing.T) {
	ctx, _, handler, tokenCreate := setupInternalAllowanceTest(t)

	allowanceID := uuid.New()
	payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(10*time.Second))

	// The peer does not trust the coordinator: a signature by the wrong key is rejected.
	badReq := &tokeninternalpb.InternalCreateTokenAllowanceRequest{
		AllowancePayload: payload,
		OwnerSignature:   signCreateAllowance(t, payload, allowanceWrongKey),
	}
	_, err := handler.InternalCreateTokenAllowance(ctx, badReq)
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))

	// The correctly-signed grant is validated and applied.
	goodReq := &tokeninternalpb.InternalCreateTokenAllowanceRequest{
		AllowancePayload: payload,
		OwnerSignature:   signCreateAllowance(t, payload, allowanceOwnerKey),
	}
	_, err = handler.InternalCreateTokenAllowance(ctx, goodReq)
	require.NoError(t, err)

	row, err := ent.GetAllowanceByAllowanceID(ctx, allowanceID)
	require.NoError(t, err)
	assert.Equal(t, schematype.TokenAllowanceStatusActive, row.Status)
}

func TestInternalCreateTokenAllowance_UnknownTokenIsNotFound(t *testing.T) {
	ctx, _, handler, tokenCreate := setupInternalAllowanceTest(t)

	payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), uuid.New(), recentTimestamp(10*time.Second))
	unknownIdentifier := make([]byte, len(payload.GetTokenIdentifier()))
	copy(unknownIdentifier, payload.GetTokenIdentifier())
	unknownIdentifier[0] ^= 0xFF
	payload.TokenIdentifier = unknownIdentifier

	_, err := handler.InternalCreateTokenAllowance(ctx, &tokeninternalpb.InternalCreateTokenAllowanceRequest{
		AllowancePayload: payload,
		OwnerSignature:   signCreateAllowance(t, payload, allowanceOwnerKey),
	})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

// A caller that reads NOT_FOUND stops retrying and reports an unknown token, so an unreadable
// token row must stay distinguishable from an absent one.
func TestInternalCreateTokenAllowance_UnreadableTokenIsNotReportedAsNotFound(t *testing.T) {
	ctx, _, handler, tokenCreate := setupInternalAllowanceTest(t)

	payload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), uuid.New(), recentTimestamp(10*time.Second))
	req := &tokeninternalpb.InternalCreateTokenAllowanceRequest{
		AllowancePayload: payload,
		OwnerSignature:   signCreateAllowance(t, payload, allowanceOwnerKey),
	}

	// A cancelled context fails the token lookup with a non-NotFound error; it is the first DB
	// read in the apply path, so nothing earlier can short-circuit it.
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()

	_, err := handler.InternalCreateTokenAllowance(canceledCtx, req)
	require.Error(t, err)
	assert.NotEqual(t, codes.NotFound, status.Code(err), "a transient DB failure must not be reported as a missing token")
}

// TestInternalCreateTokenAllowance_AcceptsStaleTimestamp documents that the internal replication
// path does NOT enforce timestamp freshness. Recovery from partial replication replays the same
// signed payload, so a peer that was down longer than the freshness window must still accept the
// original owner-provided timestamp. Replay is blocked structurally instead: unique allowance_id,
// statement-hash idempotency, permanent revocation tombstones, and monotonic revoke timestamps.
func TestInternalCreateTokenAllowance_AcceptsStaleTimestamp(t *testing.T) {
	ctx, _, handler, tokenCreate := setupInternalAllowanceTest(t)

	allowanceID := uuid.New()
	// Far outside the ±1 minute public-edge freshness window.
	stalePayload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(48*time.Hour))
	_, err := handler.InternalCreateTokenAllowance(ctx, &tokeninternalpb.InternalCreateTokenAllowanceRequest{
		AllowancePayload: stalePayload,
		OwnerSignature:   signCreateAllowance(t, stalePayload, allowanceOwnerKey),
	})
	require.NoError(t, err)

	row, err := ent.GetAllowanceByAllowanceID(ctx, allowanceID)
	require.NoError(t, err)
	assert.Equal(t, schematype.TokenAllowanceStatusActive, row.Status)
}
