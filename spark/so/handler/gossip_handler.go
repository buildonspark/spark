package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/common/uuids"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/consensus"
	sparkdb "github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/flowexecution"
	"github.com/lightsparkdev/spark/so/ent/preimagerequest"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	enttree "github.com/lightsparkdev/spark/so/ent/tree"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

var gossipMessageHandledTotal metric.Int64Counter
var gossipMessageHandledDuration metric.Float64Histogram

// consensusOpFencedTotal counts skipped consensus commit/rollback gossip ops.
// Attribute "phase" = "commit" | "rollback"; attribute "disposition" =
// "foreign" (no participant row — unexpected) | "already_terminal" (benign
// redelivery of a flow whose effect was already applied). On-call should treat
// a "foreign" spike as more concerning than "already_terminal".
var consensusOpFencedTotal metric.Int64Counter

func init() {
	meter := otel.GetMeterProvider().Meter("spark.grpc")

	counter, err := meter.Int64Counter(
		"gossip.message_handled_total",
		metric.WithDescription("Total number of gossip messages handled by type and status"),
		metric.WithUnit("{count}"),
	)
	if err != nil {
		otel.Handle(err)
		if counter == nil {
			counter = noop.Int64Counter{}
		}
	}
	gossipMessageHandledTotal = counter

	histogram, err := meter.Float64Histogram(
		"gossip.message_handled_duration",
		metric.WithDescription("Duration of gossip message handling by type"),
		metric.WithUnit("ms"),
		metric.WithExplicitBucketBoundaries(1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000),
	)
	if err != nil {
		otel.Handle(err)
		if histogram == nil {
			histogram = noop.Float64Histogram{}
		}
	}
	gossipMessageHandledDuration = histogram

	consensusOpFencedTotal = newConsensusOpFencedCounter()
}

// newConsensusOpFencedCounter builds the fence-skip counter from the current global
// meter provider; extracted so tests can rebind it to a manual reader.
func newConsensusOpFencedCounter() metric.Int64Counter {
	counter, err := otel.GetMeterProvider().Meter("spark.grpc").Int64Counter(
		"gossip.consensus_op_fenced_total",
		metric.WithDescription("Total number of consensus commit/rollback gossip ops skipped by the SP-3336 FlowExecution fence"),
		metric.WithUnit("{count}"),
	)
	if err != nil {
		otel.Handle(err)
		if counter == nil {
			counter = noop.Int64Counter{}
		}
	}
	return counter
}

type GossipHandler struct {
	config *so.Config
}

func NewGossipHandler(config *so.Config) *GossipHandler {
	return &GossipHandler{config: config}
}

// Routes incoming gossip messages to their appropriate handlers based on message type.
// The forCoordinator flag indicates whether this operator is the coordinator for the operation,
// which affects how certain message types (e.g., FinalizeTransfer) are processed.
// Returns nil for non-retryable errors to prevent the gossip system from retrying failed operations.
func (h *GossipHandler) HandleGossipMessage(ctx context.Context, gossipMessage *pbgossip.GossipMessage, forCoordinator bool) error {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Sugar().Infof("Handling gossip message with ID %s", gossipMessage.GetMessageId())

	messageType := getGossipMessageType(gossipMessage)

	startTime := time.Now()
	var err error
	switch gossipMessage.GetMessage().(type) {
	case *pbgossip.GossipMessage_CancelTransfer:
		cancelTransfer := gossipMessage.GetCancelTransfer()
		err = h.handleCancelTransferGossipMessage(ctx, cancelTransfer)
	case *pbgossip.GossipMessage_SettleSenderKeyTweak:
		settleSenderKeyTweak := gossipMessage.GetSettleSenderKeyTweak()
		err = h.handleSettleSenderKeyTweakGossipMessage(ctx, settleSenderKeyTweak)
	case *pbgossip.GossipMessage_RollbackTransfer:
		rollbackTransfer := gossipMessage.GetRollbackTransfer()
		err = h.handleRollbackTransfer(ctx, rollbackTransfer)
	case *pbgossip.GossipMessage_MarkTreesExited:
		markTreesExited := gossipMessage.GetMarkTreesExited()
		err = h.handleMarkTreesExited(ctx, markTreesExited)
	case *pbgossip.GossipMessage_FinalizeTreeCreation:
		finalizeTreeCreation := gossipMessage.GetFinalizeTreeCreation()
		err = h.handleFinalizeTreeCreationGossipMessage(ctx, finalizeTreeCreation, forCoordinator)
	case *pbgossip.GossipMessage_FinalizeTransfer:
		finalizeTransfer := gossipMessage.GetFinalizeTransfer()
		err = h.handleFinalizeTransferGossipMessage(ctx, finalizeTransfer, forCoordinator)
	case *pbgossip.GossipMessage_FinalizeNodeTimelock:
		finalizeRenewNodeTimelock := gossipMessage.GetFinalizeNodeTimelock()
		err = h.handleFinalizeNodeTimelockGossipMessage(ctx, finalizeRenewNodeTimelock, forCoordinator)
	case *pbgossip.GossipMessage_FinalizeRefundTimelock:
		finalizeRenewRefundTimelock := gossipMessage.GetFinalizeRefundTimelock()
		err = h.handleFinalizeRefundTimelockGossipMessage(ctx, finalizeRenewRefundTimelock, forCoordinator)
	case *pbgossip.GossipMessage_UpdateWalletSetting:
		updateWalletSetting := gossipMessage.GetUpdateWalletSetting()
		err = h.handleUpdateWalletSettingGossipMessage(ctx, updateWalletSetting, forCoordinator)
	case *pbgossip.GossipMessage_RollbackUtxoSwap:
		rollbackUtxoSwap := gossipMessage.GetRollbackUtxoSwap()
		err = h.handleRollbackUtxoSwapGossipMessage(ctx, rollbackUtxoSwap)
	case *pbgossip.GossipMessage_RollbackInstantUtxoSwap:
		rollbackInstantUtxoSwap := gossipMessage.GetRollbackInstantUtxoSwap()
		err = h.handleRollbackInstantUtxoSwapGossipMessage(ctx, rollbackInstantUtxoSwap)
	case *pbgossip.GossipMessage_DepositCleanup:
		depositCleanup := gossipMessage.GetDepositCleanup()
		err = h.handleDepositCleanupGossipMessage(ctx, depositCleanup)
	case *pbgossip.GossipMessage_Preimage:
		preimage := gossipMessage.GetPreimage()
		err = h.handlePreimageGossipMessage(ctx, preimage, forCoordinator)
	case *pbgossip.GossipMessage_PreimageSwap:
		preimageSwap := gossipMessage.GetPreimageSwap()
		err = h.handlePreimageSwapGossipMessage(ctx, preimageSwap, forCoordinator)
	case *pbgossip.GossipMessage_SettleSwapKeyTweak:
		settleSwapKeyTweak := gossipMessage.GetSettleSwapKeyTweak()
		err = h.handleSettleSwapKeyTweakGossipMessage(ctx, settleSwapKeyTweak)
	case *pbgossip.GossipMessage_FinalizeRefreshTimelock:
		err = fmt.Errorf("gossip message has been deprecated: %T", gossipMessage.GetMessage())
	case *pbgossip.GossipMessage_FinalizeExtendLeaf:
		err = fmt.Errorf("gossip message has been deprecated: %T", gossipMessage.GetMessage())
	case *pbgossip.GossipMessage_ArchiveStaticDepositAddress:
		archiveStaticDepositAddress := gossipMessage.GetArchiveStaticDepositAddress()
		err = h.handleArchiveStaticDepositAddressGossipMessage(ctx, archiveStaticDepositAddress, forCoordinator)
	case *pbgossip.GossipMessage_FinalizeTransferReceiver:
		finalizeTransferReceiver := gossipMessage.GetFinalizeTransferReceiver()
		err = h.handleFinalizeTransferReceiverGossipMessage(ctx, finalizeTransferReceiver, forCoordinator)
	case *pbgossip.GossipMessage_FinalizeTreeNode:
		err = h.handleFinalizeTreeNodeGossipMessage(ctx, gossipMessage.GetFinalizeTreeNode(), forCoordinator)
	case *pbgossip.GossipMessage_ConsensusCommit:
		if !forCoordinator {
			commit := gossipMessage.GetConsensusCommit()
			var op proto.Message
			if op, err = commit.GetOperation().UnmarshalNew(); err == nil {
				err = dispatchConsensusCommit(ctx, h.config, commit.GetOpType(), commit.GetFlowExecutionId(), op)
			}
		}
	case *pbgossip.GossipMessage_ConsensusRollback:
		if !forCoordinator {
			rollback := gossipMessage.GetConsensusRollback()
			var op proto.Message
			if op, err = rollback.GetOperation().UnmarshalNew(); err == nil {
				err = dispatchConsensusRollback(ctx, h.config, rollback.GetOpType(), rollback.GetFlowExecutionId(), op)
			}
		}
	default:
		err = fmt.Errorf("unsupported gossip message type: %T", gossipMessage.GetMessage())
	}

	// Transient contention during a gossip apply is retryable (redelivered, idempotent), not a hard failure.
	if err != nil && sparkdb.IsTransientContentionError(err) {
		err = sparkerrors.AbortedLockConflict(err)
	}

	if err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Handling for gossip message ID %s of type %s failed with error: %v", gossipMessage.GetMessageId(), messageType, err)
	}

	// Record metrics
	statusCode := status.Code(err)
	metricAttrs := metric.WithAttributes(
		attribute.String("message_type", messageType),
		semconv.RPCGRPCStatusCodeKey.Int(int(statusCode)),
	)
	gossipMessageHandledTotal.Add(ctx, 1, metricAttrs)
	gossipMessageHandledDuration.Record(ctx, time.Since(startTime).Seconds()*1000, metricAttrs)

	return err
}

func getGossipMessageType(msg *pbgossip.GossipMessage) string {
	if msg.Message == nil {
		return "unknown"
	}
	// Return the raw protobuf type name, e.g., "*gossip.GossipMessage_CancelTransfer"
	return fmt.Sprintf("%T", msg.GetMessage())
}

func (h *GossipHandler) handleCancelTransferGossipMessage(ctx context.Context, cancelTransfer *pbgossip.GossipMessageCancelTransfer) error {
	transferID, err := uuid.Parse(cancelTransfer.GetTransferId())
	if err != nil {
		return fmt.Errorf("failed to cancel transfer: invalid transfer ID: %s: %w", cancelTransfer.GetTransferId(), err)
	}
	transferHandler := NewBaseTransferHandler(h.config)
	err = transferHandler.CancelTransferInternal(ctx, transferID)
	if err != nil {
		if ent.IsNotFound(err) {
			// The transfer is not created, treat it as successful cancellation.
			return nil
		}
		logger := logging.GetLoggerFromContext(ctx)
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to cancel transfer %s", transferID)
	}
	return err
}

func (h *GossipHandler) handleSettleSenderKeyTweakGossipMessage(ctx context.Context, settleSenderKeyTweak *pbgossip.GossipMessageSettleSenderKeyTweak) error {
	transferHandler := NewBaseTransferHandler(h.config)
	transferID, err := uuid.Parse(settleSenderKeyTweak.GetTransferId())
	if err != nil {
		return fmt.Errorf("failed to settle sender key tweak: invalid transfer ID: %s: %w", settleSenderKeyTweak.GetTransferId(), err)
	}
	_, err = transferHandler.CommitSenderKeyTweaks(ctx, transferID, settleSenderKeyTweak.GetSenderKeyTweakProofs())
	if err != nil {
		logger := logging.GetLoggerFromContext(ctx)
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to settle sender key tweak for transfer %s", transferID)
	}
	return err
}

func (h *GossipHandler) handleRollbackTransfer(ctx context.Context, req *pbgossip.GossipMessageRollbackTransfer) error {
	logger := logging.GetLoggerFromContext(ctx)
	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return fmt.Errorf("failed to roll back transfer: invalid transfer ID: %s: %w", req.GetTransferId(), err)
	}

	logger.Sugar().Infof("Handling rollback transfer gossip message for transfer %s", transferID)

	baseHandler := NewBaseTransferHandler(h.config)
	err = baseHandler.RollbackTransfer(ctx, transferID)
	if err != nil {
		if ent.IsNotFound(err) {
			// The transfer is not created, treat it as successful rollback.
			return nil
		}
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to rollback transfer %s", transferID)
	}
	return err
}

func (h *GossipHandler) handleMarkTreesExited(ctx context.Context, req *pbgossip.GossipMessageMarkTreesExited) error {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Sugar().Infof("Handling mark trees exited gossip message for trees %+q", req.GetTreeIds())

	treeIDs, err := uuids.ParseSlice(req.GetTreeIds())
	if err != nil {
		return fmt.Errorf("failed to parse tree IDs as UUIDs: %w", err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		logger.Error("Failed to get or create current tx for request", zap.Error(err))
		return err
	}

	trees, err := db.Tree.Query().
		Where(enttree.IDIn(treeIDs...)).
		ForUpdate().
		All(ctx)
	if err != nil {
		logger.Error("Failed to query trees", zap.Error(err))
		return err
	}

	treeExitHandler := newTreeExitHandler(h.config)
	if markErr := treeExitHandler.markTreesExited(ctx, trees); markErr != nil {
		logger.With(zap.Error(markErr)).Sugar().Errorf("Failed to mark trees %+q exited", req.GetTreeIds())
		return markErr
	}
	return err
}

func (h *GossipHandler) handleDepositCleanupGossipMessage(ctx context.Context, req *pbgossip.GossipMessageDepositCleanup) error {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Sugar().Infof("Handling deposit cleanup gossip message for tree %s", req.GetTreeId())

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		logger.Error("Failed to get or create current tx for request", zap.Error(err))
		return err
	}

	treeID, err := uuid.Parse(req.GetTreeId())
	if err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to parse tree ID %s as UUID", req.GetTreeId())
		return err
	}

	// a) Query all tree nodes under this tree with lock to prevent race conditions
	treeNodes, err := db.TreeNode.Query().
		Where(treenode.HasTreeWith(enttree.IDEQ(treeID))).
		ForUpdate().
		All(ctx)
	if err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to query tree nodes for tree %s", treeID)
		return err
	}

	// b) Get the count of all tree nodes excluding those that have been extended
	nonSplitLeafCount := 0
	for _, node := range treeNodes {
		if node.Status != st.TreeNodeStatusSplitted && node.Status != st.TreeNodeStatusSplitLocked {
			nonSplitLeafCount++
		}
	}

	// c) Throw an error if this count > 1
	if nonSplitLeafCount > 1 {
		return fmt.Errorf("expected at most 1 tree node for tree %s excluding extended leaves (got: %d)", treeID, nonSplitLeafCount)
	}

	// d) Delete all tree nodes associated with the tree
	for _, node := range treeNodes {
		err = db.TreeNode.DeleteOne(node).Exec(ctx)
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf("Failed to delete tree node %s", node.ID)
			return err
		}
		logger.Sugar().Infof("Successfully deleted tree node %s for deposit cleanup", node.ID)
	}

	// Delete the tree
	switch err := db.Tree.DeleteOneID(treeID).Exec(ctx); {
	case ent.IsNotFound(err):
		logger.Sugar().Warnf("Tree %s not found for deposit cleanup", treeID)
	case err != nil:
		logger.With(zap.Error(err)).Sugar().Warnf("Failed to delete tree %s", treeID)
	default:
		logger.Sugar().Infof("Successfully deleted tree %s for deposit cleanup", treeID)
		logger.Sugar().Infof("Completed deposit cleanup processing for tree %s", treeID)
	}
	return nil
}

func (h *GossipHandler) handleFinalizeTreeCreationGossipMessage(ctx context.Context, finalizeNodeSignatures *pbgossip.GossipMessageFinalizeTreeCreation, forCoordinator bool) error {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Info("Handling finalize tree creation gossip message")

	if forCoordinator {
		return nil
	}

	depositHandler := NewInternalDepositHandler(h.config)
	err := depositHandler.FinalizeTreeCreation(ctx, &pbinternal.FinalizeTreeCreationRequest{Nodes: finalizeNodeSignatures.GetInternalNodes(), Network: finalizeNodeSignatures.GetProtoNetwork()})
	if err != nil {
		logger.Error("Failed to finalize tree creation", zap.Error(err))
	}
	return err
}

func (h *GossipHandler) handleFinalizeTransferGossipMessage(ctx context.Context, finalizeNodeSignatures *pbgossip.GossipMessageFinalizeTransfer, forCoordinator bool) error {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Info("Handling finalize transfer gossip message")

	if forCoordinator {
		return nil
	}
	transferHandler := NewInternalTransferHandler(h.config)
	err := transferHandler.FinalizeTransfer(ctx, &pbinternal.FinalizeTransferRequest{TransferId: finalizeNodeSignatures.GetTransferId(), Nodes: finalizeNodeSignatures.GetInternalNodes(), Timestamp: finalizeNodeSignatures.GetCompletionTimestamp()})
	if err != nil {
		logger.Error("Failed to finalize transfer", zap.Error(err))
	}
	return err
}

func (h *GossipHandler) handleFinalizeNodeTimelockGossipMessage(ctx context.Context, finalizeRenewNodeTimelock *pbgossip.GossipMessageFinalizeRenewNodeTimelock, forCoordinator bool) error {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Info("Handling finalize renew node timelock gossip message")

	if forCoordinator {
		return nil
	}

	renewLeafHandler := NewInternalRenewLeafHandler(h.config)
	err := renewLeafHandler.FinalizeRenewNodeTimelock(ctx, &pbinternal.FinalizeRenewNodeTimelockRequest{
		SplitNode: finalizeRenewNodeTimelock.GetSplitNode(),
		Node:      finalizeRenewNodeTimelock.GetNode(),
	})
	if err != nil {
		logger.Error("Failed to finalize renew node timelock", zap.Error(err))
	}
	return err
}

func (h *GossipHandler) handleFinalizeRefundTimelockGossipMessage(ctx context.Context, finalizeRenewRefundTimelock *pbgossip.GossipMessageFinalizeRenewRefundTimelock, forCoordinator bool) error {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Info("Handling finalize renew refund timelock gossip message")

	if forCoordinator {
		return nil
	}

	renewLeafHandler := NewInternalRenewLeafHandler(h.config)
	err := renewLeafHandler.FinalizeRenewRefundTimelock(ctx, &pbinternal.FinalizeRenewRefundTimelockRequest{
		Node: finalizeRenewRefundTimelock.GetNode(),
	})
	if err != nil {
		logger.Error("Failed to finalize renew refund timelock", zap.Error(err))
	}
	return err
}

func (h *GossipHandler) handleRollbackUtxoSwapGossipMessage(ctx context.Context, rollbackUtxoSwap *pbgossip.GossipMessageRollbackUtxoSwap) error {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Info("Handling rollback utxo swap gossip message")

	depositHandler := NewInternalDepositHandler(h.config)
	_, err := depositHandler.RollbackUtxoSwap(ctx, h.config, &pbinternal.RollbackUtxoSwapRequest{
		OnChainUtxo:           rollbackUtxoSwap.GetOnChainUtxo(),
		Signature:             rollbackUtxoSwap.GetSignature(),
		CoordinatorPublicKey:  rollbackUtxoSwap.GetCoordinatorPublicKey(),
		ConfirmationThreshold: rollbackUtxoSwap.ConfirmationThreshold,
	})
	if err != nil {
		if ent.IsNotFound(err) || status.Code(err) == codes.NotFound {
			return nil
		}
		logger.Error("failed to rollback utxo swap with gossip message", zap.Error(err))
	}
	return err
}

func (h *GossipHandler) handleRollbackInstantUtxoSwapGossipMessage(ctx context.Context, rollbackInstantUtxoSwap *pbgossip.GossipMessageRollbackInstantUtxoSwap) error {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Info("Handling rollback instant utxo swap gossip message")

	depositHandler := NewInternalDepositHandler(h.config)
	_, err := depositHandler.RollbackInstantUtxoSwap(ctx, h.config, &pbinternal.RollbackInstantUtxoSwapRequest{
		OnChainUtxo:          rollbackInstantUtxoSwap.GetOnChainUtxo(),
		Signature:            rollbackInstantUtxoSwap.GetSignature(),
		CoordinatorPublicKey: rollbackInstantUtxoSwap.GetCoordinatorPublicKey(),
		RollbackFromStatuses: rollbackInstantUtxoSwap.GetRollbackFromStatuses(),
		RollbackToStatus:     rollbackInstantUtxoSwap.GetRollbackToStatus(),
	})
	if err != nil {
		if ent.IsNotFound(err) || status.Code(err) == codes.NotFound {
			return nil
		}
		logger.Error("failed to rollback instant utxo swap with gossip message", zap.Error(err))
	}
	return err
}

func (h *GossipHandler) handlePreimageGossipMessage(ctx context.Context, gossip *pbgossip.GossipMessagePreimage, forCoordinator bool) error {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Info("Handling preimage gossip message")

	if forCoordinator {
		return nil
	}

	if len(gossip.GetPreimage()) != sha256.Size {
		err := fmt.Errorf("preimage must be %d bytes, got %d", sha256.Size, len(gossip.GetPreimage()))
		logger.Error(err.Error())
		return err
	}
	calculatedHash := sha256.Sum256(gossip.GetPreimage())
	if !bytes.Equal(calculatedHash[:], gossip.GetPaymentHash()) {
		err := fmt.Errorf("preimage hash mismatch (expected %x, got %x)", calculatedHash[:], gossip.GetPaymentHash())
		logger.Error(err.Error())
		return err
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		logger.Error("Failed to get or create current tx for request", zap.Error(err))
		return err
	}

	preimageRequests, err := db.PreimageRequest.Query().Where(preimagerequest.PaymentHashEQ(gossip.GetPaymentHash())).ForUpdate().All(ctx)
	if err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to get preimage request for %x", gossip.GetPaymentHash())
		return err
	}
	activePreimageRequests := make([]*ent.PreimageRequest, 0, len(preimageRequests))
	for _, preimageRequest := range preimageRequests {
		if preimageRequest.Status != st.PreimageRequestStatusReturned {
			activePreimageRequests = append(activePreimageRequests, preimageRequest)
		}
	}
	if len(activePreimageRequests) > 1 {
		return fmt.Errorf("preimage gossip for payment hash %x matches multiple preimage requests without a transfer binding", gossip.GetPaymentHash())
	}

	for _, preimageRequest := range activePreimageRequests {
		_, err = db.PreimageRequest.UpdateOneID(preimageRequest.ID).SetPreimage(gossip.GetPreimage()).Save(ctx)
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf("Failed to update preimage request for %x", gossip.GetPaymentHash())
			return err
		}
	}
	return nil
}

func (h *GossipHandler) handlePreimageSwapGossipMessage(ctx context.Context, gossip *pbgossip.GossipMessagePreimageSwap, forCoordinator bool) error {
	if forCoordinator {
		return nil
	}

	logger := logging.GetLoggerFromContext(ctx)
	logger.Info("Handling preimage swap gossip message")

	if len(gossip.GetPreimage()) != sha256.Size {
		return fmt.Errorf("preimage must be %d bytes, got %d", sha256.Size, len(gossip.GetPreimage()))
	}
	calculatedHash := sha256.Sum256(gossip.GetPreimage())
	if !bytes.Equal(calculatedHash[:], gossip.GetPaymentHash()) {
		return fmt.Errorf("preimage hash mismatch (expected %x, got %x)", calculatedHash[:], gossip.GetPaymentHash())
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get db context: %w", err)
	}

	preimageRequestQuery := db.PreimageRequest.Query().
		Where(preimagerequest.PaymentHashEQ(gossip.GetPaymentHash())).
		ForUpdate()
	preimageRequestTransferIDString := gossip.GetPreimageRequestTransferId()
	if preimageRequestTransferIDString == "" {
		preimageRequestTransferIDString = gossip.GetTransferId()
	} else if gossip.GetTransferId() != "" && gossip.GetTransferId() != preimageRequestTransferIDString {
		return fmt.Errorf("preimage swap gossip transfer_id %s does not match preimage_request_transfer_id %s", gossip.GetTransferId(), preimageRequestTransferIDString)
	}
	var preimageRequestTransferID uuid.UUID
	if preimageRequestTransferIDString != "" {
		preimageRequestTransferID, err = uuid.Parse(preimageRequestTransferIDString)
		if err != nil {
			return fmt.Errorf("invalid preimage request transfer ID in preimage swap gossip: %s: %w", preimageRequestTransferIDString, err)
		}
		preimageRequestQuery = preimageRequestQuery.Where(preimagerequest.HasTransfersWith(enttransfer.IDEQ(preimageRequestTransferID)))
	}
	preimageRequests, err := preimageRequestQuery.All(ctx)
	if err != nil {
		return fmt.Errorf("failed to get preimage requests for %x: %w", gossip.GetPaymentHash(), err)
	}
	activePreimageRequests := make([]*ent.PreimageRequest, 0, len(preimageRequests))
	for _, preimageRequest := range preimageRequests {
		if preimageRequest.Status != st.PreimageRequestStatusReturned {
			activePreimageRequests = append(activePreimageRequests, preimageRequest)
		}
	}
	if preimageRequestTransferIDString != "" && len(preimageRequests) == 0 {
		return fmt.Errorf("preimage swap gossip for transfer %s did not match a preimage request for payment hash %x", preimageRequestTransferID, gossip.GetPaymentHash())
	}
	if preimageRequestTransferIDString == "" && len(activePreimageRequests) > 1 {
		return fmt.Errorf("preimage swap gossip for payment hash %x matches multiple preimage requests without a transfer binding", gossip.GetPaymentHash())
	}
	if preimageRequestTransferIDString != "" && len(activePreimageRequests) == 0 {
		logger.Sugar().Infof("Preimage swap gossip for transfer %s and payment hash %x matched only returned preimage requests; skipping preimage update", preimageRequestTransferID, gossip.GetPaymentHash())
	}
	if preimageRequestTransferIDString != "" && len(activePreimageRequests) > 1 {
		return fmt.Errorf("preimage swap gossip for transfer %s and payment hash %x matches multiple active preimage requests", preimageRequestTransferID, gossip.GetPaymentHash())
	}
	for _, preimageRequest := range activePreimageRequests {
		update := db.PreimageRequest.UpdateOneID(preimageRequest.ID).SetPreimage(gossip.GetPreimage())
		if preimageRequest.Status == st.PreimageRequestStatusWaitingForPreimage {
			update = update.SetStatus(st.PreimageRequestStatusPreimageShared)
		}
		if _, err = update.Save(ctx); err != nil {
			return fmt.Errorf("failed to update preimage request for %x: %w", gossip.GetPaymentHash(), err)
		}
	}

	if gossip.GetTransferId() != "" && len(gossip.GetSenderKeyTweakProofs()) > 0 {
		transferID, err := uuid.Parse(gossip.GetTransferId())
		if err != nil {
			return fmt.Errorf("invalid transfer ID in preimage swap gossip: %s: %w", gossip.GetTransferId(), err)
		}
		transferHandler := NewBaseTransferHandler(h.config)
		if _, err = transferHandler.CommitSenderKeyTweaks(ctx, transferID, gossip.GetSenderKeyTweakProofs()); err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf("Failed to settle sender key tweak for transfer %s", transferID)
			return err
		}
	}

	return nil
}

func (h *GossipHandler) handleSettleSwapKeyTweakGossipMessage(ctx context.Context, settleSwapKeyTweak *pbgossip.GossipMessageSettleSwapKeyTweak) error {
	logger := logging.GetLoggerFromContext(ctx)
	transferHandler := NewBaseTransferHandler(h.config)
	id, err := uuid.Parse(settleSwapKeyTweak.GetCounterTransferId())
	if err != nil {
		return fmt.Errorf("invalid counter transfer id: %w", err)
	}
	// The legacy counter settles through this gossip path, which — unlike the
	// consensus counter's Prepare (requirePrimaryRefundSignaturesApplied) — has
	// no per-SO fence. Commit gossip is asynchronous, so a legacy counter could
	// otherwise finalize a consensus primary on a lagging SO before that SO wrote
	// the primary's refund-tx witnesses. Gate the settle on the primary's refund
	// signatures being applied here; the FailedPrecondition propagates so gossip
	// redelivery retries the settle once the primary commit lands.
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("unable to get database transaction: %w", err)
	}
	counterTransfer, err := db.Transfer.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("unable to load counter transfer %s: %w", id, err)
	}
	if err := requirePrimaryRefundSignaturesApplied(ctx, counterTransfer); err != nil {
		logger.With(zap.Error(err)).Sugar().Infof(
			"deferring swap key tweak settle for counter transfer %s until the primary's refund signatures land", id)
		return err
	}
	if err := transferHandler.CommitSwapKeyTweaks(ctx, id); err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to settle swap key tweak for counter transfer %s", id)
		return err
	}
	return nil
}

func (h *GossipHandler) handleUpdateWalletSettingGossipMessage(ctx context.Context, updateWalletSetting *pbgossip.GossipMessageUpdateWalletSetting, forCoordinator bool) error {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Info("Handling update wallet setting gossip message")

	if forCoordinator {
		return nil
	}

	ownerIdentityPubKey, err := keys.ParsePublicKey(updateWalletSetting.GetOwnerIdentityPublicKey())
	if err != nil {
		logger.Error("Failed to parse owner identity public key", zap.Error(err))
		return fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	logger.Sugar().Infof("Handling wallet setting update gossip message for identity public key %s", ownerIdentityPubKey)

	walletSettingHandler := NewWalletSettingHandler(h.config)
	var privateEnabled *bool
	if updateWalletSetting.PrivateEnabled != nil {
		value := updateWalletSetting.GetPrivateEnabled()
		privateEnabled = &value
	}
	_, err = walletSettingHandler.UpdateWalletSettingInternal(ctx, ownerIdentityPubKey, privateEnabled, updateWalletSetting)
	if err != nil {
		logger.Error("failed to update wallet setting from gossip message", zap.Error(err))
		return err
	}

	logger.Sugar().Infof("Successfully updated wallet setting from gossip message for identity public key %x", ownerIdentityPubKey)
	return nil
}

func (h *GossipHandler) handleArchiveStaticDepositAddressGossipMessage(ctx context.Context, archiveStaticDepositAddress *pbgossip.GossipMessageArchiveStaticDepositAddress, forCoordinator bool) error {
	logger := logging.GetLoggerFromContext(ctx)

	if forCoordinator {
		return nil
	}

	// Parse coordinator public key
	coordinatorPubKey, err := keys.ParsePublicKey(archiveStaticDepositAddress.GetCoordinatorPublicKey())
	if err != nil {
		logger.Error("failed to parse coordinator public key", zap.Error(err))
		return fmt.Errorf("failed to parse coordinator public key: %w", err)
	}

	// Parse owner identity public key
	ownerIDPubKey, err := keys.ParsePublicKey(archiveStaticDepositAddress.GetOwnerIdentityPublicKey())
	if err != nil {
		logger.Error("failed to parse owner identity public key", zap.Error(err))
		return fmt.Errorf("failed to parse owner identity public key: %w", err)
	}

	network, err := btcnetwork.FromProtoNetwork(archiveStaticDepositAddress.GetNetwork())
	if err != nil {
		logger.Error("failed to parse network", zap.Error(err))
		return fmt.Errorf("failed to parse network: %w", err)
	}

	messageHash, err := CreateArchiveStaticDepositAddressStatement(ownerIDPubKey, network, archiveStaticDepositAddress.GetAddress())
	if err != nil {
		logger.Error("failed to create archive statement", zap.Error(err))
		return fmt.Errorf("failed to create archive statement: %w", err)
	}

	if err := common.VerifyECDSASignature(coordinatorPubKey, archiveStaticDepositAddress.GetSignature(), messageHash); err != nil {
		logger.Error("failed to verify coordinator signature", zap.Error(err))
		return fmt.Errorf("failed to verify coordinator signature: %w", err)
	}

	staticDepositHandler := NewStaticDepositInternalHandler(h.config)
	err = staticDepositHandler.ArchiveStaticDepositAddress(ctx, archiveStaticDepositAddress.GetOwnerIdentityPublicKey(), archiveStaticDepositAddress.GetNetwork(), archiveStaticDepositAddress.GetAddress())
	if err != nil {
		logger.Sugar().Errorf("failed to archive static deposit address from gossip message: %w", err)
		return err
	}

	logger.Sugar().Infof("Successfully archived static deposit address %s from gossip message for identity public key %x", archiveStaticDepositAddress.GetAddress(), ownerIDPubKey.Serialize())
	return nil
}

func (h *GossipHandler) handleFinalizeTransferReceiverGossipMessage(ctx context.Context, req *pbgossip.GossipMessageFinalizeTransferReceiver, forCoordinator bool) error {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Info("Handling finalize transfer receiver gossip message")

	if forCoordinator {
		return nil
	}

	transferHandler := NewInternalTransferHandler(h.config)
	err := transferHandler.FinalizeTransferReceiver(ctx, req)
	if err != nil {
		logger.Error("Failed to finalize transfer receiver", zap.Error(err))
	}
	return err
}

func (h *GossipHandler) handleFinalizeTreeNodeGossipMessage(
	ctx context.Context,
	msg *pbgossip.GossipMessageFinalizeTreeNode,
	forCoordinator bool,
) error {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Info("Handling finalize re-sign subtree gossip message")

	if forCoordinator {
		return nil
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get db from context: %w", err)
	}

	// Collect all node IDs and lock them before updating.
	nodeIDs := make([]uuid.UUID, 0, len(msg.GetNodes()))
	for _, node := range msg.GetNodes() {
		nodeID, err := uuid.Parse(node.GetId())
		if err != nil {
			return fmt.Errorf("invalid node id in gossip: %w", err)
		}
		nodeIDs = append(nodeIDs, nodeID)
	}
	_, err = db.TreeNode.Query().
		Where(treenode.IDIn(nodeIDs...)).
		ForUpdate().
		All(ctx)
	if err != nil {
		return fmt.Errorf("failed to lock nodes for gossip update: %w", err)
	}

	for _, node := range msg.GetNodes() {
		nodeID, _ := uuid.Parse(node.GetId())

		update := db.TreeNode.UpdateOneID(nodeID).
			SetRawTx(node.GetRawTx())

		// A leaf node has refund txs; only leaf nodes get status set to AVAILABLE.
		isLeaf := len(node.GetRawRefundTx()) > 0
		if isLeaf {
			update.SetRawRefundTx(node.GetRawRefundTx()).
				SetStatus(st.TreeNodeStatusAvailable)
		} else {
			update.ClearRawRefundTx()
		}
		if len(node.GetDirectTx()) > 0 {
			update.SetDirectTx(node.GetDirectTx())
		} else {
			update.ClearDirectTx()
		}
		if len(node.GetDirectRefundTx()) > 0 {
			update.SetDirectRefundTx(node.GetDirectRefundTx())
		} else {
			update.ClearDirectRefundTx()
		}
		if len(node.GetDirectFromCpfpRefundTx()) > 0 {
			update.SetDirectFromCpfpRefundTx(node.GetDirectFromCpfpRefundTx())
		} else {
			update.ClearDirectFromCpfpRefundTx()
		}

		_, err = update.Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to update node %s from gossip: %w", node.GetId(), err)
		}
	}

	return nil
}

// dispatchConsensusCommit routes an incoming ConsensusCommit gossip message to
// the appropriate FlowHandler.Commit based on operation type. After a
// successful dispatch, transitions the matching PARTICIPANT FlowExecution row
// to COMMITTED so the reconciliation task stops treating it as in-flight.
//
// codes.AlreadyExists from handler.Commit is treated as success: the
// underlying business state has already reached the target state (e.g.,
// because gossip was redelivered after a previous successful Commit, or
// because the original Commit landed via a path that didn't update the
// FlowExecution row), so the row should still be marked COMMITTED to
// stop the reconciliation task from looping on it.
func dispatchConsensusCommit(ctx context.Context, config *so.Config, opType pbgossip.ConsensusOperationType, flowExecutionID string, op proto.Message) error {
	handler, err := consensusFlowHandler(config, opType)
	if err != nil {
		return err
	}
	return runConsensusCommit(ctx, handler, opType, flowExecutionID, op)
}

type consensusOpDisposition int

const (
	dispositionUnknown consensusOpDisposition = iota
	applyOp
	skipForeignOp
	skipCoordinatorEcho
	skipAlreadyTerminal
	// skipMalformedFlowID: the gossip flow_execution_id is not a UUID, so no row
	// lookup is even possible. Distinct from skipForeignOp (row lookup found no
	// participant) so the logs/metric don't misdescribe malformed input as an
	// absent row.
	skipMalformedFlowID
)

// classifyConsensusOp returns the disposition for a gossip-delivered consensus op,
// along with the participant's own FlowExecution row when one was loaded (nil for
// the dormant empty-id branch). On error it fails closed (returns the error; the
// op is not applied) — recovery is via gossip retry for retriable codes, otherwise
// via the participant reconciler.
//
// A participant row that is already terminal yields skipAlreadyTerminal: the
// handler effect and the row's terminal transition commit in the same request
// transaction, so a terminal row proves this flow's effect was already applied
// and the handler must not run again. This fence is what makes redelivery safe
// for op payloads keyed on non-per-attempt identifiers (e.g. the static-deposit
// refund rollback keyed by on_chain_utxo): without it, a redelivered rollback
// whose own swap was already cancelled would find — and cancel — a newer
// attempt's active swap for the same UTXO.
func classifyConsensusOp(ctx context.Context, flowExecutionID string, opType pbgossip.ConsensusOperationType) (consensusOpDisposition, *ent.FlowExecution, error) {
	// Dormant pre-April-2026 leftover (#6288): every live coordinator populates
	// flow_execution_id, so this branch is unreachable today. It is kept (rather
	// than made an error) because a gossip handler error loops redelivery
	// forever, which is worse than applying unfenced for a message that could
	// only come from a pre-rollout binary. Paired with DispatchPrepare's
	// empty-flowExecutionID skip (consensus_handler.go) — remove both together.
	if flowExecutionID == "" {
		return applyOp, nil, nil
	}
	id, err := uuid.Parse(flowExecutionID)
	if err != nil {
		// A non-UUID flow_execution_id can only come from a buggy/misbehaving
		// coordinator or on-wire corruption, and can never parse on retry. Skip
		// (never dispatch) rather than error: erroring would loop gossip
		// redelivery forever on a message that can never become valid — the same
		// rationale as the op-type-mismatch branch below. Reported as its own
		// disposition (not skipForeignOp) so the caller doesn't log/label it as a
		// row lookup that found no participant.
		logging.GetLoggerFromContext(ctx).Sugar().Warnf(
			"consensus op fence: unparseable flow_execution_id %q in gossip; skipping handler: %v", flowExecutionID, err)
		return skipMalformedFlowID, nil, nil
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return dispositionUnknown, nil, err
	}
	// ForUpdate so a commit and a rollback for the same flow can't both read
	// IN_FLIGHT and both dispatch: without the lock, concurrent decisions each
	// classify as applyOp and markParticipantFlowExecutionTerminal's CAS then
	// silently no-ops the loser, leaving the row terminal in one state while
	// both handler effects ran. Flows whose commit advances a domain status
	// (e.g. send-transfer) are partly self-fenced by that status; the swap
	// primary commit leaves the transfer status unchanged, so the row lock is
	// the only serialization point.
	row, err := db.FlowExecution.Query().Where(flowexecution.ID(id)).ForUpdate().Only(ctx)
	switch {
	case ent.IsNotFound(err):
		return skipForeignOp, nil, nil
	case err != nil:
		return dispositionUnknown, nil, fmt.Errorf("unable to load flow execution %s: %w", id, err)
	case row.OpType != int32(opType):
		// The gossip envelope's op type picks the handler, but the row proves
		// which flow this SO actually prepared under this id — a mismatch means
		// a buggy or misbehaving coordinator is steering another flow's decision
		// into a different handler. Skip (never dispatch) rather than error:
		// erroring would loop redelivery on a message that can never become
		// valid, and the real flow's decision still arrives under its own type.
		logging.GetLoggerFromContext(ctx).Sugar().Warnf(
			"consensus op fence: gossip op_type %d does not match FlowExecution row %s op_type %d; skipping handler",
			opType, flowExecutionID, row.OpType)
		return skipForeignOp, nil, nil
	case row.Role == st.FlowExecutionRoleParticipant:
		if row.Status != st.FlowExecutionStatusInFlight {
			logging.GetLoggerFromContext(ctx).Sugar().Infof(
				"consensus op fence: participant FlowExecution row %s already terminal (%s); skipping handler",
				flowExecutionID, row.Status)
			return skipAlreadyTerminal, nil, nil
		}
		return applyOp, row, nil
	default: // FlowExecutionRoleCoordinator
		return skipCoordinatorEcho, nil, nil
	}
}

// validateDecisionAgainstPreparedOp enforces PrepareBoundFlowHandler binding
// for a gossip-delivered decision: when the handler implements the interface
// and this SO persisted a prepare payload for the flow, the decision payload
// must be consistent with it. Returns (false, nil) when the decision must be
// skipped — the participant row stays IN_FLIGHT so the flow's real decision
// can still arrive; skipping rather than erroring avoids looping gossip
// redelivery on a message that can never become valid (same rationale as the
// op-type fence in classifyConsensusOp).
func validateDecisionAgainstPreparedOp(ctx context.Context, handler consensus.FlowHandler, row *ent.FlowExecution, op proto.Message, phase string) bool {
	var validate func(proto.Message, proto.Message) error
	switch bound := handler.(type) {
	case consensus.ContextPrepareBoundFlowHandler:
		validate = func(prepareOp, decisionOp proto.Message) error {
			return bound.ValidateDecisionAgainstPrepare(ctx, prepareOp, decisionOp)
		}
	case consensus.PrepareBoundFlowHandler:
		validate = bound.ValidateDecisionAgainstPrepare
	default:
		// The flow does not opt into payload binding — dispatch unchanged.
		return true
	}
	if row == nil {
		// classifyConsensusOp returns applyOp with a nil row only for the
		// dormant empty-flow_execution_id branch. For a bound flow this is a
		// fence bypass: an empty id skips the row lookup, op-type fence,
		// terminal-state check, and payload binding before reaching the
		// handler. Every flow that opts into binding is post-April-2026 (swap
		// v3 is a new op type; consensus SEND_TRANSFER is knob-gated and
		// post-rollout), so a live coordinator always populates the id — an
		// empty id on a bound op can only be a forged or misrouted decision.
		// Fail closed. (Non-bound legacy flows returned earlier and keep the
		// pre-rollout compatibility fall-through.)
		logging.GetLoggerFromContext(ctx).Sugar().Errorf(
			"consensus op fence: %s decision for a bound flow arrived with no flow_execution_id; refusing to bypass the fences", phase)
		consensusOpFencedTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("phase", phase), attribute.String("disposition", "missing_flow_execution_id")))
		return false
	}
	if row.PreparePayload == nil || len(*row.PreparePayload) == 0 {
		// A participant row for a bound flow always carries its prepare payload:
		// DispatchPrepare persists it in the same transaction as the prepare
		// work (consensus_handler.go). An empty payload is therefore an anomaly
		// — a corrupt or hand-mutated row — and we have nothing to bind the
		// decision to. Fail closed: refuse to dispatch an unbound decision
		// rather than treat the missing payload as a passing check. The row
		// stays IN_FLIGHT and the metric surfaces it for operator attention.
		logging.GetLoggerFromContext(ctx).Sugar().Errorf(
			"consensus op fence: %s for flow %s has no persisted prepare payload; refusing to dispatch an unbound decision",
			phase, row.ID)
		consensusOpFencedTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("phase", phase), attribute.String("disposition", "missing_prepare_payload")))
		return false
	}
	var prepareAny anypb.Any
	if err := proto.Unmarshal(*row.PreparePayload, &prepareAny); err != nil {
		// A persisted payload that no longer decodes is a corrupt or hand-mutated
		// row — unrecoverable by retry, exactly like the missing-payload case
		// above. Skip (stay IN_FLIGHT) with a metric rather than error: returning
		// an error would loop gossip redelivery forever on a message that can
		// never become valid.
		logging.GetLoggerFromContext(ctx).Sugar().Errorf(
			"consensus op fence: %s for flow %s has an undecodable persisted prepare payload; refusing to dispatch an unbound decision: %v",
			phase, row.ID, err)
		consensusOpFencedTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("phase", phase), attribute.String("disposition", "corrupt_prepare_payload")))
		return false
	}
	prepareOp, err := prepareAny.UnmarshalNew()
	if err != nil {
		logging.GetLoggerFromContext(ctx).Sugar().Errorf(
			"consensus op fence: %s for flow %s has an undecodable persisted prepare op; refusing to dispatch an unbound decision: %v",
			phase, row.ID, err)
		consensusOpFencedTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("phase", phase), attribute.String("disposition", "corrupt_prepare_payload")))
		return false
	}
	if err := validate(prepareOp, op); err != nil {
		// A mismatch means the decision payload names resources this SO did not
		// prepare under this flow_execution_id. This is reachable only from a
		// forged or misrouted gossip decision — never the authoritative one: the
		// decision the reconciler replays via ConsensusQueryOutcome is the
		// coordinator's own decision_payload, which it built from the same
		// request it fanned out as the prepare op, so it is by construction
		// consistent with what this SO persisted. Skipping the forged decision
		// leaves the row IN_FLIGHT so the real (matching) decision — or the
		// reconciler's replay of it — can still land. A persistent mismatch
		// would require a coordinator bug producing an authoritative decision
		// that disagrees with its own prepare op; in that case keeping the row
		// IN_FLIGHT with its resources locked is the strictly safe outcome
		// versus applying a decision to the wrong resource, and the
		// payload_mismatch metric fires so on-call can quarantine it. (The
		// presumed-abort reconciler path is the automatic backstop whenever the
		// coordinator has no authoritative record at all.)
		logging.GetLoggerFromContext(ctx).Sugar().Warnf(
			"consensus op fence: %s payload for flow %s does not match the prepare op this SO persisted; skipping handler: %v",
			phase, row.ID, err)
		consensusOpFencedTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("phase", phase), attribute.String("disposition", "payload_mismatch")))
		return false
	}
	return true
}

func consensusDispatchContext(ctx context.Context, flowExecutionID string) context.Context {
	id, err := uuid.Parse(flowExecutionID)
	if err != nil {
		return ctx
	}
	return consensus.ContextWithFlowExecutionID(ctx, id)
}

// runConsensusCommit centralizes the post-handler logic so the
// AlreadyExists-as-success rule is testable independently of the
// production opType→handler mapping. handler is supplied by the caller.
func runConsensusCommit(ctx context.Context, handler consensus.FlowHandler, opType pbgossip.ConsensusOperationType, flowExecutionID string, op proto.Message) error {
	disposition, row, err := classifyConsensusOp(ctx, flowExecutionID, opType)
	if err != nil {
		return err
	}
	switch disposition {
	case skipForeignOp:
		logging.GetLoggerFromContext(ctx).Sugar().Warnf(
			"consensus commit: no participant FlowExecution row for flow %s (op_type %d); skipping foreign commit",
			flowExecutionID, opType)
		consensusOpFencedTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("phase", "commit"), attribute.String("disposition", "foreign")))
		return nil
	case skipMalformedFlowID:
		consensusOpFencedTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("phase", "commit"), attribute.String("disposition", "malformed_flow_execution_id")))
		return nil
	case skipCoordinatorEcho:
		return nil
	case skipAlreadyTerminal:
		consensusOpFencedTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("phase", "commit"), attribute.String("disposition", "already_terminal")))
		return nil
	case applyOp:
	default:
		return fmt.Errorf("unexpected consensus op disposition %d for flow %s", disposition, flowExecutionID)
	}
	ctx = consensusDispatchContext(ctx, flowExecutionID)
	if !validateDecisionAgainstPreparedOp(ctx, handler, row, op, "commit") {
		// Fenced (mismatch / missing / corrupt prepare payload): skip without
		// erroring so gossip redelivery doesn't loop; the row stays IN_FLIGHT.
		return nil
	}
	if err := handler.Commit(ctx, op); err != nil {
		if status.Code(err) != codes.AlreadyExists {
			return err
		}
		logging.GetLoggerFromContext(ctx).Sugar().Infof(
			"consensus commit gossip: handler reports AlreadyExists for flow %s op_type %d — treating as success and marking participant row COMMITTED: %v",
			flowExecutionID, opType, err)
	}
	return markParticipantFlowExecutionTerminal(ctx, flowExecutionID, st.FlowExecutionStatusCommitted)
}

// dispatchConsensusRollback routes an incoming ConsensusRollback gossip message
// to the appropriate FlowHandler.Rollback based on operation type. After a
// successful dispatch, transitions the matching PARTICIPANT FlowExecution row
// to ROLLED_BACK.
//
// codes.AlreadyExists is treated as success for the same reason as in
// dispatchConsensusCommit: the rollback effect is already in place (or
// indistinguishable from never-prepared), so mark the row terminal.
func dispatchConsensusRollback(ctx context.Context, config *so.Config, opType pbgossip.ConsensusOperationType, flowExecutionID string, op proto.Message) error {
	handler, err := consensusFlowHandler(config, opType)
	if err != nil {
		return err
	}
	// Attach the coordinator identity of THIS flow to ctx from the participant's
	// own FlowExecution row (its coordinator_index, recorded at prepare from the
	// authenticated ConsensusPrepare — not from the rollback payload), mirroring
	// DispatchPrepare. Rollback handlers that scope their lookup by coordinator
	// identity (e.g. the instant static deposit reserve, which resolves a
	// coordinator-supplied transfer id against local rows) use this to ensure a
	// coordinator can only roll back reservations IT coordinated — a coordinator
	// driving its own flow's rollback cannot redirect the cancellation at a
	// reservation coordinated by a different SO. Best-effort: if the row/index
	// can't be resolved the ctx value is simply absent and such handlers fall
	// back to their existing scoping.
	ctx = withRollbackCoordinatorIdentity(ctx, config, flowExecutionID)
	return runConsensusRollback(ctx, handler, opType, flowExecutionID, op)
}

// withRollbackCoordinatorIdentity resolves the coordinator identity for a
// rollback from the local participant FlowExecution row and attaches it to ctx.
// Returns ctx unchanged if the row is absent, is this SO's own coordinator row,
// or the index doesn't resolve — callers must tolerate a missing value.
func withRollbackCoordinatorIdentity(ctx context.Context, config *so.Config, flowExecutionID string) context.Context {
	if flowExecutionID == "" {
		return ctx
	}
	id, err := uuid.Parse(flowExecutionID)
	if err != nil {
		return ctx
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return ctx
	}
	row, err := db.FlowExecution.Get(ctx, id)
	if err != nil || row.Role != st.FlowExecutionRoleParticipant {
		return ctx
	}
	coordinator, err := config.GetOperatorByID(uint64(row.CoordinatorIndex))
	if err != nil {
		return ctx
	}
	return consensus.WithCoordinatorIdentity(ctx, coordinator.IdentityPublicKey)
}

// runConsensusRollback mirrors runConsensusCommit for the rollback path.
func runConsensusRollback(ctx context.Context, handler consensus.FlowHandler, opType pbgossip.ConsensusOperationType, flowExecutionID string, op proto.Message) error {
	disposition, row, err := classifyConsensusOp(ctx, flowExecutionID, opType)
	if err != nil {
		return err
	}
	switch disposition {
	case skipForeignOp:
		logging.GetLoggerFromContext(ctx).Sugar().Warnf(
			"consensus rollback: no participant FlowExecution row for flow %s (op_type %d); skipping foreign rollback",
			flowExecutionID, opType)
		consensusOpFencedTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("phase", "rollback"), attribute.String("disposition", "foreign")))
		return nil
	case skipMalformedFlowID:
		consensusOpFencedTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("phase", "rollback"), attribute.String("disposition", "malformed_flow_execution_id")))
		return nil
	case skipCoordinatorEcho:
		return nil
	case skipAlreadyTerminal:
		consensusOpFencedTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("phase", "rollback"), attribute.String("disposition", "already_terminal")))
		return nil
	case applyOp:
	default:
		return fmt.Errorf("unexpected consensus op disposition %d for flow %s", disposition, flowExecutionID)
	}
	ctx = consensusDispatchContext(ctx, flowExecutionID)
	if !validateDecisionAgainstPreparedOp(ctx, handler, row, op, "rollback") {
		// Fenced (mismatch / missing / corrupt prepare payload): skip without
		// erroring so gossip redelivery doesn't loop; the row stays IN_FLIGHT.
		return nil
	}
	if err := handler.Rollback(ctx, op); err != nil {
		if status.Code(err) != codes.AlreadyExists {
			return err
		}
		logging.GetLoggerFromContext(ctx).Sugar().Infof(
			"consensus rollback gossip: handler reports AlreadyExists for flow %s op_type %d — treating as success and marking participant row ROLLED_BACK: %v",
			flowExecutionID, opType, err)
	}
	return markParticipantFlowExecutionTerminal(ctx, flowExecutionID, st.FlowExecutionStatusRolledBack)
}

// markParticipantFlowExecutionTerminal transitions a PARTICIPANT FlowExecution
// row to the target terminal status. Missing rows and already-terminal rows
// are idempotent no-ops so gossip redelivery (which is common) stays safe.
// Empty flowExecutionID means the gossip came from a pre-upgrade coordinator
// and there is no participant row to transition.
func markParticipantFlowExecutionTerminal(ctx context.Context, flowExecutionID string, target st.FlowExecutionStatus) error {
	if flowExecutionID == "" {
		return nil
	}
	id, err := uuid.Parse(flowExecutionID)
	if err != nil {
		return fmt.Errorf("invalid flow_execution_id in gossip: %w", err)
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return err
	}
	_, err = db.FlowExecution.Update().
		Where(
			flowexecution.ID(id),
			flowexecution.RoleEQ(st.FlowExecutionRoleParticipant),
			flowexecution.StatusEQ(st.FlowExecutionStatusInFlight),
		).
		SetStatus(target).
		Save(ctx)
	return err
}
