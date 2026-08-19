package handler

import (
	"context"
	cryptorand "crypto/rand"
	"math/big"
	mathrand "math/rand/v2"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	entgossip "github.com/lightsparkdev/spark/so/ent/gossip"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenallowance"
	"github.com/lightsparkdev/spark/so/entfixtures"
	"github.com/lightsparkdev/spark/so/handler/tokens"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// togglableGossipServer is a mock gossip endpoint whose response can be changed
// at runtime, so a test can start a peer failing gossip delivery and later let
// it succeed - exercising reconvergence via the retry task without owner action.
// It counts only successful deliveries so a test can assert an already-acked
// peer is not re-sent to on retry.
type togglableGossipServer struct {
	pbgossip.UnimplementedGossipServiceServer
	mu        sync.Mutex
	gossipErr error
	delivered int
}

func (s *togglableGossipServer) setGossipErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gossipErr = err
}

func (s *togglableGossipServer) deliveredCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.delivered
}

func (s *togglableGossipServer) Gossip(_ context.Context, _ *pbgossip.GossipMessage) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gossipErr != nil {
		return nil, s.gossipErr
	}
	s.delivered++
	return &emptypb.Empty{}, nil
}

func startTogglableGossipServer(t *testing.T, srv *togglableGossipServer) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	t.Cleanup(func() { _ = l.Close() })

	server := grpc.NewServer()
	pbgossip.RegisterGossipServiceServer(server, srv)
	go func() {
		if err := server.Serve(l); err != nil {
			t.Logf("mock gossip gRPC server error: %v", err)
		}
	}()
	t.Cleanup(server.Stop)
	return addr
}

// setupRevokeGossipTest builds a coordinator plus peerCount mock operators whose
// gossip delivery is individually togglable. Returns the mock servers so a test
// can flip a peer from failing to succeeding.
func setupRevokeGossipTest(t *testing.T, peerCount int) (context.Context, *db.TestContext, *so.Config, *tokens.AllowanceTokenHandler, *ent.TokenCreate, []*togglableGossipServer) {
	t.Helper()
	ctx, tc := db.ConnectToTestPostgres(t)
	cfg := sparktesting.TestConfig(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobTokenAllowancesEnabled: 1.0,
	}))

	self := cfg.Identifier
	cfg.SigningOperatorMap = map[string]*so.SigningOperator{
		self: {Identifier: self, IdentityPublicKey: cfg.IdentityPublicKey()},
	}
	servers := make([]*togglableGossipServer, peerCount)
	for i := range peerCount {
		srv := &togglableGossipServer{}
		servers[i] = srv
		addr := startTogglableGossipServer(t, srv)
		id := so.IndexToIdentifier(uint32(i + 1))
		cfg.SigningOperatorMap[id] = &so.SigningOperator{
			Identifier:                id,
			IdentityPublicKey:         keys.GeneratePrivateKey().Public(),
			AddressRpc:                addr,
			OperatorConnectionFactory: &sparktesting.DangerousTestOperatorConnectionFactoryNoTLS{},
		}
	}

	tokenCreate := createAllowanceTestTokenCreate(t, ctx, tc.Client)
	return ctx, tc, cfg, tokens.NewAllowanceTokenHandler(cfg, NewSendGossipHandler(cfg)), tokenCreate, servers
}

// installAllowanceLocally applies an allowance on the coordinator only (no
// fan-out), so a revoke test has a live grant to tombstone.
func installAllowanceLocally(t *testing.T, ctx context.Context, cfg *so.Config, tokenCreate *ent.TokenCreate, allowanceID uuid.UUID) {
	t.Helper()
	createPayload := newAllowancePayload(tokenCreate, allowanceOwnerKey.Public(), allowanceSpenderKey.Public(), allowanceID, recentTimestamp(20*time.Second))
	require.NoError(t, tokens.ValidateAndApplyCreateAllowance(ctx, cfg, createPayload, signCreateAllowance(t, createPayload, allowanceOwnerKey)))
	require.NoError(t, ent.DbCommit(ctx))
}

func loadAllowanceRow(t *testing.T, tc *db.TestContext, allowanceID uuid.UUID) *ent.TokenAllowance {
	t.Helper()
	row, err := tc.Client.TokenAllowance.Query().Where(tokenallowance.AllowanceID(allowanceID)).Only(t.Context())
	require.NoError(t, err)
	return row
}

func pendingGossipMessages(t *testing.T, ctx context.Context) []*ent.Gossip {
	t.Helper()
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	rows, err := dbTx.Gossip.Query().Where(entgossip.StatusEQ(schematype.GossipStatusPending)).All(ctx)
	require.NoError(t, err)
	return rows
}

func countAcks(t *testing.T, row *ent.Gossip) int {
	t.Helper()
	require.NotNil(t, row.Receipts)
	bitMap := common.NewBitMapFromBytes(*row.Receipts, len(row.Participants))
	acks := 0
	for i := range row.Participants {
		if bitMap.Get(i) {
			acks++
		}
	}
	return acks
}

// TestRevokeTokenAllowanceGossip_ConvergesAfterPeerRecovers: a revoke that
// initially reaches only a subset of operators still tombstones locally, records
// a durable PENDING gossip row, and converges to fully delivered once the
// lagging operator recovers - purely via the send_gossip retry task, with no
// owner retry. The already-acked peer is not re-delivered to.
func TestRevokeTokenAllowanceGossip_ConvergesAfterPeerRecovers(t *testing.T) {
	ctx, tc, cfg, handlerImpl, tokenCreate, servers := setupRevokeGossipTest(t, 2)
	servers[1].setGossipErr(status.Error(codes.Unavailable, "operator 2 down"))

	allowanceID := uuid.New()
	installAllowanceLocally(t, ctx, cfg, tokenCreate, allowanceID)

	revokePayload := newRevokePayload(allowanceID, allowanceOwnerKey.Public(), recentTimestamp(10*time.Second))
	resp, err := handlerImpl.RevokeTokenAllowance(ctx, &tokenpb.RevokeTokenAllowanceRequest{
		RevokeAllowancePayload: revokePayload,
		OwnerSignature:         signRevokeAllowance(t, revokePayload, allowanceOwnerKey),
	})
	require.NoError(t, err)

	// The tombstone is durable regardless of fan-out completeness.
	assert.Equal(t, schematype.TokenAllowanceStatusRevoked, loadAllowanceRow(t, tc, allowanceID).Status)
	// Progress reflects self plus the one operator the immediate send reached.
	assert.Len(t, resp.GetAllowanceProgress().GetAppliedOperatorPublicKeys(), 2)
	assert.Equal(t, 1, servers[0].deliveredCount(), "reachable peer receives the revoke")
	assert.Equal(t, 0, servers[1].deliveredCount(), "down peer receives nothing yet")

	// Flush the immediate-send receipts, then confirm exactly one PENDING gossip
	// row with a single peer still un-acked (what the middleware would persist).
	require.NoError(t, ent.DbCommit(ctx))
	pending := pendingGossipMessages(t, ctx)
	require.Len(t, pending, 1)
	assert.Equal(t, 1, countAcks(t, pending[0]), "one peer acked, one still pending")

	// The lagging operator recovers; the retry task redelivers.
	servers[1].setGossipErr(nil)
	pending = pendingGossipMessages(t, ctx)
	require.Len(t, pending, 1)
	redelivered, err := NewSendGossipHandler(cfg).SendGossipMessage(ctx, pending[0])
	require.NoError(t, err)

	assert.Equal(t, schematype.GossipStatusDelivered, redelivered.Status, "row converges to delivered")
	assert.Equal(t, len(redelivered.Participants), countAcks(t, redelivered))
	assert.Equal(t, 1, servers[1].deliveredCount(), "recovered peer now receives the revoke")
	assert.Equal(t, 1, servers[0].deliveredCount(), "already-acked peer is not re-delivered to")
}

// TestRevokeTokenAllowanceGossip_SingleOperatorWritesNoGossipRow: with no peers,
// the coordinator tombstones and commits with nothing to gossip.
func TestRevokeTokenAllowanceGossip_SingleOperatorWritesNoGossipRow(t *testing.T) {
	ctx, tc, cfg, handlerImpl, tokenCreate, _ := setupRevokeGossipTest(t, 0)

	allowanceID := uuid.New()
	installAllowanceLocally(t, ctx, cfg, tokenCreate, allowanceID)

	revokePayload := newRevokePayload(allowanceID, allowanceOwnerKey.Public(), recentTimestamp(10*time.Second))
	resp, err := handlerImpl.RevokeTokenAllowance(ctx, &tokenpb.RevokeTokenAllowanceRequest{
		RevokeAllowancePayload: revokePayload,
		OwnerSignature:         signRevokeAllowance(t, revokePayload, allowanceOwnerKey),
	})
	require.NoError(t, err)
	assert.Len(t, resp.GetAllowanceProgress().GetAppliedOperatorPublicKeys(), 1)
	assert.Equal(t, schematype.TokenAllowanceStatusRevoked, loadAllowanceRow(t, tc, allowanceID).Status)
	assert.Empty(t, pendingGossipMessages(t, ctx), "single-operator revoke writes no gossip row")
}

// TestApplyRevokeTokenAllowanceGossip_TombstonesFromMessage: the receiving-side
// apply reconstructs the owner-signed payload from the gossip message and
// tombstones the grant, mirroring the coordinator's local apply.
func TestApplyRevokeTokenAllowanceGossip_TombstonesFromMessage(t *testing.T) {
	ctx, tc, cfg, _, tokenCreate, _ := setupRevokeGossipTest(t, 0)

	allowanceID := uuid.New()
	installAllowanceLocally(t, ctx, cfg, tokenCreate, allowanceID)

	revokeTimestamp := recentTimestamp(10 * time.Second)
	revokePayload := newRevokePayload(allowanceID, allowanceOwnerKey.Public(), revokeTimestamp)
	msg := &pbgossip.GossipMessageRevokeTokenAllowance{
		AllowanceId:            revokePayload.GetAllowanceId(),
		OwnerPublicKey:         revokePayload.GetOwnerPublicKey(),
		RevokeVersion:          revokePayload.GetVersion(),
		OwnerProvidedTimestamp: revokePayload.GetOwnerProvidedTimestamp(),
		OwnerSignature:         signRevokeAllowance(t, revokePayload, allowanceOwnerKey),
	}

	require.NoError(t, tokens.ApplyRevokeTokenAllowanceGossip(ctx, cfg, msg))
	require.NoError(t, ent.DbCommit(ctx))

	row := loadAllowanceRow(t, tc, allowanceID)
	assert.Equal(t, schematype.TokenAllowanceStatusRevoked, row.Status)
	assert.Equal(t, revokeTimestamp, row.OwnerProvidedRevokeTimestamp)
}

// TestApplyRevokeTokenAllowanceGossip_RejectsForgedSignature: the receiving side
// re-verifies the owner signature independently, so a gossip message whose
// signature was not produced by the grant's owner key is rejected and the grant
// stays ACTIVE - durable transport confers no trust.
func TestApplyRevokeTokenAllowanceGossip_RejectsForgedSignature(t *testing.T) {
	ctx, tc, cfg, _, tokenCreate, _ := setupRevokeGossipTest(t, 0)

	allowanceID := uuid.New()
	installAllowanceLocally(t, ctx, cfg, tokenCreate, allowanceID)

	revokePayload := newRevokePayload(allowanceID, allowanceOwnerKey.Public(), recentTimestamp(10*time.Second))
	msg := &pbgossip.GossipMessageRevokeTokenAllowance{
		AllowanceId:            revokePayload.GetAllowanceId(),
		OwnerPublicKey:         revokePayload.GetOwnerPublicKey(),
		RevokeVersion:          revokePayload.GetVersion(),
		OwnerProvidedTimestamp: revokePayload.GetOwnerProvidedTimestamp(),
		// Signed by a key that is not the grant's owner.
		OwnerSignature: signRevokeAllowance(t, revokePayload, allowanceWrongKey),
	}

	err := tokens.ApplyRevokeTokenAllowanceGossip(ctx, cfg, msg)
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	assert.Equal(t, schematype.TokenAllowanceStatusActive, loadAllowanceRow(t, tc, allowanceID).Status)
}

// TestApplyRevokeTokenAllowanceGossip_AppliesWhenKnobDisabled: revocation is a security control that
// must converge even with the allowances feature disabled, so the gossip-apply path is not gated on
// the enable knob. A peer holding a grant (installed while the knob was on) with the knob now off
// must still tombstone it on delivery.
func TestApplyRevokeTokenAllowanceGossip_AppliesWhenKnobDisabled(t *testing.T) {
	ctx, tc, cfg, _, tokenCreate, _ := setupRevokeGossipTest(t, 0)

	allowanceID := uuid.New()
	installAllowanceLocally(t, ctx, cfg, tokenCreate, allowanceID)

	knobOffCtx := knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobTokenAllowancesEnabled: 0.0,
	}))
	revokePayload := newRevokePayload(allowanceID, allowanceOwnerKey.Public(), recentTimestamp(10*time.Second))
	msg := &pbgossip.GossipMessageRevokeTokenAllowance{
		AllowanceId:            revokePayload.GetAllowanceId(),
		OwnerPublicKey:         revokePayload.GetOwnerPublicKey(),
		RevokeVersion:          revokePayload.GetVersion(),
		OwnerProvidedTimestamp: revokePayload.GetOwnerProvidedTimestamp(),
		OwnerSignature:         signRevokeAllowance(t, revokePayload, allowanceOwnerKey),
	}

	require.NoError(t, tokens.ApplyRevokeTokenAllowanceGossip(knobOffCtx, cfg, msg))
	require.NoError(t, ent.DbCommit(knobOffCtx))
	assert.Equal(t, schematype.TokenAllowanceStatusRevoked, loadAllowanceRow(t, tc, allowanceID).Status)
}

// TestApplyRevokeTokenAllowanceGossip_ReplayIsIdempotent: redelivery is normal for durable gossip,
// so applying the same owner-signed revoke twice must be a no-op that never resurrects or corrupts
// the tombstone.
func TestApplyRevokeTokenAllowanceGossip_ReplayIsIdempotent(t *testing.T) {
	ctx, tc, cfg, _, tokenCreate, _ := setupRevokeGossipTest(t, 0)

	allowanceID := uuid.New()
	installAllowanceLocally(t, ctx, cfg, tokenCreate, allowanceID)

	revokeTimestamp := recentTimestamp(10 * time.Second)
	revokePayload := newRevokePayload(allowanceID, allowanceOwnerKey.Public(), revokeTimestamp)
	revokeSignature := signRevokeAllowance(t, revokePayload, allowanceOwnerKey)
	msg := &pbgossip.GossipMessageRevokeTokenAllowance{
		AllowanceId:            revokePayload.GetAllowanceId(),
		OwnerPublicKey:         revokePayload.GetOwnerPublicKey(),
		RevokeVersion:          revokePayload.GetVersion(),
		OwnerProvidedTimestamp: revokePayload.GetOwnerProvidedTimestamp(),
		OwnerSignature:         revokeSignature,
	}

	require.NoError(t, tokens.ApplyRevokeTokenAllowanceGossip(ctx, cfg, msg))
	require.NoError(t, tokens.ApplyRevokeTokenAllowanceGossip(ctx, cfg, msg))
	require.NoError(t, ent.DbCommit(ctx))

	row := loadAllowanceRow(t, tc, allowanceID)
	assert.Equal(t, schematype.TokenAllowanceStatusRevoked, row.Status)
	assert.Equal(t, uint64(revokePayload.GetVersion()), row.RevokeVersion)
	assert.Equal(t, revokeTimestamp, row.OwnerProvidedRevokeTimestamp)
	assert.Equal(t, revokeSignature, row.RevokeSignature)
	count, err := tc.Client.TokenAllowance.Query().
		Where(tokenallowance.AllowanceID(allowanceID), tokenallowance.StatusEQ(schematype.TokenAllowanceStatusRevoked)).
		Count(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

// TestApplyRevokeTokenAllowanceGossip_RejectsUnsupportedRevokeVersion: the receiving side rejects
// an unsupported revoke version independently, so an operator never tombstones with - and then
// serves - a revoke proof it cannot itself reconstruct.
func TestApplyRevokeTokenAllowanceGossip_RejectsUnsupportedRevokeVersion(t *testing.T) {
	ctx, tc, cfg, _, tokenCreate, _ := setupRevokeGossipTest(t, 0)

	allowanceID := uuid.New()
	installAllowanceLocally(t, ctx, cfg, tokenCreate, allowanceID)

	revokePayload := newRevokePayload(allowanceID, allowanceOwnerKey.Public(), recentTimestamp(10*time.Second))
	revokePayload.Version = 99
	msg := &pbgossip.GossipMessageRevokeTokenAllowance{
		AllowanceId:            revokePayload.GetAllowanceId(),
		OwnerPublicKey:         revokePayload.GetOwnerPublicKey(),
		RevokeVersion:          revokePayload.GetVersion(),
		OwnerProvidedTimestamp: revokePayload.GetOwnerProvidedTimestamp(),
		OwnerSignature:         signRevokeAllowance(t, revokePayload, allowanceOwnerKey),
	}

	err := tokens.ApplyRevokeTokenAllowanceGossip(ctx, cfg, msg)
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Equal(t, schematype.TokenAllowanceStatusActive, loadAllowanceRow(t, tc, allowanceID).Status)
}

// A non-canonical proof must still acknowledge durable delivery so redelivery terminates while
// every operator retains the same canonical tombstone.
func TestApplyRevokeTokenAllowanceGossip_DifferentProofKeepsCanonicalTombstone(t *testing.T) {
	ctx, tc, cfg, _, tokenCreate, _ := setupRevokeGossipTest(t, 0)

	allowanceID := uuid.New()
	installAllowanceLocally(t, ctx, cfg, tokenCreate, allowanceID)

	firstPayload := newRevokePayload(allowanceID, allowanceOwnerKey.Public(), recentTimestamp(10*time.Second))
	firstSignature := signRevokeAllowance(t, firstPayload, allowanceOwnerKey)
	firstMsg := &pbgossip.GossipMessageRevokeTokenAllowance{
		AllowanceId:            firstPayload.GetAllowanceId(),
		OwnerPublicKey:         firstPayload.GetOwnerPublicKey(),
		RevokeVersion:          firstPayload.GetVersion(),
		OwnerProvidedTimestamp: firstPayload.GetOwnerProvidedTimestamp(),
		OwnerSignature:         firstSignature,
	}
	require.NoError(t, tokens.ApplyRevokeTokenAllowanceGossip(ctx, cfg, firstMsg))
	require.NoError(t, ent.DbCommit(ctx))

	secondPayload := newRevokePayload(allowanceID, allowanceOwnerKey.Public(), recentTimestamp(5*time.Second))
	secondMsg := &pbgossip.GossipMessageRevokeTokenAllowance{
		AllowanceId:            secondPayload.GetAllowanceId(),
		OwnerPublicKey:         secondPayload.GetOwnerPublicKey(),
		RevokeVersion:          secondPayload.GetVersion(),
		OwnerProvidedTimestamp: secondPayload.GetOwnerProvidedTimestamp(),
		OwnerSignature:         signRevokeAllowance(t, secondPayload, allowanceOwnerKey),
	}
	require.NoError(t, NewGossipHandler(cfg).HandleGossipMessage(ctx, &pbgossip.GossipMessage{
		MessageId: uuid.NewString(),
		Message: &pbgossip.GossipMessage_RevokeTokenAllowance{
			RevokeTokenAllowance: secondMsg,
		},
	}, false))
	require.NoError(t, ent.DbCommit(ctx))

	row := loadAllowanceRow(t, tc, allowanceID)
	assert.Equal(t, schematype.TokenAllowanceStatusRevoked, row.Status)
	assert.Equal(t, uint64(firstPayload.GetVersion()), row.RevokeVersion)
	assert.Equal(t, firstPayload.GetOwnerProvidedTimestamp(), row.OwnerProvidedRevokeTimestamp)
	assert.Equal(t, firstSignature, row.RevokeSignature)
}

// The coordinator already tombstoned locally before fanning the revoke out, so the
// coordinator-side dispatch must not apply it a second time.
func TestHandleRevokeTokenAllowanceGossip_AtCoordinatorLeavesAllowanceUntouched(t *testing.T) {
	ctx, tc, cfg, _, tokenCreate, _ := setupRevokeGossipTest(t, 0)

	allowanceID := uuid.New()
	installAllowanceLocally(t, ctx, cfg, tokenCreate, allowanceID)

	payload := newRevokePayload(allowanceID, allowanceOwnerKey.Public(), recentTimestamp(10*time.Second))
	require.NoError(t, NewGossipHandler(cfg).HandleGossipMessage(ctx, &pbgossip.GossipMessage{
		MessageId: uuid.NewString(),
		Message: &pbgossip.GossipMessage_RevokeTokenAllowance{
			RevokeTokenAllowance: &pbgossip.GossipMessageRevokeTokenAllowance{
				AllowanceId:            payload.GetAllowanceId(),
				OwnerPublicKey:         payload.GetOwnerPublicKey(),
				RevokeVersion:          payload.GetVersion(),
				OwnerProvidedTimestamp: payload.GetOwnerProvidedTimestamp(),
				OwnerSignature:         signRevokeAllowance(t, payload, allowanceOwnerKey),
			},
		},
	}, true /* forCoordinator */))
	require.NoError(t, ent.DbCommit(ctx))

	row := loadAllowanceRow(t, tc, allowanceID)
	assert.Equal(t, schematype.TokenAllowanceStatusActive, row.Status)
	assert.Empty(t, row.RevokeSignature)
}

// --- helpers mirrored from the tokens package tests (unexported there) ---

var (
	allowanceTestRng    = mathrand.NewChaCha8([32]byte{0xA1, 0x10, 0x0A, 0xCE})
	allowanceOwnerKey   = keys.MustGeneratePrivateKeyFromRand(allowanceTestRng)
	allowanceSpenderKey = keys.MustGeneratePrivateKeyFromRand(allowanceTestRng)
	allowanceWrongKey   = keys.MustGeneratePrivateKeyFromRand(allowanceTestRng)
)

func newAllowancePayload(tokenCreate *ent.TokenCreate, ownerPub, spenderPub keys.Public, allowanceID uuid.UUID, ownerTimestamp uint64) *tokenpb.TokenAllowancePayload {
	return &tokenpb.TokenAllowancePayload{
		Version:                1,
		AllowanceId:            allowanceID[:],
		OwnerPublicKey:         ownerPub.Serialize(),
		SpenderPublicKey:       spenderPub.Serialize(),
		TokenIdentifier:        tokenCreate.TokenIdentifier,
		PerTransactionCap:      u128(10_000),
		TotalLimit:             u128(100_000),
		ExpiryTime:             timestamppb.New(time.Now().Add(24 * time.Hour)),
		Network:                sparkpb.Network_REGTEST,
		OwnerProvidedTimestamp: ownerTimestamp,
	}
}

func newRevokePayload(allowanceID uuid.UUID, ownerPub keys.Public, revokeTimestamp uint64) *tokenpb.RevokeTokenAllowancePayload {
	return &tokenpb.RevokeTokenAllowancePayload{
		Version:                1,
		AllowanceId:            allowanceID[:],
		OwnerPublicKey:         ownerPub.Serialize(),
		OwnerProvidedTimestamp: revokeTimestamp,
	}
}

func signCreateAllowance(t *testing.T, payload *tokenpb.TokenAllowancePayload, key keys.Private) []byte {
	t.Helper()
	hash, err := utils.HashCreateTokenAllowancePayload(payload)
	require.NoError(t, err)
	return ecdsa.Sign(key.ToBTCEC(), hash).Serialize()
}

func signRevokeAllowance(t *testing.T, payload *tokenpb.RevokeTokenAllowancePayload, key keys.Private) []byte {
	t.Helper()
	hash, err := utils.HashRevokeTokenAllowancePayload(payload)
	require.NoError(t, err)
	return ecdsa.Sign(key.ToBTCEC(), hash).Serialize()
}

func createAllowanceTestTokenCreate(t *testing.T, ctx context.Context, client *ent.Client) *ent.TokenCreate {
	t.Helper()
	// entfixtures seeds its RNG deterministically, so every fixture instance would otherwise
	// mint the same token_identifier; supply a unique one so a test can create several tokens.
	tokenIdentifier := make([]byte, 32)
	_, err := cryptorand.Read(tokenIdentifier)
	require.NoError(t, err)
	fixtures := entfixtures.New(t, ctx, client)
	_, tokenCreate := fixtures.CreateTokenCreateWithOpts(btcnetwork.Regtest, entfixtures.TokenCreateOpts{
		TokenIdentifier: tokenIdentifier,
	})
	return tokenCreate
}

// recentTimestamp returns a timestamp (in millis) that is the given duration before now.
// Use this instead of hardcoded timestamps to ensure timestamps pass validation.
func recentTimestamp(ago time.Duration) uint64 {
	return uint64(time.Now().Add(-ago).UnixMilli())
}

// u128 encodes v as a 16-byte big-endian uint128, the on-wire width the allowance caps use.
func u128(v uint64) []byte {
	return new(big.Int).SetUint64(v).FillBytes(make([]byte, 16))
}
