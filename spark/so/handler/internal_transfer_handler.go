package handler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/schema"
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
	db := ent.GetDbFromContext(ctx)
	transfer, err := h.loadTransfer(ctx, req.TransferId)
	if err != nil {
		return fmt.Errorf("unable to load transfer %s: %w", req.TransferId, err)
	}

	switch transfer.Status {
	case schema.TransferStatusReceiverKeyTweaked:
	case schema.TransferStatusReceiverKeyTweakLocked:
	case schema.TransferStatusReceiverRefundSigned:
	case schema.TransferStatusReceiverKeyTweakApplied:
		// do nothing
	default:
		return fmt.Errorf("transfer is not in receiver key tweaked status. transfer id: %s. status: %s", req.TransferId, transfer.Status)
	}

	if err := checkCoopExitTxBroadcasted(ctx, db, transfer); err != nil {
		return fmt.Errorf("failed to unlock transfer id: %s. with status: %s and error: %w", req.TransferId, transfer.Status, err)
	}

	transferNodes, err := transfer.QueryTransferLeaves().QueryLeaf().All(ctx)
	if err != nil {
		return fmt.Errorf("failed to query transfer leaves for transfer id: %s. with status: %s and error: %w", req.TransferId, transfer.Status, err)
	}
	if len(transferNodes) != len(req.Nodes) {
		return fmt.Errorf("transfer nodes count mismatch. transfer id: %s. with status: %s. transfer nodes count: %d. request nodes count: %d", req.TransferId, transfer.Status, len(transferNodes), len(req.Nodes))
	}
	transferNodeIDs := make(map[string]string)
	for _, node := range transferNodes {
		transferNodeIDs[node.ID.String()] = node.ID.String()
	}

	for _, node := range req.Nodes {
		if _, ok := transferNodeIDs[node.Id]; !ok {
			return fmt.Errorf("node not found in transfer. transfer id: %s. with status: %s. node id: %s", req.TransferId, transfer.Status, node.Id)
		}

		nodeID, err := uuid.Parse(node.Id)
		if err != nil {
			return fmt.Errorf("failed to parse node uuid. transfer id: %s. with status: %s. node id: %s", req.TransferId, transfer.Status, node.Id)
		}
		dbNode, err := db.TreeNode.Get(ctx, nodeID)
		if err != nil {
			return fmt.Errorf("failed to get dbNode. transfer id: %s. with status: %s. node id: %s with uuid: %s and error: %w", req.TransferId, transfer.Status, node.Id, nodeID, err)
		}
		_, err = dbNode.Update().
			SetRawTx(node.RawTx).
			SetRawRefundTx(node.RawRefundTx).
			SetStatus(schema.TreeNodeStatusAvailable).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to update dbNode. transfer id: %s. with status: %s. node id: %s with uuid: %s and error: %w", req.TransferId, transfer.Status, node.Id, nodeID, err)
		}
	}

	_, err = transfer.Update().SetStatus(schema.TransferStatusCompleted).SetCompletionTime(req.Timestamp.AsTime()).Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to update transfer status to completed for transfer id: %s. with status: %s and error: %w", req.TransferId, transfer.Status, err)
	}
	return nil
}

func (h *InternalTransferHandler) loadLeafRefundMap(req *pbinternal.InitiateTransferRequest) map[string][]byte {
	leafRefundMap := make(map[string][]byte)
	if req.TransferPackage != nil {
		for _, leaf := range req.TransferPackage.LeavesToSend {
			leafRefundMap[leaf.LeafId] = leaf.RawTx
		}
	} else {
		for _, leaf := range req.Leaves {
			leafRefundMap[leaf.LeafId] = leaf.RawRefundTx
		}
	}
	return leafRefundMap
}

// InitiateTransfer initiates a transfer by creating transfer and transfer_leaf
func (h *InternalTransferHandler) InitiateTransfer(ctx context.Context, req *pbinternal.InitiateTransferRequest) error {
	leafRefundMap := h.loadLeafRefundMap(req)
	transferType, err := ent.TransferTypeSchema(req.Type)
	if err != nil {
		return fmt.Errorf("failed to parse transfer type during initiate transfer for transfer id: %s with req.Type: %s and error: %w", req.TransferId, req.Type, err)
	}

	keyTweakMap, err := h.validateTransferPackage(ctx, req.TransferId, req.TransferPackage, req.SenderIdentityPublicKey)
	if err != nil {
		return err
	}

	if req.RefundSignatures != nil {
		leafRefundMap, err = applySignatures(ctx, leafRefundMap, req.RefundSignatures)
		if err != nil {
			return fmt.Errorf("failed to apply signatures to leaf refund map for transfer id: %s and error: %w", req.TransferId, err)
		}
	}
	_, _, err = h.createTransfer(
		ctx,
		req.TransferId,
		transferType,
		req.ExpiryTime.AsTime(),
		req.SenderIdentityPublicKey,
		req.ReceiverIdentityPublicKey,
		leafRefundMap,
		keyTweakMap,
		TransferRoleParticipant,
	)
	if err != nil {
		return fmt.Errorf("failed to initiate transfer for transfer id: %s and error: %w", req.TransferId, err)
	}
	return nil
}

func applySignatures(ctx context.Context, leafRefundMap map[string][]byte, refundSignatures map[string][]byte) (map[string][]byte, error) {
	db := ent.GetDbFromContext(ctx)
	resultMap := make(map[string][]byte)
	for leafID, signature := range refundSignatures {
		updatedTx, err := common.UpdateTxWithSignature(leafRefundMap[leafID], 0, signature)
		if err != nil {
			return nil, fmt.Errorf("unable to update leaf signature: %w", err)
		}

		refundTx, err := common.TxFromRawTxBytes(updatedTx)
		if err != nil {
			return nil, fmt.Errorf("unable to get refund tx: %w", err)
		}
		leafUUID, err := uuid.Parse(leafID)
		if err != nil {
			return nil, fmt.Errorf("unable to parse leaf id: %w", err)
		}
		leaf, err := db.TreeNode.Get(ctx, leafUUID)
		if err != nil {
			return nil, fmt.Errorf("unable to get leaf: %w", err)
		}
		nodeTx, err := common.TxFromRawTxBytes(leaf.RawTx)
		if err != nil {
			return nil, fmt.Errorf("unable to get node tx: %w", err)
		}
		err = common.VerifySignatureSingleInput(refundTx, 0, nodeTx.TxOut[0])
		if err != nil {
			return nil, fmt.Errorf("unable to verify leaf signature: %w", err)
		}
		resultMap[leafID] = updatedTx
	}
	return resultMap, nil
}

// InitiateCooperativeExit initiates a cooperative exit by creating transfer and transfer_leaf,
// and saving the exit txid.
func (h *InternalTransferHandler) InitiateCooperativeExit(ctx context.Context, req *pbinternal.InitiateCooperativeExitRequest) error {
	transferReq := req.Transfer
	leafRefundMap := make(map[string][]byte)
	for _, leaf := range transferReq.Leaves {
		leafRefundMap[leaf.LeafId] = leaf.RawRefundTx
	}
	transfer, _, err := h.createTransfer(
		ctx,
		transferReq.TransferId,
		schema.TransferTypeCooperativeExit,
		transferReq.ExpiryTime.AsTime(),
		transferReq.SenderIdentityPublicKey,
		transferReq.ReceiverIdentityPublicKey,
		leafRefundMap,
		nil,
		TransferRoleParticipant,
	)
	if err != nil {
		return fmt.Errorf("failed to initiate cooperative exit for transfer id: %s and error: %w", transferReq.TransferId, err)
	}

	exitID, err := uuid.Parse(req.ExitId)
	if err != nil {
		return fmt.Errorf("failed to parse exit id for cooperative exit. transfer id: %s. exit id: %s and error: %w", transferReq.TransferId, req.ExitId, err)
	}

	db := ent.GetDbFromContext(ctx)
	_, err = db.CooperativeExit.Create().
		SetID(exitID).
		SetTransfer(transfer).
		SetExitTxid(req.ExitTxid).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to create cooperative exit in db for transfer id: %s. exit id: %s and error: %w", transferReq.TransferId, req.ExitId, err)
	}
	return err
}

func (h *InternalTransferHandler) SettleSenderKeyTweak(ctx context.Context, req *pbinternal.SettleSenderKeyTweakRequest) error {
	switch req.Action {
	case pbinternal.SettleKeyTweakAction_NONE:
		return fmt.Errorf("no action to settle sender key tweak")
	case pbinternal.SettleKeyTweakAction_COMMIT:
		transfer, err := h.loadTransfer(ctx, req.TransferId)
		if err != nil {
			return fmt.Errorf("unable to load transfer %s: %w", req.TransferId, err)
		}
		return h.commitSenderKeyTweaks(ctx, transfer)
	case pbinternal.SettleKeyTweakAction_ROLLBACK:
		transfer, err := h.loadTransfer(ctx, req.TransferId)
		if err != nil {
			return fmt.Errorf("unable to load transfer %s: %w", req.TransferId, err)
		}
		return h.executeCancelTransfer(ctx, transfer)
	}
	return nil
}
