package handler

import (
	"bytes"
	"context"
	"fmt"
	"github.com/lightsparkdev/spark/common/keys"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	enttransferleaf "github.com/lightsparkdev/spark/so/ent/transferleaf"
	"github.com/lightsparkdev/spark/so/ent/treenode"
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

	transfer, err := h.loadTransferForUpdate(ctx, req.TransferId)
	if err != nil {
		return fmt.Errorf("unable to load transfer %s: %w", req.TransferId, err)
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

		if transfer.Status == st.TransferStatusCompleted {
			// Verify that the transfer details are the same between both nodes
			rawTxMatch, err := compareTxs(dbNode.RawTx, node.RawTx)
			if err != nil {
				return fmt.Errorf("failed to compare raw txs: %w", err)
			}
			directRefundTxMatch, err := compareTxs(dbNode.DirectRefundTx, node.DirectRefundTx)
			if err != nil {
				return fmt.Errorf("failed to compare direct refund txs: %w", err)
			}
			directFromCpfpRefundTxMatch, err := compareTxs(dbNode.DirectFromCpfpRefundTx, node.DirectFromCpfpRefundTx)
			if err != nil {
				return fmt.Errorf("failed to compare direct from cpfp refund txs: %w", err)
			}

			if !rawTxMatch || !directRefundTxMatch || !directFromCpfpRefundTxMatch {
				return fmt.Errorf("node is not the same as the one in the DB or maybe refundTX not matching. transfer id: %s. with status: %s. node id: %s with uuid: %s", req.TransferId, transfer.Status, node.Id, nodeID)
			}

			// Synchronize any non-nil tx fields.
			update := dbNode.Update()

			update.SetRawTx(node.RawTx) // RawTx is required field, can't be nil
			if dbNode.RawRefundTx != nil {
				update.SetRawRefundTx(node.RawRefundTx)
			}
			if dbNode.DirectRefundTx != nil {
				update.SetDirectRefundTx(node.DirectRefundTx)
			}
			if dbNode.DirectFromCpfpRefundTx != nil {
				update.SetDirectFromCpfpRefundTx(node.DirectFromCpfpRefundTx)
			}
			update.SetStatus(st.TreeNodeStatusAvailable)

			_, err = update.Save(ctx)
			if err != nil {
				return fmt.Errorf("failed to update dbNode. transfer id: %s. with status: %s. node id: %s with uuid: %s and error: %w", req.TransferId, transfer.Status, node.Id, nodeID, err)
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
				return fmt.Errorf("failed to update dbNode. transfer id: %s. with status: %s. node id: %s with uuid: %s and error: %w", req.TransferId, transfer.Status, node.Id, nodeID, err)
			}

			_, err = transfer.Update().SetStatus(st.TransferStatusCompleted).SetCompletionTime(req.Timestamp.AsTime()).Save(ctx)
			if err != nil {
				return fmt.Errorf("failed to update transfer status to completed for transfer id: %s. with status: %s and error: %w", req.TransferId, transfer.Status, err)
			}
		}
	}
	return nil
}

func (h *InternalTransferHandler) loadLeafRefundMaps(req *pbinternal.InitiateTransferRequest) (map[string][]byte, map[string][]byte, map[string][]byte) {
	cpfpLeafRefundMap := make(map[string][]byte)
	directLeafRefundMap := make(map[string][]byte)
	directFromCpfpLeafRefundMap := make(map[string][]byte)
	if req.TransferPackage != nil {
		for _, leaf := range req.TransferPackage.LeavesToSend {
			cpfpLeafRefundMap[leaf.LeafId] = leaf.RawTx
		}
		for _, leaf := range req.TransferPackage.DirectLeavesToSend {
			directLeafRefundMap[leaf.LeafId] = leaf.RawTx
		}
		for _, leaf := range req.TransferPackage.DirectFromCpfpLeavesToSend {
			directFromCpfpLeafRefundMap[leaf.LeafId] = leaf.RawTx
		}
	} else {
		for _, leaf := range req.Leaves {
			cpfpLeafRefundMap[leaf.LeafId] = leaf.RawRefundTx
			directLeafRefundMap[leaf.LeafId] = leaf.DirectRefundTx
			directFromCpfpLeafRefundMap[leaf.LeafId] = leaf.DirectFromCpfpRefundTx
		}
	}
	return cpfpLeafRefundMap, directLeafRefundMap, directFromCpfpLeafRefundMap
}

// InitiateTransfer initiates a transfer by creating transfer and transfer_leaf
func (h *InternalTransferHandler) InitiateTransfer(ctx context.Context, req *pbinternal.InitiateTransferRequest) error {
	cpfpLeafRefundMap, directLeafRefundMap, directFromCpfpLeafRefundMap := h.loadLeafRefundMaps(req)
	transferType, err := ent.TransferTypeSchema(req.Type)
	if err != nil {
		return fmt.Errorf("failed to parse transfer type during initiate transfer for transfer id: %s with req.Type: %s and error: %w", req.TransferId, req.Type, err)
	}

	senderIDPubKey, err := keys.ParsePublicKey(req.SenderIdentityPublicKey)
	if err != nil {
		return fmt.Errorf("failed to parse sender identity public key: %w", err)
	}
	receiverIDPubKey, err := keys.ParsePublicKey(req.ReceiverIdentityPublicKey)
	if err != nil {
		return fmt.Errorf("failed to parse receiver identity public key: %w", err)
	}
	keyTweakMap, err := h.validateTransferPackage(ctx, req.TransferId, req.TransferPackage, senderIDPubKey)
	if err != nil {
		return err
	}

	if req.RefundSignatures != nil {
		cpfpLeafRefundMap, err = applySignatures(ctx, cpfpLeafRefundMap, req.RefundSignatures, false)
		if err != nil {
			return fmt.Errorf("failed to apply signatures to leaf cpfp refund map for transfer id: %s and error: %w", req.TransferId, err)
		}
	}
	if req.DirectRefundSignatures != nil && req.DirectFromCpfpRefundSignatures != nil {
		directLeafRefundMap, err = applySignatures(ctx, directLeafRefundMap, req.DirectRefundSignatures, true)
		if err != nil {
			return fmt.Errorf("failed to apply signatures to leaf direct refund map for transfer id: %s and error: %w", req.TransferId, err)
		}
		directFromCpfpLeafRefundMap, err = applySignatures(ctx, directFromCpfpLeafRefundMap, req.DirectFromCpfpRefundSignatures, false)
		if err != nil {
			return fmt.Errorf("failed to apply signatures to leaf direct from cpfp refund map for transfer id: %s and error: %w", req.TransferId, err)
		}
	}
	_, _, err = h.createTransfer(
		ctx,
		req.TransferId,
		transferType,
		req.ExpiryTime.AsTime(),
		senderIDPubKey,
		receiverIDPubKey,
		cpfpLeafRefundMap,
		directLeafRefundMap,
		directFromCpfpLeafRefundMap,
		keyTweakMap,
		TransferRoleParticipant,
		false,
	)
	if err != nil {
		return fmt.Errorf("failed to initiate transfer for transfer id: %s and error: %w", req.TransferId, err)
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
	keyTweakMap, err := h.validateTransferPackage(ctx, req.TransferId, req.TransferPackage, senderIDPubKey)
	if err != nil {
		return err
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	leaves, err := loadLeavesWithLock(ctx, db, leafRefundMap)
	if err != nil {
		return fmt.Errorf("unable to load leaves: %w", err)
	}
	transfer, err := h.loadTransferForUpdate(ctx, req.TransferId)
	if err != nil {
		return fmt.Errorf("unable to find transfer %s: %w", req.TransferId, err)
	}
	if transfer.Status != st.TransferStatusSenderInitiated {
		return fmt.Errorf("transfer %s is in state %s; expected sender initiated status", req.TransferId, transfer.Status)
	}
	for _, leaf := range leaves {
		transferLeaf, err := transfer.QueryTransferLeaves().Where(
			enttransferleaf.HasLeafWith(treenode.IDEQ(leaf.ID))).WithTransfer().Only(ctx)
		if err != nil {
			return err
		}
		if leafTweak, ok := keyTweakMap[leaf.ID.String()]; ok {
			leafTweakBinary, err := proto.Marshal(leafTweak)
			if err != nil {
				return fmt.Errorf("unable to marshal leaf tweak: %w", err)
			}
			_, err = transferLeaf.Update().SetKeyTweak(leafTweakBinary).SetSignature(leafTweak.Signature).SetSecretCipher(leafTweak.SecretCipher).Save(ctx)
			if err != nil {
				return fmt.Errorf("unable to update transfer leaf: %w", err)
			}
		}
	}
	_, err = transfer.Update().SetStatus(st.TransferStatusSenderKeyTweakPending).Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to update status for transfer %s", req.TransferId)
	}

	return nil
}

func applySignatures(ctx context.Context, leafRefundMap map[string][]byte, refundSignatures map[string][]byte, useDirectTx bool) (map[string][]byte, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
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
		var nodeTx *wire.MsgTx
		if useDirectTx {
			nodeTx, err = common.TxFromRawTxBytes(leaf.DirectTx)
		} else {
			nodeTx, err = common.TxFromRawTxBytes(leaf.RawTx)
		}
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
	cpfpLeafRefundMap := make(map[string][]byte)
	directLeafRefundMap := make(map[string][]byte)
	directFromCpfpLeafRefundMap := make(map[string][]byte)
	for _, leaf := range transferReq.Leaves {
		cpfpLeafRefundMap[leaf.LeafId] = leaf.RawRefundTx
		directLeafRefundMap[leaf.LeafId] = leaf.DirectRefundTx
		directFromCpfpLeafRefundMap[leaf.LeafId] = leaf.DirectFromCpfpRefundTx
	}
	senderIDPubKey, err := keys.ParsePublicKey(transferReq.SenderIdentityPublicKey)
	if err != nil {
		return fmt.Errorf("failed to parse sender identity public key: %w", err)
	}
	receiverIDPubKey, err := keys.ParsePublicKey(transferReq.ReceiverIdentityPublicKey)
	if err != nil {
		return fmt.Errorf("failed to parse receiver identity public key: %w", err)
	}
	transfer, _, err := h.createTransfer(
		ctx,
		transferReq.TransferId,
		st.TransferTypeCooperativeExit,
		transferReq.ExpiryTime.AsTime(),
		senderIDPubKey,
		receiverIDPubKey,
		cpfpLeafRefundMap,
		directLeafRefundMap,
		directFromCpfpLeafRefundMap,
		nil,
		TransferRoleParticipant,
		false,
	)
	if err != nil {
		return fmt.Errorf("failed to initiate cooperative exit for transfer id: %s and error: %w", transferReq.TransferId, err)
	}

	exitID, err := uuid.Parse(req.ExitId)
	if err != nil {
		return fmt.Errorf("failed to parse exit id for cooperative exit. transfer id: %s. exit id: %s and error: %w", transferReq.TransferId, req.ExitId, err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
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
		transfer, err := h.loadTransferForUpdate(ctx, req.TransferId)
		if err != nil {
			return fmt.Errorf("unable to load transfer %s: %w", req.TransferId, err)
		}
		_, err = h.commitSenderKeyTweaks(ctx, transfer, false)
		return err
	case pbinternal.SettleKeyTweakAction_ROLLBACK:
		transfer, err := h.loadTransferForUpdate(ctx, req.TransferId)
		if err != nil {
			return fmt.Errorf("unable to load transfer %s: %w", req.TransferId, err)
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
	transferIDs := make([]uuid.UUID, len(req.TransferIds))
	for i, transferID := range req.TransferIds {
		transferID, err := uuid.Parse(transferID)
		if err != nil {
			return nil, fmt.Errorf("failed to parse transfer id: %w", err)
		}
		transferIDs[i] = transferID
	}
	transfers, err := db.Transfer.Query().Where(enttransfer.IDIn(transferIDs...)).All(ctx)
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
func compareTxs(rawTx1, rawTx2 []byte) (bool, error) {
	if rawTx1 == nil && rawTx2 == nil {
		return true, nil
	}
	tx1, err := common.TxFromRawTxBytes(rawTx1)
	if err != nil {
		return false, fmt.Errorf("failed to parse tx1: %w", err)
	}

	tx2, err := common.TxFromRawTxBytes(rawTx2)
	if err != nil {
		return false, fmt.Errorf("failed to parse tx2: %w", err)
	}

	if len(tx1.TxIn) != len(tx2.TxIn) {
		return false, nil
	}

	for i, txIn1 := range tx1.TxIn {
		txIn2 := tx2.TxIn[i]
		if txIn1.PreviousOutPoint != txIn2.PreviousOutPoint {
			return false, nil
		}
		if !bytes.Equal(txIn1.SignatureScript, txIn2.SignatureScript) {
			return false, nil
		}
		if txIn1.Sequence != txIn2.Sequence {
			return false, nil
		}
	}

	if len(tx1.TxOut) != len(tx2.TxOut) {
		return false, nil
	}

	for i, txOut1 := range tx1.TxOut {
		txOut2 := tx2.TxOut[i]
		if txOut1.Value != txOut2.Value {
			return false, nil
		}
		if !bytes.Equal(txOut1.PkScript, txOut2.PkScript) {
			return false, nil
		}
	}

	return true, nil
}
