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

func (h *InternalFinalizeTokenHandler) FinalizeCoordinatedTokenTransactionInternal(
	ctx context.Context,
	tokenTransactionHash []byte,
	revocationSecretsToFinalize []*ent.RecoveredRevocationSecret,
) error {
	ctx, span := GetTracer().Start(ctx, "InternalFinalizeTokenHandler.FinalizeCoordinatedTokenTransactionInternal")
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

	err = ent.FinalizeCoordinatedTokenTransactionWithRevocationKeys(ctx, tokenTransaction, revocationSecretsToFinalize)
	if err != nil {
		return tokens.FormatErrorWithTransactionEnt(fmt.Sprintf(tokens.ErrFailedToUpdateOutputs, "finalizing"), tokenTransaction, err)
	}
	return nil
}
