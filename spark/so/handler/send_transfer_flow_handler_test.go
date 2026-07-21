package handler

import (
	"context"
	"io"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	transferpkg "github.com/lightsparkdev/spark/so/transfer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestSendTransferValidateDecisionAgainstPrepare directly covers the binding
// fence, including the non-canonical-UUID case: the prepare op stores the
// client's verbatim transfer_id while the decision payloads canonicalize via
// uuid.Parse(...).String(), so the comparison must be UUID-aware, not raw
// string. A raw-string compare would reject a valid non-canonical id and
// strand participants IN_FLIGHT.
func TestSendTransferValidateDecisionAgainstPrepare(t *testing.T) {
	handler := NewSendTransferFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	id := uuid.New()
	canonical := id.String()
	nonCanonical := strings.ToUpper(canonical) // same UUID, uppercase form
	require.NotEqual(t, canonical, nonCanonical)
	prepare := &pbinternal.SendTransferPrepareRequest{
		OriginalRequest: &pb.StartTransferV3Request{TransferId: nonCanonical},
	}

	// Canonical decision ids match the non-canonical prepared id (same UUID).
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.SendTransferCommitRequest{TransferId: canonical}))
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.SendTransferRollbackRequest{TransferId: canonical}))
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.SendTransferPrepareRequest{
		OriginalRequest: &pb.StartTransferV3Request{TransferId: canonical},
	}))

	// A genuinely different UUID is rejected on both decision variants (the
	// commit and rollback paths carry copy-pasted validation).
	err := handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.SendTransferCommitRequest{TransferId: uuid.NewString()})
	require.ErrorContains(t, err, "does not match")
	err = handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.SendTransferRollbackRequest{TransferId: uuid.NewString()})
	require.ErrorContains(t, err, "does not match")

	// Presumed-abort path (prepare-shape decision) must also reject a mismatch,
	// not just accept the matching echo.
	err = handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.SendTransferPrepareRequest{
		OriginalRequest: &pb.StartTransferV3Request{TransferId: uuid.NewString()},
	})
	require.ErrorContains(t, err, "does not match")

	// Wrong prepare / decision op types are rejected.
	err = handler.ValidateDecisionAgainstPrepare(&pbinternal.SendTransferCommitRequest{}, &pbinternal.SendTransferCommitRequest{TransferId: canonical})
	require.ErrorContains(t, err, "unexpected prepare op type")
	err = handler.ValidateDecisionAgainstPrepare(prepare, &pb.StartTransferV3Request{TransferId: canonical})
	require.ErrorContains(t, err, "unexpected decision op type")
}

// TestSendTransferSigningJobsThreadAdaptorKeysPerVariant proves each refund
// variant's adaptor key is mapped consistently through BOTH the participant
// signing-job builder (buildSendTransferLocalSigningJobs) and the coordinator
// aggregation-job builder (buildSendTransferAggregationJobs) — a cpfp/direct/
// dfc mix-up in either would silently break adaptor aggregation.
func TestSendTransferSigningJobsThreadAdaptorKeysPerVariant(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{21})
	ctx, leaf, cpfpParentTx := createSendTransferSigningJobTestLeaf(t, rng)
	refundScript, err := common.P2TRScriptFromPubKey(keys.MustGeneratePrivateKeyFromRand(rng).Public())
	require.NoError(t, err)

	// Give the leaf a distinct direct parent tx for the direct refund variant.
	directParentRaw := createSendTransferSigningJobTestTx(t, wire.OutPoint{Hash: [32]byte{0x42}, Index: 0}, 950, refundScript, nil)
	directParentTx, err := common.TxFromRawTxBytes(directParentRaw)
	require.NoError(t, err)
	leaf, err = leaf.Update().SetDirectTx(directParentRaw).Save(ctx)
	require.NoError(t, err)

	job := func(parentTx *wire.MsgTx) *pb.UserSignedTxSigningJob {
		raw := createSendTransferSigningJobTestTx(t, wire.OutPoint{Hash: parentTx.TxHash(), Index: 0}, 900, refundScript, nil)
		return createSendTransferUserSignedJob(t, rng, leaf.ID.String(), raw)
	}
	pkg, err := transferpkg.ParsePackage(withDummyPackageAuth(&pb.TransferPackage{
		LeavesToSend:               []*pb.UserSignedTxSigningJob{job(cpfpParentTx)},   // cpfp parent = leaf.RawTx
		DirectLeavesToSend:         []*pb.UserSignedTxSigningJob{job(directParentTx)}, // direct parent = leaf.DirectTx
		DirectFromCpfpLeavesToSend: []*pb.UserSignedTxSigningJob{job(cpfpParentTx)},   // dfc parent = leaf.RawTx
	}))
	require.NoError(t, err)

	cpfpKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	directKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	dfcKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	adaptorKeys := TransferAdaptorPublicKeys{
		cpfpAdaptorPubKey:           cpfpKey,
		directAdaptorPubKey:         directKey,
		directFromCpfpAdaptorPubKey: dfcKey,
	}
	transferID := uuid.New()
	leafID := leaf.ID.String()
	leafMap := map[string]*ent.TreeNode{leafID: leaf}

	// Coordinator aggregation jobs: each variant carries its own adaptor key.
	agg, err := buildSendTransferAggregationJobs(ctx, transferID, pkg, leafMap, adaptorKeys)
	require.NoError(t, err)
	jobs := agg[leafID]
	require.NotNil(t, jobs.cpfp)
	require.NotNil(t, jobs.direct)
	require.NotNil(t, jobs.dfc)
	require.NotNil(t, jobs.cpfp.AdaptorPublicKey)
	require.NotNil(t, jobs.direct.AdaptorPublicKey)
	require.NotNil(t, jobs.dfc.AdaptorPublicKey)
	assert.Equal(t, cpfpKey, *jobs.cpfp.AdaptorPublicKey)
	assert.Equal(t, directKey, *jobs.direct.AdaptorPublicKey)
	assert.Equal(t, dfcKey, *jobs.dfc.AdaptorPublicKey)

	// Participant local jobs: same per-variant mapping, keyed by deterministic job id.
	local, err := buildSendTransferLocalSigningJobs(ctx, transferID, pkg, leafMap, adaptorKeys)
	require.NoError(t, err)
	byJobID := make(map[string][]byte, len(local))
	for _, j := range local {
		byJobID[j.GetJobId()] = j.GetAdaptorPublicKey()
	}
	assert.Equal(t, cpfpKey.Serialize(), byJobID[sendTransferJobID(transferID, leafID, "cpfp").String()])
	assert.Equal(t, directKey.Serialize(), byJobID[sendTransferJobID(transferID, leafID, "direct").String()])
	assert.Equal(t, dfcKey.Serialize(), byJobID[sendTransferJobID(transferID, leafID, "directFromCpfp").String()])
}

// capturingFrostClient records the AggregateFrostRequest the caller builds so a
// test can assert the request fields without a real FROST server. Only
// AggregateFrost is exercised; the embedded nil interface satisfies the rest.
type capturingFrostClient struct {
	pbfrost.FrostServiceClient
	req *pbfrost.AggregateFrostRequest
}

func (c *capturingFrostClient) AggregateFrost(_ context.Context, in *pbfrost.AggregateFrostRequest, _ ...grpc.CallOption) (*pbfrost.AggregateFrostResponse, error) {
	c.req = in
	return &pbfrost.AggregateFrostResponse{Signature: []byte{0xAB}}, nil
}

// TestAggregateLeafSignatureThreadsAdaptorPublicKey asserts aggregateLeafSignature
// populates AggregateFrostRequest.AdaptorPublicKey from the job's adaptor point
// (and leaves it empty when the job carries none), localizing a regression here
// instead of it surfacing as an opaque aggregate-signature failure.
func TestAggregateLeafSignatureThreadsAdaptorPublicKey(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{12})
	ctx, leaf, parentTx := createSendTransferSigningJobTestLeaf(t, rng)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	refundScript, err := common.P2TRScriptFromPubKey(keys.MustGeneratePrivateKeyFromRand(rng).Public())
	require.NoError(t, err)
	parentOutPoint := wire.OutPoint{Hash: parentTx.TxHash(), Index: 0}
	refundRaw := createSendTransferSigningJobTestTx(t, parentOutPoint, 900, refundScript, nil)
	adaptorPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// "operator1" is the public-share key createTestSigningKeyshare seeds.
	sharesFor := func(jobID uuid.UUID) map[string]map[string][]byte {
		return map[string]map[string][]byte{jobID.String(): {"operator1": {0x01}}}
	}

	withAdaptor, err := buildSigningJobForRefund(ctx,
		parseSendRefundJob(t, createSendTransferUserSignedJob(t, rng, leaf.ID.String(), refundRaw)),
		leaf, leaf.RawTx, uuid.New(), adaptorPubKey)
	require.NoError(t, err)
	fc := &capturingFrostClient{}
	_, _, err = aggregateLeafSignature(ctx, cfg, fc, withAdaptor, sharesFor(withAdaptor.JobID), leaf, []byte{0x02})
	require.NoError(t, err)
	require.NotNil(t, fc.req)
	assert.Equal(t, adaptorPubKey.Serialize(), fc.req.GetAdaptorPublicKey())

	withoutAdaptor, err := buildSigningJobForRefund(ctx,
		parseSendRefundJob(t, createSendTransferUserSignedJob(t, rng, leaf.ID.String(), refundRaw)),
		leaf, leaf.RawTx, uuid.New(), keys.Public{})
	require.NoError(t, err)
	fc2 := &capturingFrostClient{}
	_, _, err = aggregateLeafSignature(ctx, cfg, fc2, withoutAdaptor, sharesFor(withoutAdaptor.JobID), leaf, []byte{0x02})
	require.NoError(t, err)
	require.NotNil(t, fc2.req)
	assert.Empty(t, fc2.req.GetAdaptorPublicKey())
}

// TestSendTransferJobID_Deterministic verifies that the same (transferID,
// leafID, txKind) tuple produces the same UUID across invocations — the
// load-bearing property that lets every SO derive matching job IDs without
// sending them over the wire.
func TestSendTransferJobID_Deterministic(t *testing.T) {
	transferID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	leafID := "22222222-2222-2222-2222-222222222222"

	a1 := sendTransferJobID(transferID, leafID, "cpfp")
	a2 := sendTransferJobID(transferID, leafID, "cpfp")
	assert.Equal(t, a1, a2, "same args must produce the same UUID")

	// Different txKind → different UUID.
	b := sendTransferJobID(transferID, leafID, "direct")
	assert.NotEqual(t, a1, b, "txKind must affect the UUID")

	c := sendTransferJobID(transferID, leafID, "directFromCpfp")
	assert.NotEqual(t, a1, c)
	assert.NotEqual(t, b, c)

	// Different transferID → different UUID.
	otherTransfer := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	d := sendTransferJobID(otherTransfer, leafID, "cpfp")
	assert.NotEqual(t, a1, d)

	// Different leafID → different UUID.
	e := sendTransferJobID(transferID, "44444444-4444-4444-4444-444444444444", "cpfp")
	assert.NotEqual(t, a1, e)
}

// TestSplitLeafSignatures verifies the commit-payload signature split.
func TestSplitLeafSignatures(t *testing.T) {
	in := []*pbinternal.SendTransferLeafSignatures{
		{
			LeafId:                        "leaf-a",
			RefundSignature:               []byte{0x01},
			DirectRefundSignature:         []byte{0x02},
			DirectFromCpfpRefundSignature: []byte{0x03},
		},
		{
			LeafId:          "leaf-b",
			RefundSignature: []byte{0x04},
			// no direct sigs — these maps should not contain leaf-b
		},
		{
			// All empty — entry contributes nothing.
			LeafId: "leaf-c",
		},
	}

	cpfp, direct, dfc := splitLeafSignatures(in)

	assert.Equal(t, []byte{0x01}, cpfp["leaf-a"])
	assert.Equal(t, []byte{0x04}, cpfp["leaf-b"])
	assert.NotContains(t, cpfp, "leaf-c")

	assert.Equal(t, []byte{0x02}, direct["leaf-a"])
	assert.NotContains(t, direct, "leaf-b")
	assert.NotContains(t, direct, "leaf-c")

	assert.Equal(t, []byte{0x03}, dfc["leaf-a"])
	assert.NotContains(t, dfc, "leaf-b")
	assert.NotContains(t, dfc, "leaf-c")
}

func TestSendTransferRollbackMissingTransferNoops(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	handler := NewSendTransferFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))
	transferID := uuid.NewString()

	cases := []struct {
		name string
		op   *pbinternal.SendTransferPrepareRequest
		msg  *pbinternal.SendTransferRollbackRequest
	}{
		{
			name: "rollback payload",
			msg:  &pbinternal.SendTransferRollbackRequest{TransferId: transferID},
		},
		{
			name: "prepare payload",
			op: &pbinternal.SendTransferPrepareRequest{
				OriginalRequest: &pb.StartTransferV3Request{TransferId: transferID},
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if tt.msg != nil {
				require.NoError(t, handler.Rollback(ctx, tt.msg))
			} else {
				require.NoError(t, handler.Rollback(ctx, tt.op))
			}
		})
	}
}

func TestSendTransferRollbackAdvancedStatusesNoop(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{71})
	handler := NewSendTransferFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))

	statuses := []st.TransferStatus{
		st.TransferStatusReturned,
		st.TransferStatusApplyingSenderKeyTweak,
		st.TransferStatusSenderKeyTweaked,
		st.TransferStatusReceiverKeyTweaked,
		st.TransferStatusReceiverKeyTweakLocked,
		st.TransferStatusReceiverKeyTweakApplied,
		st.TransferStatusReceiverRefundSigned,
		st.TransferStatusCompleted,
		st.TransferStatusExpired,
	}

	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			transfer := createTestTransfer(t, ctx, rng, dbCtx.Client, status)

			err := handler.Rollback(ctx, &pbinternal.SendTransferRollbackRequest{TransferId: transfer.ID.String()})
			require.NoError(t, err)

			updated, err := dbCtx.Client.Transfer.Get(ctx, transfer.ID)
			require.NoError(t, err)
			require.Equal(t, status, updated.Status)
		})
	}
}

// TestParseSendTransferRequest_Errors covers the validation guards that turn
// malformed v3 requests into typed sparkerrors before any DB work happens.
func TestParseSendTransferRequest_Errors(t *testing.T) {
	validSenderPK := keys.GeneratePrivateKey().Public().Serialize()
	validReceiverPK := keys.GeneratePrivateKey().Public().Serialize()
	validTransferID := "11111111-1111-1111-1111-111111111111"

	makeValid := func() *pb.StartTransferV3Request {
		return &pb.StartTransferV3Request{
			TransferId: validTransferID,
			ExpiryTime: timestamppb.New(time.Now().Add(time.Hour)),
			SenderPackages: []*pb.SenderTransferPackage{{
				OwnerIdentityPublicKey: validSenderPK,
				TransferPackage:        withDummyPackageAuth(&pb.TransferPackage{}),
				ReceiverIdentityPublicKeys: map[string][]byte{
					"leaf-1": validReceiverPK,
				},
			}},
		}
	}

	cases := []struct {
		name    string
		mutate  func(*pb.StartTransferV3Request)
		wantSub string
	}{
		{
			name:    "empty request",
			mutate:  func(r *pb.StartTransferV3Request) { *r = pb.StartTransferV3Request{} },
			wantSub: "expected exactly 1 sender package",
		},
		{
			name:    "zero sender packages",
			mutate:  func(r *pb.StartTransferV3Request) { r.SenderPackages = nil },
			wantSub: "expected exactly 1 sender package",
		},
		{
			name: "two sender packages",
			mutate: func(r *pb.StartTransferV3Request) {
				r.SenderPackages = append(r.SenderPackages, r.GetSenderPackages()[0])
			},
			wantSub: "expected exactly 1 sender package",
		},
		{
			name: "nil sender package",
			mutate: func(r *pb.StartTransferV3Request) {
				r.SenderPackages[0] = nil
			},
			wantSub: "sender_package is required",
		},
		{
			name: "nil transfer package",
			mutate: func(r *pb.StartTransferV3Request) {
				r.SenderPackages[0].TransferPackage = nil
			},
			wantSub: "transfer_package is required",
		},
		{
			name:    "invalid transfer id",
			mutate:  func(r *pb.StartTransferV3Request) { r.TransferId = "not-a-uuid" },
			wantSub: "invalid transfer id",
		},
		{
			name: "invalid sender pubkey",
			mutate: func(r *pb.StartTransferV3Request) {
				r.SenderPackages[0].OwnerIdentityPublicKey = []byte{0x00}
			},
			wantSub: "owner identity public key",
		},
		{
			name: "no receivers",
			mutate: func(r *pb.StartTransferV3Request) {
				r.SenderPackages[0].ReceiverIdentityPublicKeys = nil
			},
			wantSub: "at least one receiver",
		},
		{
			name: "invalid receiver pubkey",
			mutate: func(r *pb.StartTransferV3Request) {
				r.SenderPackages[0].ReceiverIdentityPublicKeys["leaf-1"] = []byte{0x00}
			},
			wantSub: "receiver public key",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := makeValid()
			tc.mutate(req)
			_, err := parseSendTransferRequest(req)
			require.ErrorContains(t, err, tc.wantSub)
		})
	}
}

// TestParseSendTransferRequest_Happy covers the path-of-success: well-formed
// request parses without error and exposes the expected fields.
func TestParseSendTransferRequest_Happy(t *testing.T) {
	validSenderPK := keys.GeneratePrivateKey().Public().Serialize()
	receiverA := keys.GeneratePrivateKey().Public().Serialize()
	receiverB := keys.GeneratePrivateKey().Public().Serialize()

	req := &pb.StartTransferV3Request{
		TransferId: "11111111-1111-1111-1111-111111111111",
		ExpiryTime: timestamppb.New(time.Now().Add(time.Hour)),
		SenderPackages: []*pb.SenderTransferPackage{{
			OwnerIdentityPublicKey: validSenderPK,
			TransferPackage:        withDummyPackageAuth(&pb.TransferPackage{}),
			ReceiverIdentityPublicKeys: map[string][]byte{
				"leaf-1": receiverA,
				"leaf-2": receiverB,
				"leaf-3": receiverA, // duplicate → deduplicated
			},
		}},
	}

	parsed, err := parseSendTransferRequest(req)
	require.NoError(t, err)
	assert.Equal(t, uuid.MustParse("11111111-1111-1111-1111-111111111111"), parsed.transferID)
	assert.Len(t, parsed.leafReceiverMap, 3, "leaf→receiver map preserves every leaf")
	assert.Len(t, parsed.receivers, 2, "duplicate receiver pubkeys collapse into the unique set")
}

func TestParseSendTransferRequest_NilRequest(t *testing.T) {
	_, err := parseSendTransferRequest(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "request is required")
}

// TestFilterJobsForThisOperator verifies the threshold-signing filter: only
// keep jobs whose round1 commitments include this SO's identifier.
func TestFilterJobsForThisOperator(t *testing.T) {
	mkJob := func(id string, opIDs ...string) *pbinternal.SigningJob {
		commitments := make(map[string]*pbcommon.SigningCommitment, len(opIDs))
		for _, oid := range opIDs {
			commitments[oid] = &pbcommon.SigningCommitment{}
		}
		return &pbinternal.SigningJob{JobId: id, Commitments: commitments}
	}

	jobs := []*pbinternal.SigningJob{
		mkJob("job-1", "op-a", "op-b"),         // op-a is in
		mkJob("job-2", "op-b", "op-c"),         // op-a is NOT in
		mkJob("job-3", "op-a", "op-c", "op-d"), // op-a is in
	}

	filtered := filterJobsForThisOperator(jobs, "op-a")
	assert.Len(t, filtered, 2)
	assert.Equal(t, "job-1", filtered[0].GetJobId())
	assert.Equal(t, "job-3", filtered[1].GetJobId())
}

func TestBuildSigningJobForRefundValidatesParentOutpoint(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{7})
	ctx, leaf, parentTx := createSendTransferSigningJobTestLeaf(t, rng)
	refundScript, err := common.P2TRScriptFromPubKey(keys.MustGeneratePrivateKeyFromRand(rng).Public())
	require.NoError(t, err)

	parentOutPoint := wire.OutPoint{Hash: parentTx.TxHash(), Index: 0}
	validRefundRaw := createSendTransferSigningJobTestTx(t, parentOutPoint, 900, refundScript, nil)
	_, err = buildSigningJobForRefund(
		ctx,
		parseSendRefundJob(t, createSendTransferUserSignedJob(t, rng, leaf.ID.String(), validRefundRaw)),
		leaf,
		leaf.RawTx,
		uuid.New(),
		keys.Public{},
	)
	require.NoError(t, err)

	wrongOutPoint := wire.OutPoint{Hash: [32]byte{0x99}, Index: 0}
	wrongOutpointRaw := createSendTransferSigningJobTestTx(t, wrongOutPoint, 900, refundScript, nil)
	_, err = buildSigningJobForRefund(
		ctx,
		parseSendRefundJob(t, createSendTransferUserSignedJob(t, rng, leaf.ID.String(), wrongOutpointRaw)),
		leaf,
		leaf.RawTx,
		uuid.New(),
		keys.Public{},
	)
	require.ErrorContains(t, err, "refund tx input 0 must spend parent tx output 0")

	// A send-transfer refund must spend exactly one input; the second input is
	// rejected by buildSigningJobForRefund (ParsePackage permits trailing
	// unsigned inputs, which only the coop-exit connector flow uses).
	extraInputRaw := createSendTransferSigningJobTestTx(t, parentOutPoint, 900, refundScript, &wrongOutPoint)
	_, err = buildSigningJobForRefund(
		ctx,
		parseSendRefundJob(t, createSendTransferUserSignedJob(t, rng, leaf.ID.String(), extraInputRaw)),
		leaf,
		leaf.RawTx,
		uuid.New(),
		keys.Public{},
	)
	require.ErrorContains(t, err, "refund tx must have exactly 1 input")
}

// TestBuildSigningJobForRefundThreadsAdaptorPublicKey verifies the adaptor
// point set on a refund signing job survives into the marshalled FrostRound2
// proto (swap v3 signs adaptor-encumbered refunds), and that the zero value
// leaves both the helper job and the proto without one. Narrow lower-level
// test: whether the round-2 job carries the adaptor point is invisible at the
// RPC boundary (a dropped point only surfaces as a FROST verification failure
// deep in signing); the swap v3 consensus integration tests cover the
// end-to-end behavior.
func TestBuildSigningJobForRefundThreadsAdaptorPublicKey(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{9})
	ctx, leaf, parentTx := createSendTransferSigningJobTestLeaf(t, rng)
	refundScript, err := common.P2TRScriptFromPubKey(keys.MustGeneratePrivateKeyFromRand(rng).Public())
	require.NoError(t, err)
	parentOutPoint := wire.OutPoint{Hash: parentTx.TxHash(), Index: 0}
	refundRaw := createSendTransferSigningJobTestTx(t, parentOutPoint, 900, refundScript, nil)
	adaptorPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	withAdaptor, err := buildSigningJobForRefund(
		ctx,
		parseSendRefundJob(t, createSendTransferUserSignedJob(t, rng, leaf.ID.String(), refundRaw)),
		leaf,
		leaf.RawTx,
		uuid.New(),
		adaptorPubKey,
	)
	require.NoError(t, err)
	require.NotNil(t, withAdaptor.AdaptorPublicKey)
	assert.Equal(t, adaptorPubKey, *withAdaptor.AdaptorPublicKey)
	marshalled, err := marshalSigningJobHelper(withAdaptor)
	require.NoError(t, err)
	assert.Equal(t, adaptorPubKey.Serialize(), marshalled.GetAdaptorPublicKey())

	withoutAdaptor, err := buildSigningJobForRefund(
		ctx,
		parseSendRefundJob(t, createSendTransferUserSignedJob(t, rng, leaf.ID.String(), refundRaw)),
		leaf,
		leaf.RawTx,
		uuid.New(),
		keys.Public{},
	)
	require.NoError(t, err)
	assert.Nil(t, withoutAdaptor.AdaptorPublicKey)
	marshalled, err = marshalSigningJobHelper(withoutAdaptor)
	require.NoError(t, err)
	assert.Empty(t, marshalled.GetAdaptorPublicKey())
}

func TestBuildSendTransferAggregationJobsValidatesAllRefundPackageOutpoints(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{8})
	ctx, leaf, cpfpParentTx := createSendTransferSigningJobTestLeaf(t, rng)
	refundScript, err := common.P2TRScriptFromPubKey(keys.MustGeneratePrivateKeyFromRand(rng).Public())
	require.NoError(t, err)

	directParentRaw := createSendTransferSigningJobTestTx(
		t,
		wire.OutPoint{Hash: [32]byte{0x42}, Index: 0},
		950,
		refundScript,
		nil,
	)
	directParentTx, err := common.TxFromRawTxBytes(directParentRaw)
	require.NoError(t, err)
	leaf, err = leaf.Update().SetDirectTx(directParentRaw).Save(ctx)
	require.NoError(t, err)

	wrongOutPoint := wire.OutPoint{Hash: [32]byte{0x77}, Index: 0}
	makeWrongJob := func() *pb.UserSignedTxSigningJob {
		rawTx := createSendTransferSigningJobTestTx(t, wrongOutPoint, 900, refundScript, nil)
		return createSendTransferUserSignedJob(t, rng, leaf.ID.String(), rawTx)
	}
	makeValidJob := func(parentTx *wire.MsgTx) *pb.UserSignedTxSigningJob {
		rawTx := createSendTransferSigningJobTestTx(
			t,
			wire.OutPoint{Hash: parentTx.TxHash(), Index: 0},
			900,
			refundScript,
			nil,
		)
		return createSendTransferUserSignedJob(t, rng, leaf.ID.String(), rawTx)
	}

	tests := []struct {
		name    string
		pkg     func() *pb.TransferPackage
		wantErr string
	}{
		{
			name: "cpfp leaves",
			pkg: func() *pb.TransferPackage {
				return withDummyPackageAuth(&pb.TransferPackage{LeavesToSend: []*pb.UserSignedTxSigningJob{makeWrongJob()}})
			},
			wantErr: "build cpfp signing job",
		},
		{
			name: "direct leaves",
			pkg: func() *pb.TransferPackage {
				return withDummyPackageAuth(&pb.TransferPackage{
					LeavesToSend:       []*pb.UserSignedTxSigningJob{makeValidJob(cpfpParentTx)},
					DirectLeavesToSend: []*pb.UserSignedTxSigningJob{makeWrongJob()},
				})
			},
			wantErr: "build direct signing job",
		},
		{
			name: "direct from cpfp leaves",
			pkg: func() *pb.TransferPackage {
				return withDummyPackageAuth(&pb.TransferPackage{
					LeavesToSend:               []*pb.UserSignedTxSigningJob{makeValidJob(cpfpParentTx)},
					DirectLeavesToSend:         []*pb.UserSignedTxSigningJob{makeValidJob(directParentTx)},
					DirectFromCpfpLeavesToSend: []*pb.UserSignedTxSigningJob{makeWrongJob()},
				})
			},
			wantErr: "build direct-from-cpfp signing job",
		},
	}

	leafMap := map[string]*ent.TreeNode{leaf.ID.String(): leaf}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pkg, err := transferpkg.ParsePackage(tt.pkg())
			require.NoError(t, err)
			_, err = buildSendTransferAggregationJobs(ctx, uuid.New(), pkg, leafMap, TransferAdaptorPublicKeys{})
			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorContains(t, err, "refund tx input 0 must spend parent tx output 0")
		})
	}
}

// withDummyPackageAuth sets the package-level key-tweak package and user
// signature to non-empty placeholders so ParsePackage accepts the package.
// These fields are unrelated to the refund-outpoint validation under test, but
// ParsePackage rejects a package that omits them.
func withDummyPackageAuth(pkg *pb.TransferPackage) *pb.TransferPackage {
	pkg.KeyTweakPackage = map[string][]byte{"op": {0x1}}
	pkg.UserSignature = []byte{0x1}
	return pkg
}

// parseSendRefundJob parses a single proto signing job into the typed refund job buildSigningJobForRefund consumes.
func parseSendRefundJob(t *testing.T, protoJob *pb.UserSignedTxSigningJob) *transferpkg.RefundSigningJob {
	t.Helper()
	jobs, err := transferpkg.ParseRefundSigningJobs([]*pb.UserSignedTxSigningJob{protoJob}, 0)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	return jobs[0]
}

func createSendTransferSigningJobTestLeaf(t *testing.T, rng io.Reader) (context.Context, *ent.TreeNode, *wire.MsgTx) {
	t.Helper()
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client

	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	signingKeyshare := createTestSigningKeyshare(t, ctx, rng, client)
	tree := createTestTreeForClaim(t, ctx, ownerIdentityPubKey, client)
	parentScript, err := common.P2TRScriptFromPubKey(ownerSigningPubKey)
	require.NoError(t, err)

	parentTx := wire.NewMsgTx(wire.TxVersion)
	parentTx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: [32]byte{0x41}, Index: 0}, nil, nil))
	parentTx.AddTxOut(wire.NewTxOut(1000, parentScript))
	parentTxRaw, err := common.SerializeTx(parentTx)
	require.NoError(t, err)

	leaf, err := client.TreeNode.Create().
		SetStatus(st.TreeNodeStatusTransferLocked).
		SetTree(tree).
		SetNetwork(btcnetwork.Regtest).
		SetSigningKeyshare(signingKeyshare).
		SetValue(1000).
		SetVerifyingPubkey(signingKeyshare.PublicKey.Add(ownerSigningPubKey)).
		SetOwnerIdentityPubkey(ownerIdentityPubKey).
		SetOwnerSigningPubkey(ownerSigningPubKey).
		SetRawTx(parentTxRaw).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)
	return ctx, leaf, parentTx
}

func createSendTransferSigningJobTestTx(t *testing.T, prevOut wire.OutPoint, value int64, pkScript []byte, extraPrevOut *wire.OutPoint) []byte {
	t.Helper()
	tx := wire.NewMsgTx(wire.TxVersion)
	tx.AddTxIn(wire.NewTxIn(&prevOut, nil, nil))
	if extraPrevOut != nil {
		tx.AddTxIn(wire.NewTxIn(extraPrevOut, nil, nil))
	}
	tx.AddTxOut(wire.NewTxOut(value, pkScript))
	raw, err := common.SerializeTx(tx)
	require.NoError(t, err)
	return raw
}

func createSendTransferUserSignedJob(t *testing.T, rng io.Reader, leafID string, rawTx []byte) *pb.UserSignedTxSigningJob {
	t.Helper()
	return &pb.UserSignedTxSigningJob{
		LeafId:                 leafID,
		SigningPublicKey:       keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
		RawTx:                  rawTx,
		SigningNonceCommitment: createTestSigningCommitment(rng),
		SigningCommitments:     &pb.SigningCommitments{SigningCommitments: map[string]*pbcommon.SigningCommitment{"operator": createTestSigningCommitment(rng)}},
		UserSignature:          []byte{0x01},
	}
}
