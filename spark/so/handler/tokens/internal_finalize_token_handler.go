package tokens

import (
	"cmp"
	"context"
	stderrors "errors"
	"fmt"
	"slices"

	"github.com/lightsparkdev/spark/common/keys"

	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/protoconverter"
	"github.com/lightsparkdev/spark/so/tokens"
	"github.com/lightsparkdev/spark/so/utils"
	"google.golang.org/protobuf/types/known/emptypb"
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

func (h *InternalFinalizeTokenHandler) FinalizeTokenTransactionInternal(
	ctx context.Context,
	req *pb.FinalizeTokenTransactionRequest,
) (*emptypb.Empty, error) {
	tokenProtoTokenTransaction, err := protoconverter.TokenProtoFromSparkTokenTransaction(req.FinalTokenTransaction)
	if err != nil {
		return nil, fmt.Errorf("failed to convert token transaction to spark token transaction: %w", err)
	}
	ctx, span := tracer.Start(ctx, "InternalFinalizeTokenHandler.FinalizeTokenTransactionInternal", getTokenTransactionAttributes(tokenProtoTokenTransaction))
	defer span.End()
	tokenTransaction, err := ent.FetchAndLockTokenTransactionData(ctx, tokenProtoTokenTransaction)
	if err != nil {
		return nil, tokens.FormatErrorWithTransactionEnt(tokens.ErrFailedToFetchTransaction, tokenTransaction, err)
	}

	// Verify that the transaction is in a signed state before finalizing
	if tokenTransaction.Status != st.TokenTransactionStatusSigned {
		return nil, tokens.FormatErrorWithTransactionEnt(
			fmt.Sprintf(tokens.ErrInvalidTransactionStatus,
				tokenTransaction.Status, st.TokenTransactionStatusSigned),
			tokenTransaction, nil)
	}

	// Verify status of created outputs and spent outputs
	invalidOutputs := validateOutputStatuses(tokenTransaction.Edges.CreatedOutput, st.TokenOutputStatusCreatedSigned)
	if len(tokenTransaction.Edges.SpentOutput) > 0 {
		invalidOutputs = append(invalidOutputs, validateInputStatuses(tokenTransaction.Edges.SpentOutput, st.TokenOutputStatusSpentSigned)...)
	}

	if len(invalidOutputs) > 0 {
		return nil, tokens.FormatErrorWithTransactionEnt(tokens.ErrInvalidOutputs, tokenTransaction, stderrors.Join(invalidOutputs...))
	}

	if len(tokenTransaction.Edges.SpentOutput) != len(req.RevocationSecrets) {
		return nil, tokens.FormatErrorWithTransactionEnt(
			fmt.Sprintf("number of revocation keys (%d) does not match number of spent outputs (%d)",
				len(req.RevocationSecrets),
				len(tokenTransaction.Edges.SpentOutput)),
			tokenTransaction, nil)
	}
	revocationSecretMap := make(map[int][]byte)
	for _, revocationSecret := range req.RevocationSecrets {
		revocationSecretMap[int(revocationSecret.InputIndex)] = revocationSecret.RevocationSecret
	}
	// Validate that we have exactly one revocation secret for each input index
	// and that they form a contiguous sequence from 0 to len(tokenTransaction.Edges.SpentOutput)-1
	for i := 0; i < len(tokenTransaction.Edges.SpentOutput); i++ {
		if _, exists := revocationSecretMap[i]; !exists {
			return nil, tokens.FormatErrorWithTransactionEnt(
				fmt.Sprintf("missing revocation secret for input index %d", i),
				tokenTransaction, nil)
		}
	}

	revocationSecrets := make([]keys.Private, len(revocationSecretMap))
	revocationCommitments := make([]keys.Public, len(revocationSecretMap))

	spentOutputs := slices.SortedFunc(slices.Values(tokenTransaction.Edges.SpentOutput), func(a, b *ent.TokenOutput) int {
		return cmp.Compare(a.SpentTransactionInputVout, b.SpentTransactionInputVout)
	})

	// Match each output with its corresponding revocation secret
	for i, output := range spentOutputs {
		index := int(output.SpentTransactionInputVout)
		revocationSecret, exists := revocationSecretMap[index]
		if !exists {
			return nil, tokens.FormatErrorWithTransactionEnt(
				fmt.Sprintf("missing revocation secret for input at index %d", index),
				tokenTransaction, nil)
		}

		revocationPrivateKey, err := keys.ParsePrivateKey(revocationSecret)
		if err != nil {
			return nil, tokens.FormatErrorWithTransactionEnt(tokens.ErrFailedToParseRevocationPrivateKey, tokenTransaction, err)
		}

		revocationSecrets[i] = revocationPrivateKey
		commitment, err := keys.ParsePublicKey(output.WithdrawRevocationCommitment)
		if err != nil {
			return nil, tokens.FormatErrorWithTransactionEnt(tokens.ErrFailedToValidateRevocationKeys, tokenTransaction, err)
		}
		revocationCommitments[i] = commitment
	}

	err = utils.ValidateRevocationKeys(revocationSecrets, revocationCommitments)
	if err != nil {
		return nil, tokens.FormatErrorWithTransactionEnt(tokens.ErrFailedToValidateRevocationKeys, tokenTransaction, err)
	}

	err = ent.UpdateFinalizedTransaction(ctx, tokenTransaction, req.RevocationSecrets)
	if err != nil {
		return nil, tokens.FormatErrorWithTransactionEnt(fmt.Sprintf(tokens.ErrFailedToUpdateOutputs, "finalizing"), tokenTransaction, err)
	}

	return &emptypb.Empty{}, nil
}

func (h *InternalFinalizeTokenHandler) FinalizeCoordinatedTokenTransactionInternal(
	ctx context.Context,
	tokenTransactionHash []byte,
	revocationSecretsToFinalize []*ent.RecoveredRevocationSecret,
) error {
	ctx, span := tracer.Start(ctx, "InternalFinalizeTokenHandler.FinalizeCoordinatedTokenTransactionInternal")
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
