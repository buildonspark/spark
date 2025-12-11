package tokens

import (
	"context"
	"fmt"

	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/protoconverter"
	"github.com/lightsparkdev/spark/so/utils"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type BroadcastTokenHandler struct {
	config            *so.Config
	startTokenHandler *StartTokenTransactionHandler
	signTokenHandler  *SignTokenHandler
}

// NewBroadcastTokenHandler creates a new BroadcastTokenHandler.
func NewBroadcastTokenHandler(config *so.Config) *BroadcastTokenHandler {
	return &BroadcastTokenHandler{
		config:            config,
		startTokenHandler: NewStartTokenTransactionHandler(config),
		signTokenHandler:  NewSignTokenHandler(config),
	}
}

// BroadcastTokenTransaction combines start and commit into a single call for simplified transaction flows.
func (h *BroadcastTokenHandler) BroadcastTokenTransaction(
	ctx context.Context,
	req *tokenpb.BroadcastTransactionRequest,
) (*tokenpb.BroadcastTransactionResponse, error) {
	knobService := knobs.GetKnobsService(ctx)
	if knobService != nil && !knobService.RolloutRandom(knobs.KnobTokenTransactionV3Enabled, 0) {
		return nil, status.Error(codes.Unimplemented, "BroadcastTokenTransaction is not enabled")
	}

	partial := req.GetPartialTokenTransaction()
	if partial == nil {
		return nil, status.Error(codes.InvalidArgument, "partial token transaction is required")
	}
	if partial.GetVersion() < 3 {
		return nil, sparkerrors.InvalidArgumentMalformedField(
			fmt.Errorf("broadcast transaction requires version 3+ partial token transaction, got %d", partial.GetVersion()),
		)
	}

	startReq, err := protoconverter.ConvertBroadcastToStart(req)
	if err != nil {
		return nil, fmt.Errorf("failed to convert broadcast request to start request: %w", err)
	}

	startResponse, err := h.startTokenHandler.StartTokenTransaction(ctx, startReq)
	if err != nil {
		return nil, fmt.Errorf("failed to start transaction: %w", err)
	}

	// Persist the Start operation right away so later failures don't roll back the prepared state.
	if err := ent.DbCommit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit start transaction: %w", err)
	}

	finalTx := startResponse.GetFinalTokenTransaction()
	finalTxHash, err := utils.HashTokenTransaction(finalTx, false)
	if err != nil {
		return nil, fmt.Errorf("failed to hash final token transaction: %w", err)
	}

	commitReq := &tokenpb.CommitTransactionRequest{
		FinalTokenTransaction:          finalTx,
		FinalTokenTransactionHash:      finalTxHash,
		InputTtxoSignaturesPerOperator: nil,
		OwnerIdentityPublicKey:         req.GetIdentityPublicKey(),
	}

	commitResponse, err := h.signTokenHandler.CommitTransaction(ctx, commitReq)
	if err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	finalForResponse, err := protoconverter.ConvertV2TxShapeToFinal(finalTx)
	if err != nil {
		return nil, fmt.Errorf("failed to convert final transaction for response: %w", err)
	}

	return &tokenpb.BroadcastTransactionResponse{
		FinalTokenTransaction: finalForResponse,
		CommitStatus:          commitResponse.GetCommitStatus(),
		CommitProgress:        commitResponse.GetCommitProgress(),
		TokenIdentifier:       commitResponse.GetTokenIdentifier(),
	}, nil
}
