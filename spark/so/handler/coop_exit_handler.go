package handler

import (
	"context"
	"fmt"
	"log"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/authz"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/helper"
)

// CooperativeExitHandler tracks transfers
// and on-chain txs events for cooperative exits.
type CooperativeExitHandler struct {
	onchainHelper helper.OnChainHelper
	config        *so.Config
}

// NewCooperativeExitHandler creates a new CooperativeExitHandler.
func NewCooperativeExitHandler(onchainHelper helper.OnChainHelper, config *so.Config) *CooperativeExitHandler {
	return &CooperativeExitHandler{
		onchainHelper: onchainHelper,
		config:        config,
	}
}

// CooperativeExit signs refund transactions for leaves, spending connector outputs.
// It will lock the transferred leaves based on seeing a txid confirming on-chain.
func (h *CooperativeExitHandler) CooperativeExit(ctx context.Context, req *pb.CooperativeExitRequest) (*pb.CooperativeExitResponse, error) {
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, req.Transfer.OwnerIdentityPublicKey); err != nil {
		return nil, err
	}

	transferHandler := NewTransferHandler(h.onchainHelper, h.config)
	leafRefundMap := make(map[string][]byte)
	for _, job := range req.Transfer.LeavesToSend {
		leafRefundMap[job.LeafId] = job.RefundTxSigningJob.RawTx
	}

	transfer, leafMap, err := transferHandler.createTransfer(ctx, req.Transfer.TransferId, schema.TransferTypeCooperativeExit, req.Transfer.ExpiryTime.AsTime(), req.Transfer.OwnerIdentityPublicKey, req.Transfer.ReceiverIdentityPublicKey, leafRefundMap)
	if err != nil {
		return nil, fmt.Errorf("failed to create transfer: %v", err)
	}

	exitUUID, err := uuid.Parse(req.ExitId)
	if err != nil {
		return nil, fmt.Errorf("unable to parse exit_id %s: %v", req.ExitId, err)
	}

	db := ent.GetDbFromContext(ctx)
	_, err = db.CooperativeExit.Create().
		SetID(exitUUID).
		SetTransfer(transfer).
		SetExitTxid(req.ExitTxid).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create cooperative exit: %v", err)
	}

	transferProto, err := transfer.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal transfer: %v", err)
	}

	signingResults, err := signRefunds(ctx, h.config, req.Transfer.LeavesToSend, leafMap, nil)
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
		conn, err := common.NewGRPCConnection(operator.Address)
		if err != nil {
			log.Printf("Failed to connect to operator: %v", err)
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		return client.InitiateCooperativeExit(ctx, coopExitRequest)
	})
	return err
}
