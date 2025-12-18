package tokens

import (
	"encoding/hex"
	"errors"
	"fmt"

	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/utils"

	"github.com/lightsparkdev/spark/so/ent"
)

type readableSpentOutput struct {
	PrevHash string `json:"prev_hash"`
	Vout     uint32 `json:"vout"`
}

type readableCreatedOutput struct {
	OutputId        string `json:"output_id"`
	TokenIdentifier string `json:"token_identifier"`
}

const (
	ErrIdentityPublicKeyAuthFailed        = "identity public key authentication failed"
	ErrInvalidPartialTokenTransaction     = "invalid partial token transaction"
	ErrFailedToFetchPartialTransaction    = "failed to fetch partial token transaction data"
	ErrFailedToFetchTransaction           = "failed to fetch transaction"
	ErrFailedToGetUnusedKeyshares         = "failed to get unused signing keyshares"
	ErrNotEnoughUnusedKeyshares           = "not enough unused signing keyshares available"
	ErrFailedToGetNetworkFromProto        = "failed to get network from proto network"
	ErrFailedToExecuteWithNonCoordinator  = "failed to execute start token transaction with non-coordinator operators"
	ErrFailedToExecuteWithCoordinator     = "failed to execute start token transaction with coordinator"
	ErrFailedToGetKeyshareInfo            = "failed to get keyshare info"
	ErrFailedToGetCreationEntityPublicKey = "failed to get creation entity public key"
	ErrFailedToConnectToOperator          = "failed to connect to operator: %s"
	ErrFailedToExecuteWithOperator        = "failed to execute start token transaction with operator: %s"
	ErrFailedToGetOperatorList            = "failed to get operator list"
	ErrFailedToSendToLRC20Node            = "failed to send transaction to LRC20 node"
	ErrFailedToUpdateOutputs              = "failed to update outputs after %s"
	ErrFailedToGetKeyshareForOutput       = "failed to get keyshare for output"
	ErrFailedToQueryTokenFreezeStatus     = "failed to query token freeze status"
	ErrTransactionNotCoordinatedBySO      = "transaction not coordinated by this SO"
	ErrFailedToGetOwnedOutputStats        = "failed to get owned output stats"
	ErrFailedToParseRevocationPrivateKey  = "failed to parse revocation private key"
	ErrFailedToValidateRevocationKeys     = "failed to validate revocation keys"
	ErrRevocationKeyMismatch              = "keyshare public key does not match output revocation commitment"
	ErrInvalidOutputs                     = "found invalid outputs"
	ErrInvalidInputs                      = "found invalid inputs"
	ErrFailedToMarshalTokenTransaction    = "failed to marshal token transaction"
	ErrMultipleActiveFreezes              = "multiple active freezes found for this owner and token which should not happen"
	ErrNoActiveFreezes                    = "no active freezes found to thaw"
	ErrAlreadyFrozen                      = "tokens are already frozen for this owner and token"
	ErrFailedToCreateTokenFreeze          = "failed to create token freeze entity"
	ErrFailedToUpdateTokenFreeze          = "failed to update token freeze status to thawed"
	ErrInvalidOutputIDFormat              = "invalid output ID format"
	ErrFailedToQueryTokenTransactions     = "unable to query token transactions"
	ErrInvalidOperatorResponse            = "invalid response from operator"
	ErrTransactionAlreadyBroadcasted      = "transaction was already broadcasted. if retrying consider updating the client created timestamp"
	ErrTransactionAlreadyFinalized        = "transaction has already been finalized by at least one operator, cannot cancel"
	ErrTooManyOperatorsSigned             = "transaction has been signed by %d operators, which exceeds the cancellation threshold of %d"
	ErrInvalidTransactionStatus           = "transaction is in status %s, but must be in %s status to cancel"
	ErrStoredOperatorSignatureInvalid     = "stored operator signature is invalid"
	ErrTokenNotFreezable                  = "token is not configured to be freezable"
	ErrFailedToGetRevocationKeyshares     = "failed to get revocation keyshares for transaction"
	ErrFailedToConnectToOperatorForCancel = "failed to connect to operator %s"
	ErrFailedToQueryOperatorForCancel     = "failed to execute query with operator %s"
	ErrFailedToExecuteWithAllOperators    = "failed to execute query with all operators"
	ErrInputIndexOutOfRange               = "input index %d out of range (0-%d)"
	ErrInvalidOwnerSignature              = "invalid owner signature for output"
	ErrInvalidIssuerSignature             = "invalid issuer signature for mint"
	ErrFailedToHashRevocationKeyshares    = "failed to hash revocation keyshares payload"
	ErrTransactionHashMismatch            = "transaction hash in payload (%x) does not match actual transaction hash (%x)"
	ErrOperatorPublicKeyMismatch          = "operator identity public key in payload (%v) does not match this SO's identity public key (%v)"
	ErrInvalidValidityDuration            = "invalid validity duration"
	ErrTransactionPreemptedByExisting     = "transaction pre-empted by existing transaction due to existing transaction having %s (%s)"
	ErrFailedToCancelPreemptedTransaction = "failed to cancel pre-empted transaction"
	ErrFailedToConvertTokenProto          = "failed to convert token proto to spark proto (%s->%s)"
	ErrTokenAlreadyCreatedForIssuer       = "token already created for this issuer"
	ErrFailedToDecodeSparkInvoice         = "failed to decode spark invoice"
	ErrInvalidSparkInvoice                = "invalid spark invoice"
	ErrSparkInvoiceExpired                = "spark invoice expired"
	ErrTransactionPreempted               = "transaction preempted"
)

func FormatErrorWithTransactionEnt(msg string, tokenTransaction *ent.TokenTransaction, err error) error {
	if tokenTransaction == nil {
		return fmt.Errorf("nil token transaction in format error with transaction ent: message: %s, error: %w", msg, err)
	}

	outputMsg := ""

	// Format spent outputs if loaded
	spentOutputs, spentErr := tokenTransaction.Edges.SpentOutputOrErr()
	if spentErr == nil && len(spentOutputs) > 0 {
		readable := make([]readableSpentOutput, 0, min(len(spentOutputs), 5))
		for i := 0; i < min(len(spentOutputs), 5); i++ {
			readable = append(readable, readableSpentOutput{
				PrevHash: spentOutputs[i].ID.String(),
				Vout:     uint32(spentOutputs[i].CreatedTransactionOutputVout),
			})
		}
		outputMsg = fmt.Sprintf(", spent_outputs: %+v", readable)
	}

	// Format created outputs if loaded
	createdOutputs, createdErr := tokenTransaction.Edges.CreatedOutputOrErr()
	if createdErr == nil && len(createdOutputs) > 0 {
		readable := make([]readableCreatedOutput, 0, min(len(createdOutputs), 5))
		for i := 0; i < min(len(createdOutputs), 5); i++ {
			readable = append(readable, readableCreatedOutput{
				OutputId:        createdOutputs[i].ID.String(),
				TokenIdentifier: createdOutputs[i].TokenPublicKey.ToHex(),
			})
		}
		outputMsg = fmt.Sprintf("%s, created_outputs: %+v", outputMsg, readable)
	}

	if err != nil {
		return fmt.Errorf("%s (uuid: %s, partial_hash: %x, final_hash: %x%s): %w",
			msg,
			tokenTransaction.ID.String(),
			tokenTransaction.PartialTokenTransactionHash,
			tokenTransaction.FinalizedTokenTransactionHash,
			outputMsg,
			err)
	}
	return fmt.Errorf("%s (uuid: %s, partial_hash: %x, final_hash: %x%s)",
		msg,
		tokenTransaction.ID.String(),
		tokenTransaction.PartialTokenTransactionHash,
		tokenTransaction.FinalizedTokenTransactionHash,
		outputMsg)
}

func FormatTokenTransactionHashes(tokenTransaction *tokenpb.TokenTransaction) string {
	if tokenTransaction == nil {
		return "transaction: <nil>"
	}

	partialHash, err := utils.HashTokenTransaction(tokenTransaction, true)
	if err != nil {
		return fmt.Sprintf("transaction (hash_error: %v)", err)
	}

	if !utils.IsFinalTokenTransaction(tokenTransaction) {
		return fmt.Sprintf("transaction (partial_hash: %x)", partialHash)
	}

	finalHash, err := utils.HashTokenTransaction(tokenTransaction, false)
	if err != nil {
		return fmt.Sprintf("transaction (partial_hash: %x, final_hash_error: %v)", partialHash, err)
	}

	return fmt.Sprintf("transaction (partial_hash: %x, final_hash: %x)", partialHash, finalHash)
}

func FormatErrorWithTransactionProto(msg string, tokenTransaction *tokenpb.TokenTransaction, err error) error {
	formatted := FormatTokenTransactionHashes(tokenTransaction)
	txType, inferTxTypeErr := utils.InferTokenTransactionType(tokenTransaction)
	if inferTxTypeErr != nil {
		return fmt.Errorf("error inferring token txType for error format: %w, original err: %s %s: %w", inferTxTypeErr, msg, formatted, err)
	}

	var spentOutputs []readableSpentOutput
	if txType == utils.TokenTransactionTypeTransfer {
		outputsToSpend := tokenTransaction.GetTransferInput().GetOutputsToSpend()
		n := len(outputsToSpend)
		spentOutputs = []readableSpentOutput{}
		for i := 0; i < min(n, 5); i++ {
			spentOutputs = append(spentOutputs, readableSpentOutput{
				PrevHash: hex.EncodeToString(outputsToSpend[i].GetPrevTokenTransactionHash()),
				Vout:     outputsToSpend[i].GetPrevTokenTransactionVout(),
			})
		}
	}

	outputMsg := fmt.Sprintf("spent_outputs: %+v", spentOutputs)

	n := len(tokenTransaction.TokenOutputs)
	if n > 0 {
		createdOutputs := []readableCreatedOutput{}

		for i := 0; i < min(n, 5); i++ {
			output := tokenTransaction.TokenOutputs[i]
			createdOutputs = append(createdOutputs, readableCreatedOutput{
				OutputId:        output.GetId(),
				TokenIdentifier: hex.EncodeToString(output.TokenIdentifier),
			})
		}
		outputMsg = fmt.Sprintf("%s, created_outputs: %+v", outputMsg, createdOutputs)
	}

	if err != nil {
		return fmt.Errorf("%s %s, %s: %w", msg, formatted, outputMsg, err)
	}
	return fmt.Errorf("%s %s, %s", msg, formatted, outputMsg)
}

func FormatErrorWithTransactionProtoAndSparkInvoice(msg string, tokenTransaction *tokenpb.TokenTransaction, sparkInvoice string, err error) error {
	formatted := FormatTokenTransactionHashes(tokenTransaction)
	if err != nil {
		return fmt.Errorf("%s %s, spark invoice: %s: %w", msg, formatted, sparkInvoice, err)
	}
	return fmt.Errorf("%s %s, spark invoice: %s", msg, formatted, sparkInvoice)
}

func NewTransactionPreemptedError(tokenTransaction *tokenpb.TokenTransaction, reason, details string) error {
	formattedError := FormatErrorWithTransactionProto(
		fmt.Sprintf(ErrTransactionPreemptedByExisting, reason, details),
		tokenTransaction,
		sparkerrors.AlreadyExistsDuplicateOperation(errors.New("inputs cannot be spent: token transaction with these inputs is already in progress or finalized")),
	)
	return sparkerrors.AbortedTransactionPreempted(formattedError)
}

func NewTokenAlreadyCreatedError(tokenTransaction *tokenpb.TokenTransaction) error {
	formattedError := FormatErrorWithTransactionProto(ErrTokenAlreadyCreatedForIssuer, tokenTransaction, nil)
	return sparkerrors.AlreadyExistsDuplicateOperation(formattedError)
}
