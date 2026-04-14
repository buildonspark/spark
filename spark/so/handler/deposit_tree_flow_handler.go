package handler

import (
	"context"
	"fmt"

	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/handler/signing_handler"
	"github.com/lightsparkdev/spark/so/helper"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// ---------------------------------------------------------------------------
// DepositTreeFlowHandler — participant side (Prepare / Commit / Rollback)
// ---------------------------------------------------------------------------

var _ consensus.FlowHandler = (*DepositTreeFlowHandler)(nil)

// DepositTreeFlowHandler implements consensus.FlowHandler for the deposit tree
// finalization flow. Each SO independently validates the deposit address,
// checks it hasn't been finalized, and produces FROST signature shares during Prepare.
type DepositTreeFlowHandler struct {
	config *so.Config
}

func NewDepositTreeFlowHandler(config *so.Config) *DepositTreeFlowHandler {
	return &DepositTreeFlowHandler{config: config}
}

// Prepare validates the deposit, checks it hasn't been finalized, and performs
// local FROST signing. Returns FrostRound2Response with signature shares for
// SOs in the signing set, or nil for SOs outside the threshold.
func (h *DepositTreeFlowHandler) Prepare(ctx context.Context, op proto.Message) (proto.Message, error) {
	req, ok := op.(*pbinternal.DepositTreePrepareRequest)
	if !ok {
		return nil, fmt.Errorf("unexpected operation type %T for deposit tree prepare", op)
	}

	// 1. Get the original user request
	origReq := req.OriginalRequest
	if origReq == nil {
		return nil, fmt.Errorf("original_request is required")
	}

	// 2. Validate request fields
	if err := validateFinalizeDepositTreeCreationRequest(origReq); err != nil {
		return nil, err
	}

	// 3. Parse identity public key + network
	// Note: we don't call validateIdentity here because the Prepare RPC runs
	// on non-coordinator SOs via internal ConsensusPrepare, which doesn't carry
	// the user's session. The coordinator already authenticated the user before
	// fanning out. We just parse the key for loadAndValidateDepositAddress.
	reqIDPubKey, err := keys.ParsePublicKey(origReq.IdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid identity public key: %w", err)
	}

	network, err := convertAndValidateProtoNetwork(h.config, origReq.OnChainUtxo.Network)
	if err != nil {
		return nil, fmt.Errorf("invalid network %s: %w", origReq.OnChainUtxo.Network, err)
	}

	// 4. Load and validate deposit address (same validation as coordinator)
	depositAddress, onChainTx, onChainOutput, additionalUtxos, err := loadAndValidateDepositAddress(ctx, network, origReq, reqIDPubKey)
	if err != nil {
		return nil, err
	}

	// 5. Check not already finalized
	if depositAddress.Edges.Tree != nil {
		return nil, errors.AlreadyExistsDuplicateOperation(fmt.Errorf("tree already exists for deposit address %s", depositAddress.Address))
	}

	// 6. Prepare signing jobs locally
	signingJobs, _, rootTxInputCount, err := prepareSigningJobs(origReq, depositAddress, onChainTx, onChainOutput, additionalUtxos)
	if err != nil {
		return nil, err
	}

	signingJobsNonce, err := convertToSigningJobsWithPregeneratedNonce(signingJobs, origReq, rootTxInputCount)
	if err != nil {
		return nil, fmt.Errorf("failed to convert signing jobs: %w", err)
	}

	// 7. Local FROST signing — only if this SO is in the signing set.
	// The signing threshold (t-of-n) means only a subset of SOs have commitments.
	if len(signingJobsNonce) > 0 {
		_, inSigningSet := signingJobsNonce[0].Round1Packages[h.config.Identifier]
		if !inSigningSet {
			return nil, nil
		}
		internalJobs, err := buildDepositInternalSigningJobs(signingJobsNonce)
		if err != nil {
			return nil, fmt.Errorf("failed to build internal signing jobs: %w", err)
		}
		frostReq := &pbinternal.FrostRound2Request{SigningJobs: internalJobs}
		frostHandler := signing_handler.NewFrostSigningHandler(h.config)
		frostResp, err := frostHandler.FrostRound2(ctx, frostReq)
		if err != nil {
			return nil, fmt.Errorf("local frost signing failed during prepare: %w", err)
		}
		return frostResp, nil
	}

	return nil, nil
}

// Commit applies the finalized tree data. Delegates to InternalDepositHandler.FinalizeTreeCreation.
func (h *DepositTreeFlowHandler) Commit(ctx context.Context, op proto.Message) error {
	req, ok := op.(*pbinternal.FinalizeTreeCreationRequest)
	if !ok {
		return fmt.Errorf("unexpected operation type %T for deposit tree commit", op)
	}
	return NewInternalDepositHandler(h.config).FinalizeTreeCreation(ctx, req)
}

// Rollback is a no-op for deposit tree finalization. Unlike renew (which sets
// RenewLocked status), deposit Prepare doesn't mutate persistent state.
// The coordinator's DB writes in BuildCommitPayload are protected by the gRPC
// middleware transaction — partial failures roll back automatically.
// TODO: After full rollout, consider moving coordinator DB writes from
// BuildCommitPayload to Commit for consistency with non-coordinator SOs.
func (h *DepositTreeFlowHandler) Rollback(_ context.Context, _ proto.Message) error {
	return nil
}

// ---------------------------------------------------------------------------
// depositTreeCoordinatorFlow — coordinator side (CoordinatorFlow)
// ---------------------------------------------------------------------------

var _ consensus.CoordinatorFlow = (*depositTreeCoordinatorFlow)(nil)

type depositTreeCoordinatorFlow struct {
	*DepositTreeFlowHandler // embeds Prepare/Commit/Rollback

	origReq    *pb.FinalizeDepositTreeCreationRequest
	prepareReq *pbinternal.DepositTreePrepareRequest
	response   *pb.FinalizeDepositTreeCreationResponse

	// Pre-computed by coordinator
	signingJobs       []*helper.SigningJob
	verifyingKey      keys.Public
	rootTxInputCount  int
	additionalUtxos   []additionalUtxoData
	rootSigningPubKey keys.Public

	// Typed fields for createTreeAndNode / verifySignedTransactions
	depositAddressEnt *ent.DepositAddress
	onChainTxWire     *wire.MsgTx
	onChainOutputWire *wire.TxOut
	networkTyped      btcnetwork.Network
}

// PrepareOp returns the prepare request.
func (f *depositTreeCoordinatorFlow) PrepareOp() proto.Message {
	return f.prepareReq
}

// BuildCommitPayload aggregates signature shares, applies/verifies signatures,
// creates the Tree + TreeNode on the coordinator, and returns the commit message.
func (f *depositTreeCoordinatorFlow) BuildCommitPayload(ctx context.Context, results map[string]*anypb.Any) (proto.Message, error) {
	logger := logging.GetLoggerFromContext(ctx)

	// 1. Collect signature shares from all SOs' prepare results
	allShares, participantIDs, err := collectDepositSignatureShares(results)
	if err != nil {
		return nil, fmt.Errorf("failed to collect signature shares: %w", err)
	}

	// 2. Load key package for public shares
	if len(f.signingJobs) == 0 {
		return nil, fmt.Errorf("no signing jobs to aggregate")
	}
	keyPackage, err := ent.GetKeyPackage(ctx, f.config, f.signingJobs[0].SigningKeyshareID)
	if err != nil {
		return nil, fmt.Errorf("failed to load key package: %w", err)
	}

	// 3. Filter public shares to participants
	publicKeys := make(map[string][]byte, len(participantIDs))
	for _, id := range participantIDs {
		pk, ok := keyPackage.PublicShares[id]
		if !ok {
			return nil, fmt.Errorf("missing public share for operator %s", id)
		}
		publicKeys[id] = pk
	}

	// 4. Build signing results for aggregation.
	// Each SO uses deterministic index-based job IDs ("0", "1", "2", ...) so
	// results from different SOs can be correlated by position.
	signingResults := make([]*helper.SigningResult, len(f.signingJobs))
	for i, job := range f.signingJobs {
		jobKey := fmt.Sprintf("%d", i)
		shares, ok := allShares[jobKey]
		if !ok {
			return nil, fmt.Errorf("missing signature shares for job index %d", i)
		}
		signingResults[i] = &helper.SigningResult{
			JobID:           job.JobID,
			Message:         job.Message,
			SignatureShares: shares,
			PublicKeys:      publicKeys,
		}
	}

	// 5. Aggregate signatures
	signatures, err := aggregateDepositSignatures(ctx, f.config, f.origReq, signingResults, f.verifyingKey, f.rootSigningPubKey, f.rootTxInputCount)
	if err != nil {
		return nil, err
	}
	logger.Sugar().Infof("Successfully aggregated %d deposit signatures", len(signatures))

	// 6. Apply signatures to transactions
	signedCpfpRootTx, signedCpfpRefundTx, signedDirectFromCpfpRefundTx, err := applySignaturesToTransactions(f.origReq, signatures, f.rootTxInputCount)
	if err != nil {
		return nil, err
	}

	// 7. Verify signed transactions
	if err := verifySignedTransactions(signedCpfpRootTx, signedCpfpRefundTx, signedDirectFromCpfpRefundTx, f.onChainTxWire, f.onChainOutputWire, f.additionalUtxos); err != nil {
		return nil, fmt.Errorf("signed transaction verification failed: %w", err)
	}

	// 8. Create tree and node on coordinator
	createdTree, createdNode, err := createTreeAndNode(ctx, f.config, f.depositAddressEnt, f.onChainTxWire, f.onChainOutputWire, f.additionalUtxos, f.origReq.OnChainUtxo.Vout, f.networkTyped, f.verifyingKey, signedCpfpRootTx, signedCpfpRefundTx, signedDirectFromCpfpRefundTx)
	if err != nil {
		return nil, err
	}

	logger.Sugar().Infof("Created deposit tree via consensus for tree %s node %s", createdTree.ID, createdNode.ID)

	// 9. Build RPC response
	pbNode, err := createdNode.MarshalSparkProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal root node: %w", err)
	}
	f.response = &pb.FinalizeDepositTreeCreationResponse{
		RootNode: pbNode,
	}

	// 10. Build commit message — same data as FinalizeTreeCreation gossip
	protoNetwork, err := f.networkTyped.ToProtoNetwork()
	if err != nil {
		return nil, fmt.Errorf("failed to convert network: %w", err)
	}

	internalNode, err := createdNode.MarshalInternalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal root node internal: %w", err)
	}

	return &pbinternal.FinalizeTreeCreationRequest{
		Nodes:   []*pbinternal.TreeNode{internalNode},
		Network: protoNetwork,
	}, nil
}

// RollbackPayload returns an empty payload for rollback gossip.
// Deposit rollback is a no-op since Prepare doesn't mutate persistent state.
func (f *depositTreeCoordinatorFlow) RollbackPayload() proto.Message {
	return &pbinternal.DepositTreePrepareRequest{}
}

// ---------------------------------------------------------------------------
// Coordinator flow construction
// ---------------------------------------------------------------------------

// buildDepositCoordinatorFlow validates the request, prepares signing jobs,
// and builds the coordinator flow for the 2PC engine.
func buildDepositCoordinatorFlow(
	ctx context.Context,
	config *so.Config,
	req *pb.FinalizeDepositTreeCreationRequest,
) (*depositTreeCoordinatorFlow, error) {
	// Validate request
	if err := validateFinalizeDepositTreeCreationRequest(req); err != nil {
		return nil, err
	}

	reqIDPubKey, err := validateIdentity(ctx, config, req.IdentityPublicKey)
	if err != nil {
		return nil, err
	}

	network, err := convertAndValidateProtoNetwork(config, req.OnChainUtxo.Network)
	if err != nil {
		return nil, fmt.Errorf("invalid network %s: %w", req.OnChainUtxo.Network, err)
	}

	depositAddress, onChainTx, onChainOutput, additionalUtxos, err := loadAndValidateDepositAddress(ctx, network, req, reqIDPubKey)
	if err != nil {
		return nil, err
	}

	if depositAddress.Edges.Tree != nil {
		return nil, errors.AlreadyExistsDuplicateOperation(fmt.Errorf("tree already exists for deposit address %s", depositAddress.Address))
	}

	// Prepare signing jobs
	signingJobs, verifyingKey, rootTxInputCount, err := prepareSigningJobs(req, depositAddress, onChainTx, onChainOutput, additionalUtxos)
	if err != nil {
		return nil, err
	}

	rootSigningPubKey, err := keys.ParsePublicKey(req.RootTxSigningJob.SigningPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse root signing key: %w", err)
	}

	return &depositTreeCoordinatorFlow{
		DepositTreeFlowHandler: NewDepositTreeFlowHandler(config),
		origReq:                req,
		prepareReq: &pbinternal.DepositTreePrepareRequest{
			OriginalRequest: req,
		},
		signingJobs:       signingJobs,
		verifyingKey:      verifyingKey,
		rootTxInputCount:  rootTxInputCount,
		depositAddressEnt: depositAddress,
		onChainTxWire:     onChainTx,
		onChainOutputWire: onChainOutput,
		additionalUtxos:   additionalUtxos,
		networkTyped:      network,
		rootSigningPubKey: rootSigningPubKey,
	}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// collectDepositSignatureShares transposes prepare results from per-operator to per-job.
func collectDepositSignatureShares(results map[string]*anypb.Any) (map[string]map[string][]byte, []string, error) {
	allShares := make(map[string]map[string][]byte)
	participantIDs := make([]string, 0, len(results))

	for opID, anyResult := range results {
		if anyResult == nil {
			// Non-signing participant (outside threshold set) — skip
			continue
		}
		participantIDs = append(participantIDs, opID)
		resp := &pbinternal.FrostRound2Response{}
		if err := anyResult.UnmarshalTo(resp); err != nil {
			return nil, nil, fmt.Errorf("failed to unmarshal prepare result from %s: %w", opID, err)
		}
		for jobID, sigResult := range resp.Results {
			if allShares[jobID] == nil {
				allShares[jobID] = make(map[string][]byte)
			}
			allShares[jobID][opID] = sigResult.SignatureShare
		}
	}
	return allShares, participantIDs, nil
}

func buildDepositInternalSigningJobs(jobs []*helper.SigningJobWithPregeneratedNonce) ([]*pbinternal.SigningJob, error) {
	result := make([]*pbinternal.SigningJob, len(jobs))
	for i, job := range jobs {
		commitments := make(map[string]*pbcommon.SigningCommitment, len(job.Round1Packages))
		for id, c := range job.Round1Packages {
			cp, err := c.MarshalProto()
			if err != nil {
				return nil, fmt.Errorf("failed to marshal commitment for %s: %w", id, err)
			}
			commitments[id] = cp
		}
		var userCommitments *pbcommon.SigningCommitment
		if job.UserCommitment != nil {
			uc, err := job.UserCommitment.MarshalProto()
			if err != nil {
				return nil, fmt.Errorf("failed to marshal user commitment: %w", err)
			}
			userCommitments = uc
		}
		result[i] = &pbinternal.SigningJob{
			JobId:           fmt.Sprintf("%d", i),
			Message:         job.Message,
			KeyshareId:      job.SigningKeyshareID.String(),
			VerifyingKey:    job.VerifyingKey.Serialize(),
			Commitments:     commitments,
			UserCommitments: userCommitments,
		}
	}
	return result, nil
}
