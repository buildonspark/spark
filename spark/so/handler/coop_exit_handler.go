package handler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
)

// CooperativeExitHandler tracks transfers
// and on-chain txs events for cooperative exits.
type CooperativeExitHandler struct {
	config *so.Config
	// leaf to UTXO map, i.e. leaves are locked to a certain UTXO (put this in the DB somewhere)
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
	transferHandler := BaseTransferHandler{config: h.config}
	leafRefundMap := make(map[string][]byte)
	for _, job := range req.Transfer.LeavesToSend {
		leafRefundMap[job.LeafId] = job.RefundTxSigningJob.RawTx
	}

	transfer, leafMap, err := transferHandler.createTransfer(ctx, req.Transfer.TransferId, req.Transfer.ExpiryTime.AsTime(), req.Transfer.OwnerIdentityPublicKey, req.Transfer.ReceiverIdentityPublicKey, leafRefundMap, true)
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

	signingResults, err := signRefunds(ctx, h.config, req.Transfer.LeavesToSend, leafMap)
	if err != nil {
		return nil, fmt.Errorf("failed to sign refund transactions: %v", err)
	}

	response := &pb.CooperativeExitResponse{
		SigningResults: signingResults,
	}
	return response, nil
}
