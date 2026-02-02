package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/common/uuids"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/preimagerequest"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttree "github.com/lightsparkdev/spark/so/ent/tree"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"go.uber.org/zap"
	"google.golang.org/grpc/status"
)

var gossipMessageHandledTotal metric.Int64Counter

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
	logger.Sugar().Infof("Handling gossip message with ID %s", gossipMessage.MessageId)

	messageType := getGossipMessageType(gossipMessage)

	var err error
	switch gossipMessage.Message.(type) {
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
	case *pbgossip.GossipMessage_DepositCleanup:
		depositCleanup := gossipMessage.GetDepositCleanup()
		err = h.handleDepositCleanupGossipMessage(ctx, depositCleanup)
	case *pbgossip.GossipMessage_Preimage:
		preimage := gossipMessage.GetPreimage()
		err = h.handlePreimageGossipMessage(ctx, preimage, forCoordinator)
	case *pbgossip.GossipMessage_SettleSwapKeyTweak:
		settleSwapKeyTweak := gossipMessage.GetSettleSwapKeyTweak()
		err = h.handleSettleSwapKeyTweakGossipMessage(ctx, settleSwapKeyTweak)
	case *pbgossip.GossipMessage_FinalizeRefreshTimelock:
		err = fmt.Errorf("gossip message has been deprecated: %T", gossipMessage.Message)
	case *pbgossip.GossipMessage_FinalizeExtendLeaf:
		err = fmt.Errorf("gossip message has been deprecated: %T", gossipMessage.Message)
	case *pbgossip.GossipMessage_ArchiveStaticDepositAddress:
		archiveStaticDepositAddress := gossipMessage.GetArchiveStaticDepositAddress()
		err = h.handleArchiveStaticDepositAddressGossipMessage(ctx, archiveStaticDepositAddress)
	default:
		err = fmt.Errorf("unsupported gossip message type: %T", gossipMessage.Message)
	}

	if err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Handling for gossip message ID %s failed with error: %v", gossipMessage.MessageId, err)
	}

	// Record metric
	statusCode := status.Code(err)
	gossipMessageHandledTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("message_type", messageType),
		semconv.RPCGRPCStatusCodeKey.Int(int(statusCode)),
	))

	return err
}

func getGossipMessageType(msg *pbgossip.GossipMessage) string {
	if msg.Message == nil {
		return "unknown"
	}
	// Return the raw protobuf type name, e.g., "*gossip.GossipMessage_CancelTransfer"
	return fmt.Sprintf("%T", msg.Message)
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
	_, err = transferHandler.CommitSenderKeyTweaks(ctx, transferID, settleSenderKeyTweak.SenderKeyTweakProofs)
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
	if err := baseHandler.RollbackTransfer(ctx, transferID); err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to rollback transfer %s", transferID)
	}
	return err
}

func (h *GossipHandler) handleMarkTreesExited(ctx context.Context, req *pbgossip.GossipMessageMarkTreesExited) error {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Sugar().Infof("Handling mark trees exited gossip message for trees %+q", req.TreeIds)

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

	treeExitHandler := NewTreeExitHandler(h.config)
	if err := treeExitHandler.MarkTreesExited(ctx, trees); err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to mark trees %+q exited", req.TreeIds)
	}
	return err
}

func (h *GossipHandler) handleDepositCleanupGossipMessage(ctx context.Context, req *pbgossip.GossipMessageDepositCleanup) error {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Sugar().Infof("Handling deposit cleanup gossip message for tree %s", req.TreeId)

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
	err := depositHandler.FinalizeTreeCreation(ctx, &pbinternal.FinalizeTreeCreationRequest{Nodes: finalizeNodeSignatures.InternalNodes, Network: finalizeNodeSignatures.ProtoNetwork})
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
	err := transferHandler.FinalizeTransfer(ctx, &pbinternal.FinalizeTransferRequest{TransferId: finalizeNodeSignatures.TransferId, Nodes: finalizeNodeSignatures.InternalNodes, Timestamp: finalizeNodeSignatures.CompletionTimestamp})
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
		SplitNode: finalizeRenewNodeTimelock.SplitNode,
		Node:      finalizeRenewNodeTimelock.Node,
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
		Node: finalizeRenewRefundTimelock.Node,
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
		OnChainUtxo:          rollbackUtxoSwap.OnChainUtxo,
		Signature:            rollbackUtxoSwap.Signature,
		CoordinatorPublicKey: rollbackUtxoSwap.CoordinatorPublicKey,
	})
	if err != nil {
		logger.Error("failed to rollback utxo swap with gossip message", zap.Error(err))
	}
	return err
}

func (h *GossipHandler) handlePreimageGossipMessage(ctx context.Context, gossip *pbgossip.GossipMessagePreimage, forCoordinator bool) error {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Info("Handling preimage gossip message")

	if forCoordinator {
		return nil
	}

	calculatedHash := sha256.Sum256(gossip.Preimage)
	if !bytes.Equal(calculatedHash[:], gossip.PaymentHash) {
		err := fmt.Errorf("preimage hash mismatch (expected %x, got %x)", calculatedHash[:], gossip.PaymentHash)
		logger.Error(err.Error())
		return err
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		logger.Error("Failed to get or create current tx for request", zap.Error(err))
		return err
	}

	preimageRequests, err := db.PreimageRequest.Query().Where(preimagerequest.PaymentHashEQ(gossip.PaymentHash)).ForUpdate().All(ctx)
	if err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to get preimage request for %x", gossip.PaymentHash)
		return err
	}

	for _, preimageRequest := range preimageRequests {
		_, err = preimageRequest.Update().SetPreimage(gossip.Preimage).Save(ctx)
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf("Failed to update preimage request for %x", gossip.PaymentHash)
			return err
		}
	}
	return nil
}

func (h *GossipHandler) handleSettleSwapKeyTweakGossipMessage(ctx context.Context, settleSwapKeyTweak *pbgossip.GossipMessageSettleSwapKeyTweak) error {
	transferHandler := NewBaseTransferHandler(h.config)
	id, err := uuid.Parse(settleSwapKeyTweak.GetCounterTransferId())
	if err != nil {
		return fmt.Errorf("invalid counter transfer id: %w", err)
	}
	err = transferHandler.CommitSwapKeyTweaks(ctx, id)
	if err != nil {
		logger := logging.GetLoggerFromContext(ctx)
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to settle swap key tweak for counter transfer %s", id)
	}
	return err
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
	}
	logger.Sugar().Infof("Handling wallet setting update gossip message for identity public key %s", ownerIdentityPubKey)

	walletSettingHandler := NewWalletSettingHandler(h.config)
	_, err = walletSettingHandler.UpdateWalletSettingInternal(ctx, ownerIdentityPubKey, updateWalletSetting.PrivateEnabled, updateWalletSetting)
	if err != nil {
		logger.Error("failed to update wallet setting from gossip message", zap.Error(err))
		return err
	}

	logger.Sugar().Infof("Successfully updated wallet setting from gossip message for identity public key %x", ownerIdentityPubKey)
	return nil
}

func (h *GossipHandler) handleArchiveStaticDepositAddressGossipMessage(ctx context.Context, archiveStaticDepositAddress *pbgossip.GossipMessageArchiveStaticDepositAddress) error {
	logger := logging.GetLoggerFromContext(ctx)

	// Parse coordinator public key
	coordinatorPubKey, err := keys.ParsePublicKey(archiveStaticDepositAddress.CoordinatorPublicKey)
	if err != nil {
		logger.Error("failed to parse coordinator public key", zap.Error(err))
		return fmt.Errorf("failed to parse coordinator public key: %w", err)
	}

	// Parse owner identity public key
	ownerIDPubKey, err := keys.ParsePublicKey(archiveStaticDepositAddress.OwnerIdentityPublicKey)
	if err != nil {
		logger.Error("failed to parse owner identity public key", zap.Error(err))
		return fmt.Errorf("failed to parse owner identity public key: %w", err)
	}

	network, err := btcnetwork.FromProtoNetwork(archiveStaticDepositAddress.Network)
	if err != nil {
		logger.Error("failed to parse network", zap.Error(err))
		return fmt.Errorf("failed to parse network: %w", err)
	}

	messageHash, err := CreateArchiveStaticDepositAddressStatement(ownerIDPubKey, network, archiveStaticDepositAddress.Address)
	if err != nil {
		logger.Error("failed to create archive statement", zap.Error(err))
		return fmt.Errorf("failed to create archive statement: %w", err)
	}

	if err := common.VerifyECDSASignature(coordinatorPubKey, archiveStaticDepositAddress.Signature, messageHash); err != nil {
		logger.Error("failed to verify coordinator signature", zap.Error(err))
		return fmt.Errorf("failed to verify coordinator signature: %w", err)
	}

	staticDepositHandler := NewStaticDepositInternalHandler(h.config)
	err = staticDepositHandler.ArchiveStaticDepositAddress(ctx, archiveStaticDepositAddress.OwnerIdentityPublicKey, archiveStaticDepositAddress.Network, archiveStaticDepositAddress.Address)
	if err != nil {
		logger.Sugar().Errorf("failed to archive static deposit address from gossip message: %w", err)
		return err
	}

	logger.Sugar().Infof("Successfully archived static deposit address %s from gossip message for identity public key %x", archiveStaticDepositAddress.Address, ownerIDPubKey.Serialize())
	return nil
}
