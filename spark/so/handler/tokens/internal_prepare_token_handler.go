package tokens

import (
	"bytes"
	"cmp"
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"

	"github.com/lightsparkdev/spark/so/knobs"
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
	config *so.Config
}

func NewInternalPrepareTokenHandler(config *so.Config) *InternalPrepareTokenHandler {
	return &InternalPrepareTokenHandler{
		config: config,
	}
}

func (h *InternalPrepareTokenHandler) PrepareTokenTransactionInternal(ctx context.Context, req *tokeninternalpb.PrepareTransactionRequest) (*tokeninternalpb.PrepareTransactionResponse, error) {
	ctx, span := GetTracer().Start(ctx, "InternalPrepareTokenHandler.PrepareTokenTransactionInternal", GetProtoTokenTransactionTraceAttributes(ctx, req.FinalTokenTransaction))
	defer span.End()
	logger := logging.GetLoggerFromContext(ctx)
	msg := fmt.Sprintf("Starting token transaction (expiry: %s)", req.FinalTokenTransaction.ExpiryTime)
	logger.Sugar().Infof("%s %s", msg, tokens.FormatTokenTransactionHashes(req.FinalTokenTransaction))

	isCoordinator := bytes.Equal(req.CoordinatorPublicKey, h.config.IdentityPublicKey().Serialize())
	expectedRevocationPublicKeys, err := h.validateAndReserveKeyshares(ctx, req.KeyshareIds, req.FinalTokenTransaction, isCoordinator)
	if err != nil {
		return nil, err
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
	expectedCreationEntityPublicKey, err := ent.GetEntityDkgKeyPublicKey(ctx, db)
	if err != nil {
		return nil, err
	}

	finalTokenTX := req.GetFinalTokenTransaction()
	err = validateFinalTokenTransaction(h.config, finalTokenTX, req.TokenTransactionSignatures, expectedRevocationPublicKeys, expectedCreationEntityPublicKey)
	if err != nil {
		return nil, err
	}

	if finalTokenTX.Version >= 2 && finalTokenTX.GetInvoiceAttachments() != nil {
		if err := validateSparkInvoicesForTransaction(ctx, finalTokenTX); err != nil {
			return nil, err
		}
		if err := validateInvoiceAttachmentsNotInFlightOrFinalized(ctx, finalTokenTX); err != nil {
			return nil, err
		}
	}

	if finalTokenTX.Version >= 3 {
		if err := validateClientCreatedTimestamp(finalTokenTX); err != nil {
			return nil, err
		}
	}

	txType, err := utils.InferTokenTransactionType(finalTokenTX)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to check token transaction type: %w", err))
	}

	var inputTtxos []*ent.TokenOutput

	switch txType {
	case utils.TokenTransactionTypeCreate:
		createPubKey, err := keys.ParsePublicKey(finalTokenTX.GetCreateInput().GetIssuerPublicKey())
		if err != nil {
			return nil, err
		}
		if err = validateIssuerSignature(finalTokenTX, req.TokenTransactionSignatures, createPubKey); err != nil {
			return nil, tokens.FormatErrorWithTransactionProto("failed to validate create token transaction signature", req.FinalTokenTransaction, sparkerrors.FailedPreconditionBadSignature(fmt.Errorf("failed to validate create token transaction signature: %w", err)))
		}
		if err = validateIssuerTokenNotAlreadyCreated(ctx, finalTokenTX); err != nil {
			return nil, err
		}
	case utils.TokenTransactionTypeMint:
		mintPubKey, err := keys.ParsePublicKey(finalTokenTX.GetMintInput().GetIssuerPublicKey())
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse issuer public key: %w", err))
		}
		if err := validateIssuerSignature(finalTokenTX, req.GetTokenTransactionSignatures(), mintPubKey); err != nil {
			return nil, tokens.FormatErrorWithTransactionProto("failed to validate mint token transaction signature", req.FinalTokenTransaction, sparkerrors.FailedPreconditionBadSignature(fmt.Errorf("failed to validate mint token transaction signature: %w", err)))

		}
		tokenMetadata, err := ent.GetTokenMetadataForTokenTransaction(ctx, finalTokenTX)
		if err != nil {
			return nil, err
		}

		// When disconnecting LRC20, we must have token metadata
		if h.config.Token.DisconnectLRC20Node && tokenMetadata == nil {
			return nil, tokens.FormatErrorWithTransactionProto("minting not allowed because a created token was not found", req.FinalTokenTransaction,
				sparkerrors.NotFoundMissingEntity(fmt.Errorf("no tokencreate entity found for token")))
		}

		txNet, err := common.NetworkFromProtoNetwork(req.FinalTokenTransaction.Network)
		if err != nil {
			return nil, tokens.FormatErrorWithTransactionProto("failed to get network from proto network", req.FinalTokenTransaction, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to get network from proto network: %w", err)))
		}
		if txNet != tokenMetadata.Network {
			return nil, tokens.FormatErrorWithTransactionProto(
				"network mismatch",
				req.FinalTokenTransaction,
				sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("transaction network %s does not match token network %s", txNet.String(), tokenMetadata.Network.String())),
			)
		}

		err = tokens.ValidateMintDoesNotExceedMaxSupply(ctx, finalTokenTX)
		if err != nil {
			return nil, tokens.FormatErrorWithTransactionProto("max supply error", finalTokenTX, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("max supply error: %w", err)))
		}
	case utils.TokenTransactionTypeTransfer:
		inputTtxos, err = ent.FetchAndLockTokenInputs(ctx, finalTokenTX.GetTransferInput().GetOutputsToSpend())
		if err != nil {
			return nil, tokens.FormatErrorWithTransactionProto("failed to fetch outputs to spend", req.FinalTokenTransaction, sparkerrors.NotFoundMissingEntity(fmt.Errorf("failed to fetch outputs to spend: %w", err)))
		}
		if len(inputTtxos) != len(finalTokenTX.GetTransferInput().GetOutputsToSpend()) {
			return nil, tokens.FormatErrorWithTransactionProto("failed to fetch all leaves to spend", req.FinalTokenTransaction,
				sparkerrors.NotFoundMissingEntity(fmt.Errorf("failed to fetch all leaves to spend: got %d leaves, expected %d", len(inputTtxos), len(req.FinalTokenTransaction.GetTransferInput().GetOutputsToSpend()))))
		}

		if err := validateNoActiveFreezesForOutputs(ctx, inputTtxos); err != nil {
			return nil, err
		}

		err = h.validateTransferTokenTransactionUsingPreviousTransactionDataAndFinalizeCreatedSignedOutputsIfPossible(ctx, finalTokenTX, req.GetTokenTransactionSignatures(), inputTtxos, h.config.Lrc20Configs[finalTokenTX.Network.String()].TransactionExpiryDuration)
		if err != nil {
			return nil, tokens.FormatErrorWithTransactionHashAndSpentOutputs("error validating transfer using previous output data", req.FinalTokenTransaction, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("error validating transfer using previous output data: %w", err)))
		}
		if anyTtxosHaveSpentTransactions(inputTtxos) {
			if err := preemptOrRejectTransactionsWithInputEnts(ctx, finalTokenTX, inputTtxos); err != nil {
				return nil, err
			}
		}
	default:
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("token transaction type unknown"))
	}

	// Save the token transaction, created output ents, and update the outputs to spend.
	coordinatorPubKey, err := keys.ParsePublicKey(req.CoordinatorPublicKey)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse coordinator public key: %w", err))
	}
	_, err = ent.CreateStartedTransactionEntities(ctx, finalTokenTX, req.TokenTransactionSignatures, req.KeyshareIds, inputTtxos, coordinatorPubKey)
	if err != nil {
		return nil, tokens.FormatErrorWithTransactionProto("failed to save token transaction and output ent", req.FinalTokenTransaction, err)
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
func (h *InternalPrepareTokenHandler) validateAndReserveKeyshares(ctx context.Context, keyshareIDs []string, finalTokenTransaction *tokenpb.TokenTransaction, isCoordinator bool) ([]keys.Public, error) {
	keyshareUUIDs := make([]uuid.UUID, len(keyshareIDs))
	// Ensure that the coordinator SO did not pass duplicate keyshare UUIDs for different outputs.
	seenUUIDs := make(map[uuid.UUID]bool)
	for i, id := range keyshareIDs {
		keyshareUUID, err := uuid.Parse(id)
		if err != nil {
			return nil, tokens.FormatErrorWithTransactionProto("failed to parse keyshare ID", finalTokenTransaction, sparkerrors.InvalidArgumentMalformedField(err))
		}
		if seenUUIDs[keyshareUUID] {
			return nil, tokens.FormatErrorWithTransactionProto("duplicate keyshare UUID found", finalTokenTransaction, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("duplicate keyshare UUID found: %s", keyshareUUID)))
		}
		seenUUIDs[keyshareUUID] = true
		keyshareUUIDs[i] = keyshareUUID
	}

	var keysharesMap map[uuid.UUID]*ent.SigningKeyshare
	var err error
	if !isCoordinator {
		keyshares, err := ent.MarkSigningKeysharesAsUsed(ctx, h.config, keyshareUUIDs)
		addTraceEvent(ctx, "mark_keyshares", attribute.String("keyshare_ids", strings.Join(keyshareIDs, ",")), attribute.Bool("success", err == nil), attribute.Bool("skipped", false))
		if err != nil {
			return nil, tokens.FormatErrorWithTransactionProto("failed to mark keyshares as used", finalTokenTransaction, sparkerrors.InternalKeyshareError(err))
		}
		keysharesMap = make(map[uuid.UUID]*ent.SigningKeyshare, len(keyshares))
		for _, keyshare := range keyshares {
			keysharesMap[keyshare.ID] = keyshare
		}
	} else {
		addTraceEvent(ctx, "mark_keyshares", attribute.String("keyshare_ids", strings.Join(keyshareIDs, ",")), attribute.Bool("skipped", true))
		keysharesMap, err = ent.GetSigningKeysharesMap(ctx, keyshareUUIDs)
		if err != nil {
			return nil, tokens.FormatErrorWithTransactionProto("failed to get keyshares map", finalTokenTransaction, err)
		}
	}

	expectedRevocationPublicKeys := make([]keys.Public, len(keyshareIDs))
	for i, id := range keyshareUUIDs {
		keyshare, ok := keysharesMap[id]
		if !ok {
			return nil, tokens.FormatErrorWithTransactionProto("keyshare ID not found", finalTokenTransaction, sparkerrors.NotFoundMissingEntity(fmt.Errorf("keyshare ID not found: %s", id)))
		}
		expectedRevocationPublicKeys[i] = keyshare.PublicKey
	}
	return expectedRevocationPublicKeys, nil
}

// validateOperatorSpecificOwnerSignatures validates the operator-specific owner signatures in the request against the transaction
// and verifies that the number of signatures matches the expected count based on transaction type
func validateOperatorSpecificOwnerSignatures(operatorIdentityPublicKey keys.Public, ownerSignatures []*tokenpb.SignatureWithIndex, tokenTransaction *ent.TokenTransaction, finalTokenTransactionHash []byte) error {
	if len(tokenTransaction.Edges.SpentOutput) > 0 {
		return validateTransferOwnerSignatures(operatorIdentityPublicKey, ownerSignatures, tokenTransaction, finalTokenTransactionHash)
	}
	return validateIssuerOwnerSignatures(operatorIdentityPublicKey, ownerSignatures, tokenTransaction, finalTokenTransactionHash)
}

func validateTransferOwnerSignatures(operatorIdentityPublicKey keys.Public, ownerSignatures []*tokenpb.SignatureWithIndex, tokenTransaction *ent.TokenTransaction, finalTokenTransactionHash []byte) error {
	if len(ownerSignatures) != len(tokenTransaction.Edges.SpentOutput) {
		return tokens.FormatErrorWithTransactionEnt(
			"invalid number of signatures for transfer",
			tokenTransaction, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("expected %d signatures for transfer (one per input), but got %d", len(tokenTransaction.Edges.SpentOutput), len(ownerSignatures))))
	}
	numInputs := len(tokenTransaction.Edges.SpentOutput)
	signaturesByIndex := make([]*tokenpb.SignatureWithIndex, numInputs)

	// Sort signatures according to index position
	for _, sig := range ownerSignatures {
		index := int(sig.InputIndex)
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

	// Validate each signature against the operator-specific payload hash
	payloadHash, err := utils.HashOperatorSpecificPayload(finalTokenTransactionHash, operatorIdentityPublicKey)
	if err != nil {
		return tokens.FormatErrorWithTransactionEnt("failed to hash operator-specific payload", tokenTransaction, err)
	}

	for i, sig := range signaturesByIndex {
		output := spentOutputs[i]
		if err := utils.ValidateOwnershipSignature(sig.Signature, payloadHash, output.OwnerPublicKey); err != nil {
			return tokens.FormatErrorWithTransactionEnt(tokens.ErrInvalidOwnerSignature, tokenTransaction, err)
		}
	}

	return nil
}

// validateIssuerOwnerSignatures validates V2 owner signatures for mint and create transactions
// In the coordinated flow, issuer signs an operator-specific payload (finalTxHash + operatorIdentity)
func validateIssuerOwnerSignatures(operatorIdentityPublicKey keys.Public, ownerSignatures []*tokenpb.SignatureWithIndex, tokenTransaction *ent.TokenTransaction, finalTokenTransactionHash []byte) error {
	if len(ownerSignatures) != 1 {
		return tokens.FormatErrorWithTransactionEnt(
			"invalid number of signatures",
			tokenTransaction, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("expected exactly 1 signature for mint/create, but got %d", len(ownerSignatures))))
	}

	var issuerPublicKey keys.Public
	if tokenTransaction.Edges.Mint != nil {
		issuerPublicKey = tokenTransaction.Edges.Mint.IssuerPublicKey
	} else if tokenTransaction.Edges.Create != nil {
		issuerPublicKey = tokenTransaction.Edges.Create.IssuerPublicKey
	} else {
		return tokens.FormatErrorWithTransactionEnt(
			"db consistency error",
			tokenTransaction, sparkerrors.NotFoundMissingEntity(fmt.Errorf("neither mint nor create record found in db, but expected one for this transaction")))
	}

	sig := ownerSignatures[0]

	// Compute the operator-specific payload hash
	payloadHash, err := utils.HashOperatorSpecificPayload(finalTokenTransactionHash, operatorIdentityPublicKey)
	if err != nil {
		return tokens.FormatErrorWithTransactionEnt("failed to hash operator-specific payload", tokenTransaction, err)
	}

	// Validate the issuer signature against the payload hash
	if err := utils.ValidateOwnershipSignature(sig.Signature, payloadHash, issuerPublicKey); err != nil {
		return tokens.FormatErrorWithTransactionEnt(tokens.ErrInvalidIssuerSignature, tokenTransaction, err)
	}

	return nil
}

type potentiallySpendableOutput struct {
	Output *ent.TokenOutput
	Err    error
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
	if err = utils.ValidateOwnershipSignature(signaturesWithIndex[0].Signature, partialTokenTransactionHash, issuerPublicKey); err != nil {
		return tokens.FormatErrorWithTransactionProto("invalid issuer signature", tokenTransaction, err)
	}
	return nil
}

func (h *InternalPrepareTokenHandler) validateTransferTokenTransactionUsingPreviousTransactionDataAndFinalizeCreatedSignedOutputsIfPossible(
	ctx context.Context,
	tokenTransaction *tokenpb.TokenTransaction,
	signaturesWithIndex []*tokenpb.SignatureWithIndex,
	outputToSpendEnts []*ent.TokenOutput,
	v0DefaultTransactionExpiryDuration time.Duration,
) error {
	type tokenBalance struct {
		inputSum  *big.Int
		outputSum *big.Int
		hasInputs bool
	}
	tokenBalances := make(map[string]*tokenBalance)

	// Build mappings from token_identifier and token_public_key to token_create_id
	// This allows us to correctly match proto outputs to their token regardless of
	// whether they use token_identifier or token_public_key
	tokenIdentifierToCreateID := make(map[string]string)
	tokenPublicKeyToCreateID := make(map[string]string)

	expectedTokenIdentifier := tokenTransaction.TokenOutputs[0].GetTokenIdentifier()
	useTokenIdentifier := expectedTokenIdentifier != nil

	for _, outputEnt := range outputToSpendEnts {
		tokenKey := outputEnt.TokenCreateID.String()

		// Build mapping for outputs to use
		if len(outputEnt.TokenIdentifier) > 0 {
			tokenIdentifierToCreateID[hex.EncodeToString(outputEnt.TokenIdentifier)] = tokenKey
		}
		if !outputEnt.TokenPublicKey.IsZero() {
			tokenPublicKeyToCreateID[hex.EncodeToString(outputEnt.TokenPublicKey.Serialize())] = tokenKey
		}

		if tokenBalances[tokenKey] == nil {
			tokenBalances[tokenKey] = &tokenBalance{
				inputSum:  big.NewInt(0),
				outputSum: big.NewInt(0),
				hasInputs: false,
			}
		}
		tokenBalances[tokenKey].hasInputs = true
		inputAmount := new(big.Int).SetBytes(outputEnt.TokenAmount)
		tokenBalances[tokenKey].inputSum.Add(tokenBalances[tokenKey].inputSum, inputAmount)
	}

	// Sum outputs per token type by mapping to token_create_id
	for _, output := range tokenTransaction.TokenOutputs {
		var tokenKey string
		var found bool

		if useTokenIdentifier {
			tokenKey, found = tokenIdentifierToCreateID[hex.EncodeToString(output.GetTokenIdentifier())]
			if !found {
				return sparkerrors.FailedPreconditionTokenRulesViolation(fmt.Errorf("output token identifier %x does not match any input", output.GetTokenIdentifier()))
			}
		} else {
			tokenKey, found = tokenPublicKeyToCreateID[hex.EncodeToString(output.TokenPublicKey)]
			if !found {
				return tokens.FormatErrorWithTransactionProto("token not found in inputs", tokenTransaction,
					sparkerrors.FailedPreconditionTokenRulesViolation(fmt.Errorf("output token public key %x does not match any input", output.TokenPublicKey)))
			}
		}

		if tokenBalances[tokenKey] == nil {
			tokenBalances[tokenKey] = &tokenBalance{
				inputSum:  big.NewInt(0),
				outputSum: big.NewInt(0),
				hasInputs: false,
			}
		}
		outputAmount := new(big.Int).SetBytes(output.GetTokenAmount())
		tokenBalances[tokenKey].outputSum.Add(tokenBalances[tokenKey].outputSum, outputAmount)
	}

	for tokenKey, balance := range tokenBalances {
		if !balance.hasInputs {
			return sparkerrors.FailedPreconditionTokenRulesViolation(fmt.Errorf("token %s has outputs but no corresponding inputs", tokenKey))
		}
		if balance.inputSum.Cmp(balance.outputSum) != 0 {
			return sparkerrors.FailedPreconditionTokenRulesViolation(fmt.Errorf("token %s: input amount %s does not match output amount %s",
				tokenKey, balance.inputSum.String(), balance.outputSum.String()))
		}
	}

	// TODO(DL-104): For now we allow the network to be nil to support old outputs. In the future we should require it to be set.
	for i, outputEnt := range outputToSpendEnts {
		if outputEnt.Network != "" {
			entNetwork, err := outputEnt.Network.MarshalProto()
			if err != nil {
				return sparkerrors.InternalTypeConversionError(fmt.Errorf("failed to marshal network: %w", err))
			}
			if entNetwork != tokenTransaction.Network {
				return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("output %d: %d != %d", i, entNetwork, tokenTransaction.Network))
			}
		}
	}

	// Validate that the ownership signatures match the ownership public keys in the outputs to spend.
	// Although this token transaction is final we pass in 'true' to generate the partial hash.
	partialTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, true)
	if err != nil {
		return fmt.Errorf("failed to hash token transaction: %w", err)
	}

	ownerSignaturesByIndex := make(map[uint32]*tokenpb.SignatureWithIndex)
	for _, sig := range signaturesWithIndex {
		if sig == nil {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("ownership signature cannot be nil"))
		}
		ownerSignaturesByIndex[sig.InputIndex] = sig
	}

	if len(signaturesWithIndex) != len(tokenTransaction.GetTransferInput().GetOutputsToSpend()) {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("number of signatures must match number of outputs to spend"))
	}

	potentiallySpendableOutputs := make([]potentiallySpendableOutput, 0)
	for i := range tokenTransaction.GetTransferInput().GetOutputsToSpend() {
		index := uint32(i)
		ownershipSignature, exists := ownerSignaturesByIndex[index]
		if !exists {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("missing owner signature for input index %d, indexes must be contiguous", index))
		}

		// Get the corresponding output entity (they are ordered outside of this block when they are fetched)
		outputEnt := outputToSpendEnts[i]
		if outputEnt == nil {
			return sparkerrors.NotFoundMissingEntity(fmt.Errorf("could not find output entity for output to spend at index %d", i))
		}
		if err := utils.ValidateOwnershipSignature(ownershipSignature.Signature, partialTokenTransactionHash, outputEnt.OwnerPublicKey); err != nil {
			return sparkerrors.FailedPreconditionBadSignature(fmt.Errorf("invalid ownership signature for output %d: %w", i, err))
		}
		if err := validateOutputIsSpendable(ctx, i, outputEnt, tokenTransaction, v0DefaultTransactionExpiryDuration); err != nil {
			if outputEnt.Status == st.TokenOutputStatusCreatedSigned {
				// Collect potentially spendable outputs for just in time self-healing during transfer
				potentiallySpendableOutputs = append(potentiallySpendableOutputs, potentiallySpendableOutput{
					Output: outputEnt,
					Err:    err,
				})
				continue
			}
			return err
		}
	}

	if len(potentiallySpendableOutputs) > 0 {
		if knobs.GetKnobsService(ctx).GetValue(knobs.KnobFinalizeCreatedSignedOutputsJustInTime, 0) == 0 {
			logger := logging.GetLoggerFromContext(ctx)
			errs := make([]error, len(potentiallySpendableOutputs))
			for i, output := range potentiallySpendableOutputs {
				errs[i] = output.Err
			}
			partialTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, true)
			if err != nil {
				return fmt.Errorf("failed to hash token transaction: %w", err)
			}
			finalTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, false)
			if err != nil {
				return fmt.Errorf("failed to hash token transaction: %w", err)
			}
			logger.Info(
				"Just in time finalization is disabled for transaction",
				zap.String("partial_transaction_hash", hex.EncodeToString(partialTokenTransactionHash)),
				zap.String("final_transaction_hash", hex.EncodeToString(finalTokenTransactionHash)),
				zap.Int("potentially_spendable_outputs", len(potentiallySpendableOutputs)),
				zap.Errors("errors", errs),
			)
			return sparkerrors.FailedPreconditionInvalidState(
				fmt.Errorf(
					"just in time finalization is disabled, %d potentially spendable outputs. first spendable output error: %w",
					len(potentiallySpendableOutputs),
					potentiallySpendableOutputs[0].Err,
				),
			)
		}

		for _, outputResult := range potentiallySpendableOutputs {
			output := outputResult.Output
			outputErr := outputResult.Err
			if err := tryFinalizeCreatedSignedOutput(ctx, h.config, output); err != nil {
				return fmt.Errorf("%w: failed just in time finalization of created signed output %s: %w", outputErr, output.ID, err)
			}
		}
	}

	return nil
}

func tryFinalizeCreatedSignedOutput(ctx context.Context, config *so.Config, output *ent.TokenOutput) error {
	outputCreatedTx, err := output.QueryOutputCreatedTokenTransaction().
		Where(
			tokentransaction.StatusEQ(st.TokenTransactionStatusRevealed),
			tokentransaction.HasSpentOutput(),
		).
		WithPeerSignatures().
		WithSpentOutput(func(q *ent.TokenOutputQuery) {
			q.WithOutputCreatedTokenTransaction()
			q.WithTokenPartialRevocationSecretShares()
			q.WithRevocationKeyshare()
			q.ForUpdate()
		}).
		WithCreatedOutput(func(q *ent.TokenOutputQuery) {
			q.ForUpdate()
		}).
		ForUpdate().
		Only(ctx)
	if err != nil {
		return sparkerrors.InternalDatabaseTransactionLifecycleError(fmt.Errorf("failed to get parent transaction: %w", err))
	}

	signTokenHandler := NewSignTokenHandler(config)
	err = signTokenHandler.TryFinalizeRevealedTokenTransaction(ctx, outputCreatedTx)
	if err != nil {
		return fmt.Errorf("failed to finalize revealed token transaction %s: %w", outputCreatedTx.ID, err)
	}

	return nil
}

// validateOutputIsSpendable checks if a output is eligible to be spent by verifying:
// 1. The output has an appropriate status (Created+Finalized or already marked as SpentStarted) OR was spent from an expired or pre-emptable transaction
// 2. The output hasn't been withdrawn already
func validateOutputIsSpendable(ctx context.Context, index int, output *ent.TokenOutput, tokenTransaction *tokenpb.TokenTransaction, v0DefaultTransactionExpiryDuration time.Duration) error {
	if !isSpendableOutputStatus(output.Status) {
		spentTx := output.Edges.OutputSpentTokenTransaction
		if spentTx == nil {
			return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("output %d cannot be spent: status must be %s or %s (was %s), or have been spent by an expired or pre-emptable transaction (none found)",
				index, st.TokenOutputStatusCreatedFinalized, st.TokenOutputStatusSpentStarted, output.Status))
		}

		// If the previous transaction has expired, we allow the output to be spent again.
		// ValidateNotExpired only fails when the transaction is expired, so any error
		// here means the previous transaction should automatically lose.
		err := spentTx.ValidateNotExpired()
		if err == nil {
			// Previous transaction is still active; ensure it is actually pre-emptable.
			cannotPreemptErr := preemptOrRejectTransaction(ctx, tokenTransaction, spentTx)
			canPreemptSpentTx := cannotPreemptErr == nil
			if !canPreemptSpentTx {
				return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("output %d cannot be spent: status must be %s or %s (was %s) , or have been spent by an expired or pre-emptable transaction (transaction was not expired or pre-emptable, id: %s, final_hash: %s, error: %w)",
					index, st.TokenOutputStatusCreatedFinalized, st.TokenOutputStatusSpentStarted, output.Status, spentTx.ID, hex.EncodeToString(spentTx.FinalizedTokenTransactionHash), cannotPreemptErr))
			}
		}
	}

	if output.ConfirmedWithdrawBlockHash != nil {
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("output %d cannot be spent: already withdrawn", index))
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
		return err
	}
	expectedBondSats := config.Lrc20Configs[network.String()].WithdrawBondSats
	expectedRelativeBlockLocktime := config.Lrc20Configs[network.String()].WithdrawRelativeBlockLocktime
	sparkOperatorsFromConfig := config.GetSigningOperatorList()

	validationConfig := &utils.FinalValidationConfig{
		ExpectedSparkOperators:          sparkOperatorsFromConfig,
		SupportedNetworks:               config.SupportedNetworks,
		ExpectedRevocationPublicKeys:    expectedRevocationPublicKeys,
		ExpectedBondSats:                expectedBondSats,
		ExpectedRelativeBlockLocktime:   expectedRelativeBlockLocktime,
		ExpectedCreationEntityPublicKey: expectedCreationEntityPublicKey,
	}

	err = utils.ValidateFinalTokenTransaction(tokenTransaction, signaturesWithIndex, validationConfig)
	if err != nil {
		return tokens.FormatErrorWithTransactionProto("failed to validate final token transaction structure", tokenTransaction, err)
	}

	return nil
}

func validateIssuerTokenNotAlreadyCreated(ctx context.Context, tokenTransaction *tokenpb.TokenTransaction) error {
	existingTokenCreateMetadata, err := ent.GetTokenMetadataForTokenTransaction(ctx, tokenTransaction)
	if err != nil {
		return tokens.FormatErrorWithTransactionProto("failed to search for existing token create entity", tokenTransaction, err)
	}
	if existingTokenCreateMetadata != nil {
		return tokens.NewTokenAlreadyCreatedError(tokenTransaction)
	}
	return nil
}

type (
	CreatedOutputAmountMap    map[[33]byte]map[AmountKey]int
	InvoiceAmountMap          map[[33]byte]map[AmountKey]int
	CountNilAmountInvoicesMap map[[33]byte]int
)

type AmountKey [16]byte

func toAmountKey(b []byte) (AmountKey, error) {
	if len(b) > 16 {
		return AmountKey{}, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("amount exceeds 16 bytes, got %d", len(b)))
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
		return tokens.FormatErrorWithTransactionProto("failed to get created output amount map and token identifier", tokenTransaction, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to get created output amount map and token identifier: %w", err)))
	}
	senderPublicKey, network, err := validateInvoiceFields(invoiceAttachments, tokenIdentifier, transactionExpiry)
	if err != nil {
		return tokens.FormatErrorWithTransactionProto("failed to validate invoice fields", tokenTransaction, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to validate invoice fields: %w", err)))
	}
	invoiceAmountMap, countNilAmountInvoicesMap, err := countInvoiceAmounts(invoiceAttachments)
	if err != nil {
		return tokens.FormatErrorWithTransactionProto("failed to count invoice amounts", tokenTransaction, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to count invoice amounts: %w", err)))
	}

	// For each receiver and amount: ensure created outputs >= fixed-amount invoices
	for receiver, invoiceCountByAmount := range invoiceAmountMap {
		outputCountByAmount, ok := createdOutputAmountMap[receiver]
		if !ok {
			return tokens.FormatErrorWithTransactionProto("no created outputs for receiver",
				tokenTransaction, sparkerrors.FailedPreconditionTokenRulesViolation(fmt.Errorf("no created outputs for receiver %x", receiver[:])))
		}
		for amt, invoiceCount := range invoiceCountByAmount {
			if outputCountByAmount[amt] < invoiceCount {
				return tokens.FormatErrorWithTransactionProto("created output amount mismatch for fixed amount invoices",
					tokenTransaction, sparkerrors.FailedPreconditionTokenRulesViolation(fmt.Errorf("not enough created outputs for amount %x for receiver %x", amt, receiver[:])))
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
				tokenTransaction, sparkerrors.FailedPreconditionTokenRulesViolation(fmt.Errorf("not enough created outputs to cover %d nil-amount invoices; outputs=%d for receiver %x",
					countNilAmountInvoicesMap[receiver], numOutputsWithoutMatchingInvoice, receiver[:])))
		}
	}

	err = validateOutputsMatchSenderAndNetwork(ctx, tokenTransaction, senderPublicKey, network)
	if err != nil {
		return tokens.FormatErrorWithTransactionProto("failed to validate sender public key matches spent outputs owners and network", tokenTransaction, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to validate sender public key matches spent outputs owners and network: %w", err)))
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
			return keys.Public{}, common.Unspecified, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to decode spark invoice %s: %w", invoice, err))
		}
		if decoded.SparkAddress == nil || decoded.SparkAddress.GetSparkInvoiceFields() == nil {
			return keys.Public{}, common.Unspecified, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("no invoice fields in invoice %s", invoice))
		}
		_, err = keys.ParsePublicKey(decoded.SparkAddress.GetIdentityPublicKey())
		if err != nil {
			return keys.Public{}, common.Unspecified, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid recipient public key in invoice %s: %w", invoice, err))
		}
		if decoded.SparkAddress.SparkInvoiceFields.Version != uint32(1) {
			return keys.Public{}, common.Unspecified, sparkerrors.InvalidArgumentInvalidVersion(fmt.Errorf("version mismatch in invoice %s", invoice))
		}
		if _, ok := decoded.SparkAddress.SparkInvoiceFields.PaymentType.(*sparkpb.SparkInvoiceFields_TokensPayment); !ok {
			return keys.Public{}, common.Unspecified, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("not a tokens payment in invoice %s", invoice))
		}
		payment := decoded.SparkAddress.SparkInvoiceFields.PaymentType.(*sparkpb.SparkInvoiceFields_TokensPayment).TokensPayment
		// all invoices pay the outputs identifier
		if !bytes.Equal(tokenIdentifier, payment.TokenIdentifier) {
			return keys.Public{}, common.Unspecified, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("token identifier mismatch in invoice %s", invoice))
		}
		if expiry := decoded.SparkAddress.SparkInvoiceFields.GetExpiryTime(); expiry != nil {
			if err := expiry.CheckValid(); err != nil {
				return keys.Public{}, common.Unspecified, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid expiry time in invoice %s: %w", invoice, err))
			}
			if expiry.AsTime().UTC().Before(now) {
				return keys.Public{}, common.Unspecified, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("expired in invoice %s", invoice))
			}
			if !transactionExpiry.IsZero() && expiry.AsTime().UTC().Before(transactionExpiry) {
				return keys.Public{}, common.Unspecified, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invoice expiration must be >= transaction expiration in invoice %s", invoice))
			}
		}
		// if a sender public key is present, it must be the same across all invoices with a sender public key encoded
		if decoded.SparkAddress.SparkInvoiceFields.SenderPublicKey != nil {
			decodedSenderPublicKey, err := keys.ParsePublicKey(decoded.SparkAddress.SparkInvoiceFields.SenderPublicKey)
			if err != nil {
				return keys.Public{}, common.Unspecified, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid sender public key in invoice %s: %w", invoice, err))
			}
			if senderPublicKey == (keys.Public{}) {
				senderPublicKey = decodedSenderPublicKey
			} else if !decodedSenderPublicKey.Equals(senderPublicKey) {
				return keys.Public{}, common.Unspecified, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("sender public key mismatch in invoice %s: expected %x, got %x", invoice, senderPublicKey.Serialize(), decodedSenderPublicKey.Serialize()))
			}
		}
		if network == common.Unspecified {
			network = decoded.Network
		} else if network != decoded.Network {
			return keys.Public{}, common.Unspecified, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("network mismatch in invoice %s: expected %s, got %s", invoice, network, decoded.Network))
		}
		if decoded.SparkAddress.Signature != nil {
			err := common.VerifySparkAddressSignature(decoded.SparkAddress, decoded.Network)
			if err != nil {
				return keys.Public{}, common.Unspecified, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid signature in invoice %s: %w", invoice, err))
			}
		}
	}
	if network == common.Unspecified {
		return keys.Public{}, common.Unspecified, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid network encoded in invoices"))
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
			return nil, nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to decode spark invoice %s: %w", invoice, err))
		}
		recipientPubkey, err := keys.ParsePublicKey(decoded.SparkAddress.GetIdentityPublicKey())
		if err != nil {
			return nil, nil, sparkerrors.InvalidArgumentPublicKeyMismatch(fmt.Errorf("invalid recipient public key in invoice %s: %w", invoice, err))
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
				return nil, nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid amount in invoice %s: %w", invoice, err))
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
			return nil, nil, sparkerrors.InvalidArgumentPublicKeyMismatch(fmt.Errorf("invalid owner public key: %w", err))
		}
		if len(tokenIdentifier) == 0 {
			tokenIdentifier = output.GetTokenIdentifier()
		} else if !bytes.Equal(tokenIdentifier, output.GetTokenIdentifier()) {
			return nil, nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("token identifier mismatch for owner %s", ownerPubkey))
		}
		amount, err := toAmountKey(output.GetTokenAmount())
		if err != nil {
			return nil, nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid amount: %w", err))
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

func validateClientCreatedTimestamp(tokenTransaction *tokenpb.TokenTransaction) error {
	if tokenTransaction.GetClientCreatedTimestamp() == nil {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("client created timestamp cannot be nil"))
	}
	now := time.Now().UTC()
	clientTimestamp := tokenTransaction.GetClientCreatedTimestamp().AsTime().UTC()
	// The client created timestamp must be within the validity duration seconds otherwise this transaction
	// is expired.
	oldestAllowed := now.Add(-time.Duration(tokenTransaction.GetValidityDurationSeconds()) * time.Second)
	// The client created timestamp must be within 1 minute of the current time otherwise this transaction
	// is too far in the future. The clients clock is either not synced or the client is intending to
	// construct a transaction with a longer than allowed validity duration.
	latestAllowed := now.Add(1 * time.Minute)
	if clientTimestamp.Before(oldestAllowed) {
		return sparkerrors.InvalidArgumentOutOfRange(fmt.Errorf("client created timestamp too old: %s, oldest allowed: %s", clientTimestamp.Format(time.RFC3339), oldestAllowed.Format(time.RFC3339)))
	}
	if clientTimestamp.After(latestAllowed) {
		return sparkerrors.InvalidArgumentOutOfRange(fmt.Errorf("client created timestamp too far in the future: %s, latest allowed: %s", clientTimestamp.Format(time.RFC3339), latestAllowed.Format(time.RFC3339)))
	}
	return nil
}

func validateInvoiceAttachmentsNotInFlightOrFinalized(ctx context.Context, tokenTransaction *tokenpb.TokenTransaction) error {
	invoiceAttachments := tokenTransaction.GetInvoiceAttachments()
	sparkInvoiceIDs := make(map[uuid.UUID]struct{})

	for _, invoiceAttachment := range invoiceAttachments {
		sparkInvoice := invoiceAttachment.GetSparkInvoice()
		parsedInvoice, err := common.ParseSparkInvoice(sparkInvoice)
		if err != nil {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to parse spark invoice ID in invoice %s: %w", sparkInvoice, err))
		}
		if _, exists := sparkInvoiceIDs[parsedInvoice.Id]; exists {
			return sparkerrors.InvalidArgumentDuplicateField(fmt.Errorf("duplicate spark invoice ID found in invoice %s: %s", sparkInvoice, parsedInvoice.Id))
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
		Where(
			transactionFinalizedOrInFlight,
			tokentransaction.HasSparkInvoiceWith(
				sparkinvoice.IDIn(sparkInvoiceIDsToQuery...),
			),
		).
		WithSparkInvoice(func(q *ent.SparkInvoiceQuery) {
			q.Select(sparkinvoice.FieldID)
		}).
		All(ctx)
	if err != nil {
		return sparkerrors.NotFoundMissingEntity(fmt.Errorf("failed to get token transactions: %w", err))
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
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("spark invoices %v are currently in flight or finalized and are not reassignable", inFlightOrFinalizedInvoices))
	}
	return nil
}

// If sender pubkey is present, the owner of the spent outputs must match the expected sender public key.
func validateOutputsMatchSenderAndNetwork(ctx context.Context, tokenTransaction *tokenpb.TokenTransaction, senderPublicKey keys.Public, network common.Network) error {
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
			if !senderPublicKey.IsZero() {
				condition = append(condition, tokenoutput.OwnerPublicKeyEQ(senderPublicKey))
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
			return sparkerrors.NotFoundMissingEntity(fmt.Errorf("failed to get previous token transactions: %w", err))
		}
		if len(createdOutputs) != len(outputsToSpend) {
			return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("owner public key mismatch for created outputs"))
		}
	}
	return nil
}

// addTraceEvent adds a trace event if a span is available
func addTraceEvent(ctx context.Context, eventName string, attributes ...attribute.KeyValue) {
	span := trace.SpanFromContext(ctx)
	if span != nil {
		span.AddEvent(eventName, trace.WithAttributes(
			attributes...,
		))
	}
}
