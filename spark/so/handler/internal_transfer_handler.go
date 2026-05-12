package handler

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"

	"time"

	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/common/uuids"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	enttransferleaf "github.com/lightsparkdev/spark/so/ent/transferleaf"
	enttransferreceiver "github.com/lightsparkdev/spark/so/ent/transferreceiver"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// InternalTransferHandler is the transfer handler for so internal
type InternalTransferHandler struct {
	BaseTransferHandler
	config *so.Config
}

// NewInternalTransferHandler creates a new InternalTransferHandler.
func NewInternalTransferHandler(config *so.Config) *InternalTransferHandler {
	return &InternalTransferHandler{BaseTransferHandler: NewBaseTransferHandler(config), config: config}
}

// FinalizeTransfer finalizes a transfer.
func (h *InternalTransferHandler) FinalizeTransfer(ctx context.Context, req *pbinternal.FinalizeTransferRequest) error {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return fmt.Errorf("failed to parse transfer id: %w", err)
	}
	transfer, err := h.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		return fmt.Errorf("unable to load transfer %s: %w", transferID, err)
	}

	if err := checkCoopExitTxBroadcasted(ctx, db, transfer); err != nil {
		return fmt.Errorf("failed to unlock transfer id: %s. with status: %s and error: %w", transferID, transfer.Status, err)
	}

	transferNodes, err := transfer.QueryTransferLeaves().QueryLeaf().All(ctx)
	if err != nil {
		return fmt.Errorf("failed to query transfer leaves for transfer id: %s. with status: %s and error: %w", transferID, transfer.Status, err)
	}
	if len(transferNodes) != len(req.Nodes) {
		return fmt.Errorf("transfer nodes count mismatch. transfer id: %s. with status: %s. transfer nodes count: %d. request nodes count: %d", transferID, transfer.Status, len(transferNodes), len(req.Nodes))
	}
	transferNodeIDs := make(map[uuid.UUID]struct{})
	for _, node := range transferNodes {
		transferNodeIDs[node.ID] = struct{}{}
	}

	for _, node := range req.Nodes {
		nodeID, err := uuid.Parse(node.Id)
		if err != nil {
			return fmt.Errorf("failed to parse node uuid. transfer id: %s. with status: %s. node id: %s", transferID, transfer.Status, node.Id)
		}
		if _, ok := transferNodeIDs[nodeID]; !ok {
			return fmt.Errorf("node not found in transfer. transfer id: %s. with status: %s. node id: %s", transferID, transfer.Status, nodeID)
		}
		dbNode, err := db.TreeNode.Get(ctx, nodeID)
		if err != nil {
			return fmt.Errorf("failed to get dbNode. transfer id: %s. with status: %s. node id: %s and error: %w", transferID, transfer.Status, nodeID, err)
		}

		if transfer.Status == st.TransferStatusCompleted {
			// Verify that the transfer details are the same between both nodes.
			// RawTx is signed once at tree creation and never re-signed; its
			// witnesses should be byte-identical across gossip deliveries, so
			// strict compareTxs is appropriate. Refund txs (RawRefundTx,
			// DirectRefundTx, DirectFromCpfpRefundTx) are re-signed on each
			// transfer and use compareAndVerifyTxs to accept different-but-valid
			// FROST signatures from separate signing sessions.
			rawTxMatch, err := compareTxs(dbNode.RawTx, node.RawTx)
			if err != nil {
				return fmt.Errorf("failed to compare raw txs: %w", err)
			}

			// Parse prevout txs needed for signature verification on refund txs.
			nodeRawTx, err := common.TxFromRawTxBytes(dbNode.RawTx)
			if err != nil {
				return fmt.Errorf("failed to parse node raw tx for node %s: %w", nodeID, err)
			}
			if len(nodeRawTx.TxOut) == 0 {
				return fmt.Errorf("node raw tx for node %s has no outputs", nodeID)
			}
			var directNodeTxOut *wire.TxOut
			if len(dbNode.DirectTx) > 0 {
				directNodeTx, err := common.TxFromRawTxBytes(dbNode.DirectTx)
				if err != nil {
					return fmt.Errorf("failed to parse direct node tx for node %s: %w", nodeID, err)
				}
				if len(directNodeTx.TxOut) == 0 {
					return fmt.Errorf("direct node tx for node %s has no outputs", nodeID)
				}
				directNodeTxOut = directNodeTx.TxOut[0]
			}

			rawRefundTxMatch, err := compareAndVerifyTxs(dbNode.RawRefundTx, node.RawRefundTx, nodeRawTx.TxOut[0])
			if err != nil {
				return fmt.Errorf("failed to compare raw refund txs for node %s: %w", nodeID, err)
			}
			directRefundTxMatch, err := compareAndVerifyTxs(dbNode.DirectRefundTx, node.DirectRefundTx, directNodeTxOut)
			if err != nil {
				return fmt.Errorf("failed to compare direct refund txs: %w", err)
			}
			directFromCpfpRefundTxMatch, err := compareAndVerifyTxs(dbNode.DirectFromCpfpRefundTx, node.DirectFromCpfpRefundTx, nodeRawTx.TxOut[0])
			if err != nil {
				return fmt.Errorf("failed to compare direct from cpfp refund txs: %w", err)
			}

			if !rawTxMatch || !rawRefundTxMatch || !directRefundTxMatch || !directFromCpfpRefundTxMatch {
				return fmt.Errorf("node is not the same as the one in the DB or maybe refundTX not matching. transfer id: %s. with status: %s. node id: %s", transferID, transfer.Status, nodeID)
			}

			// Synchronize any non-nil tx fields.
			update := dbNode.Update()

			update.SetRawTx(node.RawTx) // RawTx is required field, can't be nil
			if node.RawRefundTx != nil {
				update.SetRawRefundTx(node.RawRefundTx)
			}

			// The old direct transactions don't apply to the new owner,
			// so overwrite them even if new direct transactions aren't provided.
			update.SetDirectRefundTx(node.DirectRefundTx)
			update.SetDirectFromCpfpRefundTx(node.DirectFromCpfpRefundTx)
			update.SetStatus(st.TreeNodeStatusAvailable)

			if _, err = update.Save(ctx); err != nil {
				return fmt.Errorf("failed to update dbNode. transfer id: %s. with status: %s. node id: %s and error: %w", transferID, transfer.Status, nodeID, err)
			}
		} else {
			_, err = dbNode.Update().
				SetRawTx(node.RawTx).
				SetRawRefundTx(node.RawRefundTx).
				SetDirectRefundTx(node.DirectRefundTx).
				SetDirectFromCpfpRefundTx(node.DirectFromCpfpRefundTx).
				SetStatus(st.TreeNodeStatusAvailable).
				Save(ctx)
			if err != nil {
				return fmt.Errorf("failed to update dbNode. transfer id: %s. with status: %s. node id: %s and error: %w", transferID, transfer.Status, nodeID, err)
			}

			_, err = transfer.Update().SetStatus(st.TransferStatusCompleted).SetCompletionTime(req.Timestamp.AsTime()).Save(ctx)
			if err != nil {
				return fmt.Errorf("failed to update transfer status to completed for transfer id: %s. with status: %s and error: %w", transferID, transfer.Status, err)
			}
		}
	}

	if err := syncReceiversToTerminalStatus(ctx, transfer.ID, st.TransferStatusCompleted, req.Timestamp.AsTime()); err != nil {
		return fmt.Errorf("failed to sync receiver statuses for transfer %s: %w", transferID, err)
	}

	return nil
}

// FinalizeTransferReceiver processes a per-receiver gossip message for MIMO transfers.
// It marks the receiver's tree nodes as Available and the receiver as Completed.
// When all receivers for a transfer are Completed, it marks the transfer itself as Completed.
func (h *InternalTransferHandler) FinalizeTransferReceiver(ctx context.Context, req *pbgossip.GossipMessageFinalizeTransferReceiver) error {
	logger := logging.GetLoggerFromContext(ctx)

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get db: %w", err)
	}

	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return fmt.Errorf("failed to parse transfer id: %w", err)
	}

	transfer, err := h.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		return fmt.Errorf("unable to load transfer %s: %w", transferID, err)
	}

	if err := validateTransferReadyForReceiverClaim(transfer); err != nil {
		return err
	}

	receiverPubKey, err := keys.ParsePublicKey(req.GetReceiverIdentityPublicKey())
	if err != nil {
		return fmt.Errorf("failed to parse receiver identity public key: %w", err)
	}

	receivers, err := transfer.QueryTransferReceivers().
		Where(enttransferreceiver.IdentityPubkeyEQ(receiverPubKey)).
		ForUpdate().
		All(ctx)
	if err != nil {
		return fmt.Errorf("failed to query receiver for transfer %s: %w", transferID, err)
	}
	if len(receivers) != 1 {
		return fmt.Errorf("expected exactly 1 receiver with pubkey %x for transfer %s, got %d", receiverPubKey.Serialize(), transferID, len(receivers))
	}
	receiver := receivers[0]

	// Idempotency: if this receiver is already completed, exit early.
	// Also mark the transfer completed if all receivers are now done.
	if receiver.Status == st.TransferReceiverStatusCompleted {
		if transfer.Status != st.TransferStatusCompleted {
			pendingCount, err := transfer.QueryTransferReceivers().
				Where(enttransferreceiver.StatusNEQ(st.TransferReceiverStatusCompleted)).
				Count(ctx)
			if err != nil {
				return fmt.Errorf("failed to count pending receivers for transfer %s: %w", transferID, err)
			}
			if pendingCount == 0 {
				_, err = transfer.Update().
					SetStatus(st.TransferStatusCompleted).
					SetCompletionTime(req.CompletionTimestamp.AsTime()).
					Save(ctx)
				if err != nil {
					return fmt.Errorf("failed to mark transfer completed for %s: %w", transferID, err)
				}
				logger.With(zap.String("transfer_id", transferID.String())).
					Info("Promoted transfer to completed, all receivers done")
			}
		}
		logger.With(zap.String("transfer_id", transferID.String())).
			Info("Receiver already completed, accepting gossip idempotently")
		return nil
	}

	receiverLeaves, err := db.TransferLeaf.Query().
		Where(enttransferleaf.TransferReceiverID(receiver.ID)).
		QueryLeaf().
		All(ctx)
	if err != nil {
		return fmt.Errorf("failed to query receiver leaves for transfer %s: %w", transferID, err)
	}
	if len(receiverLeaves) != len(req.InternalNodes) {
		return fmt.Errorf("node count mismatch for receiver in transfer %s: db has %d, gossip has %d",
			transferID, len(receiverLeaves), len(req.InternalNodes))
	}

	receiverLeafIDs := make(map[uuid.UUID]struct{})
	for _, leaf := range receiverLeaves {
		receiverLeafIDs[leaf.ID] = struct{}{}
	}

	for _, node := range req.InternalNodes {
		nodeID, err := uuid.Parse(node.Id)
		if err != nil {
			return fmt.Errorf("failed to parse node id %s: %w", node.Id, err)
		}
		if _, ok := receiverLeafIDs[nodeID]; !ok {
			return fmt.Errorf("node %s not in receiver's leaves (or duplicate) for transfer %s", nodeID, transferID)
		}
		delete(receiverLeafIDs, nodeID)
		dbNode, err := db.TreeNode.Get(ctx, nodeID)
		if err != nil {
			return fmt.Errorf("failed to get tree node %s: %w", nodeID, err)
		}

		if dbNode.Status == st.TreeNodeStatusAvailable {
			// Idempotency: node was already made Available (e.g. by safety net). Verify txs match.
			// RawTx is signed once at tree creation and never re-signed; its
			// witnesses should be byte-identical across gossip deliveries, so
			// strict compareTxs is appropriate. Refund txs (RawRefundTx,
			// DirectRefundTx, DirectFromCpfpRefundTx) are re-signed on each
			// transfer and use compareAndVerifyTxs to accept different-but-valid
			// FROST signatures from separate signing sessions.
			rawTxMatch, err := compareTxs(dbNode.RawTx, node.RawTx)
			if err != nil {
				return fmt.Errorf("failed to compare raw txs for node %s: %w", nodeID, err)
			}

			// Parse prevout txs needed for signature verification on refund txs.
			nodeRawTx, err := common.TxFromRawTxBytes(dbNode.RawTx)
			if err != nil {
				return fmt.Errorf("failed to parse node raw tx for node %s: %w", nodeID, err)
			}
			if len(nodeRawTx.TxOut) == 0 {
				return fmt.Errorf("node raw tx for node %s has no outputs", nodeID)
			}
			var directNodeTxOut *wire.TxOut
			if len(dbNode.DirectTx) > 0 {
				directNodeTx, err := common.TxFromRawTxBytes(dbNode.DirectTx)
				if err != nil {
					return fmt.Errorf("failed to parse direct node tx for node %s: %w", nodeID, err)
				}
				if len(directNodeTx.TxOut) == 0 {
					return fmt.Errorf("direct node tx for node %s has no outputs", nodeID)
				}
				directNodeTxOut = directNodeTx.TxOut[0]
			}

			rawRefundTxMatch, err := compareAndVerifyTxs(dbNode.RawRefundTx, node.RawRefundTx, nodeRawTx.TxOut[0])
			if err != nil {
				return fmt.Errorf("failed to compare raw refund txs for node %s: %w", nodeID, err)
			}
			directRefundTxMatch, err := compareAndVerifyTxs(dbNode.DirectRefundTx, node.DirectRefundTx, directNodeTxOut)
			if err != nil {
				return fmt.Errorf("failed to compare direct refund txs for node %s: %w", nodeID, err)
			}
			directFromCpfpRefundTxMatch, err := compareAndVerifyTxs(dbNode.DirectFromCpfpRefundTx, node.DirectFromCpfpRefundTx, nodeRawTx.TxOut[0])
			if err != nil {
				return fmt.Errorf("failed to compare direct from cpfp refund txs for node %s: %w", nodeID, err)
			}

			if !rawTxMatch || !rawRefundTxMatch || !directRefundTxMatch || !directFromCpfpRefundTxMatch {
				return fmt.Errorf("node txs do not match DB for already-available node %s in transfer %s", nodeID, transferID)
			}

			// Synchronize any non-nil tx fields.
			update := dbNode.Update()
			update.SetRawTx(node.RawTx)
			if node.RawRefundTx != nil {
				update.SetRawRefundTx(node.RawRefundTx)
			}
			update.SetDirectRefundTx(node.DirectRefundTx)
			update.SetDirectFromCpfpRefundTx(node.DirectFromCpfpRefundTx)
			update.SetStatus(st.TreeNodeStatusAvailable)
			if _, err = update.Save(ctx); err != nil {
				return fmt.Errorf("failed to update tree node %s: %w", nodeID, err)
			}
		} else {
			_, err = dbNode.Update().
				SetRawTx(node.RawTx).
				SetRawRefundTx(node.RawRefundTx).
				SetDirectRefundTx(node.DirectRefundTx).
				SetDirectFromCpfpRefundTx(node.DirectFromCpfpRefundTx).
				SetStatus(st.TreeNodeStatusAvailable).
				Save(ctx)
			if err != nil {
				return fmt.Errorf("failed to update tree node %s: %w", nodeID, err)
			}
		}
	}

	if receiver.Status == st.TransferReceiverStatusCompleted {
		// Idempotency: receiver already completed. Node data was verified above.
		if !receiver.CompletionTime.Equal(req.CompletionTimestamp.AsTime()) {
			logger.With(
				zap.String("transfer_id", transferID.String()),
				zap.String("receiver_id", receiver.ID.String()),
				zap.Time("existing_completion_time", receiver.CompletionTime),
				zap.Time("gossip_completion_time", req.CompletionTimestamp.AsTime()),
			).Warn("receiver already completed with different timestamp, accepting idempotently")
		}
	} else {
		_, err = receiver.Update().
			SetStatus(st.TransferReceiverStatusCompleted).
			SetCompletionTime(req.CompletionTimestamp.AsTime()).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to mark receiver completed for transfer %s: %w", transferID, err)
		}
	}

	// Mark the transfer completed when all of its receivers are now completed.
	pendingCount, err := transfer.QueryTransferReceivers().
		Where(enttransferreceiver.StatusNEQ(st.TransferReceiverStatusCompleted)).
		Count(ctx)
	if err != nil {
		return fmt.Errorf("failed to count pending receivers for transfer %s: %w", transferID, err)
	}
	if pendingCount == 0 && transfer.Status != st.TransferStatusCompleted {
		_, err = transfer.Update().
			SetStatus(st.TransferStatusCompleted).
			SetCompletionTime(req.CompletionTimestamp.AsTime()).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to mark transfer completed for %s: %w", transferID, err)
		}
	}

	logger.With(zap.String("transfer_id", transferID.String())).Sugar().Infof("Finalized receiver %s for transfer", receiver.ID)
	return nil
}

// InitiateTransfer initiates a transfer by creating transfer and transfer_leaf
func (h *InternalTransferHandler) InitiateTransfer(ctx context.Context, req *pbinternal.InitiateTransferRequest) error {
	cpfpLeafRefundMap, directLeafRefundMap, directFromCpfpLeafRefundMap := loadInternalLeafRefundMaps(req)
	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return fmt.Errorf("invalid transfer id: %s", req.GetTransferId())
	}
	transferType, err := ent.TransferTypeSchema(req.Type)
	if err != nil {
		return fmt.Errorf("failed to parse transfer type during initiate transfer for transfer id: %s with req.Type: %s and error: %w", transferID, req.Type, err)
	}

	senderIdentityPubKey, err := keys.ParsePublicKey(req.GetSenderIdentityPublicKey())
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse sender identity public key: %w", err))
	}
	receiverIdentityPubKey, err := keys.ParsePublicKey(req.GetReceiverIdentityPublicKey())
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse receiver identity public key: %w", err))
	}

	// Validate the transfer package and the decrypted key tweak proofs if the package is present
	var keyTweakMap map[string]*pb.SendLeafKeyTweak
	if req.TransferPackage != nil {
		keyTweakMap, err = h.ValidateTransferPackage(ctx, transferID, req.TransferPackage, senderIdentityPubKey, !transferType.IsSwap())
		if err != nil {
			return err
		}
		if keyTweakMap == nil {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer package produced no key tweaks for transfer %s", transferID))
		}

		if err := verifySenderKeyTweakProofsMatch(keyTweakMap, req.SenderKeyTweakProofs); err != nil {
			return err
		}
	}

	if len(req.SparkInvoice) > 0 {
		leafIDs, err := uuids.ParseSliceFunc(req.GetTransferPackage().GetLeavesToSend(), (*pb.UserSignedTxSigningJob).GetLeafId)
		if err != nil {
			return fmt.Errorf("failed to parse leaf id: %w", err)
		}

		err = validateSatsSparkInvoice(ctx, req.SparkInvoice, receiverIdentityPubKey, senderIdentityPubKey, leafIDs, false)
		if err != nil {
			return fmt.Errorf("failed to validate sats spark invoice: %s for transfer id: %s. error: %w", req.SparkInvoice, transferID, err)
		}
	}

	// Swap V3 primary transfer for a counter transfer
	var primaryTransferId uuid.UUID
	if req.GetPrimaryTransferId() != "" {
		if primaryTransferId, err = uuid.Parse(req.GetPrimaryTransferId()); err != nil {
			return fmt.Errorf("unable to parse primary transfer uuid for transfer id %s: %w", req.TransferId, err)
		}
	}

	// Swap V3 requires adapted signatures from the User and AdaptorPublicKeys must be provided for this flow.
	// If the user intends to use Swap V3 flow, they will call InitiateSwapPrimaryTransfer rpc and
	// it will validate that the adaptor public keys are provided and then call this generic rpc.
	// Here we just check if the adaptor public keys are provided and if they are
	// we assume that Swap V3 flow is used and we need to verify adaptor signatures.
	if req.AdaptorPublicKeys == nil {
		cpfpLeafRefundMap, directLeafRefundMap, directFromCpfpLeafRefundMap, err = applyRefundSignatures(
			ctx, req.TransferId,
			cpfpLeafRefundMap, directLeafRefundMap, directFromCpfpLeafRefundMap,
			req.RefundSignatures, req.DirectRefundSignatures, req.DirectFromCpfpRefundSignatures,
		)
		if err != nil {
			return err
		}
	} else {
		adaptorPubKeys := req.GetAdaptorPublicKeys()
		// Swap V3 flow
		if req.RefundSignatures == nil {
			return fmt.Errorf("refund signatures are required when adaptor public keys are provided")
		}
		cpfpAdaptorPublicKey, err := keys.ParsePublicKey(adaptorPubKeys.GetAdaptorPublicKey())
		if err != nil {
			return fmt.Errorf("failed to parse cpfp adaptor public key %s: %w", adaptorPubKeys.GetAdaptorPublicKey(), err)
		}
		cpfpLeafRefundMap, err = applySignaturesToTransactionsAndVerify(ctx, cpfpLeafRefundMap, req.RefundSignatures, false, cpfpAdaptorPublicKey)
		if err != nil {
			return fmt.Errorf("failed to apply signatures to leaf cpfp refund map for transfer id: %s and error: %w", transferID, err)
		}
		if req.DirectRefundSignatures != nil && req.DirectFromCpfpRefundSignatures != nil {
			directAdaptorPublicKey, err := keys.ParsePublicKey(adaptorPubKeys.GetDirectAdaptorPublicKey())
			if err != nil {
				return fmt.Errorf("failed to parse direct adaptor public key %s: %w", req.AdaptorPublicKeys.DirectAdaptorPublicKey, err)
			}
			directFromCpfpAdaptorPublicKey, err := keys.ParsePublicKey(req.AdaptorPublicKeys.DirectFromCpfpAdaptorPublicKey)
			if err != nil {
				return fmt.Errorf("failed to parse direct from cpfp adaptor public key %s: %w", req.AdaptorPublicKeys.DirectFromCpfpAdaptorPublicKey, err)
			}
			directLeafRefundMap, err = applySignaturesToTransactionsAndVerify(ctx, directLeafRefundMap, req.DirectRefundSignatures, true, directAdaptorPublicKey)
			if err != nil {
				return fmt.Errorf("failed to apply signatures to leaf direct refund map for transfer id: %s and error: %w", transferID, err)
			}
			directFromCpfpLeafRefundMap, err = applySignaturesToTransactionsAndVerify(ctx, directFromCpfpLeafRefundMap, req.DirectFromCpfpRefundSignatures, false, directFromCpfpAdaptorPublicKey)
			if err != nil {
				return fmt.Errorf("failed to apply signatures to leaf direct from cpfp refund map for transfer id: %s and error: %w", transferID, err)
			}
		}
	}

	_, _, err = h.createTransfer(
		ctx,
		transferID,
		req.TransferPackage,
		transferType,
		req.ExpiryTime.AsTime(),
		senderIdentityPubKey,
		receiverIdentityPubKey,
		cpfpLeafRefundMap,
		directLeafRefundMap,
		directFromCpfpLeafRefundMap,
		keyTweakMap,
		TransferRoleParticipant,
		false,
		req.SparkInvoice,
		primaryTransferId,
		nil,
	)
	if err != nil {
		return fmt.Errorf("failed to initiate transfer for transfer id: %s and error: %w", transferID, err)
	}
	return nil
}

// InitiateTransferV2 handles multi-receiver transfers from the coordinator SO.
// MVP: single sender package only.
func (h *InternalTransferHandler) InitiateTransferV2(ctx context.Context, req *pbinternal.InitiateTransferV2Request) error {
	if len(req.SenderPackages) != 1 {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("expected exactly 1 sender package, got %d", len(req.SenderPackages)))
	}
	senderPkg := req.SenderPackages[0]

	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return fmt.Errorf("invalid transfer id: %s", req.GetTransferId())
	}

	senderIdentityPubKey, err := keys.ParsePublicKey(senderPkg.GetSenderIdentityPublicKey())
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse sender identity public key: %w", err))
	}

	// Parse receivers from the leaf→receiver map.
	leafReceiverMap := make(map[string]keys.Public)
	receiverSet := make(map[string]keys.Public)
	for leafID, receiverBytes := range senderPkg.ReceiverIdentityPublicKeys {
		recvPK, err := keys.ParsePublicKey(receiverBytes)
		if err != nil {
			return sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse receiver public key for leaf %s: %w", leafID, err))
		}
		leafReceiverMap[leafID] = recvPK
		receiverSet[string(recvPK.Serialize())] = recvPK
	}
	receivers := make([]keys.Public, 0, len(receiverSet))
	for _, pk := range receiverSet {
		receivers = append(receivers, pk)
	}
	if len(receivers) == 0 {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("at least one receiver required"))
	}
	slices.SortFunc(receivers, func(a, b keys.Public) int {
		return bytes.Compare(a.Serialize(), b.Serialize())
	})

	// Validate required transfer package and decrypted key tweaks
	if senderPkg.TransferPackage == nil {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer_package is required"))
	}
	keyTweakMap, err := h.ValidateTransferPackage(ctx, transferID, senderPkg.TransferPackage, senderIdentityPubKey, true)
	if err != nil {
		return err
	}
	if keyTweakMap == nil {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer package produced no key tweaks for transfer %s", transferID))
	}
	if err := verifySenderKeyTweakProofsMatch(keyTweakMap, req.SenderKeyTweakProofs); err != nil {
		return err
	}

	cpfpLeafRefundMap, directLeafRefundMap, directFromCpfpLeafRefundMap := loadLeafRefundMapsFromTransferPackage(senderPkg.TransferPackage)

	// Apply refund signatures to transactions and verify.
	cpfpLeafRefundMap, directLeafRefundMap, directFromCpfpLeafRefundMap, err = applyRefundSignatures(
		ctx, req.TransferId,
		cpfpLeafRefundMap, directLeafRefundMap, directFromCpfpLeafRefundMap,
		senderPkg.RefundSignatures, senderPkg.DirectRefundSignatures, senderPkg.DirectFromCpfpRefundSignatures,
	)
	if err != nil {
		return err
	}

	// Create transfer with multiple receivers.
	_, _, err = h.createTransferV3(
		ctx,
		transferID,
		senderPkg.TransferPackage,
		req.ExpiryTime.AsTime(),
		senderIdentityPubKey,
		receivers,
		leafReceiverMap,
		cpfpLeafRefundMap,
		directLeafRefundMap,
		directFromCpfpLeafRefundMap,
		keyTweakMap,
		TransferRoleParticipant,
		false,
	)
	if err != nil {
		return fmt.Errorf("failed to initiate transfer V2 for transfer id: %s: %w", transferID, err)
	}
	return nil
}

func (h *InternalTransferHandler) DeliverSenderKeyTweak(ctx context.Context, req *pbinternal.DeliverSenderKeyTweakRequest) error {
	leafRefundMap := make(map[string][]byte)
	for _, leaf := range req.TransferPackage.LeavesToSend {
		leafRefundMap[leaf.LeafId] = leaf.RawTx
	}
	senderIDPubKey, err := keys.ParsePublicKey(req.SenderIdentityPublicKey)
	if err != nil {
		return fmt.Errorf("failed to parse sender identity public key: %w", err)
	}
	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return fmt.Errorf("invalid transfer id: %s", req.GetTransferId())
	}
	keyTweakMap, err := h.ValidateTransferPackage(ctx, transferID, req.TransferPackage, senderIDPubKey, false)
	if err != nil {
		return err
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	transfer, err := h.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		return fmt.Errorf("unable to find transfer %s: %w", transferID, err)
	}
	leaves, _, err := loadLeavesWithLock(ctx, db, leafRefundMap)
	if err != nil {
		return fmt.Errorf("unable to load leaves for transfer %s: %w", transferID, err)
	}
	if transfer.Status != st.TransferStatusSenderInitiated {
		return fmt.Errorf("transfer %s is in state %s; expected sender initiated status", transferID, transfer.Status)
	}
	for _, leaf := range leaves {
		transferLeaf, err := transfer.QueryTransferLeaves().Where(
			enttransferleaf.HasLeafWith(treenode.IDEQ(leaf.ID))).WithTransfer().Only(ctx)
		if err != nil {
			return err
		}
		leafTweak, ok := keyTweakMap[leaf.ID.String()]
		if !ok {
			return fmt.Errorf("key tweak not found for leaf %s in transfer %s", leaf.ID, transferID)
		}
		leafTweakBinary, err := proto.Marshal(leafTweak)
		if err != nil {
			return fmt.Errorf("unable to marshal leaf tweak for leaf %s: %w", leaf.ID, err)
		}
		_, err = transferLeaf.Update().SetKeyTweak(leafTweakBinary).SetSignature(leafTweak.Signature).SetSecretCipher(leafTweak.SecretCipher).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to update transfer leaf %s for leaf %s: %w", transferLeaf.ID, leaf.ID, err)
		}
	}
	_, err = transfer.Update().SetStatus(st.TransferStatusSenderKeyTweakPending).Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to update status for transfer %s", transferID)
	}

	return nil
}

// Used to effectively sign Tree Node transactions with provided signatures and
// execute a Bitcoin VM verification of the resulting transactions confirming that they can be broadcasted.
func applySignaturesToTransactionsAndVerify(ctx context.Context, leafRefundMap map[string][]byte, refundSignatures map[string][]byte, useDirectTx bool, adaptorPublicKey keys.Public) (map[string][]byte, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	// Collect all leaf UUIDs for batch query
	leafUUIDs, err := uuids.ParseSeq(maps.Keys(refundSignatures))
	if err != nil {
		return nil, fmt.Errorf("unable to parse leaf id: %w", err)
	}

	// Batch query to fetch all tree nodes at once
	leaves, err := db.TreeNode.Query().Where(treenode.IDIn(leafUUIDs...)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get tree nodes: %w", err)
	}

	// Create a map for quick leaf lookup by ID
	leafMap := make(map[string]*ent.TreeNode, len(leaves))
	for _, leaf := range leaves {
		leafMap[leaf.ID.String()] = leaf
	}

	resultMap := make(map[string][]byte)
	for leafID, signature := range refundSignatures {
		leafRefund, exists := leafRefundMap[leafID]
		if !exists {
			return nil, fmt.Errorf("no leaf refund found for leaf id: %s", leafID)
		}

		leaf, exists := leafMap[leafID]
		if !exists {
			return nil, fmt.Errorf("unable to get tree node %s", leafID)
		}

		var nodeTx *wire.MsgTx
		if useDirectTx {
			nodeTx, err = common.TxFromRawTxBytes(leaf.DirectTx)
		} else {
			nodeTx, err = common.TxFromRawTxBytes(leaf.RawTx)
		}
		if err != nil {
			return nil, fmt.Errorf("unable to get node tx of tree node %s: %w", leaf.ID.String(), err)
		}
		updatedTx, err := ApplySignatureToTxAndVerify(leafRefund, signature, adaptorPublicKey, nodeTx.TxOut[0], leaf.VerifyingPubkey)
		if err != nil {
			return nil, fmt.Errorf("unable to apply signature to refund tx of tree node %s and verify: %w", leaf.ID.String(), err)
		}
		resultMap[leafID] = updatedTx
	}
	return resultMap, nil
}

// ApplySignatureToTxAndVerify applies a signature to a transaction and verifies it.
// This function can take an adaptor public key as an optional parameter to
// validate adaptor signatures for the Swap V3 flow.
func ApplySignatureToTxAndVerify(rawTx []byte, signature []byte, adaptorPublicKey keys.Public, outpoint *wire.TxOut, verifyingPubkey keys.Public) ([]byte, error) {
	updatedTx, err := common.UpdateTxWithSignature(rawTx, 0, signature)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("unable to update tx signature: %w", err))
	}

	tx, err := common.TxFromRawTxBytes(updatedTx)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("unable to deserialize tx: %w", err))
	}

	// Check that the signatures are not adapted and can be verified directly
	if adaptorPublicKey.IsZero() {
		if err := common.VerifySignatureSingleInput(tx, 0, outpoint); err != nil {
			return nil, sparkerrors.FailedPreconditionBadSignature(fmt.Errorf("unable to verify tx signature: %w", err))
		}
	} else {
		// Swap V3 flow
		taprootKey := keys.PublicKeyFromKey(*txscript.ComputeTaprootKeyNoScript(verifyingPubkey.ToBTCEC()))
		sighash, err := common.SigHashFromTx(tx, 0, outpoint)
		if err != nil {
			return nil, fmt.Errorf("unable to get sighash: %w", err)
		}
		err = common.ValidateAdaptorSignature(taprootKey, sighash, signature, adaptorPublicKey)
		if err != nil {
			return nil, sparkerrors.FailedPreconditionBadSignature(fmt.Errorf("unable to validate adaptor signature: %w", err))
		}
	}
	return updatedTx, nil
}

// InitiateCooperativeExit initiates a cooperative exit by creating transfer and transfer_leaf,
// and saving the exit txid.
func (h *InternalTransferHandler) InitiateCooperativeExit(ctx context.Context, req *pbinternal.InitiateCooperativeExitRequest) error {
	transferReq := req.Transfer

	senderIDPubKey, err := keys.ParsePublicKey(transferReq.SenderIdentityPublicKey)
	if err != nil {
		return fmt.Errorf("failed to parse sender identity public key: %w", err)
	}
	receiverIDPubKey, err := keys.ParsePublicKey(transferReq.ReceiverIdentityPublicKey)
	if err != nil {
		return fmt.Errorf("failed to parse receiver identity public key: %w", err)
	}
	transferID, err := uuid.Parse(transferReq.GetTransferId())
	if err != nil {
		return fmt.Errorf("invalid transfer id: %s", transferReq.GetTransferId())
	}

	cpfpLeafRefundMap, directLeafRefundMap, directFromCpfpLeafRefundMap := loadInternalLeafRefundMaps(transferReq)

	var keyTweakMap map[string]*pb.SendLeafKeyTweak
	if transferReq.TransferPackage != nil {
		keyTweakMap, err = h.ValidateTransferPackage(ctx, transferID, transferReq.TransferPackage, senderIDPubKey, true)
		if err != nil {
			return err
		}

		// Validate required fields for the coop exit single-call path.
		if transferReq.RefundSignatures == nil {
			return fmt.Errorf("refund_signatures is required for cooperative exit with transfer package")
		}
		if transferReq.DirectFromCpfpRefundSignatures == nil {
			return fmt.Errorf("direct_from_cpfp_refund_signatures is required for cooperative exit with transfer package")
		}

		// Check actual nodes for DirectTx to enforce DirectRefundSignatures.
		leafIDs, err := uuids.ParseSeq(maps.Keys(cpfpLeafRefundMap))
		if err != nil {
			return fmt.Errorf("unable to parse leaf IDs: %w", err)
		}
		db, err := ent.GetDbFromContext(ctx)
		if err != nil {
			return fmt.Errorf("unable to get db from context: %w", err)
		}
		nodes, err := db.TreeNode.Query().Where(treenode.IDIn(leafIDs...)).All(ctx)
		if err != nil {
			return fmt.Errorf("unable to query leaves: %w", err)
		}
		hasDirectTx := false
		for _, node := range nodes {
			if len(node.DirectTx) > 0 {
				hasDirectTx = true
				break
			}
		}
		if hasDirectTx && transferReq.DirectRefundSignatures == nil {
			return fmt.Errorf("direct_refund_signatures is required when leaves have direct transactions")
		}

		// Verify aggregated refund signatures with connector-aware sighash
		cpfpLeafRefundMap, err = applySignaturesToCoopExitTransactionsAndVerify(ctx, cpfpLeafRefundMap, transferReq.RefundSignatures, false, req.GetConnectorTx())
		if err != nil {
			return fmt.Errorf("failed to apply signatures to leaf cpfp refund map for transfer id: %s and error: %w", transferReq.TransferId, err)
		}
		if len(transferReq.DirectRefundSignatures) > 0 {
			directLeafRefundMap, err = applySignaturesToCoopExitTransactionsAndVerify(ctx, directLeafRefundMap, transferReq.DirectRefundSignatures, true, req.GetConnectorTx())
			if err != nil {
				return fmt.Errorf("failed to apply signatures to leaf direct refund map for transfer id: %s and error: %w", transferReq.TransferId, err)
			}
		}
		directFromCpfpLeafRefundMap, err = applySignaturesToCoopExitTransactionsAndVerify(ctx, directFromCpfpLeafRefundMap, transferReq.DirectFromCpfpRefundSignatures, false, req.GetConnectorTx())
		if err != nil {
			return fmt.Errorf("failed to apply signatures to leaf direct from cpfp refund map for transfer id: %s and error: %w", transferReq.TransferId, err)
		}
	}

	transfer, _, err := h.createTransfer(
		ctx,
		transferID,
		transferReq.TransferPackage,
		st.TransferTypeCooperativeExit,
		transferReq.ExpiryTime.AsTime(),
		senderIDPubKey,
		receiverIDPubKey,
		cpfpLeafRefundMap,
		directLeafRefundMap,
		directFromCpfpLeafRefundMap,
		keyTweakMap,
		TransferRoleParticipant,
		false,
		"",
		uuid.Nil,
		req.GetConnectorTx(),
	)
	if err != nil {
		return fmt.Errorf("failed to initiate cooperative exit for transfer id: %s and error: %w", transferID, err)
	}

	exitID, err := uuid.Parse(req.ExitId)
	if err != nil {
		return fmt.Errorf("failed to parse exit id for cooperative exit. transfer id: %s. exit id: %s and error: %w", transferID, req.ExitId, err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	exitTxid, err := st.NewTxIDFromBytes(req.ExitTxid)
	if err != nil {
		return fmt.Errorf("failed to parse exit txid for transfer id: %s. exit id: %s and error: %w", transferReq.TransferId, req.ExitId, err)
	}

	_, err = db.CooperativeExit.Create().
		SetID(exitID).
		SetTransfer(transfer).
		SetExitTxid(exitTxid).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to create cooperative exit in db for transfer id: %s. exit id: %s and error: %w", transferID, req.ExitId, err)
	}
	return err
}

// applySignaturesToCoopExitTransactionsAndVerify applies signatures to coop exit refund transactions
// and verifies them, handling multi-input transactions that include connector outputs.
func applySignaturesToCoopExitTransactionsAndVerify(ctx context.Context, leafRefundMap map[string][]byte, refundSignatures map[string][]byte, useDirectTx bool, connectorTx []byte) (map[string][]byte, error) {
	connectorPrevOuts, err := parseConnectorTxOutputs(connectorTx)
	if err != nil {
		return nil, fmt.Errorf("unable to parse connector tx: %w", err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	leafUUIDs, err := uuids.ParseSeq(maps.Keys(refundSignatures))
	if err != nil {
		return nil, fmt.Errorf("unable to parse leaf id: %w", err)
	}

	leaves, err := db.TreeNode.Query().Where(treenode.IDIn(leafUUIDs...)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get tree nodes: %w", err)
	}

	leafMap := make(map[string]*ent.TreeNode, len(leaves))
	for _, leaf := range leaves {
		leafMap[leaf.ID.String()] = leaf
	}

	resultMap := make(map[string][]byte)
	for leafID, signature := range refundSignatures {
		leafRefund, exists := leafRefundMap[leafID]
		if !exists {
			return nil, fmt.Errorf("no leaf refund found for leaf id: %s", leafID)
		}

		leaf, exists := leafMap[leafID]
		if !exists {
			return nil, fmt.Errorf("unable to get tree node %s", leafID)
		}

		var nodeTx *wire.MsgTx
		if useDirectTx {
			nodeTx, err = common.TxFromRawTxBytes(leaf.DirectTx)
		} else {
			nodeTx, err = common.TxFromRawTxBytes(leaf.RawTx)
		}
		if err != nil {
			return nil, fmt.Errorf("unable to get node tx of tree node %s: %w", leaf.ID.String(), err)
		}

		refundTx, err := common.TxFromRawTxBytes(leafRefund)
		if err != nil {
			return nil, fmt.Errorf("unable to parse refund tx for tree node %s: %w", leaf.ID.String(), err)
		}

		if len(refundTx.TxIn) > 1 && connectorPrevOuts != nil {
			// Multi-input refund tx with connector: apply signature and verify with multi-input
			updatedTx, err := common.UpdateTxWithSignature(leafRefund, 0, signature)
			if err != nil {
				return nil, fmt.Errorf("unable to update tx signature for tree node %s: %w", leaf.ID.String(), err)
			}

			signedTx, err := common.TxFromRawTxBytes(updatedTx)
			if err != nil {
				return nil, fmt.Errorf("unable to deserialize signed tx for tree node %s: %w", leaf.ID.String(), err)
			}

			prevOuts := make(map[wire.OutPoint]*wire.TxOut, 2)
			nodeTxHash := nodeTx.TxHash()
			prevOuts[wire.OutPoint{Hash: nodeTxHash, Index: 0}] = nodeTx.TxOut[0]

			connectorOutpoint := signedTx.TxIn[1].PreviousOutPoint
			connectorTxOut, connectorExists := connectorPrevOuts[connectorOutpoint]
			if !connectorExists {
				return nil, fmt.Errorf("refund tx input 1 does not reference a valid connector output for tree node %s: %v", leaf.ID.String(), connectorOutpoint)
			}
			prevOuts[connectorOutpoint] = connectorTxOut

			prevOutFetcher := txscript.NewMultiPrevOutFetcher(prevOuts)
			if err := common.VerifySignatureInput(signedTx, 0, prevOutFetcher); err != nil {
				return nil, fmt.Errorf("unable to verify multi-input tx signature for tree node %s: %w", leaf.ID.String(), err)
			}
			resultMap[leafID] = updatedTx
		} else {
			// Single-input or no connector: use standard verification
			updatedTx, err := ApplySignatureToTxAndVerify(leafRefund, signature, keys.Public{}, nodeTx.TxOut[0], leaf.VerifyingPubkey)
			if err != nil {
				return nil, fmt.Errorf("unable to apply signature to refund tx of tree node %s and verify: %w", leaf.ID.String(), err)
			}
			resultMap[leafID] = updatedTx
		}
	}
	return resultMap, nil
}

func (h *InternalTransferHandler) SettleSenderKeyTweak(ctx context.Context, req *pbinternal.SettleSenderKeyTweakRequest) error {
	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return fmt.Errorf("invalid transfer id: %s", req.GetTransferId())
	}
	switch req.Action {
	case pbinternal.SettleKeyTweakAction_NONE:
		return fmt.Errorf("no action to settle sender key tweak")
	case pbinternal.SettleKeyTweakAction_COMMIT:
		transfer, err := h.loadTransferForUpdate(ctx, transferID)
		if err != nil {
			return fmt.Errorf("unable to load transfer %s: %w", transferID, err)
		}
		_, err = h.commitSenderKeyTweaks(ctx, transfer)
		return err
	case pbinternal.SettleKeyTweakAction_ROLLBACK:
		transfer, err := h.loadTransferForUpdate(ctx, transferID)
		if err != nil {
			return fmt.Errorf("unable to load transfer %s: %w", transferID, err)
		}
		return h.executeCancelTransfer(ctx, transfer)
	}
	return nil
}

func (h *InternalTransferHandler) GetTransfers(ctx context.Context, req *pbinternal.GetTransfersRequest) (*pbinternal.GetTransfersResponse, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	transferIDs, err := uuids.ParseSlice(req.GetTransferIds())
	if err != nil {
		return nil, fmt.Errorf("failed to parse transfer ids: %w", err)
	}
	transfers, err := db.Transfer.Query().
		Where(enttransfer.IDIn(transferIDs...)).
		WithTransferLeaves(func(q *ent.TransferLeafQuery) {
			q.WithLeaf(func(q *ent.TreeNodeQuery) {
				q.WithTree().WithSigningKeyshare().WithParent()
			})
		}).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query transfers: %w", err)
	}

	transferProtos := make([]*pb.Transfer, len(transfers))
	for i, transfer := range transfers {
		transferProtos[i], err = transfer.MarshalProto(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal transfer: %w", err)
		}
	}
	return &pbinternal.GetTransfersResponse{Transfers: transferProtos}, nil
}

// Deserializes the txs and compares the inputs and outputs.
// parseTxPair parses and version-validates both raw transactions.
func parseTxPair(rawTx1, rawTx2 []byte) (*wire.MsgTx, *wire.MsgTx, error) {
	if rawTx1 == nil || rawTx2 == nil {
		return nil, nil, fmt.Errorf("one or both transactions are nil")
	}
	tx1, err := common.TxFromRawTxBytes(rawTx1)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse tx1: %w", err)
	}
	if err := common.ValidateBitcoinTxVersion(tx1); err != nil {
		return nil, nil, fmt.Errorf("tx1 version validation failed: %w", err)
	}
	tx2, err := common.TxFromRawTxBytes(rawTx2)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse tx2: %w", err)
	}
	if err := common.ValidateBitcoinTxVersion(tx2); err != nil {
		return nil, nil, fmt.Errorf("tx2 version validation failed: %w", err)
	}
	return tx1, tx2, nil
}

// compareTxStructure returns true if tx1 and tx2 have identical inputs
// (outpoint, script, sequence) and outputs (value, pkscript).
// Witness data is not compared.
// Assumes inputs have already been length-checked if called after other checks,
// but performs its own length checks internally.
func compareTxStructure(tx1, tx2 *wire.MsgTx) bool {
	if len(tx1.TxIn) != len(tx2.TxIn) {
		return false
	}
	for i, txIn1 := range tx1.TxIn {
		txIn2 := tx2.TxIn[i]
		if txIn1.PreviousOutPoint != txIn2.PreviousOutPoint {
			return false
		}
		// SignatureScript is always nil for P2TR inputs; checked here for completeness.
		if !bytes.Equal(txIn1.SignatureScript, txIn2.SignatureScript) {
			return false
		}
		if txIn1.Sequence != txIn2.Sequence {
			return false
		}
	}
	if len(tx1.TxOut) != len(tx2.TxOut) {
		return false
	}
	for i, txOut1 := range tx1.TxOut {
		txOut2 := tx2.TxOut[i]
		if txOut1.Value != txOut2.Value {
			return false
		}
		if !bytes.Equal(txOut1.PkScript, txOut2.PkScript) {
			return false
		}
	}
	return true
}

// witnessesMatch returns true if every input in tx1 and tx2 has byte-identical
// witness stacks. Assumes tx1 and tx2 have the same number of inputs.
func witnessesMatch(tx1, tx2 *wire.MsgTx) bool {
	for i, txIn1 := range tx1.TxIn {
		txIn2 := tx2.TxIn[i]
		if len(txIn1.Witness) != len(txIn2.Witness) {
			return false
		}
		for j, item := range txIn1.Witness {
			if !bytes.Equal(item, txIn2.Witness[j]) {
				return false
			}
		}
	}
	return true
}

// compareTxs returns true if rawTx1 and rawTx2 are structurally identical,
// including byte-for-byte equal witness stacks. Returns (false, nil) for any
// mismatch; returns (false, error) only for parse or version failures.
func compareTxs(rawTx1, rawTx2 []byte) (bool, error) {
	if rawTx1 == nil && rawTx2 == nil {
		return true, nil
	}
	tx1, tx2, err := parseTxPair(rawTx1, rawTx2)
	if err != nil {
		return false, err
	}
	if !compareTxStructure(tx1, tx2) {
		return false, nil
	}
	return witnessesMatch(tx1, tx2), nil
}

// compareAndVerifyTxs returns true if rawTx1 and rawTx2 are structurally
// identical and carry valid signatures. If the witness stacks differ,
// tx2's signature is verified against prevOut — a different but cryptographically
// valid signature (e.g. from a separate FROST signing session) is accepted.
// An invalid or missing signature in tx2 is returned as an error.
// Returns (false, nil) for structural mismatches; (false, error) for parse,
// version, or signature failures. prevOut must be non-nil when the transactions
// are non-nil.
func compareAndVerifyTxs(rawTx1, rawTx2 []byte, prevOut *wire.TxOut) (bool, error) {
	if rawTx1 == nil && rawTx2 == nil {
		return true, nil
	}
	tx1, tx2, err := parseTxPair(rawTx1, rawTx2)
	if err != nil {
		return false, err
	}
	if !compareTxStructure(tx1, tx2) {
		return false, nil
	}
	if !witnessesMatch(tx1, tx2) {
		if prevOut == nil {
			return false, fmt.Errorf("cannot verify signature: prevOut is nil")
		}
		if err := common.VerifySignatureSingleInput(tx2, 0, prevOut); err != nil {
			return false, fmt.Errorf("incoming tx has invalid or missing signature: %w", err)
		}
	}
	return true, nil
}

func validateSatsSparkInvoice(ctx context.Context, invoice string, receiverPublicKey keys.Public, senderPublicKey keys.Public, leafIDsToSend []uuid.UUID, checkExpiry bool) error {
	now := time.Now().UTC()
	dedupLeafIDs := dedupUUIDs(leafIDsToSend)
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	decodedInvoice, err := common.ParseSparkInvoice(invoice)
	if err != nil {
		return fmt.Errorf("failed to decode spark invoice: %s, error: %w", invoice, err)
	}
	if decodedInvoice.Payment.Kind != common.PaymentKindSats {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invoice must be a sats invoice"))
	}
	if decodedInvoice.ReceiverPublicKey != receiverPublicKey {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("receiver identity public key does not match the invoice identity public key, expected: %x, got: %x", receiverPublicKey.Serialize(), decodedInvoice.ReceiverPublicKey.Serialize()))
	}
	if !decodedInvoice.SenderPublicKey.IsZero() && decodedInvoice.SenderPublicKey != senderPublicKey {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("sender identity public key does not match the invoice sender public key, expected: %x, got: %x", senderPublicKey.Serialize(), decodedInvoice.SenderPublicKey.Serialize()))
	}

	if checkExpiry {
		if ts := decodedInvoice.ExpiryTime; ts != nil && ts.IsValid() {
			exp := ts.AsTime()
			if exp.Before(now) {
				return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf(
					"invoice has expired. decoded expiry(UTC): %s, now(UTC): %s",
					exp.UTC().Format(time.RFC3339),
					now.UTC().Format(time.RFC3339),
				))
			}
		}
	}

	// Check if the invoice amount matches the amount in the leaves to send.
	invoiceAmount := decodedInvoice.Payment.SatsPayment.Amount
	var agg []struct {
		Count int
		Sum   sql.NullInt64
	}
	err = db.TreeNode.
		Query().
		Where(treenode.IDIn(dedupLeafIDs...)).
		Where(treenode.NetworkEQ(decodedInvoice.Network)).
		Aggregate(
			ent.As(ent.Count(), "count"),
			ent.As(ent.Sum(treenode.FieldValue), "sum"),
		).
		Scan(ctx, &agg)
	if err != nil {
		return fmt.Errorf("failed to query leaves: %w", err)
	}
	if agg[0].Count != len(dedupLeafIDs) {
		// Either the leaf ID was not found, or there was a network mismatch.
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("one or more leaves not found on expected network: %s", decodedInvoice.Network))
	}
	if invoiceAmount != nil {
		totalAmount := uint64(0)
		if agg[0].Sum.Valid {
			if agg[0].Sum.Int64 < 0 {
				return fmt.Errorf("invalid negative leaf sum: %d", agg[0].Sum.Int64)
			}
			totalAmount = uint64(agg[0].Sum.Int64)
		}
		if totalAmount != *invoiceAmount {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invoice amount does not match the transfer package amount got: %d, expected: %d", totalAmount, *invoiceAmount))
		}
	}
	return nil
}

func dedupUUIDs(in []uuid.UUID) []uuid.UUID {
	m := make(map[uuid.UUID]struct{}, len(in))
	out := make([]uuid.UUID, 0, len(in))
	for _, id := range in {
		if _, ok := m[id]; !ok {
			m[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}
