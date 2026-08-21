package handler

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/sighash"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/frost"
	"github.com/lightsparkdev/spark/so/helper"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// dispatchRecordingFrostClient records how aggregate() splits work between the
// per-job and batch RPCs. Both RPCs return a signature derived from the
// request message so tests can check result routing.
type dispatchRecordingFrostClient struct {
	pbfrost.FrostServiceClient

	singleRequests []*pbfrost.AggregateFrostRequest
	batchRequests  []*pbfrost.AggregateFrostBatchRequest
	dropJobID      string
	emptySigJobID  string
	batchErr       error
	singleErr      error
}

func (c *dispatchRecordingFrostClient) AggregateFrost(_ context.Context, req *pbfrost.AggregateFrostRequest, _ ...grpc.CallOption) (*pbfrost.AggregateFrostResponse, error) {
	c.singleRequests = append(c.singleRequests, req)
	if c.singleErr != nil {
		return nil, c.singleErr
	}
	if c.emptySigJobID != "" && string(req.GetMessage()) == c.emptySigJobID {
		return &pbfrost.AggregateFrostResponse{}, nil
	}
	return &pbfrost.AggregateFrostResponse{Signature: append([]byte("sig:"), req.GetMessage()...)}, nil
}

func (c *dispatchRecordingFrostClient) AggregateFrostBatch(_ context.Context, req *pbfrost.AggregateFrostBatchRequest, _ ...grpc.CallOption) (*pbfrost.AggregateFrostBatchResponse, error) {
	c.batchRequests = append(c.batchRequests, req)
	if c.batchErr != nil {
		return nil, c.batchErr
	}
	results := make(map[string]*pbfrost.AggregateFrostResponse, len(req.GetJobs()))
	for _, job := range req.GetJobs() {
		if job.GetJobId() == c.dropJobID {
			continue
		}
		if job.GetJobId() == c.emptySigJobID {
			results[job.GetJobId()] = &pbfrost.AggregateFrostResponse{}
			continue
		}
		results[job.GetJobId()] = &pbfrost.AggregateFrostResponse{Signature: append([]byte("sig:"), job.GetRequest().GetMessage()...)}
	}
	return &pbfrost.AggregateFrostBatchResponse{Results: results}, nil
}

// batchWithRequests seeds a frostAggregationBatch with pre-built requests via
// addRequest. addJob needs DB-backed key packages, and the RPC-dispatch
// contract under test here (batch dispatch, serial fallback, result mapping)
// is independent of how requests are built.
func batchWithRequests(t *testing.T, jobKeys ...string) *frostAggregationBatch {
	b := newFrostAggregationBatch(nil)
	for _, jobKey := range jobKeys {
		require.NoError(t, b.addRequest(jobKey, &pbfrost.AggregateFrostRequest{Message: []byte(jobKey)}))
	}
	return b
}

func TestFrostAggregationBatchAddRequestDuplicateKeyErrors(t *testing.T) {
	batch := batchWithRequests(t, "leaf-a/cpfp")
	err := batch.addRequest("leaf-a/cpfp", &pbfrost.AggregateFrostRequest{})
	require.ErrorContains(t, err, "duplicate aggregation job key")
	requireCodeAndReason(t, err, codes.Internal, sparkerrors.ReasonInternalDataInconsistency)
}

func TestFrostAggregationUsesSingleBatchRPC(t *testing.T) {
	client := &dispatchRecordingFrostClient{}
	batch := batchWithRequests(t, "leaf-b/cpfp", "leaf-a/cpfp", "leaf-a/direct")

	signatures, err := batch.aggregate(t.Context(), client)
	require.NoError(t, err)

	assert.Empty(t, client.singleRequests)
	require.Len(t, client.batchRequests, 1)
	jobs := client.batchRequests[0].GetJobs()
	require.Len(t, jobs, 3)
	assert.Equal(t, "leaf-a/cpfp", jobs[0].GetJobId())
	assert.Equal(t, "leaf-a/direct", jobs[1].GetJobId())
	assert.Equal(t, "leaf-b/cpfp", jobs[2].GetJobId())

	assert.Equal(t, []byte("sig:leaf-a/cpfp"), signatures["leaf-a/cpfp"])
	assert.Equal(t, []byte("sig:leaf-a/direct"), signatures["leaf-a/direct"])
	assert.Equal(t, []byte("sig:leaf-b/cpfp"), signatures["leaf-b/cpfp"])
}

// requireCodeAndReason asserts the sparkerrors wrapping survived, so a
// regression back to plain fmt.Errorf (losing the gRPC code/reason at the
// boundary) fails the test.
func requireCodeAndReason(t *testing.T, err error, expectedCode codes.Code, expectedReason string) {
	t.Helper()
	code, reason := sparkerrors.CodeAndReasonFrom(err)
	require.Equal(t, expectedCode, code)
	require.Equal(t, expectedReason, reason)
}

func TestFrostAggregationBatchMissingResultErrors(t *testing.T) {
	client := &dispatchRecordingFrostClient{dropJobID: "leaf-a/direct"}
	batch := batchWithRequests(t, "leaf-a/cpfp", "leaf-a/direct")

	_, err := batch.aggregate(t.Context(), client)
	require.ErrorContains(t, err, "leaf-a/direct")
	requireCodeAndReason(t, err, codes.Internal, sparkerrors.ReasonInternalSigningFailure)
}

// An empty signature in a batch result must fail the aggregation — storing it
// would commit a transfer with an unsigned refund variant.
func TestFrostAggregationBatchRejectsEmptySignature(t *testing.T) {
	client := &dispatchRecordingFrostClient{emptySigJobID: "leaf-a/direct"}
	batch := batchWithRequests(t, "leaf-a/cpfp", "leaf-a/direct")

	_, err := batch.aggregate(t.Context(), client)
	require.ErrorContains(t, err, "empty signature for job leaf-a/direct")
	requireCodeAndReason(t, err, codes.Internal, sparkerrors.ReasonInternalSigningFailure)
}

func TestFrostAggregationSerialRejectsEmptySignature(t *testing.T) {
	// The serial fake keys empty responses off the request message, which the
	// batchWithRequests helper sets to the job key. Unimplemented on the batch
	// RPC routes dispatch to the serial fallback under test.
	client := &dispatchRecordingFrostClient{
		batchErr:      status.Error(codes.Unimplemented, "unknown method aggregate_frost_batch"),
		emptySigJobID: "leaf-a/direct",
	}
	batch := batchWithRequests(t, "leaf-a/cpfp", "leaf-a/direct")

	_, err := batch.aggregate(t.Context(), client)
	require.ErrorContains(t, err, "empty signature for job leaf-a/direct")
	requireCodeAndReason(t, err, codes.Internal, sparkerrors.ReasonInternalSigningFailure)
}

// A signer that predates aggregate_frost_batch answers Unimplemented; the
// dispatcher must degrade to the serial path so a version-skewed rollout
// can't take the SO down.
func TestFrostAggregationBatchFallsBackToSerialOnUnimplemented(t *testing.T) {
	client := &dispatchRecordingFrostClient{batchErr: status.Error(codes.Unimplemented, "unknown method aggregate_frost_batch")}
	batch := batchWithRequests(t, "leaf-b/cpfp", "leaf-a/cpfp", "leaf-a/direct")

	signatures, err := batch.aggregate(t.Context(), client)
	require.NoError(t, err)

	require.Len(t, client.batchRequests, 1)
	require.Len(t, client.singleRequests, 3)
	// Serial fallback dispatches in sorted key order for determinism.
	assert.Equal(t, []byte("leaf-a/cpfp"), client.singleRequests[0].GetMessage())
	assert.Equal(t, []byte("leaf-a/direct"), client.singleRequests[1].GetMessage())
	assert.Equal(t, []byte("leaf-b/cpfp"), client.singleRequests[2].GetMessage())

	assert.Equal(t, []byte("sig:leaf-a/cpfp"), signatures["leaf-a/cpfp"])
	assert.Equal(t, []byte("sig:leaf-a/direct"), signatures["leaf-a/direct"])
	assert.Equal(t, []byte("sig:leaf-b/cpfp"), signatures["leaf-b/cpfp"])
}

func TestFrostAggregationBatchDoesNotFallBackOnOtherErrors(t *testing.T) {
	client := &dispatchRecordingFrostClient{batchErr: status.Error(codes.Internal, "signer exploded")}
	batch := batchWithRequests(t, "leaf-a/cpfp")

	_, err := batch.aggregate(t.Context(), client)
	require.Error(t, err)
	requireCodeAndReason(t, err, codes.Internal, sparkerrors.ReasonInternalSigningFailure)
	assert.Empty(t, client.singleRequests, "non-Unimplemented batch failures must not silently retry serially")
}

// Caller-context and transient failures must keep their gRPC codes — retry
// logic and on-call triage treat a canceled context, a restarting signer, or
// an oversized request differently from a genuine signing failure, and
// flattening them to INTERNAL would change every converted path's error
// contract.
func TestFrostAggregationPreservesCallerContextCodes(t *testing.T) {
	for _, transientCode := range []codes.Code{codes.DeadlineExceeded, codes.Canceled, codes.Unavailable, codes.ResourceExhausted} {
		batchClient := &dispatchRecordingFrostClient{batchErr: status.Error(transientCode, "transient signer failure")}
		_, err := batchWithRequests(t, "leaf-a/cpfp").aggregate(t.Context(), batchClient)
		code, reason := sparkerrors.CodeAndReasonFrom(err)
		assert.Equalf(t, transientCode, code, "batch path must preserve %s", transientCode)
		assert.Empty(t, reason)

		serialClient := &dispatchRecordingFrostClient{
			batchErr:  status.Error(codes.Unimplemented, "unknown method aggregate_frost_batch"),
			singleErr: status.Error(transientCode, "transient signer failure"),
		}
		_, err = batchWithRequests(t, "leaf-a/cpfp").aggregate(t.Context(), serialClient)
		code, reason = sparkerrors.CodeAndReasonFrom(err)
		assert.Equalf(t, transientCode, code, "serial path must preserve %s", transientCode)
		assert.Empty(t, reason)
	}

	// A malformed batch (InvalidArgument from this internal RPC) is an SO bug
	// and must surface as a signing failure, not leak the signer's code.
	client := &dispatchRecordingFrostClient{batchErr: status.Error(codes.InvalidArgument, "duplicate job id")}
	_, err := batchWithRequests(t, "leaf-a/cpfp").aggregate(t.Context(), client)
	requireCodeAndReason(t, err, codes.Internal, sparkerrors.ReasonInternalSigningFailure)
}

// The claim flow's readback must route each variant's signature into its own
// ClaimTransferLeafSignatures field, leaving absent variants empty — the
// claim counterpart of the send-transfer wiring test.
func TestBuildClaimTransferLeafSignaturesRoutesVariants(t *testing.T) {
	leafA, leafB := "leaf-a", "leaf-b"
	job := &helper.SigningJobWithPregeneratedNonce{}
	signingJobsByLeaf := map[string]*sendTransferLeafSigningJobs{
		leafA: {cpfp: job, direct: job},
		leafB: {cpfp: job, dfc: job},
	}
	signatures := map[string][]byte{
		leafAggregationJobKey(leafA, txKindCPFP):           []byte("sig-a-cpfp"),
		leafAggregationJobKey(leafA, txKindDirect):         []byte("sig-a-direct"),
		leafAggregationJobKey(leafB, txKindCPFP):           []byte("sig-b-cpfp"),
		leafAggregationJobKey(leafB, txKindDirectFromCPFP): []byte("sig-b-dfc"),
	}

	leafSignatures := buildClaimTransferLeafSignatures(signingJobsByLeaf, signatures, []string{leafA, leafB})
	require.Len(t, leafSignatures, 2)

	assert.Equal(t, leafA, leafSignatures[0].GetLeafId())
	assert.Equal(t, []byte("sig-a-cpfp"), leafSignatures[0].GetRefundSignature())
	assert.Equal(t, []byte("sig-a-direct"), leafSignatures[0].GetDirectRefundSignature())
	assert.Empty(t, leafSignatures[0].GetDirectFromCpfpRefundSignature())

	assert.Equal(t, leafB, leafSignatures[1].GetLeafId())
	assert.Equal(t, []byte("sig-b-cpfp"), leafSignatures[1].GetRefundSignature())
	assert.Empty(t, leafSignatures[1].GetDirectRefundSignature())
	assert.Equal(t, []byte("sig-b-dfc"), leafSignatures[1].GetDirectFromCpfpRefundSignature())
}

func TestFrostAggregationBatchChunksByJobCount(t *testing.T) {
	client := &dispatchRecordingFrostClient{}
	jobKeys := make([]string, 250)
	for i := range jobKeys {
		jobKeys[i] = fmt.Sprintf("leaf-%03d/cpfp", i)
	}
	batch := batchWithRequests(t, jobKeys...)

	signatures, err := batch.aggregate(t.Context(), client)
	require.NoError(t, err)

	require.Len(t, client.batchRequests, 3)
	assert.Len(t, client.batchRequests[0].GetJobs(), 100)
	assert.Len(t, client.batchRequests[1].GetJobs(), 100)
	assert.Len(t, client.batchRequests[2].GetJobs(), 50)
	require.Len(t, signatures, 250)
	for _, jobKey := range jobKeys {
		assert.Equal(t, append([]byte("sig:"), []byte(jobKey)...), signatures[jobKey])
	}
}

func TestFrostAggregationBatchChunksByMessageSize(t *testing.T) {
	client := &dispatchRecordingFrostClient{}
	batch := newFrostAggregationBatch(nil)
	// Two jobs that each nearly fill the byte budget must not share a chunk.
	for _, jobKey := range []string{"leaf-a/cpfp", "leaf-b/cpfp"} {
		batch.requests[jobKey] = &pbfrost.AggregateFrostRequest{
			Message: bytes.Repeat([]byte{0xab}, maxAggregateBatchBytes-1024),
		}
	}

	signatures, err := batch.aggregate(t.Context(), client)
	require.NoError(t, err)
	assert.Len(t, client.batchRequests, 2)
	assert.Len(t, signatures, 2)
}

func TestFrostAggregationBatchOversizedJobGetsOwnChunk(t *testing.T) {
	client := &dispatchRecordingFrostClient{}
	batch := newFrostAggregationBatch(nil)
	batch.requests["leaf-big/cpfp"] = &pbfrost.AggregateFrostRequest{
		Message: bytes.Repeat([]byte{0xab}, maxAggregateBatchBytes+1024),
	}
	batch.requests["leaf-small/cpfp"] = &pbfrost.AggregateFrostRequest{Message: []byte("small")}

	signatures, err := batch.aggregate(t.Context(), client)
	require.NoError(t, err)
	require.Len(t, client.batchRequests, 2)
	assert.Len(t, client.batchRequests[0].GetJobs(), 1)
	assert.Len(t, client.batchRequests[1].GetJobs(), 1)
	assert.Len(t, signatures, 2)
}

func TestFrostAggregationBatchEmptyMakesNoRPCs(t *testing.T) {
	client := &dispatchRecordingFrostClient{}
	batch := newFrostAggregationBatch(nil)

	signatures, err := batch.aggregate(t.Context(), client)
	require.NoError(t, err)
	assert.Empty(t, signatures)
	assert.Empty(t, client.singleRequests)
	assert.Empty(t, client.batchRequests)
}

// addJobFixture seeds a SQLite-backed keyshare so addJob's
// ent.GetKeyPackage lookup works, plus a signing job whose shares were
// "contributed" by operator op1.
func addJobFixture(t *testing.T) (context.Context, *frostAggregationBatch, *helper.SigningJobWithPregeneratedNonce, map[string]map[string][]byte, *ent.TreeNode) {
	ctx, _ := db.NewTestSQLiteContext(t)
	client, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	secret := keys.GeneratePrivateKey()
	keyshare, err := client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secret).
		SetPublicShares(map[string]keys.Public{"op1": secret.Public()}).
		SetPublicKey(secret.Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	commitment, err := frost.NewSigningCommitment(keys.GeneratePrivateKey().Public(), keys.GeneratePrivateKey().Public())
	require.NoError(t, err)
	job := &helper.SigningJobWithPregeneratedNonce{
		JobID:             uuid.New(),
		SigningKeyshareID: keyshare.ID,
		UserCommitment:    &commitment,
		Round1Packages:    map[string]frost.SigningCommitment{"op1": commitment},
	}
	allShares := map[string]map[string][]byte{
		job.JobID.String(): {"op1": []byte("share-bytes")},
	}
	leaf := &ent.TreeNode{
		VerifyingPubkey:    keys.GeneratePrivateKey().Public(),
		OwnerSigningPubkey: keys.GeneratePrivateKey().Public(),
	}
	return ctx, newFrostAggregationBatch(sparktesting.TestConfig(t)), job, allShares, leaf
}

func TestFrostAggregationBatchAddJobQueuesRequestAndSigningResult(t *testing.T) {
	ctx, batch, job, allShares, leaf := addJobFixture(t)
	batch.recordSigningResults = true

	require.NoError(t, batch.addJob(ctx, "leaf-1/cpfp", job, allShares, leaf, []byte("user-sig")))

	req := batch.requests["leaf-1/cpfp"]
	require.NotNil(t, req)
	assert.Equal(t, leaf.VerifyingPubkey.Serialize(), req.GetVerifyingKey())
	assert.Equal(t, leaf.OwnerSigningPubkey.Serialize(), req.GetUserPublicKey())
	assert.Equal(t, []byte("user-sig"), req.GetUserSignatureShare())
	assert.Equal(t, allShares[job.JobID.String()], req.GetSignatureShares())

	signingResult, err := batch.signingResult("leaf-1/cpfp")
	require.NoError(t, err)
	assert.Equal(t, job.JobID, signingResult.JobID)

	dupErr := batch.addJob(ctx, "leaf-1/cpfp", job, allShares, leaf, nil)
	require.ErrorContains(t, dupErr, "duplicate aggregation job key")
	requireCodeAndReason(t, dupErr, codes.Internal, sparkerrors.ReasonInternalDataInconsistency)
}

// The per-batch key-package cache must serve repeat keyshare lookups without
// re-reading the DB — a 1000-leaf transfer would otherwise pay ~3000 reads.
// Deleting the keyshare row between two addJob calls proves the second one is
// served from the cache.
func TestFrostAggregationBatchCachesKeyPackages(t *testing.T) {
	ctx, batch, job, allShares, leaf := addJobFixture(t)
	require.NoError(t, batch.addJob(ctx, "leaf-1/cpfp", job, allShares, leaf, nil))

	entClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	require.NoError(t, entClient.SigningKeyshare.DeleteOneID(job.SigningKeyshareID).Exec(ctx))

	secondJob := *job
	secondJob.JobID = uuid.New()
	allShares[secondJob.JobID.String()] = allShares[job.JobID.String()]
	require.NoError(t, batch.addJob(ctx, "leaf-1/direct", &secondJob, allShares, leaf, nil))
}

func TestFrostAggregationBatchAddJobMissingSharesErrors(t *testing.T) {
	ctx, batch, job, _, leaf := addJobFixture(t)

	err := batch.addJob(ctx, "leaf-1/cpfp", job, map[string]map[string][]byte{}, leaf, nil)
	require.ErrorContains(t, err, "missing signature shares")
	requireCodeAndReason(t, err, codes.Internal, sparkerrors.ReasonInternalInvalidOperatorResponse)
}

func TestFrostAggregationBatchAddJobMissingPublicShareErrors(t *testing.T) {
	ctx, batch, job, _, leaf := addJobFixture(t)
	allShares := map[string]map[string][]byte{job.JobID.String(): {"unknown-op": []byte("share")}}

	err := batch.addJob(ctx, "leaf-1/cpfp", job, allShares, leaf, nil)
	require.ErrorContains(t, err, "missing public share for operator unknown-op")
	requireCodeAndReason(t, err, codes.Internal, sparkerrors.ReasonInternalKeyshareError)
}

func TestFrostAggregationBatchSigningResultUnknownKeyErrors(t *testing.T) {
	batch := newFrostAggregationBatch(nil)
	_, err := batch.signingResult("never-added")
	require.ErrorContains(t, err, "no signing result recorded")
	requireCodeAndReason(t, err, codes.Internal, sparkerrors.ReasonInternalDataInconsistency)
}

// TestAggregateSendTransferLeafSignaturesWiresVariants pins the shared
// per-leaf glue directly: each variant's signing job and user signature must
// be wired into its own request, and each aggregated signature must land in
// its own SendTransferLeafSignatures field (absent variants stay empty). A
// swapped cpfp/direct user signature or a readback key mismatch fails here
// without needing a minikube run.
func TestAggregateSendTransferLeafSignaturesWiresVariants(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	entClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	secret := keys.GeneratePrivateKey()
	keyshare, err := entClient.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secret).
		SetPublicShares(map[string]keys.Public{"op1": secret.Public()}).
		SetPublicKey(secret.Public()).
		SetMinSigners(1).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)
	commitment, err := frost.NewSigningCommitment(keys.GeneratePrivateKey().Public(), keys.GeneratePrivateKey().Public())
	require.NoError(t, err)

	allShares := make(map[string]map[string][]byte)
	newJob := func(message byte) *helper.SigningJobWithPregeneratedNonce {
		job := &helper.SigningJobWithPregeneratedNonce{
			JobID:             uuid.New(),
			SigningKeyshareID: keyshare.ID,
			Message:           sighash.Hash{message},
			UserCommitment:    &commitment,
			Round1Packages:    map[string]frost.SigningCommitment{"op1": commitment},
		}
		allShares[job.JobID.String()] = map[string][]byte{"op1": []byte("share")}
		return job
	}
	newLeaf := func() *ent.TreeNode {
		return &ent.TreeNode{
			VerifyingPubkey:    keys.GeneratePrivateKey().Public(),
			OwnerSigningPubkey: keys.GeneratePrivateKey().Public(),
		}
	}

	leafA, leafB := uuid.NewString(), uuid.NewString()
	signingJobsByLeaf := map[string]*sendTransferLeafSigningJobs{
		leafA: {
			leaf: newLeaf(),
			cpfp: newJob(1), cpfpUserSig: []byte("A-cpfp-user-sig"),
			direct: newJob(2), directUserSig: []byte("A-direct-user-sig"),
		},
		leafB: {
			leaf: newLeaf(),
			cpfp: newJob(3), cpfpUserSig: []byte("B-cpfp-user-sig"),
			dfc: newJob(4), dfcUserSig: []byte("B-dfc-user-sig"),
		},
	}

	client := &dispatchRecordingFrostClient{}
	leafSignatures, batch, err := aggregateSendTransferLeafSignatures(ctx, sparktesting.TestConfig(t), client, signingJobsByLeaf, allShares, true)
	require.NoError(t, err)

	expectedSig := func(message byte) []byte {
		return append([]byte("sig:"), sighash.Hash{message}.Serialize()...)
	}

	// Each variant's user signature must be wired into that variant's request.
	userSigOf := func(leafID, txKind string) string {
		req := batch.requests[leafAggregationJobKey(leafID, txKind)]
		require.NotNilf(t, req, "no request queued for %s/%s", leafID, txKind)
		return string(req.GetUserSignatureShare())
	}
	assert.Equal(t, "A-cpfp-user-sig", userSigOf(leafA, txKindCPFP))
	assert.Equal(t, "A-direct-user-sig", userSigOf(leafA, txKindDirect))
	assert.Equal(t, "B-cpfp-user-sig", userSigOf(leafB, txKindCPFP))
	assert.Equal(t, "B-dfc-user-sig", userSigOf(leafB, txKindDirectFromCPFP))

	// Each aggregated signature must land in its own proto field.
	require.Len(t, leafSignatures, 2)
	byLeaf := map[string]*pbinternal.SendTransferLeafSignatures{}
	for _, sigs := range leafSignatures {
		byLeaf[sigs.GetLeafId()] = sigs
	}
	assert.Equal(t, expectedSig(1), byLeaf[leafA].GetRefundSignature())
	assert.Equal(t, expectedSig(2), byLeaf[leafA].GetDirectRefundSignature())
	assert.Empty(t, byLeaf[leafA].GetDirectFromCpfpRefundSignature())
	assert.Equal(t, expectedSig(3), byLeaf[leafB].GetRefundSignature())
	assert.Empty(t, byLeaf[leafB].GetDirectRefundSignature())
	assert.Equal(t, expectedSig(4), byLeaf[leafB].GetDirectFromCpfpRefundSignature())

	// recordSigningResults=true keeps per-job SigningResults for readback.
	signingResult, err := batch.signingResult(leafAggregationJobKey(leafA, txKindCPFP))
	require.NoError(t, err)
	assert.Equal(t, signingJobsByLeaf[leafA].cpfp.JobID, signingResult.JobID)
}

// TestAggregateSignaturesRoutesVariantSignaturesToTheRightLeaf pins the
// enqueue/readback routing of AggregateSignatures: each leaf's
// cpfp/direct/direct-from-cpfp signature must land in its own result map
// under its own leaf ID. The fake client derives each signature from the
// request's message, so a mis-keyed readback would surface as a wrong or
// missing signature.
func TestAggregateSignaturesRoutesVariantSignaturesToTheRightLeaf(t *testing.T) {
	client := &dispatchRecordingFrostClient{}

	leafA, leafB := uuid.NewString(), uuid.NewString()
	leafMap := map[string]*ent.TreeNode{
		leafA: {VerifyingPubkey: keys.GeneratePrivateKey().Public(), OwnerSigningPubkey: keys.GeneratePrivateKey().Public()},
		leafB: {VerifyingPubkey: keys.GeneratePrivateKey().Public(), OwnerSigningPubkey: keys.GeneratePrivateKey().Public()},
	}
	signingResult := func(message byte) *helper.SigningResult {
		return &helper.SigningResult{Message: sighash.Hash{message}}
	}
	pkg := &pbspark.TransferPackage{
		LeavesToSend: []*pbspark.UserSignedTxSigningJob{
			{LeafId: leafA}, {LeafId: leafB},
		},
		DirectLeavesToSend:         []*pbspark.UserSignedTxSigningJob{{LeafId: leafA}},
		DirectFromCpfpLeavesToSend: []*pbspark.UserSignedTxSigningJob{{LeafId: leafB}},
	}

	cpfpSigs, directSigs, dfcSigs, err := aggregateSignaturesWithClient(
		t.Context(), nil, client, uuid.NewString(), pkg,
		keys.Public{}, keys.Public{}, keys.Public{},
		map[string]*helper.SigningResult{leafA: signingResult(1), leafB: signingResult(2)},
		map[string]*helper.SigningResult{leafA: signingResult(3)},
		map[string]*helper.SigningResult{leafB: signingResult(4)},
		leafMap,
	)
	require.NoError(t, err)

	// One batched dispatch, 4 jobs total.
	require.Len(t, client.batchRequests, 1)
	require.Len(t, client.batchRequests[0].GetJobs(), 4)

	expectedSig := func(message byte) []byte {
		return append([]byte("sig:"), sighash.Hash{message}.Serialize()...)
	}
	require.Len(t, cpfpSigs, 2)
	assert.Equal(t, expectedSig(1), cpfpSigs[leafA])
	assert.Equal(t, expectedSig(2), cpfpSigs[leafB])
	require.Len(t, directSigs, 1)
	assert.Equal(t, expectedSig(3), directSigs[leafA])
	require.Len(t, dfcSigs, 1)
	assert.Equal(t, expectedSig(4), dfcSigs[leafB])
}

// expectedRoutedSig is the signature the dispatchRecordingFrostClient fake
// returns for a signing result whose message is sighash.Hash{message}.
func expectedRoutedSig(message byte) []byte {
	return append([]byte("sig:"), sighash.Hash{message}.Serialize()...)
}

func routedSigningResult(message byte) *helper.SigningResult {
	return &helper.SigningResult{Message: sighash.Hash{message}}
}

// TestAggregateDepositSignaturesPreservesPositionalOrder pins that deposit
// tree finalization returns root-input, refund, and direct-from-cpfp refund
// signatures in the same positional order as signingResults.
func TestAggregateDepositSignaturesPreservesPositionalOrder(t *testing.T) {
	client := &dispatchRecordingFrostClient{}
	req := &pbspark.FinalizeDepositTreeCreationRequest{
		RootTxSigningJob: &pbspark.UserSignedTxSigningJob{
			AdditionalInputs: []*pbspark.InputSigningData{{}},
		},
		RefundTxSigningJob:               &pbspark.UserSignedTxSigningJob{},
		DirectFromCpfpRefundTxSigningJob: &pbspark.UserSignedTxSigningJob{},
	}
	// Two root inputs, then refund, then direct-from-cpfp refund.
	signingResults := []*helper.SigningResult{
		routedSigningResult(1), routedSigningResult(2), routedSigningResult(3), routedSigningResult(4),
	}

	signatures, err := aggregateDepositSignaturesWithClient(
		t.Context(), nil, client, req, signingResults,
		keys.GeneratePrivateKey().Public(), keys.GeneratePrivateKey().Public(), 2,
	)
	require.NoError(t, err)

	require.Len(t, client.batchRequests, 1)
	require.Len(t, client.batchRequests[0].GetJobs(), 4)
	require.Len(t, signatures, 4)
	for i := range signingResults {
		assert.Equalf(t, expectedRoutedSig(byte(i+1)), signatures[i], "signature %d routed to wrong position", i)
	}
}

// TestAggregateRenewLeafSignaturesPairsJobsPositionally pins that leaf
// renewal returns each signing job's signature at the job's own index.
func TestAggregateRenewLeafSignaturesPairsJobsPositionally(t *testing.T) {
	client := &dispatchRecordingFrostClient{}
	leaf := &ent.TreeNode{
		VerifyingPubkey:    keys.GeneratePrivateKey().Public(),
		OwnerSigningPubkey: keys.GeneratePrivateKey().Public(),
	}
	signingJobHelpers := make([]*helper.SigningJobWithPregeneratedNonce, 3)
	userSigningJobs := make([]*pbspark.UserSignedTxSigningJob, 3)
	allShares := make(map[string]map[string][]byte, 3)
	for i := range signingJobHelpers {
		signingJobHelpers[i] = &helper.SigningJobWithPregeneratedNonce{
			JobID: uuid.New(), Message: sighash.Hash{byte(i + 1)},
		}
		userSigningJobs[i] = &pbspark.UserSignedTxSigningJob{}
		allShares[signingJobHelpers[i].JobID.String()] = map[string][]byte{"op1": []byte("share")}
	}

	signatures, err := aggregateRenewLeafSignatures(
		t.Context(), nil, client, signingJobHelpers, userSigningJobs, allShares,
		map[string][]byte{"op1": []byte("pub")}, leaf,
	)
	require.NoError(t, err)

	require.Len(t, signatures, 3)
	for i := range signingJobHelpers {
		assert.Equalf(t, expectedRoutedSig(byte(i+1)), signatures[i], "signature %d paired with wrong job", i)
	}
}

// TestAggregateClaimRefundSignaturesRoutesOptionalVariants pins that the
// legacy claim path routes each leaf's cpfp signature plus its optional
// direct / direct-from-cpfp signatures into that leaf's NodeSignatures, and
// leaves absent variants empty.
func TestAggregateClaimRefundSignaturesRoutesOptionalVariants(t *testing.T) {
	client := &dispatchRecordingFrostClient{}
	leafA, leafB := uuid.NewString(), uuid.NewString()
	leavesByID := map[string]*ent.TreeNode{
		leafA: {VerifyingPubkey: keys.GeneratePrivateKey().Public(), OwnerSigningPubkey: keys.GeneratePrivateKey().Public()},
		leafB: {VerifyingPubkey: keys.GeneratePrivateKey().Public(), OwnerSigningPubkey: keys.GeneratePrivateKey().Public()},
	}
	userJobs := map[string]*pbspark.UserSignedTxSigningJob{leafA: {}, leafB: {}}

	nodeSignatures, err := aggregateClaimRefundSignatures(
		t.Context(), nil, client,
		map[string]*helper.SigningResult{leafA: routedSigningResult(1), leafB: routedSigningResult(3)},
		map[string]*helper.SigningResult{leafA: routedSigningResult(2)},
		map[string]*helper.SigningResult{leafB: routedSigningResult(4)},
		userJobs, userJobs, userJobs,
		leavesByID,
	)
	require.NoError(t, err)
	require.Len(t, nodeSignatures, 2)

	byLeaf := make(map[string]*pbspark.NodeSignatures, 2)
	for _, nodeSig := range nodeSignatures {
		byLeaf[nodeSig.GetNodeId()] = nodeSig
	}
	require.Contains(t, byLeaf, leafA)
	require.Contains(t, byLeaf, leafB)

	assert.Equal(t, expectedRoutedSig(1), byLeaf[leafA].GetRefundTxSignature())
	assert.Equal(t, expectedRoutedSig(2), byLeaf[leafA].GetDirectRefundTxSignature())
	assert.Empty(t, byLeaf[leafA].GetDirectFromCpfpRefundTxSignature())

	assert.Equal(t, expectedRoutedSig(3), byLeaf[leafB].GetRefundTxSignature())
	assert.Empty(t, byLeaf[leafB].GetDirectRefundTxSignature())
	assert.Equal(t, expectedRoutedSig(4), byLeaf[leafB].GetDirectFromCpfpRefundTxSignature())
}
