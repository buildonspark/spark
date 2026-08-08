package handler

import (
	"context"
	"fmt"
	"strconv"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/collections"
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
	"github.com/lightsparkdev/spark/so/knobs"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// ---------------------------------------------------------------------------
// DepositTreeFlowHandler — participant side (Prepare / Commit / Rollback)
// ---------------------------------------------------------------------------

var (
	_ consensus.FlowHandler                    = (*DepositTreeFlowHandler)(nil)
	_ consensus.ContextPrepareBoundFlowHandler = (*DepositTreeFlowHandler)(nil)
)

// DepositTreeFlowHandler implements consensus.FlowHandler for the deposit tree
// finalization flow. Each SO independently validates the deposit address,
// checks it hasn't been finalized, and produces FROST signature shares during Prepare.
type DepositTreeFlowHandler struct {
	config *so.Config
}

func NewDepositTreeFlowHandler(config *so.Config) *DepositTreeFlowHandler {
	return &DepositTreeFlowHandler{config: config}
}

// ValidateDecisionAgainstPrepare prevents the finalize payload from replacing
// the deposit tree state each participant validated before producing signature
// shares. Rollback is a no-op, so its prepare-shaped payload carries no state
// that needs binding.
func (h *DepositTreeFlowHandler) ValidateDecisionAgainstPrepare(ctx context.Context, prepareOp proto.Message, decisionOp proto.Message) error {
	prepare, ok := prepareOp.(*pbinternal.DepositTreePrepareRequest)
	if !ok {
		return fmt.Errorf("unexpected prepare operation type %T for deposit tree", prepareOp)
	}
	orig := prepare.GetOriginalRequest()
	if orig == nil {
		return fmt.Errorf("prepared deposit tree request has no original request")
	}

	switch decision := decisionOp.(type) {
	case *pbinternal.DepositTreePrepareRequest:
		return nil
	case *pbinternal.FinalizeTreeCreationRequest:
		if len(decision.GetNodes()) != 1 || decision.GetNodes()[0] == nil || decision.GetNodes()[0].ParentNodeId != nil {
			return fmt.Errorf("deposit tree commit must contain exactly one root node")
		}
		node := decision.GetNodes()[0]
		validateBinding, err := shouldValidateDepositPrepareBinding(ctx, prepare)
		if err != nil {
			return err
		}
		if validateBinding {
			preparedKeyshareID, err := uuid.Parse(prepare.GetSigningKeyshareId())
			if err != nil {
				return fmt.Errorf("invalid prepared signing keyshare id: %w", err)
			}
			commitKeyshareID, err := uuid.Parse(node.GetSigningKeyshareId())
			if err != nil {
				return fmt.Errorf("invalid commit signing keyshare id: %w", err)
			}
			if commitKeyshareID != preparedKeyshareID {
				return fmt.Errorf("commit signing keyshare does not match prepared signing keyshare")
			}
			preparedVerifyingKey, err := keys.ParsePublicKey(prepare.GetVerifyingPubkey())
			if err != nil {
				return fmt.Errorf("invalid prepared verifying public key: %w", err)
			}
			commitVerifyingKey, err := keys.ParsePublicKey(node.GetVerifyingPubkey())
			if err != nil {
				return fmt.Errorf("invalid commit verifying public key: %w", err)
			}
			if !commitVerifyingKey.Equals(preparedVerifyingKey) {
				return fmt.Errorf("commit verifying key does not match prepared verifying key")
			}
		}
		if !samePublicKey(node.GetOwnerIdentityPubkey(), orig.GetIdentityPublicKey()) {
			return fmt.Errorf("commit root identity owner does not match prepared identity owner")
		}
		if !samePublicKey(node.GetOwnerSigningPubkey(), orig.GetRootTxSigningJob().GetSigningPublicKey()) {
			return fmt.Errorf("commit root signing owner does not match prepared signing owner")
		}
		primaryTx, primaryOutput, additionalUtxos, err := preparedDepositRootInputs(orig)
		if err != nil {
			return err
		}
		expectedValue, err := checkedDepositRootTotalValue(primaryOutput, additionalUtxos)
		if err != nil {
			return err
		}
		if node.GetValue() != expectedValue {
			return fmt.Errorf("commit root value %d does not match prepared deposit value %d", node.GetValue(), expectedValue)
		}
		if decision.GetNetwork() != orig.GetOnChainUtxo().GetNetwork() {
			return fmt.Errorf("commit network %s does not match prepared network %s", decision.GetNetwork(), orig.GetOnChainUtxo().GetNetwork())
		}
		if node.GetVout() != orig.GetOnChainUtxo().GetVout() {
			return fmt.Errorf("commit root vout %d does not match prepared primary utxo vout %d", node.GetVout(), orig.GetOnChainUtxo().GetVout())
		}
		if len(node.GetDirectTx()) != 0 {
			return fmt.Errorf("commit root direct transaction must be empty")
		}
		if len(node.GetDirectRefundTx()) != 0 {
			return fmt.Errorf("commit root direct refund transaction must be empty")
		}
		if node.GetRefundTimelock() != 0 {
			return fmt.Errorf("commit root refund timelock must be zero")
		}
		if err := decisionTxMatchesPrepared(node.GetRawTx(), orig.GetRootTxSigningJob().GetRawTx(), "root"); err != nil {
			return err
		}
		if err := decisionTxMatchesPrepared(node.GetRawRefundTx(), orig.GetRefundTxSigningJob().GetRawTx(), "refund"); err != nil {
			return err
		}
		if err := decisionTxMatchesPrepared(node.GetDirectFromCpfpRefundTx(), orig.GetDirectFromCpfpRefundTxSigningJob().GetRawTx(), "direct-from-cpfp refund"); err != nil {
			return err
		}
		if err := verifySignedTransactions(node.GetRawTx(), node.GetRawRefundTx(), node.GetDirectFromCpfpRefundTx(), primaryTx, primaryOutput, additionalUtxos); err != nil {
			return fmt.Errorf("commit signed transaction verification failed: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unexpected decision operation type %T for deposit tree", decisionOp)
	}
}

func preparedDepositRootInputs(req *pb.FinalizeDepositTreeCreationRequest) (*wire.MsgTx, *wire.TxOut, []additionalUtxoData, error) {
	primaryUtxo := req.GetOnChainUtxo()
	if primaryUtxo == nil {
		return nil, nil, nil, fmt.Errorf("prepared primary utxo is required")
	}
	primaryTx, err := common.TxFromRawTxBytes(primaryUtxo.GetRawTx())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("invalid prepared primary utxo transaction: %w", err)
	}
	if uint64(primaryUtxo.GetVout()) >= uint64(len(primaryTx.TxOut)) {
		return nil, nil, nil, fmt.Errorf("prepared primary utxo vout %d is out of bounds", primaryUtxo.GetVout())
	}

	additionalUtxos := make([]additionalUtxoData, 0, len(req.GetAdditionalOnChainUtxos()))
	for i, additionalUtxo := range req.GetAdditionalOnChainUtxos() {
		if additionalUtxo == nil {
			return nil, nil, nil, fmt.Errorf("prepared additional utxo %d is required", i)
		}
		additionalTx, err := common.TxFromRawTxBytes(additionalUtxo.GetRawTx())
		if err != nil {
			return nil, nil, nil, fmt.Errorf("invalid prepared additional utxo %d transaction: %w", i, err)
		}
		if uint64(additionalUtxo.GetVout()) >= uint64(len(additionalTx.TxOut)) {
			return nil, nil, nil, fmt.Errorf("prepared additional utxo %d vout %d is out of bounds", i, additionalUtxo.GetVout())
		}
		additionalUtxos = append(additionalUtxos, additionalUtxoData{
			onChainTx:     additionalTx,
			onChainOutput: additionalTx.TxOut[additionalUtxo.GetVout()],
			vout:          additionalUtxo.GetVout(),
		})
	}

	return primaryTx, primaryTx.TxOut[primaryUtxo.GetVout()], additionalUtxos, nil
}

func shouldValidateDepositPrepareBinding(ctx context.Context, req *pbinternal.DepositTreePrepareRequest) (bool, error) {
	hasKeyshareID := req.GetSigningKeyshareId() != ""
	hasVerifyingKey := len(req.GetVerifyingPubkey()) != 0
	if hasKeyshareID != hasVerifyingKey {
		return false, fmt.Errorf("signing keyshare id and verifying public key must be provided together")
	}
	if !hasKeyshareID {
		if knobs.GetKnobsService(ctx).GetValue(knobs.KnobRequireDepositTreePrepareBinding, 0) > 0 {
			return false, fmt.Errorf("deposit prepare binding is required")
		}
		return false, nil
	}
	return true, nil
}

func validateDepositPrepareBinding(ctx context.Context, req *pbinternal.DepositTreePrepareRequest, expectedKeyshareID uuid.UUID, expectedVerifyingKey keys.Public) error {
	validateBinding, err := shouldValidateDepositPrepareBinding(ctx, req)
	if err != nil || !validateBinding {
		return err
	}
	keyshareID, err := uuid.Parse(req.GetSigningKeyshareId())
	if err != nil {
		return fmt.Errorf("invalid signing keyshare id: %w", err)
	}
	if keyshareID != expectedKeyshareID {
		return fmt.Errorf("signing keyshare id does not match deposit address")
	}
	verifyingKey, err := keys.ParsePublicKey(req.GetVerifyingPubkey())
	if err != nil {
		return fmt.Errorf("invalid verifying public key: %w", err)
	}
	if !verifyingKey.Equals(expectedVerifyingKey) {
		return fmt.Errorf("verifying public key does not match deposit address")
	}
	return nil
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
	origReq := req.GetOriginalRequest()
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
	reqIDPubKey, err := keys.ParsePublicKey(origReq.GetIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("invalid identity public key: %w", err)
	}

	network, err := convertAndValidateProtoNetwork(h.config, origReq.GetOnChainUtxo().GetNetwork())
	if err != nil {
		return nil, fmt.Errorf("invalid network %s: %w", origReq.GetOnChainUtxo().GetNetwork(), err)
	}

	// 4. Load and validate deposit address. The coordinator already
	// authoritatively verified UTXO confirmation before fanning out; participant
	// SOs skip that check (enforceUtxoConfirmation=false) since their own chain
	// watcher may lag the coordinator and hasn't necessarily recorded the deposit
	// UTXOs yet.
	depositAddress, onChainTx, onChainOutput, additionalUtxos, err := loadAndValidateDepositAddress(ctx, network, origReq, reqIDPubKey, false)
	if err != nil {
		return nil, err
	}
	expectedVerifyingKey := depositAddress.Edges.SigningKeyshare.PublicKey.Add(depositAddress.OwnerSigningPubkey)
	if err := validateDepositPrepareBinding(ctx, req, depositAddress.Edges.SigningKeyshare.ID, expectedVerifyingKey); err != nil {
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
		pk, ok := keyPackage.GetPublicShares()[id]
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
		jobKey := strconv.Itoa(i)
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
	createdTree, createdNode, err := createTreeAndNode(ctx, f.config, f.depositAddressEnt, f.onChainTxWire, f.onChainOutputWire, f.additionalUtxos, f.origReq.GetOnChainUtxo().GetVout(), f.networkTyped, f.verifyingKey, signedCpfpRootTx, signedCpfpRefundTx, signedDirectFromCpfpRefundTx)
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

	reqIDPubKey, err := validateIdentity(ctx, config, req.GetIdentityPublicKey())
	if err != nil {
		return nil, err
	}

	network, err := convertAndValidateProtoNetwork(config, req.GetOnChainUtxo().GetNetwork())
	if err != nil {
		return nil, fmt.Errorf("invalid network %s: %w", req.GetOnChainUtxo().GetNetwork(), err)
	}

	depositAddress, onChainTx, onChainOutput, additionalUtxos, err := loadAndValidateDepositAddress(ctx, network, req, reqIDPubKey, true)
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

	rootSigningPubKey, err := keys.ParsePublicKey(req.GetRootTxSigningJob().GetSigningPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse root signing key: %w", err)
	}

	return &depositTreeCoordinatorFlow{
		DepositTreeFlowHandler: NewDepositTreeFlowHandler(config),
		origReq:                req,
		prepareReq: &pbinternal.DepositTreePrepareRequest{
			OriginalRequest:   req,
			SigningKeyshareId: depositAddress.Edges.SigningKeyshare.ID.String(),
			VerifyingPubkey:   verifyingKey.Serialize(),
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
		for jobID, sigResult := range resp.GetResults() {
			if allShares[jobID] == nil {
				allShares[jobID] = make(map[string][]byte)
			}
			allShares[jobID][opID] = sigResult.GetSignatureShare()
		}
	}
	return allShares, participantIDs, nil
}

func buildDepositInternalSigningJobs(jobs []*helper.SigningJobWithPregeneratedNonce) ([]*pbinternal.SigningJob, error) {
	result := make([]*pbinternal.SigningJob, len(jobs))
	for i, job := range jobs {
		commitments := collections.ConvertObjectMapToProtoMap(job.Round1Packages)
		var userCommitments *pbcommon.SigningCommitment
		if job.UserCommitment != nil {
			userCommitments = job.UserCommitment.MarshalProto()
		}
		result[i] = &pbinternal.SigningJob{
			JobId:           strconv.Itoa(i),
			Message:         job.Message.Serialize(),
			KeyshareId:      job.SigningKeyshareID.String(),
			VerifyingKey:    job.VerifyingKey.Serialize(),
			Commitments:     commitments,
			UserCommitments: userCommitments,
		}
	}
	return result, nil
}
