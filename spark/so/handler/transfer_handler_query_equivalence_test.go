//go:build lightspark

package handler

import (
	"context"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Equivalence tests for the pending-transfer MIMO vs legacy paths.
//
// The two paths — legacy queryTransfers and queryPendingTransfersMIMO — must
// return semantically identical results for the production-relevant pending
// query shapes: the same set of transfer IDs, the same pagination offsets,
// and equivalent per-transfer projections. This harness calls each path
// directly (no routing knob) and asserts that equivalence.
//
// Query-shape legend (used in test names + the parent PR's perf table):
//   - R1: receiver participant, bare predicate (network-only filter on
//     transfers + identity_pubkey + status)
//   - R2: receiver participant + types filter (e.g. types=[SWAP])
//   - R3: receiver participant + transfer_id filter (singular lookup)
//   - S1: sender participant, bare predicate
//   - SR1: sender_or_receiver participant — the UNION ALL path
//
// Postgres-only: queryPendingTransfersMIMO uses raw SQL with pq.Array bindings
// and ANY($N::text[]) — not supported by SQLite.
//
// File also contains MIMO-only contract tests at the bottom (Now-binding,
// args validation) — they share the equivFixture but bypass the public
// handler boundary to pin contracts that aren't observable from there.

// equivFixture sets up shared state for equivalence tests: a Postgres-backed
// Ent client, an authenticated session for the queried wallet, the privacy
// knob enabled, and the handler under test.
type equivFixture struct {
	t       *testing.T
	ctx     context.Context
	client  *ent.Client
	cfg     *so.Config
	handler *TransferHandler
	rng     *rand.ChaCha8
	baseNow time.Time

	// Pubkeys built into the dataset.
	cold   keys.Public // wallet with 0 pending receiver-side
	light  keys.Public // wallet with a handful of pending receivers
	medium keys.Public // wallet with many pending receivers
	sender keys.Public // wallet with sender-side pending transfers
	both   keys.Public // wallet with both sender and receiver pending data
	other  keys.Public // unrelated wallet — no pending data anywhere

	// The transfer used by multi-receiver assertions.
	multiReceiverTransferID uuid.UUID
	multiReceiverPrimary    keys.Public // primary queried receiver
	multiReceiverExtra      keys.Public // second receiver on same transfer
}

func newEquivFixture(t *testing.T) *equivFixture {
	t.Helper()
	ctx, _ := db.ConnectToTestPostgres(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	cfg := sparktesting.TestConfig(t)
	cfg.AuthzEnforced = true

	f := &equivFixture{
		t:       t,
		ctx:     ctx,
		client:  client,
		cfg:     cfg,
		handler: NewTransferHandler(cfg),
		rng:     rand.NewChaCha8([32]byte{}),
		baseNow: time.Now(),
	}
	f.cold = f.newPubkey()
	f.light = f.newPubkey()
	f.medium = f.newPubkey()
	f.sender = f.newPubkey()
	f.both = f.newPubkey()
	f.other = f.newPubkey()
	return f
}

func (f *equivFixture) newPubkey() keys.Public {
	return keys.MustGeneratePrivateKeyFromRand(f.rng).Public()
}

// pendingPair pairs a transfers.status with the transfer_receivers.status a
// single-receiver transfer holds alongside it in production (the two move in
// lockstep only for single-receiver — a multi-receiver parent stays at
// SENDER_KEY_TWEAKED). Both legacy and MIMO consider the resulting transfer
// pending, so they should return equivalent results.
type pendingPair struct {
	transferStatus st.TransferStatus
	receiverStatus st.TransferReceiverStatus
}

// pendingPairs is the set of (transfer.status, receiver.status) combinations
// that show up in real pending-transfer traffic and are pending under BOTH
// the legacy and MIMO predicates. Each spans one of the 5 receiver-pending
// statuses.
//
// These pairs model single-receiver transfers, where parent and receiver
// status move in lockstep: e.g. at SENDER_KEY_TWEAKED the receiver is at
// RECEIVER_CLAIM_PENDING (sender done, receiver hasn't started claim). Legacy
// queryTransfers picks this up via t.status; MIMO via r.status. (A
// multi-receiver parent stays at SENDER_KEY_TWEAKED while its receivers advance
// independently.) INITIATED is in neither path's pending set — the pre-tweak
// state, where the sender hasn't finished its handoff and the receiver cannot act.
var pendingPairs = []pendingPair{
	{st.TransferStatusSenderKeyTweaked, st.TransferReceiverStatusReceiverClaimPending},
	{st.TransferStatusReceiverKeyTweaked, st.TransferReceiverStatusKeyTweaked},
	{st.TransferStatusReceiverKeyTweakLocked, st.TransferReceiverStatusKeyTweakLocked},
	{st.TransferStatusReceiverKeyTweakApplied, st.TransferReceiverStatusKeyTweakApplied},
	{st.TransferStatusReceiverRefundSigned, st.TransferReceiverStatusRefundSigned},
}

type makeTransferOpts struct {
	network        btcnetwork.Network
	transferType   st.TransferType
	transferStatus st.TransferStatus
	sender         keys.Public
	receiver       keys.Public
	receiverStatus st.TransferReceiverStatus
	expiryTime     time.Time
	createTime     time.Time
	extraReceivers []extraReceiverEquiv
}

type extraReceiverEquiv struct {
	pubkey keys.Public
	status st.TransferReceiverStatus
}

// makeTransfer creates a transfer plus its sender and receiver edge rows,
// matching the production dual-write contract. createTime is propagated
// to all edge rows per the cross-participant create_time invariant.
func (f *equivFixture) makeTransfer(opts makeTransferOpts) *ent.Transfer {
	f.t.Helper()
	if opts.network == btcnetwork.Unspecified {
		opts.network = btcnetwork.Regtest
	}
	if opts.transferType == "" {
		opts.transferType = st.TransferTypeTransfer
	}
	if opts.expiryTime.IsZero() {
		// Sender-pending paths require expiry < now. Default to 24h in the
		// past so sender-pending fixtures qualify; receiver-side queries
		// don't filter on expiry, so this default is safe for both.
		opts.expiryTime = f.baseNow.Add(-24 * time.Hour)
	}
	if opts.createTime.IsZero() {
		opts.createTime = f.baseNow.Add(-2 * time.Hour)
	}
	if opts.receiverStatus == "" {
		opts.receiverStatus = st.TransferReceiverStatusInitiated
	}

	transfer, err := f.client.Transfer.Create().
		SetNetwork(opts.network).
		SetType(opts.transferType).
		SetStatus(opts.transferStatus).
		SetExpiryTime(opts.expiryTime).
		SetTotalValue(1000).
		SetSenderIdentityPubkey(opts.sender).
		SetReceiverIdentityPubkey(opts.receiver).
		SetCreateTime(opts.createTime).
		Save(f.ctx)
	require.NoError(f.t, err)

	_, err = f.client.TransferSender.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(opts.sender).
		SetCreateTime(opts.createTime).
		SetTransferType(transfer.Type).
		Save(f.ctx)
	require.NoError(f.t, err)

	_, err = f.client.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(opts.receiver).
		SetStatus(opts.receiverStatus).
		SetCreateTime(opts.createTime).
		SetTransferType(transfer.Type).
		Save(f.ctx)
	require.NoError(f.t, err)

	for _, extra := range opts.extraReceivers {
		_, err := f.client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(extra.pubkey).
			SetStatus(extra.status).
			SetCreateTime(opts.createTime).
			SetTransferType(transfer.Type).
			Save(f.ctx)
		require.NoError(f.t, err)
	}
	return transfer
}

// privacyEnabled installs WalletSetting rows so HasReadAccessToWallet falls
// through to the session check for these wallets. Required for the queried
// wallet so the access check actually runs (rather than being bypassed by
// "no privacy setting → public").
func (f *equivFixture) privacyEnabled(pubkeys ...keys.Public) {
	f.t.Helper()
	for _, pk := range pubkeys {
		_, err := f.client.WalletSetting.Create().
			SetOwnerIdentityPublicKey(pk).
			SetPrivateEnabled(true).
			Save(f.ctx)
		require.NoError(f.t, err)
	}
}

// ctxForViewer returns a context authenticated as the given pubkey.
// QueryAllTransfers routing is purely filter-shape based, so no routing knob is set.
func (f *equivFixture) ctxForViewer(viewer keys.Public) context.Context {
	return authn.InjectSessionForTests(f.ctx, viewer, 9999999999)
}

// setupEquivalenceData populates the fixture with the data shape required by
// the equivalence cases. Returns the slice of transfer IDs created (handy
// for subset checks) but most assertions just look at handler output.
func (f *equivFixture) setupEquivalenceData() {
	f.t.Helper()

	// Privacy-protected wallets — the access check actually runs against
	// these. Other wallets stay public so SSP-style internal queries still
	// work without session injection.
	f.privacyEnabled(f.cold, f.light, f.medium, f.sender, f.both)

	// light: 5 pending receivers, one per pending pair, all on REGTEST and
	// type TRANSFER, with create_time spread so ORDER BY DESC is meaningful.
	for i, p := range pendingPairs {
		f.makeTransfer(makeTransferOpts{
			transferStatus: p.transferStatus,
			receiverStatus: p.receiverStatus,
			sender:         f.newPubkey(),
			receiver:       f.light,
			createTime:     f.baseNow.Add(time.Duration(-30-i) * time.Minute),
		})
	}

	// medium: 20 pending receivers on REGTEST. Mixed pair selection gives
	// status diversity. Spread create_time across 20 distinct minutes.
	for i := range 20 {
		p := pendingPairs[i%len(pendingPairs)]
		f.makeTransfer(makeTransferOpts{
			transferStatus: p.transferStatus,
			receiverStatus: p.receiverStatus,
			sender:         f.newPubkey(),
			receiver:       f.medium,
			createTime:     f.baseNow.Add(time.Duration(-100-i) * time.Minute),
		})
	}

	// medium also has type variation — one transfer of each non-TRANSFER
	// pending type so the types-filter cases have something to match.
	for i, ttype := range []st.TransferType{st.TransferTypeSwap, st.TransferTypePreimageSwap, st.TransferTypePrimarySwapV3, st.TransferTypeCounterSwap} {
		f.makeTransfer(makeTransferOpts{
			transferType:   ttype,
			transferStatus: st.TransferStatusReceiverKeyTweaked,
			receiverStatus: st.TransferReceiverStatusKeyTweaked,
			sender:         f.newPubkey(),
			receiver:       f.medium,
			createTime:     f.baseNow.Add(time.Duration(-200-i) * time.Minute),
		})
	}

	// medium also gets a MAINNET pending transfer to exercise the network
	// filter (REGTEST queries must not return MAINNET rows).
	f.makeTransfer(makeTransferOpts{
		network:        btcnetwork.Mainnet,
		transferStatus: st.TransferStatusReceiverKeyTweaked,
		receiverStatus: st.TransferReceiverStatusKeyTweaked,
		sender:         f.newPubkey(),
		receiver:       f.medium,
		createTime:     f.baseNow.Add(-300 * time.Minute),
	})

	// medium: a COMPLETED transfer that must NOT show up as pending in
	// either path (sanity that exclusion still works).
	f.makeTransfer(makeTransferOpts{
		transferStatus: st.TransferStatusCompleted,
		receiverStatus: st.TransferReceiverStatusCompleted,
		sender:         f.newPubkey(),
		receiver:       f.medium,
		createTime:     f.baseNow.Add(-400 * time.Minute),
	})

	// sender: sender-pending transfers. Mix of expired (qualifies) and
	// not-yet-expired (excluded). Both paths apply expiry_time < <now>
	// where <now> is sampled in Go at request entry.
	for i, st0 := range []st.TransferStatus{st.TransferStatusSenderKeyTweakPending, st.TransferStatusSenderInitiated} {
		// Expired — qualifies as pending in both paths.
		f.makeTransfer(makeTransferOpts{
			transferStatus: st0,
			receiverStatus: st.TransferReceiverStatusInitiated,
			sender:         f.sender,
			receiver:       f.newPubkey(),
			expiryTime:     f.baseNow.Add(-1 * time.Hour),
			createTime:     f.baseNow.Add(time.Duration(-50-i) * time.Minute),
		})
		// Not yet expired — must be excluded by both paths.
		f.makeTransfer(makeTransferOpts{
			transferStatus: st0,
			receiverStatus: st.TransferReceiverStatusInitiated,
			sender:         f.sender,
			receiver:       f.newPubkey(),
			expiryTime:     f.baseNow.Add(24 * time.Hour),
			createTime:     f.baseNow.Add(time.Duration(-60-i) * time.Minute),
		})
	}

	// both: one transfer where `both` is the sender (sender-pending,
	// expired) and one where `both` is the receiver (receiver-pending).
	// Used by the sender_or_receiver cases.
	f.makeTransfer(makeTransferOpts{
		transferStatus: st.TransferStatusSenderKeyTweakPending,
		receiverStatus: st.TransferReceiverStatusInitiated,
		sender:         f.both,
		receiver:       f.newPubkey(),
		expiryTime:     f.baseNow.Add(-1 * time.Hour),
		createTime:     f.baseNow.Add(-70 * time.Minute),
	})
	f.makeTransfer(makeTransferOpts{
		transferStatus: st.TransferStatusReceiverKeyTweaked,
		receiverStatus: st.TransferReceiverStatusKeyTweaked,
		sender:         f.newPubkey(),
		receiver:       f.both,
		createTime:     f.baseNow.Add(-80 * time.Minute),
	})
	// both: same pubkey on sender (column) AND receiver (edge) sides. SR1's
	// sender arm does NOT match this row (t.status = RECEIVER_KEY_TWEAKED is
	// not in PendingSenderStatuses); only the receiver arm matches. UNION ALL
	// dedup is not exercised here — and can't be, given the disjointness
	// invariant locked by TestPendingStatusesDisjoint in so/mimo.
	f.makeTransfer(makeTransferOpts{
		transferStatus: st.TransferStatusReceiverKeyTweaked,
		receiverStatus: st.TransferReceiverStatusKeyTweaked,
		sender:         f.both,
		receiver:       f.both,
		createTime:     f.baseNow.Add(-90 * time.Minute),
	})

	// Multi-receiver transfer: light is the primary queried receiver, the
	// extra receiver is a separate pubkey. Both receivers are in pending
	// states. This isolates the MarshalProto-vs-MarshalProtoForReceiver
	// divergence on multi-receiver shapes.
	f.multiReceiverPrimary = f.newPubkey()
	f.multiReceiverExtra = f.newPubkey()
	f.privacyEnabled(f.multiReceiverPrimary)
	multi := f.makeTransfer(makeTransferOpts{
		transferStatus: st.TransferStatusReceiverKeyTweaked,
		receiverStatus: st.TransferReceiverStatusKeyTweaked,
		sender:         f.newPubkey(),
		receiver:       f.multiReceiverPrimary,
		createTime:     f.baseNow.Add(-110 * time.Minute),
		extraReceivers: []extraReceiverEquiv{
			{pubkey: f.multiReceiverExtra, status: st.TransferReceiverStatusKeyTweaked},
		},
	})
	f.multiReceiverTransferID = multi.ID
}

// runBothPaths invokes each pending path on the same filter — legacy
// queryTransfers (pendingOnly) and queryPendingTransfersMIMO — under one
// knob-free authenticated context. Returns both responses + errors.
func (f *equivFixture) runBothPaths(viewer keys.Public, filter *pb.TransferFilter) (legacyResp, mimoResp *pb.QueryTransfersResponse, legacyErr, mimoErr error) {
	f.t.Helper()
	ctx := f.ctxForViewer(viewer)
	legacyResp, legacyErr = f.handler.queryTransfers(ctx, filter, true, false)
	mimoResp, mimoErr = f.handler.queryPendingTransfersMIMO(ctx, filter)
	return legacyResp, mimoResp, legacyErr, mimoErr
}

// transferIDsOf extracts the ordered transfer IDs from a response. nil-safe.
func transferIDsOf(resp *pb.QueryTransfersResponse) []string {
	if resp == nil {
		return nil
	}
	ids := make([]string, len(resp.GetTransfers()))
	for i, t := range resp.GetTransfers() {
		ids[i] = t.GetId()
	}
	return ids
}

// leafIDSetOf returns the sorted set of leaf-row IDs on a transfer proto, independent of order.
func leafIDSetOf(t *pb.Transfer) []string {
	ids := make([]string, len(t.GetLeaves()))
	for i, l := range t.GetLeaves() {
		ids[i] = l.GetLeaf().GetId()
	}
	slices.Sort(ids)
	return ids
}

// assertResultsEquivalent validates the equivalence contract between the
// legacy and MIMO paths for a single filter. The contract:
//   - errors agree (both nil or both non-nil with the same gRPC code)
//   - the ordered list of transfer IDs is identical
//   - the response Offset is identical
//   - per-transfer projection (Status, Type, Network) matches by ID
//   - per-transfer leaf-id sets match (ElementsMatch — order is undefined
//     across the two marshaling paths)
func assertResultsEquivalent(t *testing.T, name string, legacy, mimo *pb.QueryTransfersResponse, legacyErr, mimoErr error) {
	t.Helper()
	if legacyErr != nil || mimoErr != nil {
		assert.Equal(t, status.Code(legacyErr), status.Code(mimoErr),
			"%s: gRPC code mismatch (legacy=%v, mimo=%v)", name, legacyErr, mimoErr)
		// If both errored, no further comparison.
		if legacyErr != nil && mimoErr != nil {
			return
		}
		t.Fatalf("%s: only one path errored (legacy=%v, mimo=%v)", name, legacyErr, mimoErr)
	}

	legacyIDs := transferIDsOf(legacy)
	mimoIDs := transferIDsOf(mimo)
	if !assert.Equal(t, legacyIDs, mimoIDs, "%s: transfer ID order mismatch", name) {
		return
	}
	assert.Equal(t, legacy.GetOffset(), mimo.GetOffset(), "%s: response Offset mismatch", name)

	mimoByID := make(map[string]*pb.Transfer, len(mimo.GetTransfers()))
	for _, t := range mimo.GetTransfers() {
		mimoByID[t.GetId()] = t
	}
	for _, lt := range legacy.GetTransfers() {
		mt, ok := mimoByID[lt.GetId()]
		require.True(t, ok, "%s: transfer %s in legacy response missing from MIMO", name, lt.GetId())
		assert.Equal(t, lt.GetStatus(), mt.GetStatus(), "%s: transfer %s Status mismatch", name, lt.GetId())
		assert.Equal(t, lt.GetType(), mt.GetType(), "%s: transfer %s Type mismatch", name, lt.GetId())
		assert.Equal(t, lt.GetNetwork(), mt.GetNetwork(), "%s: transfer %s Network mismatch", name, lt.GetId())
		assert.ElementsMatch(t, leafIDSetOf(lt), leafIDSetOf(mt),
			"%s: transfer %s leaf-id set mismatch (legacy uses MarshalProto, MIMO uses MarshalProtoForReceiver — single-receiver should be equivalent)", name, lt.GetId())
	}
}

// receiverFilter is a test-helper for building a TransferFilter rooted at the
// receiver participant variant.
func receiverFilter(pubkey keys.Public) *pb.TransferFilter {
	return &pb.TransferFilter{
		Participant: &pb.TransferFilter_ReceiverIdentityPublicKey{
			ReceiverIdentityPublicKey: pubkey.Serialize(),
		},
		Network: pb.Network_REGTEST,
	}
}

func senderFilter(pubkey keys.Public) *pb.TransferFilter {
	return &pb.TransferFilter{
		Participant: &pb.TransferFilter_SenderIdentityPublicKey{
			SenderIdentityPublicKey: pubkey.Serialize(),
		},
		Network: pb.Network_REGTEST,
	}
}

func senderOrReceiverFilter(pubkey keys.Public) *pb.TransferFilter {
	return &pb.TransferFilter{
		Participant: &pb.TransferFilter_SenderOrReceiverIdentityPublicKey{
			SenderOrReceiverIdentityPublicKey: pubkey.Serialize(),
		},
		Network: pb.Network_REGTEST,
	}
}

// -----------------------------------------------------------------------------
// Table-driven equivalence cases.
// -----------------------------------------------------------------------------

func TestQueryPendingTransfers_Equivalence(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("equivalence tests require Postgres (raw SQL uses pq.Array + ANY/NOW)")
	}
	f := newEquivFixture(t)
	f.setupEquivalenceData()

	// Pre-pick a real transfer ID for the singular cases. The light wallet
	// has 5 pending receivers — one per pendingPair; any will do.
	resp, err := f.handler.queryTransfers(f.ctxForViewer(f.light), receiverFilter(f.light), true, false)
	require.NoError(t, err)
	require.NotEmpty(t, resp.GetTransfers(), "fixture should produce light-pending transfers")
	lightTransferID := resp.GetTransfers()[0].GetId()
	otherWalletTransferIDs := make([]string, 0, 3)
	for i, tr := range resp.GetTransfers() {
		if i >= 3 {
			break
		}
		otherWalletTransferIDs = append(otherWalletTransferIDs, tr.GetId())
	}

	cases := []struct {
		name   string
		viewer keys.Public
		filter *pb.TransferFilter
	}{
		// R1 — receiver bare + network
		{"R1_receiver_cold_pubkey", f.cold, receiverFilter(f.cold)},
		{"R1_receiver_light_pubkey", f.light, receiverFilter(f.light)},
		{"R1_receiver_medium_pubkey", f.medium, receiverFilter(f.medium)},

		// R2 — receiver + types
		{
			"R2_receiver_types_swap_only", f.medium,
			withTypes(receiverFilter(f.medium), pb.TransferType_SWAP),
		},
		{
			"R2_receiver_types_swap_family", f.medium,
			withTypes(receiverFilter(f.medium), pb.TransferType_SWAP, pb.TransferType_PREIMAGE_SWAP, pb.TransferType_PRIMARY_SWAP_V3),
		},
		{
			"R2_receiver_types_no_match", f.cold,
			withTypes(receiverFilter(f.cold), pb.TransferType_TRANSFER),
		},
		{
			"R2_receiver_types_counter_swap", f.medium,
			withTypes(receiverFilter(f.medium), pb.TransferType_COUNTER_SWAP),
		},

		// R3 — singular by transfer_id
		{
			"R3_singular_existing_pending", f.light,
			withTransferIDs(receiverFilter(f.light), lightTransferID),
		},
		{
			"R3_singular_nonexistent", f.light,
			withTransferIDs(receiverFilter(f.light), uuid.New().String()),
		},
		{
			"R3_singular_multiple_ids", f.light,
			withTransferIDs(receiverFilter(f.light), otherWalletTransferIDs...),
		},
		{
			"R3_singular_id_for_other_pubkey", f.cold,
			withTransferIDs(receiverFilter(f.cold), lightTransferID),
		},

		// S1 — sender bare
		{"S1_sender_bare", f.sender, senderFilter(f.sender)},
		{"S1_sender_no_pending_pubkey", f.cold, senderFilter(f.cold)},

		// SR1 — sender_or_receiver
		{"SR1_both_arms", f.both, senderOrReceiverFilter(f.both)},
		{"SR1_receiver_only_arm", f.light, senderOrReceiverFilter(f.light)},
		{"SR1_sender_only_arm", f.sender, senderOrReceiverFilter(f.sender)},
		{"SR1_neither_arm", f.other, senderOrReceiverFilter(f.other)},

		// Time / order / pagination
		{
			"TIME_created_after_excludes_old", f.medium,
			withCreatedAfter(receiverFilter(f.medium), f.baseNow.Add(-150*time.Minute)),
		},
		{
			"TIME_created_before_excludes_recent", f.medium,
			withCreatedBefore(receiverFilter(f.medium), f.baseNow.Add(-150*time.Minute)),
		},
		{
			"ORDER_ascending", f.medium,
			withOrder(receiverFilter(f.medium), pb.Order_ASCENDING),
		},
		{
			"PAGE_limit_below_count", f.medium,
			withLimitOffset(receiverFilter(f.medium), 2, 0),
		},
		{
			"PAGE_limit_above_count", f.cold,
			withLimitOffset(receiverFilter(f.cold), 100, 0),
		},
		{
			"PAGE_deep_offset", f.medium,
			withLimitOffset(receiverFilter(f.medium), 5, 10),
		},
		{
			"PAGE_offset_past_end", f.medium,
			withLimitOffset(receiverFilter(f.medium), 5, 1000),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			legacy, mimo, lerr, merr := f.runBothPaths(tc.viewer, tc.filter)
			assertResultsEquivalent(t, tc.name, legacy, mimo, lerr, merr)
		})
	}
}

// -----------------------------------------------------------------------------
// Access-check equivalence
// -----------------------------------------------------------------------------

// Privacy on + session matches → both paths return rows.
// Privacy on + session mismatch → both paths return empty (Offset=-1).
// Privacy on + no session → both paths return empty (Offset=-1).

func TestQueryPendingTransfers_Equivalence_Access_SessionMatches(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("equivalence tests require Postgres")
	}
	f := newEquivFixture(t)
	f.setupEquivalenceData()

	legacy, mimo, lerr, merr := f.runBothPaths(f.light, receiverFilter(f.light))
	assertResultsEquivalent(t, "access_session_matches", legacy, mimo, lerr, merr)
	assert.NotEmpty(t, legacy.GetTransfers(), "expected non-empty result when session matches")
}

func TestQueryPendingTransfers_Equivalence_Access_SessionMismatch(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("equivalence tests require Postgres")
	}
	f := newEquivFixture(t)
	f.setupEquivalenceData()

	// Authenticate as `other`, query for `light`. Privacy is on for `light`,
	// so the access check must reject and both paths must return empty.
	ctx := f.ctxForViewer(f.other)
	respLegacy, errLegacy := f.handler.queryTransfers(ctx, receiverFilter(f.light), true, false)
	respMIMO, errMIMO := f.handler.queryPendingTransfersMIMO(ctx, receiverFilter(f.light))

	assertResultsEquivalent(t, "access_session_mismatch", respLegacy, respMIMO, errLegacy, errMIMO)
	assert.Empty(t, respLegacy.GetTransfers(), "expected empty result on session mismatch")
	assert.Equal(t, int64(-1), respLegacy.GetOffset(), "expected Offset=-1 on session mismatch")
}

func TestQueryPendingTransfers_Equivalence_Access_NoSession(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("equivalence tests require Postgres")
	}
	f := newEquivFixture(t)
	f.setupEquivalenceData()

	// No session injected — the whole point is no-session + privacy enforced. Both
	// paths must reject via the access check and return empty.
	ctx := f.ctx
	respLegacy, errLegacy := f.handler.queryTransfers(ctx, receiverFilter(f.light), true, false)
	respMIMO, errMIMO := f.handler.queryPendingTransfersMIMO(ctx, receiverFilter(f.light))

	assertResultsEquivalent(t, "access_no_session", respLegacy, respMIMO, errLegacy, errMIMO)
	assert.Empty(t, respLegacy.GetTransfers(), "expected empty result with no session + privacy enabled")
}

// -----------------------------------------------------------------------------
// Multi-receiver: highlight the MarshalProto vs MarshalProtoForReceiver split.
// -----------------------------------------------------------------------------

// TestQueryPendingTransfers_Equivalence_MultiReceiver verifies that for a
// multi-receiver transfer queried by one of its receivers, both paths return
// the same transfer ID — but the per-leaf projection MAY differ if the
// transfer has leaves, since legacy uses MarshalProto (all leaves) and MIMO
// uses MarshalProtoForReceiver (just the queried receiver's leaves).
//
// In MIMO MVP single-receiver this divergence is hidden because each transfer
// has at most one receiver edge. The fixture under test deliberately includes
// a multi-receiver transfer with NO leaves so the single-call equivalence
// (transfer ID, status, type, network) holds. If/when multi-receiver
// transfers carry receiver-tagged leaves in production, this assertion will
// surface the divergence loudly.
func TestQueryPendingTransfers_Equivalence_MultiReceiver(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("equivalence tests require Postgres")
	}
	f := newEquivFixture(t)
	f.setupEquivalenceData()

	legacy, mimo, lerr, merr := f.runBothPaths(f.multiReceiverPrimary, receiverFilter(f.multiReceiverPrimary))
	assertResultsEquivalent(t, "multi_receiver_primary", legacy, mimo, lerr, merr)

	// Sanity: the multi-receiver transfer is in both responses.
	require.Len(t, legacy.GetTransfers(), 1)
	require.Len(t, mimo.GetTransfers(), 1)
	assert.Equal(t, f.multiReceiverTransferID.String(), legacy.GetTransfers()[0].GetId())
	assert.Equal(t, f.multiReceiverTransferID.String(), mimo.GetTransfers()[0].GetId())
}

// -----------------------------------------------------------------------------
// Pagination consistency across the legacy/MIMO handoff
// -----------------------------------------------------------------------------

// TestQueryPendingTransfers_Equivalence_PaginationCrossKnob proves the paging
// handoff is safe: a caller that pages via the legacy path and then again via
// the MIMO path sees no overlap, no drops, and the union equals a single
// full-page call. Both directions of the handoff are exercised.
func TestQueryPendingTransfers_Equivalence_PaginationCrossKnob(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("equivalence tests require Postgres")
	}
	f := newEquivFixture(t)
	f.setupEquivalenceData()

	const pageSize = 5
	ctx := f.ctxForViewer(f.medium)

	// Single full-page query (limit=10, offset=0) under the legacy path —
	// canonical reference for the union of two halves.
	full, err := f.handler.queryTransfers(
		ctx,
		withLimitOffset(receiverFilter(f.medium), 2*pageSize, 0),
		true, false,
	)
	require.NoError(t, err)
	require.Len(t, full.GetTransfers(), 2*pageSize, "fixture should provide at least 10 medium-pending transfers")
	fullIDs := transferIDsOf(full)

	// Page 1 under legacy.
	page1, err := f.handler.queryTransfers(
		ctx,
		withLimitOffset(receiverFilter(f.medium), pageSize, 0),
		true, false,
	)
	require.NoError(t, err)
	page1IDs := transferIDsOf(page1)

	// Page 2 under MIMO — the contract must hold across the path handoff.
	page2, err := f.handler.queryPendingTransfersMIMO(
		ctx,
		withLimitOffset(receiverFilter(f.medium), pageSize, pageSize),
	)
	require.NoError(t, err)
	page2IDs := transferIDsOf(page2)

	assert.Equal(t, fullIDs[:pageSize], page1IDs, "page 1 (legacy) does not match first half of full page")
	assert.Equal(t, fullIDs[pageSize:], page2IDs, "page 2 (MIMO) does not match second half of full page")

	// And the reverse direction — page1 MIMO + page2 legacy.
	page1Mimo, err := f.handler.queryPendingTransfersMIMO(
		ctx,
		withLimitOffset(receiverFilter(f.medium), pageSize, 0),
	)
	require.NoError(t, err)
	page2Legacy, err := f.handler.queryTransfers(
		ctx,
		withLimitOffset(receiverFilter(f.medium), pageSize, pageSize),
		true, false,
	)
	require.NoError(t, err)
	assert.Equal(t, fullIDs[:pageSize], transferIDsOf(page1Mimo), "page 1 (MIMO) does not match first half of full page (reverse direction)")
	assert.Equal(t, fullIDs[pageSize:], transferIDsOf(page2Legacy), "page 2 (legacy) does not match second half of full page (reverse direction)")
}

// -----------------------------------------------------------------------------
// Cross-path pagination — one helper, three coverage extensions
// -----------------------------------------------------------------------------

// crossKnobPaginationCheck verifies page-by-page pagination is consistent
// across the legacy/MIMO handoff. For each page index, both the legacy path
// and the MIMO path must return the same window of transfer IDs as the
// corresponding slice of a single full-sweep call under legacy — a caller
// that pages with the path handoff between requests must see no overlap and
// no drops.
//
// Caller is responsible for ensuring `viewer` has at least
// pageSize*pageCount qualifying pending transfers under `filter`.
func (f *equivFixture) crossKnobPaginationCheck(t *testing.T, viewer keys.Public, filter *pb.TransferFilter, pageSize, pageCount int) {
	t.Helper()

	ctx := f.ctxForViewer(viewer)

	full, err := f.handler.queryTransfers(
		ctx,
		withLimitOffset(filter, int64(pageSize*pageCount), 0),
		true, false,
	)
	require.NoError(t, err)
	require.Lenf(t, full.GetTransfers(), pageSize*pageCount,
		"fixture must produce >= %d pending transfers for cross-path pagination", pageSize*pageCount)
	fullIDs := transferIDsOf(full)

	paths := []struct {
		label string
		run   func(f2 *pb.TransferFilter) (*pb.QueryTransfersResponse, error)
	}{
		{"legacy", func(f2 *pb.TransferFilter) (*pb.QueryTransfersResponse, error) {
			return f.handler.queryTransfers(ctx, f2, true, false)
		}},
		{"MIMO", func(f2 *pb.TransferFilter) (*pb.QueryTransfersResponse, error) {
			return f.handler.queryPendingTransfersMIMO(ctx, f2)
		}},
	}

	for page := range pageCount {
		offset := int64(page * pageSize)
		expected := fullIDs[page*pageSize : (page+1)*pageSize]

		for _, kb := range paths {
			resp, err := kb.run(withLimitOffset(filter, int64(pageSize), offset))
			require.NoErrorf(t, err, "page %d (%s)", page, kb.label)
			assert.Equalf(t, expected, transferIDsOf(resp),
				"page %d (%s): pagination window does not match the full-sweep reference",
				page, kb.label)
		}
	}
}

// TestQueryPendingTransfers_Equivalence_PaginationCrossKnob_Ascending locks
// the C3 fix (matching secondary id sort direction) across the legacy/MIMO
// handoff. The single-path ORDER_ascending case in the table-driven suite
// passes only because the fixture spreads create_time across distinct
// minutes; this test exercises the cross-path pagination handoff in ASC mode.
func TestQueryPendingTransfers_Equivalence_PaginationCrossKnob_Ascending(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("equivalence tests require Postgres")
	}
	f := newEquivFixture(t)
	f.setupEquivalenceData()

	f.crossKnobPaginationCheck(t, f.medium,
		withOrder(receiverFilter(f.medium), pb.Order_ASCENDING), 5, 2)
}

// TestQueryPendingTransfers_Equivalence_PaginationCrossKnob_Sender locks
// cross-path pagination on the participant=Sender path. The PR's audit
// confirmed no internal callers, but external SDK callers may pass
// participant=Sender, so this path needs the same legacy/MIMO handoff
// guarantees as Receiver and SenderOrReceiver.
//
// Uses a dedicated pubkey with 10 pending senders (the shared fixture's
// f.sender only has 2, insufficient for 2 pages of 5).
func TestQueryPendingTransfers_Equivalence_PaginationCrossKnob_Sender(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("equivalence tests require Postgres")
	}
	f := newEquivFixture(t)
	f.setupEquivalenceData()

	sender := f.newPubkey()
	f.privacyEnabled(sender)
	for i := range 10 {
		status := st.TransferStatusSenderKeyTweakPending
		if i%2 == 1 {
			status = st.TransferStatusSenderInitiated
		}
		f.makeTransfer(makeTransferOpts{
			transferStatus: status,
			receiverStatus: st.TransferReceiverStatusInitiated,
			sender:         sender,
			receiver:       f.newPubkey(),
			expiryTime:     f.baseNow.Add(-1 * time.Hour),
			createTime:     f.baseNow.Add(time.Duration(-500-i) * time.Minute),
		})
	}

	f.crossKnobPaginationCheck(t, sender, senderFilter(sender), 5, 2)
}

// TestQueryPendingTransfers_Equivalence_PaginationCrossKnob_SR1_DeepOffset
// locks cross-path pagination on the participant=SenderOrReceiver path at
// 3 pages of size 5 (offset reaches 10). This exercises the
// perArmLimit = offset+limit math in buildPendingIDsQuerySenderOrReceiver
// — at offset=10 each arm must walk far enough that the merged stream has
// 15 candidates available.
//
// Uses a dedicated pubkey with 8 sender-pending + 8 receiver-pending = 16
// total (perArmLimit=15 in the deepest page).
func TestQueryPendingTransfers_Equivalence_PaginationCrossKnob_SR1_DeepOffset(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("equivalence tests require Postgres")
	}
	f := newEquivFixture(t)
	f.setupEquivalenceData()

	sr := f.newPubkey()
	f.privacyEnabled(sr)

	// Sender-pending half — alternating SENDER_KEY_TWEAK_PENDING / SENDER_INITIATED.
	for i := range 8 {
		status := st.TransferStatusSenderKeyTweakPending
		if i%2 == 1 {
			status = st.TransferStatusSenderInitiated
		}
		f.makeTransfer(makeTransferOpts{
			transferStatus: status,
			receiverStatus: st.TransferReceiverStatusInitiated,
			sender:         sr,
			receiver:       f.newPubkey(),
			expiryTime:     f.baseNow.Add(-1 * time.Hour),
			createTime:     f.baseNow.Add(time.Duration(-700-i) * time.Minute),
		})
	}

	// Receiver-pending half — varied across pendingPairs.
	for i := range 8 {
		pair := pendingPairs[i%len(pendingPairs)]
		f.makeTransfer(makeTransferOpts{
			transferStatus: pair.transferStatus,
			receiverStatus: pair.receiverStatus,
			sender:         f.newPubkey(),
			receiver:       sr,
			createTime:     f.baseNow.Add(time.Duration(-800-i) * time.Minute),
		})
	}

	f.crossKnobPaginationCheck(t, sr, senderOrReceiverFilter(sr), 5, 3)
}

// -----------------------------------------------------------------------------
// Tied-create_time ordering — the step-2 ent secondary-sort regression test
// -----------------------------------------------------------------------------

// TestQueryPendingTransfers_Equivalence_TiedCreateTime asserts that when
// multiple pending transfers share an identical create_time, the MIMO path
// returns them in a direction-consistent secondary order:
//
//	ASC  → (create_time ASC,  id ASC)
//	DESC → (create_time DESC, id DESC)
//
// This is the contract the step-1 raw SQL produces; step-2 ent must preserve
// it. Pre-fix, step-2 hardcoded id DESC for both directions, so ASC mode
// would silently reverse tied-row order across the legacy/MIMO handoff.
//
// Legacy queryTransfers has no secondary sort on id; its behavior on ties is
// Postgres-native (heap order, indeterminate). This test asserts MIMO
// self-consistency (DESC == reverse(ASC)) and SET-equivalence with legacy —
// NOT order-equivalence with legacy on ties.
//
// This asymmetry is intentional and pre-existing: legacy has been
// non-deterministic on ties in production for as long as queryTransfers has
// existed; MIMO is strictly better. Across the legacy/MIMO handoff, a caller
// polling tied rows could see them reorder between requests — that's a known
// acknowledged consequence of replacing a non-deterministic path with a
// deterministic one, not a regression introduced by this PR.
//
// Future editors: do NOT tighten the legacy assertion to order-equivalence
// without first adding a tiebreaker to queryTransfers' ORDER BY (and
// re-validating the full legacy perf table — the R1 stuck-user case is the
// cardinality regime most at risk of plan-shape change from a new sort
// column).
func TestQueryPendingTransfers_Equivalence_TiedCreateTime(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("equivalence tests require Postgres")
	}
	f := newEquivFixture(t)

	// Dedicated pubkey — no contamination from setupEquivalenceData.
	receiver := f.newPubkey()
	f.privacyEnabled(receiver)

	// 5 transfers sharing the exact same create_time and same pending pair.
	const tieCount = 5
	tieTime := f.baseNow.Add(-2 * time.Hour)
	for range tieCount {
		f.makeTransfer(makeTransferOpts{
			transferStatus: st.TransferStatusReceiverKeyTweaked,
			receiverStatus: st.TransferReceiverStatusKeyTweaked,
			sender:         f.newPubkey(),
			receiver:       receiver,
			createTime:     tieTime,
		})
	}

	ctx := f.ctxForViewer(receiver)

	respASC, err := f.handler.queryPendingTransfersMIMO(ctx, withOrder(receiverFilter(receiver), pb.Order_ASCENDING))
	require.NoError(t, err)
	require.Len(t, respASC.GetTransfers(), tieCount)
	idsASC := transferIDsOf(respASC)

	respDESC, err := f.handler.queryPendingTransfersMIMO(ctx, withOrder(receiverFilter(receiver), pb.Order_DESCENDING))
	require.NoError(t, err)
	require.Len(t, respDESC.GetTransfers(), tieCount)
	idsDESC := transferIDsOf(respDESC)

	// MIMO must be self-consistent across order direction on ties.
	reversed := slices.Clone(idsASC)
	slices.Reverse(reversed)
	assert.Equal(t, idsDESC, reversed,
		"MIMO DESC must be the exact reverse of MIMO ASC on tied-create_time rows; pre-fix, step-2 hardcoded id DESC for both directions and reversed tied-row order in ASC mode")

	// MIMO ASC ties must come back in id ASC order (the step-1 SQL contract).
	asciiSortedASC := slices.Sorted(slices.Values(idsASC))
	assert.Equal(t, asciiSortedASC, idsASC, "MIMO ASC must return tied rows in id ASC order")

	// SET-equivalence with legacy on ties (order may differ — legacy has no
	// secondary sort).
	respLegacyDESC, err := f.handler.queryTransfers(ctx, receiverFilter(receiver), true, false)
	require.NoError(t, err)
	legacyIDs := transferIDsOf(respLegacyDESC)
	require.Len(t, legacyIDs, tieCount)
	assert.ElementsMatch(t, legacyIDs, idsDESC,
		"legacy and MIMO must return the same SET of pending transfers on tied-create_time rows; only intra-tie order may differ")
}

// -----------------------------------------------------------------------------
// Filter builders
// -----------------------------------------------------------------------------

func withTypes(filter *pb.TransferFilter, types ...pb.TransferType) *pb.TransferFilter {
	filter.Types = types
	return filter
}

func withTransferIDs(filter *pb.TransferFilter, ids ...string) *pb.TransferFilter {
	filter.TransferIds = ids
	return filter
}

func withCreatedAfter(filter *pb.TransferFilter, ts time.Time) *pb.TransferFilter {
	filter.TimeFilter = &pb.TransferFilter_CreatedAfter{
		CreatedAfter: timestamppb.New(ts),
	}
	return filter
}

func withCreatedBefore(filter *pb.TransferFilter, ts time.Time) *pb.TransferFilter {
	filter.TimeFilter = &pb.TransferFilter_CreatedBefore{
		CreatedBefore: timestamppb.New(ts),
	}
	return filter
}

func withOrder(filter *pb.TransferFilter, order pb.Order) *pb.TransferFilter {
	filter.Order = order
	return filter
}

func withLimitOffset(filter *pb.TransferFilter, limit, offset int64) *pb.TransferFilter {
	filter.Limit = limit
	filter.Offset = offset
	return filter
}

// -----------------------------------------------------------------------------
// MIMO-only contract tests (below the public handler boundary).
//
// Pin the contract that the MIMO sender-pending expiry predicate filters on
// args.now rather than Postgres NOW(). Calls the dispatch directly — at the
// public handler boundary args.now is always time.Now(), so a deterministic
// boundary case is invisible there. fixedNow sits an hour in the past so
// wall-clock NOW() is far ahead of every fixture; if the SQL were still using
// NOW(), the after-fixedNow transfer would also come back.
// -----------------------------------------------------------------------------

func TestQueryMIMOPendingTransferIDs_NowIsBoundParameter_S1(t *testing.T) {
	f := newEquivFixture(t)
	fixedNow := f.baseNow.Add(-1 * time.Hour)

	sender := f.newPubkey()
	f.privacyEnabled(sender)

	expired := f.makeTransfer(makeTransferOpts{
		transferStatus: st.TransferStatusSenderKeyTweakPending,
		receiverStatus: st.TransferReceiverStatusInitiated,
		sender:         sender,
		receiver:       f.newPubkey(),
		expiryTime:     fixedNow.Add(-1 * time.Millisecond),
		createTime:     f.baseNow.Add(-50 * time.Minute),
	})

	f.makeTransfer(makeTransferOpts{
		transferStatus: st.TransferStatusSenderKeyTweakPending,
		receiverStatus: st.TransferReceiverStatusInitiated,
		sender:         sender,
		receiver:       f.newPubkey(),
		expiryTime:     fixedNow.Add(1 * time.Millisecond),
		createTime:     f.baseNow.Add(-51 * time.Minute),
	})

	ids, err := queryMIMOPendingTransferIDs(f.ctx, f.client, queryMIMOPendingArgs{
		participant:  participantRoleSender,
		walletPubkey: sender,
		network:      pb.Network_REGTEST,
		order:        pb.Order_DESCENDING,
		limit:        100,
		offset:       0,
		now:          fixedNow,
	})
	require.NoError(t, err)

	require.Len(t, ids, 1, "args.now not bound: after-fixedNow row leaked through")
	assert.Equal(t, expired.ID, ids[0])
}

func TestQueryMIMOPendingTransferIDs_NowIsBoundParameter_SR1(t *testing.T) {
	f := newEquivFixture(t)
	fixedNow := f.baseNow.Add(-1 * time.Hour)

	sender := f.newPubkey()
	f.privacyEnabled(sender)

	expired := f.makeTransfer(makeTransferOpts{
		transferStatus: st.TransferStatusSenderKeyTweakPending,
		receiverStatus: st.TransferReceiverStatusInitiated,
		sender:         sender,
		receiver:       f.newPubkey(),
		expiryTime:     fixedNow.Add(-1 * time.Millisecond),
		createTime:     f.baseNow.Add(-50 * time.Minute),
	})
	f.makeTransfer(makeTransferOpts{
		transferStatus: st.TransferStatusSenderKeyTweakPending,
		receiverStatus: st.TransferReceiverStatusInitiated,
		sender:         sender,
		receiver:       f.newPubkey(),
		expiryTime:     fixedNow.Add(1 * time.Millisecond),
		createTime:     f.baseNow.Add(-51 * time.Minute),
	})

	ids, err := queryMIMOPendingTransferIDs(f.ctx, f.client, queryMIMOPendingArgs{
		participant:  participantRoleSenderOrReceiver,
		walletPubkey: sender,
		network:      pb.Network_REGTEST,
		order:        pb.Order_DESCENDING,
		limit:        100,
		offset:       0,
		now:          fixedNow,
	})
	require.NoError(t, err)

	require.Len(t, ids, 1, "SR1 sender arm: args.now not bound")
	assert.Equal(t, expired.ID, ids[0])
}

// Guard against the silent failure mode where a future caller omits args.now:
// time.Time{} encodes as '0001-01-01' in Postgres and the sender-arm predicate
// becomes unsatisfiable, returning zero rows with no error.
func TestQueryMIMOPendingTransferIDs_RequiresNow(t *testing.T) {
	f := newEquivFixture(t)

	_, err := queryMIMOPendingTransferIDs(f.ctx, f.client, queryMIMOPendingArgs{
		participant:  participantRoleSender,
		walletPubkey: f.newPubkey(),
		network:      pb.Network_REGTEST,
		order:        pb.Order_DESCENDING,
		limit:        100,
		offset:       0,
		// now intentionally omitted
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "now")
}

// -----------------------------------------------------------------------------
// QueryAllTransfers equivalence — legacy queryTransfers vs the specialized
// MIMO paths reached through QueryAllTransfers' shape-based routing. The paths
// must return the same transfer IDs, ordering, and per-transfer projections.
// -----------------------------------------------------------------------------

// runBothPathsAllTransfers obtains the legacy result by calling queryTransfers
// directly and the MIMO result by calling QueryAllTransfers (which routes to a
// specialized path purely by filter shape) — both on the same authenticated
// context and filter.
func (f *equivFixture) runBothPathsAllTransfers(viewer keys.Public, filter *pb.TransferFilter) (legacyResp, mimoResp *pb.QueryTransfersResponse, legacyErr, mimoErr error) {
	f.t.Helper()
	ctx := f.ctxForViewer(viewer)
	legacyResp, legacyErr = f.handler.queryTransfers(ctx, filter, false, false)
	mimoResp, mimoErr = f.handler.QueryAllTransfers(ctx, filter, false)
	return legacyResp, mimoResp, legacyErr, mimoErr
}

func withOutgoingInFlightStatuses(filter *pb.TransferFilter, statuses ...pb.TransferStatus) *pb.TransferFilter {
	return &pb.TransferFilter{
		Participant: filter.GetParticipant(),
		Network:     filter.GetNetwork(),
		Statuses:    statuses,
	}
}

func TestQueryAllTransfers_Equivalence_OutgoingInFlight(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("equivalence tests require Postgres (raw SQL uses pq.Array + ANY)")
	}
	f := newEquivFixture(t)
	f.setupEquivalenceData()

	cases := []struct {
		name   string
		viewer keys.Public
		filter *pb.TransferFilter
	}{
		// Sender + full 4-state — routes to queryOutgoingInFlight. Fixture's
		// f.sender has 4 sender-pending transfers (2 expired + 2 not-yet-expired,
		// 2 SENDER_INITIATED + 2 SENDER_KEY_TWEAK_PENDING). QueryAllTransfers
		// doesn't filter expiry, so all 4 must match in both paths.
		{
			"sender_full_4state",
			f.sender,
			withOutgoingInFlightStatuses(senderFilter(f.sender),
				pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED,
				pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED_COORDINATOR,
				pb.TransferStatus_TRANSFER_STATUS_APPLYING_SENDER_KEY_TWEAK,
				pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING,
			),
		},
		// Sender + single status — subset of the 4-state set. Should route to
		// queryOutgoingInFlight (subset matches the partial's WHERE) and return
		// only the 2 SENDER_INITIATED transfers.
		{
			"sender_single_SENDER_INITIATED",
			f.sender,
			withOutgoingInFlightStatuses(senderFilter(f.sender),
				pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED,
			),
		},
		// Sender + 2-state subset — still inside the partial's WHERE; routes
		// to queryOutgoingInFlight. Returns 4 (all SENDER_INITIATED +
		// SENDER_KEY_TWEAK_PENDING).
		{
			"sender_2state_subset",
			f.sender,
			withOutgoingInFlightStatuses(senderFilter(f.sender),
				pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED,
				pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING,
			),
		},
		// Sender + status outside the partial (SENDER_KEY_TWEAKED) — should
		// fall through to legacy. New handler is bypassed; equivalence still
		// holds because both paths execute the same legacy code.
		{
			"sender_status_outside_partial_falls_through",
			f.sender,
			withOutgoingInFlightStatuses(senderFilter(f.sender),
				pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED,
			),
		},
		// Receiver participant — falls through to legacy unconditionally.
		{
			"receiver_falls_through",
			f.medium,
			withOutgoingInFlightStatuses(receiverFilter(f.medium),
				pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED,
			),
		},
		// nil filter — routing must not panic; both paths fall through to legacy
		// which rejects with InvalidArgument.
		{
			"nil_filter_rejected",
			f.medium,
			nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			legacy, mimo, lerr, merr := f.runBothPathsAllTransfers(tc.viewer, tc.filter)
			assertResultsEquivalent(t, tc.name, legacy, mimo, lerr, merr)
		})
	}
}

// Locks the HasReceiver(walletPubkey) branch in queryOutgoingInFlight: when a
// sender-only filter request hits a transfer where the wallet is also a
// receiver, both paths must marshal the receiver-scoped projection. A
// multi-receiver self-transfer is the discriminator — without the branch,
// MarshalProto would return all leaves while MarshalProtoForReceiver filters
// to just the wallet's leaves.
func TestQueryAllTransfers_Equivalence_OutgoingInFlight_SelfTransfer(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("equivalence tests require Postgres (raw SQL uses pq.Array + ANY)")
	}
	f := newEquivFixture(t)

	self := f.newPubkey()
	other := f.newPubkey()
	f.privacyEnabled(self)
	f.makeTransfer(makeTransferOpts{
		transferStatus: st.TransferStatusSenderInitiated,
		receiverStatus: st.TransferReceiverStatusInitiated,
		sender:         self,
		receiver:       self,
		extraReceivers: []extraReceiverEquiv{
			{pubkey: other, status: st.TransferReceiverStatusInitiated},
		},
		expiryTime: f.baseNow.Add(-1 * time.Hour),
		createTime: f.baseNow.Add(-30 * time.Minute),
	})

	filter := withOutgoingInFlightStatuses(senderFilter(self),
		pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED,
	)
	legacy, mimo, lerr, merr := f.runBothPathsAllTransfers(self, filter)
	assertResultsEquivalent(t, "self_transfer_4state", legacy, mimo, lerr, merr)
}

// -----------------------------------------------------------------------------
// QueryAllTransfers equivalence — legacy queryTransfers vs the specialized
// queryByTypes path. The dominant prod shape is SR1 + 4-type
// [TRANSFER, PREIMAGE_SWAP, COOPERATIVE_EXIT, UTXO_SWAP] + no status filter —
// these tests pin equivalence for that shape and its sender / receiver only
// variants, plus the fall-through cases that must continue to route to legacy.
// -----------------------------------------------------------------------------

func TestQueryAllTransfers_Equivalence_ByTypes(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("equivalence tests require Postgres (raw SQL execution path)")
	}
	f := newEquivFixture(t)
	f.setupEquivalenceData()

	feedTypes := []pb.TransferType{
		pb.TransferType_TRANSFER,
		pb.TransferType_PREIMAGE_SWAP,
		pb.TransferType_COOPERATIVE_EXIT,
		pb.TransferType_UTXO_SWAP,
	}

	cases := []struct {
		name   string
		viewer keys.Public
		filter *pb.TransferFilter
	}{
		// SR1 4-type feed-view — the dominant prod shape this handler targets.
		{
			"SR1_feed_4type_medium",
			f.medium,
			withTypes(senderOrReceiverFilter(f.medium), feedTypes...),
		},
		{
			"SR1_feed_4type_light",
			f.light,
			withTypes(senderOrReceiverFilter(f.light), feedTypes...),
		},
		{
			"SR1_feed_4type_sender",
			f.sender,
			withTypes(senderOrReceiverFilter(f.sender), feedTypes...),
		},
		// Both arms reachable (sender and receiver on different transfers) —
		// dedup is not load-bearing here (no self-transfer) but the UNION must
		// merge correctly.
		{
			"SR1_feed_4type_both",
			f.both,
			withTypes(senderOrReceiverFilter(f.both), feedTypes...),
		},
		// Receiver-only arm.
		{
			"receiver_feed_4type_medium",
			f.medium,
			withTypes(receiverFilter(f.medium), feedTypes...),
		},
		// Sender-only arm.
		{
			"sender_feed_4type_sender",
			f.sender,
			withTypes(senderFilter(f.sender), feedTypes...),
		},
		// Single type — exercises leading-equality on the composite.
		{
			"SR1_single_type_TRANSFER",
			f.medium,
			withTypes(senderOrReceiverFilter(f.medium), pb.TransferType_TRANSFER),
		},
		{
			"SR1_single_type_SWAP",
			f.medium,
			withTypes(senderOrReceiverFilter(f.medium), pb.TransferType_SWAP),
		},
		// Type set with no matches on this wallet — both paths return empty.
		{
			"receiver_types_no_match",
			f.cold,
			withTypes(receiverFilter(f.cold), pb.TransferType_COUNTER_SWAP),
		},
		// ORDER ASCENDING — type composite is DESC, exercises backwards walk.
		{
			"SR1_order_ascending",
			f.medium,
			withOrder(withTypes(senderOrReceiverFilter(f.medium), feedTypes...), pb.Order_ASCENDING),
		},
		// Pagination.
		{
			"SR1_pagination_limit_offset",
			f.medium,
			withLimitOffset(withTypes(senderOrReceiverFilter(f.medium), feedTypes...), 5, 3),
		},
		// Time filters.
		{
			"SR1_created_after",
			f.medium,
			withCreatedAfter(withTypes(senderOrReceiverFilter(f.medium), feedTypes...), f.baseNow.Add(-150*time.Minute)),
		},
		{
			"SR1_created_before",
			f.medium,
			withCreatedBefore(withTypes(senderOrReceiverFilter(f.medium), feedTypes...), f.baseNow.Add(-150*time.Minute)),
		},
		// Fall-through: status filter set — predicate requires len(statuses) == 0.
		{
			"falls_through_when_status_set",
			f.medium,
			func() *pb.TransferFilter {
				flt := withTypes(receiverFilter(f.medium), pb.TransferType_TRANSFER)
				flt.Statuses = []pb.TransferStatus{pb.TransferStatus_TRANSFER_STATUS_COMPLETED}
				return flt
			}(),
		},
		// Fall-through: no types.
		{
			"falls_through_when_no_types",
			f.medium,
			receiverFilter(f.medium),
		},
		// Fall-through: transfer_ids set.
		{
			"falls_through_when_transfer_ids_set",
			f.light,
			withTransferIDs(withTypes(receiverFilter(f.light), pb.TransferType_TRANSFER), uuid.New().String()),
		},
		// Negative pagination — both paths must reject before producing data.
		{
			"negative_limit_rejected",
			f.medium,
			withLimitOffset(withTypes(senderOrReceiverFilter(f.medium), feedTypes...), -1, 0),
		},
		{
			"negative_offset_rejected",
			f.medium,
			withLimitOffset(withTypes(senderOrReceiverFilter(f.medium), feedTypes...), 50, -1),
		},
		// Duplicate types — the per-type rewrite must dedupe so sub-queries stay
		// disjoint by transfer_type; otherwise the SQL page is inflated with
		// repeats and the resulting nextOffset diverges from the legacy path.
		{
			"duplicate_types_deduped",
			f.medium,
			withTypes(senderFilter(f.medium), pb.TransferType_TRANSFER, pb.TransferType_TRANSFER, pb.TransferType_PREIMAGE_SWAP),
		},
		// Malformed pubkey — both paths must reject with InvalidArgument; the
		// new route must classify the parse error as InvalidArgumentMalformedKey
		// rather than returning a wrapped Internal/Unknown.
		{
			"invalid_sender_pubkey_rejected",
			f.medium,
			&pb.TransferFilter{
				Participant: &pb.TransferFilter_SenderIdentityPublicKey{
					SenderIdentityPublicKey: []byte{0xff},
				},
				Network: pb.Network_REGTEST,
				Types:   []pb.TransferType{pb.TransferType_TRANSFER},
			},
		},
		// Out-of-range type enum — both paths must reject with InvalidArgument.
		{
			"invalid_type_enum_rejected",
			f.medium,
			func() *pb.TransferFilter {
				flt := senderFilter(f.medium)
				flt.Types = []pb.TransferType{pb.TransferType(99999)}
				return flt
			}(),
		},
		// nil filter — routing must not panic; both paths fall through to legacy
		// which rejects with InvalidArgument.
		{
			"nil_filter_rejected",
			f.medium,
			nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			legacy, mimo, lerr, merr := f.runBothPathsAllTransfers(tc.viewer, tc.filter)
			assertResultsEquivalent(t, tc.name, legacy, mimo, lerr, merr)
		})
	}
}

// Self-transfer dedup: a transfer where sender == primary receiver must
// surface exactly once through SR1 + types. The UNION in
// BuildByTypesQuerySenderOrReceiver collapses the duplicate rows; without
// DISTINCT the ordered ID comparison would fail.
func TestQueryAllTransfers_Equivalence_ByTypes_SelfTransfer(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("equivalence tests require Postgres (raw SQL execution path)")
	}
	f := newEquivFixture(t)

	self := f.newPubkey()
	other := f.newPubkey()
	f.privacyEnabled(self)
	f.makeTransfer(makeTransferOpts{
		transferType:   st.TransferTypeTransfer,
		transferStatus: st.TransferStatusCompleted,
		receiverStatus: st.TransferReceiverStatusCompleted,
		sender:         self,
		receiver:       self,
		extraReceivers: []extraReceiverEquiv{
			{pubkey: other, status: st.TransferReceiverStatusCompleted},
		},
		createTime: f.baseNow.Add(-30 * time.Minute),
	})

	filter := withTypes(senderOrReceiverFilter(self), pb.TransferType_TRANSFER)
	legacy, mimo, lerr, merr := f.runBothPathsAllTransfers(self, filter)
	assertResultsEquivalent(t, "self_transfer_dedup", legacy, mimo, lerr, merr)
}

// -----------------------------------------------------------------------------
// QueryAllTransfers equivalence — legacy queryTransfers vs the specialized
// queryReceiverByTypeStatus path. Covers receiver-arm queries with both type
// and status filters: SDK getOwnedBalance receiver shape (GOB2), audit Shape
// A ([SWAP] + 5-state cross-axis), audit Shape B ([COOPERATIVE_EXIT] +
// 2-sender-pending), partial umbrellas (narrowing must fire), terminals, and
// fall-through cases that must stay on legacy.
// -----------------------------------------------------------------------------

func withStatuses(filter *pb.TransferFilter, statuses ...pb.TransferStatus) *pb.TransferFilter {
	filter.Statuses = statuses
	return filter
}

func TestQueryAllTransfers_Equivalence_ReceiverByTypeStatus(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("equivalence tests require Postgres (raw SQL uses pq.Array + ANY)")
	}
	f := newEquivFixture(t)
	f.setupEquivalenceData()

	activeCounterSwap := []pb.TransferStatus{
		pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED,
		pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED_COORDINATOR,
		pb.TransferStatus_TRANSFER_STATUS_APPLYING_SENDER_KEY_TWEAK,
		pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING,
		pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED,
		pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAK_LOCKED,
		pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAK_APPLIED,
		pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED,
		pb.TransferStatus_TRANSFER_STATUS_RECEIVER_REFUND_SIGNED,
	}
	counterSwapTypes := []pb.TransferType{pb.TransferType_COUNTER_SWAP_V3, pb.TransferType_COUNTER_SWAP}

	shapeAStatuses := []pb.TransferStatus{
		pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED,
		pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED,
		pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAK_LOCKED,
		pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAK_APPLIED,
		pb.TransferStatus_TRANSFER_STATUS_RECEIVER_REFUND_SIGNED,
	}

	cases := []struct {
		name   string
		viewer keys.Public
		filter *pb.TransferFilter
	}{
		// GOB2 shape — SDK getOwnedBalance receiver arm. Routes to new handler.
		// 2 types × 2 buckets = 4 sub-queries; narrowing predicate covers the
		// 4 sender-pending umbrella.
		{
			"GOB2_full_active_counter_swap",
			f.medium,
			withStatuses(withTypes(receiverFilter(f.medium), counterSwapTypes...), activeCounterSwap...),
		},
		{
			"GOB2_full_active_counter_swap_light",
			f.light,
			withStatuses(withTypes(receiverFilter(f.light), counterSwapTypes...), activeCounterSwap...),
		},
		// Shape A — [SWAP] + SENDER_KEY_TWEAKED + 4 RECEIVER_*. All 5 statuses
		// translate 1:1 (no narrowing needed), all land in
		// idx_transferreceiver_claim_pending_pubkey_time's partial WHERE —
		// postTweakActive bucket only.
		{
			"shapeA_swap_receiver_pending",
			f.medium,
			withStatuses(withTypes(receiverFilter(f.medium), pb.TransferType_SWAP), shapeAStatuses...),
		},
		// Shape B — [COOPERATIVE_EXIT] + 2 sender-pending. Translates to
		// INITIATED only; narrowing carries the 2 t.status values for exactness.
		// remainder bucket only.
		{
			"shapeB_coop_exit_partial_sender_pending",
			f.medium,
			withStatuses(withTypes(receiverFilter(f.medium), pb.TransferType_COOPERATIVE_EXIT),
				pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED,
				pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING),
		},
		// Single receiver-named status — drives postTweakActive bucket only, 1:1
		// translation, no narrowing.
		{
			"single_receiver_key_tweaked",
			f.medium,
			withStatuses(withTypes(receiverFilter(f.medium), pb.TransferType_TRANSFER),
				pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED),
		},
		// Terminal-only — COMPLETED translates 1:1, REMAINDER bucket via type
		// composite.
		{
			"terminal_completed_only",
			f.medium,
			withStatuses(withTypes(receiverFilter(f.medium), pb.TransferType_TRANSFER),
				pb.TransferStatus_TRANSFER_STATUS_COMPLETED),
		},
		// Mixed terminal + receiver-named — both buckets fire.
		{
			"mixed_completed_plus_receiver_tweaked",
			f.medium,
			withStatuses(withTypes(receiverFilter(f.medium), pb.TransferType_TRANSFER),
				pb.TransferStatus_TRANSFER_STATUS_COMPLETED,
				pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED),
		},
		// No matches on this wallet — empty result, both paths agree.
		{
			"receiver_no_matches",
			f.cold,
			withStatuses(withTypes(receiverFilter(f.cold), counterSwapTypes...), activeCounterSwap...),
		},
		// ORDER ASCENDING — exercises backwards walk on indexes.
		{
			"order_ascending",
			f.medium,
			withOrder(withStatuses(withTypes(receiverFilter(f.medium), pb.TransferType_TRANSFER),
				pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED), pb.Order_ASCENDING),
		},
		// Pagination.
		{
			"pagination_limit_offset",
			f.medium,
			withLimitOffset(withStatuses(withTypes(receiverFilter(f.medium), pb.TransferType_TRANSFER),
				pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED), 5, 1),
		},
		// Time filters.
		{
			"created_after",
			f.medium,
			withCreatedAfter(withStatuses(withTypes(receiverFilter(f.medium), pb.TransferType_TRANSFER),
				pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED), f.baseNow.Add(-150*time.Minute)),
		},
		{
			"created_before",
			f.medium,
			withCreatedBefore(withStatuses(withTypes(receiverFilter(f.medium), pb.TransferType_TRANSFER),
				pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED), f.baseNow.Add(-150*time.Minute)),
		},
		// Duplicate types — the per-type rewrite must dedupe.
		{
			"duplicate_types_deduped",
			f.medium,
			withStatuses(withTypes(receiverFilter(f.medium),
				pb.TransferType_COUNTER_SWAP, pb.TransferType_COUNTER_SWAP, pb.TransferType_COUNTER_SWAP_V3),
				pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED),
		},
		// Fall-through: sender participant — predicate requires receiver.
		{
			"falls_through_when_sender_participant",
			f.sender,
			withStatuses(withTypes(senderFilter(f.sender), counterSwapTypes...), activeCounterSwap...),
		},
		// Fall-through: SR1 participant.
		{
			"falls_through_when_SR1_participant",
			f.both,
			withStatuses(withTypes(senderOrReceiverFilter(f.both), counterSwapTypes...), activeCounterSwap...),
		},
		// Fall-through: no types.
		{
			"falls_through_when_no_types",
			f.medium,
			withStatuses(receiverFilter(f.medium), pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED),
		},
		// Fall-through: no statuses.
		{
			"falls_through_when_no_statuses",
			f.medium,
			withTypes(receiverFilter(f.medium), pb.TransferType_TRANSFER),
		},
		// Fall-through: transfer_ids set.
		{
			"falls_through_when_transfer_ids_set",
			f.light,
			withTransferIDs(withStatuses(withTypes(receiverFilter(f.light), pb.TransferType_TRANSFER),
				pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED), uuid.New().String()),
		},
		// Negative pagination — both paths must reject.
		{
			"negative_limit_rejected",
			f.medium,
			withLimitOffset(withStatuses(withTypes(receiverFilter(f.medium), counterSwapTypes...),
				activeCounterSwap...), -1, 0),
		},
		{
			"negative_offset_rejected",
			f.medium,
			withLimitOffset(withStatuses(withTypes(receiverFilter(f.medium), counterSwapTypes...),
				activeCounterSwap...), 50, -1),
		},
		// Out-of-range status enum — both paths must reject.
		{
			"invalid_status_enum_rejected",
			f.medium,
			withStatuses(withTypes(receiverFilter(f.medium), pb.TransferType_TRANSFER), pb.TransferStatus(99999)),
		},
		// nil filter — routing must not panic.
		{
			"nil_filter_rejected",
			f.medium,
			nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			legacy, mimo, lerr, merr := f.runBothPathsAllTransfers(tc.viewer, tc.filter)
			assertResultsEquivalent(t, tc.name, legacy, mimo, lerr, merr)
		})
	}
}

// Self-transfer: receiver-arm-only handler must marshal via
// MarshalProtoForReceiver when the wallet is also the sender (HasReceiver
// branch). Multi-receiver self-transfer is the discriminator.
func TestQueryAllTransfers_Equivalence_ReceiverByTypeStatus_SelfTransfer(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("equivalence tests require Postgres (raw SQL execution path)")
	}
	f := newEquivFixture(t)

	self := f.newPubkey()
	other := f.newPubkey()
	f.privacyEnabled(self)
	f.makeTransfer(makeTransferOpts{
		transferType:   st.TransferTypeCounterSwap,
		transferStatus: st.TransferStatusReceiverKeyTweaked,
		receiverStatus: st.TransferReceiverStatusKeyTweaked,
		sender:         self,
		receiver:       self,
		extraReceivers: []extraReceiverEquiv{
			{pubkey: other, status: st.TransferReceiverStatusKeyTweaked},
		},
		createTime: f.baseNow.Add(-30 * time.Minute),
	})

	filter := withStatuses(
		withTypes(receiverFilter(self), pb.TransferType_COUNTER_SWAP),
		pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED,
	)
	legacy, mimo, lerr, merr := f.runBothPathsAllTransfers(self, filter)
	assertResultsEquivalent(t, "self_transfer_receiver_by_type_status", legacy, mimo, lerr, merr)
}

// Locks the MIMO-forward receiver-axis semantics: a multi-receiver transfer
// where one receiver row is COMPLETED while the parent transfers.status (and
// the other receiver) lags behind must still surface for the COMPLETED
// receiver when queried with statuses=[COMPLETED]. The pure-1:1 sub-query
// filters on r.status without a t.status narrowing predicate by design —
// per-receiver state is the authoritative axis for receiver-arm queries, and
// legacy queryTransfers' t.status-only filter would silently drop this row.
// This is a documented divergence from legacy, not an equivalence concern.
func TestQueryAllTransfers_ReceiverByTypeStatus_PerReceiverCompletedDivergence(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("requires Postgres (raw SQL execution path)")
	}
	f := newEquivFixture(t)

	completedReceiver := f.newPubkey()
	pendingReceiver := f.newPubkey()
	sender := f.newPubkey()
	f.privacyEnabled(completedReceiver)

	transfer := f.makeTransfer(makeTransferOpts{
		transferType:   st.TransferTypeTransfer,
		transferStatus: st.TransferStatusReceiverRefundSigned,
		receiverStatus: st.TransferReceiverStatusCompleted,
		sender:         sender,
		receiver:       completedReceiver,
		extraReceivers: []extraReceiverEquiv{
			{pubkey: pendingReceiver, status: st.TransferReceiverStatusRefundSigned},
		},
		createTime: f.baseNow.Add(-30 * time.Minute),
	})

	filter := withStatuses(
		withTypes(receiverFilter(completedReceiver), pb.TransferType_TRANSFER),
		pb.TransferStatus_TRANSFER_STATUS_COMPLETED,
	)
	ctx := f.ctxForViewer(completedReceiver)
	resp, err := f.handler.QueryAllTransfers(ctx, filter, false)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.GetTransfers(), 1, "completed receiver should see the transfer despite parent t.status != COMPLETED")
	assert.Equal(t, transfer.ID.String(), resp.GetTransfers()[0].GetId())
}

// TestQueryAllTransfers_ReceiverByTypeStatus_PerReceiverPostTweakDivergence
// locks the receiver-axis semantics for the remaining post-tweak r.statuses
// beyond COMPLETED. The marquee COMPLETED case lives above; this table
// extends the class to KEY_TWEAKED, KEY_TWEAK_LOCKED, and REFUND_SIGNED so a
// future "fix" that re-adds a t.status narrowing predicate to the pure
// sub-query — for any of these statuses — breaks the test instead of
// silently dropping rows from prod.
//
// Each case constructs a multi-receiver transfer where the queried receiver
// is one stage ahead of the parent t.status, the other receiver still lags,
// then verifies the receiver-axis filter returns the transfer.
func TestQueryAllTransfers_ReceiverByTypeStatus_PerReceiverPostTweakDivergence(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("requires Postgres (raw SQL execution path)")
	}

	cases := []struct {
		name          string
		parentStatus  st.TransferStatus
		queriedStatus st.TransferReceiverStatus
		laggingStatus st.TransferReceiverStatus
		filterStatus  pb.TransferStatus
	}{
		{
			name:          "key_tweaked_ahead_of_sender_key_tweaked_parent",
			parentStatus:  st.TransferStatusSenderKeyTweaked,
			queriedStatus: st.TransferReceiverStatusKeyTweaked,
			laggingStatus: st.TransferReceiverStatusReceiverClaimPending,
			filterStatus:  pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED,
		},
		{
			name:          "key_tweak_locked_ahead_of_receiver_key_tweaked_parent",
			parentStatus:  st.TransferStatusReceiverKeyTweaked,
			queriedStatus: st.TransferReceiverStatusKeyTweakLocked,
			laggingStatus: st.TransferReceiverStatusKeyTweaked,
			filterStatus:  pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAK_LOCKED,
		},
		{
			name:          "refund_signed_ahead_of_receiver_key_tweak_applied_parent",
			parentStatus:  st.TransferStatusReceiverKeyTweakApplied,
			queriedStatus: st.TransferReceiverStatusRefundSigned,
			laggingStatus: st.TransferReceiverStatusKeyTweakApplied,
			filterStatus:  pb.TransferStatus_TRANSFER_STATUS_RECEIVER_REFUND_SIGNED,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newEquivFixture(t)
			advancedReceiver := f.newPubkey()
			laggingReceiver := f.newPubkey()
			sender := f.newPubkey()
			f.privacyEnabled(advancedReceiver)

			transfer := f.makeTransfer(makeTransferOpts{
				transferType:   st.TransferTypeTransfer,
				transferStatus: tc.parentStatus,
				receiverStatus: tc.queriedStatus,
				sender:         sender,
				receiver:       advancedReceiver,
				extraReceivers: []extraReceiverEquiv{
					{pubkey: laggingReceiver, status: tc.laggingStatus},
				},
				createTime: f.baseNow.Add(-30 * time.Minute),
			})

			filter := withStatuses(
				withTypes(receiverFilter(advancedReceiver), pb.TransferType_TRANSFER),
				tc.filterStatus,
			)
			ctx := f.ctxForViewer(advancedReceiver)
			resp, err := f.handler.QueryAllTransfers(ctx, filter, false)
			require.NoError(t, err)
			require.NotNil(t, resp)
			require.Len(t, resp.GetTransfers(), 1, "advanced receiver should see the transfer despite lagging parent t.status")
			assert.Equal(t, transfer.ID.String(), resp.GetTransfers()[0].GetId())
		})
	}
}

// TestQueryAllTransfers_Equivalence_CounterSwap covers the queryCounterSwap
// handler — sender-or-receiver participant + counter-swap types + a status
// filter that's a subset of ACTIVE_COUNTER_SWAP_STATUSES. The asymmetric
// design (sender column-based per-status UNION ALL; receiver edge-based
// per-type × per-bucket) must produce results equivalent to legacy
// queryTransfers across all routing-eligible shapes, AND fall through to
// legacy cleanly for shapes the routing predicate rejects.
func TestQueryAllTransfers_Equivalence_CounterSwap(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("equivalence tests require Postgres (raw SQL uses pq.Array + ANY)")
	}
	f := newEquivFixture(t)
	f.setupEquivalenceData()

	activeCounterSwap := []pb.TransferStatus{
		pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED,
		pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED_COORDINATOR,
		pb.TransferStatus_TRANSFER_STATUS_APPLYING_SENDER_KEY_TWEAK,
		pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING,
		pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED,
		pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAK_LOCKED,
		pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAK_APPLIED,
		pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED,
		pb.TransferStatus_TRANSFER_STATUS_RECEIVER_REFUND_SIGNED,
	}
	counterSwapTypes := []pb.TransferType{pb.TransferType_COUNTER_SWAP_V3, pb.TransferType_COUNTER_SWAP}

	cases := []struct {
		name   string
		viewer keys.Public
		filter *pb.TransferFilter
	}{
		// SDK shape — sender_or_receiver + 2 counter-swap types + 9 active statuses.
		// Triggers narrowing-redundancy optimization (all 4 sender-pending present).
		{
			"SDK_full_active_counter_swap_both_arms",
			f.both,
			withStatuses(withTypes(senderOrReceiverFilter(f.both), counterSwapTypes...), activeCounterSwap...),
		},
		{
			"SDK_full_active_counter_swap_medium",
			f.medium,
			withStatuses(withTypes(senderOrReceiverFilter(f.medium), counterSwapTypes...), activeCounterSwap...),
		},
		{
			"SDK_full_active_counter_swap_light",
			f.light,
			withStatuses(withTypes(senderOrReceiverFilter(f.light), counterSwapTypes...), activeCounterSwap...),
		},
		// Cold pubkey — empty result; both paths agree on zero rows.
		{
			"SDK_full_no_matches_on_cold_pubkey",
			f.cold,
			withStatuses(withTypes(senderOrReceiverFilter(f.cold), counterSwapTypes...), activeCounterSwap...),
		},
		// Single COUNTER_SWAP type — exercises only one branch of the per-type
		// UNION ALL on the receiver arm.
		{
			"single_type_counter_swap_v1",
			f.both,
			withStatuses(withTypes(senderOrReceiverFilter(f.both), pb.TransferType_COUNTER_SWAP), activeCounterSwap...),
		},
		// Partial-umbrella subset — only 1 of the 4 sender-pending statuses.
		// Narrowing-redundancy does NOT fire; the collapsing-remainder
		// sub-query KEEPS the t.status narrowing predicate for correctness.
		// Both paths must still agree.
		{
			"partial_umbrella_single_sender_pending",
			f.both,
			withStatuses(withTypes(senderOrReceiverFilter(f.both), counterSwapTypes...),
				pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED),
		},
		// Partial-umbrella with no sender-pending statuses — exercises only
		// the receiver pure-postTweakActive bucket; no collapsing class at all.
		{
			"partial_no_sender_pending",
			f.medium,
			withStatuses(withTypes(senderOrReceiverFilter(f.medium), counterSwapTypes...),
				pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED,
				pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAK_LOCKED),
		},
		// Single receiver-named status — drives pure-postTweakActive only.
		{
			"single_receiver_key_tweaked",
			f.medium,
			withStatuses(withTypes(senderOrReceiverFilter(f.medium), counterSwapTypes...),
				pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED),
		},
		// ORDER ASCENDING — exercises backwards walk on indexes via Merge Append.
		{
			"order_ascending",
			f.both,
			withOrder(withStatuses(withTypes(senderOrReceiverFilter(f.both), counterSwapTypes...),
				activeCounterSwap...), pb.Order_ASCENDING),
		},
		// Pagination.
		{
			"pagination_limit_offset",
			f.both,
			withLimitOffset(withStatuses(withTypes(senderOrReceiverFilter(f.both), counterSwapTypes...),
				activeCounterSwap...), 5, 1),
		},
		// Time filters.
		{
			"created_after",
			f.medium,
			withCreatedAfter(withStatuses(withTypes(senderOrReceiverFilter(f.medium), counterSwapTypes...),
				activeCounterSwap...), f.baseNow.Add(-150*time.Minute)),
		},
		{
			"created_before",
			f.medium,
			withCreatedBefore(withStatuses(withTypes(senderOrReceiverFilter(f.medium), counterSwapTypes...),
				activeCounterSwap...), f.baseNow.Add(-150*time.Minute)),
		},
		// Duplicate types — type dedupe inside resolveTypeStrings.
		{
			"duplicate_types_deduped",
			f.both,
			withStatuses(withTypes(senderOrReceiverFilter(f.both),
				pb.TransferType_COUNTER_SWAP, pb.TransferType_COUNTER_SWAP, pb.TransferType_COUNTER_SWAP_V3),
				activeCounterSwap...),
		},
		// Fall-through: sender-only participant — routes to legacy.
		{
			"falls_through_when_sender_only_participant",
			f.sender,
			withStatuses(withTypes(senderFilter(f.sender), counterSwapTypes...), activeCounterSwap...),
		},
		// Fall-through: receiver-only participant — the receiver-by-type-status
		// path (checked before counter-swap) claims this shape; equivalent to
		// legacy on these single-receiver fixtures.
		{
			"falls_through_when_receiver_only_participant",
			f.medium,
			withStatuses(withTypes(receiverFilter(f.medium), counterSwapTypes...), activeCounterSwap...),
		},
		// Fall-through: non-counter-swap type — no specialized path claims a
		// sender-or-receiver + non-counter-swap-type shape, so it routes to the
		// by-participant-fallback path.
		{
			"falls_through_when_type_not_counter_swap",
			f.medium,
			withStatuses(withTypes(senderOrReceiverFilter(f.medium), pb.TransferType_TRANSFER),
				activeCounterSwap...),
		},
		// Fall-through: mixed types (one counter-swap + one not) — entire
		// request rejected since not all types are counter-swap.
		{
			"falls_through_when_mixed_type_set",
			f.medium,
			withStatuses(withTypes(senderOrReceiverFilter(f.medium),
				pb.TransferType_COUNTER_SWAP, pb.TransferType_TRANSFER), activeCounterSwap...),
		},
		// Fall-through: no statuses.
		{
			"falls_through_when_no_statuses",
			f.medium,
			withTypes(senderOrReceiverFilter(f.medium), counterSwapTypes...),
		},
		// Fall-through: no types.
		{
			"falls_through_when_no_types",
			f.medium,
			withStatuses(senderOrReceiverFilter(f.medium), activeCounterSwap...),
		},
		// Fall-through: transfer_ids set.
		{
			"falls_through_when_transfer_ids_set",
			f.light,
			withTransferIDs(withStatuses(withTypes(senderOrReceiverFilter(f.light), counterSwapTypes...),
				activeCounterSwap...), uuid.New().String()),
		},
		// Negative pagination — both paths must reject.
		{
			"negative_limit_rejected",
			f.medium,
			withLimitOffset(withStatuses(withTypes(senderOrReceiverFilter(f.medium), counterSwapTypes...),
				activeCounterSwap...), -1, 0),
		},
		{
			"negative_offset_rejected",
			f.medium,
			withLimitOffset(withStatuses(withTypes(senderOrReceiverFilter(f.medium), counterSwapTypes...),
				activeCounterSwap...), 50, -1),
		},
		// Out-of-range status enum — both paths must reject.
		{
			"invalid_status_enum_rejected",
			f.medium,
			withStatuses(withTypes(senderOrReceiverFilter(f.medium), counterSwapTypes...),
				pb.TransferStatus(99999)),
		},
		// nil filter — routing must not panic.
		{
			"nil_filter_rejected",
			f.medium,
			nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			legacy, mimo, lerr, merr := f.runBothPathsAllTransfers(tc.viewer, tc.filter)
			assertResultsEquivalent(t, tc.name, legacy, mimo, lerr, merr)
		})
	}
}

// TestQueryAllTransfers_Equivalence_CounterSwap_SelfTransfer exercises the
// cross-arm UNION DISTINCT — the wallet is both sender and receiver of the
// same transfer; outer dedup must collapse the duplicate row.
func TestQueryAllTransfers_Equivalence_CounterSwap_SelfTransfer(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("equivalence tests require Postgres (raw SQL execution path)")
	}
	f := newEquivFixture(t)

	self := f.newPubkey()
	other := f.newPubkey()
	f.privacyEnabled(self)
	f.makeTransfer(makeTransferOpts{
		transferType:   st.TransferTypeCounterSwap,
		transferStatus: st.TransferStatusReceiverKeyTweaked,
		receiverStatus: st.TransferReceiverStatusKeyTweaked,
		sender:         self,
		receiver:       self,
		extraReceivers: []extraReceiverEquiv{
			{pubkey: other, status: st.TransferReceiverStatusKeyTweaked},
		},
		createTime: f.baseNow.Add(-30 * time.Minute),
	})

	activeCounterSwap := []pb.TransferStatus{
		pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED,
		pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED_COORDINATOR,
		pb.TransferStatus_TRANSFER_STATUS_APPLYING_SENDER_KEY_TWEAK,
		pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING,
		pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED,
		pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAK_LOCKED,
		pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAK_APPLIED,
		pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED,
		pb.TransferStatus_TRANSFER_STATUS_RECEIVER_REFUND_SIGNED,
	}
	filter := withStatuses(
		withTypes(senderOrReceiverFilter(self),
			pb.TransferType_COUNTER_SWAP, pb.TransferType_COUNTER_SWAP_V3),
		activeCounterSwap...,
	)
	legacy, mimo, lerr, merr := f.runBothPathsAllTransfers(self, filter)
	assertResultsEquivalent(t, "self_transfer_counter_swap", legacy, mimo, lerr, merr)
}

// TestQueryAllTransfers_Equivalence_ByParticipantFallback covers the shapes the
// fallback is expected to claim. Single-receiver fixtures only: legacy uses
// the denormalized parent columns (t.sender_identity_pubkey,
// t.receiver_identity_pubkey) while the fallback uses the receiver/sender
// edge tables — for single-receiver these agree by construction. Multi-receiver
// divergence is tested separately below.
func TestQueryAllTransfers_Equivalence_ByParticipantFallback(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("equivalence tests require Postgres (receiver-axis predicate semantics)")
	}
	f := newEquivFixture(t)
	f.setupEquivalenceData()

	cases := []struct {
		name   string
		viewer keys.Public
		filter *pb.TransferFilter
	}{
		// Bare sender — column SenderIdentityPubkeyEQ vs HasTransferSendersWith.
		// Single-receiver fixtures: edge row matches the parent column.
		{
			"bare_sender",
			f.sender,
			senderFilter(f.sender),
		},
		// Sender + statuses (non-OutgoingInFlight subset so OutgoingInFlight
		// doesn't claim). COMPLETED on sender pubkey returns 0 from the seeded
		// data but still exercises the predicate path.
		{
			"sender_plus_completed",
			f.sender,
			withStatuses(senderFilter(f.sender), pb.TransferStatus_TRANSFER_STATUS_COMPLETED),
		},
		// Bare receiver — column ReceiverIdentityPubkeyEQ vs
		// HasTransferReceiversWith. Single-receiver fixtures only.
		{
			"bare_receiver",
			f.medium,
			receiverFilter(f.medium),
		},
		// Bare SR1 — Or(receiver_col, sender_col) vs Or(HasReceivers, HasSenders).
		{
			"bare_SR1",
			f.both,
			senderOrReceiverFilter(f.both),
		},
		// SR1 + statuses on a wallet with only single-receiver entries.
		{
			"SR1_plus_receiver_key_tweaked",
			f.both,
			withStatuses(senderOrReceiverFilter(f.both),
				pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED),
		},
		// Receiver + COMPLETED on an empty-result wallet — both paths return [].
		{
			"receiver_completed_no_matches",
			f.cold,
			withStatuses(receiverFilter(f.cold), pb.TransferStatus_TRANSFER_STATUS_COMPLETED),
		},
		// Pagination.
		{
			"pagination_limit_offset",
			f.medium,
			withLimitOffset(receiverFilter(f.medium), 5, 2),
		},
		// Time filter — created_after.
		{
			"created_after",
			f.medium,
			withCreatedAfter(receiverFilter(f.medium), f.baseNow.Add(-150*time.Minute)),
		},
		// ORDER ASCENDING.
		{
			"order_ascending",
			f.medium,
			withOrder(receiverFilter(f.medium), pb.Order_ASCENDING),
		},
		// Negative pagination — both paths must reject.
		{
			"negative_limit_rejected",
			f.medium,
			withLimitOffset(receiverFilter(f.medium), -1, 0),
		},
		{
			"negative_offset_rejected",
			f.medium,
			withLimitOffset(receiverFilter(f.medium), 50, -1),
		},
		// Network unset — both paths must reject.
		{
			"network_unset_rejected",
			f.medium,
			&pb.TransferFilter{
				Participant: &pb.TransferFilter_ReceiverIdentityPublicKey{
					ReceiverIdentityPublicKey: f.medium.Serialize(),
				},
			},
		},
		// Out-of-range status enum — both paths must reject.
		{
			"invalid_status_enum_rejected",
			f.medium,
			withStatuses(receiverFilter(f.medium), pb.TransferStatus(99999)),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			legacy, mimo, lerr, merr := f.runBothPathsAllTransfers(tc.viewer, tc.filter)
			assertResultsEquivalent(t, tc.name, legacy, mimo, lerr, merr)
		})
	}
}

// TestQueryAllTransfers_ByParticipantFallback_PerReceiverDivergence locks the
// MIMO-correctness invariant: a multi-receiver transfer where one receiver
// row is COMPLETED while the parent transfers.status lags must surface for
// the COMPLETED receiver's bare-receiver-plus-statuses query. Legacy's
// t.status-only filter silently drops this row; the fallback's
// receiver-edge predicate (filtering on transfer_receivers.status) returns
// it. This is the documented divergence from legacy that motivates the
// fallback's existence — the equivalence tests above pass on single-receiver
// fixtures, but the receiver-axis is the authoritative source of truth and
// the fallback is the floor that enforces it.
func TestQueryAllTransfers_ByParticipantFallback_PerReceiverDivergence(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("requires Postgres (receiver-axis predicate semantics)")
	}
	f := newEquivFixture(t)

	completedReceiver := f.newPubkey()
	laggingReceiver := f.newPubkey()
	sender := f.newPubkey()
	f.privacyEnabled(completedReceiver)

	transfer := f.makeTransfer(makeTransferOpts{
		transferType:   st.TransferTypeTransfer,
		transferStatus: st.TransferStatusReceiverRefundSigned,
		receiverStatus: st.TransferReceiverStatusCompleted,
		sender:         sender,
		receiver:       completedReceiver,
		extraReceivers: []extraReceiverEquiv{
			{pubkey: laggingReceiver, status: st.TransferReceiverStatusRefundSigned},
		},
		createTime: f.baseNow.Add(-30 * time.Minute),
	})

	// No types filter — receiverByTypeStatus declines, fallback claims.
	filter := withStatuses(receiverFilter(completedReceiver),
		pb.TransferStatus_TRANSFER_STATUS_COMPLETED)

	ctx := f.ctxForViewer(completedReceiver)

	// Legacy queryTransfers returns 0 rows — t.status=RECEIVER_REFUND_SIGNED
	// fails the COMPLETED filter.
	legacyResp, err := f.handler.queryTransfers(ctx, filter, false, false)
	require.NoError(t, err)
	assert.Empty(t, legacyResp.GetTransfers(),
		"legacy filters on t.status only and silently drops the completed receiver's row")

	// QueryAllTransfers routes to the fallback by shape and returns the transfer
	// — r.status=COMPLETED on the completedReceiver's edge satisfies the
	// receiver-axis predicate.
	mimoResp, err := f.handler.QueryAllTransfers(ctx, filter, false)
	require.NoError(t, err)
	require.Len(t, mimoResp.GetTransfers(), 1,
		"fallback surfaces the completed receiver despite parent t.status lag")
	assert.Equal(t, transfer.ID.String(), mimoResp.GetTransfers()[0].GetId())
}

// -----------------------------------------------------------------------------
// Receiver leaf-scoping invariant — RPC-boundary lock.
// -----------------------------------------------------------------------------
//
// The participant-filtered query endpoints receiver-scope their output: a
// receiver querying a multi-receiver transfer gets back only its own leaves and
// only itself in Receivers[]. The SDK relies on this — it treats every returned
// leaf as belonging to the queried receiver. The equivalence suite above can't
// guard the invariant: its fixtures carry no leaves (makeTransfer creates
// none), so its lone leaf-set assertion is vacuous, and it only proves
// legacy==MIMO sameness — if both paths stopped scoping in lockstep it would
// stay green. These tests pin the scoping at each query RPC with a transfer that
// actually has receiver-tagged leaves, so a regression (e.g. a shared marshal
// helper that stops scoping) fails loudly even once the SDK repoints off these
// endpoints and real traffic no longer exercises them.

// receiverLeafSpec declares a receiver and the receiver-status it holds on a
// transfer built by makeMultiReceiverTransferWithLeaves.
type receiverLeafSpec struct {
	pubkey keys.Public
	status st.TransferReceiverStatus
}

// receiverLeaf records, per receiver, the TransferReceiver row and the TreeNode
// id of the single leaf tagged to it. leafNodeID equals the marshaled leaf's
// GetLeaf().GetId(), so tests assert the scoped set against it directly.
type receiverLeaf struct {
	pubkey     keys.Public
	receiverID uuid.UUID
	leafNodeID uuid.UUID
}

func leafNodeIDFor(leaves []receiverLeaf, pubkey keys.Public) string {
	for _, l := range leaves {
		if l.pubkey.Equals(pubkey) {
			return l.leafNodeID.String()
		}
	}
	return ""
}

// makeMultiReceiverTransferWithLeaves builds a transfer with one sender, N
// receivers, and one receiver-tagged leaf per receiver — the shape makeTransfer
// deliberately omits (it creates no leaves). The real TransferLeaf/TreeNode rows
// are what make the receiver-scoping projection observable through the query
// RPCs. The parent status is caller-supplied since a multi-receiver parent lags
// its receivers.
func (f *equivFixture) makeMultiReceiverTransferWithLeaves(parentStatus st.TransferStatus, sender keys.Public, specs []receiverLeafSpec) (*ent.Transfer, []receiverLeaf) {
	f.t.Helper()

	createTime := f.baseNow.Add(-2 * time.Hour)
	transfer, err := f.client.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetType(st.TransferTypeTransfer).
		SetStatus(parentStatus).
		SetExpiryTime(f.baseNow.Add(-24 * time.Hour)).
		SetTotalValue(uint64(1000 * len(specs))).
		SetSenderIdentityPubkey(sender).
		SetReceiverIdentityPubkey(specs[0].pubkey).
		SetCreateTime(createTime).
		Save(f.ctx)
	require.NoError(f.t, err)

	_, err = f.client.TransferSender.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(sender).
		SetCreateTime(createTime).
		SetTransferType(transfer.Type).
		Save(f.ctx)
	require.NoError(f.t, err)

	tree := createTestTreeForClaim(f.t, f.ctx, sender, f.client)

	out := make([]receiverLeaf, 0, len(specs))
	for _, spec := range specs {
		receiver, err := f.client.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(spec.pubkey).
			SetStatus(spec.status).
			SetCreateTime(createTime).
			SetTransferType(transfer.Type).
			Save(f.ctx)
		require.NoError(f.t, err)

		keyshare := createTestSigningKeyshare(f.t, f.ctx, f.rng, f.client)
		leafNode := createTestTreeNode(f.t, f.ctx, f.rng, f.client, tree, keyshare)
		transferLeaf := createTestTransferLeaf(f.t, f.ctx, f.client, transfer, leafNode)
		_, err = transferLeaf.Update().SetTransferReceiverID(receiver.ID).Save(f.ctx)
		require.NoError(f.t, err)

		out = append(out, receiverLeaf{pubkey: spec.pubkey, receiverID: receiver.ID, leafNodeID: leafNode.ID})
	}
	return transfer, out
}

// TestQueryAllTransfers_ScopesLeavesToQueriedReceiver locks the receiver
// projection at the exported QueryAllTransfers boundary — the endpoint contract
// the SDK depends on, independent of which internal handler the filter routes
// to. A bare receiver filter falls through to queryByParticipantFallback, which
// must return the transfer scoped to the querying receiver's leaves.
func TestQueryAllTransfers_ScopesLeavesToQueriedReceiver(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("requires Postgres (raw SQL execution path)")
	}
	f := newEquivFixture(t)
	recvA, recvB, sender := f.newPubkey(), f.newPubkey(), f.newPubkey()
	f.privacyEnabled(recvA, recvB)

	transfer, leaves := f.makeMultiReceiverTransferWithLeaves(
		st.TransferStatusSenderKeyTweaked, sender,
		[]receiverLeafSpec{
			{pubkey: recvA, status: st.TransferReceiverStatusKeyTweaked},
			{pubkey: recvB, status: st.TransferReceiverStatusKeyTweaked},
		},
	)

	for _, tc := range []struct {
		name   string
		viewer keys.Public
	}{
		{"receiver_A", recvA},
		{"receiver_B", recvB},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := f.ctxForViewer(tc.viewer)
			resp, err := f.handler.QueryAllTransfers(ctx, receiverFilter(tc.viewer), false)
			require.NoError(t, err)
			require.Len(t, resp.GetTransfers(), 1)
			got := resp.GetTransfers()[0]
			assert.Equal(t, transfer.ID.String(), got.GetId())
			assert.Equal(t, []string{leafNodeIDFor(leaves, tc.viewer)}, leafIDSetOf(got),
				"receiver must see exactly its own leaf, never the sibling's")
			require.Len(t, got.GetReceivers(), 1, "Receivers[] must be scoped to the queried receiver")
			assert.Equal(t, tc.viewer.Serialize(), got.GetReceivers()[0].GetIdentityPublicKey())
		})
	}
}

// TestQueryPendingTransfers_ScopesLeavesToQueriedReceiver locks the same
// projection at the exported QueryPendingTransfers boundary (→
// queryPendingTransfersMIMO). Receivers sit on the pending receiver-arm while
// the multi-receiver parent stays at SENDER_KEY_TWEAKED.
func TestQueryPendingTransfers_ScopesLeavesToQueriedReceiver(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("requires Postgres (raw SQL execution path)")
	}
	f := newEquivFixture(t)
	recvA, recvB, sender := f.newPubkey(), f.newPubkey(), f.newPubkey()
	f.privacyEnabled(recvA, recvB)

	transfer, leaves := f.makeMultiReceiverTransferWithLeaves(
		st.TransferStatusSenderKeyTweaked, sender,
		[]receiverLeafSpec{
			{pubkey: recvA, status: st.TransferReceiverStatusReceiverClaimPending},
			{pubkey: recvB, status: st.TransferReceiverStatusReceiverClaimPending},
		},
	)

	for _, tc := range []struct {
		name   string
		viewer keys.Public
	}{
		{"receiver_A", recvA},
		{"receiver_B", recvB},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := f.ctxForViewer(tc.viewer)
			resp, err := f.handler.QueryPendingTransfers(ctx, receiverFilter(tc.viewer))
			require.NoError(t, err)
			require.Len(t, resp.GetTransfers(), 1)
			got := resp.GetTransfers()[0]
			assert.Equal(t, transfer.ID.String(), got.GetId())
			assert.Equal(t, []string{leafNodeIDFor(leaves, tc.viewer)}, leafIDSetOf(got),
				"receiver must see exactly its own leaf, never the sibling's")
			require.Len(t, got.GetReceivers(), 1, "Receivers[] must be scoped to the queried receiver")
			assert.Equal(t, tc.viewer.Serialize(), got.GetReceivers()[0].GetIdentityPublicKey())
		})
	}
}

// TestQueryTransfers_LegacyParticipantPath_ScopesLeavesToReceiver locks the
// receiver-scoping branch of the legacy queryTransfers fallback specifically.
// Production routing now steers participant-bearing shapes to the MIMO
// handlers, so this branch loses real-traffic coverage — but it stays reachable
// until the forced-upgrade retirement, and its scoping must not rot meanwhile.
func TestQueryTransfers_LegacyParticipantPath_ScopesLeavesToReceiver(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("requires Postgres")
	}
	f := newEquivFixture(t)
	recvA, recvB, sender := f.newPubkey(), f.newPubkey(), f.newPubkey()
	f.privacyEnabled(recvA, recvB)

	_, leaves := f.makeMultiReceiverTransferWithLeaves(
		st.TransferStatusSenderKeyTweaked, sender,
		[]receiverLeafSpec{
			{pubkey: recvA, status: st.TransferReceiverStatusKeyTweaked},
			{pubkey: recvB, status: st.TransferReceiverStatusKeyTweaked},
		},
	)

	ctx := f.ctxForViewer(recvA)
	resp, err := f.handler.queryTransfers(ctx, receiverFilter(recvA), false, false)
	require.NoError(t, err)
	require.Len(t, resp.GetTransfers(), 1)
	got := resp.GetTransfers()[0]
	assert.Equal(t, []string{leafNodeIDFor(leaves, recvA)}, leafIDSetOf(got),
		"legacy queryTransfers must scope leaves to the queried receiver")
	require.Len(t, got.GetReceivers(), 1)
	assert.Equal(t, recvA.Serialize(), got.GetReceivers()[0].GetIdentityPublicKey())
}

// TestQueryTransfersByID_ReturnsAllLeavesUnscoped is the opposite lock: the
// by-id endpoint has no participant to scope to and must return the FULL
// transfer — every leaf and every receiver. This pins that the scoping in the
// participant-filtered paths is deliberate, not an accident of the shared
// marshal helper — the two contracts must be able to diverge under refactor.
func TestQueryTransfersByID_ReturnsAllLeavesUnscoped(t *testing.T) {
	if !sparktesting.PostgresTestsEnabled() {
		t.Skip("requires Postgres")
	}
	f := newEquivFixture(t)
	recvA, recvB, sender := f.newPubkey(), f.newPubkey(), f.newPubkey()
	f.privacyEnabled(recvA, recvB)

	transfer, leaves := f.makeMultiReceiverTransferWithLeaves(
		st.TransferStatusSenderKeyTweaked, sender,
		[]receiverLeafSpec{
			{pubkey: recvA, status: st.TransferReceiverStatusKeyTweaked},
			{pubkey: recvB, status: st.TransferReceiverStatusKeyTweaked},
		},
	)

	ctx := f.ctxForViewer(recvA)
	resp, err := f.handler.QueryTransfersByID(ctx, &pb.QueryTransfersByIdRequest{
		TransferIds: []string{transfer.ID.String()},
		Network:     pb.Network_REGTEST,
	})
	require.NoError(t, err)
	require.Len(t, resp.GetTransfers(), 1)
	got := resp.GetTransfers()[0]
	assert.ElementsMatch(t,
		[]string{leafNodeIDFor(leaves, recvA), leafNodeIDFor(leaves, recvB)},
		leafIDSetOf(got),
		"by-id must return ALL leaves — no receiver scoping")
	gotReceivers := make([][]byte, 0, len(got.GetReceivers()))
	for _, r := range got.GetReceivers() {
		gotReceivers = append(gotReceivers, r.GetIdentityPublicKey())
	}
	assert.ElementsMatch(t,
		[][]byte{recvA.Serialize(), recvB.Serialize()},
		gotReceivers,
		"by-id must return exactly both receivers — no receiver scoping")
}
