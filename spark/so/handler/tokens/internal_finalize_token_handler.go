package tokens

import (
	"context"
	stderrors "errors"
	"fmt"

	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/tokens"
)

type InternalFinalizeTokenHandler struct {
	config *so.Config
}

// NewInternalFinalizeTokenHandler creates a new InternalFinalizeTokenHandler.
func NewInternalFinalizeTokenHandler(config *so.Config) *InternalFinalizeTokenHandler {
	return &InternalFinalizeTokenHandler{
		config: config,
	}
}

func (h *InternalFinalizeTokenHandler) FinalizeTransferTransactionInternal(
	ctx context.Context,
	tokenTransactionHash []byte,
	revocationSecretsToFinalize []*ent.RecoveredRevocationSecret,
) error {
	ctx, span := GetTracer().Start(ctx, "InternalFinalizeTokenHandler.FinalizeTransferTransactionInternal")
	defer span.End()
	tokenTransaction, err := ent.FetchAndLockTokenTransactionDataByHash(ctx, tokenTransactionHash)
	if err != nil {
		return tokens.FormatErrorWithTransactionEnt(tokens.ErrFailedToFetchTransaction, tokenTransaction, err)
	}

	if tokenTransaction.Status != st.TokenTransactionStatusSigned && tokenTransaction.Status != st.TokenTransactionStatusRevealed {
		return tokens.FormatErrorWithTransactionEnt(
			fmt.Sprintf(tokens.ErrInvalidTransactionStatus,
				tokenTransaction.Status, fmt.Sprintf("%s or %s", st.TokenTransactionStatusSigned, st.TokenTransactionStatusRevealed)),
			tokenTransaction, nil)
	}
	invalidOutputs := validateOutputStatuses(tokenTransaction.Edges.CreatedOutput, st.TokenOutputStatusCreatedSigned)
	if len(tokenTransaction.Edges.SpentOutput) > 0 {
		invalidOutputs = append(invalidOutputs, validateInputStatuses(tokenTransaction.Edges.SpentOutput, st.TokenOutputStatusSpentSigned)...)
	}
	if len(invalidOutputs) > 0 {
		return tokens.FormatErrorWithTransactionEnt(tokens.ErrInvalidOutputs, tokenTransaction, stderrors.Join(invalidOutputs...))
	}
	if len(tokenTransaction.Edges.SpentOutput) != len(revocationSecretsToFinalize) {
		return tokens.FormatErrorWithTransactionEnt(
			fmt.Sprintf("number of revocation keys (%d) does not match number of spent outputs (%d)",
				len(revocationSecretsToFinalize),
				len(tokenTransaction.Edges.SpentOutput)),
			tokenTransaction, nil)
	}

	err = ent.FinalizeTransferTransactionWithRevocationKeys(ctx, tokenTransaction, revocationSecretsToFinalize)
	if err != nil {
		return tokens.FormatErrorWithTransactionEnt(fmt.Sprintf(tokens.ErrFailedToUpdateOutputs, "finalizing"), tokenTransaction, err)
	}
	return nil
}

// FinalizeMintOrCreateTransactionInternal finalizes a MINT or CREATE token transaction by hash.
// Fetches and locks the transaction, then delegates to FinalizeMintOrCreateTransaction.
// Unlike transfers, MINT/CREATE transactions don't have revocation secrets to exchange.
func (h *InternalFinalizeTokenHandler) FinalizeMintOrCreateTransactionInternal(
	ctx context.Context,
	tokenTransactionHash []byte,
) error {
	ctx, span := GetTracer().Start(ctx, "InternalFinalizeTokenHandler.FinalizeMintOrCreateTransactionInternal")
	defer span.End()

	tokenTransaction, err := ent.FetchAndLockTokenTransactionDataByHash(ctx, tokenTransactionHash)
	if err != nil {
		return tokens.FormatErrorWithTransactionEnt(tokens.ErrFailedToFetchTransaction, tokenTransaction, err)
	}

	return h.FinalizeMintOrCreateTransaction(ctx, tokenTransaction)
}

// FinalizeMintOrCreateTransaction finalizes a MINT or CREATE token transaction.
// Use this when you already have the entity loaded and locked.
// Unlike transfers, MINT/CREATE transactions don't have revocation secrets to exchange.
func (h *InternalFinalizeTokenHandler) FinalizeMintOrCreateTransaction(
	ctx context.Context,
	tokenTransaction *ent.TokenTransaction,
) error {
	ctx, span := GetTracer().Start(ctx, "InternalFinalizeTokenHandler.FinalizeMintOrCreateTransaction")
	defer span.End()

	// Idempotency: if already finalized, return success
	if tokenTransaction.Status == st.TokenTransactionStatusFinalized {
		return nil
	}

	if tokenTransaction.Status != st.TokenTransactionStatusSigned {
		return tokens.FormatErrorWithTransactionEnt(
			fmt.Sprintf(tokens.ErrInvalidTransactionStatus,
				tokenTransaction.Status, st.TokenTransactionStatusSigned),
			tokenTransaction, nil)
	}

	// Validate all created outputs are in CreatedSigned state
	invalidOutputs := validateOutputStatuses(tokenTransaction.Edges.CreatedOutput, st.TokenOutputStatusCreatedSigned)
	if len(invalidOutputs) > 0 {
		return tokens.FormatErrorWithTransactionEnt(tokens.ErrInvalidOutputs, tokenTransaction, stderrors.Join(invalidOutputs...))
	}

	err := ent.FinalizeMintOrCreateTransaction(ctx, tokenTransaction)
	if err != nil {
		return tokens.FormatErrorWithTransactionEnt(fmt.Sprintf(tokens.ErrFailedToUpdateOutputs, "finalizing"), tokenTransaction, err)
	}
	return nil
}
