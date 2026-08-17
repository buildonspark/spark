package handler

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/collections"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	// maxAggregateBatchJobs mirrors MAX_AGGREGATE_BATCH_JOBS enforced by the
	// signer's aggregate_frost_batch RPC; larger batches are rejected with
	// INVALID_ARGUMENT.
	maxAggregateBatchJobs = 100
	// maxAggregateBatchBytes bounds the cumulative encoded size of one batch
	// request, keeping it comfortably under the signer's default 4 MiB gRPC
	// decode limit. A transfer at the 1000-leaf cap can carry ~3000 jobs whose
	// commitment/share maps would otherwise overflow a single message.
	maxAggregateBatchBytes = 3 << 20
)

// txKind* name the refund tx variants in aggregation job keys and FROST
// job-ID derivation. Shared across flows so enqueue and readback can't drift.
const (
	txKindCPFP           = "cpfp"
	txKindDirect         = "direct"
	txKindDirectFromCPFP = "directFromCpfp"
)

// leafAggregationJobKey identifies one refund-variant aggregation job within a
// frostAggregationBatch. txKind is one of the txKind* constants.
func leafAggregationJobKey(leafID string, txKind string) string {
	return leafID + "/" + txKind
}

// frostAggregationBatch accumulates AggregateFrost requests so a flow can
// dispatch them all at once instead of paying one RPC round trip per job (up
// to 3 refund variants per leaf).
type frostAggregationBatch struct {
	config   *so.Config
	requests map[string]*pbfrost.AggregateFrostRequest
	// recordSigningResults makes addJob keep a per-job helper.SigningResult
	// for readback. Off by default — only the send-transfer flow publishes
	// them, and building one per job (with its owner-identifier sort) is
	// wasted work on the other flows' commit paths.
	recordSigningResults bool
	signingResults       map[string]*helper.SigningResult
	// keyPackages memoizes ent.GetKeyPackage per keyshare within this batch.
	// A transfer's refund variants share one keyshare per leaf, and a
	// 1000-leaf transfer would otherwise pay ~3000 keyshare/secret reads
	// before dispatching.
	keyPackages map[uuid.UUID]*pbfrost.KeyPackage
}

func newFrostAggregationBatch(config *so.Config) *frostAggregationBatch {
	return &frostAggregationBatch{
		config:         config,
		requests:       make(map[string]*pbfrost.AggregateFrostRequest),
		signingResults: make(map[string]*helper.SigningResult),
		keyPackages:    make(map[uuid.UUID]*pbfrost.KeyPackage),
	}
}

// prefetchKeyPackages loads the key packages for keyshareIDs into the
// per-batch cache with one query and one batched secret hydration, instead of
// one sequential read per distinct keyshare during addJob.
func (b *frostAggregationBatch) prefetchKeyPackages(ctx context.Context, keyshareIDs []uuid.UUID) error {
	if len(keyshareIDs) == 0 {
		return nil
	}
	keyPackages, err := ent.GetKeyPackages(ctx, b.config, keyshareIDs)
	if err != nil {
		err = fmt.Errorf("unable to get key packages: %w", err)
		if !sparkerrors.HasReason(err) {
			err = sparkerrors.InternalDatabaseReadError(err)
		}
		return err
	}
	maps.Copy(b.keyPackages, keyPackages)
	return nil
}

// keyPackage returns the key package for keyshareID, reading through the
// per-batch cache.
func (b *frostAggregationBatch) keyPackage(ctx context.Context, keyshareID uuid.UUID) (*pbfrost.KeyPackage, error) {
	if keyPackage, ok := b.keyPackages[keyshareID]; ok {
		return keyPackage, nil
	}
	keyPackage, err := ent.GetKeyPackage(ctx, b.config, keyshareID)
	if err != nil {
		err = fmt.Errorf("unable to get key package: %w", err)
		if !sparkerrors.HasReason(err) {
			err = sparkerrors.InternalDatabaseReadError(err)
		}
		return nil, err
	}
	b.keyPackages[keyshareID] = keyPackage
	return keyPackage, nil
}

// prepareJob runs the per-job work shared by addJob and addMpcJob: the
// duplicate-key check, the key package read, and the share collection.
func (b *frostAggregationBatch) prepareJob(
	ctx context.Context,
	jobKey string,
	job *helper.SigningJobWithPregeneratedNonce,
	allShares map[string]map[string][]byte,
) (*pbfrost.KeyPackage, map[string][]byte, map[string][]byte, error) {
	if _, ok := b.requests[jobKey]; ok {
		return nil, nil, nil, sparkerrors.InternalDataInconsistency(fmt.Errorf("duplicate aggregation job key %s", jobKey))
	}
	keyPackage, err := b.keyPackage(ctx, job.SigningKeyshareID)
	if err != nil {
		return nil, nil, nil, err
	}
	shares, ok := allShares[job.JobID.String()]
	if !ok {
		return nil, nil, nil, sparkerrors.InternalInvalidOperatorResponse(fmt.Errorf("missing signature shares for job %s", job.JobID))
	}
	// Public shares filtered to the t-of-n that actually contributed shares
	// for this job (different leaves can carry different round1 commitment
	// sets). NOTE: legacy signing_coordinator.go filters by the union of
	// round1 keys; for single-sender v3 today the two reduce to the same
	// set because round1 already arrives t-of-n. If a future flow ships
	// n-of-n round1 commitments with only t-of-n contributing shares, this
	// SigningResult.PublicKeys would be narrower than the legacy path's —
	// a wire-contract divergence between the two aggregators. AggregateFrost
	// itself only requires t public shares matching t signature shares, so
	// the FROST math is correct either way.
	publicShares := make(map[string][]byte, len(shares))
	for id := range shares {
		share, ok := keyPackage.GetPublicShares()[id]
		if !ok {
			return nil, nil, nil, sparkerrors.InternalKeyshareError(fmt.Errorf("missing public share for operator %s", id))
		}
		publicShares[id] = share
	}
	return keyPackage, shares, publicShares, nil
}

// recordSigningResult records a SigningResult mirroring
// helper.GetSignaturesWithPregeneratedNonce's output shape for callers that
// need it (the send-transfer flow publishes it in its response protos).
func (b *frostAggregationBatch) recordSigningResult(
	jobKey string,
	job *helper.SigningJobWithPregeneratedNonce,
	keyPackage *pbfrost.KeyPackage,
	shares map[string][]byte,
	publicShares map[string][]byte,
) {
	if !b.recordSigningResults {
		return
	}
	// KeyshareOwnerIdentifiers lists every owner of the keyshare (not
	// just this job's t-of-n contributors) — matches
	// signing_coordinator.go. Sorted for deterministic response bytes.
	keyshareOwnerIdentifiers := slices.Collect(maps.Keys(keyPackage.GetPublicShares()))
	slices.Sort(keyshareOwnerIdentifiers)
	b.signingResults[jobKey] = &helper.SigningResult{
		JobID:                    job.JobID,
		Message:                  job.Message,
		SignatureShares:          shares,
		SigningCommitments:       job.Round1Packages,
		PublicKeys:               publicShares,
		KeyshareOwnerIdentifiers: keyshareOwnerIdentifiers,
		KeyshareThreshold:        keyPackage.GetMinSigners(),
	}
}

// addJob builds the AggregateFrostRequest for one signing job and queues it
// under jobKey.
func (b *frostAggregationBatch) addJob(
	ctx context.Context,
	jobKey string,
	job *helper.SigningJobWithPregeneratedNonce,
	allShares map[string]map[string][]byte,
	leaf *ent.TreeNode,
	userSignatureShare []byte,
) error {
	keyPackage, shares, publicShares, err := b.prepareJob(ctx, jobKey, job, allShares)
	if err != nil {
		return err
	}

	// A job carrying an adaptor point (set by buildSigningJobForRefund) makes
	// FROST produce an adaptor-embedded signature — reading it off the job
	// keeps round-2 signing and aggregation structurally incapable of
	// disagreeing about the point.
	var adaptorPublicKeyBytes []byte
	if job.AdaptorPublicKey != nil && !job.AdaptorPublicKey.IsZero() {
		adaptorPublicKeyBytes = job.AdaptorPublicKey.Serialize()
	}
	roundCommitments := collections.ConvertObjectMapToProtoMap(job.Round1Packages)
	b.requests[jobKey] = &pbfrost.AggregateFrostRequest{
		Message:            job.Message.Serialize(),
		SignatureShares:    shares,
		PublicShares:       publicShares,
		VerifyingKey:       leaf.VerifyingPubkey.Serialize(),
		Commitments:        roundCommitments,
		UserCommitments:    job.UserCommitment.MarshalProto(),
		UserPublicKey:      leaf.OwnerSigningPubkey.Serialize(),
		UserSignatureShare: userSignatureShare,
		AdaptorPublicKey:   adaptorPublicKeyBytes,
	}

	b.recordSigningResult(jobKey, job, keyPackage, shares, publicShares)
	return nil
}

// addMpcJob is addJob's multi-sub-user form (SIGNING_SCHEME_MPC_USER_GROUP):
// the user side is the sub-user contribution list, the single-user fields
// stay absent, and adaptor signatures are not supported. Sub-user verifying
// shares are not on the wire, so the signer detects a bad sub-user
// contribution only when the aggregate fails to verify, without per-position
// blame.
func (b *frostAggregationBatch) addMpcJob(
	ctx context.Context,
	jobKey string,
	job *helper.SigningJobWithPregeneratedNonce,
	allShares map[string]map[string][]byte,
	verifyingKey keys.Public,
	subuserShares []*pbfrost.SubUserSignatureShare,
) error {
	if job.SigningScheme != pbfrost.SigningScheme_SIGNING_SCHEME_MPC_USER_GROUP {
		return sparkerrors.InternalDataInconsistency(fmt.Errorf("job %s is not an MPC signing job", jobKey))
	}
	if job.AdaptorPublicKey != nil && !job.AdaptorPublicKey.IsZero() {
		return sparkerrors.InternalDataInconsistency(fmt.Errorf("adaptor signatures are not supported for MPC job %s", jobKey))
	}
	keyPackage, shares, publicShares, err := b.prepareJob(ctx, jobKey, job, allShares)
	if err != nil {
		return err
	}

	b.requests[jobKey] = &pbfrost.AggregateFrostRequest{
		Message:         job.Message.Serialize(),
		SignatureShares: shares,
		PublicShares:    publicShares,
		VerifyingKey:    verifyingKey.Serialize(),
		Commitments:     collections.ConvertObjectMapToProtoMap(job.Round1Packages),
		SigningScheme:   pbfrost.SigningScheme_SIGNING_SCHEME_MPC_USER_GROUP,
		SubuserShares:   subuserShares,
	}

	b.recordSigningResult(jobKey, job, keyPackage, shares, publicShares)
	return nil
}

// addRequest queues a pre-built AggregateFrostRequest under jobKey. Most
// legacy paths should prefer addSigningResultJob; this raw entry point exists
// for requests whose fields don't come from a SigningResult.
func (b *frostAggregationBatch) addRequest(jobKey string, req *pbfrost.AggregateFrostRequest) error {
	if _, ok := b.requests[jobKey]; ok {
		return sparkerrors.InternalDataInconsistency(fmt.Errorf("duplicate aggregation job key %s", jobKey))
	}
	b.requests[jobKey] = req
	return nil
}

// userSignedSigningJob is the shape shared by pb.UserSignedTxSigningJob and
// pb.InputSigningData that aggregation requests read the user's round-1/round-2
// contributions from.
type userSignedSigningJob interface {
	GetSigningCommitments() *pb.SigningCommitments
	GetSigningNonceCommitment() *pbcommon.SigningCommitment
	GetUserSignature() []byte
}

// addSigningResultJob queues a request built from a completed SO signing
// round (SigningResult) plus the user's signed job. The single builder keeps
// the request field construction identical across the legacy transfer, claim,
// and deposit paths — a drifted copy would aggregate against wrong or missing
// fields. Pass keys.Public{} for adaptorPublicKey when the flow doesn't use
// adaptor signatures (it serializes to nil).
func (b *frostAggregationBatch) addSigningResultJob(
	jobKey string,
	signingResult *helper.SigningResult,
	userJob userSignedSigningJob,
	verifyingKey keys.Public,
	userPublicKey keys.Public,
	adaptorPublicKey keys.Public,
) error {
	return b.addRequest(jobKey, &pbfrost.AggregateFrostRequest{
		Message:            signingResult.Message.Serialize(),
		SignatureShares:    signingResult.SignatureShares,
		PublicShares:       signingResult.PublicKeys,
		VerifyingKey:       verifyingKey.Serialize(),
		Commitments:        userJob.GetSigningCommitments().GetSigningCommitments(),
		UserCommitments:    userJob.GetSigningNonceCommitment(),
		UserPublicKey:      userPublicKey.Serialize(),
		UserSignatureShare: userJob.GetUserSignature(),
		AdaptorPublicKey:   adaptorPublicKey.Serialize(),
	})
}

// aggregate dispatches every queued job and returns the aggregated signatures
// keyed by the jobKey passed to addJob or addRequest. It issues
// aggregate_frost_batch RPCs (the signer fans each batch's jobs out across
// cores), chunked to respect the signer's per-request job and message-size
// limits. A signer that does not serve the batch RPC degrades to one
// aggregate_frost call per job in deterministic key order.
func (b *frostAggregationBatch) aggregate(ctx context.Context, frostClient pbfrost.FrostServiceClient) (map[string][]byte, error) {
	signatures := make(map[string][]byte, len(b.requests))
	if len(b.requests) == 0 {
		return signatures, nil
	}
	jobKeys := slices.Sorted(maps.Keys(b.requests))

	err := b.aggregateBatched(ctx, frostClient, jobKeys, signatures)
	switch {
	case err == nil:
		return signatures, nil
	case errors.Is(err, errAggregateBatchUnimplemented):
		// A signer that predates the aggregate_frost_batch RPC answers
		// Unimplemented. Degrade to the serial path instead of failing every
		// signing flow until the signer is upgraded.
		logging.GetLoggerFromContext(ctx).Sugar().Warnf("frost signer does not implement aggregate_frost_batch; falling back to per-job aggregation")
		clear(signatures)
	default:
		return nil, err
	}

	for _, jobKey := range jobKeys {
		resp, err := frostClient.AggregateFrost(ctx, b.requests[jobKey])
		if err != nil {
			return nil, wrapSignerRPCError(fmt.Sprintf("unable to aggregate frost signature for job %s", jobKey), err)
		}
		if len(resp.GetSignature()) == 0 {
			return nil, sparkerrors.InternalSigningError(fmt.Errorf("frost aggregation returned an empty signature for job %s", jobKey))
		}
		signatures[jobKey] = resp.GetSignature()
	}
	return signatures, nil
}

// errAggregateBatchUnimplemented marks a signer that does not serve
// aggregate_frost_batch, telling aggregate() to fall back to per-job calls.
var errAggregateBatchUnimplemented = errors.New("frost signer does not implement aggregate_frost_batch")

// wrapSignerRPCError classifies a signer RPC failure. Caller-context and
// transient codes (deadline exceeded, canceled, signer restarting, oversized
// request) keep their gRPC status so retry logic and on-call triage can tell
// them apart; everything else — including InvalidArgument, which for this
// internal RPC means the SO built a malformed batch — is a signing failure
// and surfaces as INTERNAL/SIGNING_FAILURE.
func wrapSignerRPCError(msg string, err error) error {
	wrapped := fmt.Errorf("%s: %w", msg, err)
	switch status.Code(err) {
	case codes.DeadlineExceeded, codes.Canceled, codes.Unavailable, codes.ResourceExhausted:
		return wrapped
	default:
		return sparkerrors.InternalSigningError(wrapped)
	}
}

// aggregateBatched dispatches the jobs as chunked aggregate_frost_batch RPCs,
// filling signatures keyed by job key.
func (b *frostAggregationBatch) aggregateBatched(ctx context.Context, frostClient pbfrost.FrostServiceClient, jobKeys []string, signatures map[string][]byte) error {
	for _, chunk := range b.buildChunks(jobKeys) {
		resp, err := frostClient.AggregateFrostBatch(ctx, &pbfrost.AggregateFrostBatchRequest{Jobs: chunk})
		if err != nil {
			if status.Code(err) == codes.Unimplemented {
				return errAggregateBatchUnimplemented
			}
			return wrapSignerRPCError("unable to batch aggregate frost signatures", err)
		}
		// Results correlate to jobs by the job-key string alone. That is
		// not a cryptographic binding, but it doesn't need to be: the
		// signer is a same-pod component that holds this SO's key shares,
		// so a signer that misattributed results could equally forge them
		// outright — it is inside the trust boundary. FROST aggregation
		// also verifies each signature against its own request's message
		// and verifying key before returning it, so a mis-keyed result
		// would be an invalid signature for the tx it gets attached to,
		// not a silently spendable one.
		for _, job := range chunk {
			result, ok := resp.GetResults()[job.GetJobId()]
			if !ok {
				return sparkerrors.InternalSigningError(fmt.Errorf("frost batch aggregation response is missing job %s", job.GetJobId()))
			}
			// An empty signature must not flow into leaf signature protos and
			// commit a transfer with an unsigned refund variant.
			if len(result.GetSignature()) == 0 {
				return sparkerrors.InternalSigningError(fmt.Errorf("frost batch aggregation returned an empty signature for job %s", job.GetJobId()))
			}
			signatures[job.GetJobId()] = result.GetSignature()
		}
	}
	return nil
}

// buildChunks splits the queued jobs (in sorted-key order) into batch requests
// that stay within both the signer's job-count limit and its gRPC decode
// limit. A single job larger than the byte budget still gets its own chunk —
// it would have been one oversized unary request on the fallback path too.
func (b *frostAggregationBatch) buildChunks(jobKeys []string) [][]*pbfrost.AggregateFrostJob {
	var chunks [][]*pbfrost.AggregateFrostJob
	var chunk []*pbfrost.AggregateFrostJob
	chunkBytes := 0
	for _, jobKey := range jobKeys {
		job := &pbfrost.AggregateFrostJob{JobId: jobKey, Request: b.requests[jobKey]}
		jobBytes := proto.Size(job)
		if len(chunk) > 0 && (len(chunk) == maxAggregateBatchJobs || chunkBytes+jobBytes > maxAggregateBatchBytes) {
			chunks = append(chunks, chunk)
			chunk = nil
			chunkBytes = 0
		}
		chunk = append(chunk, job)
		chunkBytes += jobBytes
	}
	return append(chunks, chunk)
}

// signingResult returns the SigningResult recorded by addJob for jobKey.
// Errors when no such job was added — readback keys must mirror the addJob
// calls, and a silently-nil result would flow into response protos and panic
// on a nil MarshalProto receiver much later.
func (b *frostAggregationBatch) signingResult(jobKey string) (*helper.SigningResult, error) {
	result, ok := b.signingResults[jobKey]
	if !ok || result == nil {
		return nil, sparkerrors.InternalDataInconsistency(fmt.Errorf("no signing result recorded for aggregation job %s", jobKey))
	}
	return result, nil
}

// aggregateLeafRefundSignatures enqueues every leaf's refund tx variants
// (cpfp/direct/direct-from-cpfp) into one batch, dispatches it, and returns
// the signatures keyed by leafAggregationJobKey plus the sorted leaf IDs.
// Shared by every coordinator flow that aggregates per-leaf refund variants;
// keeping the enqueue/readback key scheme in one place means a change to it
// cannot silently diverge between flows. recordSigningResults makes the batch
// keep per-job SigningResults for readback via signingResult — only pass true
// when the caller publishes them (the send-transfer flow's response protos).
func aggregateLeafRefundSignatures(
	ctx context.Context,
	config *so.Config,
	frostClient pbfrost.FrostServiceClient,
	signingJobsByLeaf map[string]*sendTransferLeafSigningJobs,
	allShares map[string]map[string][]byte,
	recordSigningResults bool,
) (map[string][]byte, []string, *frostAggregationBatch, error) {
	// Sort leaves for deterministic iteration (helps tests + log reproducibility).
	leafIDs := slices.Sorted(maps.Keys(signingJobsByLeaf))

	batch := newFrostAggregationBatch(config)
	batch.recordSigningResults = recordSigningResults

	// One query + one batched secret hydration for every leaf's keyshare,
	// instead of a sequential read per leaf inside the addJob loop.
	keyshareIDSet := make(map[uuid.UUID]struct{}, len(signingJobsByLeaf))
	keyshareIDs := make([]uuid.UUID, 0, len(signingJobsByLeaf))
	for _, jobs := range signingJobsByLeaf {
		for _, job := range []*helper.SigningJobWithPregeneratedNonce{jobs.cpfp, jobs.direct, jobs.dfc} {
			if job == nil {
				continue
			}
			if _, ok := keyshareIDSet[job.SigningKeyshareID]; !ok {
				keyshareIDSet[job.SigningKeyshareID] = struct{}{}
				keyshareIDs = append(keyshareIDs, job.SigningKeyshareID)
			}
		}
	}
	if err := batch.prefetchKeyPackages(ctx, keyshareIDs); err != nil {
		return nil, nil, nil, err
	}

	for _, leafID := range leafIDs {
		jobs := signingJobsByLeaf[leafID]
		if jobs.cpfp != nil {
			if err := batch.addJob(ctx, leafAggregationJobKey(leafID, txKindCPFP), jobs.cpfp, allShares, jobs.leaf, jobs.cpfpUserSig); err != nil {
				return nil, nil, nil, fmt.Errorf("build cpfp aggregation job for leaf %s: %w", leafID, err)
			}
		}
		if jobs.direct != nil {
			if err := batch.addJob(ctx, leafAggregationJobKey(leafID, txKindDirect), jobs.direct, allShares, jobs.leaf, jobs.directUserSig); err != nil {
				return nil, nil, nil, fmt.Errorf("build direct aggregation job for leaf %s: %w", leafID, err)
			}
		}
		if jobs.dfc != nil {
			if err := batch.addJob(ctx, leafAggregationJobKey(leafID, txKindDirectFromCPFP), jobs.dfc, allShares, jobs.leaf, jobs.dfcUserSig); err != nil {
				return nil, nil, nil, fmt.Errorf("build direct-from-cpfp aggregation job for leaf %s: %w", leafID, err)
			}
		}
	}

	signatures, err := batch.aggregate(ctx, frostClient)
	if err != nil {
		return nil, nil, nil, err
	}
	return signatures, leafIDs, batch, nil
}

// buildClaimTransferLeafSignatures maps aggregated signatures back onto each
// leaf's ClaimTransferLeafSignatures proto by the shared job-key scheme, in
// leafIDs order — the claim flow's counterpart of the readback inside
// aggregateSendTransferLeafSignatures.
func buildClaimTransferLeafSignatures(
	signingJobsByLeaf map[string]*sendTransferLeafSigningJobs,
	signatures map[string][]byte,
	leafIDs []string,
) []*pbinternal.ClaimTransferLeafSignatures {
	leafSignatures := make([]*pbinternal.ClaimTransferLeafSignatures, 0, len(leafIDs))
	for _, leafID := range leafIDs {
		jobs := signingJobsByLeaf[leafID]
		sigs := &pbinternal.ClaimTransferLeafSignatures{LeafId: leafID}
		if jobs.cpfp != nil {
			sigs.RefundSignature = signatures[leafAggregationJobKey(leafID, txKindCPFP)]
		}
		if jobs.direct != nil {
			sigs.DirectRefundSignature = signatures[leafAggregationJobKey(leafID, txKindDirect)]
		}
		if jobs.dfc != nil {
			sigs.DirectFromCpfpRefundSignature = signatures[leafAggregationJobKey(leafID, txKindDirectFromCPFP)]
		}
		leafSignatures = append(leafSignatures, sigs)
	}
	return leafSignatures
}

// aggregateSendTransferLeafSignatures aggregates every leaf's refund-variant
// FROST shares in one batch and returns the per-leaf signature protos in
// sorted-leaf order. Shared by the send-transfer, coop-exit, and
// initiate-preimage-swap coordinator flows. The batch is returned so callers
// that also need the per-job SigningResults (the send-transfer flow's
// response protos, requested via recordSigningResults) can read them back via
// signingResult.
func aggregateSendTransferLeafSignatures(
	ctx context.Context,
	config *so.Config,
	frostClient pbfrost.FrostServiceClient,
	signingJobsByLeaf map[string]*sendTransferLeafSigningJobs,
	allShares map[string]map[string][]byte,
	recordSigningResults bool,
) ([]*pbinternal.SendTransferLeafSignatures, *frostAggregationBatch, error) {
	signatures, leafIDs, batch, err := aggregateLeafRefundSignatures(ctx, config, frostClient, signingJobsByLeaf, allShares, recordSigningResults)
	if err != nil {
		return nil, nil, err
	}

	leafSignatures := make([]*pbinternal.SendTransferLeafSignatures, 0, len(leafIDs))
	for _, leafID := range leafIDs {
		jobs := signingJobsByLeaf[leafID]
		sigs := &pbinternal.SendTransferLeafSignatures{LeafId: leafID}
		if jobs.cpfp != nil {
			sigs.RefundSignature = signatures[leafAggregationJobKey(leafID, txKindCPFP)]
		}
		if jobs.direct != nil {
			sigs.DirectRefundSignature = signatures[leafAggregationJobKey(leafID, txKindDirect)]
		}
		if jobs.dfc != nil {
			sigs.DirectFromCpfpRefundSignature = signatures[leafAggregationJobKey(leafID, txKindDirectFromCPFP)]
		}
		leafSignatures = append(leafSignatures, sigs)
	}
	return leafSignatures, batch, nil
}

// aggregateMpcSendTransferLeafSignatures is aggregateSendTransferLeafSignatures'
// multiparty form: every job aggregates in MPC user-group mode with the
// sub-users' signature shares folded in, and all three refund variants are
// always present (the multiparty parser requires them). Signing results are
// always recorded — the MPC coordinator flow publishes them in its response.
func aggregateMpcSendTransferLeafSignatures(
	ctx context.Context,
	config *so.Config,
	frostClient pbfrost.FrostServiceClient,
	signingJobsByLeaf map[string]*mpcSendTransferLeafSigningJobs,
	allShares map[string]map[string][]byte,
) ([]*pbinternal.SendTransferLeafSignatures, *frostAggregationBatch, error) {
	leafIDs := slices.Sorted(maps.Keys(signingJobsByLeaf))

	batch := newFrostAggregationBatch(config)
	batch.recordSigningResults = true

	keyshareIDSet := make(map[uuid.UUID]struct{}, len(signingJobsByLeaf))
	keyshareIDs := make([]uuid.UUID, 0, len(signingJobsByLeaf))
	for _, jobs := range signingJobsByLeaf {
		for _, job := range []*helper.SigningJobWithPregeneratedNonce{jobs.cpfp, jobs.direct, jobs.dfc} {
			if job == nil {
				continue
			}
			if _, ok := keyshareIDSet[job.SigningKeyshareID]; !ok {
				keyshareIDSet[job.SigningKeyshareID] = struct{}{}
				keyshareIDs = append(keyshareIDs, job.SigningKeyshareID)
			}
		}
	}
	if err := batch.prefetchKeyPackages(ctx, keyshareIDs); err != nil {
		return nil, nil, err
	}

	for _, leafID := range leafIDs {
		jobs := signingJobsByLeaf[leafID]
		verifyingKey := jobs.leaf.VerifyingPubkey
		for _, variant := range []struct {
			txKind string
			job    *helper.SigningJobWithPregeneratedNonce
			shares []*pbfrost.SubUserSignatureShare
		}{
			{txKindCPFP, jobs.cpfp, jobs.cpfpShares},
			{txKindDirect, jobs.direct, jobs.directShares},
			{txKindDirectFromCPFP, jobs.dfc, jobs.dfcShares},
		} {
			if variant.job == nil {
				return nil, nil, sparkerrors.InternalDataInconsistency(fmt.Errorf("missing %s signing job for mpc leaf %s", variant.txKind, leafID))
			}
			if err := batch.addMpcJob(ctx, leafAggregationJobKey(leafID, variant.txKind), variant.job, allShares, verifyingKey, variant.shares); err != nil {
				return nil, nil, fmt.Errorf("build %s aggregation job for mpc leaf %s: %w", variant.txKind, leafID, err)
			}
		}
	}

	signatures, err := batch.aggregate(ctx, frostClient)
	if err != nil {
		return nil, nil, err
	}

	leafSignatures := make([]*pbinternal.SendTransferLeafSignatures, 0, len(leafIDs))
	for _, leafID := range leafIDs {
		leafSignatures = append(leafSignatures, &pbinternal.SendTransferLeafSignatures{
			LeafId:                        leafID,
			RefundSignature:               signatures[leafAggregationJobKey(leafID, txKindCPFP)],
			DirectRefundSignature:         signatures[leafAggregationJobKey(leafID, txKindDirect)],
			DirectFromCpfpRefundSignature: signatures[leafAggregationJobKey(leafID, txKindDirectFromCPFP)],
		})
	}
	return leafSignatures, batch, nil
}
