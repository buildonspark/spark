package handler

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	"go.uber.org/zap"

	"github.com/google/uuid"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/pendingsendtransfer"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/partner"
)

// CooperativeExitHandler tracks transfers
// and on-chain txs events for cooperative exits.
type CooperativeExitHandler struct {
	config *so.Config
}

// NewCooperativeExitHandler creates a new CooperativeExitHandler.
func NewCooperativeExitHandler(config *so.Config) *CooperativeExitHandler {
	return &CooperativeExitHandler{
		config: config,
	}
}

// CooperativeExitV2 signs refund transactions for leaves, spending connector outputs.
// It will lock the transferred leaves based on seeing a txid confirming on-chain.
// It enforces the use of direct transactions for unilateral exits.
func (h *CooperativeExitHandler) CooperativeExitV2(ctx context.Context, req *pb.CooperativeExitRequest) (resp *pb.CooperativeExitResponse, retErr error) {
	reqTransferOwnerIdentityPubKey, err := keys.ParsePublicKey(req.Transfer.OwnerIdentityPublicKey)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("unable to parse transfer owner identity public key: %w", err))
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, reqTransferOwnerIdentityPubKey); err != nil {
		return nil, err
	}

	if req.Transfer.TransferPackage != nil {
		return h.cooperativeExitWithTransferPackage(ctx, req)
	}

	transferHandler := NewTransferHandler(h.config)

	cpfpLeafRefundMap := make(map[string][]byte)
	directLeafRefundMap := make(map[string][]byte)
	directFromCpfpLeafRefundMap := make(map[string][]byte)
	for _, job := range req.Transfer.LeavesToSend {
		cpfpLeafRefundMap[job.LeafId] = job.RefundTxSigningJob.RawTx
		if job.DirectRefundTxSigningJob != nil {
			directLeafRefundMap[job.LeafId] = job.DirectRefundTxSigningJob.RawTx
		}
		if job.DirectFromCpfpRefundTxSigningJob == nil {
			return nil, fmt.Errorf("DirectFromCpfpRefundTxSigningJob is required. Please upgrade to the latest SDK version")
		}
		directFromCpfpLeafRefundMap[job.LeafId] = job.DirectFromCpfpRefundTxSigningJob.RawTx
	}

	reqTransferReceiverIdentityPubKey, err := keys.ParsePublicKey(req.Transfer.ReceiverIdentityPublicKey)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("unable to parse transfer receiver identity public key: %w", err))
	}

	entTx, err := ent.GetTxFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get database transaction: %w", err)
	}

	transferUUID, err := uuid.Parse(req.Transfer.TransferId)
	if err != nil {
		return nil, fmt.Errorf("unable to parse transfer_id as a uuid %s: %w", req.Transfer.TransferId, err)
	}
	_, err = ent.CreateOrResetPendingSendTransfer(ctx, transferUUID)
	if err != nil {
		return nil, fmt.Errorf("unable to create pending send transfer: %w", err)
	}
	err = entTx.Commit()
	if err != nil {
		return nil, fmt.Errorf("unable to commit database transaction: %w", err)
	}

	// Rollback PendingSendTransfer on any failure between here and the success
	// point. cancelGossip is set to true before syncing to other SOs.
	needsRollback := true
	cancelGossip := false
	defer func() {
		if !needsRollback || retErr == nil {
			return
		}
		if rbErr := transferHandler.rollbackTransferInit(ctx, transferUUID, cancelGossip); rbErr != nil {
			retErr = fmt.Errorf("rollback failed: %w while processing coop exit %s: %w", rbErr, transferUUID, retErr)
		}
	}()

	transfer, leafMap, err := transferHandler.createTransfer(
		ctx,
		transferUUID,
		nil,
		st.TransferTypeCooperativeExit,
		req.Transfer.ExpiryTime.AsTime(),
		reqTransferOwnerIdentityPubKey,
		reqTransferReceiverIdentityPubKey,
		cpfpLeafRefundMap,
		directLeafRefundMap,
		directFromCpfpLeafRefundMap,
		nil,
		TransferRoleCoordinator,
		true,
		"",
		uuid.Nil,
		req.GetConnectorTx(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create transfer %s: %w", req.Transfer.TransferId, err)
	}

	exitUUID, err := uuid.Parse(req.ExitId)
	if err != nil {
		return nil, fmt.Errorf("unable to parse exit_id %x: %w", req.ExitId, err)
	}

	if len(req.ExitTxid) != 32 {
		return nil, fmt.Errorf("exit_txid %x is not 32 bytes", req.ExitTxid)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for transfer id %s exit txid %x: %w", req.Transfer.TransferId, req.ExitTxid, err)
	}

	exitTxid, err := st.NewTxIDFromBytes(req.ExitTxid)
	if err != nil {
		return nil, fmt.Errorf("failed to parse exit txid for transfer id %s exit txid %x: %w", req.Transfer.TransferId, req.ExitTxid, err)
	}

	_, err = db.CooperativeExit.Create().
		SetID(exitUUID).
		SetTransfer(transfer).
		SetExitTxid(exitTxid).
		// ConfirmationHeight is nil since the transaction is not confirmed yet.
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create cooperative exit for exit id %s exit txid %s: %w", req.ExitId, exitTxid.String(), err)
	}

	transferProto, err := transfer.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal transfer for transfer id %s exit id %s: %w", req.Transfer.TransferId, req.ExitId, err)
	}

	if len(req.GetConnectorTx()) == 0 {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("connector tx required for cooperative exit validation. Please upgrade to the latest SDK version"))
	}

	signingResults, err := signRefunds(ctx, h.config, req.Transfer, leafMap, keys.Public{}, keys.Public{}, keys.Public{}, req.GetConnectorTx())
	if err != nil {
		return nil, fmt.Errorf("failed to sign refund transactions for transfer id %s exit id %s: %w", req.Transfer.TransferId, req.ExitId, err)
	}

	cancelGossip = true
	err = transferHandler.syncCoopExitInit(ctx, req, nil, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to sync transfer init for transfer id %s exit id %s: %w", req.Transfer.TransferId, req.ExitId, err)
	}

	// After this point, the coop exit sync is considered successful.
	needsRollback = false

	// Commit the current transaction to persist the transfer data, ensuring
	// consistency with non-coordinator SOs.
	entTx, err = ent.GetTxFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get database transaction: %w", err)
	}
	if err = entTx.Commit(); err != nil {
		return nil, fmt.Errorf("unable to commit transfer data after successful sync: %w", err)
	}

	partner.SaveTransferPartner(ctx, transferUUID, st.TransferPartnerTypeCooperativeExit)

	// Mark PendingSendTransfer finished on success.
	db, err = ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get database context: %w", err)
	}
	_, err = db.PendingSendTransfer.Update().Where(pendingsendtransfer.TransferID(transferUUID)).SetStatus(st.PendingSendTransferStatusFinished).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update pending send transfer: %w", err)
	}

	response := &pb.CooperativeExitResponse{
		Transfer:       transferProto,
		SigningResults: signingResults,
	}
	return response, nil
}

// cooperativeExitWithTransferPackage handles the single-call cooperative exit flow where
// the client includes the TransferPackage directly. The SO aggregates signatures internally
// and syncs with other operators in one call, instead of requiring a separate
// FinalizeTransferWithTransferPackage call.
func (h *CooperativeExitHandler) cooperativeExitWithTransferPackage(ctx context.Context, req *pb.CooperativeExitRequest) (*pb.CooperativeExitResponse, error) {
	logger := logging.GetLoggerFromContext(ctx)
	transferHandler := NewTransferHandler(h.config)

	reqTransferOwnerIdentityPubKey, err := keys.ParsePublicKey(req.Transfer.OwnerIdentityPublicKey)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("unable to parse transfer owner identity public key: %w", err))
	}

	transferID, err := uuid.Parse(req.Transfer.TransferId)
	if err != nil {
		return nil, fmt.Errorf("unable to parse transfer_id as a uuid %s: %w", req.Transfer.TransferId, err)
	}

	leafTweakMap, err := transferHandler.ValidateTransferPackage(ctx, transferID, req.Transfer.TransferPackage, reqTransferOwnerIdentityPubKey, true)
	if err != nil {
		return nil, fmt.Errorf("failed to validate transfer package for coop exit %s: %w", transferID, err)
	}

	if len(req.GetConnectorTx()) == 0 {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("connector_tx is required for cooperative exit. Please upgrade to the latest SDK version"))
	}

	leafCpfpRefundMap, leafDirectRefundMap, leafDirectFromCpfpRefundMap := loadLeafRefundMaps(req.Transfer)

	reqTransferReceiverIdentityPubKey, err := keys.ParsePublicKey(req.Transfer.ReceiverIdentityPublicKey)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("unable to parse transfer receiver identity public key: %w", err))
	}

	// Mutual exclusivity
	if err := createPendingSendTransferAndCommit(ctx, transferID); err != nil {
		return nil, err
	}

	// Create transfer with key tweaks
	transfer, leafMap, err := transferHandler.createTransfer(
		ctx,
		transferID,
		nil,
		st.TransferTypeCooperativeExit,
		req.Transfer.ExpiryTime.AsTime(),
		reqTransferOwnerIdentityPubKey,
		reqTransferReceiverIdentityPubKey,
		leafCpfpRefundMap,
		leafDirectRefundMap,
		leafDirectFromCpfpRefundMap,
		leafTweakMap,
		TransferRoleCoordinator,
		true,
		"",
		uuid.Nil,
		req.GetConnectorTx(),
	)
	if err != nil {
		originalErr := err
		if rbErr := transferHandler.rollbackTransferInit(ctx, transferID, false /* cancelGossip */); rbErr != nil {
			return nil, fmt.Errorf("rollback failed: %w while creating transfer: %w", rbErr, originalErr)
		}
		return nil, fmt.Errorf("failed to create transfer for coop exit %s: %w", transferID, originalErr)
	}

	// Create cooperative exit record
	exitUUID, err := uuid.Parse(req.ExitId)
	if err != nil {
		return nil, fmt.Errorf("unable to parse exit_id %x: %w", req.ExitId, err)
	}
	if len(req.ExitTxid) != 32 {
		return nil, fmt.Errorf("exit_txid %x is not 32 bytes", req.ExitTxid)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get db for transfer id %s exit txid %x: %w", req.Transfer.TransferId, req.ExitTxid, err)
	}
	exitTxid, err := st.NewTxIDFromBytes(req.ExitTxid)
	if err != nil {
		return nil, fmt.Errorf("failed to parse exit txid for transfer id %s exit txid %x: %w", req.Transfer.TransferId, req.ExitTxid, err)
	}
	_, err = db.CooperativeExit.Create().
		SetID(exitUUID).
		SetTransfer(transfer).
		SetExitTxid(exitTxid).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create cooperative exit for exit id %s exit txid %s: %w", req.ExitId, exitTxid.String(), err)
	}

	// Sign refunds with pregenerated nonces, aggregate, and update leaves.
	refundSignatures, err := transferHandler.signAggregateAndUpdateRefunds(
		ctx, transfer, req.Transfer.GetTransferId(), req.Transfer.TransferPackage, leafMap,
		keys.Public{}, keys.Public{}, keys.Public{}, req.GetConnectorTx(),
	)
	if err != nil {
		return nil, fmt.Errorf("coop exit %s: %w", transferID, err)
	}

	// Sync with other operators
	err = transferHandler.syncCoopExitInit(ctx, req, refundSignatures.finalCpfpSignatureMap, refundSignatures.finalDirectSignatureMap, refundSignatures.finalDfcSignatureMap)
	if err != nil {
		syncErr := err
		logger.With(zap.Error(syncErr)).Sugar().Errorf("Failed to sync coop exit init for transfer %s", transferID)
		if rbErr := transferHandler.rollbackTransferInit(ctx, transferID, true /* cancelGossip */); rbErr != nil {
			return nil, fmt.Errorf("rollback failed: %w while syncing coop exit %s: %w", rbErr, transferID, syncErr)
		}
		return nil, fmt.Errorf("failed to sync coop exit init for transfer %s: %w", transferID, syncErr)
	}

	// Set coordinator key tweaks and update status
	err = transferHandler.setSoCoordinatorKeyTweaks(ctx, transfer, req.Transfer.TransferPackage, reqTransferOwnerIdentityPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to set coordinator key tweaks for coop exit %s: %w", transferID, err)
	}
	transfer, err = transfer.Update().SetStatus(st.TransferStatusSenderKeyTweakPending).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to update transfer status for coop exit %s: %w", transferID, err)
	}

	// Commit and update pending send transfer to finished
	entTx, err := ent.GetTxFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get database transaction: %w", err)
	}
	if err := entTx.Commit(); err != nil {
		return nil, fmt.Errorf("unable to commit database transaction: %w", err)
	}

	partner.SaveTransferPartner(ctx, transfer.ID, st.TransferPartnerTypeCooperativeExit)

	transfer, err = transferHandler.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to load transfer: %w", err)
	}

	db, err = ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get database transaction: %w", err)
	}
	_, err = db.PendingSendTransfer.Update().Where(pendingsendtransfer.TransferID(transfer.ID)).SetStatus(st.PendingSendTransferStatusFinished).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update pending send transfer: %w", err)
	}

	transferProto, err := transfer.MarshalProto(ctx)
	if err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Unable to marshal transfer %s", transfer.ID)
	}

	return &pb.CooperativeExitResponse{
		Transfer:       transferProto,
		SigningResults: nil,
	}, nil
}

func (h *TransferHandler) syncCoopExitInit(
	ctx context.Context,
	req *pb.CooperativeExitRequest,
	cpfpRefundSignatures map[string][]byte,
	directRefundSignatures map[string][]byte,
	directFromCpfpRefundSignatures map[string][]byte,
) error {
	transfer := req.Transfer

	initTransferRequest := &pbinternal.InitiateTransferRequest{
		TransferId:                transfer.TransferId,
		SenderIdentityPublicKey:   transfer.OwnerIdentityPublicKey,
		ReceiverIdentityPublicKey: transfer.ReceiverIdentityPublicKey,
		ExpiryTime:                transfer.ExpiryTime,
	}

	if transfer.TransferPackage != nil {
		initTransferRequest.TransferPackage = transfer.TransferPackage
		initTransferRequest.RefundSignatures = cpfpRefundSignatures
		initTransferRequest.DirectRefundSignatures = directRefundSignatures
		initTransferRequest.DirectFromCpfpRefundSignatures = directFromCpfpRefundSignatures
	} else {
		var leaves []*pbinternal.InitiateTransferLeaf
		for _, leaf := range transfer.LeavesToSend {
			var directRefundTx []byte
			var directFromCpfpRefundTx []byte
			if leaf.DirectRefundTxSigningJob != nil {
				directRefundTx = leaf.DirectRefundTxSigningJob.RawTx
			}
			if leaf.DirectFromCpfpRefundTxSigningJob != nil {
				directFromCpfpRefundTx = leaf.DirectFromCpfpRefundTxSigningJob.RawTx
			}
			leaves = append(leaves, &pbinternal.InitiateTransferLeaf{
				LeafId:                 leaf.LeafId,
				RawRefundTx:            leaf.RefundTxSigningJob.RawTx,
				DirectRefundTx:         directRefundTx,
				DirectFromCpfpRefundTx: directFromCpfpRefundTx,
			})
		}
		initTransferRequest.Leaves = leaves
	}

	coopExitRequest := &pbinternal.InitiateCooperativeExitRequest{
		Transfer:    initTransferRequest,
		ExitId:      req.ExitId,
		ExitTxid:    req.ExitTxid,
		ConnectorTx: req.GetConnectorTx(),
	}
	selection := helper.OperatorSelection{
		Option: helper.OperatorSelectionOptionExcludeSelf,
	}
	_, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		logger := logging.GetLoggerFromContext(ctx)

		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			logger.Error("Failed to connect to operator", zap.Error(err))
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		return client.InitiateCooperativeExit(ctx, coopExitRequest)
	})
	return err
}
