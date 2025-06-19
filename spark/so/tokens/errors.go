package tokens

import (
	"fmt"

	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/ent"
)

const (
	// Error message constants
	ErrIdentityPublicKeyAuthFailed        = "identity public key authentication failed"
	ErrInvalidPartialTokenTransaction     = "invalid partial token transaction"
	ErrFailedToHashPartialTransaction     = "failed to hash partial token transaction"
	ErrFailedToFetchPartialTransaction    = "failed to fetch partial token transaction data"
	ErrFailedToFetchTransaction           = "failed to fetch transaction"
	ErrFailedToGetUnusedKeyshares         = "failed to get unused signing keyshares"
	ErrNotEnoughUnusedKeyshares           = "not enough unused signing keyshares available"
	ErrFailedToGetNetworkFromProto        = "failed to get network from proto network"
	ErrFailedToExecuteWithNonCoordinator  = "failed to execute start token transaction with non-coordinator operators"
	ErrFailedToExecuteWithCoordinator     = "failed to execute start token transaction with coordinator"
	ErrFailedToGetKeyshareInfo            = "failed to get keyshare info"
	ErrFailedToHashFinalTransaction       = "failed to hash final token transaction"
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
	ErrTransactionAlreadyFinalized        = "transaction has already been finalized by at least one operator, cannot cancel"
	ErrTooManyOperatorsSigned             = "transaction has been signed by %d operators, which exceeds the cancellation threshold of %d"
	ErrInvalidTransactionStatus           = "transaction is in status %s, but must be in %s status to cancel"
	ErrStoredOperatorSignatureInvalid     = "stored operator signature is invalid"
	ErrFailedToGetRevocationKeyshares     = "failed to get revocation keyshares for transaction"
	ErrFailedToConnectToOperatorForCancel = "failed to connect to operator %s"
	ErrFailedToQueryOperatorForCancel     = "failed to execute query with operator %s"
	ErrFailedToExecuteWithAllOperators    = "failed to execute query with all operators"
	ErrInputIndexOutOfRange               = "input index %d out of range (0-%d)"
	ErrInvalidOwnerSignature              = "invalid owner signature for output"
	ErrInvalidIssuerSignature             = "invalid issuer signature for mint"
	ErrFailedToHashRevocationKeyshares    = "failed to hash revocation keyshares payload"
	ErrTransactionHashMismatch            = "transaction hash in payload (%x) does not match actual transaction hash (%x)"
	ErrOperatorPublicKeyMismatch          = "operator identity public key in payload (%x) does not match this SO's identity public key (%x)"
)

func FormatErrorWithTransactionEnt(msg string, tokenTransaction *ent.TokenTransaction, err error) error {
	return fmt.Errorf("%s (uuid: %s, hash: %x): %w",
		msg,
		tokenTransaction.ID.String(),
		tokenTransaction.FinalizedTokenTransactionHash,
		err)
}

func FormatErrorWithTransactionProto(msg string, tokenTransaction *sparkpb.TokenTransaction, err error) error {
	if err != nil {
		return fmt.Errorf("%s (transaction: %s): %w",
			msg,
			tokenTransaction.String(),
			err)
	}
	return fmt.Errorf("%s (transaction: %s)",
		msg,
		tokenTransaction.String())
}
