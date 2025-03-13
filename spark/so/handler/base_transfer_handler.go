package handler

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pbspark "github.com/lightsparkdev/spark-go/proto/spark"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/authz"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/preimagerequest"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	enttransfer "github.com/lightsparkdev/spark-go/so/ent/transfer"
	enttransferleaf "github.com/lightsparkdev/spark-go/so/ent/transferleaf"
	"github.com/lightsparkdev/spark-go/so/ent/treenode"
	"github.com/lightsparkdev/spark-go/so/helper"
	"google.golang.org/protobuf/proto"
)

// BaseTransferHandler is the base transfer handler that is shared for internal and external transfer handlers.
type BaseTransferHandler struct {
	config *so.Config
}

// NewBaseTransferHandler creates a new BaseTransferHandler.
func NewBaseTransferHandler(config *so.Config) BaseTransferHandler {
	return BaseTransferHandler{
		config: config,
	}
}

func validateLeafRefundTxOutput(refundTx *wire.MsgTx, receiverIdentityPublicKey []byte) error {
	if len(refundTx.TxOut) == 0 {
		return fmt.Errorf("refund tx must have at least 1 output")
	}
	receiverIdentityPubkey, err := secp256k1.ParsePubKey(receiverIdentityPublicKey)
	if err != nil {
		return fmt.Errorf("unable to parse receiver pubkey: %v", err)
	}
	recieverP2trScript, err := common.P2TRScriptFromPubKey(receiverIdentityPubkey)
	if err != nil {
		return fmt.Errorf("unable to generate p2tr script from receiver pubkey: %v", err)
	}
	if !bytes.Equal(recieverP2trScript, refundTx.TxOut[0].PkScript) {
		return fmt.Errorf("refund tx is expected to send to receiver identity pubkey")
	}
	return nil
}

func validateLeafRefundTxInput(refundTx *wire.MsgTx, oldSequence uint32, leafOutPoint *wire.OutPoint, expectedInputCount uint32) error {
	newTimeLock := refundTx.TxIn[0].Sequence & 0xFFFF
	oldTimeLock := oldSequence & 0xFFFF
	if newTimeLock >= oldTimeLock {
		return fmt.Errorf("time lock on the new refund tx %d must be less than the old one %d", newTimeLock, oldTimeLock)
	}
	if len(refundTx.TxIn) != int(expectedInputCount) {
		return fmt.Errorf("refund tx should have %d inputs, but has %d", expectedInputCount, len(refundTx.TxIn))
	}
	if !refundTx.TxIn[0].PreviousOutPoint.Hash.IsEqual(&leafOutPoint.Hash) || refundTx.TxIn[0].PreviousOutPoint.Index != leafOutPoint.Index {
		return fmt.Errorf("unexpected input in refund tx")
	}
	return nil
}

func validateSendLeafRefundTx(leaf *ent.TreeNode, rawTx []byte, receiverIdentityKey []byte, expectedInputCount uint32) error {
	newRefundTx, err := common.TxFromRawTxBytes(rawTx)
	if err != nil {
		return fmt.Errorf("unable to load new refund tx: %v", err)
	}
	oldRefundTx, err := common.TxFromRawTxBytes(leaf.RawRefundTx)
	if err != nil {
		return fmt.Errorf("unable to load old refund tx: %v", err)
	}
	oldRefundTxIn := oldRefundTx.TxIn[0]
	leafOutPoint := wire.OutPoint{
		Hash:  oldRefundTxIn.PreviousOutPoint.Hash,
		Index: oldRefundTxIn.PreviousOutPoint.Index,
	}

	err = validateLeafRefundTxInput(newRefundTx, oldRefundTxIn.Sequence, &leafOutPoint, expectedInputCount)
	if err != nil {
		return fmt.Errorf("unable to validate refund tx inputs: %v", err)
	}

	err = validateLeafRefundTxOutput(newRefundTx, receiverIdentityKey)
	if err != nil {
		return fmt.Errorf("unable to validate refund tx output: %v", err)
	}

	return nil
}

func (h *BaseTransferHandler) createTransfer(
	ctx context.Context,
	transferID string,
	transferType schema.TransferType,
	expiryTime time.Time,
	senderIdentityPublicKey []byte,
	receiverIdentityPublicKey []byte,
	leafRefundMap map[string][]byte,
	senderKeyTweakProofs map[string]*pbspark.SecretProof,
) (*ent.Transfer, map[string]*ent.TreeNode, error) {
	transferUUID, err := uuid.Parse(transferID)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to parse transfer_id as a uuid %s: %v", transferID, err)
	}

	if expiryTime.Unix() != 0 && expiryTime.Before(time.Now()) {
		return nil, nil, fmt.Errorf("invalid expiry_time %s: %v", expiryTime.String(), err)
	}

	db := ent.GetDbFromContext(ctx)
	transfer, err := db.Transfer.Create().
		SetID(transferUUID).
		SetSenderIdentityPubkey(senderIdentityPublicKey).
		SetReceiverIdentityPubkey(receiverIdentityPublicKey).
		SetStatus(schema.TransferStatusSenderInitiated).
		SetTotalValue(0).
		SetExpiryTime(expiryTime).
		SetType(transferType).
		Save(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create transfer: %v", err)
	}

	leaves, err := loadLeaves(ctx, db, leafRefundMap)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to load leaves: %v", err)
	}

	switch transferType {
	case schema.TransferTypeCooperativeExit:
		err = h.validateCooperativeExitLeaves(ctx, transfer, leaves, leafRefundMap, receiverIdentityPublicKey)
	case schema.TransferTypeTransfer:
		err = h.validateTransferLeaves(ctx, transfer, leaves, leafRefundMap, receiverIdentityPublicKey)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("unable to validate transfer leaves: %v", err)
	}

	err = createTransferLeaves(ctx, db, transfer, leaves, leafRefundMap, senderKeyTweakProofs)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create transfer leaves: %v", err)
	}

	err = setTotalTransferValue(ctx, db, transfer, leaves)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to update transfer total value: %v", err)
	}

	leaves, err = lockLeaves(ctx, db, leaves)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to lock leaves: %v", err)
	}

	leafMap := make(map[string]*ent.TreeNode)
	for _, leaf := range leaves {
		leafMap[leaf.ID.String()] = leaf
	}

	return transfer, leafMap, nil
}

func loadLeaves(ctx context.Context, db *ent.Tx, leafRefundMap map[string][]byte) ([]*ent.TreeNode, error) {
	leaves := make([]*ent.TreeNode, 0)
	var network *schema.Network
	for leafID := range leafRefundMap {
		leafUUID, err := uuid.Parse(leafID)
		if err != nil {
			return nil, fmt.Errorf("unable to parse leaf_id %s: %v", leafID, err)
		}

		leaf, err := db.TreeNode.Get(ctx, leafUUID)
		if err != nil || leaf == nil {
			return nil, fmt.Errorf("unable to find leaf %s: %v", leafID, err)
		}
		tree, err := leaf.QueryTree().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to find tree for leaf %s: %v", leafID, err)
		}
		if network == nil {
			network = &tree.Network
		} else if tree.Network != *network {
			return nil, fmt.Errorf("leaves sent for transfer must be on the same network")
		}
		leaves = append(leaves, leaf)
	}
	return leaves, nil
}

func (h *BaseTransferHandler) validateCooperativeExitLeaves(ctx context.Context, transfer *ent.Transfer, leaves []*ent.TreeNode, leafRefundMap map[string][]byte, receiverIdentityPublicKey []byte) error {
	for _, leaf := range leaves {
		rawRefundTx := leafRefundMap[leaf.ID.String()]
		err := validateSendLeafRefundTx(leaf, rawRefundTx, receiverIdentityPublicKey, 2)
		if err != nil {
			return fmt.Errorf("unable to validate refund tx for leaf %s: %v", leaf.ID, err)
		}
		err = h.leafAvailableToTransfer(ctx, leaf, transfer)
		if err != nil {
			return fmt.Errorf("unable to validate leaf %s: %v", leaf.ID, err)
		}
	}
	return nil
}

func (h *BaseTransferHandler) validateTransferLeaves(ctx context.Context, transfer *ent.Transfer, leaves []*ent.TreeNode, leafRefundMap map[string][]byte, receiverIdentityPublicKey []byte) error {
	for _, leaf := range leaves {
		rawRefundTx := leafRefundMap[leaf.ID.String()]
		err := validateSendLeafRefundTx(leaf, rawRefundTx, receiverIdentityPublicKey, 1)
		if err != nil {
			return fmt.Errorf("unable to validate refund tx for leaf %s: %v", leaf.ID, err)
		}
		err = h.leafAvailableToTransfer(ctx, leaf, transfer)
		if err != nil {
			return fmt.Errorf("unable to validate leaf %s: %v", leaf.ID, err)
		}
	}
	return nil
}

func (h *BaseTransferHandler) leafAvailableToTransfer(ctx context.Context, leaf *ent.TreeNode, transfer *ent.Transfer) error {
	if leaf.Status != schema.TreeNodeStatusAvailable {
		if leaf.Status == schema.TreeNodeStatusTransferLocked {
			transferLeaves, err := transfer.QueryTransferLeaves().Where(
				enttransferleaf.HasLeafWith(treenode.IDEQ(leaf.ID)),
			).WithTransfer().All(ctx)
			if err != nil {
				return fmt.Errorf("unable to find transfer leaf for leaf %s: %v", leaf.ID.String(), err)
			}
			now := time.Now()
			for _, transferLeaf := range transferLeaves {
				if transferLeaf.Edges.Transfer.Status == schema.TransferStatusSenderInitiated && transferLeaf.Edges.Transfer.ExpiryTime.Before(now) {
					_, err := h.CancelSendTransfer(ctx, &pbspark.CancelSendTransferRequest{
						TransferId:              transfer.ID.String(),
						SenderIdentityPublicKey: transfer.SenderIdentityPubkey,
					}, CancelSendTransferIntentTask)
					if err != nil {
						return fmt.Errorf("unable to cancel transfer: %v", err)
					}
				}
			}
		}
		return fmt.Errorf("leaf %s is not available to transfer, status: %s", leaf.ID.String(), leaf.Status)
	}
	if !bytes.Equal(leaf.OwnerIdentityPubkey, transfer.SenderIdentityPubkey) {
		return fmt.Errorf("leaf %s is not owned by sender", leaf.ID.String())
	}
	return nil
}

func createTransferLeaves(
	ctx context.Context,
	db *ent.Tx,
	transfer *ent.Transfer,
	leaves []*ent.TreeNode,
	leafRefundMap map[string][]byte,
	senderKeyTweakProofs map[string]*pbspark.SecretProof,
) error {
	for _, leaf := range leaves {
		rawRefundTx := leafRefundMap[leaf.ID.String()]
		mutator := db.TransferLeaf.Create().
			SetTransfer(transfer).
			SetLeaf(leaf).
			SetPreviousRefundTx(leaf.RawRefundTx).
			SetIntermediateRefundTx(rawRefundTx)

		if senderKeyTweakProofs != nil {
			proof := senderKeyTweakProofs[leaf.ID.String()]
			proofBytes, err := proto.Marshal(proof)
			if err != nil {
				return fmt.Errorf("unable to marshal sender key tweak proof: %v", err)
			}
			mutator = mutator.SetSenderKeyTweakProof(proofBytes)
		}
		_, err := mutator.Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to create transfer leaf: %v", err)
		}
	}
	return nil
}

func setTotalTransferValue(ctx context.Context, db *ent.Tx, transfer *ent.Transfer, leaves []*ent.TreeNode) error {
	totalAmount := uint64(0)
	for _, leaf := range leaves {
		totalAmount += leaf.Value
	}
	_, err := db.Transfer.UpdateOne(transfer).SetTotalValue(totalAmount).Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update transfer total value: %v", err)
	}
	return nil
}

func lockLeaves(ctx context.Context, db *ent.Tx, leaves []*ent.TreeNode) ([]*ent.TreeNode, error) {
	lockedLeaves := make([]*ent.TreeNode, 0)
	for _, leaf := range leaves {
		lockedLeaf, err := db.TreeNode.UpdateOne(leaf).SetStatus(schema.TreeNodeStatusTransferLocked).Save(ctx)
		lockedLeaves = append(lockedLeaves, lockedLeaf)
		if err != nil {
			return nil, fmt.Errorf("unable to update leaf status: %v", err)
		}
	}
	return lockedLeaves, nil
}

type CancelSendTransferIntent int

const (
	CancelSendTransferIntentInternal CancelSendTransferIntent = iota
	CancelSendTransferIntentExternal
	CancelSendTransferIntentTask
)

func (h *BaseTransferHandler) CancelSendTransfer(
	ctx context.Context,
	req *pbspark.CancelSendTransferRequest,
	intent CancelSendTransferIntent,
) (*pbspark.CancelSendTransferResponse, error) {
	if intent == CancelSendTransferIntentExternal {
		if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, req.SenderIdentityPublicKey); err != nil {
			return nil, err
		}
	}

	transfer, err := h.loadTransfer(ctx, req.TransferId)
	if err != nil {
		return nil, fmt.Errorf("unable to load transfer %s: %v", req.TransferId, err)
	}
	if !bytes.Equal(transfer.SenderIdentityPubkey, req.SenderIdentityPublicKey) {
		return nil, fmt.Errorf("only sender is eligible to cancel the transfer %s", req.TransferId)
	}
	// Don't error if the transfer is already returned.
	if transfer.Status == schema.TransferStatusReturned {
		return &pbspark.CancelSendTransferResponse{}, nil
	}
	if transfer.Status != schema.TransferStatusSenderInitiated && transfer.Status != schema.TransferStatusSenderKeyTweakPending {
		return nil, fmt.Errorf("transfer %s is expected to be at status TransferStatusSenderInitiated or TransferStatusSenderKeyTweakPending but %s found", req.TransferId, transfer.Status)
	}
	if intent == CancelSendTransferIntentExternal && transfer.ExpiryTime.After(time.Now()) {
		return nil, fmt.Errorf("transfer %s has not expired, expires at %s", req.TransferId, transfer.ExpiryTime.String())
	}

	transfer, err = transfer.Update().SetStatus(schema.TransferStatusReturned).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update transfer status: %v", err)
	}

	err = h.cancelTransferUnlockLeaves(ctx, transfer)
	if err != nil {
		return nil, fmt.Errorf("unable to unlock leaves in the transfer: %v", err)
	}

	err = h.cancelTransferCancelRequest(ctx, transfer, intent)
	if err != nil {
		return nil, fmt.Errorf("unable to cancel associated request: %v", err)
	}

	if intent != CancelSendTransferIntentInternal {
		operatorSelection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
		_, err = helper.ExecuteTaskWithAllOperators(ctx, h.config, &operatorSelection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
			conn, err := operator.NewGRPCConnection()
			if err != nil {
				return nil, err
			}
			defer conn.Close()

			client := pbinternal.NewSparkInternalServiceClient(conn)
			_, err = client.CancelSendTransfer(ctx, req)
			if err != nil {
				return nil, fmt.Errorf("unable to cancel transfer: %v", err)
			}
			return nil, nil
		})
		if err != nil {
			return nil, fmt.Errorf("unable to execute task with all operators: %v", err)
		}
	}

	return &pbspark.CancelSendTransferResponse{}, nil
}

func (h *BaseTransferHandler) cancelTransferUnlockLeaves(ctx context.Context, transfer *ent.Transfer) error {
	transferLeaves, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to get transfer leaves: %v", err)
	}

	for _, leaf := range transferLeaves {
		treenode, err := leaf.QueryLeaf().Only(ctx)
		if err != nil {
			return fmt.Errorf("unable to get tree node: %v", err)
		}
		_, err = treenode.Update().SetStatus(schema.TreeNodeStatusAvailable).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to update tree node status: %v", err)
		}
	}
	return nil
}

func (h *BaseTransferHandler) cancelTransferCancelRequest(ctx context.Context, transfer *ent.Transfer, intent CancelSendTransferIntent) error {
	if transfer.Type == schema.TransferTypePreimageSwap {
		db := ent.GetDbFromContext(ctx)
		preimageRequest, err := db.PreimageRequest.Query().Where(preimagerequest.HasTransfersWith(enttransfer.ID(transfer.ID))).Only(ctx)
		if err != nil || preimageRequest == nil {
			return fmt.Errorf("cannot find preimage request for transfer %s", transfer.ID.String())
		}
		if intent != CancelSendTransferIntentInternal && preimageRequest.Status != schema.PreimageRequestStatusWaitingForPreimage {
			return fmt.Errorf("preimage request is not in the waiting for preimage status")
		}
		err = preimageRequest.Update().SetStatus(schema.PreimageRequestStatusReturned).Exec(ctx)
		if err != nil {
			return fmt.Errorf("unable to update preimage request status: %v", err)
		}
	}
	return nil
}

func (h *BaseTransferHandler) loadTransfer(ctx context.Context, transferID string) (*ent.Transfer, error) {
	transferUUID, err := uuid.Parse(transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to parse transfer_id as a uuid %s: %v", transferID, err)
	}

	db := ent.GetDbFromContext(ctx)
	transfer, err := db.Transfer.Query().Where(enttransfer.ID(transferUUID)).ForUpdate().Only(ctx)
	if err != nil || transfer == nil {
		return nil, fmt.Errorf("unable to find transfer %s: %v", transferID, err)
	}
	return transfer, nil
}

func (h *BaseTransferHandler) loadTransferWithoutUpdate(ctx context.Context, transferID string) (*ent.Transfer, error) {
	transferUUID, err := uuid.Parse(transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to parse transfer_id as a uuid %s: %v", transferID, err)
	}

	db := ent.GetDbFromContext(ctx)
	transfer, err := db.Transfer.Query().Where(enttransfer.ID(transferUUID)).Only(ctx)
	if err != nil || transfer == nil {
		return nil, fmt.Errorf("unable to find transfer %s: %v", transferID, err)
	}
	return transfer, nil
}
