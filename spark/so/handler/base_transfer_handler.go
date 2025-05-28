package handler

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	eciesgo "github.com/ecies/go/v2"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/preimagerequest"
	"github.com/lightsparkdev/spark/so/ent/schema"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	enttransferleaf "github.com/lightsparkdev/spark/so/ent/transferleaf"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"google.golang.org/protobuf/proto"
)

type TransferRole int

const (
	// TransferRoleCoordinator is the role of the coordinator in a transfer.
	// The coordinator is reponsible to make sure that the transfer key tweak is applied to all other participants,
	// if the participants agree to the key tweak.
	TransferRoleCoordinator TransferRole = iota
	// TransferRoleParticipant is the role of a participant in a transfer.
	TransferRoleParticipant
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
		return fmt.Errorf("unable to parse receiver pubkey: %w", err)
	}
	recieverP2trScript, err := common.P2TRScriptFromPubKey(receiverIdentityPubkey)
	if err != nil {
		return fmt.Errorf("unable to generate p2tr script from receiver pubkey: %w", err)
	}
	if !bytes.Equal(recieverP2trScript, refundTx.TxOut[0].PkScript) {
		return fmt.Errorf("refund tx is expected to send to receiver identity pubkey")
	}
	return nil
}

func validateLeafRefundTxInput(refundTx *wire.MsgTx, oldSequence uint32, leafOutPoint *wire.OutPoint, expectedInputCount uint32) error {
	newTimeLock := refundTx.TxIn[0].Sequence & 0xFFFF
	oldTimeLock := oldSequence & 0xFFFF
	if newTimeLock+spark.TimeLockInterval > oldTimeLock {
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
		return fmt.Errorf("unable to load new refund tx: %w", err)
	}
	oldRefundTx, err := common.TxFromRawTxBytes(leaf.RawRefundTx)
	if err != nil {
		return fmt.Errorf("unable to load old refund tx: %w", err)
	}
	oldRefundTxIn := oldRefundTx.TxIn[0]
	leafOutPoint := wire.OutPoint{
		Hash:  oldRefundTxIn.PreviousOutPoint.Hash,
		Index: oldRefundTxIn.PreviousOutPoint.Index,
	}

	err = validateLeafRefundTxInput(newRefundTx, oldRefundTxIn.Sequence, &leafOutPoint, expectedInputCount)
	if err != nil {
		return fmt.Errorf("unable to validate refund tx inputs: %w", err)
	}

	err = validateLeafRefundTxOutput(newRefundTx, receiverIdentityKey)
	if err != nil {
		return fmt.Errorf("unable to validate refund tx output: %w", err)
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
	leafTweakMap map[string]*pbspark.SendLeafKeyTweak,
	role TransferRole,
) (*ent.Transfer, map[string]*ent.TreeNode, error) {
	transferUUID, err := uuid.Parse(transferID)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to parse transfer_id as a uuid %s: %w", transferID, err)
	}

	if expiryTime.Unix() != 0 && expiryTime.Before(time.Now()) {
		return nil, nil, fmt.Errorf("invalid expiry_time %s: %w", expiryTime.String(), err)
	}

	var status schema.TransferStatus
	if len(leafTweakMap) > 0 {
		if role == TransferRoleCoordinator {
			status = schema.TransferStatusSenderInitiatedCoordinator
		} else {
			status = schema.TransferStatusSenderKeyTweakPending
		}
	} else {
		status = schema.TransferStatusSenderInitiated
	}

	db := ent.GetDbFromContext(ctx)
	transfer, err := db.Transfer.Create().
		SetID(transferUUID).
		SetSenderIdentityPubkey(senderIdentityPublicKey).
		SetReceiverIdentityPubkey(receiverIdentityPublicKey).
		SetStatus(status).
		SetTotalValue(0).
		SetExpiryTime(expiryTime).
		SetType(transferType).
		Save(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create transfer: %w", err)
	}

	if len(leafRefundMap) == 0 {
		return nil, nil, errors.InvalidUserInputErrorf("must provide at least one leaf for transfer")
	}

	leaves, err := loadLeavesWithLock(ctx, db, leafRefundMap)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to load leaves: %w", err)
	}

	switch transferType {
	case schema.TransferTypeCooperativeExit:
		err = h.validateCooperativeExitLeaves(ctx, transfer, leaves, leafRefundMap, receiverIdentityPublicKey)
	case schema.TransferTypeTransfer:
		err = h.validateTransferLeaves(ctx, transfer, leaves, leafRefundMap, receiverIdentityPublicKey)
	case schema.TransferTypeUtxoSwap:
		err = h.validateUtxoSwapLeaves(ctx, transfer, leaves, leafRefundMap, receiverIdentityPublicKey)
	case schema.TransferTypePreimageSwap, schema.TransferTypeSwap, schema.TransferTypeCounterSwap:
		// do nothing
	}
	if err != nil {
		return nil, nil, fmt.Errorf("unable to validate transfer leaves: %w", err)
	}

	err = createTransferLeaves(ctx, db, transfer, leaves, leafRefundMap, leafTweakMap)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create transfer leaves: %w", err)
	}

	err = setTotalTransferValue(ctx, db, transfer, leaves)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to update transfer total value: %w", err)
	}

	leaves, err = lockLeaves(ctx, db, leaves)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to lock leaves: %w", err)
	}

	leafMap := make(map[string]*ent.TreeNode)
	for _, leaf := range leaves {
		leafMap[leaf.ID.String()] = leaf
	}

	return transfer, leafMap, nil
}

func loadLeavesWithLock(ctx context.Context, db *ent.Tx, leafRefundMap map[string][]byte) ([]*ent.TreeNode, error) {
	leaves := make([]*ent.TreeNode, 0)
	var network *schema.Network
	for leafID := range leafRefundMap {
		leafUUID, err := uuid.Parse(leafID)
		if err != nil {
			return nil, fmt.Errorf("unable to parse leaf_id %s: %w", leafID, err)
		}

		leaf, err := db.TreeNode.
			Query().
			Where(treenode.ID(leafUUID)).
			ForUpdate().
			Only(ctx)
		if err != nil || leaf == nil {
			return nil, fmt.Errorf("unable to find leaf %s: %w", leafID, err)
		}
		tree, err := leaf.QueryTree().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to find tree for leaf %s: %w", leafID, err)
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
			return fmt.Errorf("unable to validate refund tx for leaf %s: %w", leaf.ID, err)
		}
		err = h.leafAvailableToTransfer(ctx, leaf, transfer)
		if err != nil {
			return fmt.Errorf("unable to validate leaf %s: %w", leaf.ID, err)
		}
	}
	return nil
}

func (h *BaseTransferHandler) validateUtxoSwapLeaves(ctx context.Context, transfer *ent.Transfer, leaves []*ent.TreeNode, leafRefundMap map[string][]byte, receiverIdentityPublicKey []byte) error {
	for _, leaf := range leaves {
		rawRefundTx := leafRefundMap[leaf.ID.String()]
		err := validateSendLeafRefundTx(leaf, rawRefundTx, receiverIdentityPublicKey, 1)
		if err != nil {
			return fmt.Errorf("unable to validate refund tx for leaf %s: %w", leaf.ID, err)
		}
		err = h.leafAvailableToTransfer(ctx, leaf, transfer)
		if err != nil {
			return fmt.Errorf("unable to validate leaf %s: %w", leaf.ID, err)
		}
	}
	return nil
}

func (h *BaseTransferHandler) validateTransferLeaves(ctx context.Context, transfer *ent.Transfer, leaves []*ent.TreeNode, leafRefundMap map[string][]byte, receiverIdentityPublicKey []byte) error {
	for _, leaf := range leaves {
		rawRefundTx := leafRefundMap[leaf.ID.String()]
		err := validateSendLeafRefundTx(leaf, rawRefundTx, receiverIdentityPublicKey, 1)
		if err != nil {
			return fmt.Errorf("unable to validate refund tx for leaf %s: %w", leaf.ID, err)
		}
		err = h.leafAvailableToTransfer(ctx, leaf, transfer)
		if err != nil {
			return fmt.Errorf("unable to validate leaf %s: %w", leaf.ID, err)
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
				return fmt.Errorf("unable to find transfer leaf for leaf %s: %w", leaf.ID.String(), err)
			}
			now := time.Now()
			for _, transferLeaf := range transferLeaves {
				if transferLeaf.Edges.Transfer.Status == schema.TransferStatusSenderInitiated && transferLeaf.Edges.Transfer.ExpiryTime.Before(now) {
					_, err := h.CancelTransfer(ctx, &pbspark.CancelTransferRequest{
						TransferId:              transfer.ID.String(),
						SenderIdentityPublicKey: transfer.SenderIdentityPubkey,
					}, CancelTransferIntentTask)
					if err != nil {
						return fmt.Errorf("unable to cancel transfer: %w", err)
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
	leafTweakMap map[string]*pbspark.SendLeafKeyTweak,
) error {
	for _, leaf := range leaves {
		rawRefundTx := leafRefundMap[leaf.ID.String()]
		mutator := db.TransferLeaf.Create().
			SetTransfer(transfer).
			SetLeaf(leaf).
			SetPreviousRefundTx(leaf.RawRefundTx).
			SetIntermediateRefundTx(rawRefundTx)
		if leafTweakMap != nil {
			leafTweak, ok := leafTweakMap[leaf.ID.String()]
			if ok {
				leafTweakBinary, err := proto.Marshal(leafTweak)
				if err != nil {
					return fmt.Errorf("unable to marshal leaf tweak: %w", err)
				}
				mutator = mutator.SetKeyTweak(leafTweakBinary)
			}
		}
		_, err := mutator.Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to create transfer leaf: %w", err)
		}
	}
	return nil
}

func setTotalTransferValue(ctx context.Context, db *ent.Tx, transfer *ent.Transfer, leaves []*ent.TreeNode) error {
	totalAmount := getTotalTransferValue(leaves)
	_, err := db.Transfer.UpdateOne(transfer).SetTotalValue(totalAmount).Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update transfer total value: %w", err)
	}
	return nil
}

func getTotalTransferValue(leaves []*ent.TreeNode) uint64 {
	totalAmount := uint64(0)
	for _, leaf := range leaves {
		totalAmount += leaf.Value
	}
	return totalAmount
}

func lockLeaves(ctx context.Context, db *ent.Tx, leaves []*ent.TreeNode) ([]*ent.TreeNode, error) {
	lockedLeaves := make([]*ent.TreeNode, 0)
	for _, leaf := range leaves {
		lockedLeaf, err := db.TreeNode.UpdateOne(leaf).SetStatus(schema.TreeNodeStatusTransferLocked).Save(ctx)
		lockedLeaves = append(lockedLeaves, lockedLeaf)
		if err != nil {
			return nil, fmt.Errorf("unable to update leaf status: %w", err)
		}
	}
	return lockedLeaves, nil
}

type CancelTransferIntent int

const (
	CancelTransferIntentInternal CancelTransferIntent = iota
	CancelTransferIntentExternal
	CancelTransferIntentTask
	CancelTransferIntentInternalWithNotifyOtherOperators
)

func (h *BaseTransferHandler) CancelTransferInternal(
	ctx context.Context,
	transferID string,
	senderIdentityPublicKey []byte,
	intent CancelTransferIntent,
) (*pbspark.CancelTransferResponse, error) {
	if intent == CancelTransferIntentExternal || intent == CancelTransferIntentInternalWithNotifyOtherOperators {
		operatorSelection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
		_, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &operatorSelection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
			conn, err := operator.NewGRPCConnection()
			if err != nil {
				return nil, err
			}
			defer conn.Close()

			client := pbinternal.NewSparkInternalServiceClient(conn)
			_, err = client.CancelTransfer(ctx, &pbspark.CancelTransferRequest{
				TransferId:              transferID,
				SenderIdentityPublicKey: senderIdentityPublicKey,
			})
			if err != nil {
				return nil, fmt.Errorf("unable to cancel transfer: %w", err)
			}
			return nil, nil
		})
		if err != nil {
			return nil, fmt.Errorf("unable to cancel transfer: %w", err)
		}
	}

	transfer, err := h.loadTransfer(ctx, transferID)
	if err != nil {
		logger := logging.GetLoggerFromContext(ctx)
		logger.Info("Transfer not found", "transfer_id", transferID)
		return &pbspark.CancelTransferResponse{}, nil
	}
	if !bytes.Equal(transfer.SenderIdentityPubkey, senderIdentityPublicKey) {
		return nil, fmt.Errorf("only sender is eligible to cancel the transfer %s", transferID)
	}

	// The expiry time is only checked for coordinator SO because the creation time of each SO could be different.
	if intent == CancelTransferIntentExternal && transfer.Status != schema.TransferStatusSenderInitiated && transfer.ExpiryTime.After(time.Now()) {
		return nil, fmt.Errorf("transfer %s has not expired, expires at %s", transferID, transfer.ExpiryTime.String())
	}

	// Check to see if preimage has already been shared before cancelling
	// Only check external requests as there currently exists some internal
	// use case for cancelling transfers after preimage share, e.g. preimage
	// is incorrect
	if intent == CancelTransferIntentExternal {
		db := ent.GetDbFromContext(ctx)
		preimageRequest, err := db.PreimageRequest.Query().Where(preimagerequest.HasTransfersWith(enttransfer.ID(transfer.ID))).Only(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return nil, fmt.Errorf("encountered error when fetching preimage request for transfer id %s: %w", transferID, err)
		}
		if preimageRequest != nil && preimageRequest.Status == schema.PreimageRequestStatusPreimageShared {
			return nil, errors.FailedPreconditionErrorf("Cannot cancel an invoice whose preimage has already been revealed")
		}
	}

	err = h.executeCancelTransfer(ctx, transfer)
	if err != nil {
		logger := logging.GetLoggerFromContext(ctx)
		logger.Error("failed to cancel transfer", "error", err, "transfer_id", transferID)
		return nil, err
	}

	return &pbspark.CancelTransferResponse{}, nil
}

func (h *BaseTransferHandler) CancelTransfer(
	ctx context.Context,
	req *pbspark.CancelTransferRequest,
	intent CancelTransferIntent,
) (*pbspark.CancelTransferResponse, error) {
	if intent == CancelTransferIntentExternal {
		if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, req.SenderIdentityPublicKey); err != nil {
			return nil, err
		}
	}

	return h.CancelTransferInternal(ctx, req.TransferId, req.SenderIdentityPublicKey, intent)
}

func (h *BaseTransferHandler) executeCancelTransfer(ctx context.Context, transfer *ent.Transfer) error {
	// Don't error if the transfer is already returned.
	logger := logging.GetLoggerFromContext(ctx)
	if transfer.Status == schema.TransferStatusReturned {
		logger.Info("Transfer already returned", "transfer_id", transfer.ID.String())
		return nil
	}
	if transfer.Status != schema.TransferStatusSenderInitiated && transfer.Status != schema.TransferStatusSenderKeyTweakPending {
		return fmt.Errorf("transfer %s is expected to be at status TransferStatusSenderInitiated or TransferStatusSenderKeyTweakPending but %s found", transfer.ID.String(), transfer.Status)
	}
	var err error
	transfer, err = transfer.Update().SetStatus(schema.TransferStatusReturned).Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update transfer status: %w", err)
	}

	err = h.cancelTransferUnlockLeaves(ctx, transfer)
	if err != nil {
		return fmt.Errorf("unable to unlock leaves in the transfer: %w", err)
	}

	err = h.cancelTransferCancelRequest(ctx, transfer)
	if err != nil {
		return fmt.Errorf("unable to cancel associated request: %w", err)
	}

	return nil
}

func (h *BaseTransferHandler) cancelTransferUnlockLeaves(ctx context.Context, transfer *ent.Transfer) error {
	transferLeaves, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to get transfer leaves: %w", err)
	}

	for _, leaf := range transferLeaves {
		treenode, err := leaf.QueryLeaf().Only(ctx)
		if err != nil {
			return fmt.Errorf("unable to get tree node: %w", err)
		}
		_, err = treenode.Update().SetStatus(schema.TreeNodeStatusAvailable).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to update tree node status: %w", err)
		}
	}
	return nil
}

func (h *BaseTransferHandler) cancelTransferCancelRequest(ctx context.Context, transfer *ent.Transfer) error {
	if transfer.Type == schema.TransferTypePreimageSwap {
		db := ent.GetDbFromContext(ctx)
		preimageRequest, err := db.PreimageRequest.Query().Where(preimagerequest.HasTransfersWith(enttransfer.ID(transfer.ID))).Only(ctx)
		if err != nil || preimageRequest == nil {
			return fmt.Errorf("cannot find preimage request for transfer %s", transfer.ID.String())
		}
		err = preimageRequest.Update().SetStatus(schema.PreimageRequestStatusReturned).Exec(ctx)
		if err != nil {
			return fmt.Errorf("unable to update preimage request status: %w", err)
		}
	}
	return nil
}

func (h *BaseTransferHandler) loadTransfer(ctx context.Context, transferID string) (*ent.Transfer, error) {
	transferUUID, err := uuid.Parse(transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to parse transfer_id as a uuid %s: %w", transferID, err)
	}

	db := ent.GetDbFromContext(ctx)
	transfer, err := db.Transfer.Query().Where(enttransfer.ID(transferUUID)).ForUpdate().Only(ctx)
	if err != nil || transfer == nil {
		return nil, fmt.Errorf("unable to find transfer %s: %w", transferID, err)
	}
	return transfer, nil
}

func (h *BaseTransferHandler) loadTransferWithoutUpdate(ctx context.Context, transferID string) (*ent.Transfer, error) {
	transferUUID, err := uuid.Parse(transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to parse transfer_id as a uuid %s: %w", transferID, err)
	}

	db := ent.GetDbFromContext(ctx)
	transfer, err := db.Transfer.Query().Where(enttransfer.ID(transferUUID)).Only(ctx)
	if err != nil || transfer == nil {
		return nil, fmt.Errorf("unable to find transfer %s: %w", transferID, err)
	}
	return transfer, nil
}

// validateTransferPackage validates the transfer package, to ensure the key tweaks are valid.
func (h *BaseTransferHandler) validateTransferPackage(_ context.Context, transferID string, req *pbspark.TransferPackage, senderIdentityPublicKey []byte) (map[string]*pbspark.SendLeafKeyTweak, error) {
	// If the transfer package is nil, we don't need to validate it.
	if req == nil {
		return nil, nil
	}

	// Decrypt the key tweaks
	leafTweaksCipherText := req.KeyTweakPackage[h.config.Identifier]
	if leafTweaksCipherText == nil {
		return nil, fmt.Errorf("no key tweaks found for SO %s", h.config.Identifier)
	}

	decryptionPrivateKey := eciesgo.NewPrivateKeyFromBytes(h.config.IdentityPrivateKey)
	leafTweaksBinary, err := eciesgo.Decrypt(decryptionPrivateKey, leafTweaksCipherText)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt key tweaks: %w", err)
	}

	leafTweaks := &pbspark.SendLeafKeyTweaks{}
	err = proto.Unmarshal(leafTweaksBinary, leafTweaks)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal key tweaks: %w", err)
	}

	leafTweaksMap := make(map[string]*pbspark.SendLeafKeyTweak)
	for _, leafTweak := range leafTweaks.LeavesToSend {
		leafTweaksMap[leafTweak.LeafId] = leafTweak
	}

	transferIDUUID, err := uuid.Parse(transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to parse transfer_id as a uuid %s: %w", transferID, err)
	}
	payloadToVerify := common.GetTransferPackageSigningPayload(transferIDUUID, req)

	signature, err := ecdsa.ParseDERSignature(req.UserSignature)
	if err != nil {
		return nil, fmt.Errorf("unable to parse user signature: %w", err)
	}
	userPublicKey, err := secp256k1.ParsePubKey(senderIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse user public key: %w", err)
	}
	valid := signature.Verify(payloadToVerify, userPublicKey)
	if !valid {
		return nil, fmt.Errorf("invalid signature")
	}

	for _, leafTweak := range leafTweaksMap {
		err := secretsharing.ValidateShare(
			&secretsharing.VerifiableSecretShare{
				SecretShare: secretsharing.SecretShare{
					FieldModulus: secp256k1.S256().N,
					Threshold:    int(h.config.Threshold),
					Index:        big.NewInt(int64(h.config.Index + 1)),
					Share:        new(big.Int).SetBytes(leafTweak.SecretShareTweak.SecretShare),
				},
				Proofs: leafTweak.SecretShareTweak.Proofs,
			},
		)
		if err != nil {
			return nil, fmt.Errorf("unable to validate share: %w", err)
		}
	}

	return leafTweaksMap, nil
}

func (h *BaseTransferHandler) commitSenderKeyTweaks(ctx context.Context, transfer *ent.Transfer) error {
	if transfer.Status == schema.TransferStatusSenderKeyTweaked {
		return nil
	}
	if transfer.Status != schema.TransferStatusSenderKeyTweakPending && transfer.Status != schema.TransferStatusSenderInitiatedCoordinator {
		return fmt.Errorf("transfer %s is not in sender key tweak pending status", transfer.ID.String())
	}
	transferLeaves, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to get transfer leaves: %w", err)
	}
	for _, leaf := range transferLeaves {
		keyTweak := &pbspark.SendLeafKeyTweak{}
		err := proto.Unmarshal(leaf.KeyTweak, keyTweak)
		if err != nil {
			return fmt.Errorf("unable to unmarshal key tweak: %w", err)
		}
		treeNode, err := leaf.QueryLeaf().Only(ctx)
		if err != nil {
			return fmt.Errorf("unable to get tree node: %w", err)
		}
		err = helper.TweakLeafKey(ctx, treeNode, keyTweak, nil)
		if err != nil {
			return fmt.Errorf("unable to tweak leaf key: %w", err)
		}
		_, err = leaf.Update().
			SetKeyTweak(nil).
			SetSecretCipher(keyTweak.SecretCipher).
			SetSignature(keyTweak.Signature).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to update leaf key tweak: %w", err)
		}
	}
	_, err = transfer.Update().SetStatus(schema.TransferStatusSenderKeyTweaked).Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update transfer status: %w", err)
	}
	return nil
}
