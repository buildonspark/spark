package tokens

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/errors"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/tokens"
	"github.com/lightsparkdev/spark/so/utils"
)

// validateStatuses is a shared helper that checks if all provided outputs have one of the
// expected statuses. The idFormatter formats the identifier used in error messages
// (e.g., "output 0" or "input <id>").
func validateStatuses(
	outputs []*ent.TokenOutput,
	idFormatter func(i int, output *ent.TokenOutput) string,
	expectedStatuses ...st.TokenOutputStatus,
) []error {
	var invalidOutputs []error
	for i, output := range outputs {
		matchesExpected := false
		for _, status := range expectedStatuses {
			if output.Status == status {
				matchesExpected = true
				break
			}
		}
		if !matchesExpected {
			var expectedDesc string
			if len(expectedStatuses) == 1 {
				expectedDesc = fmt.Sprintf("%s", expectedStatuses[0])
			} else {
				parts := make([]string, len(expectedStatuses))
				for i, s := range expectedStatuses {
					parts[i] = fmt.Sprintf("%s", s)
				}
				expectedDesc = fmt.Sprintf("one of [%s]", strings.Join(parts, " or "))
			}
			invalidOutputs = append(invalidOutputs, fmt.Errorf("%s has invalid status %s, expected %s",
				idFormatter(i, output), output.Status, expectedDesc))
		}
	}
	return invalidOutputs
}

// validateOutputStatuses checks if all created outputs have one of the expected statuses
func validateOutputStatuses(outputs []*ent.TokenOutput, expectedStatuses ...st.TokenOutputStatus) []error {
	return validateStatuses(outputs, func(i int, _ *ent.TokenOutput) string {
		return fmt.Sprintf("output %d", i)
	}, expectedStatuses...)
}

// validateInputStatuses checks if all spent outputs have one of the expected statuses and aren't withdrawn
func validateInputStatuses(outputs []*ent.TokenOutput, expectedStatuses ...st.TokenOutputStatus) []error {
	return validateStatuses(outputs, func(_ int, output *ent.TokenOutput) string {
		return fmt.Sprintf("input %x", output.ID)
	}, expectedStatuses...)
}

// validateTokenTransactionForSigning validates a token transaction for signing.
// It verifies status, non-expiration, spent and created output statuses, and transaction specific conditions.
func validateTokenTransactionForSigning(ctx context.Context, config *so.Config, tokenTransactionEnt *ent.TokenTransaction) error {
	if tokenTransactionEnt.Status != st.TokenTransactionStatusStarted &&
		tokenTransactionEnt.Status != st.TokenTransactionStatusSigned {
		return fmt.Errorf("signing failed because transaction is not in correct state, expected %s or %s, current status: %s", st.TokenTransactionStatusStarted, st.TokenTransactionStatusSigned, tokenTransactionEnt.Status)
	}

	// Get the network-specific transaction expiry duration
	schemaNetwork, err := tokenTransactionEnt.GetNetworkFromEdges()
	if err != nil {
		return err
	}
	network, err := common.NetworkFromSchemaNetwork(schemaNetwork)
	if err != nil {
		return err
	}
	transactionV0ExpiryDuration := config.Lrc20Configs[network.String()].TransactionExpiryDuration

	if err := tokenTransactionEnt.ValidateNotExpired(transactionV0ExpiryDuration); err != nil {
		return err
	}

	// The outputs should almost always be in Started but we also allow Signed in order to allow signing retry in case
	// an earlier coordinated sign attempt failed or in the case of an unexpected operator race.
	invalidOutputs := validateOutputStatuses(tokenTransactionEnt.Edges.CreatedOutput, st.TokenOutputStatusCreatedStarted, st.TokenOutputStatusCreatedSigned)
	if len(invalidOutputs) > 0 {
		return fmt.Errorf("%s: %w", tokens.ErrInvalidOutputs, stderrors.Join(invalidOutputs...))
	}

	// Type-specific validations
	txType := tokenTransactionEnt.InferTokenTransactionTypeEnt()
	switch txType {
	case utils.TokenTransactionTypeCreate:
		if tokenTransactionEnt.Edges.Create == nil {
			return sparkerrors.InternalDatabaseMissingEdge(fmt.Errorf("create input ent not found when attempting to sign create transaction"))
		}
	case utils.TokenTransactionTypeMint:
		// For mint transactions, validate that the mint does not exceed the max supply.
		// This is also checked during the Start() step, but we check before signing as well
		// in case two transactions are started at once.
		if err := tokens.ValidateMintDoesNotExceedMaxSupplyEnt(ctx, tokenTransactionEnt); err != nil {
			return err
		}
	case utils.TokenTransactionTypeTransfer:
		// If token outputs are being spent, verify the expected status of inputs and check for active freezes.
		if len(tokenTransactionEnt.Edges.SpentOutput) == 0 {
			return sparkerrors.InternalDatabaseMissingEdge(fmt.Errorf("no spent outputs found when attempting to validate transfer transaction"))
		}

		invalidInputs := validateInputStatuses(tokenTransactionEnt.Edges.SpentOutput, st.TokenOutputStatusSpentStarted, st.TokenOutputStatusSpentSigned)
		if len(invalidInputs) > 0 {
			return fmt.Errorf("%s: %w", tokens.ErrInvalidInputs, stderrors.Join(invalidInputs...))
		}

		// Collect owner public keys for freeze check.
		ownerPublicKeys := make([]keys.Public, len(tokenTransactionEnt.Edges.SpentOutput))
		tokenCreateId := tokenTransactionEnt.Edges.SpentOutput[0].TokenCreateID
		if tokenCreateId == uuid.Nil {
			return fmt.Errorf("no created token found when attempting to validate transfer transaction")
		}
		for i, output := range tokenTransactionEnt.Edges.SpentOutput {
			ownerPublicKeys[i] = output.OwnerPublicKey
		}

		// Bulk query all input ids to ensure none of them are frozen.
		activeFreezes, err := ent.GetActiveFreezes(ctx, ownerPublicKeys, tokenCreateId)
		if err != nil {
			return fmt.Errorf("%s: %w", tokens.ErrFailedToQueryTokenFreezeStatus, err)
		}

		if len(activeFreezes) > 0 {
			for _, freeze := range activeFreezes {
				logger := logging.GetLoggerFromContext(ctx)
				logger.Sugar().Infof(
					"Found active freeze for owner %x (token: %x, timestamp: %d)",
					freeze.OwnerPublicKey,
					freeze.TokenPublicKey,
					freeze.WalletProvidedFreezeTimestamp,
				)
			}
			return errors.FailedPreconditionTokenRulesViolation(fmt.Errorf("at least one input is frozen. Cannot proceed with transaction"))
		}
	default:
		return fmt.Errorf("token transaction type unknown")
	}

	return nil
}
