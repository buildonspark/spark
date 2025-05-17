package handler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/schema"
	"github.com/lightsparkdev/spark/so/helper"
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

// CooperativeExit signs refund transactions for leaves, spending connector outputs.
// It will lock the transferred leaves based on seeing a txid confirming on-chain.
func (h *CooperativeExitHandler) CooperativeExit(ctx context.Context, req *pb.CooperativeExitRequest) (*pb.CooperativeExitResponse, error) {
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, req.Transfer.OwnerIdentityPublicKey); err != nil {
		return nil, err
	}

	transferHandler := NewTransferHandler(h.config)
	leafRefundMap := make(map[string][]byte)
	for _, job := range req.Transfer.LeavesToSend {
		leafRefundMap[job.LeafId] = job.RefundTxSigningJob.RawTx
	}

	transfer, leafMap, err := transferHandler.createTransfer(
		ctx,
		req.Transfer.TransferId,
		schema.TransferTypeCooperativeExit,
		req.Transfer.ExpiryTime.AsTime(),
		req.Transfer.OwnerIdentityPublicKey,
		req.Transfer.ReceiverIdentityPublicKey,
		leafRefundMap,
		nil,
		TransferRoleCoordinator,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create transfer: %v", err)
	}

	exitUUID, err := uuid.Parse(req.ExitId)
	if err != nil {
		return nil, fmt.Errorf("unable to parse exit_id %s: %v", req.ExitId, err)
	}

	if len(req.ExitTxid) != 32 {
		return nil, fmt.Errorf("exit_txid is not 32 bytes: %v", req.ExitTxid)
	}

	db := ent.GetDbFromContext(ctx)
	_, err = db.CooperativeExit.Create().
		SetID(exitUUID).
		SetTransfer(transfer).
		SetExitTxid(req.ExitTxid).
		// ConfirmationHeight is nil since the transaction is not confirmed yet.
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create cooperative exit: %v", err)
	}

	transferProto, err := transfer.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal transfer: %v", err)
	}

	signingResults, err := signRefunds(ctx, h.config, req.Transfer, leafMap, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to sign refund transactions: %v", err)
	}

	err = transferHandler.syncCoopExitInit(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to sync transfer init: %v", err)
	}

	response := &pb.CooperativeExitResponse{
		Transfer:       transferProto,
		SigningResults: signingResults,
	}
	return response, nil
}

func (h *TransferHandler) syncCoopExitInit(ctx context.Context, req *pb.CooperativeExitRequest) error {
	transfer := req.Transfer
	leaves := make([]*pbinternal.InitiateTransferLeaf, 0)
	for _, leaf := range transfer.LeavesToSend {
		leaves = append(leaves, &pbinternal.InitiateTransferLeaf{
			LeafId:      leaf.LeafId,
			RawRefundTx: leaf.RefundTxSigningJob.RawTx,
		})
	}
	initTransferRequest := &pbinternal.InitiateTransferRequest{
		TransferId:                transfer.TransferId,
		SenderIdentityPublicKey:   transfer.OwnerIdentityPublicKey,
		ReceiverIdentityPublicKey: transfer.ReceiverIdentityPublicKey,
		ExpiryTime:                transfer.ExpiryTime,
		Leaves:                    leaves,
	}
	coopExitRequest := &pbinternal.InitiateCooperativeExitRequest{
		Transfer: initTransferRequest,
		ExitId:   req.ExitId,
		ExitTxid: req.ExitTxid,
	}
	selection := helper.OperatorSelection{
		Option: helper.OperatorSelectionOptionExcludeSelf,
	}
	_, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		logger := logging.GetLoggerFromContext(ctx)

		conn, err := operator.NewGRPCConnection()
		if err != nil {
			logger.Error("Failed to connect to operator", "error", err)
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		return client.InitiateCooperativeExit(ctx, coopExitRequest)
	})
	return err
}
