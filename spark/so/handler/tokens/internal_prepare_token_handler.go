package tokens

import (
	"bytes"
	"cmp"
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"slices"
	"time"

	"github.com/lightsparkdev/spark/common/keys"

	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"

	"github.com/lightsparkdev/spark/so/tokens"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/predicate"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/sparkinvoice"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/utils"
)

type InternalPrepareTokenHandler struct {
	config           *so.Config
	enablePreemption bool
}

func NewInternalPrepareTokenHandler(config *so.Config) *InternalPrepareTokenHandler {
	return &InternalPrepareTokenHandler{
		config:           config,
		enablePreemption: false,
	}
}

func NewInternalPrepareTokenHandlerWithPreemption(config *so.Config) *InternalPrepareTokenHandler {
	return &InternalPrepareTokenHandler{
		config:           config,
		enablePreemption: true,
	}
}

func (h *InternalPrepareTokenHandler) PrepareTokenTransactionInternal(ctx context.Context, req *tokeninternalpb.PrepareTransactionRequest) (*tokeninternalpb.PrepareTransactionResponse, error) {
	ctx, span := tracer.Start(ctx, "InternalPrepareTokenHandler.PrepareTokenTransactionInternal", getTokenTransactionAttributes(req.FinalTokenTransaction))
	defer span.End()
	partialTransactionHash, err := utils.HashTokenTransaction(req.FinalTokenTransaction, true)
	ctx, logger := logging.WithAttrs(ctx, tokens.GetPartialTokenTransactionAttrs(partialTransactionHash))

	if err != nil {
		return nil, tokens.FormatErrorWithTransactionProto("failed to compute transaction hash", req.FinalTokenTransaction, sparkerrors.InvalidUserInputErrorf("failed to compute transaction hash: %w", err))
	}

	logger.Info("Starting token transaction", "keyshare_ids", req.KeyshareIds, "expiry_time", req.FinalTokenTransaction.ExpiryTime.String())

	expectedRevocationPublicKeys, err := h.validateAndReserveKeyshares(ctx, req.KeyshareIds, req.FinalTokenTransaction)
	if err != nil {
		return nil, err
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	expectedCreationEntityPublicKey, err := ent.GetEntityDkgKeyPublicKey(ctx, db.Client())
	if err != nil {
		return nil, err
	}

	err = validateFinalTokenTransaction(h.config, req.FinalTokenTransaction, req.TokenTransactionSignatures, expectedRevocationPublicKeys, expectedCreationEntityPublicKey)
	if err != nil {
		return nil, tokens.FormatErrorWithTransactionProto("invalid final token transaction", req.FinalTokenTransaction, sparkerrors.InvalidUserInputErrorf("invalid final token transaction: %w", err))
	}

	//nolint:all
	if req.FinalTokenTransaction.Version >= 2 && req.FinalTokenTransaction.GetInvoiceAttachments() != nil {
		// TODO: (CNT-493) Re-enable invoice functionality once spark address migration is complete
		return nil, sparkerrors.UnimplementedErrorf("spark invoice support not implemented")
		err = validateSparkInvoicesForTransaction(ctx, req.FinalTokenTransaction)
		if err != nil {
			return nil, err
		}
		err = validateInvoiceAttachmentsNotInFlightOrFinalized(ctx, req.FinalTokenTransaction)
		if err != nil {
			return nil, err
		}
	}

	txType, err := utils.InferTokenTransactionType(req.FinalTokenTransaction)
	if err != nil {
		return nil, sparkerrors.InvalidUserInputErrorf("failed to check token transaction type: %w", err)
	}

	var inputTtxos []*ent.TokenOutput
	switch txType {
	case utils.TokenTransactionTypeCreate:
		createPubKey, err := keys.ParsePublicKey(req.FinalTokenTransaction.GetCreateInput().GetIssuerPublicKey())
		if err != nil {
			return nil, err
		}
		if err = validateIssuerSignature(req.FinalTokenTransaction, req.TokenTransactionSignatures, createPubKey); err != nil {
			return nil, tokens.FormatErrorWithTransactionProto("failed to validate create token transaction signature", req.FinalTokenTransaction, sparkerrors.InvalidUserInputErrorf("failed to validate create token transaction signature: %w", err))
		}
		if err = validateIssuerTokenNotAlreadyCreated(ctx, req.FinalTokenTransaction); err != nil {
			return nil, err
		}
	case utils.TokenTransactionTypeMint:
		mintPubKey, err := keys.ParsePublicKey(req.FinalTokenTransaction.GetMintInput().GetIssuerPublicKey())
		if err != nil {
			return nil, err
		}
		if err = validateIssuerSignature(req.FinalTokenTransaction, req.TokenTransactionSignatures, mintPubKey); err != nil {
			return nil, tokens.FormatErrorWithTransactionProto("failed to validate mint token transaction signature", req.FinalTokenTransaction, sparkerrors.InvalidUserInputErrorf("failed to validate mint token transaction signature: %w", err))
		}
		tokenMetadata, err := ent.GetTokenMetadataForTokenTransaction(ctx, req.FinalTokenTransaction)
		if err != nil {
			return nil, err
		}
		if tokenMetadata == nil {
			return nil, tokens.FormatErrorWithTransactionProto("minting not allowed because a created token was not found", req.FinalTokenTransaction,
				sparkerrors.FailedPreconditionErrorf("no tokencreate entity found for token"))
		}

		txNet, err := common.NetworkFromProtoNetwork(req.FinalTokenTransaction.Network)
		if err != nil {
			return nil, tokens.FormatErrorWithTransactionProto("failed to get network from proto network", req.FinalTokenTransaction, sparkerrors.InvalidUserInputErrorf("failed to get network from proto network: %w", err))
		}
		if txNet != tokenMetadata.Network {
			return nil, tokens.FormatErrorWithTransactionProto(
				"network mismatch",
				req.FinalTokenTransaction,
				sparkerrors.FailedPreconditionErrorf("transaction network %s does not match token network %s", txNet.String(), tokenMetadata.Network.String()),
			)
		}

		err = tokens.ValidateMintDoesNotExceedMaxSupply(ctx, req.FinalTokenTransaction)
		if err != nil {
			return nil, tokens.FormatErrorWithTransactionProto("max supply error", req.FinalTokenTransaction, sparkerrors.InvalidUserInputErrorf("max supply error: %w", err))
		}
	case utils.TokenTransactionTypeTransfer:
		inputTtxos, err = ent.FetchAndLockTokenInputs(ctx, req.FinalTokenTransaction.GetTransferInput().GetOutputsToSpend())
		if err != nil {
			return nil, tokens.FormatErrorWithTransactionProto("failed to fetch outputs to spend", req.FinalTokenTransaction, sparkerrors.InvalidUserInputErrorf("failed to fetch outputs to spend: %w", err))
		}
		if len(inputTtxos) != len(req.FinalTokenTransaction.GetTransferInput().GetOutputsToSpend()) {
			return nil, tokens.FormatErrorWithTransactionProto("failed to fetch all leaves to spend", req.FinalTokenTransaction,
				sparkerrors.NotFoundErrorf("failed to fetch all leaves to spend: got %d leaves, expected %d", len(inputTtxos), len(req.FinalTokenTransaction.GetTransferInput().GetOutputsToSpend())))
		}

		err = validateTransferTokenTransactionUsingPreviousTransactionData(ctx, h.enablePreemption, req.FinalTokenTransaction, req.TokenTransactionSignatures, inputTtxos, h.config.Lrc20Configs[req.FinalTokenTransaction.Network.String()].TransactionExpiryDuration)
		if err != nil {
			return nil, tokens.FormatErrorWithTransactionProto("error validating transfer using previous output data", req.FinalTokenTransaction, sparkerrors.InvalidUserInputErrorf("error validating transfer using previous output data: %w", err))
		}
		if h.enablePreemption && anyTtxosHaveSpentTransactions(inputTtxos) {
			if err := preemptOrRejectTransactionsWithInputEnts(ctx, req.FinalTokenTransaction, inputTtxos); err != nil {
				return nil, err
			}
		}
	default:
		return nil, sparkerrors.InvalidUserInputErrorf("token transaction type unknown")
	}

	// Save the token transaction, created output ents, and update the outputs to spend.
	coordinatorPubKey, err := keys.ParsePublicKey(req.CoordinatorPublicKey)
	if err != nil {
		return nil, sparkerrors.InvalidUserInputErrorf("failed to parse coordinator public key: %w", err)
	}
	_, err = ent.CreateStartedTransactionEntities(ctx, req.FinalTokenTransaction, req.TokenTransactionSignatures, req.KeyshareIds, inputTtxos, coordinatorPubKey)
	if err != nil {
		return nil, tokens.FormatErrorWithTransactionProto("failed to save token transaction and output ent", req.FinalTokenTransaction, sparkerrors.InternalErrorf("failed to save token transaction and output ent: %w", err))
	}

	return &tokeninternalpb.PrepareTransactionResponse{}, nil
}

func anyTtxosHaveSpentTransactions(ttxos []*ent.TokenOutput) bool {
	for _, ttxo := range ttxos {
		if ttxo.Edges.OutputSpentTokenTransaction != nil {
			return true
		}
	}
	return false
}

// validateAndReserveKeyshares parses keyshare IDs, checks for duplicates, marks them as used, and returns expected revocation public keys
func (h *InternalPrepareTokenHandler) validateAndReserveKeyshares(ctx context.Context, keyshareIDs []string, finalTokenTransaction *tokenpb.TokenTransaction) ([]keys.Public, error) {
	logger := logging.GetLoggerFromContext(ctx)
	keyshareUUIDs := make([]uuid.UUID, len(keyshareIDs))
	// Ensure that the coordinator SO did not pass duplicate keyshare UUIDs for different outputs.
	seenUUIDs := make(map[uuid.UUID]bool)
	for i, id := range keyshareIDs {
		keyshareUUID, err := uuid.Parse(id)
		if err != nil {
			return nil, tokens.FormatErrorWithTransactionProto("failed to parse keyshare ID", finalTokenTransaction, sparkerrors.InvalidUserInputErrorf("failed to parse keyshare ID: %w", err))
		}
		if seenUUIDs[keyshareUUID] {
			return nil, tokens.FormatErrorWithTransactionProto("duplicate keyshare UUID found", finalTokenTransaction, sparkerrors.InvalidUserInputErrorf("duplicate keyshare UUID found: %s", keyshareUUID))
		}
		seenUUIDs[keyshareUUID] = true
		keyshareUUIDs[i] = keyshareUUID
	}
	logger.Info("Marking keyshares as used")
	keysharesMap, err := ent.MarkSigningKeysharesAsUsed(ctx, h.config, keyshareUUIDs)
	if err != nil {
		return nil, tokens.FormatErrorWithTransactionProto("failed to mark keyshares as used", finalTokenTransaction, sparkerrors.InternalErrorf("failed to mark keyshares as used: %w", err))
	}
	logger.Info("Keyshares marked as used")
	expectedRevocationPublicKeys := make([]keys.Public, len(keyshareIDs))
	for i, id := range keyshareUUIDs {
		keyshare, ok := keysharesMap[id]
		if !ok {
			return nil, tokens.FormatErrorWithTransactionProto("keyshare ID not found", finalTokenTransaction, sparkerrors.InvalidUserInputErrorf("keyshare ID not found: %s", id))
		}
		pubKey, err := keys.ParsePublicKey(keyshare.PublicKey)
		if err != nil {
			return nil, tokens.FormatErrorWithTransactionProto("failed to parse public key", finalTokenTransaction, sparkerrors.InvalidUserInputErrorf("failed to parse public key: %w", err))
		}
		expectedRevocationPublicKeys[i] = pubKey
	}
	return expectedRevocationPublicKeys, nil
}

// validateOperatorSpecificSignatures validates the signatures in the request against the transaction hash
// and verifies that the number of signatures matches the expected count based on transaction type
func validateOperatorSpecificSignatures(identityPublicKey keys.Public, operatorSpecificSignatures []*sparkpb.OperatorSpecificOwnerSignature, tokenTransaction *ent.TokenTransaction) error {
	if len(tokenTransaction.Edges.SpentOutput) > 0 {
		return validateTransferOperatorSpecificSignatures(identityPublicKey, operatorSpecificSignatures, tokenTransaction)
	}
	return validateIssuerOperatorSpecificSignatures(identityPublicKey, operatorSpecificSignatures, tokenTransaction)
}

// validateTransferOperatorSpecificSignatures validates signatures for transfer transactions
func validateTransferOperatorSpecificSignatures(identityPublicKey keys.Public, operatorSpecificSignatures []*sparkpb.OperatorSpecificOwnerSignature, tokenTransaction *ent.TokenTransaction) error {
	if len(operatorSpecificSignatures) != len(tokenTransaction.Edges.SpentOutput) {
		return tokens.FormatErrorWithTransactionEnt(
			"invalid number of signatures for transfer",
			tokenTransaction, sparkerrors.InvalidUserInputErrorf("expected %d signatures for transfer (one per input), but got %d", len(tokenTransaction.Edges.SpentOutput), len(operatorSpecificSignatures)))
	}
	numInputs := len(tokenTransaction.Edges.SpentOutput)
	signaturesByIndex := make([]*sparkpb.OperatorSpecificOwnerSignature, numInputs)

	// Sort signatures according to index position
	for _, sig := range operatorSpecificSignatures {
		index := int(sig.OwnerSignature.InputIndex)
		if index < 0 || index >= numInputs {
			return tokens.FormatErrorWithTransactionEnt(
				fmt.Sprintf(tokens.ErrInputIndexOutOfRange, index, numInputs-1),
				tokenTransaction, nil)
		}

		if signaturesByIndex[index] != nil {
			return tokens.FormatErrorWithTransactionEnt(
				fmt.Sprintf("duplicate signature for input index %d", index),
				tokenTransaction, nil)
		}

		signaturesByIndex[index] = sig
	}

	for i := 0; i < numInputs; i++ {
		if signaturesByIndex[i] == nil {
			return tokens.FormatErrorWithTransactionEnt(
				fmt.Sprintf("missing signature for input index %d", i),
				tokenTransaction, nil)
		}
	}

	// Sort spent outputs by their index
	spentOutputs := slices.SortedFunc(slices.Values(tokenTransaction.Edges.SpentOutput), func(a, b *ent.TokenOutput) int {
		return cmp.Compare(a.SpentTransactionInputVout, b.SpentTransactionInputVout)
	})

	// Validate each signature against its corresponding output
	for i, sig := range signaturesByIndex {
		payloadHash, err := utils.HashOperatorSpecificTokenTransactionSignablePayload(sig.Payload)
		if err != nil {
			return sparkerrors.InternalErrorf("%s: %w", tokens.ErrFailedToHashRevocationKeyshares, err)
		}

		if !bytes.Equal(sig.Payload.FinalTokenTransactionHash, tokenTransaction.FinalizedTokenTransactionHash) {
			return sparkerrors.FailedPreconditionErrorf(tokens.ErrTransactionHashMismatch,
				sig.Payload.FinalTokenTransactionHash, tokenTransaction.FinalizedTokenTransactionHash)
		}

		payloadPubKey, err := keys.ParsePublicKey(sig.Payload.OperatorIdentityPublicKey)
		if err != nil {
			return sparkerrors.InvalidUserInputErrorf("unable to parse signature payload operator identity public key: %w", err)
		}
		if !payloadPubKey.Equals(identityPublicKey) {
			return sparkerrors.FailedPreconditionErrorf(tokens.ErrOperatorPublicKeyMismatch, payloadPubKey, identityPublicKey)
		}
		output := spentOutputs[i]
		ownerPubKey, err := keys.ParsePublicKey(output.OwnerPublicKey)
		if err != nil {
			return sparkerrors.InvalidUserInputErrorf("unable to parse signature owner public key: %w", err)
		}
		if err := utils.ValidateOwnershipSignature(sig.OwnerSignature.Signature, payloadHash, ownerPubKey); err != nil {
			return tokens.FormatErrorWithTransactionEnt(tokens.ErrInvalidOwnerSignature, tokenTransaction, err)
		}
	}

	return nil
}

// validateIssuerOperatorSpecificSignatures validates signatures for mint and create transactions
func validateIssuerOperatorSpecificSignatures(identityPublicKey keys.Public, operatorSpecificSignatures []*sparkpb.OperatorSpecificOwnerSignature, tokenTransaction *ent.TokenTransaction) error {
	if len(operatorSpecificSignatures) != 1 {
		return tokens.FormatErrorWithTransactionEnt(
			"invalid number of signatures",
			tokenTransaction, sparkerrors.InvalidUserInputErrorf("expected exactly 1 signature for mint/create, but got %d", len(operatorSpecificSignatures)))
	}

	var issuerPublicKey keys.Public
	if tokenTransaction.Edges.Mint != nil {
		issuerKey, err := keys.ParsePublicKey(tokenTransaction.Edges.Mint.IssuerPublicKey)
		if err != nil {
			return sparkerrors.InvalidUserInputErrorf("unable to parse issuer public key: %w", err)
		}
		issuerPublicKey = issuerKey
	} else if tokenTransaction.Edges.Create != nil {
		issuerKey, err := keys.ParsePublicKey(tokenTransaction.Edges.Create.IssuerPublicKey)
		if err != nil {
			return sparkerrors.InvalidUserInputErrorf("unable to parse issuer public key: %w", err)
		}
		issuerPublicKey = issuerKey
	} else {
		return tokens.FormatErrorWithTransactionEnt(
			"db consistency error",
			tokenTransaction, sparkerrors.NotFoundErrorf("neither mint nor create record found in db, but expected one for this transaction"))
	}

	sig := operatorSpecificSignatures[0]

	// Validate the signature payload
	payloadHash, err := utils.HashOperatorSpecificTokenTransactionSignablePayload(sig.Payload)
	if err != nil {
		return tokens.FormatErrorWithTransactionEnt(tokens.ErrFailedToHashRevocationKeyshares, tokenTransaction, err)
	}

	if !bytes.Equal(sig.Payload.FinalTokenTransactionHash, tokenTransaction.FinalizedTokenTransactionHash) {
		return sparkerrors.FailedPreconditionErrorf(tokens.ErrTransactionHashMismatch,
			sig.Payload.FinalTokenTransactionHash, tokenTransaction.FinalizedTokenTransactionHash)
	}

	if len(sig.Payload.OperatorIdentityPublicKey) > 0 {
		payloadPubKey, err := keys.ParsePublicKey(sig.Payload.OperatorIdentityPublicKey)
		if err != nil {
			return sparkerrors.InvalidUserInputErrorf("unable to parse signature payload operator identity public key: %w", err)
		}
		if !payloadPubKey.Equals(identityPublicKey) {
			return sparkerrors.FailedPreconditionErrorf(tokens.ErrOperatorPublicKeyMismatch, payloadPubKey, identityPublicKey)
		}
	}

	// Validate the signature using the issuer public key from the database
	if err := utils.ValidateOwnershipSignature(sig.OwnerSignature.Signature, payloadHash, issuerPublicKey); err != nil {
		errorMsg := tokens.ErrInvalidIssuerSignature
		if tokenTransaction.Edges.Create != nil {
			errorMsg = "invalid issuer signature for create transaction"
		}
		return tokens.FormatErrorWithTransactionEnt(errorMsg, tokenTransaction, err)
	}

	return nil
}

// validateOutputs checks if all created outputs have the expected status
func validateOutputs(outputs []*ent.TokenOutput, expectedStatus st.TokenOutputStatus) []string {
	var invalidOutputs []string
	for i, output := range outputs {
		if output.Status != expectedStatus {
			invalidOutputs = append(invalidOutputs, fmt.Sprintf("output %d has invalid status %s, expected %s",
				i, output.Status, expectedStatus))
		}
	}
	return invalidOutputs
}

// validateInputs checks if all spent outputs have the expected status and aren't withdrawn
func validateInputs(outputs []*ent.TokenOutput, expectedStatus st.TokenOutputStatus) []string {
	var invalidOutputs []string
	for _, output := range outputs {
		if output.Status != expectedStatus {
			invalidOutputs = append(invalidOutputs, fmt.Sprintf("input %x has invalid status %s, expected %s",
				output.ID, output.Status, expectedStatus))
		}
		if output.ConfirmedWithdrawBlockHash != nil {
			invalidOutputs = append(invalidOutputs, fmt.Sprintf("input %x is already withdrawn",
				output.ID))
		}
	}
	return invalidOutputs
}

func validateIssuerSignature(
	tokenTransaction *tokenpb.TokenTransaction,
	signaturesWithIndex []*tokenpb.SignatureWithIndex,
	issuerPublicKey keys.Public,
) error {
	// Although this token transaction is final we pass in 'true' to generate the partial hash.
	partialTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, true)
	if err != nil {
		return tokens.FormatErrorWithTransactionProto("failed to hash token transaction", tokenTransaction, err)
	}

	err = utils.ValidateOwnershipSignature(signaturesWithIndex[0].Signature, partialTokenTransactionHash, issuerPublicKey)
	if err != nil {
		return tokens.FormatErrorWithTransactionProto("invalid issuer signature", tokenTransaction, err)
	}

	return nil
}

func validateTransferTokenTransactionUsingPreviousTransactionData(
	ctx context.Context,
	enablePreemption bool,
	tokenTransaction *tokenpb.TokenTransaction,
	signaturesWithIndex []*tokenpb.SignatureWithIndex,
	outputToSpendEnts []*ent.TokenOutput,
	v0DefaultTransactionExpiryDuration time.Duration,
) error {
	// All created outputs having the same token identifier is validated upstream, so only need to check against the first one.
	expectedTokenIdentifier := tokenTransaction.TokenOutputs[0].GetTokenIdentifier()
	if expectedTokenIdentifier != nil {
		// Validate that all spent outputs have the same token identifier
		for i, outputEnt := range outputToSpendEnts {
			if !bytes.Equal(outputEnt.TokenIdentifier, expectedTokenIdentifier) {
				return tokens.FormatErrorWithTransactionProto("token identifier mismatch", tokenTransaction, sparkerrors.FailedPreconditionErrorf("output %d has different token identifier", i))
			}
		}
	} else {
		expectedTokenPubKey := tokenTransaction.TokenOutputs[0].GetTokenPublicKey()
		if expectedTokenPubKey == nil {
			return tokens.FormatErrorWithTransactionProto("invalid token public key", tokenTransaction, sparkerrors.InvalidUserInputErrorf("token public key is required in outputs"))
		}
		// Validate that all spent outputs have the same token public key
		for i, outputEnt := range outputToSpendEnts {
			if !bytes.Equal(outputEnt.TokenPublicKey, expectedTokenPubKey) {
				return tokens.FormatErrorWithTransactionProto("token public key mismatch", tokenTransaction, sparkerrors.FailedPreconditionErrorf("output %d has different token public key", i))
			}
		}
	}

	// TODO(DL-104): For now we allow the network to be nil to support old outputs. In the future we should require it to be set.
	for i, outputEnt := range outputToSpendEnts {
		if outputEnt.Network != ("") {
			entNetwork, err := outputEnt.Network.MarshalProto()
			if err != nil {
				return tokens.FormatErrorWithTransactionProto("failed to marshal network", tokenTransaction, sparkerrors.InternalErrorf("failed to marshal network: %w", err))
			}
			if entNetwork != tokenTransaction.Network {
				return tokens.FormatErrorWithTransactionProto("network mismatch", tokenTransaction, sparkerrors.FailedPreconditionErrorf("output %d: %d != %d", i, entNetwork, tokenTransaction.Network))
			}
		}
	}
	// Validate token conservation in inputs + outputs.
	totalInputAmount := new(big.Int)
	for _, outputEnt := range outputToSpendEnts {
		inputAmount := new(big.Int).SetBytes(outputEnt.TokenAmount)
		totalInputAmount.Add(totalInputAmount, inputAmount)
	}
	totalOutputAmount := new(big.Int)
	for _, outputLeaf := range tokenTransaction.TokenOutputs {
		outputAmount := new(big.Int).SetBytes(outputLeaf.GetTokenAmount())
		totalOutputAmount.Add(totalOutputAmount, outputAmount)
	}
	if totalInputAmount.Cmp(totalOutputAmount) != 0 {
		return tokens.FormatErrorWithTransactionProto("token amount mismatch", tokenTransaction, sparkerrors.FailedPreconditionErrorf("total input amount %s does not match total output amount %s", totalInputAmount.String(), totalOutputAmount.String()))
	}

	// Validate that the ownership signatures match the ownership public keys in the outputs to spend.
	// Although this token transaction is final we pass in 'true' to generate the partial hash.
	partialTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, true)
	if err != nil {
		return tokens.FormatErrorWithTransactionProto("failed to hash token transaction", tokenTransaction, err)
	}

	ownerSignaturesByIndex := make(map[uint32]*tokenpb.SignatureWithIndex)
	for _, sig := range signaturesWithIndex {
		if sig == nil {
			return tokens.FormatErrorWithTransactionProto("invalid signature", tokenTransaction, sparkerrors.InvalidUserInputErrorf("ownership signature cannot be nil"))
		}
		ownerSignaturesByIndex[sig.InputIndex] = sig
	}

	if len(signaturesWithIndex) != len(tokenTransaction.GetTransferInput().GetOutputsToSpend()) {
		return tokens.FormatErrorWithTransactionProto("signature count mismatch", tokenTransaction, sparkerrors.InvalidUserInputErrorf("number of signatures must match number of outputs to spend"))
	}

	for i := range tokenTransaction.GetTransferInput().GetOutputsToSpend() {
		index := uint32(i)
		ownershipSignature, exists := ownerSignaturesByIndex[index]
		if !exists {
			return tokens.FormatErrorWithTransactionProto("missing signature", tokenTransaction, sparkerrors.InvalidUserInputErrorf("missing owner signature for input index %d, indexes must be contiguous", index))
		}

		// Get the corresponding output entity (they are ordered outside of this block when they are fetched)
		outputEnt := outputToSpendEnts[i]
		if outputEnt == nil {
			return tokens.FormatErrorWithTransactionProto("missing output entity", tokenTransaction, sparkerrors.NotFoundErrorf("could not find output entity for output to spend at index %d", i))
		}

		ownerPublicKey, err := keys.ParsePublicKey(outputEnt.OwnerPublicKey)
		if err != nil {
			return tokens.FormatErrorWithTransactionProto("failed to parse owner public key", tokenTransaction, sparkerrors.InvalidUserInputErrorf("failed to parse owner key: %w", err))
		}
		err = utils.ValidateOwnershipSignature(ownershipSignature.Signature, partialTokenTransactionHash, ownerPublicKey)
		if err != nil {
			return tokens.FormatErrorWithTransactionProto("invalid ownership signature", tokenTransaction, sparkerrors.InvalidUserInputErrorf("invalid ownership signature: %w", err))
		}
		err = validateOutputIsSpendable(ctx, enablePreemption, i, outputEnt, tokenTransaction, v0DefaultTransactionExpiryDuration)
		if err != nil {
			return err
		}
	}

	return nil
}

// validateOutputIsSpendable checks if a output is eligible to be spent by verifying:
// 1. The output has an appropriate status (Created+Finalized or already marked as SpentStarted) OR was spent from an expired or pre-emptable transaction
// 2. The output hasn't been withdrawn already
func validateOutputIsSpendable(ctx context.Context, enablePreemption bool, index int, output *ent.TokenOutput, tokenTransaction *tokenpb.TokenTransaction, v0DefaultTransactionExpiryDuration time.Duration) error {
	if !isSpendableOutputStatus(output.Status) {
		spentTx := output.Edges.OutputSpentTokenTransaction
		if spentTx == nil {
			return sparkerrors.FailedPreconditionErrorf("output %d cannot be spent: status must be %s or %s (was %s), or have been spent by an expired or pre-emptable transaction (none found)",
				index, st.TokenOutputStatusCreatedFinalized, st.TokenOutputStatusSpentStarted, output.Status)
		}
		if !spentTx.IsExpired(time.Now(), v0DefaultTransactionExpiryDuration) {
			canPreemptSpentTx := false
			var cannotPreemptErr error
			if enablePreemption {
				cannotPreemptErr = preemptOrRejectTransaction(ctx, tokenTransaction, spentTx)
				canPreemptSpentTx = cannotPreemptErr == nil
			}
			if !canPreemptSpentTx {
				return sparkerrors.FailedPreconditionErrorf("output %d cannot be spent: status must be %s or %s (was %s), or have been spent by an expired or pre-emptable transaction (transaction was not expired or pre-emptable, id: %s, final_hash: %s, error: %w)",
					index, st.TokenOutputStatusCreatedFinalized, st.TokenOutputStatusSpentStarted, output.Status, spentTx.ID, hex.EncodeToString(spentTx.FinalizedTokenTransactionHash), cannotPreemptErr)
			}
		}
	}

	if output.ConfirmedWithdrawBlockHash != nil {
		return sparkerrors.FailedPreconditionErrorf("output %d cannot be spent: already withdrawn", index)
	}

	return nil
}

// isSpendableOutputStatus checks if a output's status allows it to be spent.
func isSpendableOutputStatus(status st.TokenOutputStatus) bool {
	return status == st.TokenOutputStatusCreatedFinalized || status == st.TokenOutputStatusSpentStarted
}

func validateFinalTokenTransaction(
	config *so.Config,
	tokenTransaction *tokenpb.TokenTransaction,
	signaturesWithIndex []*tokenpb.SignatureWithIndex,
	expectedRevocationPublicKeys []keys.Public,
	expectedCreationEntityPublicKey keys.Public,
) error {
	network, err := common.NetworkFromProtoNetwork(tokenTransaction.Network)
	if err != nil {
		return sparkerrors.InternalErrorf("failed to get network from proto network: %w", err)
	}
	expectedBondSats := config.Lrc20Configs[network.String()].WithdrawBondSats
	expectedRelativeBlockLocktime := config.Lrc20Configs[network.String()].WithdrawRelativeBlockLocktime
	sparkOperatorsFromConfig := config.GetSigningOperatorList()

	validationConfig := &utils.FinalValidationConfig{
		ExpectedSparkOperators:             sparkOperatorsFromConfig,
		SupportedNetworks:                  config.SupportedNetworks,
		RequireTokenIdentifierForMints:     config.Token.RequireTokenIdentifierForMints,
		RequireTokenIdentifierForTransfers: config.Token.RequireTokenIdentifierForTransfers,
		ExpectedRevocationPublicKeys:       expectedRevocationPublicKeys,
		ExpectedBondSats:                   expectedBondSats,
		ExpectedRelativeBlockLocktime:      expectedRelativeBlockLocktime,
		ExpectedCreationEntityPublicKey:    expectedCreationEntityPublicKey,
	}

	err = utils.ValidateFinalTokenTransaction(tokenTransaction, signaturesWithIndex, validationConfig)
	if err != nil {
		return sparkerrors.InvalidUserInputErrorf("failed to validate final token transaction structure: %w", err)
	}

	return nil
}

func validateIssuerTokenNotAlreadyCreated(ctx context.Context, tokenTransaction *tokenpb.TokenTransaction) error {
	existingTokenCreateMetadata, err := ent.GetTokenMetadataForTokenTransaction(ctx, tokenTransaction)
	if err != nil {
		return tokens.FormatErrorWithTransactionProto("failed to search for existing token create entity", tokenTransaction, sparkerrors.InternalErrorf("failed to search for existing token create entity: %w", err))
	}
	if existingTokenCreateMetadata != nil {
		return tokens.NewTokenAlreadyCreatedError(tokenTransaction)
	}
	return nil
}

type CreatedOutputAmountMap map[[33]byte]map[AmountKey]int
type InvoiceAmountMap map[[33]byte]map[AmountKey]int
type CountNilAmountInvoicesMap map[[33]byte]int

type AmountKey [16]byte

func toAmountKey(b []byte) (AmountKey, error) {
	if len(b) > 16 {
		return AmountKey{}, sparkerrors.InternalErrorf("amount exceeds 16 bytes, got %d", len(b))
	}
	var k AmountKey
	copy(k[16-len(b):], b)
	return k, nil
}

// validates that a token transaction's spark invoices are valid.
// spark_invoices are version 1
// spark_invoices are for token transactions
// spark_invoices pay the same token identifier
// spark_invoices are not expired.
// created_outputs match the invoices on the transaction
// spent_outputs owner matches encoded sender public key if present
func validateSparkInvoicesForTransaction(ctx context.Context, tokenTransaction *tokenpb.TokenTransaction) error {
	invoiceAttachments := tokenTransaction.GetInvoiceAttachments()
	if len(invoiceAttachments) == 0 {
		return nil
	}

	var transactionExpiry time.Time
	if expiry := tokenTransaction.GetExpiryTime(); expiry != nil {
		transactionExpiry = expiry.AsTime().UTC()
	}

	createdOutputAmountMap, tokenIdentifier, err := getCreatedOutputAmountMapAndTokenIdentifier(tokenTransaction)
	if err != nil {
		return tokens.FormatErrorWithTransactionProto("failed to get created output amount map and token identifier", tokenTransaction, sparkerrors.InvalidUserInputErrorf("failed to get created output amount map and token identifier: %w", err))
	}
	senderPublicKey, network, err := validateInvoiceFields(invoiceAttachments, tokenIdentifier, transactionExpiry)
	if err != nil {
		return tokens.FormatErrorWithTransactionProto("failed to validate invoice fields", tokenTransaction, sparkerrors.InvalidUserInputErrorf("failed to validate invoice fields: %w", err))
	}
	invoiceAmountMap, countNilAmountInvoicesMap, err := countInvoiceAmounts(invoiceAttachments)
	if err != nil {
		return tokens.FormatErrorWithTransactionProto("failed to count invoice amounts", tokenTransaction, sparkerrors.InvalidUserInputErrorf("failed to count invoice amounts: %w", err))
	}

	// For each receiver and amount: ensure created outputs >= fixed-amount invoices
	for receiver, invoiceCountByAmount := range invoiceAmountMap {
		outputCountByAmount, ok := createdOutputAmountMap[receiver]
		if !ok {
			return tokens.FormatErrorWithTransactionProto("no created outputs for receiver",
				tokenTransaction, sparkerrors.FailedPreconditionErrorf("no created outputs for receiver %x", receiver[:]))
		}
		for amt, invoiceCount := range invoiceCountByAmount {
			if outputCountByAmount[amt] < invoiceCount {
				return tokens.FormatErrorWithTransactionProto("created output amount mismatch for fixed amount invoices",
					tokenTransaction, sparkerrors.FailedPreconditionErrorf("not enough created outputs for amount %x for receiver %x", amt, receiver[:]))
			}
		}
	}

	// For each receiver: ensure remaining outputs (after fixed-amount allocation) >= nil-amount invoices
	for receiver, outputCountByAmount := range createdOutputAmountMap {
		invoiceCountByAmt := invoiceAmountMap[receiver]

		numOutputsWithoutMatchingInvoice := 0
		for amt, numOutputs := range outputCountByAmount {
			numInvoices := invoiceCountByAmt[amt]
			numOutputsWithoutMatchingInvoice += numOutputs - numInvoices
		}
		if numOutputsWithoutMatchingInvoice < countNilAmountInvoicesMap[receiver] {
			return tokens.FormatErrorWithTransactionProto("created output amount mismatch for nil amount invoices",
				tokenTransaction, sparkerrors.FailedPreconditionErrorf("not enough created outputs to cover %d nil-amount invoices; outputs=%d for receiver %x",
					countNilAmountInvoicesMap[receiver], numOutputsWithoutMatchingInvoice, receiver[:]))
		}
	}

	err = validateOutputsMatchSenderAndNetwork(ctx, tokenTransaction, senderPublicKey, network)
	if err != nil {
		return tokens.FormatErrorWithTransactionProto("failed to validate sender public key matches spent outputs owners and network", tokenTransaction, sparkerrors.InvalidUserInputErrorf("failed to validate sender public key matches spent outputs owners and network: %w", err))
	}

	return nil
}

func validateInvoiceFields(invoiceAttachments []*tokenpb.InvoiceAttachment, tokenIdentifier []byte, transactionExpiry time.Time) (keys.Public, common.Network, error) {
	now := time.Now().UTC()
	var senderPublicKey keys.Public
	var network common.Network
	for _, attachment := range invoiceAttachments {
		invoice := attachment.GetSparkInvoice()
		decoded, err := common.DecodeSparkAddress(invoice)
		if err != nil {
			return keys.Public{}, common.Unspecified, sparkerrors.InvalidUserInputErrorf("failed to decode spark invoice %s: %w", invoice, err)
		}
		if decoded.SparkAddress == nil || decoded.SparkAddress.GetSparkInvoiceFields() == nil {
			return keys.Public{}, common.Unspecified, sparkerrors.InvalidUserInputErrorf("no invoice fields in invoice %s", invoice)
		}
		_, err = keys.ParsePublicKey(decoded.SparkAddress.GetIdentityPublicKey())
		if err != nil {
			return keys.Public{}, common.Unspecified, sparkerrors.InvalidUserInputErrorf("invalid recipient public key in invoice %s: %w", invoice, err)
		}
		if decoded.SparkAddress.SparkInvoiceFields.Version != uint32(1) {
			return keys.Public{}, common.Unspecified, sparkerrors.InvalidUserInputErrorf("version mismatch in invoice %s", invoice)
		}
		if _, ok := decoded.SparkAddress.SparkInvoiceFields.PaymentType.(*sparkpb.SparkInvoiceFields_TokensPayment); !ok {
			return keys.Public{}, common.Unspecified, sparkerrors.InvalidUserInputErrorf("not a tokens payment in invoice %s", invoice)
		}
		payment := decoded.SparkAddress.SparkInvoiceFields.PaymentType.(*sparkpb.SparkInvoiceFields_TokensPayment).TokensPayment
		// all invoices pay the outputs identifier
		if !bytes.Equal(tokenIdentifier, payment.TokenIdentifier) {
			return keys.Public{}, common.Unspecified, sparkerrors.InvalidUserInputErrorf("token identifier mismatch in invoice %s", invoice)
		}
		if expiry := decoded.SparkAddress.SparkInvoiceFields.GetExpiryTime(); expiry != nil {
			if err := expiry.CheckValid(); err != nil {
				return keys.Public{}, common.Unspecified, sparkerrors.InvalidUserInputErrorf("invalid expiry time in invoice %s: %w", invoice, err)
			}
			if expiry.AsTime().UTC().Before(now) {
				return keys.Public{}, common.Unspecified, sparkerrors.InvalidUserInputErrorf("expired in invoice %s", invoice)
			}
			if !transactionExpiry.IsZero() && expiry.AsTime().UTC().Before(transactionExpiry) {
				return keys.Public{}, common.Unspecified, sparkerrors.InvalidUserInputErrorf("invoice expiration must be >= transaction expiration in invoice %s", invoice)
			}
		}
		// if a sender public key is present, it must be the same across all invoices with a sender public key encoded
		if decoded.SparkAddress.SparkInvoiceFields.SenderPublicKey != nil {
			decodedSenderPublicKey, err := keys.ParsePublicKey(decoded.SparkAddress.SparkInvoiceFields.SenderPublicKey)
			if err != nil {
				return keys.Public{}, common.Unspecified, sparkerrors.InvalidUserInputErrorf("invalid sender public key in invoice %s: %w", invoice, err)
			}
			if senderPublicKey == (keys.Public{}) {
				senderPublicKey = decodedSenderPublicKey
			} else if !decodedSenderPublicKey.Equals(senderPublicKey) {
				return keys.Public{}, common.Unspecified, sparkerrors.InvalidUserInputErrorf("sender public key mismatch in invoice %s: expected %x, got %x", invoice, senderPublicKey.Serialize(), decodedSenderPublicKey.Serialize())
			}
		}
		if network == common.Unspecified {
			network = decoded.Network
		} else if network != decoded.Network {
			return keys.Public{}, common.Unspecified, sparkerrors.InvalidUserInputErrorf("network mismatch in invoice %s: expected %s, got %s", invoice, network, decoded.Network)
		}
		if decoded.SparkAddress.Signature != nil {
			err := common.VerifySparkAddressSignature(decoded.SparkAddress, decoded.Network)
			if err != nil {
				return keys.Public{}, common.Unspecified, sparkerrors.InvalidUserInputErrorf("invalid signature in invoice %s: %w", invoice, err)
			}
		}
	}
	if network == common.Unspecified {
		return keys.Public{}, common.Unspecified, sparkerrors.InvalidUserInputErrorf("invalid network encoded in invoices")
	}
	return senderPublicKey, network, nil
}

// countInvoiceAmounts maps the invoices by amount to each receiver and counts the number of nil amount invoices for each receiver
func countInvoiceAmounts(invoiceAttachments []*tokenpb.InvoiceAttachment) (InvoiceAmountMap, CountNilAmountInvoicesMap, error) {
	countNilAmountInvoicesMap := make(CountNilAmountInvoicesMap)
	invoiceAmountMap := make(InvoiceAmountMap)
	for _, attachment := range invoiceAttachments {
		invoice := attachment.GetSparkInvoice()
		decoded, err := common.DecodeSparkAddress(invoice)
		if err != nil {
			return nil, nil, sparkerrors.InvalidUserInputErrorf("failed to decode spark invoice %s: %w", invoice, err)
		}
		recipientPubkey, err := keys.ParsePublicKey(decoded.SparkAddress.GetIdentityPublicKey())
		if err != nil {
			return nil, nil, sparkerrors.InvalidUserInputErrorf("invalid recipient public key in invoice %s: %w", invoice, err)
		}
		payment := decoded.SparkAddress.SparkInvoiceFields.PaymentType.(*sparkpb.SparkInvoiceFields_TokensPayment).TokensPayment

		var recipient [33]byte
		copy(recipient[:], recipientPubkey.Serialize())
		if invoiceAmountMap[recipient] == nil {
			invoiceAmountMap[recipient] = make(map[AmountKey]int)
		}
		if len(payment.Amount) == 0 {
			countNilAmountInvoicesMap[recipient]++
		} else {
			amount, err := toAmountKey(payment.Amount)
			if err != nil {
				return nil, nil, sparkerrors.InvalidUserInputErrorf("invalid amount in invoice %s: %w", invoice, err)
			}
			invoiceAmountMap[recipient][amount]++
		}
	}
	return invoiceAmountMap, countNilAmountInvoicesMap, nil
}

func getCreatedOutputAmountMapAndTokenIdentifier(tokenTransaction *tokenpb.TokenTransaction) (CreatedOutputAmountMap, []byte, error) {
	createdOutputMap := make(CreatedOutputAmountMap)
	var tokenIdentifier []byte
	for _, output := range tokenTransaction.TokenOutputs {
		ownerPubkey, err := keys.ParsePublicKey(output.GetOwnerPublicKey())
		if err != nil {
			return nil, nil, sparkerrors.InvalidUserInputErrorf("invalid owner public key: %w", err)
		}
		if len(tokenIdentifier) == 0 {
			tokenIdentifier = output.GetTokenIdentifier()
		} else if !bytes.Equal(tokenIdentifier, output.GetTokenIdentifier()) {
			return nil, nil, sparkerrors.FailedPreconditionErrorf("token identifier mismatch for owner %s", ownerPubkey)
		}
		amount, err := toAmountKey(output.GetTokenAmount())
		if err != nil {
			return nil, nil, sparkerrors.InvalidUserInputErrorf("invalid amount: %w", err)
		}
		var owner [33]byte
		copy(owner[:], ownerPubkey.Serialize())
		if createdOutputMap[owner] == nil {
			createdOutputMap[owner] = make(map[AmountKey]int)
		}
		createdOutputMap[owner][amount]++
	}
	return createdOutputMap, tokenIdentifier, nil
}

func validateInvoiceAttachmentsNotInFlightOrFinalized(ctx context.Context, tokenTransaction *tokenpb.TokenTransaction) error {
	invoiceAttachments := tokenTransaction.GetInvoiceAttachments()
	sparkInvoiceIDs := make(map[uuid.UUID]struct{})

	for _, invoiceAttachment := range invoiceAttachments {
		sparkInvoice := invoiceAttachment.GetSparkInvoice()
		parsedInvoice, err := common.ParseSparkInvoice(sparkInvoice)
		if err != nil {
			return sparkerrors.InvalidUserInputErrorf("failed to parse spark invoice ID in invoice %s: %w", sparkInvoice, err)
		}
		if _, exists := sparkInvoiceIDs[parsedInvoice.Id]; exists {
			return sparkerrors.FailedPreconditionErrorf("duplicate spark invoice ID found in invoice %s: %s", sparkInvoice, parsedInvoice.Id)
		}
		sparkInvoiceIDs[parsedInvoice.Id] = struct{}{}
	}
	sparkInvoiceIDsToQuery := make([]uuid.UUID, 0, len(sparkInvoiceIDs))
	for sparkInvoiceID := range sparkInvoiceIDs {
		sparkInvoiceIDsToQuery = append(sparkInvoiceIDsToQuery, sparkInvoiceID)
	}
	now := time.Now().UTC()
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return err
	}
	transactionFinalizedOrInFlight := tokentransaction.Or(
		tokentransaction.StatusIn(
			st.TokenTransactionStatusFinalized,
			st.TokenTransactionStatusRevealed,
		),
		tokentransaction.And(
			tokentransaction.StatusIn(
				st.TokenTransactionStatusStarted,
				st.TokenTransactionStatusSigned,
			),
			tokentransaction.Or(
				tokentransaction.ExpiryTimeIsNil(),
				tokentransaction.ExpiryTimeGT(now),
			),
		),
	)

	inFlightOrFinalizedTransactions, err := db.TokenTransaction.Query().
		Where(tokentransaction.And(
			transactionFinalizedOrInFlight,
			tokentransaction.HasSparkInvoiceWith(
				sparkinvoice.IDIn(sparkInvoiceIDsToQuery...),
			),
		)).
		WithSparkInvoice(func(q *ent.SparkInvoiceQuery) {
			q.Select(sparkinvoice.FieldID)
		}).
		All(ctx)
	if err != nil {
		return sparkerrors.NotFoundErrorf("failed to get token transactions: %w", err)
	}
	var inFlightOrFinalizedInvoices []uuid.UUID
	for _, transaction := range inFlightOrFinalizedTransactions {
		for _, invoice := range transaction.Edges.SparkInvoice {
			if _, exists := sparkInvoiceIDs[invoice.ID]; exists {
				inFlightOrFinalizedInvoices = append(inFlightOrFinalizedInvoices, invoice.ID)
			}
		}
	}
	if len(inFlightOrFinalizedInvoices) > 0 {
		return sparkerrors.FailedPreconditionErrorf("spark invoices %v are currently in flight or finalized and are not reassignable", inFlightOrFinalizedInvoices)
	}
	return nil
}

// If sender pubkey is present, the owner of the spent outputs must match the expected sender public key.
func validateOutputsMatchSenderAndNetwork(ctx context.Context, tokenTransaction *tokenpb.TokenTransaction, senderPublicKey keys.Public, network common.Network) error {
	var senderPublicKeyBytes []byte
	if senderPublicKey != (keys.Public{}) {
		senderPublicKeyBytes = senderPublicKey.Serialize()
	}

	var outputsToSpend []*tokenpb.TokenOutputToSpend
	if tokenTransaction.GetTransferInput() != nil {
		outputsToSpend = tokenTransaction.GetTransferInput().OutputsToSpend
	}
	schemaNetwork, err := common.SchemaNetworkFromNetwork(network)
	if err != nil {
		return err
	}
	if len(outputsToSpend) > 0 {
		voutsByPrevHash := make(map[string][]int32)
		hashBytesByKey := make(map[string][]byte)
		for _, o := range outputsToSpend {
			prevHash := o.PrevTokenTransactionHash
			prevVout := int32(o.PrevTokenTransactionVout)
			key := hex.EncodeToString(prevHash)
			hashBytesByKey[key] = prevHash
			existing := voutsByPrevHash[key]
			seen := false
			for _, v := range existing {
				if v == prevVout {
					seen = true
					break
				}
			}
			if !seen {
				voutsByPrevHash[key] = append(existing, prevVout)
			}
		}

		predicates := make([]predicate.TokenOutput, 0, len(voutsByPrevHash))
		for prevHash, vouts := range voutsByPrevHash {
			hash := hashBytesByKey[prevHash]
			condition := []predicate.TokenOutput{
				tokenoutput.HasOutputCreatedTokenTransactionWith(
					tokentransaction.FinalizedTokenTransactionHashEQ(hash),
				),
				tokenoutput.CreatedTransactionOutputVoutIn(vouts...),
				tokenoutput.NetworkEQ(schemaNetwork),
			}
			if len(senderPublicKeyBytes) > 0 {
				condition = append(condition, tokenoutput.OwnerPublicKeyEQ(senderPublicKeyBytes))
			}
			predicates = append(predicates, tokenoutput.And(condition...))
		}

		db, err := ent.GetDbFromContext(ctx)
		if err != nil {
			return err
		}
		createdOutputs, err := db.TokenOutput.
			Query().
			Where(
				tokenoutput.Or(predicates...),
			).
			All(ctx)
		if err != nil {
			return sparkerrors.NotFoundErrorf("failed to get previous token transactions: %w", err)
		}
		if len(createdOutputs) != len(outputsToSpend) {
			return sparkerrors.FailedPreconditionErrorf("owner public key mismatch for created outputs")
		}
	}
	return nil
}
